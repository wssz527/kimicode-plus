package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// plistProgramArguments returns the ProgramArguments array of a rendered plist.
// It parses the XML by hand so the test fails when the renderer changes shape,
// rather than leaning on a helper the renderer could drift from.
func plistProgramArguments(t *testing.T, plist string) []string {
	t.Helper()
	start := strings.Index(plist, "<key>ProgramArguments</key>")
	if start < 0 {
		t.Fatal("plist has no ProgramArguments")
	}
	open := strings.Index(plist[start:], "<array>")
	closeTag := strings.Index(plist[start:], "</array>")
	if open < 0 || closeTag < 0 || closeTag < open {
		t.Fatal("ProgramArguments is not an array")
	}
	body := plist[start+open+len("<array>") : start+closeTag]
	values := []string{}
	for {
		begin := strings.Index(body, "<string>")
		if begin < 0 {
			break
		}
		end := strings.Index(body[begin:], "</string>")
		if end < 0 {
			t.Fatal("unterminated string in ProgramArguments")
		}
		values = append(values, unescapeXML(body[begin+len("<string>"):begin+end]))
		body = body[begin+end+len("</string>"):]
	}
	return values
}

func unescapeXML(value string) string {
	replacer := strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'",
	)
	return replacer.Replace(value)
}

// TestLaunchAgentPlistCarriesTheAttachCredentials is the regression guard for the
// agent that reported itself enabled while every wake-up failed: a launchd run
// has no other channel for the core address or the pairing token.
func TestLaunchAgentPlistCarriesTheAttachCredentials(t *testing.T) {
	config := testAgentConfig()
	plist, err := LaunchAgentPlist(config)
	if err != nil {
		t.Fatal(err)
	}
	program := plistProgramArguments(t, plist)
	if len(program) < 2 {
		t.Fatalf("program arguments too short: %v", program)
	}
	if program[0] != config.Executable {
		t.Fatalf("program does not start with the executable: %v", program)
	}
	if program[1] != "attach" {
		t.Fatalf("program does not run the attach subcommand: %v", program)
	}
	// Credentials must be the last pair, so a token containing "=" or "-" cannot
	// be read as another flag.
	if got := program[len(program)-4:]; strings.Join(got, " ") != "--core-url "+config.CoreURL+" --pairing-token "+config.PairingToken {
		t.Fatalf("credentials are not rendered last: %v", got)
	}
}

// TestLaunchAgentProgramArgumentsMatchAttachArguments pins the rendered plist to
// the single argument builder, so the two cannot drift again.
func TestLaunchAgentProgramArgumentsMatchAttachArguments(t *testing.T) {
	config := testAgentConfig()
	plist, err := LaunchAgentPlist(config)
	if err != nil {
		t.Fatal(err)
	}
	program := plistProgramArguments(t, plist)
	want := append([]string{config.Executable}, AttachArguments(config)...)
	if strings.Join(program, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("plist arguments drifted from AttachArguments:\n got %v\nwant %v", program, want)
	}
	// An agent with no profile and daily consent still matches its builder.
	config.Profile = ""
	config.AllowDailyInstance = true
	plist, err = LaunchAgentPlist(config)
	if err != nil {
		t.Fatal(err)
	}
	program = plistProgramArguments(t, plist)
	want = append([]string{config.Executable}, AttachArguments(config)...)
	if strings.Join(program, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("daily agent arguments drifted:\n got %v\nwant %v", program, want)
	}
}

