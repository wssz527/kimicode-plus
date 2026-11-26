package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

const plannerDefaultOutput = 8192

type PlannerModelOptions struct {
	Thinking         string
	AdaptiveThinking bool
	DefaultEffort    string
	SupportEfforts   []string
	OffEffort        string
	MaxOutput        int64
}

// PlannerContext holds signed reasoning and tool blocks needed to continue a native
// tool turn. Only the provider and model that produced them can receive them back.
type PlannerContext struct {
	Provider string            `json:"provider"`
	Protocol string            `json:"protocol"`
	Model    string            `json:"model"`
	Blocks   []json.RawMessage `json:"blocks"`
}

func (o PlannerModelOptions) effort() string {
	valid := func(e string) bool { return e != "" && e != "none" && e != "no" && e != o.OffEffort }
	if valid(o.DefaultEffort) && (len(o.SupportEfforts) == 0 || slices.Contains(o.SupportEfforts, o.DefaultEffort)) {
		return o.DefaultEffort
	}
	for _, effort := range []string{"high", "medium", "low", "xhigh", "max", "minimal"} {
		if valid(effort) && slices.Contains(o.SupportEfforts, effort) {
			return effort
		}
	}
	return ""
}

func (o PlannerModelOptions) output() int64 {
	if o.MaxOutput > 0 {
		return o.MaxOutput
	}
	return plannerDefaultOutput
}

func (o PlannerModelOptions) thinking() bool {
	return o.Thinking != "none" && (o.Thinking != "" || o.AdaptiveThinking || o.effort() != "")
}

func (c *Client) StreamPlanner(ctx context.Context, cfg Config, model string, options PlannerModelOptions, messages []Message, tools []map[string]any, emit func(StreamEvent) error) error {
	if cfg.Protocol == "openai" {
		return c.OpenStream(ctx, cfg, model, plannerOpenAIRequest(cfg, model, options, messages, tools), emit, nil)
	}

	suffix, body, err := plannerNativeRequest(cfg, model, options, messages, tools)
	if err != nil {
		return err
	}
	budget := c.ChatBudget
	if budget <= 0 {
		budget = DefaultChatBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	raw, err := c.request(ctx, c.HTTP, cfg, "POST", suffix, body)
	if err != nil {
		return err
	}
	event, err := plannerNativeEvent(cfg, model, raw)
	if err != nil {
		return err
	}
	return emit(event)
}

// kimiThinkingAPIHost reports whether the endpoint is a Kimi/Moonshot API host
// whose Chat Completions surface documents the private `thinking` parameter:
// the Kimi Code managed endpoint and the Moonshot open platform. The check is
// endpoint identity, not a model ID list, so a reasoning model on any other
// supplier is never handed a vendor-private field.
func kimiThinkingAPIHost(cfg Config) bool {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return false
	}
	switch base.Hostname() {
	case "api.kimi.com", "api.moonshot.cn", "api.moonshot.ai":
		return true
	}
	return false
}

// plannerOpenAIRequest builds the Chat Completions body from config-verified
// capabilities only.
//
//   - max_tokens is sent when the model config carries an output limit (the
//     official model readback or the registry `output` capability): that value
//     was stored for this model, so the field is chosen deliberately. Strict
//     OpenAI/Azure deployments that reject max_tokens for reasoning models are
//     never sent an unverified limit — the request simply omits it and the
//     supplier default applies. On Kimi/Moonshot endpoints the parameter and
//     its 8192 default are verified behavior and stay.
//   - `thinking` is Kimi/Moonshot-private and never leaves those endpoints;
//     other reasoning suppliers are served by reasoning_effort.
//   - reasoning_effort is sent only for an effort the model config declares
//     (defaultEffort within supportEfforts, or a declared single effort).
func plannerOpenAIRequest(cfg Config, model string, options PlannerModelOptions, messages []Message, tools []map[string]any) map[string]any {
	kimi := kimiThinkingAPIHost(cfg)
	body := map[string]any{"model": model, "messages": messages}
	limit := options.MaxOutput
	if limit <= 0 && kimi {
		limit = plannerDefaultOutput
	}
	if limit > 0 {
		body["max_tokens"] = limit
	}
	if kimi && (options.Thinking == "toggle" || options.AdaptiveThinking && options.Thinking != "none") {
		body["thinking"] = map[string]string{"type": "enabled"}
	}
	if effort := options.effort(); effort != "" && options.Thinking != "none" {
		body["reasoning_effort"] = effort
	}
	if len(tools) != 0 {
		body["tools"] = tools
		body["tool_choice"] = "auto"
	}
	return body
}

