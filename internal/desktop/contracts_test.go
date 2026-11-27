package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func contractProbeByID(t *testing.T, report ContractReport, id string) ContractProbe {
	t.Helper()
	for _, probe := range report.Probes {
		if probe.ID == id {
			return probe
		}
	}
	t.Fatalf("probe %s not found in %+v", id, report.Probes)
	return ContractProbe{}
}

func newContractTestServer(t *testing.T, handlers map[string]http.HandlerFunc) *KAPClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, NativeAPI)
		if h, ok := handlers[path]; ok {
			h(w, r)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 404, "msg": "not found"})
	}))
	t.Cleanup(server.Close)
	kap := NewKAPClient("")
	kap.base = server.URL
	kap.token = "synthetic-token"
	return kap
}

func TestContractAPISnapshotsPassAcrossVersions(t *testing.T) {
	snapshots := map[string]map[string]any{
		"1.3.4": {
			"/config":      map[string]any{"providers": map[string]any{"kimi": map[string]any{"type": "kimi"}}, "models": map[string]any{"kimi/k2": map[string]any{"model": "k2"}}},
			"/providers":   map[string]any{"items": []any{map[string]any{"id": "kimi", "type": "kimi"}}},
			"/models":      map[string]any{"items": []any{map[string]any{"id": "kimi/k2", "model": "k2"}}},
			"/oauth/usage": map[string]any{"kind": "ok", "quota": map[string]any{"usages": map[string]any{}}},
			"/sessions":    map[string]any{"items": []any{map[string]any{"id": "s-134", "title": "demo"}}},
		},
		"1.3.5": {
			"/config":      map[string]any{"providers": map[string]any{}, "models": map[string]any{}, "default_model": "kimi/k2.5"},
			"/providers":   map[string]any{"items": []any{map[string]any{"id": "managed:kimi-code", "type": "kimi"}}},
			"/models":      map[string]any{"items": []any{map[string]any{"model": "k2.5"}}},
			"/oauth/usage": map[string]any{"kind": "unauthenticated"},
			"/sessions":    map[string]any{"items": []any{map[string]any{"id": "s-135"}}},
		},
		"1.3.7": {
			"/config":      map[string]any{"providers": map[string]any{}, "models": map[string]any{}},
			"/providers":   map[string]any{"items": []any{map[string]any{"id": "managed:kimi-code"}}},
			"/models":      map[string]any{"items": []any{map[string]any{"id": "managed:kimi-code/k2.8", "model": "k2.8"}}},
			"/oauth/usage": map[string]any{"kind": "ok", "quota": map[string]any{"usages": map[string]any{"limit5h": map[string]any{"usedRatio": 0.2}}}},
			"/sessions":    map[string]any{"items": []any{map[string]any{"id": "s-137", "title": "session"}}},
		},
		"1.4.3": {
			"/config":      map[string]any{"providers": map[string]any{}, "models": map[string]any{}},
			"/providers":   map[string]any{"items": []any{map[string]any{"id": "managed:kimi-code"}}},
			"/models":      map[string]any{"items": []any{map[string]any{"id": "managed:kimi-code/k2.9", "model": "k2.9"}}},
			"/oauth/usage": map[string]any{"kind": "ok", "quota": map[string]any{"usages": map[string]any{"limit5h": map[string]any{"usedRatio": 0.1}}}},
			"/sessions":    map[string]any{"items": []any{map[string]any{"id": "s-143", "title": "session"}}},
		},
		"1.5.0": {
			"/config":      map[string]any{"providers": map[string]any{}, "models": map[string]any{}},
			"/providers":   map[string]any{"items": []any{map[string]any{"id": "managed:kimi-code"}}},
			"/models":      map[string]any{"items": []any{map[string]any{"id": "managed:kimi-code/k2.9", "model": "k2.9"}}},
			"/oauth/usage": map[string]any{"kind": "ok", "quota": map[string]any{"usages": map[string]any{"limit5h": map[string]any{"usedRatio": 0.1}}}},
			"/sessions":    map[string]any{"items": []any{map[string]any{"id": "s-150", "title": "session"}}},
		},
		"1.5.1": {
			"/config":      map[string]any{"providers": map[string]any{}, "models": map[string]any{}},
			"/providers":   map[string]any{"items": []any{map[string]any{"id": "managed:kimi-code"}}},
			"/models":      map[string]any{"items": []any{map[string]any{"id": "managed:kimi-code/k2.9", "model": "k2.9"}}},
			"/oauth/usage": map[string]any{"kind": "ok", "quota": map[string]any{"usages": map[string]any{"limit5h": map[string]any{"usedRatio": 0.1}}}},
			"/sessions":    map[string]any{"items": []any{map[string]any{"id": "s-151", "title": "session"}}},
		},
		"1.9.9": {
			"/config":      map[string]any{"providers": map[string]any{}, "models": map[string]any{}},
			"/providers":   map[string]any{"items": []any{map[string]any{"id": "managed:kimi-code"}}},
			"/models":      map[string]any{"items": []any{map[string]any{"id": "managed:kimi-code/k3", "model": "k3"}}},
			"/oauth/usage": map[string]any{"kind": "ok", "usage": map[string]any{}},
			"/sessions":    map[string]any{"items": []any{map[string]any{"id": "s-199"}}},
		},
	}

	for version, fixtures := range snapshots {
		t.Run(version, func(t *testing.T) {
			handlers := map[string]http.HandlerFunc{}
			for endpoint, body := range fixtures {
				payload := body
				handlers[endpoint] = func(w http.ResponseWriter, r *http.Request) {
					writeNativeTestEnvelope(w, payload)
				}
			}
			kap := newContractTestServer(t, handlers)
			service := &NativeService{kap: kap}
			rawReport, err := json.Marshal(map[string]any{
				"appVersion": version,
				"electron":   SupportedElectron,
				"probes": []ContractProbe{
					{ID: "dom.session_info_available", Group: "dom", Status: ContractAvailable},
					{ID: "dom.right_panel", Group: "dom", Status: ContractAvailable},
					{ID: "dom.sidebar_nav", Group: "dom", Status: ContractAvailable, Detail: "多标签页模式"},
					{ID: "dom.settings_dialog", Group: "dom", Status: ContractAvailable},
					{ID: "dom.settings_account", Group: "dom", Status: ContractAvailable},
					{ID: "dom.settings_agent", Group: "dom", Status: ContractAvailable},
					{ID: "dom.settings_providers", Group: "dom", Status: ContractAvailable},
					{ID: "dom.settings_lab", Group: "dom", Status: ContractAvailable},
					{ID: "dom.sidebar_footer", Group: "dom", Status: ContractAvailable},
					{ID: "dom.account_menu", Group: "dom", Status: ContractAvailable},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Action(context.Background(), "contracts.report", rawReport); err != nil {
				t.Fatal(err)
			}
			value, err := service.Action(context.Background(), "contracts.status", nil)
			if err != nil {
				t.Fatal(err)
			}
			report, ok := value.(ContractReport)
			if !ok {
				t.Fatalf("unexpected report type %T", value)
			}
			wantVerified := version != "1.9.9"
			if report.Verified != wantVerified {
				t.Fatalf("version %s verified = %v, want %v", version, report.Verified, wantVerified)
			}
			if !wantVerified && report.VersionNote != UnverifiedVersionNote {
				t.Fatalf("unverified version note = %q, want %q", report.VersionNote, UnverifiedVersionNote)
			}
			for _, id := range []string{
				"api.config", "api.providers", "api.models", "api.oauth_usage", "api.sessions",
				"dom.session_info_available", "dom.right_panel", "dom.sidebar_nav",
				"dom.settings_dialog", "dom.settings_account", "dom.settings_agent",
				"dom.settings_providers", "dom.settings_lab", "dom.sidebar_footer", "dom.account_menu",
			} {
				probe := contractProbeByID(t, report, id)
				if probe.Status != ContractAvailable {
					t.Fatalf("version %s probe %s = %s (%s), want available", version, id, probe.Status, probe.Reason)
				}
			}
		})
	}
}

func TestContractSingleFailureIsolatesOnlyAffectedProbe(t *testing.T) {
	kap := newContractTestServer(t, map[string]http.HandlerFunc{
		"/config": func(w http.ResponseWriter, r *http.Request) {
			writeNativeTestEnvelope(w, map[string]any{"providers": map[string]any{}, "models": map[string]any{}})
		},
		"/providers": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 404, "msg": "gone"})
		},
		"/models": func(w http.ResponseWriter, r *http.Request) {
			writeNativeTestEnvelope(w, map[string]any{"unexpected": true})
		},
		"/oauth/usage": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 401, "msg": "unauthorized"})
		},
		"/sessions": func(w http.ResponseWriter, r *http.Request) {
			writeNativeTestEnvelope(w, map[string]any{"items": []any{map[string]any{"id": "s1"}}})
		},
	})
	service := &NativeService{kap: kap}
	raw, _ := json.Marshal(map[string]any{
		"appVersion": "1.9.9",
		"electron":   SupportedElectron,
		"probes": []ContractProbe{
			{ID: "dom.session_info_available", Group: "dom", Status: ContractDisabled, Reason: "会话信息「可打开」分组缺失"},
			{ID: "dom.right_panel", Group: "dom", Status: ContractAvailable},
			{ID: "dom.sidebar_nav", Group: "dom", Status: ContractAvailable},
		},
	})
	if _, err := service.Action(context.Background(), "contracts.report", raw); err != nil {
		t.Fatal(err)
	}
	report := service.ContractStatus(context.Background())

	if p := contractProbeByID(t, report, "dom.session_info_available"); p.Status != ContractDisabled || !strings.Contains(p.Reason, "缺失") {
		t.Fatalf("deleted DOM probe = %+v", p)
	}
	if p := contractProbeByID(t, report, "dom.right_panel"); p.Status != ContractAvailable {
		t.Fatalf("unrelated DOM probe affected: %+v", p)
	}
	if p := contractProbeByID(t, report, "api.providers"); p.Status != ContractDisabled || !strings.Contains(p.Reason, "404") {
		t.Fatalf("404 API probe = %+v", p)
	}
	if p := contractProbeByID(t, report, "api.models"); p.Status != ContractDisabled || !strings.Contains(p.Reason, "items") {
		t.Fatalf("schema-broken API probe = %+v", p)
	}
	if p := contractProbeByID(t, report, "api.oauth_usage"); p.Status != ContractUntested {
		t.Fatalf("401 oauth usage probe = %+v, want untested", p)
	}
	if p := contractProbeByID(t, report, "api.sessions"); p.Status != ContractAvailable {
		t.Fatalf("healthy sessions probe affected: %+v", p)
	}
	if p := contractProbeByID(t, report, "api.config"); p.Status != ContractAvailable {
		t.Fatalf("healthy config probe affected: %+v", p)
	}
}