// TestLaunchAgentPlistIsAValidPlist asks the system parser to read the rendered
// XML: launchd rejects a malformed plist at bootstrap, which would surface as a
// settings page that silently never recovers.
func TestLaunchAgentPlistIsAValidPlist(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("plutil is macOS only")
	}
	config := testAgentConfig()
	config.CoreURL = "http://127.0.0.1:8321/?a=1&b=<2>"
	config.PairingToken = strings.Repeat("t", 20) + `"&<>` + strings.Repeat("t", 24)
	plist, err := LaunchAgentPlist(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agent.plist")
	if err = os.WriteFile(path, []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("/usr/bin/plutil", "-lint", path).CombinedOutput()
	if err != nil {
		t.Fatalf("rendered plist is not valid XML: %v\n%s", err, output)
	}
	// And the system parser must see the same credentials the CLI validates.
	read, err := exec.Command("/usr/bin/plutil", "-extract", "ProgramArguments", "json", "-o", "-", path).Output()
	if err != nil {
		t.Fatalf("ProgramArguments could not be read back: %v", err)
	}
	var arguments []string
	if err = json.Unmarshal(read, &arguments); err != nil {
		t.Fatalf("ProgramArguments is not a string array: %v", err)
	}
	want := append([]string{config.Executable}, AttachArguments(config)...)
	if strings.Join(arguments, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("plutil read a different command line:\n got %v\nwant %v", arguments, want)
	}
}

func TestLaunchAgentPlistEscapesCredentials(t *testing.T) {
	config := testAgentConfig()
	config.CoreURL = "http://127.0.0.1:8321/?a=1&b=<2>"
	config.PairingToken = strings.Repeat("t", 20) + `"&<>` + strings.Repeat("t", 24)
	plist, err := LaunchAgentPlist(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plist, "<2>") || strings.Contains(plist, `"&<>`) {
		t.Fatal("credentials were not XML-escaped")
	}
	program := plistProgramArguments(t, plist)
	if program[len(program)-3] != config.CoreURL {
		t.Fatalf("escaped core URL did not survive a round trip: %q", program[len(program)-3])
	}
	if program[len(program)-1] != config.PairingToken {
		t.Fatalf("escaped token did not survive a round trip: %q", program[len(program)-1])
	}
}

func TestLaunchAgentPlistRefusesAnAgentThatCannotAttach(t *testing.T) {
	missingURL := testAgentConfig()
	missingURL.CoreURL = ""
	if _, err := LaunchAgentPlist(missingURL); err == nil {
		t.Fatal("agent rendered without a core URL")
	}
	shortToken := testAgentConfig()
	shortToken.PairingToken = "too-short"
	if _, err := LaunchAgentPlist(shortToken); err == nil {
		t.Fatal("agent rendered with a placeholder token")
	}
	noData := testAgentConfig()
	noData.DataDir = ""
	if _, err := LaunchAgentPlist(noData); err == nil {
		t.Fatal("agent rendered without a data directory")
	}
}

func TestInstallLaunchAgentWritesThePlistOwnedByTheUser(t *testing.T) {
	withFakeLaunch(t)
	path, err := LaunchAgentPath(LaunchAgentLabel)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	config := testAgentConfig()
	config.DataDir = t.TempDir()
	if _, err = InstallLaunchAgent(config); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// The plist carries the pairing token, so it must stay readable by the user
	// only.
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("plist permissions are %o, want 600", mode)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	program := plistProgramArguments(t, string(data))
	found := false
	for _, value := range program {
		if value == config.PairingToken {
			found = true
		}
	}
	if !found {
		t.Fatal("installed plist does not carry the pairing token")
	}
}

func TestLayoutForPinsVerifiedElectronOnly(t *testing.T) {
	layout, err := LayoutFor(SupportedElectron)
	if err != nil {
		t.Fatalf("the supported Electron version has no fuse layout: %v", err)
	}
	if layout.Electron != SupportedElectron || layout.NodeInspectorSignal != 3 || layout.Count != 9 {
		t.Fatalf("unexpected layout: %+v", layout)
	}
	// Every host contract must have a pinned fuse layout, otherwise a supported
	// app version would pass the host check and fail the fuse check.
	for _, host := range SupportedHosts {
		if _, err := LayoutFor(host.Electron); err != nil {
			t.Fatalf("%s host contract has no fuse layout: %v", host.AppVersion, err)
		}
	}
	for _, version := range []string{"46.0.0", "44.0.0", ""} {
		if _, err := LayoutFor(version); !errors.Is(err, ErrUnverifiedFuseVersion) {
			t.Fatalf("%q was accepted as a verified Electron version: %v", version, err)
		}
	}
}

func TestCheckInspectorFuseForFailsClosedOnUnknownElectron(t *testing.T) {
	// The version check runs before any file is touched, so no binary is needed.
	if err := CheckInspectorFuseFor("/tmp/absent-framework", "46.0.0"); !errors.Is(err, ErrUnverifiedFuseVersion) {
		t.Fatalf("unknown Electron version did not fail closed: %v", err)
	}
}

func TestInspectorFuseAtRejectsAMovedLayout(t *testing.T) {
	layout, err := LayoutFor(SupportedElectron)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"fewer fuses":     fuseBinary(1, "10110000"),
		"more fuses":      fuseBinary(1, "1011000011"),
		"wrong version":   fuseBinary(2, "101100001"),
		"oversized count": fuseBinary(1, strings.Repeat("1", fuseMaxCount+1)),
	}
	for name, binary := range cases {
		if err := InspectorFuseAt(binary, layout); !errors.Is(err, ErrUnknownFuseLayout) && !errors.Is(err, ErrFuseBlockTooLarge) {
			t.Fatalf("%s: layout change was not detected: %v", name, err)
		}
	}
	if err := InspectorFuseAt(fuseBinary(1, "101100001"), layout); err != nil {
		t.Fatalf("matching layout rejected: %v", err)
	}
}

