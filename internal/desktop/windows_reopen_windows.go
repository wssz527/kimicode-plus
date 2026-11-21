//go:build windows

package desktop

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

func findWindowsAttachHost(ctx context.Context, dataDir, executable, profile string) (WindowsProcessInfo, error) {
	payload, err := windowsDiscoverRaw(ctx)
	if err != nil {
		return WindowsProcessInfo{}, err
	}
	executable, err = resolveWindowsKimiExecutable(dataDir, executable)
	if err != nil {
		return WindowsProcessInfo{}, err
	}
	matches, err := SelectWindowsMainProcesses(payload.SID, payload.desktopProcesses(), executable, profile)
	if err != nil {
		return WindowsProcessInfo{}, err
	}
	if len(matches) != 1 || matches[0].SessionID == 0 || matches[0].SessionID != payload.SessionID {
		return WindowsProcessInfo{}, errors.New("请在当前 Windows 桌面中打开一个 Kimi Code 窗口后重试")
	}
	return matches[0], nil
}

func reopenWindowsHost(ctx context.Context, s *NativeService) (map[string]any, error) {
	if s.isSafe() {
		return nil, ErrSafeMode
	}
	if !s.attachOpMu.TryLock() {
		return nil, errors.New("KimiCode+ 正在处理接入，请稍后重试")
	}
	started := false
	defer func() {
		if !started {
			s.attachOpMu.Unlock()
		}
	}()
	cfg := s.attachConfig()
	if cfg.Executable == "" || cfg.DataDir == "" || cfg.KimiHome == "" {
		return nil, errors.New("请在已连接的 KimiCode+ 设置中重新打开 Kimi Code")
	}
	proc, err := findWindowsAttachHost(ctx, cfg.DataDir, "", cfg.Profile)
	if err != nil {
		return nil, err
	}
	if cfg.Profile == "" && IsWindowsDebugInstance(proc, ReadWindowsLauncherState(cfg.DataDir)) {
		return nil, errors.New("检测到调试或测试实例，请关闭它并从 KimiCode+ 入口打开正常客户端")
	}
	if _, err := VerifyWindowsBinaryFile(proc.Executable); err != nil && !errors.Is(err, ErrInspectorDisabled) {
		return nil, errors.New("无法确认官方客户端，Kimi Code 保持打开")
	}
	_, hasDebugPort := extractDebugPortArg(SplitWindowsCommandLine(proc.CommandLine))
	if hasDebugPort {
		conn, err := windowsCompatibilityBrowser(ctx, proc, cfg.Profile)
		if err != nil {
			return nil, err
		}
		defer conn.CloseNow()
		if err := confirmReopenIdle(ctx, cfg.KimiHome, proc.PID); err != nil {
			return nil, err
		}
		if err := conn.Write(ctx, websocket.MessageText, []byte(`{"id":1,"method":"Browser.close"}`)); err != nil {
			return nil, errors.New("无法请求 Kimi Code 正常退出。请从客户端菜单退出，再从 KimiCode+ 入口打开。")
		}
	} else if err := confirmReopenIdle(ctx, cfg.KimiHome, proc.PID); err != nil {
		return nil, err
	}
	started = true
	_ = recordWindowsAttachState(cfg.DataDir, proc, "reopening", nil)
	go func() {
		defer s.attachOpMu.Unlock()
		if !hasDebugPort {
			if err := quitWindowsApplication(proc); err != nil {
				_ = recordWindowsAttachFailure(cfg.DataDir, proc, fmt.Errorf("Windows 无法让 Kimi Code 正常退出，请从客户端菜单退出：%w", err))
				return
			}
		}
		wait, cancel := context.WithTimeout(s.ctx, 20*time.Second)
		defer cancel()
		for {
			processes, err := windowsListProcesses(wait)
			if err == nil {
				stillRunning := false
				for _, current := range processes {
					if VerifyWindowsProcessIdentity(proc, current) == nil {
						stillRunning = true
						break
					}
				}
				if !stillRunning {
					break
				}
			}
			select {
			case <-wait.Done():
				_ = recordWindowsAttachFailure(cfg.DataDir, proc, errors.New("Kimi Code 尚未退出，请从客户端菜单退出后再打开"))
				return
			case <-time.After(400 * time.Millisecond):
			}
		}
		launch, stop := context.WithTimeout(s.ctx, 75*time.Second)
		defer stop()
		_, err := RunWindowsLauncherPlatform(launch, WindowsLaunchOptions{
			DataDir: cfg.DataDir, KimiHome: cfg.KimiHome, Profile: cfg.Profile, Executable: cfg.Executable,
			KimiExecutable: proc.Executable, AllowDailyInstance: cfg.Profile == "",
		})
		if err != nil {
			_ = recordWindowsAttachFailure(cfg.DataDir, proc, fmt.Errorf("无法重新打开 Kimi Code：%w", err))
		}
	}()
	return map[string]any{"reopening": true}, nil
}

