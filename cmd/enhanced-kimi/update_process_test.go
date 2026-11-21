package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"enhancedkimi/internal/server"
	"enhancedkimi/internal/storage"
	"enhancedkimi/internal/updater"
)

const (
	previousTestVersion = "0.2.0"
	targetTestVersion   = "0.2.1"
	restartChildCert    = "EKD_F3_TEST_CERTIFICATE"
	restartChildAPI     = "EKD_F3_TEST_API"
	restartChildDir     = "EKD_F3_TEST_WORKDIR"
	restartChildAltDir  = "EKD_F3_TEST_ALT_WORKDIR"
)

func TestMain(m *testing.M) {
	if certificate := os.Getenv(restartChildCert); certificate != "" {
		if err := runUpdateTestChild(certificate); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runUpdateTestChild(certificate string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	// A resident copy may run from the data directory instead of the download
	// directory; both are allowed, any other directory is not.
	if !sameTestDirectory(cwd, os.Getenv(restartChildDir)) && !sameTestDirectory(cwd, os.Getenv(restartChildAltDir)) {
		return fmt.Errorf("working directory was not preserved: %q", cwd)
	}
	path, err := os.Executable()
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	var trailer [64]byte
	if _, err = file.Seek(-int64(len(trailer)), io.SeekEnd); err == nil {
		_, err = file.Read(trailer[:])
	}
	_ = file.Close()
	if err != nil {
		return err
	}
	server.Version = previousTestVersion
	switch {
	case bytes.HasSuffix(trailer[:], []byte(releaseTrailer("exits"))):
		os.Exit(23)
	case bytes.HasSuffix(trailer[:], []byte(releaseTrailer("timeout"))):
		for {
			time.Sleep(time.Hour)
		}
	case bytes.HasSuffix(trailer[:], []byte(releaseTrailer("schema"))):
		// The candidate migrates the database forward and then dies before the
		// health check. The previous release cannot open that schema, so a
		// binary-only rollback would leave the plugins module FAULTED.
		if err := breakSchemaCompatibility(); err != nil {
			return err
		}
		os.Exit(24)
	case bytes.HasSuffix(trailer[:], []byte(releaseTrailer("success"))):
		server.Version = targetTestVersion
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(certificate)) {
		return errors.New("invalid test release certificate")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots}
	return runCore(os.Getenv(restartChildAPI), &http.Client{Transport: transport})
}

func sameTestDirectory(actual, expected string) bool {
	if expected == "" {
		return false
	}
	actualInfo, err := os.Stat(actual)
	if err != nil {
		return false
	}
	expectedInfo, err := os.Stat(expected)
	if err != nil {
		return false
	}
	return os.SameFile(actualInfo, expectedInfo)
}

func releaseTrailer(scenario string) string { return "\nEKD_F3_TEST_RELEASE:" + scenario + "\n" }

// breakSchemaCompatibility commits a schema version the previous release refuses
// to open, which is exactly what a real forward migration would do.
func breakSchemaCompatibility() error {
	dir := ""
	for index := 0; index+1 < len(os.Args); index++ {
		if os.Args[index] == "--data-dir" {
			dir = os.Args[index+1]
		}
	}
	if dir == "" {
		return errors.New("candidate core did not receive --data-dir")
	}
	db, err := storage.Open(filepath.Join(dir, "enhancement.db"))
	if err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE schema_versions SET version = 99 WHERE module = 'plugins'`)
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	return err
}

func TestCoreUpdateProcesses(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("requires a supported update platform")
	}
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	previous, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, state, version, reason, externalRelease string
	}{
		{"success", updater.Updated, targetTestVersion, "", "EKD_F3_TEST_SUCCESS_RELEASE_DIR"},
		{"exits", updater.RolledBack, previousTestVersion, "通过健康检查前退出", "EKD_F3_TEST_EXIT_RELEASE_DIR"},
		{"timeout", updater.RolledBack, previousTestVersion, "检查超时", ""},
		{"managed-copy", updater.Updated, targetTestVersion, "", ""},
		{"db-rollback", updater.RolledBack, previousTestVersion, "通过健康检查前退出", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			managed := tc.name == "managed-copy"
			if managed && runtime.GOOS != "darwin" {
				t.Skip("managed LaunchAgent copy is macOS only")
			}
			if tc.name == "timeout" && testing.Short() {
				t.Skip("waits for the production health deadline")
			}
			target := runtime.GOOS + "-" + runtime.GOARCH
			extension, executableExtension := ".tar.gz", ""
			if runtime.GOOS == "windows" {
				extension, executableExtension = ".zip", ".exe"
			}
			archiveName := "enhanced-kimi-" + targetTestVersion + "-" + target + extension
			var archive, checksum []byte
			if dir := os.Getenv(tc.externalRelease); dir != "" {
				archive = readFixtureFile(t, filepath.Join(dir, archiveName))
				checksum = readFixtureFile(t, filepath.Join(dir, "SHA256SUMS"))
			} else {
				scenario := tc.name
				if managed {
					scenario = "success"
				}
				if scenario == "db-rollback" {
					scenario = "schema"
				}
				image := append(bytes.Clone(previous), releaseTrailer(scenario)...)
				archive = archiveTestBinary(t, "enhanced-kimi-"+target+executableExtension, image)
				checksum = fmt.Appendf(nil, "%x  %s\n", sha256.Sum256(archive), archiveName)
			}
			workDir := filepath.Join(t.TempDir(), "Kimi's workspace")
			if err := os.Mkdir(workDir, 0o700); err != nil {
				t.Fatal(err)
			}
			installed := filepath.Join(workDir, "enhanced-kimi"+executableExtension)
			if err := os.WriteFile(installed, previous, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workDir, "THIRD_PARTY_NOTICES.txt"), []byte("old notices"), 0o644); err != nil {
				t.Fatal(err)
			}
			dataDir := filepath.Join(t.TempDir(), "Kimi's runtime")
			updateTarget := installed
			fakeHome := t.TempDir()
			var managedPlist string
			if managed {
				parent, err := filepath.EvalSymlinks(filepath.Dir(dataDir))
				if err != nil {
					t.Fatal(err)
				}
				dataDir = filepath.Join(parent, filepath.Base(dataDir))
				updateTarget = filepath.Join(dataDir, "resident", "enhanced-kimi")
				agents := filepath.Join(fakeHome, "Library", "LaunchAgents")
				if err := os.MkdirAll(agents, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(updateTarget), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(updateTarget, previous, 0o700); err != nil {
					t.Fatal(err)
				}
				managedPlist = filepath.Join(agents, "com.user.enhanced-kimi.core-managed.plist")
			}
			runtimePath := filepath.Join(dataDir, "runtime.json")
			updatePath := filepath.Join(dataDir, "updates", "result.json")
			ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
			defer cancel()
			client := &http.Client{Timeout: time.Second}
			releases := fixtureReleaseServer(t, archiveName, archive, checksum)
			certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: releases.Certificate().Raw})
			cmd := exec.CommandContext(ctx, installed, "--data-dir", dataDir, "--workspace", workDir, "--port", "0")
			cmd.Dir = workDir
			cmd.Stderr = os.Stderr
			// The resident copy must not inherit the download directory as its
			// working directory, so the managed scenario starts in the data dir.
			cmd.Env = append(os.Environ(), restartChildCert+"="+string(certificate), restartChildAPI+"="+releases.URL,
				restartChildDir+"="+workDir, "EKD_UPDATE_REPO=f3/test-release")
			if managed {
				// The restarted resident copy must run from the data directory, not
				// from the download folder the user may delete.
				cmd.Env = append(cmd.Env, "HOME="+fakeHome, restartChildAltDir+"="+dataDir)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			var finalPID int
			defer func() {
				if finalPID == 0 {
					finalPID = readTestRuntime(runtimePath).PID
				}
				if finalPID > 0 && finalPID != cmd.Process.Pid {
					killTestProcess(finalPID)
				}
			}()
			oldExited := make(chan error, 1)
			go func() { oldExited <- cmd.Wait() }()
			defer func() { _ = cmd.Process.Kill() }()
			before := waitTestRuntime(t, ctx, runtimePath, func(boot testRuntime) bool {
				return boot.PID == cmd.Process.Pid && healthyTestCore(client, boot)
			})
			if before.Version != previousTestVersion {
				t.Fatalf("initial startup: version=%q", before.Version)
			}
			if managed {
				plist := "<plist version=\"1.0\"><dict><key>ProgramArguments</key><array><string>" + strings.ReplaceAll(updateTarget, "'", "&apos;") + "</string></array></dict></plist>"
				if err := os.WriteFile(managedPlist, []byte(plist), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var status updater.Status
			if err := testCoreRequest(client, before, http.MethodPost, "update/check", http.StatusOK, &status); err != nil ||
				status.State != updater.Available || !status.CanApply || status.LatestVersion != targetTestVersion {
				t.Fatalf("check returned %+v: %v", status, err)
			}
			if err := testCoreRequest(client, before, http.MethodPost, "update/apply", http.StatusAccepted, &status); err != nil || status.State != updater.Downloading {
				t.Fatalf("apply returned %+v: %v", status, err)
			}
			select {
			case err := <-oldExited:
				if err != nil {
					t.Fatalf("original core did not stop cleanly: %v", err)
				}
			case <-ctx.Done():
				t.Fatalf("original core did not stop: %v, status=%+v", ctx.Err(), status)
			}
			var updateStatus updater.Status
			after := waitTestRuntime(t, ctx, runtimePath, func(boot testRuntime) bool {
				if boot.PID > 0 && boot.PID != before.PID && healthyTestCore(client, boot) &&
					testCoreRequest(client, boot, http.MethodGet, "update/status", http.StatusOK, &updateStatus) == nil && updateStatus.State == tc.state {
					return true
				}
				var current updater.UpdateResult
				if raw, err := os.ReadFile(updatePath); err == nil && json.Unmarshal(raw, &current) == nil && current.State == updater.Failed {
					t.Fatalf("supervisor recorded failure: %+v", current)
				}
				return false
			})
			finalPID = after.PID
			if after.Version != tc.version || after.URL != before.URL || after.PairingToken == before.PairingToken {
				t.Fatalf("recovered core: version=%q samePort=%v freshPairingToken=%v", after.Version, after.URL == before.URL, after.PairingToken != before.PairingToken)
			}
			if current := updateStatus.LastAttempt; current == nil || current.State != tc.state || current.TargetVersion != targetTestVersion ||
				current.PreviousVersion != previousTestVersion || current.CompletedAt == "" ||
				(tc.reason == "" && current.Error != "") || !strings.Contains(current.Error, tc.reason) {
				t.Fatalf("unexpected final status: %+v, attempt=%+v", updateStatus, updateStatus.LastAttempt)
			}
			var saved updater.UpdateResult
			if err := json.Unmarshal(readFixtureFile(t, updatePath), &saved); err != nil || saved != *updateStatus.LastAttempt {
				t.Fatalf("result was not persisted: %+v: %v", saved, err)
			}
			if tc.name == "db-rollback" {
				// The rollback restored the database: the previous release restarted
				// with every module READY rather than FAULTED.
				modules := readTestModules(t, client, after)
				for _, module := range modules {
					if module.State == "FAULTED" {
						t.Fatalf("module %s stayed faulted after rollback: %+v", module.ID, module)
					}
				}
				ready := false
				for _, module := range modules {
					if module.ID == "plugins" && module.State == "READY" {
						ready = true
					}
				}
				if !ready {
					t.Fatalf("plugins module was not restored: %+v", modules)
				}
				waitUpdateCondition(t, ctx, "transaction backup to be cleaned", func() bool {
					entries, err := os.ReadDir(filepath.Join(dataDir, "updates"))
					if err != nil {
						return false
					}
					for _, entry := range entries {
						if entry.IsDir() && strings.HasPrefix(entry.Name(), "tx-") {
							return false
						}
					}
					_, activeErr := os.Stat(filepath.Join(dataDir, "updates", "active-tx.json"))
					return errors.Is(activeErr, os.ErrNotExist)
				})
			}
			waitUpdateCondition(t, ctx, "staged files to be cleaned", func() bool {
				stages, err := os.ReadDir(filepath.Join(dataDir, "updates", "downloads"))
				return err == nil && len(stages) == 0
			})
			if _, err := os.Stat(updateTarget + ".bak"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("backup remained after %s: %v", tc.state, err)
			}
			if managed && !bytes.Equal(previous, readFixtureFile(t, installed)) {
				t.Fatal("update changed the originally downloaded executable")
			}
			current := readFixtureFile(t, updateTarget)
			if tc.state == updater.Updated {
				if bytes.Equal(current, previous) {
					t.Fatal("successful update did not replace the previous executable")
				}
				if string(readFixtureFile(t, filepath.Join(filepath.Dir(updateTarget), "THIRD_PARTY_NOTICES.txt"))) == "old notices" {
					t.Fatal("successful update did not install notices")
				}
				if len(readFixtureFile(t, filepath.Join(filepath.Dir(updateTarget), "LICENSE"))) == 0 {
					t.Fatal("successful update installed an empty license")
				}
			} else {
				if !bytes.Equal(current, previous) {
					t.Fatal("rollback did not restore the previous executable")
				}
				if got := string(readFixtureFile(t, filepath.Join(workDir, "THIRD_PARTY_NOTICES.txt"))); got != "old notices" {
					t.Fatalf("rollback changed existing notices: %q", got)
				}
				if err := testCoreRequest(client, after, http.MethodPost, "update/check", http.StatusOK, &status); err != nil || status.LastAttempt == nil || *status.LastAttempt != saved {
					t.Fatalf("checking the release hid rollback diagnostics: %+v: %v", status, err)
				}
			}
			if _, err := os.Stat(filepath.Join(dataDir, "update-restart.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("restart marker remained after startup: %v", err)
			}
		})
	}
}

type testRuntime struct {
	URL, PairingToken, Version string
	PID                        int
}

func readTestRuntime(path string) testRuntime {
	var record testRuntime
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &record)
	}
	return record
}

func waitTestRuntime(t *testing.T, ctx context.Context, path string, ready func(testRuntime) bool) testRuntime {
	t.Helper()
	var record testRuntime
	waitUpdateCondition(t, ctx, "core to become healthy with the expected update state", func() bool {
		record = readTestRuntime(path)
		return ready(record)
	})
	return record
}

func waitUpdateCondition(t *testing.T, ctx context.Context, description string, ready func() bool) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s: %v", description, ctx.Err())
		}
	}
}

func readTestModules(t *testing.T, client *http.Client, record testRuntime) []server.Module {
	t.Helper()
	var health struct{ Modules []server.Module }
	if err := testCoreRequest(client, record, http.MethodGet, "health", http.StatusOK, &health); err != nil {
		t.Fatalf("cannot read module health: %v", err)
	}
	return health.Modules
}

func healthyTestCore(client *http.Client, record testRuntime) bool {
	var health struct{ Status, Version string }
	return record.URL != "" && testCoreRequest(client, record, http.MethodGet, "health", http.StatusOK, &health) == nil &&
		health.Status == "OK" && health.Version == record.Version
}

func testCoreRequest(client *http.Client, record testRuntime, method, endpoint string, expectedCode int, target any) error {
	request, err := http.NewRequest(method, record.URL+"/enhance/v1/"+endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+record.PairingToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != expectedCode {
		return fmt.Errorf("%s %s: got HTTP %d, want %d", method, endpoint, response.StatusCode, expectedCode)
	}
	return json.NewDecoder(response.Body).Decode(target)
}

func killTestProcess(pid int) {
	if process, err := os.FindProcess(pid); err == nil {
		_ = process.Kill()
		if runtime.GOOS == "windows" {
			_, _ = process.Wait()
		}
		_ = process.Release()
	}
}

func readFixtureFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fixtureReleaseServer(t *testing.T, name string, archive, checksum []byte) *httptest.Server {
	t.Helper()
	releases := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/f3/test-release/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v" + targetTestVersion, "assets": []map[string]any{
				{"name": name, "size": len(archive), "browser_download_url": "https://" + r.Host + "/archive"},
				{"name": "SHA256SUMS", "browser_download_url": "https://" + r.Host + "/checksum"},
			}})
		case "/archive":
			_, _ = w.Write(archive)
		case "/checksum":
			_, _ = w.Write(checksum)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(releases.Close)
	return releases
}

func archiveTestBinary(t *testing.T, name string, binary []byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	files := []struct {
		name string
		data []byte
		mode int64
	}{{name, binary, 0o755}, {"THIRD_PARTY_NOTICES.txt", []byte("new notices"), 0o644}, {"LICENSE", []byte("new license"), 0o644}}
	if runtime.GOOS == "windows" {
		writer := zip.NewWriter(&archive)
		for _, file := range files {
			entry, err := writer.Create(file.name)
			if err == nil {
				_, err = entry.Write(file.data)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		compressed, err := gzip.NewWriterLevel(&archive, gzip.BestSpeed)
		if err != nil {
			t.Fatal(err)
		}
		writer := tar.NewWriter(compressed)
		for _, file := range files {
			if err := writer.WriteHeader(&tar.Header{Name: file.name, Mode: file.mode, Size: int64(len(file.data))}); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write(file.data); err != nil {
				t.Fatal(err)
			}
		}
		if err := errors.Join(writer.Close(), compressed.Close()); err != nil {
			t.Fatal(err)
		}
	}
	return archive.Bytes()
}
