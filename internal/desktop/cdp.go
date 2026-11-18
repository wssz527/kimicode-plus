package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	KimiExecutable = "/Applications/Kimi Code.app/Contents/MacOS/Kimi Code"
	KimiFramework  = "/Applications/Kimi Code.app/Contents/Frameworks/Electron Framework.framework/Versions/A/Electron Framework"
	InspectorPort  = 9229

	// SupportedElectron is the Electron version every entry of SupportedHosts runs
	// on. It is the single source of truth for version-gated checks such as the
	// fuse layout, so they cannot drift from the host contract.
	SupportedElectron = "45.1.0"
)

// ProcessInfo is the identity of a running Kimi Code main process. It is taken
// twice — once before the signal and once after — and must be identical: a
// process that exited and was replaced by another instance has a different start
// time even when it reuses the PID.
type ProcessInfo struct {
	PID        int
	UID        int
	Started    string
	Executable string
}

// DebugFlags mark an instance that was launched for testing or debugging. Such
// an instance is never attached to automatically: its inspector may already be
// open on another port, and the caller has no consent to take it over.
var DebugFlags = []string{"--user-data-dir", "--remote-debugging", "--inspect"}

// IsDebugInstance reports whether the command line carries a debug flag.
func IsDebugInstance(command string) bool {
	for _, field := range strings.Fields(command) {
		for _, flag := range DebugFlags {
			if field == flag || strings.HasPrefix(field, flag+"=") {
				return true
			}
			// --remote-debugging-port=9222 is the same family as
			// --remote-debugging: any flag that extends it counts.
			if strings.HasPrefix(field, flag+"-") {
				return true
			}
		}
	}
	return false
}

// FindMainPID locates the single running Kimi Code main process. An empty
// profile accepts any instance; a non-empty profile narrows the match to the one
// launched with that --user-data-dir, which is how isolated verification runs
// address their own host.
func FindMainPID(ctx context.Context, profile string) (int, error) {
	if !hostPlatformSupported {
		return 0, ErrUnsupportedPlatform
	}
	if profile != "" && !filepath.IsAbs(profile) {
		return 0, errors.New("隔离宿主的 profile 必须是绝对路径")
	}
	output, err := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return 0, err
	}
	return parseMainPID(output, KimiExecutable, profile)
}

// parseMainPID keeps only main processes (no --type= helper) of this executable.
// An empty profile accepts every instance; otherwise the command line must carry
// that exact --user-data-dir value.
func parseMainPID(output []byte, executable, profile string) (int, error) {
	matches := []int{}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		separator := strings.Index(line, " ")
		if separator <= 0 {
			continue
		}
		pid, err := strconv.Atoi(line[:separator])
		command := strings.TrimSpace(line[separator+1:])
		if err != nil || command != executable && !strings.HasPrefix(command, executable+" ") {
			continue
		}
		if strings.Contains(command, "--type=") {
			continue
		}
		if profile != "" && !strings.Contains(command, "--user-data-dir="+profile) {
			continue
		}
		matches = append(matches, pid)
	}
	if len(matches) != 1 {
		return 0, fmt.Errorf("需要且只能找到一个 Kimi Code 主进程，实际找到 %d 个", len(matches))
	}
	return matches[0], nil
}

// ProcessIdentity reads uid, start time and executable path for a PID. It is the
// half of the identity check that survives a PID reuse.
func ProcessIdentity(ctx context.Context, pid int) (ProcessInfo, error) {
	output, err := exec.CommandContext(ctx, "/bin/ps", "-p", strconv.Itoa(pid), "-o", "pid=,uid=,lstart=,comm=").Output()
	if err != nil {
		return ProcessInfo{}, err
	}
	return parseProcessIdentity(output)
}

var processStartLayout = "Mon Jan _2 15:04:05 2006"

