package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func chatClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client := catalogClient(t)
	if _, err := client.DB.Exec(`UPDATE registry_providers SET base_url=? WHERE id='synthetic'`, baseURL); err != nil {
		t.Fatal(err)
	}
	return client
}

// The defect this guards against: the shared http.Client carried the 15-second
// catalog timeout, so a reply that took longer than the catalog page window
// failed even though the provider was still producing tokens. The slow reply
// here really is slower than PageBudget.
func TestChatOutlivesCatalogWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("a reply longer than the catalog page window takes real time")
	}
	var once sync.Once
	release := make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"slow reply"}}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`)
	}))
	defer remote.Close()
	defer once.Do(func() { close(release) })
	client := chatClient(t, remote.URL)
	client.ChatBudget = PageBudget + 30*time.Second
	time.AfterFunc(PageBudget+time.Second, func() { once.Do(func() { close(release) }) })
	start := time.Now()
	reply, err := client.Chat(context.Background(), "synthetic", "any", []Message{{Role: "user", Content: "hi"}})
	if err != nil || reply.Text != "slow reply" {
		t.Fatalf("chat was cut short after %s: %q %v", time.Since(start), reply.Text, err)
	}
	if elapsed := time.Since(start); elapsed <= PageBudget {
		t.Fatalf("reply returned in %s, so it never exceeded the catalog window", elapsed)
	}
}

// The default client must not carry an overall timeout at all: any value here
// becomes a cap on generation, which the chat budget cannot override.
func TestDefaultClientHasNoGlobalTimeout(t *testing.T) {
	client := catalogClient(t)
	if client.HTTP.Timeout != 0 {
		t.Fatalf("shared client carries a %s cap that bounds chat", client.HTTP.Timeout)
	}
	if client.ChatBudget <= CatalogBudget {
		t.Fatalf("chat budget %s does not exceed the catalog budget %s", client.ChatBudget, CatalogBudget)
	}
}

// Same mechanism, scaled down: an overall client timeout still wins over the
// chat budget, so this asserts the test above is not vacuous.
func TestGlobalClientTimeoutWouldCutChat(t *testing.T) {
	release := make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"late"}}]}`)
	}))
	defer remote.Close()
	defer close(release)
	client := chatClient(t, remote.URL)
	client.HTTP.Timeout = 150 * time.Millisecond
	_, err := client.Chat(context.Background(), "synthetic", "any", []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("global client timeout no longer caps chat; the regression test proves nothing")
	}
}

func TestChatBudgetIsBoundedAndReported(t *testing.T) {
	release := make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer remote.Close()
	defer close(release)
	client := chatClient(t, remote.URL)
	client.ChatBudget = 150 * time.Millisecond
	start := time.Now()
	_, err := client.Chat(context.Background(), "synthetic", "any", []Message{{Role: "user", Content: "hi"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("chat budget not applied: %s", elapsed)
	}
}

func TestChatBudgetIsNotTheCatalogDeadline(t *testing.T) {
	if DefaultChatBudget <= CatalogBudget {
		t.Fatalf("chat budget %s must exceed the catalog budget %s", DefaultChatBudget, CatalogBudget)
	}
	client := catalogClient(t)
	client.ChatBudget = 99 * time.Second
	var seen time.Time
	client.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Fatal("chat has no deadline")
		}
		seen = deadline
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"ok"}}]}`))}, nil
	})
	start := time.Now()
	if _, err := client.Chat(context.Background(), "synthetic", "m", []Message{{Role: "user", Content: "x"}}); err != nil {
		t.Fatal(err)
	}
	if remaining := seen.Sub(start); remaining < 90*time.Second {
		t.Fatalf("chat used %s instead of its own budget", remaining)
	}
}

// A hung catalog page must fail after its own budget instead of waiting for
// the whole 45-second window to close.
func TestCatalogKeepsItsPerPageBudget(t *testing.T) {
	release := make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer remote.Close()
	defer close(release)
	client := chatClient(t, remote.URL)
	client.PageBudget = 150 * time.Millisecond
	start := time.Now()
	if _, err := client.Models(context.Background(), "synthetic"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hung page not bounded: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("page budget not applied: %s", elapsed)
	}
}

func TestUpstreamStatusReachesCallersAsStatusError(t *testing.T) {
	var respond int
	client := catalogClient(t)
	client.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: respond, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"quota"}}`))}, nil
	})
	respond = 429
	_, err := client.Chat(context.Background(), "synthetic", "m", []Message{{Role: "user", Content: "x"}})
	var status *StatusError
	if !errors.As(err, &status) || status.Status != 429 {
		t.Fatalf("429 not surfaced: %v", err)
	}
	if strings.Contains(err.Error(), "quota") {
		t.Fatalf("upstream body leaked into the error: %v", err)
	}
	respond = 200
	if _, err := client.Chat(context.Background(), "synthetic", "m", []Message{{Role: "user", Content: "x"}}); err == nil {
		t.Fatal("empty 200 accepted as a reply")
	}
}