func TestContractReportResetsStaleProbesOnVersionTransition(t *testing.T) {
	service := &NativeService{}
	raw132, _ := json.Marshal(map[string]any{
		"appVersion": "1.3.2",
		"electron":   SupportedElectron,
		"probes": []ContractProbe{
			{ID: "dom.settings_account", Group: "dom", Status: ContractAvailable},
			{ID: "dom.sidebar_nav", Group: "dom", Status: ContractAvailable},
		},
	})
	if _, err := service.Action(context.Background(), "contracts.report", raw132); err != nil {
		t.Fatal(err)
	}
	r132 := service.ContractStatus(context.Background())
	if r132.AppVersion != "1.3.2" || r132.Verified {
		t.Fatalf("1.3.2 status = %+v, want unverified 1.3.2", r132)
	}
	if p := contractProbeByID(t, r132, "dom.settings_account"); p.Status != ContractAvailable {
		t.Fatalf("1.3.2 settings_account = %+v", p)
	}

	raw137, _ := json.Marshal(map[string]any{
		"appVersion": "1.3.7",
		"electron":   SupportedElectron,
		"probes": []ContractProbe{
			{ID: "dom.sidebar_nav", Group: "dom", Status: ContractAvailable},
		},
	})
	if _, err := service.Action(context.Background(), "contracts.report", raw137); err != nil {
		t.Fatal(err)
	}
	r137 := service.ContractStatus(context.Background())
	if r137.AppVersion != "1.3.7" || !r137.Verified || r137.VersionNote != "" {
		t.Fatalf("1.3.7 transition status = %+v, want verified 1.3.7", r137)
	}
	if p := contractProbeByID(t, r137, "dom.settings_account"); p.Status != ContractUntested {
		t.Fatalf("stale 1.3.2 probe leaked across version upgrade: %+v", p)
	}
}

