package plugins

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stagedFiles answers the one shape a plugin receives: paths that exist, named
// the way the user dropped them, inside the plugin's own staging root.
func stagedFiles(t *testing.T, value map[string]any) ([]map[string]any, string) {
	t.Helper()
	inner := value["value"].(map[string]any)
	files, _ := inner["files"].([]any)
	out := make([]map[string]any, 0, len(files))
	for _, item := range files {
		out = append(out, item.(map[string]any))
	}
	batch, _ := inner["batch"].(string)
	return out, batch
}

func base64Of(text string) string {
	return base64.StdEncoding.EncodeToString([]byte(text))
}

// Dropped bytes become files the send path already understands, and they land
// under the plugin's own directory rather than a shared temporary directory.
func TestStagedDropBecomesReadableFilesUnderThePlugin(t *testing.T) {
	core := &pluginCoreDouble{}
	manager, token := officialManager(t, core, []string{CapAttachments})
	code, value := callPlugin(t, manager, token, "attachments.stage", map[string]any{
		"files": []any{
			map[string]any{"name": "diagram.png", "data": base64Of("png-bytes")},
			map[string]any{"name": "notes.txt", "data": base64Of("hello")},
		},
	})
	if code != http.StatusOK {
		t.Fatalf("attachments.stage => %d %#v", code, value)
	}
	files, batch := stagedFiles(t, value)
	if len(files) != 2 {
		t.Fatalf("staged %d files, want 2", len(files))
	}
	root := filepath.Dir(batch)
	dataDir := filepath.Clean(manager.DataDir())
	if !strings.HasPrefix(filepath.Clean(root), dataDir+string(filepath.Separator)) {
		t.Fatalf("staging root escaped the plugin data directory: %s", root)
	}
	if strings.Contains(root, filepath.Join("plugins", "test.official")) {
		t.Fatalf("staging root sits inside the install directory: %s", root)
	}
	for _, item := range files {
		path := item["path"].(string)
		if !strings.HasPrefix(path, batch) {
			t.Fatalf("staged file outside its batch: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("staged file is not readable: %v", err)
		}
		if len(data) == 0 {
			t.Fatalf("staged file is empty: %s", path)
		}
		if filepath.Base(path) == "diagram.png" || !strings.HasSuffix(path, ".png") && filepath.Ext(path) == ".png" {
			// The name keeps the extension; the numeric prefix only orders it.
			continue
		}
	}
	first := files[0]["path"].(string)
	second := files[1]["path"].(string)
	// The bytes that reach sessions.send are the staged paths, so the send path
	// needs no second validation rule of its own.
	if code, value := callPlugin(t, manager, token, "attachments.send", map[string]any{
		"id": "s1", "text": "see attached", "paths": []string{first, second},
	}); code != http.StatusOK {
		t.Fatalf("attachments.send => %d %#v", code, value)
	}
	last := core.last()
	if last.action != "sessions.send" {
		t.Fatalf("send did not reach the core: %#v", last)
	}
	sent, _ := last.payload["attachments"].([]any)
	if len(sent) != 2 {
		t.Fatalf("send carried %d attachments: %#v", len(sent), last.payload)
	}
}

// The batch is removed with the send, so the dropped bytes do not stay on disk
// after the message is delivered.
func TestSendingAStagedBatchRemovesIt(t *testing.T) {
	manager, token := officialManager(t, &pluginCoreDouble{}, []string{CapAttachments})
	_, value := callPlugin(t, manager, token, "attachments.stage", map[string]any{
		"files": []any{map[string]any{"name": "a.txt", "data": base64Of("one")}},
	})
	files, batch := stagedFiles(t, value)
	if _, err := os.Stat(files[0]["path"].(string)); err != nil {
		t.Fatalf("staged file missing before send: %v", err)
	}
	if code, sent := callPlugin(t, manager, token, "attachments.send", map[string]any{
		"id": "s1", "text": "hi", "paths": []string{files[0]["path"].(string)}, "batch": batch,
	}); code != http.StatusOK {
		t.Fatalf("send => %d %#v", code, sent)
	}
	if _, err := os.Stat(batch); !os.IsNotExist(err) {
		t.Fatalf("the staged batch survived the send: %v", err)
	}
}

// A failed send still cleans up: the core must not keep bytes a plugin could
// reference again after the message was refused.
func TestFailedSendStillReleasesTheStagedBatch(t *testing.T) {
	core := &failingCore{failOn: "sessions.send"}
	manager, token := officialManager(t, core, []string{CapAttachments})
	_, value := callPlugin(t, manager, token, "attachments.stage", map[string]any{
		"files": []any{map[string]any{"name": "a.txt", "data": base64Of("one")}},
	})
	files, batch := stagedFiles(t, value)
	if code, _ := callPlugin(t, manager, token, "attachments.send", map[string]any{
		"id": "s1", "text": "hi", "paths": []string{files[0]["path"].(string)}, "batch": batch,
	}); code == http.StatusOK {
		t.Fatal("a refused send was reported as success")
	}
	if _, err := os.Stat(batch); !os.IsNotExist(err) {
		t.Fatalf("a failed send left the batch behind: %v", err)
	}
}

// A batch that `stage` returned can be released by passing it straight back:
// the answer carries a path, and refusing it would make the documented flow
// unusable.
func TestReleaseAcceptsTheBatchStageReturned(t *testing.T) {
	manager, token := officialManager(t, &pluginCoreDouble{}, []string{CapAttachments})
	_, value := callPlugin(t, manager, token, "attachments.stage", map[string]any{
		"files": []any{map[string]any{"name": "a.txt", "data": base64Of("one")}},
	})
	_, batch := stagedFiles(t, value)
	if code, released := callPlugin(t, manager, token, "attachments.release", map[string]any{"batch": batch}); code != http.StatusOK {
		t.Fatalf("release of its own batch => %d %#v", code, released)
	}
	if _, err := os.Stat(batch); !os.IsNotExist(err) {
		t.Fatalf("the batch survived its release: %v", err)
	}
	// The bare name works too, because that is what a caller reading the path
	// apart would send.
	_, value = callPlugin(t, manager, token, "attachments.stage", map[string]any{
		"files": []any{map[string]any{"name": "b.txt", "data": base64Of("two")}},
	})
	_, second := stagedFiles(t, value)
	if code, released := callPlugin(t, manager, token, "attachments.release", map[string]any{"batch": filepath.Base(second)}); code != http.StatusOK {
		t.Fatalf("release by bare name => %d %#v", code, released)
	}
	if _, err := os.Stat(second); !os.IsNotExist(err) {
		t.Fatalf("the batch survived a release by name: %v", err)
	}
}

// A plugin names its own path to delete; only a batch name resolves, and only
// inside its own staging root.
func TestReleaseCannotDeleteOutsideTheStagingRoot(t *testing.T) {
	manager, token := officialManager(t, &pluginCoreDouble{}, []string{CapAttachments})
	outside := filepath.Join(manager.pluginsDir(), "test.official", "renderer.js")
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("fixture file missing: %v", err)
	}
	for _, batch := range []string{"../renderer.js", "../../renderer.js", "/etc/hosts"} {
		if code, value := callPlugin(t, manager, token, "attachments.release", map[string]any{"batch": batch}); code != http.StatusOK && code != http.StatusBadRequest {
			t.Fatalf("release %q => %d %#v", batch, code, value)
		}
		if _, err := os.Stat(outside); err != nil {
			t.Fatalf("release %q deleted a file outside the staging root: %v", batch, err)
		}
	}
}