func windowsCompatibilityBrowser(ctx context.Context, proc WindowsProcessInfo, profile string) (*websocket.Conn, error) {
	unknown := errors.New("无法确认当前客户端处于兼容接入模式。请从客户端菜单退出，再从 KimiCode+ 入口打开。")
	port, ok := extractDebugPortArg(SplitWindowsCommandLine(proc.CommandLine))
	if !ok {
		return nil, unknown
	}
	listeners, err := windowsListListeners(ctx, port)
	if err != nil || VerifyWindowsDebugPortOwner(listeners, port, proc.PID) != nil {
		return nil, unknown
	}
	var version WindowsCDPVersion
	if err := fetchWindowsCDPJSON(ctx, port, "/json/version", &version); err != nil {
		return nil, unknown
	}
	endpoint, err := url.Parse(version.WebSocketDebuggerURL)
	if err != nil || endpoint.Scheme != "ws" || endpoint.Host != "127.0.0.1:"+strconv.Itoa(port) || endpoint.User != nil ||
		endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" ||
		!strings.HasPrefix(endpoint.EscapedPath(), "/devtools/browser/") ||
		!windowsNodeTargetPath.MatchString(strings.TrimPrefix(endpoint.EscapedPath(), "/devtools/browser")) {
		return nil, unknown
	}
	if err := validateLiveWindowsHost(ctx, AttachOptions{Profile: profile}, proc); err != nil {
		return nil, err
	}
	client := &http.Client{Transport: &http.Transport{}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	conn, _, err := websocket.Dial(ctx, endpoint.String(), &websocket.DialOptions{HTTPClient: client})
	return conn, err
}

func confirmReopenIdle(ctx context.Context, home string, pid int) error {
	unknown := errors.New("无法确认当前客户端的会话是否空闲，Kimi Code 保持打开")
	base, token, err := readKAPEndpoint(home, pid)
	if err != nil {
		return unknown
	}
	endpoint, err := url.Parse(base)
	if err != nil {
		return unknown
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil {
		return unknown
	}
	listeners, err := windowsListListeners(ctx, port)
	if err != nil || VerifyWindowsDebugPortOwner(listeners, port, pid) != nil {
		return unknown
	}
	probe, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	kap := NewKAPClient(home)
	kap.base, kap.token = base, token
	kap.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer kap.client.CloseIdleConnections()
	scan := newReopenSessionsScan()
	query := url.Values{"page_size": {strconv.Itoa(reopenSessionPageSize)}}
	for {
		raw, err := kap.send(probe, http.MethodGet, "/sessions?"+query.Encode(), nil)
		if err != nil {
			return unknown
		}
		hasMore, nextToken, err := scan.checkPage(raw)
		if err != nil {
			return err
		}
		if !hasMore {
			return nil
		}
		query.Set("page_token", nextToken)
	}
}
