package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// streamLineLimit bounds one SSE line from upstream. A provider that emits a
// huge single line is a protocol violation, not a reply.
const streamLineLimit = 1 << 20

// FunctionDelta is one fragment of a streamed tool call. Name usually arrives
// once; Arguments arrives in pieces and must be concatenated by the client.
type FunctionDelta struct {
	Name      *string `json:"name,omitempty"`
	Arguments *string `json:"arguments,omitempty"`
}

// ToolCallDelta keeps the protocol fields a client needs to assemble a call:
// index identifies the slot, id/type arrive on the first fragment.
type ToolCallDelta struct {
	Index    *int           `json:"index,omitempty"`
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type,omitempty"`
	Function *FunctionDelta `json:"function,omitempty"`
}

// StreamEvent is one decoded upstream delta. Text, Reasoning and ToolCalls are
// independent: a chunk may carry any combination, or only usage.
type StreamEvent struct {
	ID             string
	Model          string
	Text           string
	Reasoning      string
	ToolCalls      []ToolCallDelta
	Usage          json.RawMessage
	Finish         string
	PlannerContext *PlannerContext
	// Role is present on the first delta of an OpenAI-compatible stream.
	Role string
}

// StreamChat is a convenience wrapper that emits every event.
func (c *Client) StreamChat(ctx context.Context, p Config, model string, body map[string]any, emit func(StreamEvent) error) error {
	return c.OpenStream(ctx, p, model, body, emit, nil)
}

// OpenStream streams one completion and hands every decoded delta to emit.
// ready runs once the upstream is known to be a valid event stream, so a caller
// can still choose the HTTP status.
//
// Success requires the upstream to end the turn explicitly: at least one
// non-empty finish_reason and a [DONE] terminator. A stream that just stops is
// an error, never a complete answer.
func (c *Client) OpenStream(ctx context.Context, p Config, model string, body map[string]any, emit func(StreamEvent) error, ready func()) error {
	if p.Protocol != "openai" {
		return &APIError{status: http.StatusNotImplemented, Code: "STREAM_PROTOCOL", Message: "网关仅支持 OpenAI 兼容协议的流式透传"}
	}
	budget := c.ChatBudget
	if budget <= 0 {
		budget = DefaultChatBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	body["stream"] = true
	body["stream_options"] = mergeStreamOptions(body["stream_options"])
	req, err := c.newRequest(ctx, p, "POST", "/chat/completions", body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	res, err := c.do(ctx, c.HTTP, req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	contentType := strings.ToLower(res.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "text/event-stream") {
		return &APIError{status: http.StatusBadGateway, Code: "UPSTREAM_NOT_STREAMING", Message: "上游未返回事件流；未做非流式降级"}
	}
	if ready != nil {
		ready()
	}
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), streamLineLimit)
	var data strings.Builder
	sawEvent := false
	sawFinish := false
	sawDone := false
	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		payload := data.String()
		data.Reset()
		if payload == "[DONE]" {
			sawDone = true
			return nil
		}
		var chunk struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Role             string          `json:"role"`
					Content          string          `json:"content"`
					ReasoningContent string          `json:"reasoning_content"`
					Reasoning        string          `json:"reasoning"`
					ToolCalls        []ToolCallDelta `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage json.RawMessage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return &APIError{status: http.StatusBadGateway, Code: "UPSTREAM_STREAM_INVALID", Message: "上游事件流包含无法解析的数据"}
		}
		event := StreamEvent{ID: chunk.ID, Model: chunk.Model, Usage: chunk.Usage}
		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta
			event.Role = delta.Role
			event.Text = delta.Content
			event.Reasoning = delta.ReasoningContent
			if event.Reasoning == "" {
				event.Reasoning = delta.Reasoning
			}
			event.ToolCalls = delta.ToolCalls
			if chunk.Choices[0].FinishReason != nil {
				event.Finish = *chunk.Choices[0].FinishReason
				if event.Finish != "" {
					sawFinish = true
				}
			}
		}
		if event.Text == "" && event.Reasoning == "" && event.Finish == "" && len(event.Usage) == 0 && event.Role == "" && len(event.ToolCalls) == 0 {
			return nil
		}
		sawEvent = true
		return emit(event)
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
			// Comment / keep-alive.
		case strings.HasPrefix(line, "data:"):
			value := strings.TrimPrefix(line, "data:")
			value = strings.TrimPrefix(value, " ")
			data.WriteString(value)
		case strings.HasPrefix(line, "event:"), strings.HasPrefix(line, "id:"), strings.HasPrefix(line, "retry:"):
			// Named events carry no payload we need.
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, bufio.ErrTooLong) {
			return &APIError{status: http.StatusBadGateway, Code: "UPSTREAM_STREAM_LINE", Message: "上游事件流单行超过 1 MiB"}
		}
		return fmt.Errorf("upstream stream truncated: %w", err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !sawDone {
		return &APIError{status: http.StatusBadGateway, Code: "UPSTREAM_STREAM_INCOMPLETE", Message: "上游事件流未以 [DONE] 结束，结果未知"}
	}
	if !sawFinish {
		return &APIError{status: http.StatusBadGateway, Code: "UPSTREAM_STREAM_INCOMPLETE", Message: "上游事件流没有 finish_reason，结果未知"}
	}
	if !sawEvent {
		return &APIError{status: http.StatusBadGateway, Code: "UPSTREAM_STREAM_EMPTY", Message: "上游事件流没有任何内容"}
	}
	return nil
}

// mergeStreamOptions keeps whatever the client asked for and forces usage
// reporting. include_usage cannot be turned off: the ledger needs the counts,
// and a caller must not be able to silently disable accounting.
func mergeStreamOptions(existing any) map[string]any {
	merged := map[string]any{}
	switch value := existing.(type) {
	case map[string]any:
		for key, item := range value {
			merged[key] = item
		}
	case json.RawMessage:
		var decoded map[string]any
		if json.Unmarshal(value, &decoded) == nil {
			for key, item := range decoded {
				merged[key] = item
			}
		}
	}
	merged["include_usage"] = true
	return merged
}
