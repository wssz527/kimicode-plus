package updater

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func autoCheckOptions(t *testing.T) Options {
	t.Helper()
	return Options{CurrentVersion: "0.2.0", DataDir: t.TempDir(), GOOS: "darwin", GOARCH: "amd64", Now: time.Now}
}

func updateFeed(t *testing.T, failing *atomic.Bool, attempts *atomic.Int32, tag string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		if failing != nil && failing.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(release{Tag: tag, PublishedAt: fixedTime.Format(time.RFC3339), URL: "https://github.com/test-owner/test-repo/releases/tag/" + tag})
	}))
	t.Cleanup(server.Close)
	return server
}

func TestAutoCheckPreferencePersistence(t *testing.T) {
	options := autoCheckOptions(t)
	manager := New(options)
	if !manager.Status().AutoCheck {
		t.Fatal("auto check must default to enabled")
	}
	status, err := manager.SetAutoCheck(false)
	if err != nil || status.AutoCheck {
		t.Fatalf("disable auto check: %+v, %v", status, err)
	}
	data, err := os.ReadFile(filepath.Join(options.DataDir, prefsName))
	if err != nil || string(data) != `{"autoCheck":false}` {
		t.Fatalf("unexpected preference file %q: %v", data, err)
	}
	manager.Close()

	restored := New(options)
	if restored.Status().AutoCheck {
		t.Fatal("disabled preference was not restored")
	}
	if _, err = restored.SetAutoCheck(true); err != nil {
		t.Fatal(err)
	}
	restored.Close()

	reopened := New(options)
	if !reopened.Status().AutoCheck {
		t.Fatal("enabled preference was not restored")
	}
	reopened.Close()

	if err = os.WriteFile(filepath.Join(options.DataDir, prefsName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	fallback := New(options)
	if !fallback.Status().AutoCheck {
		t.Fatal("damaged preference file must fall back to enabled")
	}
	fallback.Close()
}

func TestAutoCheckToggleNotifies(t *testing.T) {
	options := autoCheckOptions(t)
	events := make(chan Status, 16)
	options.OnStatusChange = func(status Status) {
		select {
		case events <- status:
		default:
		}
	}
	manager := New(options)
	defer manager.Close()
	if _, err := manager.SetAutoCheck(false); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-events:
		if status.AutoCheck {
			t.Fatalf("toggle event kept auto check enabled: %+v", status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("auto check toggle did not notify")
	}
}

func TestAutoCheckToggleSurvivesInFlightCheck(t *testing.T) {
	started, unblock := make(chan struct{}, 1), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-unblock
		_ = json.NewEncoder(w).Encode(release{Tag: "v0.2.0"})
	}))
	t.Cleanup(server.Close)
	options := autoCheckOptions(t)
	options.APIBaseURL = server.URL + "/mirror"
	options.AllowInsecureHTTP = true
	manager := New(options)
	defer manager.Close()
	checked := make(chan Status, 1)
	go func() {
		status, _ := manager.Check(t.Context())
		checked <- status
	}()
	<-started
	if status, err := manager.SetAutoCheck(false); err != nil || status.AutoCheck {
		t.Fatalf("disable during check: %+v, %v", status, err)
	}
	close(unblock)
	status := <-checked
	if status.AutoCheck || manager.Status().AutoCheck {
		t.Fatalf("check commit reverted the preference: %+v", status)
	}
	data, err := os.ReadFile(filepath.Join(options.DataDir, prefsName))
	if err != nil || string(data) != `{"autoCheck":false}` {
		t.Fatalf("preference file %q: %v", data, err)
	}
}

func TestAutoCheckFindsUpdate(t *testing.T) {
	remote := darwinRelease(t, "\xcf\xfa\xed\xfe payload")
	server := serveRelease(t, remote)
	options := autoCheckOptions(t)
	options.APIBaseURL = server.URL + "/mirror"
	options.AllowInsecureHTTP = true
	options.AutoCheckInitialDelay = 5 * time.Millisecond
	options.AutoCheckInterval = time.Hour
	events := make(chan Status, 32)
	options.OnStatusChange = func(status Status) {
		select {
		case events <- status:
		default:
		}
	}
	manager := New(options)
	defer manager.Close()
	manager.StartAutoCheck()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case status := <-events:
			if status.State != Available {
				continue
			}
			if status.LatestVersion != "0.3.0" || !status.UpdateAvailable || !status.AutoCheck || remote.get.Load() == 0 {
				t.Fatalf("unexpected auto check result: %+v", status)
			}
			return
		case <-deadline:
			t.Fatalf("auto check did not find the release: %+v", manager.Status())
		}
	}
}

