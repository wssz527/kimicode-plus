package desktop

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func fuseBinary(version byte, fuses string) []byte {
	var buffer bytes.Buffer
	buffer.WriteString("prefix-bytes-before-the-sentinel")
	buffer.WriteString(fuseSentinelPrefix)
	buffer.WriteString(fuseSentinelSuffix)
	buffer.WriteByte(version)
	buffer.WriteByte(byte(len(fuses)))
	buffer.WriteString(fuses)
	buffer.WriteString("suffix")
	return buffer.Bytes()
}

func TestInspectorFuseAcceptsEnabledSignal(t *testing.T) {
	// Electron 45 layout observed on this machine: version 1, 9 fuses, and the
	// node inspector signal fuse at index 3 reads '1'.
	if err := InspectorFuse(fuseBinary(1, "101100001")); err != nil {
		t.Fatalf("enabled fuse rejected: %v", err)
	}
}

func TestInspectorFuseRejectsDisabledSignal(t *testing.T) {
	err := InspectorFuse(fuseBinary(1, "101000001"))
	if !errors.Is(err, ErrInspectorDisabled) {
		t.Fatalf("disabled signal fuse was accepted: %v", err)
	}
}

func TestInspectorFuseRejectsUnknownLayout(t *testing.T) {
	cases := map[string][]byte{
		"no sentinel":     []byte("nothing here"),
		"wrong version":   fuseBinary(2, "101100001"),
		"truncated block": fuseBinary(1, "101"),
		"non-ascii fuse":  fuseBinary(1, "101\x00\0000001"),
		"empty binary":    {},
	}
	for name, binary := range cases {
		if err := InspectorFuse(binary); !errors.Is(err, ErrUnknownFuseLayout) {
			t.Fatalf("%s: expected unknown layout, got %v", name, err)
		}
	}
}

func TestIsDebugInstanceSkipsTestLaunches(t *testing.T) {
	executable := KimiExecutable
	for _, command := range []string{
		executable + " --user-data-dir=/tmp/isolated",
		executable + " --remote-debugging-port=9222",
		executable + " --inspect=9229",
		executable + " --no-sandbox --user-data-dir=/tmp/other --foo",
	} {
		if !IsDebugInstance(command) {
			t.Fatalf("debug instance accepted: %s", command)
		}
	}
	for _, command := range []string{
		executable,
		executable + " --no-first-run",
		executable + " --lang=zh-Hans",
	} {
		if IsDebugInstance(command) {
			t.Fatalf("normal instance rejected: %s", command)
		}
	}
}

func TestParseMainPIDAcceptsDockLaunchWithoutProfile(t *testing.T) {
	executable := KimiExecutable
	output := []byte(" 201 " + executable + "\n 202 " + executable + " --type=renderer\n")
	pid, err := parseMainPID(output, executable, "")
	if err != nil || pid != 201 {
		t.Fatalf("dock launch not discovered: pid=%d err=%v", pid, err)
	}
	// A profile filter still narrows the match to the isolated instance.
	output = []byte(" 201 " + executable + "\n 203 " + executable + " --user-data-dir=/tmp/isolated\n")
	pid, err = parseMainPID(output, executable, "/tmp/isolated")
	if err != nil || pid != 203 {
		t.Fatalf("profile filter failed: pid=%d err=%v", pid, err)
	}
	if _, err = parseMainPID(output, executable, "/tmp/absent"); err == nil {
		t.Fatal("absent profile matched an instance")
	}
}

func TestParseMainPIDRefusesAmbiguousHosts(t *testing.T) {
	executable := KimiExecutable
	output := []byte(" 301 " + executable + "\n 302 " + executable + "\n")
	if _, err := parseMainPID(output, executable, ""); err == nil || !strings.Contains(err.Error(), "2") {
		t.Fatalf("two main processes were accepted: %v", err)
	}
}

func TestParseProcessIdentityReadsStartTimeAndOwner(t *testing.T) {
	line := []byte("  512  501 Sun Nov 16 10:11:12 2026 /Applications/Kimi Code.app/Contents/MacOS/Kimi Code\n")
	info, err := parseProcessIdentity(line)
	if err != nil {
		t.Fatal(err)
	}
	if info.PID != 512 || info.UID != 501 {
		t.Fatalf("identity misread: %+v", info)
	}
	if info.Started != "Sun Nov 16 10:11:12 2026" {
		t.Fatalf("start time lost: %q", info.Started)
	}
	// The executable contains spaces, so it must be reassembled from the tail.
	if info.Executable != KimiExecutable {
		t.Fatalf("executable misread: %q", info.Executable)
	}
	if _, err = parseProcessIdentity([]byte("")); err == nil {
		t.Fatal("empty identity accepted")
	}
	if _, err = parseProcessIdentity([]byte("not-a-pid 501 Sun Nov 16 10:11:12 2026 /x")); err == nil {
		t.Fatal("garbage identity accepted")
	}
}

func TestInspectorDeadlineExpressionGuardsProcessAndCloses(t *testing.T) {
	expression := inspectorDeadlineExpression(4242)
	if !strings.Contains(expression, "process.pid!==4242") {
		t.Fatal("deadline expression does not pin the process")
	}
	if !strings.Contains(expression, "12000") {
		t.Fatal("deadline window is not the 12s budget")
	}
	if !strings.Contains(expression, "inspector.close()") && !strings.Contains(expression, "inspector.close") {
		t.Fatal("deadline expression does not close the inspector")
	}
	// The deadline must be armed before any identity read returns.
	if !strings.HasPrefix(expression, "(()=>{if(process.pid!==") {
		t.Fatalf("identity guard does not run first: %s", expression)
	}
	closeExpression := closeInspectorExpression(4242)
	if !strings.Contains(closeExpression, "process.pid===4242") {
		t.Fatal("close expression is not pinned to the host")
	}
	if !strings.Contains(closeExpression, "clearTimeout") {
		t.Fatal("close expression leaves the deadline armed")
	}
}

func TestConsentIsRequiredForTheDailyInstance(t *testing.T) {
	if ErrConsentRequired == nil {
		t.Fatal("missing consent error")
	}
	// The consent text must say what will happen, not just that access is denied.
	message := ErrConsentRequired.Error()
	for _, fragment := range []string{"日常客户端", "界面", "授权"} {
		if !strings.Contains(message, fragment) {
			t.Fatalf("consent message missing %q: %s", fragment, message)
		}
	}
}
