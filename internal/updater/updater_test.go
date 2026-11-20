package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const oldBinary = "previous executable"

var fixedTime = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

type packageFile struct {
	name string
	data string
	mode os.FileMode
}

func makeArchive(t *testing.T, targetOS string, files ...packageFile) []byte {
	t.Helper()
	var out bytes.Buffer
	if targetOS == "windows" {
		w := zip.NewWriter(&out)
		for _, item := range files {
			header := &zip.FileHeader{Name: item.name, Method: zip.Deflate}
			header.SetMode(item.mode | 0o755)
			entry, err := w.CreateHeader(header)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = entry.Write([]byte(item.data)); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		gz := gzip.NewWriter(&out)
		w := tar.NewWriter(gz)
		for _, item := range files {
			header := &tar.Header{Name: item.name, Mode: 0o755, Typeflag: tar.TypeReg, Size: int64(len(item.data))}
			if item.mode&os.ModeSymlink != 0 {
				header.Typeflag, header.Size, header.Linkname = tar.TypeSymlink, 0, "target"
			}
			if err := w.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(item.data)); err != nil {
				t.Fatal(err)
			}
		}
		if err := errors.Join(w.Close(), gz.Close()); err != nil {
			t.Fatal(err)
		}
	}
	return out.Bytes()
}

func TestVersionPrecedence(t *testing.T) {
	valid := []string{"0.0.0", "v1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.9", "1.0.10", "10.0.0", "1000000000000000000000.0.0"}
	for i, input := range valid {
		v, ok := parseVersion(input)
		if !ok {
			t.Fatalf("valid version rejected: %s", input)
		}
		for j, other := range valid {
			w, _ := parseVersion(other)
			if got := v.compare(w); (got < 0) != (i < j) || (got == 0) != (i == j) {
				t.Fatalf("%s vs. %s = %d", input, other, got)
			}
		}
	}
	for _, input := range []string{"1", "1.2", "v1.02.3", "1.2.3-", "1.2.3-alpha.02", "1.2.3-alpha..1", "1.2.3+", "1.2.3-空", "v 1.2.3"} {
		if _, ok := parseVersion(input); ok {
			t.Errorf("invalid version accepted: %s", input)
		}
	}
	a, _ := parseVersion("1.0.0+one")
	b, _ := parseVersion("v1.0.0+two")
	if a.compare(b) != 0 {
		t.Fatal("build metadata affected precedence")
	}
}

type fakeRelease struct {
	tag          string
	name         string
	archive      []byte
	sums         string
	status       int
	assets       []asset
	get          atomic.Int32
	packageGet   atomic.Int32
	servePackage http.HandlerFunc
}

func serveRelease(t *testing.T, releaseData *fakeRelease) *httptest.Server {
	t.Helper()
	t.Setenv("EKD_UPDATE_REPO", "test-owner/test-repo")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mirror/repos/test-owner/test-repo/releases/latest":
			releaseData.get.Add(1)
			if r.Method != http.MethodGet || r.Header.Get("Accept") != "application/vnd.github+json" || r.UserAgent() != "KimiCodePlus/0.2.0" {
				t.Errorf("unexpected release request: %s %v", r.Method, r.Header)
			}
			if releaseData.status != 0 {
				w.WriteHeader(releaseData.status)
				return
			}
			origin := "http://" + r.Host
			assets := releaseData.assets
			if assets == nil {
				assets = []asset{{Name: releaseData.name, URL: origin + "/package", Size: int64(len(releaseData.archive))}}
				if releaseData.sums != "" {
					assets = append(assets, asset{Name: "SHA256SUMS", URL: origin + "/sums"})
				}
			}
			_ = json.NewEncoder(w).Encode(release{Tag: releaseData.tag, PublishedAt: fixedTime.Format(time.RFC3339), URL: "https://github.com/test-owner/test-repo/releases/tag/" + releaseData.tag, Notes: "## Changes\nUpdated", Assets: assets})
		case "/sums":
			_, _ = w.Write([]byte(releaseData.sums))
		case "/package":
			releaseData.packageGet.Add(1)
			if releaseData.servePackage != nil {
				releaseData.servePackage(w, r)
			} else {
				_, _ = w.Write(releaseData.archive)
			}
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func testOptions(t *testing.T, endpoint, target string) Options {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "core")
	if target == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, []byte(oldBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	return Options{CurrentVersion: "0.2.0", Executable: exe, DataDir: t.TempDir(), APIBaseURL: endpoint + "/mirror", GOOS: target, GOARCH: "amd64", AllowInsecureHTTP: true, RestartState: func() RestartState { return RestartState{Port: 48352} }, RequestRestart: func() {}, Now: func() time.Time { return fixedTime }}
}

func darwinRelease(t *testing.T, binary string) *fakeRelease {
	t.Helper()
	name := "enhanced-kimi-0.3.0-darwin-amd64.tar.gz"
	archive := makeArchive(t, "darwin", packageFile{"enhanced-kimi-darwin-amd64", binary, 0}, packageFile{noticesName, "new notices", 0}, packageFile{"RUN.txt", "instructions", 0})
	return &fakeRelease{tag: "v0.3.0", name: name, archive: archive, sums: fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), name)}
}

