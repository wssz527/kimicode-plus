package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pluginCoreDouble is a Core that records what reached it. It is deliberately not
// a NativeService: these tests prove the plugin boundary, while the adapter tests
// in package server prove the native hop.
type pluginCoreDouble struct {
	calls []call
}

type call struct {
	action  string
	payload map[string]any
}

func (c *pluginCoreDouble) Action(_ context.Context, action string, payload json.RawMessage) (any, error) {
	values := map[string]any{}
	_ = json.Unmarshal(payload, &values)
	c.calls = append(c.calls, call{action: action, payload: values})
	switch action {
	case "sessions.history":
		return map[string]any{
			"sessionId": values["id"], "agentId": values["agentId"],
			"messages": []any{}, "approvals": []any{}, "hasMore": true, "nextBefore": 12, "pageSize": values["size"],
		}, nil
	case "models.list":
		return map[string]any{"items": []map[string]string{{"id": "kimi-k2", "name": "Kimi K2"}}, "total": 1}, nil
	case "session.reply":
		return map[string]any{"ok": true}, nil
	case "session.abort":
		return map[string]any{"aborted": true}, nil
	case "handoff":
		return map[string]any{"sessionId": "new-session"}, nil
	default:
		return map[string]any{"action": action}, nil
	}
}

func (c *pluginCoreDouble) Events() EventSource { return nil }

func (c *pluginCoreDouble) last() call {
	if len(c.calls) == 0 {
		return call{}
	}
	return c.calls[len(c.calls)-1]
}

func officialManager(t *testing.T, core Core, capabilities []string) (*Manager, string) {
	t.Helper()
	root := t.TempDir()
	manager, err := newManagerAt(t, root)
	if err != nil {
		t.Fatal(err)
	}
	manager.UseCore(core)
	manager.platform = "darwin"
	files := map[string][]byte{
		"plugin.json": []byte(manifestFor("test.official", 2, capabilities)),
		"renderer.js": []byte("export function activate(){}\n"),
		"LICENSE":     []byte("MIT\n"),
	}
	digest := ContentDigest(files)
	manager.officialDigests = func() (map[string]string, error) { return map[string]string{"test.official": digest}, nil }
	if err := manager.ReleaseBundled(context.Background(), map[string]map[string][]byte{"test.official": files}); err != nil {
		t.Fatal(err)
	}
	enabled, err := manager.Enable(context.Background(), "test.official")
	if err != nil {
		t.Fatal(err)
	}
	return manager, enabled.Token
}

func TestPrivilegedActionsReachTheCoreWithTheRightShape(t *testing.T) {
	core := &pluginCoreDouble{}
	manager, token := officialManager(t, core, []string{
		CapSessionsApprove, CapSessionsAbort, CapSessionsHistory, CapHandoff, CapModelsReadV2,
	})

	// Approval is the native reply action; the plugin never names it directly.
	if code, value := callPlugin(t, manager, token, "sessions.approve", map[string]any{"id": "s1", "interactionId": "i1", "value": map[string]any{"decision": "approved"}}); code != http.StatusOK {
		t.Fatalf("sessions.approve => %d %#v", code, value)
	}
	if last := core.last(); last.action != "session.reply" || last.payload["interactionId"] != "i1" {
		t.Fatalf("approval did not reach session.reply: %#v", last)
	}
	if code, _ := callPlugin(t, manager, token, "sessions.abort", map[string]any{"id": "s1"}); code != http.StatusOK {
		t.Fatal("sessions.abort failed")
	}
	if last := core.last(); last.action != "session.abort" || last.payload["id"] != "s1" {
		t.Fatalf("abort did not reach session.abort: %#v", last)
	}
	code, value := callPlugin(t, manager, token, "sessions.history", map[string]any{"id": "s1", "beforeOrdinal": 40, "pageSize": 20, "agentId": "main"})
	if code != http.StatusOK {
		t.Fatalf("sessions.history => %d %#v", code, value)
	}
	if last := core.last(); last.action != "sessions.history" || last.payload["before"] != float64(40) || last.payload["size"] != float64(20) {
		t.Fatalf("history cursor was not forwarded: %#v", last)
	}
	inner := value["value"].(map[string]any)
	if inner["hasMore"] != true {
		t.Fatalf("history lost hasMore: %#v", inner)
	}
	if code, value := callPlugin(t, manager, token, "handoff.commit", map[string]any{"text": "plan text", "mode": "plan"}); code != http.StatusOK {
		t.Fatalf("handoff.commit => %d %#v", code, value)
	}
	last := core.last()
	if last.action != "handoff" || last.payload["mode"] != "plan" || last.payload["text"] != "plan text" {
		t.Fatalf("handoff did not reach the native action: %#v", last)
	}
	if last.payload["targetId"] != nil {
		t.Fatalf("new-session handoff carried a target: %#v", last.payload)
	}
	if code, value := callPlugin(t, manager, token, "models.list", nil); code != http.StatusOK || !strings.Contains(mustString(value), "kimi-k2") {
		t.Fatalf("models.list => %d %#v", code, value)
	}
}

