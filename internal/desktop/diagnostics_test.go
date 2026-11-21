package desktop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func diagnosticsDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return dir
}

func diagnosticsService(t *testing.T, dataDir string) *NativeService {
	t.Helper()
	service, _ := newAddonFeatureService(t)
	service.UseAttach(AttachConfig{DataDir: dataDir})
	return service
}

func diagnosticsOf(t *testing.T, service *NativeService) map[string]any {
	t.Helper()
	value, err := service.Action(context.Background(), "diagnostics.get", nil)
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("diagnostics.get returned %T", value)
	}
	return payload
}

func sectionOf(t *testing.T, payload map[string]any, key string) map[string]any {
	t.Helper()
	section, ok := payload[key].(map[string]any)
	if !ok {
		t.Fatalf("%s is %T", key, payload[key])
	}
	return section
}

// A missing log is a real state the panel must describe, not an empty string
// that reads as "no problems".
func TestDiagnosticsReportsMissingFiles(t *testing.T) {
	dir := diagnosticsDataDir(t)
	service := diagnosticsService(t, dir)
	payload := diagnosticsOf(t, service)

	log := sectionOf(t, payload, "attachLog")
	if log["available"] != false {
		t.Fatalf("attachLog available = %v, want false", log["available"])
	}
	if log["note"] != "尚未生成" {
		t.Fatalf("attachLog note = %q", log["note"])
	}
	if log["path"] != filepath.Join(dir, "attach.log") {
		t.Fatalf("attachLog path = %v", log["path"])
	}

	notices := sectionOf(t, payload, "notices")
	if notices["available"] != false {
		t.Fatalf("notices available = %v, want false", notices["available"])
	}
	if !strings.HasSuffix(notices["path"].(string), noticesFileName) {
		t.Fatalf("notices path = %v", notices["path"])
	}
}

func TestDiagnosticsTailsAttachLogAndRedactsSecrets(t *testing.T) {
	dir := diagnosticsDataDir(t)
	service := diagnosticsService(t, dir)
	var body strings.Builder
	for index := 0; index < 600; index++ {
		body.WriteString("2026-11-17T10:00:00Z attach tick line ")
		body.WriteString(strings.Repeat("x", 20))
		body.WriteString("\n")
	}
	body.WriteString("connected with Bearer sdjkhfsdkjfhksjdfhksjdfh token\n")
	if err := os.WriteFile(filepath.Join(dir, "attach.log"), []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	log := sectionOf(t, diagnosticsOf(t, service), "attachLog")
	if log["available"] != true {
		t.Fatalf("attachLog available = %v", log["available"])
	}
	tail, _ := log["tail"].(string)
	if !strings.Contains(tail, "Bearer [REDACTED:BEARER_TOKEN]") {
		t.Fatalf("tail did not redact the bearer token: %q", lastLines(tail, 3))
	}
	if strings.Contains(tail, "sdjkhfsdkjfhksjdfhksjdfh") {
		t.Fatal("tail leaked the raw token")
	}
	if log["truncated"] != true {
		t.Fatal("a 19 KiB log should report truncation at 8 KiB")
	}
	if len(tail) > attachLogTailBytes {
		t.Fatalf("tail is %d bytes, want at most %d", len(tail), attachLogTailBytes)
	}
	if !strings.HasSuffix(strings.TrimRight(tail, "\n"), "token") {
		t.Fatalf("tail did not end at the end of the file: %q", lastLines(tail, 3))
	}
}

// A byte window can land mid-line; the fragment must be dropped rather than
// rendered as a broken first line.
func TestDiagnosticsDropsPartialFirstLine(t *testing.T) {
	dir := diagnosticsDataDir(t)
	service := diagnosticsService(t, dir)
	var body strings.Builder
	body.WriteString("head-line\n")
	for index := 0; index < 9000; index++ {
		body.WriteString("y")
	}
	body.WriteString("\nfinal-line\n")
	if err := os.WriteFile(filepath.Join(dir, "attach.log"), []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	tail, _ := sectionOf(t, diagnosticsOf(t, service), "attachLog")["tail"].(string)
	if strings.HasPrefix(tail, "y") {
		t.Fatalf("tail kept a partial leading line: %q", tail[:20])
	}
	if !strings.HasPrefix(tail, "final-line") {
		t.Fatalf("tail = %q", lastLines(tail, 2))
	}
}

func TestDiagnoseFileHeadsNotices(t *testing.T) {
	path := filepath.Join(t.TempDir(), noticesFileName)
	content := "Enhanced Kimi Desktop — third-party notices\n" + strings.Repeat("license line\n", 400)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	status := diagnoseFile(path, "head", noticesHeadBytes)
	if status["available"] != true {
		t.Fatalf("available = %v", status["available"])
	}
	head, _ := status["head"].(string)
	if !strings.HasPrefix(head, "Enhanced Kimi Desktop") {
		t.Fatalf("head = %q", head[:40])
	}
	if len(head) > noticesHeadBytes {
		t.Fatalf("head is %d bytes, want at most %d", len(head), noticesHeadBytes)
	}
	if status["truncated"] != true {
		t.Fatal("a 5 KiB notices file should report truncation at 2 KiB")
	}
	if !strings.HasSuffix(strings.TrimRight(head, "\n"), "license line") {
		t.Fatalf("head did not end on a whole line: %q", lastLines(head, 2))
	}
}

func TestNoticesPathSitsNextToExecutable(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Skip("no executable path in this environment")
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	want := filepath.Join(filepath.Dir(executable), noticesFileName)
	if got := noticesFilePath(); got != want {
		t.Fatalf("noticesFilePath() = %q, want %q", got, want)
	}
}

func lastLines(text string, count int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return strings.Join(lines, "\n")
}

// The resident copy is what runs after the download is deleted, so its notices
// must be readable when the running binary has none next to it.
func TestNoticesPathFallsBackToResidentCopy(t *testing.T) {
	dir := t.TempDir()
	resident := filepath.Join(dir, "resident")
	if err := os.MkdirAll(resident, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resident, noticesFileName), []byte("resident notices"), 0o644); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Skip("no executable path in this environment")
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err == nil {
		executable = resolved
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(executable), noticesFileName)); err == nil {
		t.Skip("a notices file exists next to the test binary; the fallback is not exercised")
	}
	path := noticesFilePathFor(dir)
	if path != filepath.Join(resident, noticesFileName) {
		t.Fatalf("notices path = %q", path)
	}
	if status := diagnoseFile(path, "head", noticesHeadBytes); status["available"] != true {
		t.Fatalf("resident notices were not readable: %+v", status)
	}
	if got := noticesFilePathFor(""); got != filepath.Join(filepath.Dir(executable), noticesFileName) {
		t.Fatalf("empty data dir did not keep the binary path: %q", got)
	}
}
