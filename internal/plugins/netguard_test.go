package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"enhancedkimi/internal/credentials"
)

// --- host syntax -----------------------------------------------------------

// TestNetHostRejectsNonCanonicalIPForms pins the boundary the grant grammar
// draws: only a canonical literal is an address. Anything that merely looks like
// one is refused rather than handed to the resolver, because "0x7f000001",
// "2130706433" and "127.1" all reach loopback while reading like names.
func TestNetHostRejectsNonCanonicalIPForms(t *testing.T) {
	refused := []string{
		"net:0x7f000001", // hexadecimal IPv4
		"net:2130706433", // decimal IPv4
		"net:127.1",      // short form
		"net:0177.0.0.1", // octal octet
		"net:127.0.0.1.", // trailing dot
		"net:metadata",   // single label that is not localhost
		"net:*",          // bare wildcard
		"net:*.127.0.0.1",
		"net:127.0.0.1:8080",
		"net:http://example.com",
	}
	for _, entry := range refused {
		if _, err := ParseCapabilities([]string{entry}); err == nil {
			t.Fatalf("%s was accepted as a host grant", entry)
		}
	}

	accepted := map[string]string{
		"net:127.0.0.1":     "127.0.0.1",
		"net:localhost":     "localhost",
		"net:::1":           "::1",
		"net:example.com":   "example.com",
		"net:*.example.com": "*.example.com",
	}
	for entry, want := range accepted {
		grants, err := ParseCapabilities([]string{entry})
		if err != nil {
			t.Fatalf("%s should be accepted: %v", entry, err)
		}
		if hosts := grants.Hosts(); len(hosts) != 1 || hosts[0] != want {
			t.Fatalf("%s parsed to %#v, want %q", entry, hosts, want)
		}
	}
}

// TestNetHostGrantIsExact is the review's concrete claim: a grant for
// "localhost" must not authorize "127.0.0.1". Matching is exact, so it does not.
func TestNetHostGrantIsExact(t *testing.T) {
	local, err := ParseCapabilities([]string{"net:localhost"})
	if err != nil {
		t.Fatal(err)
	}
	if local.AllowsHost("127.0.0.1") {
		t.Fatal("net:localhost authorized 127.0.0.1")
	}
	if !local.AllowsHost("localhost") {
		t.Fatal("net:localhost did not authorize localhost")
	}

	loopback, err := ParseCapabilities([]string{"net:127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if !loopback.AllowsHost("127.0.0.1") {
		t.Fatal("net:127.0.0.1 did not authorize its own literal")
	}
	if loopback.AllowsHost("localhost") {
		t.Fatal("net:127.0.0.1 authorized the name localhost")
	}
}

// --- address policy --------------------------------------------------------

// TestBlockedAddressPolicy covers the answer an approved name is not allowed to
// turn into: the machine itself, the local network, and the metadata endpoints
// that live in them.
func TestBlockedAddressPolicy(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "::1", "10.1.2.3", "172.16.5.4", "192.168.1.1",
		"169.254.169.254", "100.64.0.1", "0.0.0.0", "fc00::1", "fe80::1", "255.255.255.255",
	}
	for _, address := range blocked {
		ip := net.ParseIP(address)
		if ip == nil {
			t.Fatalf("%s is not an IP", address)
		}
		if !isBlockedAddress(ip) {
			t.Fatalf("%s was treated as a public address", address)
		}
	}
	for _, address := range []string{"1.1.1.1", "2606:4700:4700::1111", "93.184.216.34"} {
		if isBlockedAddress(net.ParseIP(address)) {
			t.Fatalf("%s was treated as private", address)
		}
	}
}

func ipLookup(addresses ...string) func(context.Context, string) ([]net.IPAddr, error) {
	return func(context.Context, string) ([]net.IPAddr, error) {
		out := make([]net.IPAddr, 0, len(addresses))
		for _, address := range addresses {
			out = append(out, net.IPAddr{IP: net.ParseIP(address)})
		}
		return out, nil
	}
}

// TestResolveApprovedAddrRejectsPrivateAnswers is the rebinding shape: a name the
// user approved, whose current answer points at the machine or the local network.
func TestResolveApprovedAddrRejectsPrivateAnswers(t *testing.T) {
	ctx := context.Background()
	for _, address := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "::1"} {
		_, err := resolveApprovedAddr(ctx, ipLookup(address), "public.example")
		var pluginErr *Error
		if !errors.As(err, &pluginErr) || pluginErr.Code != "PLUGIN_NET_HOST_DENIED" {
			t.Fatalf("%s answer refused with %v", address, err)
		}
	}

	// A mixed answer is refused as a whole: taking the first address would let
	// the answer order decide whether the request is safe.
	if _, err := resolveApprovedAddr(ctx, ipLookup("93.184.216.34", "127.0.0.1"), "mixed.example"); err == nil {
		t.Fatal("a mixed public/private answer was approved")
	}

	// A public answer is approved and the return value is the literal to dial.
	addr, err := resolveApprovedAddr(ctx, ipLookup("93.184.216.34"), "public.example")
	if err != nil {
		t.Fatalf("a public answer was refused: %v", err)
	}
	if addr.String() != "93.184.216.34" {
		t.Fatalf("approved %s", addr)
	}
}

