package registry_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"enhancedkimi/internal/registry"
	"enhancedkimi/internal/storage"
	"enhancedkimi/internal/usage"
)

// TestLiveLoopback exercises both modules through a real HTTP server and a real
// on-disk SQLite file, with no model/network access. It is a production-shape
// smoke test rather than a fake in-process handler shortcut.
func TestLiveLoopback(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "e2e.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	reg, err := registry.New(db)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	use, err := usage.New(db)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	mux := http.NewServeMux()
	reg.Register(mux)
	use.Register(mux)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go server.Serve(listener)
	defer server.Close()
	base := "http://" + listener.Addr().String()

	call := func(method, path string, body any, out any) int {
		t.Helper()
		var reader io.Reader = http.NoBody
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			reader = bytes.NewReader(encoded)
		}
		request, err := http.NewRequest(method, base+path, reader)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if out != nil && len(payload) > 0 {
			if err := json.Unmarshal(payload, out); err != nil {
				t.Fatalf("decode %s %s: %v (%s)", method, path, err, payload)
			}
		}
		return response.StatusCode
	}

	fixture, err := os.ReadFile(filepath.Join("..", "registry", "testdata", "models-dev-sample.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	var catalog any
	if err := json.Unmarshal(fixture, &catalog); err != nil {
		t.Fatalf("fixture decode: %v", err)
	}
	var applied struct {
		Revision int64 `json:"revision"`
		Preview  bool  `json:"preview"`
		Import   *struct {
			Created int `json:"created"`
		} `json:"import"`
	}
	if status := call("POST", "/enhance/v1/models/modelsdev", map[string]any{"catalog": catalog, "revision": 0}, &applied); status != http.StatusOK {
		t.Fatalf("modelsdev status = %d", status)
	}
	if applied.Preview || applied.Import == nil || applied.Import.Created == 0 {
		t.Fatalf("modelsdev = %+v", applied)
	}

	var models struct {
		Items []struct {
			ProviderID   string `json:"providerId"`
			ID           string `json:"id"`
			Capabilities map[string]struct {
				Value     any     `json:"value"`
				Source    string  `json:"source"`
				FetchedAt *string `json:"fetchedAt"`
			} `json:"capabilities"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if status := call("GET", "/enhance/v1/models?limit=100", nil, &models); status != http.StatusOK {
		t.Fatalf("models status = %d", status)
	}
	if models.Total < 8 {
		t.Fatalf("models total = %d", models.Total)
	}
	found := false
	for _, model := range models.Items {
		if model.ProviderID == "anthropic" && model.ID == "claude-opus-5-5" {
			found = true
			if model.Capabilities["context"].Source != "modelsdev" || model.Capabilities["context"].FetchedAt == nil {
				t.Fatalf("provenance = %+v", model.Capabilities["context"])
			}
			if model.Capabilities["cacheTTL"].Source != "UNKNOWN" {
				t.Fatalf("cacheTTL = %+v", model.Capabilities["cacheTTL"])
			}
		}
	}
	if !found {
		t.Fatal("anthropic model missing after modelsdev import")
	}

	var override struct {
		Revision int64 `json:"revision"`
		After    struct {
			Value  any    `json:"value"`
			Source string `json:"source"`
		} `json:"after"`
	}
	status := call("POST", "/enhance/v1/models/override", map[string]any{
		"providerId": "anthropic", "id": "claude-opus-5-5", "field": "context", "value": nil, "revision": applied.Revision,
	}, &override)
	if status != http.StatusOK || override.After.Source != "manual" || override.After.Value != nil {
		t.Fatalf("override = %d %+v", status, override)
	}

	var changes struct {
		Items []struct {
			ID       int64  `json:"id"`
			Revision int64  `json:"revision"`
			Summary  string `json:"summary"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if status := call("GET", "/enhance/v1/model-changes?limit=10", nil, &changes); status != http.StatusOK || changes.Total < 2 {
		t.Fatalf("changes = %d %+v", status, changes)
	}

	if status := call("POST", "/enhance/v1/usage/demo", nil, nil); status != http.StatusOK {
		t.Fatalf("demo status = %d", status)
	}
	var demoAgain map[string]any
	if status := call("POST", "/enhance/v1/usage/demo", nil, &demoAgain); status != http.StatusOK || demoAgain["added"].(float64) != 0 {
		t.Fatalf("demo repeat = %d %+v", status, demoAgain)
	}
	var summary struct {
		Requests        int                `json:"requests"`
		Input           *int64             `json:"input"`
		Output          *int64             `json:"output"`
		UnknownRequests int                `json:"unknownRequests"`
		Coverage        map[string]float64 `json:"coverage"`
		Estimate        struct {
			Kind     string   `json:"kind"`
			Amount   *float64 `json:"amount"`
			Currency *string  `json:"currency"`
		} `json:"estimate"`
		Actual struct {
			Kind   string   `json:"kind"`
			Amount *float64 `json:"amount"`
		} `json:"actual"`
		Subscription struct {
			Kind      string  `json:"kind"`
			Remaining *string `json:"remaining"`
		} `json:"subscription"`
	}
	if status := call("GET", "/enhance/v1/usage/summary", nil, &summary); status != http.StatusOK {
		t.Fatalf("summary status = %d", status)
	}
	if summary.Requests == 0 || summary.UnknownRequests == 0 {
		t.Fatalf("summary requests = %+v", summary)
	}
	if summary.Input != nil {
		t.Fatalf("demo has unknown rows; top-level input must stay null, got %d", *summary.Input)
	}
	if summary.Estimate.Kind != "ESTIMATE" || summary.Estimate.Amount == nil || summary.Estimate.Currency == nil {
		t.Fatalf("estimate = %+v", summary.Estimate)
	}
	if summary.Actual.Kind != "UNKNOWN" || summary.Actual.Amount != nil {
		t.Fatalf("actual = %+v", summary.Actual)
	}
	if summary.Subscription.Kind != "SUBSCRIPTION" || summary.Subscription.Remaining != nil {
		t.Fatalf("subscription = %+v", summary.Subscription)
	}

	// Restart against the same file and confirm the ledger and registry survive.
	server.Close()
	db.Close()
	reopened, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	reg2, err := registry.New(reopened)
	if err != nil {
		t.Fatalf("registry reopen: %v", err)
	}
	use2, err := usage.New(reopened)
	if err != nil {
		t.Fatalf("usage reopen: %v", err)
	}
	mux2 := http.NewServeMux()
	reg2.Register(mux2)
	use2.Register(mux2)
	listener2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server2 := &http.Server{Handler: mux2, ReadHeaderTimeout: 5 * time.Second}
	go server2.Serve(listener2)
	defer server2.Close()
	base = "http://" + listener2.Addr().String()
	var restored struct {
		Total int `json:"total"`
	}
	if status := call("GET", "/enhance/v1/models?limit=100", nil, &restored); status != http.StatusOK || restored.Total < 8 {
		t.Fatalf("models after restart = %d %+v", status, restored)
	}
	var restoredSummary struct {
		Requests int `json:"requests"`
	}
	if status := call("GET", "/enhance/v1/usage/summary", nil, &restoredSummary); status != http.StatusOK || restoredSummary.Requests != summary.Requests {
		t.Fatalf("summary after restart = %d %+v", status, restoredSummary)
	}
	fmt.Printf("live check: %d models, %d usage requests\n", restored.Total, restoredSummary.Requests)
}
