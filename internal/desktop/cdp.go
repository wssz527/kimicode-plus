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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

const (
	KimiExecutable = "/Applications/Kimi Code.app/Contents/MacOS/Kimi Code"
	InspectorPort  = 9229
)

type HostContract struct {
	AppVersion     string
	Electron       string
	RendererScheme string
	DOMRevision    int
}

var SupportedHosts = []HostContract{
	{AppVersion: "1.3.4", Electron: "45.1.0", RendererScheme: "app://renderer/", DOMRevision: 1},
	{AppVersion: "1.3.5", Electron: "45.1.0", RendererScheme: "app://renderer/", DOMRevision: 1},
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

func FindMainPID(ctx context.Context, profile string) (int, error) {
	if profile == "" || !filepath.IsAbs(profile) {
		return 0, errors.New("必须显式指定隔离宿主的 profile 绝对路径，拒绝附着开发主实例")
	}
	output, err := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return 0, err
	}
	return parseMainPID(output, KimiExecutable, profile)
}

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
		if strings.Contains(command, "--type=") || !strings.Contains(command, "--user-data-dir="+profile) {
			continue
		}
		matches = append(matches, pid)
	}
	if len(matches) != 1 {
		return 0, fmt.Errorf("需要且只能找到一个指定 profile 的 Kimi Code 主进程，实际找到 %d 个", len(matches))
	}
	return matches[0], nil
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

func inspectorOpen(ctx context.Context) bool {
	conn, err := net.DialTimeout("tcp4", "127.0.0.1:9229", 300*time.Millisecond)
	if err == nil {
		conn.Close()
		return true
	}
	return false
}

func Attach(ctx context.Context, pid int) (*CDP, error) {
	if pid <= 0 {
		return nil, errors.New("宿主 PID 无效")
	}
	if inspectorOpen(ctx) {
		return nil, errors.New("9229 调试端口已占用，拒绝附着未知进程")
	}
	if err := syscall.Kill(pid, syscall.SIGUSR1); err != nil {
		return nil, err
	}
	var endpoint string
	for range 60 {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:9229/json/list", nil)
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
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Port() != strconv.Itoa(InspectorPort) {
		return nil, errors.New("宿主没有提供可信的本机调试端点")
	}
	conn, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"http://127.0.0.1:9229"}}})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(32 << 20)
	cdp := &CDP{pid: pid, conn: conn, pending: map[int]chan cdpResponse{}, closed: make(chan struct{})}
	go cdp.read()
	if _, err = cdp.Evaluate(ctx, `globalThis.__ekdDeadline&&clearTimeout(globalThis.__ekdDeadline);globalThis.__ekdDeadline=setTimeout(()=>{try{(typeof require==='function'?require:process.mainModule.require.bind(process.mainModule))('node:inspector').close()}catch{}},90000);({pid:process.pid,type:process.type,electron:process.versions.electron})`); err != nil {
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

func (c *CDP) Close(ctx context.Context) error {
	var err error
	c.once.Do(func() {
		_, _ = c.Evaluate(ctx, fmt.Sprintf(`clearTimeout(globalThis.__ekdDeadline);if(process.pid===%d&&process.type==='browser'){setTimeout(()=>{try{(typeof require==='function'?require:process.mainModule.require.bind(process.mainModule))('node:inspector').close()}catch{}},25)}true`, c.pid))
		err = c.conn.Close(websocket.StatusNormalClosure, "ekd detach")
	})
	return err
}