func parseProcessIdentity(output []byte) (ProcessInfo, error) {
	line := strings.TrimSpace(string(output))
	if line == "" {
		return ProcessInfo{}, errors.New("宿主进程不存在")
	}
	fields := strings.Fields(line)
	// pid uid Mon Jan 2 15:04:05 2026 /path/to/executable
	// lstart is exactly five fields; the executable may contain spaces, so it is
	// everything from the sixth field on.
	const lstartFields = 5
	if len(fields) < 2+lstartFields+1 {
		return ProcessInfo{}, errors.New("无法解析宿主进程身份")
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return ProcessInfo{}, errors.New("无法解析宿主进程号")
	}
	uid, err := strconv.Atoi(fields[1])
	if err != nil {
		return ProcessInfo{}, errors.New("无法解析宿主进程所有者")
	}
	started := strings.Join(fields[2:2+lstartFields], " ")
	executable := strings.Join(fields[2+lstartFields:], " ")
	info := ProcessInfo{PID: pid, UID: uid, Started: started, Executable: executable}
	if parsed, parseErr := time.Parse(processStartLayout, started); parseErr != nil {
		return ProcessInfo{}, errors.New("无法解析宿主启动时间")
	} else if parsed.IsZero() {
		return ProcessInfo{}, errors.New("宿主启动时间无效")
	}
	return info, nil
}

// CommandLine returns the full command line of a PID, used to recognise debug
// instances before any signal is sent.
func CommandLine(ctx context.Context, pid int) (string, error) {
	output, err := exec.CommandContext(ctx, "/bin/ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// ErrUnsupportedPlatform names the support boundary of the host integration.
// Only the macOS host path has been verified end to end; the other targets are
// compile-only, so they must refuse instead of signalling a process or claiming
// a successful attach.
var ErrUnsupportedPlatform = errors.New("增强宿主接入目前只支持 macOS 宿主；本平台仅保证编译通过，不会向任何进程发送信号")

// VerifyLiveProcess confirms the running process still executes the verified
// binary. The platform-specific runtime image check catches a replaced
// application bundle even when the on-disk path is unchanged.
func VerifyLiveProcess(ctx context.Context, info ProcessInfo) error {
	if !hostPlatformSupported {
		return ErrUnsupportedPlatform
	}
	current, err := ProcessIdentity(ctx, info.PID)
	if err != nil {
		return err
	}
	if current != info {
		return errors.New("宿主进程身份在接入过程中发生变化，已停止接入")
	}
	if current.UID != os.Getuid() {
		return errors.New("宿主进程不属于当前用户，拒绝接入")
	}
	return verifyRuntimeImage(ctx, info)
}

type HostContract struct {
	AppVersion     string
	Electron       string
	RendererScheme string
	DOMRevision    int
}

var SupportedHosts = []HostContract{
	{AppVersion: "1.3.4", Electron: SupportedElectron, RendererScheme: "app://renderer/", DOMRevision: 1},
	{AppVersion: "1.3.5", Electron: SupportedElectron, RendererScheme: "app://renderer/", DOMRevision: 1},
	{AppVersion: "1.3.7", Electron: SupportedElectron, RendererScheme: "app://renderer/", DOMRevision: 1},
}

type HostIdentity struct {
	PID        int    `json:"pid"`
	Type       string `json:"type"`
	AppVersion string `json:"appVersion"`
	Electron   string `json:"electron"`
}

func (i HostIdentity) Contract() (HostContract, error) {
	if i.Type != "browser" {
		return HostContract{}, errors.New("目标进程不是 Kimi Code 主进程")
	}
	for _, contract := range SupportedHosts {
		if contract.AppVersion == i.AppVersion && contract.Electron == i.Electron {
			return contract, nil
		}
	}
	return HostContract{}, fmt.Errorf("Kimi Code %s / Electron %s 尚未通过增强契约验证", i.AppVersion, i.Electron)
}

type CDP struct {
	pid     int
	conn    *websocket.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	next    int
	pending map[int]chan cdpResponse
	closed  chan struct{}
	once    sync.Once
}

type cdpResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// inspectorPortOwner reports which PID listens on the inspector port. An empty
// string means nothing is listening.
func inspectorPortOwner(ctx context.Context) (string, error) {
	output, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", "-iTCP:"+strconv.Itoa(InspectorPort), "-sTCP:LISTEN", "-Fpn").Output()
	if err != nil {
		// lsof exits non-zero when no socket matches, which is the normal idle case.
		return "", nil
	}
	lines := strings.Split(string(output), "\n")
	current := ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			current = line[1:]
		case 'n':
			if current != "" && line[1:] == "127.0.0.1:"+strconv.Itoa(InspectorPort) {
				return current, nil
			}
		}
	}
	if current != "" {
		return current, nil
	}
	return "", nil
}