func TestAutoCheckWaitsForExplicitEnable(t *testing.T) {
	remote := darwinRelease(t, "\xcf\xfa\xed\xfe payload")
	server := serveRelease(t, remote)
	options := autoCheckOptions(t)
	options.APIBaseURL = server.URL + "/mirror"
	options.AllowInsecureHTTP = true
	options.AutoCheckInitialDelay = time.Millisecond
	options.AutoCheckInterval = 5 * time.Millisecond
	manager := New(options)
	defer manager.Close()
	if _, err := manager.SetAutoCheck(false); err != nil {
		t.Fatal(err)
	}
	manager.StartAutoCheck()
	time.Sleep(80 * time.Millisecond)
	if got := remote.get.Load(); got != 0 {
		t.Fatalf("disabled auto check contacted the update service %d times", got)
	}
	if _, err := manager.SetAutoCheck(true); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for remote.get.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("enabling auto check did not wake the loop: %+v", manager.Status())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSilentAutoCheckFailureKeepsCalmStatus(t *testing.T) {
	t.Setenv("EKD_UPDATE_REPO", "test-owner/test-repo")

	t.Run("idle", func(t *testing.T) {
		var failing atomic.Bool
		failing.Store(true)
		var attempts atomic.Int32
		server := updateFeed(t, &failing, &attempts, "v0.2.0")
		options := autoCheckOptions(t)
		options.APIBaseURL = server.URL + "/mirror"
		options.AllowInsecureHTTP = true
		options.AutoCheckInitialDelay = 5 * time.Millisecond
		options.AutoCheckInterval = 5 * time.Second
		manager := New(options)
		defer manager.Close()
		manager.StartAutoCheck()
		deadline := time.Now().Add(5 * time.Second)
		for {
			status := manager.Status()
			if attempts.Load() > 0 && status.State != Checking {
				if status.State != Idle || status.Error != "" {
					t.Fatalf("silent background failure replaced idle status: %+v", status)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("background check did not run: %+v", status)
			}
			time.Sleep(time.Millisecond)
		}
		var (
			status Status
			err    error
		)
		for retryDeadline := time.Now().Add(time.Second); ; {
			status, err = manager.Check(t.Context())
			if !errors.Is(err, ErrBusy) || time.Now().After(retryDeadline) {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if err == nil || status.State != Failed || status.Error == "" {
			t.Fatalf("manual check hid the failure: %+v, %v", status, err)
		}
	})

	t.Run("up to date", func(t *testing.T) {
		var failing atomic.Bool
		var attempts atomic.Int32
		server := updateFeed(t, &failing, &attempts, "v0.2.0")
		options := autoCheckOptions(t)
		options.APIBaseURL = server.URL + "/mirror"
		options.AllowInsecureHTTP = true
		options.AutoCheckInitialDelay = 100 * time.Millisecond
		options.AutoCheckInterval = 40 * time.Millisecond
		manager := New(options)
		defer manager.Close()
		status, err := manager.Check(t.Context())
		if err != nil || status.State != UpToDate {
			t.Fatalf("initial check: %+v, %v", status, err)
		}
		failing.Store(true)
		manager.StartAutoCheck()
		deadline := time.Now().Add(5 * time.Second)
		for {
			status = manager.Status()
			if attempts.Load() >= 3 && status.State == UpToDate {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("silent failure clobbered up-to-date status: %+v, attempts %d", status, attempts.Load())
			}
			time.Sleep(time.Millisecond)
		}
		if status.Error != "" {
			t.Fatalf("silent failure left an error behind: %+v", status)
		}
	})
}

func TestAutoCheckRespectsSafeMode(t *testing.T) {
	t.Setenv("EKD_UPDATE_REPO", "test-owner/test-repo")
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(release{Tag: "v0.2.0"})
	}))
	t.Cleanup(server.Close)
	var safe atomic.Bool
	safe.Store(true)
	options := autoCheckOptions(t)
	options.APIBaseURL = server.URL + "/mirror"
	options.AllowInsecureHTTP = true
	options.Safe = safe.Load
	options.AutoCheckInitialDelay = time.Millisecond
	options.AutoCheckInterval = 5 * time.Millisecond
	manager := New(options)
	defer manager.Close()
	manager.StartAutoCheck()
	time.Sleep(60 * time.Millisecond)
	if got := hits.Load(); got != 0 {
		t.Fatalf("safe mode auto check contacted the update service %d times", got)
	}
	safe.Store(false)
	deadline := time.Now().Add(5 * time.Second)
	for hits.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("auto check did not resume after leaving safe mode: %+v", manager.Status())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAutoCheckStopsOnClose(t *testing.T) {
	options := autoCheckOptions(t)
	options.AutoCheckInitialDelay = time.Hour
	options.AutoCheckInterval = time.Hour
	manager := New(options)
	manager.StartAutoCheck()
	done := manager.autoCheckDone
	manager.StartAutoCheck()
	if manager.autoCheckDone != done {
		t.Fatal("StartAutoCheck started more than one loop")
	}
	manager.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("auto check loop survived Close")
	}
}