// plannerNativeRequest keeps the native limits: anthropic requires max_tokens,
// and the Responses and Gemini fields are accepted for every model on their
// protocols, so the 8192 default is verified behavior there rather than an
// invented Chat Completions parameter.
func plannerNativeRequest(cfg Config, model string, options PlannerModelOptions, messages []Message, tools []map[string]any) (string, map[string]any, error) {
	var system []string
	for _, message := range messages {
		if message.Role == "system" {
			system = append(system, message.Content)
		}
	}
	instructions := strings.Join(system, "\n\n")
	functions := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		if function, ok := tool["function"].(map[string]any); ok {
			functions = append(functions, function)
		}
	}
	body := map[string]any{}
	if cfg.Protocol == "responses" {
		body["model"], body["input"], body["store"], body["max_output_tokens"] = model, plannerResponsesInput(cfg, model, messages), false, options.output()
		if instructions != "" {
			body["instructions"] = instructions
		}
		if options.thinking() {
			effort := options.effort()
			if effort == "" {
				effort = "medium"
			}
			body["reasoning"] = map[string]string{"effort": effort, "summary": "auto"}
			body["include"] = []string{"reasoning.encrypted_content"}
		}
		if len(functions) > 0 {
			converted := make([]map[string]any, 0, len(functions))
			for _, f := range functions {
				converted = append(converted, map[string]any{"type": "function", "name": f["name"], "description": f["description"], "parameters": f["parameters"], "strict": false})
			}
			body["tools"] = converted
		}
		return "/responses", body, nil
	}

	input := plannerNativeInput(cfg, model, messages)
	switch cfg.Protocol {
	case "anthropic":
		suffix := "/messages"
		if !strings.HasSuffix(strings.TrimRight(cfg.BaseURL, "/"), "/v1") {
			suffix = "/v1" + suffix
		}
		body["model"], body["messages"], body["max_tokens"] = model, input, options.output()
		if instructions != "" {
			body["system"] = instructions
		}
		if options.thinking() {
			if options.AdaptiveThinking {
				body["thinking"] = map[string]string{"type": "adaptive"}
				switch effort := options.effort(); effort {
				case "low", "medium", "high", "xhigh", "max":
					body["output_config"] = map[string]string{"effort": effort}
				}
			} else if options.output() > 1024 {
				body["thinking"] = map[string]any{"type": "enabled", "budget_tokens": min(options.output()-1, plannerThinkingBudget(options.effort()))}
			}
		}
		if len(functions) > 0 {
			converted := make([]map[string]any, 0, len(functions))
			for _, f := range functions {
				converted = append(converted, map[string]any{"name": f["name"], "description": f["description"], "input_schema": f["parameters"]})
			}
			body["tools"] = converted
		}
		return suffix, body, nil
	case "gemini":
		if strings.ContainsAny(model, "/?#") {
			return "", nil, errors.New("invalid Gemini model ID")
		}
		body["contents"] = input
		if instructions != "" {
			body["systemInstruction"] = map[string]any{"parts": []map[string]string{{"text": instructions}}}
		}
		generation := map[string]any{"maxOutputTokens": options.output()}
		if options.thinking() {
			thinking := map[string]any{"includeThoughts": true}
			if strings.HasPrefix(model, "gemini-3") {
				switch effort := options.effort(); effort {
				case "low", "high":
					thinking["thinkingLevel"] = strings.ToUpper(effort)
				case "minimal", "medium":
					if strings.Contains(model, "flash") {
						thinking["thinkingLevel"] = strings.ToUpper(effort)
					}
				case "xhigh", "max":
					thinking["thinkingLevel"] = "HIGH"
				}
			} else if effort := options.effort(); effort != "" {
				thinking["thinkingBudget"] = plannerThinkingBudget(effort)
			}
			generation["thinkingConfig"] = thinking
		}
		body["generationConfig"] = generation
		if len(functions) > 0 {
			declarations := make([]map[string]any, 0, len(functions))
			for _, f := range functions {
				declarations = append(declarations, map[string]any{"name": f["name"], "description": f["description"], "parametersJsonSchema": f["parameters"]})
			}
			body["tools"] = []map[string]any{{"functionDeclarations": declarations}}
		}
		return "/models/" + url.PathEscape(model) + ":generateContent", body, nil
	default:
		return "", nil, errors.New("unsupported provider protocol")
	}
}