func TestAccountSecretOverridesProviderSecret(t *testing.T) {
	var seen string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer remote.Close()
	client := chatClient(t, remote.URL)
	config, err := client.Config(context.Background(), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	account := "provider/synthetic/account-a"
	if _, err := client.ChatWith(context.Background(), config.WithSecret(&account), "m", []Message{{Role: "user", Content: "x"}}); err == nil {
		t.Fatal("missing credential reference accepted")
	}
	if seen != "" {
		t.Fatalf("request left without a resolvable credential: %q", seen)
	}
	client.Secrets = staticStore{account: "synthetic-account-key"}
	if _, err := client.ChatWith(context.Background(), config.WithSecret(&account), "m", []Message{{Role: "user", Content: "x"}}); err != nil {
		t.Fatal(err)
	}
	if seen != "Bearer synthetic-account-key" {
		t.Fatalf("account credential not used: %q", seen)
	}
	empty := ""
	if _, err := client.ChatWith(context.Background(), config.WithSecret(&empty), "m", []Message{{Role: "user", Content: "x"}}); err != nil {
		t.Fatal(err)
	}
	if seen != "" {
		t.Fatalf("empty override still sent a credential: %q", seen)
	}
}

func TestWithSecretKeepsConfigValueSemantics(t *testing.T) {
	base := Config{ID: "p", Protocol: "openai", BaseURL: "https://example.invalid"}
	if got := base.WithSecret(nil); got.SecretRef.Valid {
		t.Fatal("nil override must clear the reference")
	}
	empty := ""
	if got := base.WithSecret(&empty); got.SecretRef.Valid {
		t.Fatal("empty override must clear the reference")
	}
	ref := "provider/p/account"
	got := base.WithSecret(&ref)
	if !got.SecretRef.Valid || got.SecretRef.String != ref {
		t.Fatalf("override not applied: %+v", got.SecretRef)
	}
	if base.SecretRef.Valid {
		t.Fatal("WithSecret mutated the receiver")
	}
}

type staticStore map[string]string

func (s staticStore) Put(context.Context, string, []byte) error    { return nil }
func (s staticStore) PutNew(context.Context, string, []byte) error { return nil }
func (s staticStore) Get(_ context.Context, ref string) ([]byte, error) {
	v, ok := s[ref]
	if !ok {
		return nil, errors.New("not found")
	}
	return []byte(v), nil
}
func (s staticStore) Delete(context.Context, string) error { return nil }

func TestChatRejectsNonLoopbackHTTP(t *testing.T) {
	client := chatClient(t, "http://198.51.100.7:8080")
	if _, err := client.Chat(context.Background(), "synthetic", "m", []Message{{Role: "user", Content: "x"}}); err == nil {
		t.Fatal("plain HTTP to a public address accepted")
	}
}

// Reasoning must round-trip in both directions: read from an upstream reply and
// sent back unchanged when an assistant turn bridges two requests.
func TestReasoningContentRoundTripsOnTheWire(t *testing.T) {
	var requestBody map[string]any
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&requestBody)
		io.WriteString(w, `{"choices":[{"message":{"content":"answer","reasoning_content":"chain of thought"},"finish_reason":"stop"}]}`)
	}))
	defer remote.Close()
	client := chatClient(t, remote.URL)
	messages := []Message{
		{Role: "user", Content: "question"},
		{Role: "assistant", Content: "prior answer", ReasoningContent: "prior reasoning"},
		{Role: "user", Content: "follow up"},
	}
	reply, err := client.Chat(context.Background(), "synthetic", "m", messages)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Reasoning != "chain of thought" {
		t.Fatalf("reasoning not parsed: %q", reply.Reasoning)
	}
	sent, _ := requestBody["messages"].([]any)
	if len(sent) != 3 {
		t.Fatalf("messages %+v", requestBody["messages"])
	}
	assistant, _ := sent[1].(map[string]any)
	if assistant["reasoning_content"] != "prior reasoning" {
		t.Fatalf("assistant reasoning dropped upstream: %+v", assistant)
	}
	if assistant["content"] != "prior answer" {
		t.Fatalf("assistant content dropped upstream: %+v", assistant)
	}
}