// writeFramework writes a synthetic framework whose fuse block sits at offset.
func writeFramework(t *testing.T, size int, offset int) string {
	t.Helper()
	binary := bytes.Repeat([]byte{'x'}, size)
	copy(binary[offset:], fuseBinary(1, "101100001"))
	path := filepath.Join(t.TempDir(), "framework")
	if err := os.WriteFile(path, binary, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckInspectorFuseReadsOnlyBoundedChunks(t *testing.T) {
	layout, err := LayoutFor(SupportedElectron)
	if err != nil {
		t.Fatal(err)
	}
	// A 9MB file with the block near the end: the scan must reach it by streaming,
	// which a fixed head/middle/tail window would miss.
	path := writeFramework(t, 9<<20, 8<<20)
	if err := scanFuseLayout(path, layout); err != nil {
		t.Fatalf("streaming scan missed a block deep in the file: %v", err)
	}
	// The same binary with the block disabled is reported, not skipped over.
	binary, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	disabled := bytes.Replace(binary, []byte("101100001"), []byte("101000001"), 1)
	disabledPath := filepath.Join(t.TempDir(), "disabled")
	if err = os.WriteFile(disabledPath, disabled, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = scanFuseLayout(disabledPath, layout); !errors.Is(err, ErrInspectorDisabled) {
		t.Fatalf("disabled signal fuse was not reported: %v", err)
	}
}

func TestScanFuseLayoutFindsBlocksAcrossChunkBoundaries(t *testing.T) {
	layout, err := LayoutFor(SupportedElectron)
	if err != nil {
		t.Fatal(err)
	}
	// Straddle the sentinel over a chunk boundary so the carry logic is exercised.
	offset := fuseChunkSize - 5
	path := writeFramework(t, 3<<20, offset)
	if err = scanFuseLayout(path, layout); err != nil {
		t.Fatalf("block on a chunk boundary was missed: %v", err)
	}
	// No sentinel anywhere: bounded reads still terminate with a verdict.
	empty := filepath.Join(t.TempDir(), "empty")
	if err = os.WriteFile(empty, bytes.Repeat([]byte{'x'}, 3<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = scanFuseLayout(empty, layout); !errors.Is(err, ErrUnknownFuseLayout) {
		t.Fatalf("a file without a fuse block was accepted: %v", err)
	}
}

func TestCheckInspectorFuseCachesTheVerdictPerFile(t *testing.T) {
	layout, err := LayoutFor(SupportedElectron)
	if err != nil {
		t.Fatal(err)
	}
	path := writeFramework(t, 4<<20, 3<<20)
	scans := 0
	previous := fuseScan
	fuseScan = func(scanned string, applied FuseLayout) error {
		scans++
		return previous(scanned, applied)
	}
	t.Cleanup(func() { fuseScan = previous; resetFuseCache() })
	resetFuseCache()
	for i := 0; i < 3; i++ {
		if err = CheckInspectorFuseFor(path, SupportedElectron); err != nil {
			t.Fatalf("call %d failed: %v", i, err)
		}
	}
	// One scan serves every settings refresh: the framework is 200MB on a real
	// install and attach.status is polled.
	if scans != 1 {
		t.Fatalf("framework was scanned %d times for one unchanged file", scans)
	}
	// A rewritten binary invalidates the cached verdict.
	if err = os.WriteFile(path, bytes.Repeat([]byte{'y'}, 4<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = CheckInspectorFuseFor(path, SupportedElectron); !errors.Is(err, ErrUnknownFuseLayout) {
		t.Fatalf("stale verdict served after the file changed: %v", err)
	}
	if scans != 2 {
		t.Fatalf("changed file was not rescanned: %d scans", scans)
	}
	_ = layout
}

func TestScanFuseLayoutRejectsAShortFile(t *testing.T) {
	layout, err := LayoutFor(SupportedElectron)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "tiny")
	if err = os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = scanFuseLayout(path, layout); !errors.Is(err, ErrUnknownFuseLayout) {
		t.Fatalf("a file shorter than a fuse block was accepted: %v", err)
	}
	if err = scanFuseLayout(filepath.Join(t.TempDir(), "absent"), layout); !errors.Is(err, ErrUnknownFuseLayout) {
		t.Fatalf("an unreadable framework was accepted: %v", err)
	}
}

func TestRealFrameworkFuseBlockIsFound(t *testing.T) {
	if _, err := os.Stat(KimiFramework); err != nil {
		t.Skip("Kimi Code is not installed")
	}
	// The shipped Electron build must pass the pinned layout; a failure here means
	// this machine's client is not one the loader can attach to.
	if err := CheckInspectorFuse(KimiFramework); err != nil && !errors.Is(err, ErrInspectorDisabled) {
		t.Fatalf("installed framework rejected: %v", err)
	}
}
