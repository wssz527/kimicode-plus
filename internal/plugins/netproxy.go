package plugins

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"enhancedkimi/internal/credentials"
	"enhancedkimi/internal/redact"
)

const (
	proxyResponseLimit = 8 << 20
	proxyRequestLimit  = 1 << 20
	proxyTimeout       = 30 * time.Second
	proxyRedirectLimit = 3
)

var proxyHeaderPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// proxyClient is the shared outbound client. It is created per manager rather
// than per request so connections are reused, and it never follows a redirect to
// a host the plugin did not declare.
func newProxyClient() *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   proxyTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			// Returning an error stops the redirect chain. The caller sees the
			// 3xx response and can decide; following it silently would let a
			// declared host bounce the plugin to an undeclared one.
			return http.ErrUseLastResponse
		},
	}
}

type proxyRequest struct {
	URL        string            `json:"url"`
	Method     string            `json:"method,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body,omitempty"`
	Credential string            `json:"credential,omitempty"`
	// CredentialHeader names where the credential goes. Without it the plugin
	// gets an Authorization: Bearer header, which is the common case.
	CredentialHeader string `json:"credentialHeader,omitempty"`
	CredentialPrefix string `json:"credentialPrefix,omitempty"`
}

type proxyResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	URL     string            `json:"url"`
	// RedirectTo is set when the origin answered 3xx. The plugin sees the target
	// and can retry explicitly, which is the only way a cross-host redirect is
	// ever followed.
	RedirectTo string `json:"redirectTo,omitempty"`
}

// proxy runs one outbound request for a plugin. The credential is resolved here,
// appended to the outbound headers, and never appears in the result, an activity
// row or an error message.
func (m *Manager) proxy(ctx context.Context, id string, grants Grants, vault *credentials.Vault, action string, r *http.Request, params json.RawMessage) (any, error) {
	var request proxyRequest
	if err := decodeInto(ctx, r, params, &request); err != nil {
		return nil, err
	}
	parsed, err := parseProxyURL(request.URL)
	if err != nil {
		return nil, err
	}
	if err := grants.AuthorizeNet(action, parsed.Hostname()); err != nil {
		return nil, err
	}
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	if method == "" {
		method = http.MethodGet
	}
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead:
	default:
		return nil, apiError(http.StatusBadRequest, "PLUGIN_NET_METHOD", "不支持的请求方法 "+method)
	}
	if len(request.Body) > proxyRequestLimit {
		return nil, apiError(http.StatusRequestEntityTooLarge, "PLUGIN_NET_BODY", "请求体超过 1MB")
	}

	headers := make(http.Header, len(request.Headers)+2)
	for name, value := range request.Headers {
		if !proxyHeaderPattern.MatchString(name) {
			return nil, apiError(http.StatusBadRequest, "PLUGIN_NET_HEADER", "请求头名称无效")
		}
		if len(value) > 4096 {
			return nil, apiError(http.StatusBadRequest, "PLUGIN_NET_HEADER", "请求头值过长")
		}
		// A plugin must not be able to smuggle the injected credential back out
		// through a header it sets itself.
		headers.Set(name, value)
	}
	headers.Del("Host")

	if name := strings.TrimSpace(request.Credential); name != "" {
		if err := grants.AuthorizeCredential(action, name); err != nil {
			return nil, err
		}
		value, err := credentialValue(ctx, vault, name)
		if err != nil {
			return nil, err
		}
		header := strings.TrimSpace(request.CredentialHeader)
		if header == "" {
			header = "Authorization"
		}
		if !proxyHeaderPattern.MatchString(header) {
			return nil, apiError(http.StatusBadRequest, "PLUGIN_NET_HEADER", "凭据注入的请求头名称无效")
		}
		prefix := request.CredentialPrefix
		if header == "Authorization" && prefix == "" {
			prefix = "Bearer "
		}
		headers.Set(header, prefix+value)
	}

	outbound, err := http.NewRequestWithContext(ctx, method, parsed.String(), bytes.NewReader([]byte(request.Body)))
	if err != nil {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_NET_REQUEST", "无法构建请求")
	}
	outbound.Header = headers
	outbound.Header.Set("Accept-Encoding", "identity")

	client := m.proxyClient()
	response, err := client.Do(outbound)
	if err != nil {
		// The error text can contain the URL; it cannot contain the injected
		// header, which net/http never echoes. It is redacted anyway.
		return nil, apiError(http.StatusBadGateway, "PLUGIN_NET_FAILED", redact.Text(err.Error()))
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, proxyResponseLimit))
	if err != nil {
		return nil, apiError(http.StatusBadGateway, "PLUGIN_NET_FAILED", "读取响应失败")
	}
	result := proxyResponse{
		Status:  response.StatusCode,
		Headers: map[string]string{},
		Body:    string(body),
		URL:     parsed.String(),
	}
	for name, values := range response.Header {
		lower := strings.ToLower(name)
		// Set-Cookie is dropped: the core has no cookie jar to hand a plugin, and
		// echoing one into the renderer would create a second session store.
		if lower == "set-cookie" {
			continue
		}
		result.Headers[name] = strings.Join(values, ", ")
	}
	if location := response.Header.Get("Location"); location != "" && response.StatusCode >= 300 && response.StatusCode < 400 {
		if target, err := parsed.Parse(location); err == nil {
			result.RedirectTo = target.String()
		}
	}
	if action != "net.probe" {
		_ = recordActivity(ctx, m.db, id, ActivityNetwork, method+" "+parsed.Hostname()+" → "+itoa(response.StatusCode), m.now())
	}
	return result, nil
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits [12]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		i--
		digits[i] = '-'
	}
	return string(digits[i:])
}

// parseProxyURL accepts only http and https. A plugin cannot ask the core to
// read a local file or open a data URL, both of which would turn the proxy into a
// file reader.
func parseProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_NET_URL", "请求地址为空或过长")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_NET_URL", "请求地址无效")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_NET_SCHEME", "只允许 http 与 https")
	}
	if parsed.Hostname() == "" {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_NET_URL", "请求地址缺少主机名")
	}
	if parsed.User != nil {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_NET_URL", "请求地址不能内嵌凭据")
	}
	return parsed, nil
}

// proxyClient lazily creates the outbound client and keeps it per manager.
func (m *Manager) proxyClient() *http.Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client == nil {
		m.client = newProxyClient()
	}
	return m.client
}

// CloseIdleConnections releases pooled connections at shutdown.
func (m *Manager) CloseIdleConnections() {
	m.mu.Lock()
	client := m.client
	m.mu.Unlock()
	if client != nil {
		if transport, ok := client.Transport.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
	}
}
