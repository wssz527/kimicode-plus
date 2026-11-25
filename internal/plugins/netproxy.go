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

const KCTPluginID = "kimicode-plus.kct"
const KCTSettingsKey = "settings.v1"

const (
	proxyResponseLimit = 8 << 20
	proxyRequestLimit  = 1 << 20
	proxyTimeout       = 30 * time.Second
)

var proxyHeaderPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// proxyFromEnvironment is a variable so tests can drive both egress paths
// without the process-wide cache inside http.ProxyFromEnvironment: the standard
// function reads HTTP(S)_PROXY and NO_PROXY once per process.
var proxyFromEnvironment = http.ProxyFromEnvironment

// proxyClient builds a one-shot client pinned to one egress path: the address
// the guard just approved, or the proxy the environment configures. The
// transport is not shared: a pooled connection belongs to one host's resolution,
// and reusing it for another host would let a keep-alive socket outlive the
// check that authorized it.
func newProxyClient(dial func(ctx context.Context, network, addr string) (net.Conn, error), proxyURL *url.URL) *http.Client {
	transport := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          1,
		MaxIdleConnsPerHost:   1,
		DisableKeepAlives:     true,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	if proxyURL != nil {
		transport.Proxy = http.ProxyURL(proxyURL)
	} else {
		transport.DialContext = dial
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

// proxyForTarget reports the proxy the environment configures for one target.
// A loopback target is never proxied: the grant names a service on this machine,
// and sending that request to a proxy would address a different machine. The
// proxy itself is reached through the standard dialer because its address comes
// from the user's own environment, not from the plugin's grant.
func proxyForTarget(scheme, host, port string) *url.URL {
	if isLoopbackTarget(host) {
		return nil
	}
	request := &http.Request{URL: &url.URL{Scheme: scheme, Host: net.JoinHostPort(host, port)}}
	proxyURL, err := proxyFromEnvironment(request)
	if err != nil || proxyURL == nil {
		return nil
	}
	return proxyURL
}

func isLoopbackTarget(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
	if err := grants.AuthorizeNetURL(action, parsed); err != nil {
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
	if id == KCTPluginID {
		if err := m.authorizeTranslationEndpoint(ctx, parsed, method, request.Credential); err != nil {
			return nil, err
		}
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

	// The broad `net:https` grant carries no host list. A literal address or
	// `localhost` it did not name gets no loopback/private exception; only a target
	// the user wrote in a `net:` entry is exempt.
	host := parsed.Hostname()
	if !grants.AllowsHost(host) {
		if isLoopbackTarget(host) {
			return nil, apiError(http.StatusForbidden, "PLUGIN_NET_HOST_DENIED", "插件网络目标不能是本机或内网地址 "+host)
		}
		if literal, ok := canonicalIP(strings.TrimSuffix(host, ".")); ok && isBlockedAddress(net.ParseIP(literal)) {
			return nil, apiError(http.StatusForbidden, "PLUGIN_NET_HOST_DENIED", "插件网络目标不能是本机或内网地址 "+literal)
		}
	}
	port := endpointPort(parsed)

	// Behind an HTTP(S) proxy the socket goes to the proxy, which owns DNS for
	// the target. A local answer is still checked when one arrives: a name that
	// points at the machine or the LAN must not slip past the address guard just
	// because the user runs a proxy. The lookup is best-effort because proxy-only
	// networks have no local DNS, so a failure is tolerated. Without a proxy the
	// answer is the address that gets dialed, so it is authoritative.
	var client *http.Client
	if proxyURL := proxyForTarget(parsed.Scheme, host, port); proxyURL != nil {
		lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := resolveApprovedAddr(lookupCtx, m.resolver().LookupIPAddr, host)
		cancel()
		if err != nil && !isDNSFailure(err) {
			return nil, err
		}
		client = newProxyClient(nil, proxyURL)
	} else {
		addr, err := resolveApprovedAddr(ctx, m.resolver().LookupIPAddr, host)
		if err != nil {
			return nil, err
		}
		client = newProxyClient(guardedDialer(addr, port), nil)
	}
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
	_ = recordActivity(ctx, m.db, id, ActivityNetwork, method+" "+parsed.Hostname()+" → "+itoa(response.StatusCode), m.now())
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

func (m *Manager) authorizeTranslationEndpoint(ctx context.Context, endpoint *url.URL, method, credential string) error {
	deny := apiError(http.StatusForbidden, "PLUGIN_NET_ENDPOINT_DENIED", "翻译请求必须发送至已配置的翻译端点")
	if method != http.MethodPost || endpoint.Fragment != "" {
		return deny
	}
	host := strings.ToLower(endpoint.Hostname())
	if credential == "" && endpoint.Scheme == "https" && (endpoint.Port() == "" || endpoint.Port() == "443") {
		switch host + endpoint.EscapedPath() {
		case "edge.microsoft.com/translate/translatetext", "translate.googleapis.com/translate_a/single":
			return nil
		}
	}
	raw, found, err := storageGet(ctx, m.db, KCTPluginID, KCTSettingsKey)
	if err != nil {
		return apiError(http.StatusServiceUnavailable, "PLUGIN_STORAGE_FAILED", "无法读取翻译端点设置")
	}
	var settings struct {
		Provider       string `json:"provider"`
		CustomEndpoint string `json:"customEndpoint"`
		UseCredential  *bool  `json:"useCredential"`
	}
	if !found || json.Unmarshal(raw, &settings) != nil || settings.Provider != "custom" || (credential != "" && (credential != "translator" || settings.UseCredential != nil && !*settings.UseCredential)) {
		return deny
	}
	configured, err := parseProxyURL(settings.CustomEndpoint)
	if err != nil || configured.Fragment != "" || (configured.Scheme != "https" && !isLoopbackTarget(configured.Hostname())) {
		return deny
	}
	if endpoint.Scheme != configured.Scheme || !strings.EqualFold(endpoint.Hostname(), configured.Hostname()) || endpointPort(endpoint) != endpointPort(configured) || endpointPath(endpoint) != endpointPath(configured) || endpoint.RawQuery != configured.RawQuery {
		return deny
	}
	return nil
}

func endpointPort(endpoint *url.URL) string {
	if port := endpoint.Port(); port != "" {
		return port
	}
	if endpoint.Scheme == "https" {
		return "443"
	}
	return "80"
}

func endpointPath(endpoint *url.URL) string {
	if endpoint.Path == "" {
		return "/"
	}
	return endpoint.EscapedPath()
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

// resolver returns the resolver used for outbound plugin requests. Tests inject
// one so a rebinding or private-address answer is exercised without touching the
// network; production uses the system resolver.
func (m *Manager) resolver() *net.Resolver {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lookup == nil {
		m.lookup = net.DefaultResolver
	}
	return m.lookup
}
