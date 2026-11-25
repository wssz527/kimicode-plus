package updater

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSignedManifestBindsArchiveAcrossDownloadSources(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	const archive = "enhanced-kimi-1.2.3-darwin-arm64.tar.gz"
	digest := strings.Repeat("a", 64)
	unsigned := fmt.Appendf(nil, `[{"version":"1.2.3","archive":%q,"target":"darwin-arm64","sha256":%q,"releaseNotes":"更新 · <test>"}]`, archive, digest)
	keyID := "test-key"
	signed, err := SignReleaseManifest(unsigned, keyID, private)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]ed25519.PublicKey{keyID: public}
	m := New(Options{CurrentVersion: "1.2.2", GOOS: "darwin", GOARCH: "arm64", ManifestPublicKeys: keys})
	t.Cleanup(m.Close)
	published, err := m.releaseFromManifest(signed)
	if err != nil {
		t.Fatal(err)
	}
	m.archive, m.checksums = m.selectAssets(published.Assets, published.Tag)
	if m.archive.signedSHA256 != digest {
		t.Fatalf("signed digest = %q", m.archive.signedSHA256)
	}
	m.releaseSources = []string{"https://mirror.example", "https://upstream.example"}
	for _, candidate := range m.downloadCandidates(published.Tag) {
		if candidate.archive.signedSHA256 != digest {
			t.Fatalf("download lost signed digest: %+v", candidate)
		}
	}
	for _, tc := range []struct {
		name    string
		data    []byte
		require bool
		valid   bool
	}{
		{"legacy", unsigned, false, true},
		{"legacy strict", unsigned, true, false},
		{"valid", signed, true, true},
		{"tampered digest", []byte(strings.ReplaceAll(string(signed), digest, strings.Repeat("b", 64))), false, false},
		{"unknown key", []byte(strings.ReplaceAll(string(signed), keyID, "untrusted")), false, false},
		{"null signature", []byte(`[{"version":"1.2.3","signature":null}]`), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := verifyReleaseManifest(tc.data, keys, tc.require)
			if (err == nil) != tc.valid {
				t.Fatalf("verify = %v, want valid=%v", err, tc.valid)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", strings.Repeat("c", 64), archive)
	}))
	t.Cleanup(server.Close)
	m.options.AllowInsecureHTTP = true
	_, err = m.downloadChecksum(context.Background(), downloadCandidate{
		archive: m.archive, checksums: asset{URL: server.URL},
	})
	if err == nil || !strings.Contains(err.Error(), "签名发布清单") {
		t.Fatalf("untrusted checksum = %v", err)
	}
}