func TestSlowAPIProbeDoesNotStarveOtherProbes(t *testing.T) {
	prev := apiProbeTimeout
	apiProbeTimeout = 150 * time.Millisecond
	t.Cleanup(func() { apiProbeTimeout = prev })

	kap := newContractTestServer(t, map[string]http.HandlerFunc{
		"/config": func(w http.ResponseWriter, r *http.Request) {
			writeNativeTestEnvelope(w, map[string]any{"providers": map[string]any{}, "models": map[string]any{}})
		},
		"/providers": func(w http.ResponseWriter, r *http.Request) {
			writeNativeTestEnvelope(w, map[string]any{"items": []any{map[string]any{"id": "kimi"}}})
		},
		"/models": func(w http.ResponseWriter, r *http.Request) {
			writeNativeTestEnvelope(w, map[string]any{"items": []any{map[string]any{"id": "kimi/k2.8"}}})
		},
		"/oauth/usage": func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		},
		"/sessions": func(w http.ResponseWriter, r *http.Request) {
			writeNativeTestEnvelope(w, map[string]any{"items": []any{map[string]any{"id": "s1"}}})
		},
	})
	service := &NativeService{kap: kap}
	report := service.ContractStatus(context.Background())

	if p := contractProbeByID(t, report, "api.oauth_usage"); p.Status != ContractDisabled {
		t.Fatalf("hanging oauth_usage probe = %+v, want disabled", p)
	}
	for _, id := range []string{"api.config", "api.providers", "api.models", "api.sessions"} {
		if p := contractProbeByID(t, report, id); p.Status != ContractAvailable {
			t.Fatalf("healthy probe %s starved by slow oauth_usage: %+v", id, p)
		}
	}
}