func TestToolMessagesAndCallsRoundTripThroughComplete(t *testing.T) {
	var requestBody map[string]any
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&requestBody)
		io.WriteString(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":2}}`)
	}))
	defer remote.Close()
	client := chatClient(t, remote.URL)
	config, err := client.Config(context.Background(), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	extras := map[string]any{
		"temperature": 0.2,
		"tools":       []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}}},
		"thinking":    map[string]any{"type": "enabled"},
	}
	messages := []Message{
		{Role: "user", Content: "find x"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_0", Type: "function", Function: ToolCallFunction{Name: "lookup", Arguments: `{"q":"y"}`}}}},
		{Role: "tool", ToolCallID: "call_0", Content: "result"},
	}
	reply, err := client.Complete(context.Background(), config, "m", extras, messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(reply.ToolCalls) != 1 || reply.ToolCalls[0].Function.Arguments != `{"q":"x"}` || reply.FinishReason != "tool_calls" {
		t.Fatalf("tool calls not parsed: %+v", reply)
	}
	if requestBody["temperature"] != 0.2 {
		t.Fatalf("extras temperature dropped: %+v", requestBody)
	}
	if _, ok := requestBody["tools"]; !ok {
		t.Fatalf("tools dropped: %+v", requestBody)
	}
	if _, ok := requestBody["thinking"]; !ok {
		t.Fatalf("thinking dropped: %+v", requestBody)
	}
	sent, _ := requestBody["messages"].([]any)
	assistant, _ := sent[1].(map[string]any)
	if assistant["tool_calls"] == nil {
		t.Fatalf("assistant tool_calls dropped: %+v", assistant)
	}
	tool, _ := sent[2].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_0" {
		t.Fatalf("tool message dropped: %+v", tool)
	}
	if requestBody["stream"] != false {
		t.Fatalf("buffered call asked for a stream: %+v", requestBody["stream"])
	}
}

func TestStreamRequiresFinishAndDone(t *testing.T) {
	client := catalogClient(t)
	stream := func(payload string) *Client {
		client.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(payload))}, nil
		})
		return client
	}
	cases := map[string]string{
		"no done":    "data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":\"stop\"}]}\n\n",
		"no finish":  "data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n",
		"empty body": "",
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			stream(payload)
			err := client.OpenStream(context.Background(), Config{ID: "synthetic", Protocol: "openai", BaseURL: "http://127.0.0.1"}, "m", map[string]any{}, func(StreamEvent) error { return nil }, nil)
			var api *APIError
			if !errors.As(err, &api) || api.Code != "UPSTREAM_STREAM_INCOMPLETE" {
				t.Fatalf("incomplete stream accepted: %v", err)
			}
		})
	}
	stream("data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	if err := client.OpenStream(context.Background(), Config{ID: "synthetic", Protocol: "openai", BaseURL: "http://127.0.0.1"}, "m", map[string]any{}, func(StreamEvent) error { return nil }, nil); err != nil {
		t.Fatalf("complete stream rejected: %v", err)
	}
}

func TestStreamForwardsToolCallDeltasAndUsage(t *testing.T) {
	client := catalogClient(t)
	payload := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"add\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"
	client.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(payload))}, nil
	})
	events := []StreamEvent{}
	err := client.OpenStream(context.Background(), Config{ID: "synthetic", Protocol: "openai", BaseURL: "http://127.0.0.1"}, "m", map[string]any{}, func(event StreamEvent) error {
		events = append(events, event)
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events %+v", events)
	}
	if len(events[0].ToolCalls) != 1 || events[0].ToolCalls[0].ID != "call_1" || events[0].ToolCalls[0].Function == nil || *events[0].ToolCalls[0].Function.Name != "add" {
		t.Fatalf("tool delta lost fields: %+v", events[0].ToolCalls)
	}
	if events[1].Finish != "tool_calls" || events[1].ToolCalls[0].Function == nil || *events[1].ToolCalls[0].Function.Arguments != "{}" {
		t.Fatalf("tool argument fragment lost: %+v", events[1])
	}
	if events[2].Text != "" || string(events[2].Usage) != `{"prompt_tokens":3,"completion_tokens":1}` {
		t.Fatalf("pure usage chunk mangled: %+v", events[2])
	}
}
