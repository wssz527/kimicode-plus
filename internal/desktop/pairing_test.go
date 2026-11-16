package desktop

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"enhancedkimi/internal/security"
)

func TestPairingIsolatedCapabilityBridge(t *testing.T) {
	code := security.Random()
	var handler http.Handler
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	defer server.Close()
	handler = NewIsolatedTestHost(server.URL, "isolated-test-host", "test-1", code)
	options := PairingOptions{Endpoint: server.URL, ExpectedHostID: "isolated-test-host", ExpectedVersion: "test-1", PairingCode: code}
	if _, err := PairIsolatedHost(context.Background(), options); err == nil {
		t.Fatal("unapproved host contacted")
	}
	options.ApprovedIsolatedTestHost = true
	client, err := PairIsolatedHost(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	report, err := client.Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !report.Synthetic || report.Capabilities["submit"] != "UNAVAILABLE" || report.Capabilities["metadata"] != "AVAILABLE" {
		t.Fatalf("false capability %+v", report)
	}
	t.Logf("protocol=%d host=%s version=%s synthetic=%v scope=%v; no native contact or inference", report.Protocol, report.HostID, report.HostVersion, report.Synthetic, report.Scopes)
	options.ExpectedHostID = "different-host"
	if _, err := PairIsolatedHost(context.Background(), options); err == nil {
		t.Fatal("host substitution accepted")
	}
	options.ExpectedHostID = "isolated-test-host"
	options.ExpectedVersion = "incompatible"
	if _, err := PairIsolatedHost(context.Background(), options); err == nil {
		t.Fatal("version mismatch accepted")
	}
	req, _ := http.NewRequest("GET", server.URL+"/ekd/adapter/v1/capabilities", nil)
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("Host check missing")
	}
}