func inspectorOpen(ctx context.Context) bool {
	conn, err := net.DialTimeout("tcp4", "127.0.0.1:"+strconv.Itoa(InspectorPort), 300*time.Millisecond)
	if err == nil {
		conn.Close()
		return true
	}
	return false
}

// WaitInspectorGone blocks until the inspector port stops accepting connections
// or the deadline passes, reporting whether it closed. A port that stays open
// means the temporary inspector could not be shut down, which must be surfaced:
// the next launch would otherwise refuse to attach to a "busy" port.
func WaitInspectorGone(ctx context.Context, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !inspectorOpen(ctx) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	return !inspectorOpen(ctx)
}

// inspectorDeadlineExpression arms the temporary-inspector fuse before anything
// else runs. The window is deliberately short: the previous 90s budget meant a
// stuck attach left a live debugging port on the client for over a minute.
func inspectorDeadlineExpression(pid int) string {
	return fmt.Sprintf(`(()=>{if(process.pid!==%d||process.type!=='browser')throw new Error('调试端点不是预期的宿主主进程');const load=typeof require==='function'?require:process.mainModule.require.bind(process.mainModule);const inspector=load('node:inspector');clearTimeout(globalThis.__ekdDeadline);globalThis.__ekdDeadline=setTimeout(()=>{try{inspector.close()}catch{}},12000);return{pid:process.pid,type:process.type,electron:process.versions.electron};})()`, pid)
}

// closeInspectorExpression clears the deadline and closes the inspector from
// inside the host process, so no port is left behind.
func closeInspectorExpression(pid int) string {
	return fmt.Sprintf(`(()=>{clearTimeout(globalThis.__ekdDeadline);if(process.pid===%d&&process.type==='browser'){setTimeout(()=>{try{(typeof require==='function'?require:process.mainModule.require.bind(process.mainModule))('node:inspector').close()}catch{}},25)}return true;})()`, pid)
}