// TestResolveApprovedAddrHonorsExplicitLiteral is the documented exception: the
// user granting `net:127.0.0.1` is the decision that allows a local target. A
// name resolving to the same address is still refused above.
func TestResolveApprovedAddrHonorsExplicitLiteral(t *testing.T) {
	ctx := context.Background()
	neverCalled := func(context.Context, string) ([]net.IPAddr, error) {
		t.Fatal("an IP literal was resolved through DNS")
		return nil, nil
	}
	for literal, want := range map[string]string{"127.0.0.1": "127.0.0.1", "::1": "::1"} {
		addr, err := resolveApprovedAddr(ctx, neverCalled, literal)
		if err != nil {
			t.Fatalf("explicit literal %s refused: %v", literal, err)
		}
		if addr.String() != want {
			t.Fatalf("approved %s for %s", addr, literal)
		}
	}
}

func TestResolveApprovedAddrReportsDNSFailure(t *testing.T) {
	failing := func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("no such host") }
	_, err := resolveApprovedAddr(context.Background(), failing, "missing.example")
	var pluginErr *Error
	if !errors.As(err, &pluginErr) || pluginErr.Code != "PLUGIN_NET_DNS_FAILED" {
		t.Fatalf("wrong error for a failed lookup: %v", err)
	}
	if _, err := resolveApprovedAddr(context.Background(), ipLookup(), "empty.example"); err == nil {
		t.Fatal("an empty answer was approved")
	}
}

// --- proxy integration -----------------------------------------------------

// TestProxyDialsTheApprovedAddress proves the request reaches the server the
// guard approved, not a second resolution of the name. The grant names the
// literal, so the loopback listener is reachable; the same address reached
// through an ungranted name is not.
func TestProxyDialsTheApprovedAddress(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("the server saw an injected credential: %q", got)
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer server.Close()

	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(t)
	request := httptest.NewRequest("POST", "/enhance/v1/plugin-runtime/net.fetch", nil)

	grants, err := ParseCapabilities([]string{"net:127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	params := json.RawMessage(`{"url":"http://127.0.0.1:` + port + `/v1"}`)
	value, err := manager.proxy(context.Background(), "test.sample", grants, noVault(), "net.fetch", request, params)
	if err != nil {
		t.Fatalf("explicit loopback grant was refused: %v", err)
	}
	result, ok := value.(proxyResponse)
	if !ok {
		t.Fatalf("unexpected result type %T", value)
	}
	if result.Status != 200 || !strings.Contains(result.Body, "ok") {
		t.Fatalf("unexpected response: %#v", result)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("server saw %d requests", hits)
	}

	// The same address through the name localhost is refused: the grant is for the
	// literal, not for every spelling of it.
	nameParams := json.RawMessage(`{"url":"http://localhost:` + port + `/v1"}`)
	if _, err := manager.proxy(context.Background(), "test.sample", grants, noVault(), "net.fetch", request, nameParams); err == nil {
		t.Fatal("an ungranted name reached the server")
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("refused request still reached the server: %d hits", hits)
	}
}

// TestProxyRefusesUngrantedIP keeps the deny path honest: a literal the user did
// not approve is refused before any connection is attempted.
func TestProxyRefusesUngrantedIP(t *testing.T) {
	manager := newTestManager(t)
	grants, err := ParseCapabilities([]string{"net:example.com"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/enhance/v1/plugin-runtime/net.fetch", nil)
	for _, target := range []string{
		"http://127.0.0.1:1/v1",
		"http://169.254.169.254/latest/meta-data",
		"http://10.0.0.5/admin",
	} {
		params := json.RawMessage(`{"url":"` + target + `"}`)
		_, err := manager.proxy(context.Background(), "test.sample", grants, noVault(), "net.fetch", request, params)
		var pluginErr *Error
		if !errors.As(err, &pluginErr) || pluginErr.Code != "PLUGIN_CAPABILITY_DENIED" {
			t.Fatalf("%s refused with %v", target, err)
		}
	}
}

// TestProxyRefusesNameResolvingToLoopback is the end-to-end rebinding case: the
// name is granted, but the answer points at the machine, so no connection is
// made and the injected credential never leaves the core.
func TestProxyRefusesNameResolvingToLoopback(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer server.Close()

	manager := newTestManager(t)
	grants, err := ParseCapabilities([]string{"net:rebind.example"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/enhance/v1/plugin-runtime/net.fetch", nil)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	params := json.RawMessage(`{"url":"http://rebind.example:` + port + `/v1"}`)
	_, err = manager.proxy(context.Background(), "test.sample", grants, noVault(), "net.fetch", request, params)
	if err == nil {
		t.Fatal("a name resolving to loopback was allowed through")
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("refused request still reached the server: %d hits", hits)
	}
}

// newTestManager is the smallest manager the proxy path needs: a real database,
// a data directory and no core, because net.fetch resolves the credential from
// the vault it is handed rather than through a plugin action.
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	manager, _ := testManager(t, func() bool { return false })
	return manager
}

func newTestManagerWithResolver(t *testing.T, resolver *net.Resolver) *Manager {
	t.Helper()
	root := t.TempDir()
	manager, err := newManagerAt(t, root, WithResolver(resolver))
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

// noVault is the nil vault the net.fetch tests pass: none of them inject a
// credential, so the proxy never reads it.
func noVault() *credentials.Vault { return nil }