// A bundled plugin that holds session capabilities but fails its official
// content check cannot reach the core.
func TestBundledPluginWithSameIDAndWrongDigestCannotReachPrivilegedActions(t *testing.T) {
	core := &pluginCoreDouble{}
	manager, _ := testManager(t, func() bool { return false })
	manager.UseCore(core)
	// Same id as the official plugin, different content: this is the case the
	// allowlist exists for.
	source := writePluginDir(t, t.TempDir(), manifestFor("test.official", 2, []string{CapSessionsHistory, CapHandoff}), nil)
	files := map[string][]byte{}
	err := filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return walkErr
		}
		relative, relErr := filepath.Rel(source, path)
		if relErr != nil {
			return relErr
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		files[filepath.ToSlash(relative)] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The allowlist names the same id with a different digest, which is exactly
	// the tampered-bundle case.
	manager.officialDigests = func() (map[string]string, error) {
		return map[string]string{"test.official": ContentDigest(files) + "00"}, nil
	}
	if err := manager.ReleaseBundled(context.Background(), map[string]map[string][]byte{"test.official": files}); err != nil {
		t.Fatal(err)
	}
	grants, err := ParseCapabilities([]string{CapSessionsHistory})
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.entries["test.official"].grants = grants
	manager.mu.Unlock()
	enabled, err := manager.Enable(context.Background(), "test.official")
	if err != nil {
		t.Fatal(err)
	}
	code, value := callPlugin(t, manager, enabled.Token, "sessions.history", map[string]any{"id": "s1"})
	if code != http.StatusForbidden || !strings.Contains(mustString(value), "PLUGIN_PRIVILEGE_DENIED") {
		t.Fatalf("third-party history => %d %#v", code, value)
	}
	if len(core.calls) != 0 {
		t.Fatalf("third-party request reached the core: %#v", core.calls)
	}
}

