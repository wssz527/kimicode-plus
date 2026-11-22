package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The macOS host renews its twelve-hour session from the core's runtime record.
// The path is derived from the data directory and must reach the injected
// bootstrap, because a host that never received it could not renew at all — the
// exact failure this fix exists for.
func TestBootstrapConfigCarriesRuntimePath(t *testing.T) {
	dir := t.TempDir()
	want, err := filepath.Abs(filepath.Join(dir, "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	config, err := bootstrapConfig(AttachOptions{
		CoreURL: "http://127.0.0.1:9", PairingToken: strings.Repeat("t", 48), DataDir: dir,
	}, "1.3.7", "1.3.7", SupportedElectron, "module", "css")
	if err != nil {
		t.Fatal(err)
	}
	if config["runtimePath"] != want {
		t.Fatalf("runtimePath: got %v, want %s", config["runtimePath"], want)
	}
	if _, present := config["windowsMainBridge"]; present {
		t.Fatal("the macOS payload must not claim to be the Windows main bridge")
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["runtimePath"] != want {
		t.Fatalf("runtimePath did not survive the injection payload: %#v", decoded)
	}
}

// A host whose data directory is unknown cannot be given a renewal path, and the
// payload must then omit it rather than carry an empty string the host would try
// to read on every renewal.
func TestBootstrapConfigOmitsRuntimePathWithoutDataDir(t *testing.T) {
	config, err := bootstrapConfig(AttachOptions{
		CoreURL: "http://127.0.0.1:9", PairingToken: strings.Repeat("t", 48),
	}, "1.3.7", "1.3.7", SupportedElectron, "module", "css")
	if err != nil {
		t.Fatal(err)
	}
	if _, present := config["runtimePath"]; present {
		t.Fatalf("runtimePath was sent without a data directory: %#v", config)
	}
	if config["coreURL"] != "http://127.0.0.1:9" || config["pairingToken"] == nil {
		t.Fatalf("the pairing inputs were dropped: %#v", config)
	}
}

// The two options used to be one field, which made a renderer with a runtime path
// look like a Windows main process. They are now independent, and the host reads
// each one for exactly its own purpose.
func TestBootstrapSeparatesRuntimePathFromMainBridge(t *testing.T) {
	if !strings.Contains(bootstrapSource, "if (options.windowsMainBridge && (typeof BrowserWindow") {
		t.Fatal("the Electron interface check no longer follows windowsMainBridge")
	}
	if strings.Contains(bootstrapSource, "options.runtimePath && (typeof BrowserWindow") {
		t.Fatal("the Electron interface check is still tied to runtimePath")
	}
	if !strings.Contains(bootstrapSource, "coreURL: options.windowsMainBridge ? options.coreURL : ''") {
		t.Fatal("the renderer coreURL no longer follows windowsMainBridge")
	}
	if strings.Contains(bootstrapSource, "coreURL: options.runtimePath ? options.coreURL") {
		t.Fatal("the renderer coreURL is still tied to runtimePath")
	}
}

func TestBootstrapConfigCarriesCoreVersion(t *testing.T) {
	withVersion, err := bootstrapConfig(AttachOptions{
		CoreURL: "http://127.0.0.1:9", PairingToken: strings.Repeat("t", 48), CoreVersion: "0.2.7",
	}, "1.3.7", "1.3.7", SupportedElectron, "module", "css")
	if err != nil {
		t.Fatal(err)
	}
	if withVersion["coreVersion"] != "0.2.7" {
		t.Fatalf("coreVersion: got %v, want 0.2.7", withVersion["coreVersion"])
	}
	withoutVersion, err := bootstrapConfig(AttachOptions{
		CoreURL: "http://127.0.0.1:9", PairingToken: strings.Repeat("t", 48),
	}, "1.3.7", "1.3.7", SupportedElectron, "module", "css")
	if err != nil {
		t.Fatal(err)
	}
	if _, present := withoutVersion["coreVersion"]; present {
		t.Fatalf("empty coreVersion should be omitted: %#v", withoutVersion)
	}
}

// Writes the runtime record the way the core does, so other tests read a real
// file rather than a fixture string.
func writeCoreRuntime(t *testing.T, path, coreURL, pairingToken string) {
	t.Helper()
	raw, err := json.Marshal(CoreRuntimeRecord{URL: coreURL, PairingToken: pairingToken})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
