package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestPublicCatalogCacheFailureAndImport(t *testing.T) {
	raw, err := os.ReadFile("testdata/public-modelsdev.json")
	if err != nil {
		t.Fatal(err)
	}
	mode := "ok"
	conditional := false
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("unexpected method %s", r.Method)
		}
		if mode == "fail" {
			w.WriteHeader(503)
			return
		}
		if mode == "empty" {
			w.Write([]byte(`{}`))
			return
		}
		if r.Header.Get("If-None-Match") == `"fixture-1"` && mode == "ok" {
			conditional = true
			w.WriteHeader(304)
			return
		}
		w.Header().Set("ETag", `"fixture-1"`)
		w.Header().Set("Last-Modified", "Wed, 11 Nov 2026 00:00:00 GMT")
		w.Write(raw)
	}))
	defer host.Close()
	client := &http.Client{Timeout: time.Second, CheckRedirect: catalogHTTPClient().CheckRedirect}
	s, _ := newTestService(t)
	ctx := context.Background()
	first, err := s.refreshCatalog(ctx, client, host.URL)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != "FETCHED" || first.SHA256 == "" {
		t.Fatalf("status %+v", first)
	}
	second, err := s.refreshCatalog(ctx, client, host.URL)
	if err != nil || second.State != "NOT_MODIFIED" || !conditional || first.RetrievedAt != second.RetrievedAt {
		t.Fatalf("cache failure %+v %v", second, err)
	}
	for _, m := range []string{"fail", "empty"} {
		mode = m
		if _, err = s.refreshCatalog(ctx, client, host.URL); err == nil {
			t.Fatalf("accepted %s", m)
		}
		cached, err := s.cachedCatalog(ctx)
		if err != nil || cached.status.SHA256 != first.SHA256 {
			t.Fatal("failed refresh destroyed old snapshot")
		}
	}
	_, err = s.CreateProvider(ctx, providerInput{ID: "my.gateway", Name: "Local instance", Protocol: "openai", BaseURL: "http://127.0.0.1:11434/v1"})
	if err != nil {
		t.Fatal(err)
	}
	records, err := s.catalogInputs(ctx, "moonshotai", "my.gateway")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := s.Preview(ctx, records)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Import(ctx, records, preview.Revision, "public-modelsdev")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Import(ctx, records, result.Revision, "public-modelsdev")
	if err != nil || again.Revision != result.Revision {
		t.Fatalf("repeat imported %+v %v", again, err)
	}
	model := getModel(t, s, "my.gateway", records[0].ID)
	cap := model.Capabilities["context"]
	if cap.Source != SourceModelsDev || cap.SourceRevision == nil || *cap.SourceRevision != first.SHA256 || cap.SourceURL == nil || *cap.SourceURL != PublicCatalogURL {
		t.Fatalf("provenance lost %+v", cap)
	}
	p, _, err := s.loadProvider(s.db, "my.gateway")
	if err != nil || p.BaseURL != "http://127.0.0.1:11434/v1" {
		t.Fatal("catalog changed instance endpoint")
	}
}

func TestFindPublicModelMatchesEndpointAndModelID(t *testing.T) {
	const fixture = `{
		"exact": {"api":"https://example.com/v1", "models":{"family/model":{"id":"family/model","tool_call":true,"reasoning":false,"modalities":{"input":["text","image"]},"limit":{"output":512}}}},
		"alias": {"api":"https://example.com/v1", "models":{"family/model":{"id":"family/model","tool_call":false}}},
		"wrong-key": {"api":"https://example.org/v1", "models":{"family/model":{"id":"different","tool_call":true}}},
		"openai": {"models":{"test-model":{"id":"test-model","tool_call":true}}}
	}`
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(fixture)) }))
	defer host.Close()
	s, db := newTestService(t)
	ctx := context.Background()
	if _, err := s.refreshCatalog(ctx, host.Client(), host.URL); err != nil {
		t.Fatal(err)
	}
	before := revision(t, s)
	caps, err := s.FindPublicModel(ctx, "exact", "https://EXAMPLE.com:443/v1/", "family/model")
	if err != nil || caps["tools"].Value != true || caps["vision"].Value != true || caps["video"].Value != false || caps["thinking"].Value != false || caps["output"].Value != float64(512) {
		t.Fatalf("exact metadata = %#v, %v", caps, err)
	}
	for _, tc := range []struct{ id, endpoint, model string }{
		{"custom", "https://example.com/v1", "family/model"},
		{"exact", "https://example.com/v2", "family/model"},
		{"exact", "https://example.com/v1", "MODEL"},
		{"wrong-key", "https://example.org/v1", "family/model"},
		{"exact", "https://example.com/v1?credential=value", "family/model"},
	} {
		if caps, err = s.FindPublicModel(ctx, tc.id, tc.endpoint, tc.model); err != nil || caps != nil {
			t.Fatalf("accepted %v: %#v, %v", tc, caps, err)
		}
	}
	caps, err = s.FindPublicModel(ctx, "local-openai", "https://api.openai.com/v1", "test-model")
	if err != nil || caps["tools"].Value != true {
		t.Fatalf("exact preset endpoint without catalog api = %#v, %v", caps, err)
	}
	batch, err := s.FindPublicModels(ctx, "exact", "https://EXAMPLE.com:443/v1/", []string{"family/model", "missing-model", "family/model"})
	if err != nil || len(batch) != 1 || batch["family/model"]["output"].Value != float64(512) || batch["family/model"]["vision"].Value != true {
		t.Fatalf("batch lookup = %#v, %v", batch, err)
	}
	var providers int
	if err := db.QueryRow(`SELECT COUNT(*) FROM registry_providers`).Scan(&providers); err != nil || providers != 0 || revision(t, s) != before {
		t.Fatalf("catalog lookup changed registry config: %d, %v", providers, err)
	}
}

func TestPublicCatalogRedirectAndMalformedBody(t *testing.T) {
	targetHit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetHit = true }))
	defer target.Close()
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer host.Close()
	s, _ := newTestService(t)
	client := &http.Client{Timeout: time.Second, CheckRedirect: catalogHTTPClient().CheckRedirect}
	if _, err := s.refreshCatalog(context.Background(), client, host.URL); err == nil || targetHit {
		t.Fatal("redirect escaped fixed source")
	}
}

func TestLivePublicCatalog(t *testing.T) {
	if os.Getenv("EKD_PUBLIC_FETCH") != "1" {
		t.Skip("explicit real public read opt-in")
	}
	s, _ := newTestService(t)
	status, err := s.RefreshCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(status)
	t.Log(string(raw))
	if status.State != "FETCHED" || status.Bytes == 0 || len(status.Providers) == 0 {
		t.Fatal("empty live result")
	}
	records, err := s.catalogInputsAfterCreatingTestProvider(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Preview(context.Background(), records)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Import(context.Background(), records, p.Revision, "public-modelsdev")
	if err != nil || out.Imported == 0 {
		t.Fatalf("live import %+v %v", out, err)
	}
	t.Logf("real public metadata imported %d models; zero inference calls", out.Imported)
	second, err := s.RefreshCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("conditional fetch state=%s sha256=%s", second.State, second.SHA256)
}

func (s *Service) catalogInputsAfterCreatingTestProvider(ctx context.Context) ([]modelInput, error) {
	if _, err := s.CreateProvider(ctx, providerInput{ID: "public-test", Name: "Public metadata only", Protocol: "openai", BaseURL: "https://example.com/v1"}); err != nil {
		return nil, err
	}
	return s.catalogInputs(ctx, "moonshotai", "public-test")
}
