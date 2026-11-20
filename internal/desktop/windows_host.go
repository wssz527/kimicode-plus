package desktop

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"enhancedkimi/internal/platform"
)

const (
	WindowsKimiExecutableName  = "Kimi Code.exe"
	windowsLauncherStateFile   = "windows-launcher-state.json"
	windowsLauncherConfigFile  = "launcher-config.json"
	windowsLauncherScriptName  = "enhanced-kimi-launcher.vbs"
	windowsPEHeaderOffsetField = 0x3c
	windowsPEMachineAMD64      = 0x8664
	windowsPEMachineARM64      = 0xaa64
	windowsFuseScanChunkSize   = 1 << 20
	windowsFuseScanCarrySize   = len(fuseSentinelPrefix) + len(fuseSentinelSuffix) + fuseBlockPrefixLen + fuseMaxCount + 32
)

var (
	ErrWindowsInvalidPE     = errors.New("目标程序不是有效的 Windows PE x64/ARM64 可执行文件")
	ErrWindowsNotElectron   = errors.New("目标程序不包含 Electron 运行时特征，拒绝接入")
	ErrWindowsPortNotOwned  = errors.New("调试端口的监听者不是已校验的 Kimi Code 主进程，拒绝接入")
	ErrWindowsPortNotLocal  = errors.New("调试端口监听在非本机环回地址，存在暴露风险，拒绝接入")
	ErrWindowsDebugInstance = errors.New("检测到非本启动器拉起的调试或测试实例，增强不会自动接入")
)

// WindowsProcessInfo captures the identity of a running Kimi Code process on Windows.
type WindowsProcessInfo struct {
	PID         int    `json:"pid"`
	SessionID   int    `json:"sessionId"`
	OwnerSID    string `json:"ownerSid"`
	Executable  string `json:"executable"`
	CommandLine string `json:"commandLine"`
	Started     string `json:"started"`
}

// WindowsLauncherState records the exact Kimi Code process and loopback debug
// port launched by our Windows launcher so we can distinguish launcher-managed
// daily instances from unrelated debugging sessions.
type WindowsLauncherState struct {
	PID           int    `json:"pid"`
	DebugPort     int    `json:"debugPort"`
	InspectorPort int    `json:"inspectorPort,omitempty"`
	OwnerSID      string `json:"ownerSid,omitempty"`
	Executable    string `json:"executable"`
	Started       string `json:"started"`
	LaunchedByUs  bool   `json:"launchedByUs"`
	LaunchedAt    string `json:"launchedAt"`
}

// WindowsPortListener represents one TCP listening socket entry reported by the OS.
type WindowsPortListener struct {
	PID          int    `json:"pid"`
	LocalAddress string `json:"localAddress"`
	LocalPort    int    `json:"localPort"`
}

// WindowsTCPConnection represents an established TCP connection used to audit
// whether any foreign local process connected to Kimi Code's debugging port.
type WindowsTCPConnection struct {
	PID           int    `json:"pid"`
	LocalAddress  string `json:"localAddress"`
	LocalPort     int    `json:"localPort"`
	RemoteAddress string `json:"remoteAddress"`
	RemotePort    int    `json:"remotePort"`
	State         string `json:"state"`
}

// WindowsDebugClientAlert records a non-self process connected to the debugging port.
type WindowsDebugClientAlert struct {
	PeerPID       int    `json:"peerPid"`
	PeerLocalPort int    `json:"peerLocalPort"`
	DebugPort     int    `json:"debugPort"`
	Reason        string `json:"reason"`
}

// WindowsBinaryInfo summarizes the verified PE/Electron binary properties.
type WindowsBinaryInfo struct {
	Arch            string `json:"arch"`
	ElectronPresent bool   `json:"electronPresent"`
	FuseVersion     byte   `json:"fuseVersion"`
	FuseCount       int    `json:"fuseCount"`
	InspectorFuseOn bool   `json:"inspectorFuseOn"`
	RunAsNodeFuseOn bool   `json:"runAsNodeFuseOn"`
}

// WindowsLauncherPaths returns the fixed launcher locations under dataDir.
func WindowsLauncherPaths(dataDir string) (launcherDir, scriptPath, configPath, statePath string) {
	launcherDir = filepath.Join(dataDir, "launcher")
	scriptPath = filepath.Join(launcherDir, windowsLauncherScriptName)
	configPath = filepath.Join(launcherDir, windowsLauncherConfigFile)
	statePath = filepath.Join(dataDir, windowsLauncherStateFile)
	return
}

