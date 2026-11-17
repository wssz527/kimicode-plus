package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"enhancedkimi/internal/storage"
)

func diffTestService(t *testing.T, cwds map[string]string) *NativeService {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/sessions/") {
			id := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
			cwd, ok := cwds[id]
			if !ok {
				http.NotFound(w, r)
				return
			}
			writeNativeTestEnvelope(w, map[string]any{
				"id":       id,
				"title":    "Session " + id,
				"metadata": map[string]any{"cwd": cwd},
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	db, err := storage.Open(filepath.Join(t.TempDir(), "diff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	kap := NewKAPClient("")
	kap.base, kap.token = server.URL, "synthetic"
	service, err := NewNativeService(db, kap, t.TempDir(), func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	return service
}

func runGitFixture(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func TestSessionDiffReadsRedactsAndReportsUnavailable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dirtyRepo := t.TempDir()
	runGitFixture(t, dirtyRepo, "init")
	runGitFixture(t, dirtyRepo, "config", "user.email", "test@example.com")
	runGitFixture(t, dirtyRepo, "config", "user.name", "Test")
	tracked := filepath.Join(dirtyRepo, "app.txt")
	if err := os.WriteFile(tracked, []byte("line 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitFixture(t, dirtyRepo, "add", "app.txt")
	runGitFixture(t, dirtyRepo, "commit", "-m", "init")
	if err := os.WriteFile(tracked, []byte("line 1\napi_key=sk-testsecret12345678\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cleanRepo := t.TempDir()
	runGitFixture(t, cleanRepo, "init")
	runGitFixture(t, cleanRepo, "config", "user.email", "test@example.com")
	runGitFixture(t, cleanRepo, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(cleanRepo, "README.md"), []byte("ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitFixture(t, cleanRepo, "add", "README.md")
	runGitFixture(t, cleanRepo, "commit", "-m", "init")

	plainDir := t.TempDir()
	missingDir := filepath.Join(t.TempDir(), "does-not-exist")

	service := diffTestService(t, map[string]string{
		"dirty":   dirtyRepo,
		"clean":   cleanRepo,
		"plain":   plainDir,
		"missing": missingDir,
	})

	call := func(id string) map[string]any {
		payload, _ := json.Marshal(map[string]any{"id": id})
		value, err := service.Action(context.Background(), "sessions.diff", payload)
		if err != nil {
			t.Fatalf("sessions.diff(%s): %v", id, err)
		}
		return value.(map[string]any)
	}

	dirty := call("dirty")
	if dirty["available"] != true || dirty["clean"] != false {
		t.Fatalf("dirty repo should report available non-clean diff: %#v", dirty)
	}
	diffText, _ := dirty["diff"].(string)
	if !strings.Contains(diffText, "app.txt") || strings.Contains(diffText, "sk-testsecret12345678") || !strings.Contains(diffText, "[REDACTED:") {
		t.Fatalf("diff did not include change or leaked secret: %q", diffText)
	}

	clean := call("clean")
	if clean["available"] != true || clean["clean"] != true || clean["diff"] != "" {
		t.Fatalf("clean repo should report clean=true: %#v", clean)
	}

	plain := call("plain")
	if plain["available"] != false || !strings.Contains(plain["reason"].(string), "Git") {
		t.Fatalf("non-git dir should report unavailable: %#v", plain)
	}

	missing := call("missing")
	if missing["available"] != false || !strings.Contains(missing["reason"].(string), "不存在") {
		t.Fatalf("missing dir should report unavailable: %#v", missing)
	}
}
