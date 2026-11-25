package provider

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"enhancedkimi/internal/credentials"
)

type Config struct {
	ID, Protocol, BaseURL string
	SecretRef             sql.NullString
	CustomHeaders         map[string]string
}

// WithSecret returns a copy that authenticates with ref instead of the
// provider's stored secretRef. It is how an account-specific credential
// reaches the wire without rewriting provider configuration.
func (p Config) WithSecret(ref *string) Config {
	switch {
	case ref == nil:
		p.SecretRef = sql.NullString{}
	case *ref == "":
		p.SecretRef = sql.NullString{}
	default:
		p.SecretRef = sql.NullString{String: *ref, Valid: true}
	}
	return p
}

const (
	// CatalogBudget bounds the whole model discovery call.
	CatalogBudget = 45 * time.Second
	// PageBudget bounds a single catalog request.
	PageBudget = 15 * time.Second
	// DefaultChatBudget bounds one chat completion. Generation is not
	// catalog traffic: a long reply may legitimately exceed the catalog
	// window, and the shared HTTP client must not cap it.
	DefaultChatBudget = 5 * time.Minute
)

type Client struct {
	DB         *sql.DB
	Secrets    credentials.InsertStore
	HTTP       *http.Client
	ChatBudget time.Duration
	PageBudget time.Duration
}

