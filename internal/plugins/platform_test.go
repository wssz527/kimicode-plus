package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"enhancedkimi/internal/security"
)

func TestWindowsElectronMainSurfacesRequirePairedHostSession(t *testing.T) {
	manager, _ := testManager(t, func() bool { return false })
	manager.platform = "windows"
	root := writePluginDir(t, filepath.Join(t.TempDir(), "source"), manifestFor("test.windows-main", 2, []string{CapUIRightPanel, CapBrowserView}), nil)
	preview, err := manager.Preview(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(context.Background(), InstallRequest{Root: root, PackageDigest: preview.PackageDigest, Capabilities: &preview.Capabilities, ConfirmPrivileged: true}); err != nil {
		t.Fatal(err)
	}
	plugin, err := manager.Enable(context.Background(), preview.ID)
	if err != nil {
		t.Fatal(err)
	}

	guard := security.New("127.0.0.1:43125")
	mux := http.NewServeMux()
	manager.Register(mux)
	mux.HandleFunc("POST "+APIPrefix+"/pair", guard.Pair)
	handler := guard.Wrap(mux)
	request := func(method, path, token string, payload any) *httptest.ResponseRecorder {
		t.Helper()
		data, _ := json.Marshal(payload)
		r := httptest.NewRequest(method, "http://"+guard.Host()+APIPrefix+path, strings.NewReader(string(data)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	pair := request(http.MethodPost, "/pair", "", map[string]any{
		"pairingToken": guard.PairingToken(), "permissions": []string{"plugins:read", "plugins:write", security.ElectronMainPermission},
	})
	var credentials struct{ SessionToken string }
	if pair.Code != 200 || json.Unmarshal(pair.Body.Bytes(), &credentials) != nil || credentials.SessionToken == "" {
		t.Fatalf("pairing: %d %s", pair.Code, pair.Body)
	}
	for _, test := range []struct {
		name, token   string
		count, status int
	}{
		{"renderer", guard.IssueSessionToken(nil), 3, 409},
		{"direct", guard.PairingToken(), 3, 409},
		{"native", credentials.SessionToken, 0, 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := request(http.MethodGet, "/plugins", test.token, nil)
			var list struct {
				PlatformLimitations []PlatformLimitation
				Items               []Plugin
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.PlatformLimitations) != test.count || len(list.Items) != 1 || len(list.Items[0].PlatformLimitations) != min(test.count, 2) {
				t.Fatalf("list: %d %s", w.Code, w.Body)
			}
			w = request(http.MethodPost, "/plugin-runtime/ui.popout", test.token, map[string]string{"token": plugin.Token})
			if w.Code != test.status {
				t.Fatalf("popout: %d %s", w.Code, w.Body)
			}
			w = request(http.MethodPost, "/plugin-runtime/browser.open", test.token, map[string]any{"token": plugin.Token, "params": map[string]string{"viewId": "surface", "instanceId": "test", "url": "http://127.0.0.1/"}})
			if w.Code != test.status {
				t.Fatalf("browser: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestWindowsPlatformLimitationsPreserveOtherPluginCapabilities(t *testing.T) {
	manager, _ := testManager(t, func() bool { return false })
	manager.platform = "windows"
	ctx := context.Background()
	grants := []string{CapUIEntry, CapUIRightPanel, CapBrowserView, CapCommands}
	root := writePluginDir(t, filepath.Join(t.TempDir(), "source"), manifestFor("test.windows", 2, grants), nil)
	preview, err := manager.Preview(ctx, root)
	if err != nil || !reflect.DeepEqual(preview.PlatformLimitations, PlatformLimitations("windows")) {
		t.Fatalf("preview: %+v (%v)", preview, err)
	}
	if _, err = manager.Install(ctx, InstallRequest{Root: root, PackageDigest: preview.PackageDigest, Capabilities: &grants, ConfirmPrivileged: true}); err != nil {
		t.Fatal(err)
	}
	plugin, err := manager.Enable(ctx, preview.ID)
	if err != nil || !reflect.DeepEqual(plugin.PlatformLimitations, preview.PlatformLimitations) {
		t.Fatalf("enabled plugin: %+v (%v)", plugin, err)
	}
	for _, action := range []string{"browser.open", "browser.close", "ui.popout"} {
		code, value := callPlugin(t, manager, plugin.Token, action, nil)
		failure, _ := value["error"].(map[string]any)
		if code != http.StatusConflict || failure["code"] != "PLATFORM_UNSUPPORTED" || failure["message"] == "" {
			t.Fatalf("%s => %d %#v", action, code, value)
		}
	}
	for _, action := range []string{"register", "list", "invoke", "unregister"} {
		code, value := callPlugin(t, manager, plugin.Token, "commands."+action, map[string]any{"name": "windows.echo", "command": "windows.echo"})
		if code != http.StatusOK {
			t.Fatalf("commands.%s => %d %#v", action, code, value)
		}
	}
	root = writePluginDir(t, filepath.Join(t.TempDir(), "storage"), manifestFor("test.storage", 2, []string{CapStorage}), nil)
	preview, err = manager.Preview(ctx, root)
	if err != nil || len(preview.PlatformLimitations) != 0 {
		t.Fatalf("unrelated capabilities: %+v (%v)", preview, err)
	}
}