// Changing one file of an official install must revoke every privileged action
// without changing the stored grant.
func TestOfficialDigestMismatchRevokesActions(t *testing.T) {
	core := &pluginCoreDouble{}
	manager, token := officialManager(t, core, []string{CapSessionsHistory})
	manager.mu.Lock()
	dir := manager.entries["test.official"].dir
	manager.mu.Unlock()
	if err := os.WriteFile(filepath.Join(dir, "LICENSE"), []byte("MIT\nchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, value := callPlugin(t, manager, token, "sessions.history", map[string]any{"id": "s1"})
	if code != http.StatusForbidden || !strings.Contains(mustString(value), "PLUGIN_PRIVILEGE_DENIED") {
		t.Fatalf("tampered plugin acted: %d %#v", code, value)
	}
	if len(core.calls) != 0 {
		t.Fatalf("tampered plugin reached the core: %#v", core.calls)
	}
}

// Every event scope and command must obey the same gate: a plugin cannot get a
// planner event or call a command by declaring a capability it did not receive.
func TestEventScopesAndCommandsCannotBypassTheGate(t *testing.T) {
	manager, token := officialManager(t, &pluginCoreDouble{}, []string{CapSessionsHistory})
	for _, scope := range []string{"planner:p1", "session:s1"} {
		code, value := callPlugin(t, manager, token, "events.poll", map[string]any{"scopes": []string{scope}, "after": 0})
		if code != http.StatusOK {
			t.Fatalf("events.poll %s => %d %#v", scope, code, value)
		}
		if events, _ := value["value"].(map[string]any)["events"].([]any); len(events) != 0 {
			t.Fatalf("%s delivered events without a grant: %#v", scope, value)
		}
	}
	code, value := callPlugin(t, manager, token, "commands.register", map[string]any{"name": "multiSession.open"})
	if code != http.StatusForbidden || !strings.Contains(mustString(value), "PLUGIN_CAPABILITY_DENIED") {
		t.Fatalf("commands.register without commands => %d %#v", code, value)
	}
}

// The runtime identity of a plugin whose files changed must drop the v2 names,
// because the renderer builds its own surface from that list.
func TestRuntimeIdentityHidesPrivilegedAfterTamper(t *testing.T) {
	manager, token := officialManager(t, &pluginCoreDouble{}, []string{CapUIEntry, CapSessionsHistory})
	_, value := callPlugin(t, manager, token, "runtime.info", nil)
	inner := value["value"].(map[string]any)
	if !containsAny(inner["capabilities"], CapUIEntry) {
		t.Fatalf("identity lost the granted capability: %#v", inner)
	}
	manager.mu.Lock()
	dir := manager.entries["test.official"].dir
	manager.mu.Unlock()
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifestFor("test.official", 2, []string{CapUIEntry})), 0o600); err != nil {
		t.Fatal(err)
	}
	_, value = callPlugin(t, manager, token, "runtime.info", nil)
	inner = value["value"].(map[string]any)
	if containsAny(inner["capabilities"], CapUIEntry) || containsAny(inner["capabilities"], CapSessionsHistory) {
		t.Fatalf("tampered identity still advertises v2 capabilities: %#v", inner)
	}
}

func containsAny(list any, value string) bool {
	items, _ := list.([]any)
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

// The v2 capability names must not be reachable from a v1 manifest, and a v2
// manifest must still be able to declare the v1 capabilities it needs.
func TestManifestVersionSplit(t *testing.T) {
	if _, err := ParseManifest([]byte(manifestFor("test.v1", 1, []string{CapUIEntry}))); err == nil {
		t.Fatal("v1 manifest declared a v2 capability")
	}
	if _, err := ParseManifest([]byte(manifestFor("test.v2", 2, []string{CapUIEntry, CapStorage}))); err != nil {
		t.Fatalf("v2 manifest was refused: %v", err)
	}
	// A plugin manifest written before v2 keeps working unchanged.
	if _, err := ParseManifest([]byte(testManifest)); err != nil {
		t.Fatalf("v1 manifest stopped parsing: %v", err)
	}
}

// The planner owner is the plugin the token resolved to. A plugin that puts a
// different owner in its own payload cannot adopt another plugin's threads.
func TestPlannerOwnerIsRewrittenFromTheToken(t *testing.T) {
	core := &pluginCoreDouble{}
	manager, token := officialManager(t, core, []string{CapPlannerRun})
	code, value := callPlugin(t, manager, token, "planner.create", map[string]any{
		"model": "synthetic", "options": map[string]any{}, "pluginOwner": "kimicode-plus.victim",
	})
	if code != http.StatusOK {
		t.Fatalf("planner.create => %d %#v", code, value)
	}
	last := core.last()
	if last.action != "planner.service.planner.create" {
		t.Fatalf("planner.create reached %q", last.action)
	}
	if owner := last.payload["pluginOwner"]; owner != "test.official" {
		t.Fatalf("planner owner forwarded as %v", owner)
	}
	// Read-only discovery carries no owner at all: it creates nothing.
	if code, _ := callPlugin(t, manager, token, "planner.threads", nil); code != http.StatusOK {
		t.Fatal("planner.threads failed")
	}
	if _, present := core.last().payload["pluginOwner"]; present {
		t.Fatal("planner.threads carried a plugin owner")
	}
}
