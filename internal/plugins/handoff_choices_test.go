package plugins

import (
	"net/http"
	"testing"
)

// A handoff that creates a session carries the directory, model and title the
// plugin chose, because that is the only moment they can apply.
func TestHandoffCommitCarriesNewSessionChoices(t *testing.T) {
	core := &pluginCoreDouble{}
	manager, token := officialManager(t, core, []string{CapHandoff})
	code, value := callPlugin(t, manager, token, "handoff.commit", map[string]any{
		"text": "carry on", "mode": "plan", "cwd": "/tmp/work", "model": "kimi-k2", "title": "继续实现",
	})
	if code != http.StatusOK {
		t.Fatalf("handoff.commit => %d %#v", code, value)
	}
	last := core.last()
	if last.action != "handoff" {
		t.Fatalf("handoff did not reach the native action: %#v", last)
	}
	if last.payload["cwd"] != "/tmp/work" || last.payload["model"] != "kimi-k2" || last.payload["title"] != "继续实现" {
		t.Fatalf("new-session choices were dropped: %#v", last.payload)
	}
}

// A handoff into an existing session never carries them: that session keeps its
// own directory and model.
func TestHandoffCommitToAnExistingSessionIgnoresNewSessionChoices(t *testing.T) {
	core := &pluginCoreDouble{}
	manager, token := officialManager(t, core, []string{CapHandoff})
	code, value := callPlugin(t, manager, token, "handoff.commit", map[string]any{
		"targetId": "s1", "text": "carry on", "cwd": "/tmp/work", "model": "kimi-k2", "title": "继续实现",
	})
	if code != http.StatusOK {
		t.Fatalf("handoff.commit => %d %#v", code, value)
	}
	last := core.last()
	if last.payload["targetId"] != "s1" {
		t.Fatalf("the target was lost: %#v", last.payload)
	}
	for _, key := range []string{"cwd", "model", "title"} {
		if _, present := last.payload[key]; present {
			t.Fatalf("an existing target carried %s: %#v", key, last.payload)
		}
	}
}

// The preview shows the choices a commit would make, so a plugin can render the
// form without guessing.
func TestHandoffPreviewReachesThePlugin(t *testing.T) {
	core := &pluginCoreDouble{}
	manager, token := officialManager(t, core, []string{CapHandoff})
	code, value := callPlugin(t, manager, token, "handoff.preview", map[string]any{
		"text": "carry on", "cwd": "/tmp/work", "model": "kimi-k2", "title": "继续实现",
	})
	if code != http.StatusOK {
		t.Fatalf("handoff.preview => %d %#v", code, value)
	}
	preview := value["value"].(map[string]any)
	if preview["cwd"] != "/tmp/work" || preview["model"] != "kimi-k2" || preview["title"] != "继续实现" {
		t.Fatalf("preview lost the choices: %#v", preview)
	}
	// The preview is answered locally; it must not touch the core.
	if len(core.calls) != 0 {
		t.Fatalf("preview reached the core: %#v", core.calls)
	}
}

// An empty mode stays the documented default rather than becoming a value the
// core has to reject.
func TestHandoffCommitDefaultsTheMode(t *testing.T) {
	core := &pluginCoreDouble{}
	manager, token := officialManager(t, core, []string{CapHandoff})
	if code, value := callPlugin(t, manager, token, "handoff.commit", map[string]any{"text": "carry on"}); code != http.StatusOK {
		t.Fatalf("handoff.commit => %d %#v", code, value)
	}
	if mode := core.last().payload["mode"]; mode != "normal" {
		t.Fatalf("mode = %v, want normal", mode)
	}
}

// A refused handoff text never reaches the core, whatever else it carries.
func TestHandoffCommitRefusesBadTextBeforeTheCore(t *testing.T) {
	core := &pluginCoreDouble{}
	manager, token := officialManager(t, core, []string{CapHandoff})
	if code, _ := callPlugin(t, manager, token, "handoff.commit", map[string]any{"text": "   ", "cwd": "/tmp/work"}); code != http.StatusBadRequest {
		t.Fatalf("empty handoff text => %d", code)
	}
	if len(core.calls) != 0 {
		t.Fatalf("a refused handoff reached the core: %#v", core.calls)
	}
}