func TestCheckRelease(t *testing.T) {
	for _, tc := range []struct {
		name, tag, state, note string
		code                   int
		available, canApply    bool
	}{
		{"new version", "v0.3.0", Available, "", 0, true, true},
		{"same version", "v0.2.0", UpToDate, "", 0, false, false},
		{"older release", "v0.1.9", UpToDate, "", 0, false, false},
		{"older prerelease", "v0.2.0-rc.1", UpToDate, "", 0, false, false},
		{"first release", "", UpToDate, "尚未发布", 404, false, false},
		{"HTTP error", "", Failed, "", 403, false, false},
		{"invalid version", "version three", Failed, "", 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := darwinRelease(t, "\xcf\xfa\xed\xfe payload")
			r.tag, r.status = tc.tag, tc.code
			server := serveRelease(t, r)
			m := New(testOptions(t, server.URL, "darwin"))
			if got := m.Status(); got.State != Idle || r.get.Load() != 0 || got.CurrentVersion != "0.2.0" {
				t.Fatalf("constructor contacted GitHub or returned invalid state: %+v", got)
			}
			s, err := m.Check(t.Context())
			if (err != nil) != (tc.state == Failed) || s.State != tc.state || s.UpdateAvailable != tc.available || s.CanApply != tc.canApply || s.CheckedAt != fixedTime.Format(time.RFC3339) || !strings.Contains(s.Note, tc.note) || r.get.Load() != 1 {
				t.Fatalf("unexpected release status: %+v, err=%v", s, err)
			}
			if tc.available && (s.LatestVersion != "0.3.0" || s.PublishedAt == "" || s.ReleaseNotes != "## Changes\nUpdated" || !strings.Contains(s.ReleaseURL, "/releases/tag/v0.3.0")) {
				t.Fatalf("missing metadata: %+v", s)
			}
		})
	}
}

func TestSelectAssetsRequiresExactVersionAndPlatform(t *testing.T) {
	for _, platform := range []struct{ os, arch, ext string }{
		{"darwin", "arm64", ".tar.gz"}, {"darwin", "amd64", ".tar.gz"}, {"windows", "amd64", ".zip"},
	} {
		t.Run(platform.os+"-"+platform.arch, func(t *testing.T) {
			m := New(Options{CurrentVersion: "0.2.0", GOOS: platform.os, GOARCH: platform.arch})
			defer m.Close()
			suffix := platform.os + "-" + platform.arch + platform.ext
			otherAssets := []asset{
				{Name: "SHA256SUMS", URL: "https://example.com/sums"},
				{Name: suffix, URL: "https://example.com/no-version"},
				{Name: "enhanced-kimi-0.2.0-" + suffix, URL: "https://example.com/old"},
				{Name: "different-v0.3.0-" + suffix, URL: "https://example.com/different"},
				{Name: "enhanced-kimi-0.3.0-windows-arm64.zip", URL: "https://example.com/other-platform"},
			}
			if archive, sums := m.selectAssets(otherAssets, "v0.3.0"); archive.Name != "" || sums.Name != "SHA256SUMS" {
				t.Fatalf("unexpected fallback: archive=%+v sums=%+v", archive, sums)
			}
			for _, prefix := range []string{"", "v"} {
				correct := asset{Name: "enhanced-kimi-" + prefix + "0.3.0-" + suffix, URL: "https://example.com/target"}
				if archive, _ := m.selectAssets(append(otherAssets, correct), "0.3.0"); archive != correct {
					t.Fatalf("exact match rejected: %+v", archive)
				}
			}
		})
	}
}

