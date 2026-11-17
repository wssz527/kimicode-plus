package desktop

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPluginWindowRoutesCommandsAndReleasesItsScope(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the executable plugin-window probe needs it")
	}
	probe, err := filepath.Abs("testdata/plugin_window_probe.cjs")
	if err != nil {
		t.Fatal(err)
	}
	source, err := filepath.Abs("assets/bootstrap.js")
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(node, probe, source).CombinedOutput()
	if err != nil {
		t.Fatalf("plugin-window probe failed: %v\n%s", err, output)
	}
	var run struct {
		Opened probeResult `json:"opened"`
		Window struct {
			Surface             string `json:"surface"`
			PluginID            string `json:"pluginId"`
			LegacyPlannerWindow bool   `json:"legacyPlannerWindow"`
		} `json:"window"`
		ForeignRegistration probeResult `json:"foreignRegistration"`
		MainRegistration    probeResult `json:"mainRegistration"`
		PopupRegistration   probeResult `json:"popupRegistration"`
		CrossWindow         struct {
			OK    bool `json:"ok"`
			Value struct {
				ExecutedBy string `json:"executedBy"`
				Command    string `json:"command"`
				Args       struct {
					SessionID string `json:"sessionId"`
				} `json:"args"`
			} `json:"value"`
		} `json:"crossWindow"`
		ReverseWindow struct {
			OK    bool `json:"ok"`
			Value struct {
				ExecutedBy string `json:"executedBy"`
			} `json:"value"`
		} `json:"reverseWindow"`
		View struct {
			OK    bool `json:"ok"`
			Value struct {
				InstanceID string `json:"instanceId"`
			} `json:"value"`
		} `json:"view"`
		MainView struct {
			OK    bool `json:"ok"`
			Value struct {
				InstanceID string `json:"instanceId"`
			} `json:"value"`
		} `json:"mainView"`
		DistinctInstanceIDs bool `json:"distinctInstanceIds"`
		BeforeCloseViews    int  `json:"beforeCloseViews"`
		Closed              struct {
			Destroyed          bool `json:"destroyed"`
			RendererRemoved    bool `json:"rendererRemoved"`
			Views              int  `json:"views"`
			OwnedViewDestroyed bool `json:"ownedViewDestroyed"`
			MainViewAlive      bool `json:"mainViewAlive"`
			DetachedViews      int  `json:"detachedViews"`
			PopupCommand       bool `json:"popupCommand"`
			ClosedCoreViews    []struct {
				PluginID   string `json:"pluginId"`
				ViewID     string `json:"viewId"`
				InstanceID string `json:"instanceId"`
			} `json:"closedCoreViews"`
		} `json:"closed"`
		ClosedCommand struct {
			Refused string `json:"refused"`
		} `json:"closedCommand"`
		MainStillWorks struct {
			OK bool `json:"ok"`
		} `json:"mainStillWorks"`
		MainViewReload struct {
			OK bool `json:"ok"`
		} `json:"mainViewReload"`
	}
	if err := json.Unmarshal(output, &run); err != nil {
		t.Fatalf("unreadable probe answer: %v\n%s", err, output)
	}
	if !run.Opened.OK || run.Window.Surface != "plugin" || run.Window.PluginID != "kimicode-plus.planner" || run.Window.LegacyPlannerWindow {
		t.Fatalf("window did not follow the core-authorized plugin: %#v", run)
	}
	if run.ForeignRegistration.OK {
		t.Fatal("a plugin registered another plugin's command in its window")
	}
	if !run.MainRegistration.OK || !run.PopupRegistration.OK {
		t.Fatalf("valid command registrations failed: %#v", run)
	}
	if !run.CrossWindow.OK || run.CrossWindow.Value.ExecutedBy != "host" || run.CrossWindow.Value.Args.SessionID != "session-1" {
		t.Fatalf("command did not run in the owning main window: %#v", run.CrossWindow)
	}
	if !run.ReverseWindow.OK || run.ReverseWindow.Value.ExecutedBy != "plugin" {
		t.Fatalf("command did not run in the owning popup: %#v", run.ReverseWindow)
	}
	if !run.View.OK || !run.MainView.OK || !run.DistinctInstanceIDs || run.BeforeCloseViews != 2 {
		t.Fatalf("window view instances were not isolated: %#v", run)
	}
	if !run.Closed.Destroyed || !run.Closed.RendererRemoved || run.Closed.Views != 1 || !run.Closed.OwnedViewDestroyed || !run.Closed.MainViewAlive || run.Closed.DetachedViews != 1 || run.Closed.PopupCommand {
		t.Fatalf("closing the popup leaked or broke views: %#v", run)
	}
	if len(run.Closed.ClosedCoreViews) != 1 || run.Closed.ClosedCoreViews[0].InstanceID != run.View.Value.InstanceID || run.Closed.ClosedCoreViews[0].ViewID != "web" {
		t.Fatalf("closing the popup did not release its core view record: %#v", run.Closed.ClosedCoreViews)
	}
	if run.ClosedCommand.Refused != "PLUGIN_COMMAND_UNAVAILABLE" || !run.MainStillWorks.OK || !run.MainViewReload.OK {
		t.Fatalf("closed-window routing or main view was not kept healthy: %#v", run)
	}
}