func plannerThinkingBudget(effort string) int64 {
	switch effort {
	case "minimal", "low":
		return 1024
	case "high", "xhigh", "max":
		return 8192
	default:
		return 4096
	}
}

func plannerNativeBlocks(cfg Config, model string, message Message) []any {
	var blocks []any
	if saved := message.PlannerContext; saved != nil && saved.Provider == cfg.ID && saved.Protocol == cfg.Protocol && saved.Model == model {
		for _, block := range saved.Blocks {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func plannerResponsesInput(cfg Config, model string, messages []Message) []any {
	items := []any{}
	for _, message := range messages {
		switch message.Role {
		case "user", "assistant":
			if blocks := plannerNativeBlocks(cfg, model, message); len(blocks) > 0 {
				items = append(items, blocks...)
				continue
			}
			if message.Content != "" {
				items = append(items, map[string]any{"role": message.Role, "content": message.Content})
			}
			for _, call := range message.ToolCalls {
				items = append(items, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
			}
		case "tool":
			items = append(items, map[string]any{"type": "function_call_output", "call_id": message.ToolCallID, "output": message.Content})
		}
	}
	return items
}

func plannerNativeInput(cfg Config, model string, messages []Message) []map[string]any {
	gemini := cfg.Protocol == "gemini"
	partsKey := "content"
	if gemini {
		partsKey = "parts"
	}
	input := []map[string]any{}
	appendParts := func(role string, parts []any) {
		if len(parts) == 0 {
			return
		}
		if last := len(input) - 1; last >= 0 && input[last]["role"] == role {
			input[last][partsKey] = append(input[last][partsKey].([]any), parts...)
			return
		}
		input = append(input, map[string]any{"role": role, partsKey: parts})
	}
	calls := map[string]ToolCall{}
	geminiIDs := map[string]bool{}
	for _, message := range messages {
		switch message.Role {
		case "user", "assistant":
			role := message.Role
			if gemini && role == "assistant" {
				role = "model"
			}
			blocks := plannerNativeBlocks(cfg, model, message)
			for _, call := range message.ToolCalls {
				calls[call.ID] = call
				if gemini {
					geminiIDs[call.ID] = len(blocks) > 0 && !strings.HasPrefix(call.ID, "planner-gemini-")
				}
			}
			if len(blocks) > 0 {
				appendParts(role, blocks)
				continue
			}
			if message.Content != "" {
				part := map[string]any{"text": message.Content}
				if !gemini {
					part["type"] = "text"
				}
				blocks = append(blocks, part)
			}
			for _, call := range message.ToolCalls {
				var args map[string]any
				if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args == nil {
					args = map[string]any{}
				}
				if gemini {
					blocks = append(blocks, map[string]any{"functionCall": map[string]any{"name": call.Function.Name, "args": args}})
				} else {
					blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": args})
				}
			}
			appendParts(role, blocks)
		case "tool":
			if gemini {
				call := calls[message.ToolCallID]
				result := map[string]any{"name": call.Function.Name, "response": map[string]string{"output": message.Content}}
				if geminiIDs[message.ToolCallID] {
					result["id"] = message.ToolCallID
				}
				appendParts("user", []any{map[string]any{"functionResponse": result}})
			} else {
				appendParts("user", []any{map[string]any{"type": "tool_result", "tool_use_id": message.ToolCallID, "content": message.Content}})
			}
		}
	}
	return input
}

func plannerNativeEvent(cfg Config, model string, raw []byte) (StreamEvent, error) {
	var reply struct {
		Content    []json.RawMessage       `json:"content"`
		Output     []json.RawMessage       `json:"output"`
		Status     string                  `json:"status"`
		StopReason string                  `json:"stop_reason"`
		Incomplete struct{ Reason string } `json:"incomplete_details"`
		Candidates []struct {
			Content      struct{ Parts []json.RawMessage }
			FinishReason string
		}
		Usage         json.RawMessage `json:"usage"`
		UsageMetadata json.RawMessage `json:"usageMetadata"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return StreamEvent{}, errors.New("invalid planner completion")
	}
	event := StreamEvent{Model: model, Role: "assistant", Finish: "stop", Usage: reply.Usage}
	blocks := reply.Content
	switch cfg.Protocol {
	case "anthropic":
		if reply.StopReason == "max_tokens" {
			event.Finish = "length"
		}
	case "gemini":
		if len(reply.Candidates) == 0 {
			return StreamEvent{}, errors.New("Gemini returned no planner candidates")
		}
		blocks = reply.Candidates[0].Content.Parts
		event.Usage = reply.UsageMetadata
		if reply.Candidates[0].FinishReason == "MAX_TOKENS" {
			event.Finish = "length"
		}
	case "responses":
		if reply.Status == "failed" || reply.Status == "incomplete" && reply.Incomplete.Reason != "max_output_tokens" {
			return StreamEvent{}, errors.New("Responses planner completion did not finish")
		}
		blocks = reply.Output
		if reply.Incomplete.Reason == "max_output_tokens" {
			event.Finish = "length"
		}
	}
	for i, rawBlock := range blocks {
		var block struct {
			Type, Text, Thinking, ID, Name, Arguments string
			CallID                                    string `json:"call_id"`
			Input                                     json.RawMessage
			Thought                                   bool
			FunctionCall                              *struct {
				Name, ID string
				Args     json.RawMessage
			}
			Content, Summary []struct{ Text string }
		}
		if err := json.Unmarshal(rawBlock, &block); err != nil {
			return StreamEvent{}, errors.New("invalid planner completion block")
		}
		switch {
		case block.Type == "thinking":
			event.Reasoning += block.Thinking
		case block.Type == "reasoning":
			for _, part := range block.Summary {
				event.Reasoning += part.Text
			}
		case block.Thought:
			event.Reasoning += block.Text
		case block.FunctionCall != nil:
			call := block.FunctionCall
			id := call.ID
			if id == "" {
				id = fmt.Sprintf("planner-gemini-%d", i)
			}
			event.ToolCalls = append(event.ToolCalls, plannerCallDelta(i, id, call.Name, string(call.Args)))
		case block.Type == "tool_use":
			event.ToolCalls = append(event.ToolCalls, plannerCallDelta(i, block.ID, block.Name, string(block.Input)))
		case block.Type == "function_call":
			event.ToolCalls = append(event.ToolCalls, plannerCallDelta(i, block.CallID, block.Name, block.Arguments))
		case block.Type == "message":
			for _, part := range block.Content {
				event.Text += part.Text
			}
		default:
			event.Text += block.Text
		}
	}
	if len(event.ToolCalls) > 0 && event.Finish != "length" {
		event.Finish = "tool_calls"
		event.PlannerContext = &PlannerContext{Provider: cfg.ID, Protocol: cfg.Protocol, Model: model, Blocks: blocks}
	}
	return event, nil
}

func plannerCallDelta(index int, id, name, args string) ToolCallDelta {
	return ToolCallDelta{Index: &index, ID: id, Type: "function", Function: &FunctionDelta{Name: &name, Arguments: &args}}
}