func TestCheckRateLimitError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	t.Cleanup(server.Close)
	m := New(testOptions(t, server.URL, "darwin"))
	s, err := m.Check(t.Context())
	if err == nil || s.State != Failed || !strings.Contains(s.Error, "每小时上限") {
		t.Fatalf("expected rate-limit hint, got status=%+v err=%v", s, err)
	}
}

func TestCheckConfigurationAndBlockers(t *testing.T) {
	r := darwinRelease(t, "\xcf\xfa\xed\xfe payload")
	server := serveRelease(t, r)
	options := testOptions(t, server.URL, "darwin")
	for _, tc := range []struct {
		name, reason string
		configure    func(*Options, *fakeRelease)
	}{
		{"missing checksum", "缺少 SHA256SUMS", func(_ *Options, r *fakeRelease) { r.sums = "" }},
		{"wrong architecture", "该版本缺少本平台安装包", func(o *Options, _ *fakeRelease) { o.GOARCH = "arm64" }},
		{"Linux has no release artifact", "该版本缺少本平台安装包", func(o *Options, _ *fakeRelease) { o.GOOS = "linux" }},
		{"temporary executable", "go run", func(o *Options, _ *fakeRelease) {
			dir := filepath.Join(t.TempDir(), "go-build12345")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			o.Executable = filepath.Join(dir, "core")
		}},
		{"safe mode", "安全模式", func(o *Options, _ *fakeRelease) { o.Safe = func() bool { return true } }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := darwinRelease(t, "\xcf\xfa\xed\xfe payload")
			o := options
			tc.configure(&o, r)
			o.APIBaseURL = serveRelease(t, r).URL + "/mirror"
			s, err := New(o).Check(t.Context())
			if err != nil || !s.UpdateAvailable || s.CanApply || !strings.Contains(s.CannotApplyReason, tc.reason) {
				t.Fatalf("check did not block apply: %+v, err=%v", s, err)
			}
		})
	}
	if runtime.GOOS != "windows" {
		dir := filepath.Dir(options.Executable)
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		s, err := New(options).Check(t.Context())
		if err != nil || s.CanApply || !strings.Contains(s.CannotApplyReason, "目录不可写") {
			t.Fatalf("read-only directory accepted: %+v, err=%v", s, err)
		}
	}
	invalid := options
	invalid.AllowInsecureHTTP = false
	if s, err := New(invalid).Check(t.Context()); err == nil || !strings.Contains(s.Error, "HTTPS") {
		t.Fatalf("untrusted HTTP accepted: %+v, %v", s, err)
	}
	t.Setenv("EKD_UPDATE_API_BASE", options.APIBaseURL)
	options.APIBaseURL = ""
	if _, err := New(options).Check(t.Context()); err != nil {
		t.Fatalf("API override ignored: %v", err)
	}
	t.Setenv("EKD_UPDATE_REPO", "owner/../repo")
	if s, err := New(options).Check(t.Context()); err == nil || !strings.Contains(s.Error, "owner/repo") {
		t.Fatalf("invalid repository accepted: %+v, %v", s, err)
	}
}