// Oversized and malformed drops are refused before anything is written.
func TestStagingRejectsOversizedAndMalformedDrops(t *testing.T) {
	manager, token := officialManager(t, &pluginCoreDouble{}, []string{CapAttachments})
	empty := map[string]any{"name": "a.txt", "data": base64Of("")}
	cases := []struct {
		name   string
		params map[string]any
		code   int
	}{
		{"no files", map[string]any{}, http.StatusBadRequest},
		{"empty content", map[string]any{"files": []any{empty}}, http.StatusBadRequest},
		{"bad base64", map[string]any{"files": []any{map[string]any{"name": "a.txt", "data": "!!!!"}}}, http.StatusBadRequest},
		{"no name", map[string]any{"files": []any{map[string]any{"name": "", "data": base64Of("x")}}}, http.StatusBadRequest},
		{"not an object", map[string]any{"files": []any{"a.txt"}}, http.StatusBadRequest},
	}
	for _, item := range cases {
		if code, value := callPlugin(t, manager, token, "attachments.stage", item.params); code != item.code {
			t.Fatalf("%s => %d %#v", item.name, code, value)
		}
	}
	tooMany := make([]any, 0, attachmentMax+1)
	for index := 0; index <= attachmentMax; index++ {
		tooMany = append(tooMany, map[string]any{"name": "a.txt", "data": base64Of("x")})
	}
	if code, value := callPlugin(t, manager, token, "attachments.stage", map[string]any{"files": tooMany}); code != http.StatusBadRequest {
		t.Fatalf("more than %d files => %d %#v", attachmentMax, code, value)
	}
	big := base64.StdEncoding.EncodeToString(make([]byte, attachmentFileLimit+1))
	if code, value := callPlugin(t, manager, token, "attachments.stage", map[string]any{
		"files": []any{map[string]any{"name": "big.bin", "data": big}},
	}); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("an oversized file => %d %#v", code, value)
	}
}

