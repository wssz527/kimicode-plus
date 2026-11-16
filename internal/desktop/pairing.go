package desktop

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/security"
)

const PairingProtocol = 1

type CapabilityReport struct {
	Protocol     int               `json:"protocol"`
	HostID       string            `json:"hostId"`
	HostVersion  string            `json:"hostVersion"`
	Challenge    string            `json:"challenge"`
	Synthetic    bool              `json:"synthetic"`
	Capabilities map[string]string `json:"capabilities"`
	Scopes       []string          `json:"scopes"`
}

type PairingOptions struct {
	Endpoint                 string
	ExpectedHostID           string
	ExpectedVersion          string
	PairingCode              string
	ApprovedIsolatedTestHost bool
}

type PairedHost struct {
	endpoint, hostID, version, token string
	client                           *http.Client
	report                           CapabilityReport
}

func PairIsolatedHost(ctx context.Context, options PairingOptions) (*PairedHost, error) {
	if !options.ApprovedIsolatedTestHost {
		return nil, fmt.Errorf("explicit isolated test host approval required")
	}
	u, err := url.Parse(options.Endpoint)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Scheme != "http" {
		return nil, fmt.Errorf("test bridge requires a literal loopback HTTP origin")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() || options.ExpectedHostID == "" || options.ExpectedVersion == "" || len(options.PairingCode) < 32 {
		return nil, fmt.Errorf("test host identity and pairing code required")
	}
	host := &PairedHost{endpoint: strings.TrimRight(options.Endpoint, "/"), hostID: options.ExpectedHostID, version: options.ExpectedVersion, client: &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("pairing redirect denied") }}}
	challenge := security.Random()
	payload, _ := json.Marshal(map[string]any{"protocol": PairingProtocol, "pairingCode": options.PairingCode, "challenge": challenge, "scopes": []string{"capabilities:read"}})
	req, err := http.NewRequestWithContext(ctx, "POST", host.endpoint+"/ekd/adapter/v1/pair", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := host.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("test host pairing denied (%d)", resp.StatusCode)
	}
	var result struct {
		Token  string           `json:"token"`
		Report CapabilityReport `json:"report"`
	}
	if err := decodeBounded(resp.Body, &result); err != nil {
		return nil, err
	}
	if len(result.Token) < 32 || result.Report.Challenge != challenge || !host.validReport(result.Report) {
		return nil, fmt.Errorf("pairing identity/protocol/scope mismatch")
	}
	host.token = result.Token
	host.report = result.Report
	return host, nil
}

func decodeBounded(reader io.Reader, out any) error {
	raw, err := io.ReadAll(io.LimitReader(reader, 65537))
	if err != nil {
		return err
	}
	if len(raw) > 65536 {
		return fmt.Errorf("capability report too large")
	}
	return json.Unmarshal(raw, out)
}
func (h *PairedHost) validReport(r CapabilityReport) bool {
	if r.Protocol != PairingProtocol || r.HostID != h.hostID || r.HostVersion != h.version || !r.Synthetic || len(r.Scopes) != 1 || r.Scopes[0] != "capabilities:read" {
		return false
	}
	for _, v := range r.Capabilities {
		if v != "AVAILABLE" && v != "UNAVAILABLE" && v != "UNKNOWN" {
			return false
		}
	}
	return true
}
func (h *PairedHost) Report(ctx context.Context) (CapabilityReport, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", h.endpoint+"/ekd/adapter/v1/capabilities", nil)
	if err != nil {
		return CapabilityReport{}, err
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	resp, err := h.client.Do(req)
	if err != nil {
		return CapabilityReport{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return CapabilityReport{}, fmt.Errorf("capability read denied")
	}
	var report CapabilityReport
	if err = decodeBounded(resp.Body, &report); err != nil {
		return report, err
	}
	if !h.validReport(report) {
		return report, fmt.Errorf("host identity changed")
	}
	return report, nil
}
func (h *PairedHost) Close() { h.token = ""; h.client.CloseIdleConnections() }

func NewIsolatedTestHost(origin, hostID, version, pairingCode string) http.Handler {
	mux := http.NewServeMux()
	var mu sync.Mutex
	grants := map[string]time.Time{}
	report := func(challenge string) CapabilityReport {
		return CapabilityReport{Protocol: PairingProtocol, HostID: hostID, HostVersion: version, Challenge: challenge, Synthetic: true, Scopes: []string{"capabilities:read"}, Capabilities: map[string]string{"metadata": "AVAILABLE", "sessions": "UNAVAILABLE", "submit": "UNAVAILABLE", "attachments": "UNAVAILABLE", "compaction": "UNAVAILABLE"}}
	}
	mux.HandleFunc("POST /ekd/adapter/v1/pair", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Protocol    int      `json:"protocol"`
			PairingCode string   `json:"pairingCode"`
			Challenge   string   `json:"challenge"`
			Scopes      []string `json:"scopes"`
		}
		if !httpx.Decode(w, r, &req) {
			return
		}
		if req.Protocol != PairingProtocol || len(pairingCode) < 32 || subtle.ConstantTimeCompare([]byte(req.PairingCode), []byte(pairingCode)) != 1 || len(req.Challenge) != 64 || len(req.Scopes) != 1 || req.Scopes[0] != "capabilities:read" {
			httpx.Fail(w, 403, "PAIRING_DENIED", "测试配对凭据或权限不匹配")
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for token, expiry := range grants {
			if time.Now().After(expiry) {
				delete(grants, token)
			}
		}
		if len(grants) >= 16 {
			httpx.Fail(w, 429, "GRANT_LIMIT", "测试授权已达上限")
			return
		}
		token := security.Random()
		grants[token] = time.Now().Add(5 * time.Minute)
		httpx.JSON(w, 200, map[string]any{"token": token, "report": report(req.Challenge)})
	})
	mux.HandleFunc("GET /ekd/adapter/v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		expiry, ok := grants[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		mu.Unlock()
		if !ok || time.Now().After(expiry) {
			httpx.Fail(w, 403, "SCOPE_DENIED", "授权无效或过期")
			return
		}
		httpx.JSON(w, 200, report(""))
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		u, _ := url.Parse(origin)
		if u == nil || r.Host != u.Host || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != origin) {
			httpx.Fail(w, 403, "ORIGIN_DENIED", "测试宿主来源不匹配")
			return
		}
		mux.ServeHTTP(w, r)
	})
}