func TestApplyStagesBeforeRequestingRestart(t *testing.T) {
	for _, target := range []string{"darwin", "windows"} {
		t.Run(target, func(t *testing.T) {
			ext, binary := "", "\xcf\xfa\xed\xfe updated executable"
			archiveSuffix := ".tar.gz"
			if target == "windows" {
				ext, binary = ".exe", "MZ windows executable"
				archiveSuffix = ".zip"
			}
			name := "enhanced-kimi-v0.3.0-" + target + "-amd64" + archiveSuffix
			archive := makeArchive(t, target, packageFile{"enhanced-kimi-" + target + "-amd64" + ext, binary, 0}, packageFile{noticesName, "new notices", 0})
			r := &fakeRelease{tag: "v0.3.0", name: name, archive: archive, sums: fmt.Sprintf("%x *%s\r\n", sha256.Sum256(archive), name)}
			o := testOptions(t, serveRelease(t, r).URL, target)
			o.Executable = filepath.Join(filepath.Dir(o.Executable), "Kimi's 核心"+ext)
			if err := os.WriteFile(o.Executable, []byte(oldBinary), 0o755); err != nil {
				t.Fatal(err)
			}
			executable, err := filepath.EvalSymlinks(o.Executable)
			if err != nil {
				t.Fatal(err)
			}
			var stage string
			restarted := false
			o.RequestRestart = func() { restarted = true }
			o.launchSupervisor = func(helper, filename string) error {
				if restarted {
					t.Fatal("old core asked to stop before supervisor was ready")
				}
				stage = filepath.Dir(filename)
				checkFile(t, helper, oldBinary)
				checkFile(t, filepath.Join(stage, "binary"), binary)
				checkFile(t, filepath.Join(stage, noticesName), "new notices")
				data, err := os.ReadFile(filename)
				var job supervisorJob
				if err != nil || json.Unmarshal(data, &job) != nil || job.Executable != executable || job.OldPID != os.Getpid() ||
					job.Restart.Port != 48352 || job.PreviousVersion != "0.2.0" || job.TargetVersion != "0.3.0" || !job.HasNotices {
					t.Fatalf("invalid supervisor job: %+v, %v", job, err)
				}
				return nil
			}
			m := New(o)
			defer m.Close()
			s, err := m.Apply(t.Context())
			if err != nil || s.State != Restarting || !restarted || s.DownloadedBytes != int64(len(r.archive)) || s.DownloadedBytes != s.TotalBytes || r.get.Load() != 1 {
				t.Fatalf("apply: %+v, %v", s, err)
			}
			checkFile(t, o.Executable, oldBinary)
			if _, err := os.Stat(o.Executable + ".bak"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("old core replaced files before exiting: %v", err)
			}
			if _, err := m.Apply(t.Context()); !errors.Is(err, ErrBusy) {
				t.Fatalf("duplicate apply accepted: %v", err)
			}
			checkFile(t, filepath.Join(stage, "binary"), binary)
		})
	}
}

func TestApplyRejectsTamperingAndMalformedChecksums(t *testing.T) {
	for _, name := range []string{"tampered archive", "uppercase hash", "different archive", "duplicate checksum", "large checksum", "bad magic"} {
		t.Run(name, func(t *testing.T) {
			r := darwinRelease(t, "\xcf\xfa\xed\xfe updated executable")
			want, download := "校验值", false
			switch name {
			case "tampered archive":
				r.archive[len(r.archive)-1] ^= 1
				want, download = "SHA-256 校验失败", true
			case "uppercase hash":
				r.sums = strings.ToUpper(r.sums[:64]) + r.sums[64:]
			case "different archive":
				r.sums = strings.Replace(r.sums, r.name, "other-"+r.name, 1)
			case "duplicate checksum":
				r.sums += r.sums
			case "large checksum":
				r.sums += strings.Repeat("#", maxSumsBytes)
				want = "大小限制"
			case "bad magic":
				r = darwinRelease(t, "not an executable")
				want, download = "程序格式", true
			}
			o := testOptions(t, serveRelease(t, r).URL, "darwin")
			o.RequestRestart = func() { t.Error("unexpected restart") }
			s, err := New(o).Apply(t.Context())
			if err == nil || s.State != Failed || !strings.Contains(s.Error, want) || (r.packageGet.Load() > 0) != download {
				t.Fatalf("invalid download accepted: %+v, %v", s, err)
			}
			checkFile(t, o.Executable, oldBinary)
			checkEmptyDownloads(t, o.DataDir)
			if _, err = os.Stat(o.Executable + ".bak"); !os.IsNotExist(err) {
				t.Fatalf("invalid package reached replacement: %v", err)
			}
		})
	}
}