// A dropped name carrying separators cannot change where the file is written.
func TestStagedNamesCannotEscapeTheBatch(t *testing.T) {
	manager, token := officialManager(t, &pluginCoreDouble{}, []string{CapAttachments})
	_, value := callPlugin(t, manager, token, "attachments.stage", map[string]any{
		"files": []any{map[string]any{"name": "../../etc/passwd", "data": base64Of("x")}},
	})
	files, batch := stagedFiles(t, value)
	path := files[0]["path"].(string)
	if filepath.Dir(path) != batch {
		t.Fatalf("a traversal name landed outside the batch: %s", path)
	}
	if strings.Contains(path, "..") {
		t.Fatalf("a traversal name survived: %s", path)
	}
}

// The staging actions are gated by attachments like the send action.
func TestStagingRequiresTheAttachmentsCapability(t *testing.T) {
	core := &pluginCoreDouble{}
	manager, token := officialManager(t, core, []string{CapSessionsRead})
	for _, action := range []string{"attachments.stage", "attachments.release"} {
		if code, value := callPlugin(t, manager, token, action, map[string]any{"files": []any{}}); code != http.StatusForbidden {
			t.Fatalf("%s without attachments => %d %#v", action, code, value)
		}
	}
}

// A manager with no data directory cannot stage: a shared temporary directory
// would let one plugin read another plugin's dropped bytes.
func TestStagingNeedsAPluginDataDirectory(t *testing.T) {
	root := t.TempDir()
	manager, err := newManagerAt(t, root)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"plugin.json": []byte(manifestFor("test.official", 2, []string{CapAttachments})),
		"renderer.js": []byte("export function activate(){}\n"),
		"LICENSE":     []byte("MIT\n"),
	}
	digest := ContentDigest(files)
	manager.officialDigests = func() (map[string]string, error) { return map[string]string{"test.official": digest}, nil }
	if err = manager.ReleaseBundled(context.Background(), map[string]map[string][]byte{"test.official": files}); err != nil {
		t.Fatal(err)
	}
	enabled, err := manager.Enable(context.Background(), "test.official")
	if err != nil {
		t.Fatal(err)
	}
	// Only now remove the data root: staging must refuse rather than fall back to
	// a shared temporary directory.
	manager.dataDir = ""
	code, value := callPlugin(t, manager, enabled.Token, "attachments.stage", map[string]any{
		"files": []any{map[string]any{"name": "a.txt", "data": base64Of("x")}},
	})
	if code != http.StatusServiceUnavailable {
		t.Fatalf("a manager without a data dir staged anyway: %d %#v", code, value)
	}
}

// The payload the renderer sends is a JSON object; a bare array must not be
// accepted as a list of files.
func TestStagingRequiresAnObjectPayload(t *testing.T) {
	manager, token := officialManager(t, &pluginCoreDouble{}, []string{CapAttachments})
	code, _ := callPlugin(t, manager, token, "attachments.stage", []any{})
	if code != http.StatusBadRequest {
		t.Fatalf("an array payload => %d", code)
	}
	raw, _ := json.Marshal(map[string]any{"files": "a.txt"})
	if code, _ := callPlugin(t, manager, token, "attachments.stage", json.RawMessage(raw)); code != http.StatusBadRequest {
		t.Fatalf("a non-list files field => %d", code)
	}
}