func Attach(ctx context.Context, pid int) (*CDP, error) {
	if pid <= 0 {
		return nil, errors.New("宿主 PID 无效")
	}
	if !hostPlatformSupported {
		return nil, ErrUnsupportedPlatform
	}
	if inspectorOpen(ctx) {
		return nil, errors.New("9229 调试端口已被占用，拒绝附着未知进程")
	}
	if err := activateInspector(pid); err != nil {
		return nil, err
	}
	var endpoint string
	for range 50 {
		// Re-check identity on every poll: a PID reused by another process must
		// never be attached to.
		if _, err := ProcessIdentity(ctx, pid); err != nil {
			return nil, errors.New("宿主进程在接入过程中退出")
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(InspectorPort)+"/json/list", nil)
		response, err := (&http.Client{Timeout: 400 * time.Millisecond, Transport: &http.Transport{Proxy: nil}}).Do(req)
		if err == nil {
			var targets []struct {
				Type string `json:"type"`
				URL  string `json:"webSocketDebuggerUrl"`
			}
			err = json.NewDecoder(response.Body).Decode(&targets)
			response.Body.Close()
			if err == nil {
				for _, target := range targets {
					if target.Type == "node" {
						endpoint = target.URL
						break
					}
				}
			}
		}
		if endpoint != "" {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if endpoint == "" {
		return nil, errors.New("宿主没有打开临时调试端点")
	}
	// The listener must belong to the expected process. Without this check a port
	// opened by anything else on the machine would be accepted.
	owner, err := inspectorPortOwner(ctx)
	if err != nil || owner != strconv.Itoa(pid) {
		return nil, errors.New("9229 调试端口的监听者不是预期宿主进程，拒绝接入")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Port() != strconv.Itoa(InspectorPort) {
		return nil, errors.New("宿主没有提供可信的本机调试端点")
	}
	conn, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"http://127.0.0.1:" + strconv.Itoa(InspectorPort)}}})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(32 << 20)
	cdp := &CDP{pid: pid, conn: conn, pending: map[int]chan cdpResponse{}, closed: make(chan struct{})}
	go cdp.read()
	// The fuse is the very first command, before any identity read or payload
	// load, so a stalled attach still closes its own port within 12 seconds.
	if _, err = cdp.Evaluate(ctx, inspectorDeadlineExpression(pid)); err != nil {
		cdp.Close(context.Background())
		return nil, err
	}
	return cdp, nil
}

func (c *CDP) read() {
	defer close(c.closed)
	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			c.mu.Lock()
			for id, pending := range c.pending {
				close(pending)
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}
		var envelope struct {
			ID int `json:"id"`
			cdpResponse
		}
		if json.Unmarshal(data, &envelope) != nil || envelope.ID == 0 {
			continue
		}
		c.mu.Lock()
		pending := c.pending[envelope.ID]
		delete(c.pending, envelope.ID)
		c.mu.Unlock()
		if pending != nil {
			pending <- envelope.cdpResponse
			close(pending)
		}
	}
}

func (c *CDP) Evaluate(ctx context.Context, expression string) (json.RawMessage, error) {
	c.mu.Lock()
	c.next++
	id := c.next
	pending := make(chan cdpResponse, 1)
	c.pending[id] = pending
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	payload, _ := json.Marshal(map[string]any{"id": id, "method": "Runtime.evaluate", "params": map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true}})
	c.writeMu.Lock()
	err := c.conn.Write(ctx, websocket.MessageText, payload)
	c.writeMu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, errors.New("宿主调试连接已断开")
	case response, ok := <-pending:
		if !ok {
			return nil, errors.New("宿主调试连接已断开")
		}
		if response.Error != nil {
			return nil, errors.New(response.Error.Message)
		}
		var result struct {
			Result struct {
				Value json.RawMessage `json:"value"`
			} `json:"result"`
			ExceptionDetails *struct {
				Text      string `json:"text"`
				Exception struct {
					Description string `json:"description"`
				} `json:"exception"`
			} `json:"exceptionDetails"`
		}
		if err := json.Unmarshal(response.Result, &result); err != nil {
			return nil, err
		}
		if result.ExceptionDetails != nil {
			if result.ExceptionDetails.Exception.Description != "" {
				return nil, errors.New(result.ExceptionDetails.Exception.Description)
			}
			return nil, errors.New(result.ExceptionDetails.Text)
		}
		return result.Result.Value, nil
	}
}

// Close detaches and confirms the temporary inspector really closed. A port left
// listening is reported as an error so the caller can tell the user, instead of
// silently leaving a debugging endpoint open on the client.
func (c *CDP) Close(ctx context.Context) error {
	var err error
	c.once.Do(func() {
		_, _ = c.Evaluate(ctx, closeInspectorExpression(c.pid))
		err = c.conn.Close(websocket.StatusNormalClosure, "ekd detach")
		if WaitInspectorGone(ctx, 15*time.Second) {
			return
		}
		if err == nil {
			err = errors.New("临时调试端点没有关闭，请正常退出客户端后重试")
		}
	})
	return err
}