func TestArchiveTraversalLinksAndMagic(t *testing.T) {
	for _, tc := range []struct{ target, name, magic string }{
		{"linux", "enhanced-kimi-linux-amd64", "\x7fELF validated"},
		{"darwin", "enhanced-kimi-darwin-amd64", "\xcf\xfa\xed\xfe validated"},
		{"windows", "enhanced-kimi-windows-amd64.exe", "MZ validated"},
	} {
		t.Run(tc.target, func(t *testing.T) {
			for _, forbidden := range []packageFile{{"../escape", "danger", 0}, {"/absolute", "danger", 0}, {"nested/../escape", "danger", 0}, {`..\escape`, "danger", 0}, {`C:/escape`, "danger", 0}, {"symlink", "", os.ModeSymlink}, {tc.name, tc.magic, 0}} {
				t.Run(forbidden.name, func(t *testing.T) {
					dir := t.TempDir()
					filename := filepath.Join(dir, "archive")
					data := makeArchive(t, tc.target, packageFile{tc.name, tc.magic, 0}, forbidden)
					if err := os.WriteFile(filename, data, 0o600); err != nil {
						t.Fatal(err)
					}
					if _, err := extractArchive(filename, dir, tc.target, "amd64"); err == nil {
						t.Fatal("unsafe entry accepted")
					}
				})
			}
			dir := t.TempDir()
			filename := filepath.Join(dir, "archive")
			data := makeArchive(t, tc.target, packageFile{"nested/" + tc.name, tc.magic, 0}, packageFile{noticesName, "notices", 0})
			if err := os.WriteFile(filename, data, 0o600); err != nil {
				t.Fatal(err)
			}
			files, err := extractArchive(filename, dir, tc.target, "amd64")
			if err != nil {
				t.Fatal(err)
			}
			checkFile(t, files.binary, tc.magic)
			checkFile(t, files.notices, "notices")
		})
	}
}

func TestSafeModeAndConcurrentApply(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix test archive")
	}
	r := darwinRelease(t, "\xcf\xfa\xed\xfe payload")
	started, proceed := make(chan struct{}), make(chan struct{})
	r.servePackage = func(w http.ResponseWriter, req *http.Request) {
		_, _ = w.Write(r.archive[:5])
		w.(http.Flusher).Flush()
		close(started)
		select {
		case <-proceed:
			_, _ = w.Write(r.archive[5:])
		case <-req.Context().Done():
		}
	}
	var safe atomic.Bool
	safe.Store(true)
	o := testOptions(t, serveRelease(t, r).URL, "darwin")
	o.Safe = safe.Load
	m := New(o)
	defer m.Close()
	if _, err := m.Apply(t.Context()); !errors.Is(err, ErrSafeMode) || r.get.Load() != 0 {
		t.Fatalf("safe mode allowed release activity: %v", err)
	}
	safe.Store(false)
	if _, err := m.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if status, err := m.StartApply(ctx); err != nil || status.State != Downloading {
		t.Fatalf("failed to start update: %+v, %v", status, err)
	}
	cancel()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("download did not start after request cancellation")
	}
	if _, err := m.Apply(t.Context()); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent apply accepted: %v", err)
	}
	if _, err := m.Check(t.Context()); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent check accepted: %v", err)
	}
	for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
		if status := m.Status(); status.State == Downloading && status.DownloadedBytes == 5 && status.TotalBytes == int64(len(r.archive)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("download progress not exposed: %+v", m.Status())
		}
	}
	safe.Store(true)
	close(proceed)
	for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
		if status := m.Status(); status.State == Failed && strings.Contains(status.Error, "安全模式") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("safe mode change did not stop the update: %+v", m.Status())
		}
	}
	checkFile(t, o.Executable, oldBinary)
	checkEmptyDownloads(t, o.DataDir)
}

func checkFile(t *testing.T, file, expected string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil || string(data) != expected {
		t.Fatalf("unexpected %s: %q, %v", filepath.Base(file), data, err)
	}
}

func checkEmptyDownloads(t *testing.T, dataDir string) {
	t.Helper()
	files, err := os.ReadDir(filepath.Join(dataDir, "updates", "downloads"))
	if len(files) != 0 || err != nil && !os.IsNotExist(err) {
		t.Fatalf("download not cleaned up: %v, %v", files, err)
	}
}