// ToolCall is one assistant tool invocation. Arguments stay a JSON string
// because that is what the protocol carries and what a client must echo back
// unchanged.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Message is the wire form of one chat turn. ReasoningContent, ToolCalls and
// ToolCallID round-trip through the same struct so a supplier sees exactly what
// it produced: pointers are avoided, and omitempty keeps plain text turns
// unchanged.
type Message struct {
	Role             string          `json:"role"`
	Content          string          `json:"content"`
	Name             string          `json:"name,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	PlannerContext   *PlannerContext `json:"-"`
}
type Reply struct {
	Text         string
	Reasoning    string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        json.RawMessage
}
type Model struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func New(db *sql.DB, secrets credentials.InsertStore) *Client {
	return &Client{DB: db, Secrets: secrets, ChatBudget: DefaultChatBudget, PageBudget: PageBudget, HTTP: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c *Client) Config(ctx context.Context, id string) (Config, error) {
	p := Config{ID: id}
	err := c.DB.QueryRowContext(ctx, `SELECT protocol,base_url,secret_ref FROM registry_providers WHERE id=?`, id).Scan(&p.Protocol, &p.BaseURL, &p.SecretRef)
	if errors.Is(err, sql.ErrNoRows) {
		return Config{}, fmt.Errorf("%w: %s", ErrProviderNotFound, id)
	}
	return p, err
}

// StatusError carries an upstream non-2xx status so a caller that speaks a
// different protocol can map it without parsing an error string.
type StatusError struct{ Status int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("provider returned HTTP %d; request was not retried", e.Status)
}

// newRequest validates the target URL, attaches credentials and builds the
// request. Streaming and buffered calls share it so the security checks cannot
// diverge.
func (c *Client) newRequest(ctx context.Context, p Config, method, suffix string, body any) (*http.Request, error) {
	base, err := url.Parse(p.BaseURL)
	if err != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("invalid provider URL")
	}
	u, err := url.Parse(strings.TrimRight(p.BaseURL, "/") + suffix)
	if err != nil {
		return nil, errors.New("invalid provider URL")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()))) {
		return nil, errors.New("provider requires HTTPS or explicit loopback HTTP")
	}
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if p.SecretRef.Valid && p.SecretRef.String != "" {
		if c.Secrets == nil {
			return nil, errors.New("provider credential unavailable; unlock vault and check secretRef")
		}
		secret, e := c.Secrets.Get(ctx, p.SecretRef.String)
		if e != nil {
			return nil, errors.New("provider credential unavailable; unlock vault and check secretRef")
		}
		defer clear(secret)
		if p.Protocol == "anthropic" {
			req.Header.Set("x-api-key", string(secret))
		} else if p.Protocol == "gemini" {
			req.Header.Set("x-goog-api-key", string(secret))
		} else {
			req.Header.Set("Authorization", "Bearer "+string(secret))
		}
	}
	if p.Protocol == "anthropic" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	for name, value := range p.CustomHeaders {
		switch http.CanonicalHeaderKey(name) {
		case "Host", "Content-Length", "Transfer-Encoding", "Connection", "Proxy-Authorization", "Accept", "Content-Type", "Authorization", "X-Api-Key", "X-Goog-Api-Key":
			continue
		}
		req.Header.Set(name, value)
	}
	return req, nil
}

// do sends a prepared request with the given client budget. Transport
// failures are mapped to the shared sentinel errors; non-2xx responses close
// the body and return a StatusError naming the upstream status only.
func (c *Client) do(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		return nil, errors.New("provider connection failed; request was not retried")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		res.Body.Close()
		return nil, &StatusError{Status: res.StatusCode}
	}
	return res, nil
}

func (c *Client) request(ctx context.Context, client *http.Client, p Config, method, suffix string, body any) ([]byte, error) {
	req, err := c.newRequest(ctx, p, method, suffix, body)
	if err != nil {
		return nil, err
	}
	res, err := c.do(ctx, client, req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 2*1024*1024+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, context.DeadlineExceeded
		}
		return nil, errors.New("provider response is incomplete")
	}
	if len(data) > 2*1024*1024 {
		return nil, errors.New("provider response exceeds 2 MiB")
	}
	return data, nil
}

// CatalogModels returns the models already imported for a provider. It reads
// local state only, so a gateway planning a route never waits on a supplier or
// fails because one supplier is unreachable.
func (c *Client) CatalogModels(ctx context.Context, id string) ([]Model, error) {
	rows, err := c.DB.QueryContext(ctx, `SELECT model_id, display_name FROM registry_models WHERE provider_id=? ORDER BY model_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Model{}
	for rows.Next() {
		var m Model
		if err := rows.Scan(&m.ID, &m.Name); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Models performs live catalog discovery with the shared 45-second budget. It
// is not used on a request path that must stay local.
func (c *Client) Models(ctx context.Context, id string) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, CatalogBudget)
	defer cancel()
	p, err := c.Config(ctx, id)
	if err != nil {
		return nil, err
	}
	var out []Model
	next := ""
	seen := map[string]bool{}
	for page := 0; page < 20; page++ {
		suffix := "/models"
		if next != "" {
			if p.Protocol == "anthropic" {
				suffix += "?after_id=" + url.QueryEscape(next)
			} else {
				suffix += "?pageToken=" + url.QueryEscape(next)
			}
		}
		raw, err := c.modelPage(ctx, p, suffix)
		if err != nil {
			return nil, err
		}
		var response struct {
			Data []struct {
				ID   string `json:"id"`
				Name string `json:"display_name"`
			} `json:"data"`
			Models []struct {
				Name        string `json:"name"`
				DisplayName string `json:"displayName"`
			} `json:"models"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
			Next    string `json:"nextPageToken"`
		}
		if json.Unmarshal(raw, &response) != nil {
			return nil, errors.New("invalid model catalog")
		}
		for _, m := range response.Data {
			if m.ID != "" && !seen[m.ID] {
				out = append(out, Model{m.ID, m.Name})
				seen[m.ID] = true
			}
		}
		for _, m := range response.Models {
			id := strings.TrimPrefix(m.Name, "models/")
			if id != "" && !seen[id] {
				out = append(out, Model{id, m.DisplayName})
				seen[id] = true
			}
		}
		next = response.Next
		if response.HasMore {
			next = response.LastID
			if next == "" {
				return nil, errors.New("catalog pagination cursor missing")
			}
		}
		if next == "" {
			if out == nil {
				out = []Model{}
			}
			return out, nil
		}
	}
	return nil, errors.New("catalog exceeds 20 pages; no partial import performed")
}

// pageContext bounds one catalog page without moving the request deadline.
// The child context keeps the caller's overall Deadline so every page shares
// the discovery window, while a timer cancels a page that overruns its own
// budget. Cancelling through Done instead of a new deadline is what stops the
// per-page limit from renewing the shared window.
func (c *Client) pageContext(ctx context.Context) (context.Context, func() bool, context.CancelFunc) {
	budget := c.PageBudget
	if budget <= 0 {
		budget = PageBudget
	}
	pageCtx, cancel := context.WithCancel(ctx)
	var expired atomic.Bool
	timer := time.AfterFunc(budget, func() {
		expired.Store(true)
		cancel()
	})
	return pageCtx, expired.Load, func() {
		timer.Stop()
		cancel()
	}
}

func (c *Client) modelPage(ctx context.Context, p Config, suffix string) ([]byte, error) {
	if p.Protocol == "anthropic" && !strings.HasSuffix(strings.TrimRight(p.BaseURL, "/"), "/v1") {
		suffix = "/v1" + suffix
	}
	pageCtx, expired, stop := c.pageContext(ctx)
	defer stop()
	raw, err := c.request(pageCtx, c.HTTP, p, "GET", suffix, nil)
	if err != nil && expired() && ctx.Err() == nil {
		return nil, context.DeadlineExceeded
	}
	return raw, err
}

// ErrProviderNotFound distinguishes a missing provider row from a provider
// whose credential is unavailable. The two need different HTTP answers: a
// vanished provider is a configuration error the user can fix, while a locked
// vault is a temporary state.
var ErrProviderNotFound = errors.New("provider not found")

// Ready reports whether a provider can be dispatched to. It returns
// ErrProviderNotFound for a missing row and the vault error for a locked or
// unresolved credential.
func (c *Client) Ready(ctx context.Context, id string) error {
	p, _, err := c.EffectiveConfig(ctx, id)
	if err != nil {
		return err
	}
	if p.SecretRef.Valid && p.SecretRef.String != "" {
		value, err := c.Secrets.Get(ctx, p.SecretRef.String)
		clear(value)
		return err
	}
	return nil
}
func (c *Client) Chat(ctx context.Context, id, model string, messages []Message) (Reply, error) {
	p, err := c.Config(ctx, id)
	if err != nil {
		return Reply{}, err
	}
	return c.ChatWith(ctx, p, model, messages)
}

// ChatWith sends one completion for an explicit config. The caller supplies
// the credential via Config.WithSecret when an account overrides it.
func (c *Client) ChatWith(ctx context.Context, p Config, model string, messages []Message) (Reply, error) {
	return c.Complete(ctx, p, model, nil, messages)
}

// Complete sends one non-streaming completion with protocol extras. For the
// OpenAI protocol extras are merged into the request body as received, so
// accepted fields (temperature, tools, thinking, builtin_function, …) reach the
// supplier instead of being dropped while the body is rebuilt.
func (c *Client) Complete(ctx context.Context, p Config, model string, extras map[string]any, messages []Message) (Reply, error) {
	budget := c.ChatBudget
	if budget <= 0 {
		budget = DefaultChatBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var suffix string
	var body any
	switch p.Protocol {
	case "openai":
		suffix = "/chat/completions"
		merged := map[string]any{"model": model, "messages": messages, "stream": false}
		for key, value := range extras {
			switch key {
			case "model", "messages", "stream":
				continue
			}
			merged[key] = value
		}
		body = merged
	case "responses":
		suffix = "/responses"
		body = map[string]any{"model": model, "input": messages, "stream": false, "store": false}
	case "anthropic":
		suffix = "/messages"
		if !strings.HasSuffix(strings.TrimRight(p.BaseURL, "/"), "/v1") {
			suffix = "/v1" + suffix
		}
		body = map[string]any{"model": model, "messages": messages, "max_tokens": 4096, "stream": false}
	case "gemini":
		if strings.ContainsAny(model, "/?#") {
			return Reply{}, errors.New("invalid Gemini model ID")
		}
		suffix = "/models/" + url.PathEscape(model) + ":generateContent"
		contents := []any{}
		for _, m := range messages {
			role := "user"
			if m.Role == "assistant" {
				role = "model"
			}
			contents = append(contents, map[string]any{"role": role, "parts": []any{map[string]string{"text": m.Content}}})
		}
		body = map[string]any{"contents": contents}
	default:
		return Reply{}, errors.New("unsupported provider protocol")
	}
	raw, err := c.request(ctx, c.HTTP, p, "POST", suffix, body)
	if err != nil {
		return Reply{}, err
	}
	var v struct {
		Choices []struct {
			Message struct {
				Content          string     `json:"content"`
				ReasoningContent string     `json:"reasoning_content"`
				Reasoning        string     `json:"reasoning"`
				ToolCalls        []ToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		Usage         json.RawMessage `json:"usage"`
		UsageMetadata json.RawMessage `json:"usageMetadata"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return Reply{}, errors.New("invalid chat response")
	}
	reply := Reply{Usage: v.Usage}
	switch p.Protocol {
	case "openai":
		if len(v.Choices) > 0 {
			choice := v.Choices[0]
			reply.Text = choice.Message.Content
			reply.Reasoning = choice.Message.ReasoningContent
			if reply.Reasoning == "" {
				reply.Reasoning = choice.Message.Reasoning
			}
			reply.ToolCalls = choice.Message.ToolCalls
			reply.FinishReason = choice.FinishReason
		}
	case "responses":
		for _, o := range v.Output {
			for _, part := range o.Content {
				reply.Text += part.Text
			}
		}
	case "anthropic":
		for _, part := range v.Content {
			reply.Text += part.Text
		}
	case "gemini":
		reply.Usage = v.UsageMetadata
		if len(v.Candidates) > 0 {
			for _, part := range v.Candidates[0].Content.Parts {
				if !part.Thought {
					reply.Text += part.Text
				}
			}
		}
	}
	// A reasoning-only or tool-only reply is still a real answer: an assistant
	// turn may legitimately carry no text when it calls a tool.
	if strings.TrimSpace(reply.Text) == "" && strings.TrimSpace(reply.Reasoning) == "" && len(reply.ToolCalls) == 0 {
		return Reply{}, errors.New("provider returned no text; no automatic retry")
	}
	return reply, nil
}