// DefaultWindowsKimiExecutable resolves the standard per-user Kimi Code.exe path.
func DefaultWindowsKimiExecutable(localAppData, explicitExe string) (string, error) {
	if trimmed := strings.TrimSpace(explicitExe); trimmed != "" {
		if !isWindowsAbsPath(trimmed) {
			return "", errors.New("Windows Kimi Code 可执行文件路径必须是绝对路径")
		}
		return cleanWindowsPath(trimmed), nil
	}
	base := strings.TrimSpace(localAppData)
	if !isWindowsAbsPath(base) {
		return "", errors.New("无法定位 LOCALAPPDATA 目录，请显式指定 Kimi Code.exe 路径")
	}
	return joinWindowsPath(base, "Programs", "Kimi Code", WindowsKimiExecutableName), nil
}

func isWindowsAbsPath(p string) bool {
	if len(p) >= 3 && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		return true
	}
	return strings.HasPrefix(p, `\\`) || filepath.IsAbs(p)
}

func cleanWindowsPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, "/", `\`)
	for strings.Contains(p, `\\`) && !strings.HasPrefix(p, `\\`) {
		p = strings.ReplaceAll(p, `\\`, `\`)
	}
	return strings.TrimRight(p, `\`)
}

func joinWindowsPath(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Trim(strings.ReplaceAll(strings.TrimSpace(part), "/", `\`), `\`)
		if part != "" {
			cleaned = append(cleaned, part)
		}
	}
	return strings.Join(cleaned, `\`)
}

// WindowsPathEqual compares two Windows paths case-insensitively with normalized slashes.
func WindowsPathEqual(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	return strings.EqualFold(cleanWindowsPath(a), cleanWindowsPath(b))
}

// SplitWindowsCommandLine splits a Windows process command line into argv tokens,
// honoring double-quoted segments containing spaces (such as "C:\...\Kimi Code.exe").
func SplitWindowsCommandLine(commandLine string) []string {
	var args []string
	var cur strings.Builder
	inQuotes := false
	hasToken := false
	for i := 0; i < len(commandLine); i++ {
		ch := commandLine[i]
		switch ch {
		case '"':
			hasToken = true
			if inQuotes && i+1 < len(commandLine) && commandLine[i+1] == '"' {
				cur.WriteByte('"')
				i++
			} else {
				inQuotes = !inQuotes
			}
		case ' ', '\t', '\r', '\n':
			if inQuotes {
				cur.WriteByte(ch)
			} else if hasToken {
				args = append(args, cur.String())
				cur.Reset()
				hasToken = false
			}
		default:
			hasToken = true
			cur.WriteByte(ch)
		}
	}
	if hasToken {
		args = append(args, cur.String())
	}
	return args
}

func extractDebugPortArg(args []string) (int, bool) {
	for i := 1; i < len(args); i++ {
		lower := strings.ToLower(args[i])
		if strings.HasPrefix(lower, "--remote-debugging-port=") {
			val := strings.TrimPrefix(lower, "--remote-debugging-port=")
			if port, err := strconv.Atoi(val); err == nil && port > 0 && port <= 65535 {
				return port, true
			}
		} else if lower == "--remote-debugging-port" && i+1 < len(args) {
			if port, err := strconv.Atoi(args[i+1]); err == nil && port > 0 && port <= 65535 {
				return port, true
			}
		}
	}
	return 0, false
}

func isWindowsDebugArg(arg string) bool {
	lower := strings.ToLower(strings.TrimSpace(arg))
	for _, prefix := range []string{
		"--inspect", "-inspect",
		"--debug", "-debug",
		"--remote-debugging", "-remote-debugging",
		"--user-data-dir", "-user-data-dir",
		"--user-data-path", "-user-data-path",
		"--session-data-dir", "-session-data-dir",
		"--profile", "-profile",
		"--guest", "-guest",
		"--incognito", "-incognito",
	} {
		if lower == prefix || strings.HasPrefix(lower, prefix+"=") || strings.HasPrefix(lower, prefix+"-") {
			return true
		}
	}
	return false
}

// IsWindowsDebugInstance determines whether a Windows Kimi Code command line
// represents an external debugging/test instance. An instance launched by our
// launcher whose PID, start time, executable, and loopback debugging port all
// match the launcher lease is treated as a daily instance (returns false), while
// any other debug/alternate-profile flag is still treated as a debug instance.
func IsWindowsDebugInstance(info WindowsProcessInfo, launcher *WindowsLauncherState) bool {
	args := SplitWindowsCommandLine(info.CommandLine)
	matchesLauncher := launcher != nil &&
		launcher.LaunchedByUs &&
		launcher.DebugPort > 0 &&
		launcher.PID == info.PID &&
		launcher.OwnerSID != "" && launcher.OwnerSID == info.OwnerSID &&
		launcher.Started != "" &&
		launcher.Started == info.Started &&
		WindowsPathEqual(launcher.Executable, info.Executable)

	expectedPortFlag := ""
	if matchesLauncher {
		expectedPortFlag = fmt.Sprintf("--remote-debugging-port=%d", launcher.DebugPort)
	}

	for i := 1; i < len(args); i++ {
		arg := args[i]
		lower := strings.ToLower(arg)
		if matchesLauncher {
			if launcher.InspectorPort > 0 && lower == "--inspect=127.0.0.1:"+strconv.Itoa(launcher.InspectorPort) {
				continue
			}
			if lower == expectedPortFlag {
				continue
			}
			if lower == "--remote-debugging-address=127.0.0.1" {
				continue
			}
			if lower == "--remote-debugging-port" && i+1 < len(args) && args[i+1] == strconv.Itoa(launcher.DebugPort) {
				i++
				continue
			}
			if lower == "--remote-debugging-address" && i+1 < len(args) && args[i+1] == "127.0.0.1" {
				i++
				continue
			}
		}
		if isWindowsDebugArg(arg) {
			return true
		}
	}
	return false
}

// SelectWindowsMainProcesses filters raw Win32_Process records down to verified
// Kimi Code main Electron processes owned by currentSID. Helper processes
// (--type=...) and non-flag script invocations (e.g. Kimi Code.exe ./bin/...)
// are ignored.
func SelectWindowsMainProcesses(
	currentSID string,
	processes []WindowsProcessInfo,
	expectedExecutable string,
	profile string,
) ([]WindowsProcessInfo, error) {
	currentSID = strings.TrimSpace(currentSID)
	if currentSID == "" {
		return nil, errors.New("无法确定当前 Windows 用户 SID，拒绝接入")
	}
	var matches []WindowsProcessInfo
	for _, proc := range processes {
		if proc.PID <= 0 {
			continue
		}
		if strings.TrimSpace(proc.OwnerSID) != currentSID {
			continue
		}
		args := SplitWindowsCommandLine(proc.CommandLine)
		if len(args) == 0 {
			continue
		}
		// Skip Electron helper processes (--type=renderer/gpu/utility/...)
		isHelper := false
		for _, a := range args[1:] {
			lower := strings.ToLower(a)
			if strings.HasPrefix(lower, "--type=") || lower == "--type" || strings.HasPrefix(lower, "-type=") {
				isHelper = true
				break
			}
		}
		if isHelper {
			continue
		}
		// Kimi Code launches internal Node helper scripts with the same EXE followed by a script path.
		hasScriptArg := false
		for i := 1; i < len(args); i++ {
			a := args[i]
			if !strings.HasPrefix(a, "-") {
				// Allow value immediately following a known two-token flag.
				prev := strings.ToLower(args[i-1])
				if prev == "--remote-debugging-port" || prev == "--remote-debugging-address" || prev == "--user-data-dir" {
					continue
				}
				hasScriptArg = true
				break
			}
		}
		if hasScriptArg {
			continue
		}
		if _, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(proc.Started)); err != nil {
			return nil, errors.New("无法解析 Windows 宿主进程启动时间，拒绝接入")
		}
		if expectedExecutable != "" {
			if !WindowsPathEqual(proc.Executable, expectedExecutable) || !WindowsPathEqual(args[0], expectedExecutable) {
				return nil, errors.New("运行中的 Kimi Code 主进程与预期安装路径不一致，拒绝接入")
			}
		} else if !WindowsPathEqual(proc.Executable, args[0]) {
			return nil, errors.New("运行中的 Kimi Code 进程可执行文件与命令行不一致，拒绝接入")
		}
		if profile != "" {
			want := "--user-data-dir=" + profile
			matchedProfile := false
			for i := 1; i < len(args); i++ {
				if WindowsPathEqual(args[i], want) || strings.EqualFold(args[i], want) {
					matchedProfile = true
					break
				}
				if strings.EqualFold(args[i], "--user-data-dir") && i+1 < len(args) && WindowsPathEqual(args[i+1], profile) {
					matchedProfile = true
					break
				}
			}
			if !matchedProfile {
				continue
			}
		}
		matches = append(matches, proc)
	}
	return matches, nil
}

// VerifyWindowsProcessIdentity compares a freshly queried process snapshot
// against the previously verified identity so PID reuse or ownership change is
// caught before any CDP command runs.
func VerifyWindowsProcessIdentity(expected, current WindowsProcessInfo) error {
	if expected.PID <= 0 || current.PID != expected.PID {
		return errors.New("Windows 宿主进程号在接入过程中发生变化，已停止接入")
	}
	if expected.OwnerSID == "" || current.OwnerSID != expected.OwnerSID {
		return errors.New("Windows 宿主进程属主 SID 不匹配，拒绝接入")
	}
	if current.SessionID != expected.SessionID {
		return errors.New("Windows 宿主桌面会话已变化，拒绝接入")
	}
	if expected.Started == "" || current.Started != expected.Started {
		return errors.New("Windows 宿主进程启动时间已变化（可能发生 PID 复用），已停止接入")
	}
	if !WindowsPathEqual(expected.Executable, current.Executable) {
		return errors.New("Windows 宿主进程可执行文件路径发生变化，拒绝接入")
	}
	return nil
}

// VerifyWindowsPEAndFuses validates the PE header (x64 or ARM64), presence of
// the Electron runtime marker, and the Electron fuse sentinel block in memory.
func VerifyWindowsPEAndFuses(data []byte) (WindowsBinaryInfo, error) {
	info, err := verifyPEHeader(data)
	if err != nil {
		return WindowsBinaryInfo{}, err
	}
	if !bytes.Contains(data, []byte("Electron/")) {
		return WindowsBinaryInfo{}, ErrWindowsNotElectron
	}
	sentinel := []byte(fuseSentinelPrefix + fuseSentinelSuffix)
	at := bytes.Index(data, sentinel)
	if at < 0 {
		return WindowsBinaryInfo{}, ErrUnknownFuseLayout
	}
	block := at + len(sentinel)
	if block+fuseBlockPrefixLen > len(data) {
		return WindowsBinaryInfo{}, ErrUnknownFuseLayout
	}
	version := data[block]
	count := int(data[block+1])
	if version != 1 || count < 4 || count > fuseMaxCount || block+fuseBlockPrefixLen+count > len(data) {
		return WindowsBinaryInfo{}, ErrUnknownFuseLayout
	}
	fuses := data[block+fuseBlockPrefixLen : block+fuseBlockPrefixLen+count]
	for _, b := range fuses {
		if b != '0' && b != '1' && b != 'r' {
			return WindowsBinaryInfo{}, ErrUnknownFuseLayout
		}
	}
	info.ElectronPresent = true
	info.FuseVersion = version
	info.FuseCount = count
	info.InspectorFuseOn = fuses[3] == '1'
	info.RunAsNodeFuseOn = fuses[0] == '1'
	if !info.InspectorFuseOn {
		return info, ErrInspectorDisabled
	}
	return info, nil
}

func verifyPEHeader(header []byte) (WindowsBinaryInfo, error) {
	if len(header) < 64 || header[0] != 'M' || header[1] != 'Z' {
		return WindowsBinaryInfo{}, ErrWindowsInvalidPE
	}
	peOffset := int(binary.LittleEndian.Uint32(header[windowsPEHeaderOffsetField : windowsPEHeaderOffsetField+4]))
	if peOffset < 0 || peOffset+6 > len(header) {
		return WindowsBinaryInfo{}, ErrWindowsInvalidPE
	}
	if string(header[peOffset:peOffset+4]) != "PE\x00\x00" {
		return WindowsBinaryInfo{}, ErrWindowsInvalidPE
	}
	machine := binary.LittleEndian.Uint16(header[peOffset+4 : peOffset+6])
	switch machine {
	case windowsPEMachineAMD64:
		return WindowsBinaryInfo{Arch: "amd64"}, nil
	case windowsPEMachineARM64:
		return WindowsBinaryInfo{Arch: "arm64"}, nil
	default:
		return WindowsBinaryInfo{}, ErrWindowsInvalidPE
	}
}

// VerifyWindowsBinaryFile streams Kimi Code.exe from disk to verify PE header,
// Electron marker, and Electron fuse block without loading the entire 200MB EXE
// into memory at once.
func VerifyWindowsBinaryFile(executable string) (WindowsBinaryInfo, error) {
	f, err := os.Open(executable)
	if err != nil {
		return WindowsBinaryInfo{}, fmt.Errorf("无法读取 Windows 宿主可执行文件：%w", err)
	}
	defer f.Close()

	head := make([]byte, 4096)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return WindowsBinaryInfo{}, err
	}
	info, err := verifyPEHeader(head[:n])
	if err != nil {
		return WindowsBinaryInfo{}, err
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return WindowsBinaryInfo{}, err
	}
	sentinel := []byte(fuseSentinelPrefix + fuseSentinelSuffix)
	electronMarker := []byte("Electron/")
	buf := make([]byte, windowsFuseScanChunkSize+windowsFuseScanCarrySize)
	carry := 0
	foundElectron := false
	foundFuse := false
	var fuseErr error

	for {
		readN, readErr := f.Read(buf[carry : carry+windowsFuseScanChunkSize])
		window := buf[:carry+readN]
		if !foundElectron && bytes.Contains(window, electronMarker) {
			foundElectron = true
		}
		if !foundFuse {
			if at := bytes.Index(window, sentinel); at >= 0 {
				block := at + len(sentinel)
				if block+fuseBlockPrefixLen <= len(window) {
					count := int(window[block+1])
					if count > fuseMaxCount {
						fuseErr = ErrFuseBlockTooLarge
						foundFuse = true
					} else if block+fuseBlockPrefixLen+count <= len(window) {
						version := window[block]
						if version != 1 || count < 4 {
							fuseErr = ErrUnknownFuseLayout
						} else {
							fuses := window[block+fuseBlockPrefixLen : block+fuseBlockPrefixLen+count]
							valid := true
							for _, b := range fuses {
								if b != '0' && b != '1' && b != 'r' {
									valid = false
									break
								}
							}
							if !valid {
								fuseErr = ErrUnknownFuseLayout
							} else {
								info.FuseVersion = version
								info.FuseCount = count
								info.InspectorFuseOn = fuses[3] == '1'
								info.RunAsNodeFuseOn = fuses[0] == '1'
								if !info.InspectorFuseOn {
									fuseErr = ErrInspectorDisabled
								}
							}
						}
						foundFuse = true
					}
				}
			}
		}
		if foundElectron && foundFuse {
			break
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return WindowsBinaryInfo{}, readErr
		}
		if len(window) > windowsFuseScanCarrySize {
			copy(buf, window[len(window)-windowsFuseScanCarrySize:])
			carry = windowsFuseScanCarrySize
		} else {
			carry = len(window)
		}
	}
	if !foundElectron {
		return WindowsBinaryInfo{}, ErrWindowsNotElectron
	}
	if !foundFuse {
		return WindowsBinaryInfo{}, ErrUnknownFuseLayout
	}
	if fuseErr != nil {
		return info, fuseErr
	}
	info.ElectronPresent = true
	return info, nil
}

func isLoopbackIPString(addr string) bool {
	addr = strings.TrimSpace(strings.Trim(addr, "[]"))
	if addr == "127.0.0.1" || addr == "::1" || strings.EqualFold(addr, "localhost") {
		return true
	}
	ip := net.ParseIP(addr)
	return ip != nil && ip.IsLoopback()
}

// VerifyWindowsDebugPortOwner ensures the TCP listener on debugPort belongs
// strictly to expectedPID and binds exclusively to a loopback interface.
func VerifyWindowsDebugPortOwner(listeners []WindowsPortListener, debugPort int, expectedPID int) error {
	if debugPort <= 0 || debugPort > 65535 {
		return errors.New("无效的调试端口号")
	}
	if expectedPID <= 0 {
		return errors.New("无效的目标宿主 PID")
	}
	matchedExpected := false
	for _, l := range listeners {
		if l.LocalPort != debugPort {
			continue
		}
		if !isLoopbackIPString(l.LocalAddress) {
			return fmt.Errorf("%w（%s:%d）", ErrWindowsPortNotLocal, l.LocalAddress, l.LocalPort)
		}
		if l.PID != expectedPID {
			return fmt.Errorf("%w（端口 %d 实际属主 PID %d，预期 %d）", ErrWindowsPortNotOwned, debugPort, l.PID, expectedPID)
		}
		matchedExpected = true
	}
	if !matchedExpected {
		return fmt.Errorf("未在 127.0.0.1:%d 检测到目标宿主进程（PID %d）的调试监听端口", debugPort, expectedPID)
	}
	return nil
}

// DetectForeignDebugClients inspects established TCP connections against the
// debugging port and flags any local peer process other than hostPID and selfPID.
func DetectForeignDebugClients(connections []WindowsTCPConnection, debugPort, hostPID, selfPID int) []WindowsDebugClientAlert {
	if debugPort <= 0 {
		return nil
	}
	// Map local client port -> owning PID for connections targeting debugPort.
	peerByClientPort := map[int]int{}
	serverPeerPorts := map[int]bool{}
	for _, c := range connections {
		if !strings.EqualFold(c.State, "Established") && c.State != "" {
			continue
		}
		if c.LocalPort == debugPort && c.PID == hostPID {
			serverPeerPorts[c.RemotePort] = true
		}
		if c.RemotePort == debugPort && isLoopbackIPString(c.RemoteAddress) {
			peerByClientPort[c.LocalPort] = c.PID
		}
	}
	var alerts []WindowsDebugClientAlert
	seen := map[int]bool{}
	for clientPort := range serverPeerPorts {
		peerPID, ok := peerByClientPort[clientPort]
		if !ok {
			alerts = append(alerts, WindowsDebugClientAlert{
				PeerPID:       0,
				PeerLocalPort: clientPort,
				DebugPort:     debugPort,
				Reason:        fmt.Sprintf("检测到未知进程经本机端口 %d 连接到调试端口 %d", clientPort, debugPort),
			})
			continue
		}
		if peerPID == selfPID || peerPID == hostPID {
			continue
		}
		if seen[peerPID] {
			continue
		}
		seen[peerPID] = true
		alerts = append(alerts, WindowsDebugClientAlert{
			PeerPID:       peerPID,
			PeerLocalPort: clientPort,
			DebugPort:     debugPort,
			Reason:        fmt.Sprintf("检测到其他进程（PID %d）连接到 Kimi Code 调试端口 %d", peerPID, debugPort),
		})
	}
	return alerts
}

// FormatDebugClientAlerts renders a human-readable diagnostic warning string.
func FormatDebugClientAlerts(alerts []WindowsDebugClientAlert) string {
	if len(alerts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(alerts))
	for _, a := range alerts {
		parts = append(parts, a.Reason)
	}
	return "调试端口安全告警：" + strings.Join(parts, "；")
}

// WriteWindowsLauncherState atomically persists the launcher lease file.
func WriteWindowsLauncherState(dataDir string, state WindowsLauncherState) error {
	if strings.TrimSpace(dataDir) == "" {
		return errors.New("缺少增强数据目录，无法保存启动器状态")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	_, _, _, statePath := WindowsLauncherPaths(dataDir)
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return platform.AtomicPrivateFile(statePath, append(encoded, '\n'))
}

// ReadWindowsLauncherState loads the saved launcher lease, returning nil if absent.
func ReadWindowsLauncherState(dataDir string) *WindowsLauncherState {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	_, _, _, statePath := WindowsLauncherPaths(dataDir)
	raw, err := os.ReadFile(statePath)
	if err != nil {
		return nil
	}
	var state WindowsLauncherState
	if err := json.Unmarshal(raw, &state); err != nil || state.PID <= 0 || state.DebugPort <= 0 {
		return nil
	}
	return &state
}

// PickFreeLoopbackPort allocates an unused ephemeral TCP port on 127.0.0.1.
func PickFreeLoopbackPort() (int, error) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || addr.Port <= 0 {
		return 0, errors.New("无法分配本机环回调试端口")
	}
	return addr.Port, nil
}

// EncodeWindowsPowerShellCommand encodes a PowerShell script as UTF-16LE base64
// for powershell.exe -EncodedCommand, avoiding Windows command-line quote mangling.
func EncodeWindowsPowerShellCommand(script string) string {
	units := utf16.Encode([]rune(script))
	raw := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(raw[i*2:], u)
	}
	return base64.StdEncoding.EncodeToString(raw)
}
