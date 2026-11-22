package credentials

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestKeychainAccountIsScopedToDataDir(t *testing.T) {
	if got := keychainAccount(""); got != "default" {
		t.Fatalf("empty data dir account %q", got)
	}
	primary := keychainAccount("/Users/example/.kimi-code")
	secondary := keychainAccount("/Users/example/.kimi-code-secondary")
	if primary == secondary {
		t.Fatal("different data directories share a keychain account")
	}
	if !strings.HasPrefix(primary, "vault-") || len(primary) != len("vault-")+16 {
		t.Fatalf("account %q is not a scoped vault name", primary)
	}
	for _, r := range strings.TrimPrefix(primary, "vault-") {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("account %q is not hex", primary)
		}
	}
	for _, alias := range []string{
		"/Users/example/.kimi-code/",
		"/Users/example/./.kimi-code",
		"/Users/example/x/../.kimi-code",
	} {
		if got := keychainAccount(alias); got != primary {
			t.Fatalf("equivalent path %q maps to %q, want %q", alias, got, primary)
		}
	}
}

func TestKeyStoreSecretEncodingRoundTrips(t *testing.T) {
	secrets := []string{
		"{{SECRET_8q1zg3ce}}",
		"manual password with spaces",
		"中文密码 \"quoted\" \\backslash",
		"line\nbreak",
		strings.Repeat("x", 1024),
	}
	for _, secret := range secrets {
		encoded := encodeKeyStoreSecret(secret)
		if !strings.HasPrefix(encoded, osKeyStorePrefix) {
			t.Fatalf("encoded value %q is not versioned", encoded)
		}
		for _, r := range encoded {
			if r < 0x20 || r > 0x7e {
				t.Fatalf("encoded value %q is not printable ASCII", encoded)
			}
		}
		decoded, err := decodeKeyStoreSecret(encoded)
		if err != nil || decoded != secret {
			t.Fatalf("round trip %q: %q %v", secret, decoded, err)
		}
	}
	if got, err := decodeKeyStoreSecret("plain-manual-value"); err != nil || got != "plain-manual-value" {
		t.Fatalf("raw value rejected: %q %v", got, err)
	}
	if _, err := decodeKeyStoreSecret(osKeyStorePrefix + "!!!"); err == nil {
		t.Fatal("malformed encoded value accepted")
	}
}

func TestNewOSKeyStoreSelectsPlatformBackend(t *testing.T) {
	store := NewOSKeyStore(t.TempDir())
	switch runtime.GOOS {
	case "darwin":
		if !store.Available() || store.BackendName() != "MACOS_KEYCHAIN" {
			t.Fatalf("darwin key store: available=%v backend=%q", store.Available(), store.BackendName())
		}
	case "windows":
		if !store.Available() || store.BackendName() != "WINDOWS_DPAPI" {
			t.Fatalf("windows key store: available=%v backend=%q", store.Available(), store.BackendName())
		}
	default:
		if store.Available() || store.BackendName() != "NONE" {
			t.Fatalf("other key store: available=%v backend=%q", store.Available(), store.BackendName())
		}
		ctx := context.Background()
		if secret, found, err := store.Load(ctx); err != nil || found || secret != "" {
			t.Fatalf("other key store load: %q %v %v", secret, found, err)
		}
		if err := store.Save(ctx, "secret"); err == nil {
			t.Fatal("unavailable key store accepted a secret")
		}
		if err := store.Delete(ctx); err != nil {
			t.Fatalf("unavailable key store delete: %v", err)
		}
	}
}

// The macOS Keychain round trip is kept out of the default suite: it writes to
// the user's real login keychain (under a data-dir scoped account) and needs an
// interactive session.
func TestDarwinKeyStoreRoundTrip(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS Keychain is darwin-only")
	}
	if os.Getenv("EKD_TEST_OS_KEYCHAIN") != "1" {
		t.Skip("set EKD_TEST_OS_KEYCHAIN=1 to exercise the real Keychain")
	}
	ctx := context.Background()
	store := NewOSKeyStore(t.TempDir())
	if !store.Available() {
		t.Skip("security CLI is not available")
	}
	t.Cleanup(func() { _ = store.Delete(ctx) })

	if secret, found, err := store.Load(ctx); err != nil || found || secret != "" {
		t.Fatalf("fresh key store: %q %v %v", secret, found, err)
	}
	for _, secret := range []string{"{{SECRET_8q1zg3ce}}", "中文密码 with spaces \"quotes\""} {
		if err := store.Save(ctx, secret); err != nil {
			t.Fatalf("save %q: %v", secret, err)
		}
		got, found, err := store.Load(ctx)
		if err != nil || !found || got != secret {
			t.Fatalf("load after save: %q %v %v (want %q)", got, found, err, secret)
		}
	}
	if err := store.Delete(ctx); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, found, err := store.Load(ctx); err != nil || found {
		t.Fatalf("deleted key still loads: %v %v", found, err)
	}
	if err := store.Delete(ctx); err != nil {
		t.Fatalf("deleting a missing item: %v", err)
	}
}
