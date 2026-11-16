package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const NativeAPI = "/api/v1"

type NativeUsage struct {
	ContextTokens int64  `json:"context_tokens"`
	ContextLimit  *int64 `json:"context_limit"`
}

type NativeSession struct {
	ID                 string       `json:"id"`
	Title              string       `json:"title"`
	CWD                string       `json:"cwd,omitempty"`
	UpdatedAt          string       `json:"updated_at,omitempty"`
	LastTurnReason     string       `json:"last_turn_reason,omitempty"`
	Busy               bool         `json:"busy"`
	MainTurnActive     bool         `json:"main_turn_active"`
	PendingInteraction string       `json:"pending_interaction"`
	Usage              *NativeUsage `json:"usage,omitempty"`
}

type NativeAttachment struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
}

type NativeMessage struct {
	ID          string             `json:"id"`
	Role        string             `json:"role"`
	Text        string             `json:"text"`
	Tool        string             `json:"tool,omitempty"`
	At          string             `json:"at,omitempty"`
	Attachments []NativeAttachment `json:"attachments,omitempty"`
}

type NativeOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type NativeApproval struct {
	ID      string         `json:"id"`
	Title   string         `json:"title"`
	Detail  string         `json:"detail,omitempty"`
	Options []NativeOption `json:"options,omitempty"`
}

type SessionView struct {
	Session   NativeSession    `json:"session"`
	Messages  []NativeMessage  `json:"messages"`
	Approvals []NativeApproval `json:"approvals"`
	Goal      json.RawMessage  `json:"goal,omitempty"`
}

type nativeEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"msg"`
	Data    json.RawMessage `json:"data"`
}

type NativeError struct {
	Code    string
	Message string
}

func (e *NativeError) Error() string { return e.Message }

type KAPClient struct {
	home   string
	client *http.Client

	mu      sync.Mutex
	base    string
	token   string
	watched map[string]bool
	writeMu sync.Mutex
	socket  *websocket.Conn
}

func NewKAPClient(home string) *KAPClient {
	return &KAPClient{home: filepath.Clean(home), watched: map[string]bool{}, client: &http.Client{Transport: &http.Transport{Proxy: nil}}}
}

func (k *KAPClient) resolve() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.base != "" && k.token != "" {
		return nil
	}
	if k.home == "" || !filepath.IsAbs(k.home) {
		return errors.New("必须指定独立的 KIMI_CODE_HOME 绝对路径")
	}
	entries, err := os.ReadDir(filepath.Join(k.home, "server", "instances"))
	if err != nil {
		return errors.New("未找到官方服务实例，请确认隔离客户端已启动")
	}
	type instance struct {
		Port        int    `json:"port"`
		Host        string `json:"host"`
		HeartbeatAt int64  `json:"heartbeat_at"`
	}
	instances := []instance{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(k.home, "server", "instances", entry.Name()))
		if err != nil || len(data) > 65536 {
			continue
		}
		var item instance
		if json.Unmarshal(data, &item) == nil && item.Port > 0 && item.Port <= 65535 {
			instances = append(instances, item)
		}
	}
	if len(instances) == 0 {
		return errors.New("官方服务实例记录为空")
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].HeartbeatAt > instances[j].HeartbeatAt })
	host := instances[0].Host
	if host == "" {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("官方服务地址不是本机回环")
	}
	secret, err := os.ReadFile(filepath.Join(k.home, "server.token"))
	if err != nil {
		return errors.New("官方服务访问凭据不可用，请重新打开隔离客户端")
	}
	token := strings.TrimSpace(string(secret))
	if token == "" || len(token) > 4096 {
		return errors.New("官方服务访问凭据格式无效")
	}
	k.base = "http://" + net.JoinHostPort(host, strconv.Itoa(instances[0].Port))
	k.token = token
	return nil
}

func (k *KAPClient) endpoint() (string, string, error) {
	if err := k.resolve(); err != nil {
		return "", "", err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.base, k.token, nil
}

func (k *KAPClient) Request(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	base, token, err := k.endpoint()
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return nil, errors.New("官方接口路径无效")
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, base+NativeAPI+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := k.client.Do(req)
	if err != nil {
		return nil, errors.New("无法连接官方会话服务")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var envelope nativeEnvelope
	if len(data) > 0 && json.Unmarshal(data, &envelope) == nil && (envelope.Code != 0 || envelope.Message != "" || envelope.Data != nil) {
		if envelope.Code != 0 {
			return nil, &NativeError{Code: "NATIVE_ERROR", Message: "官方接口返回：" + envelope.Message}
		}
		if response.StatusCode >= 400 {
			return nil, &NativeError{Code: "NATIVE_HTTP", Message: "官方接口返回 HTTP " + strconv.Itoa(response.StatusCode)}
		}
		return envelope.Data, nil
	}
	if response.StatusCode >= 400 {
		return nil, &NativeError{Code: "NATIVE_HTTP", Message: "官方接口返回 HTTP " + strconv.Itoa(response.StatusCode)}
	}
	if len(data) == 0 {
		return json.RawMessage("null"), nil
	}
	return json.RawMessage(data), nil
}

func decodeNative[T any](raw json.RawMessage, label string) (T, error) {
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, fmt.Errorf("%s的响应格式与当前适配器不兼容", label)
	}
	return value, nil
}

func mapNativeSession(raw json.RawMessage) (NativeSession, error) {
	var value struct {
		ID                 string       `json:"id"`
		Title              string       `json:"title"`
		UpdatedAt          string       `json:"updated_at"`
		LastTurnReason     string       `json:"last_turn_reason"`
		Busy               bool         `json:"busy"`
		MainTurnActive     bool         `json:"main_turn_active"`
		PendingInteraction string       `json:"pending_interaction"`
		Usage              *NativeUsage `json:"usage"`
		Metadata           struct {
			CWD string `json:"cwd"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &value); err != nil || value.ID == "" {
		return NativeSession{}, errors.New("会话响应缺少有效 ID")
	}
	pending := value.PendingInteraction
	if pending == "" {
		pending = "none"
	}
	return NativeSession{ID: value.ID, Title: value.Title, CWD: value.Metadata.CWD, UpdatedAt: value.UpdatedAt, LastTurnReason: value.LastTurnReason, Busy: value.Busy, MainTurnActive: value.MainTurnActive, PendingInteraction: pending, Usage: value.Usage}, nil
}

func (k *KAPClient) Sessions(ctx context.Context) ([]NativeSession, error) {
	raw, err := k.Request(ctx, http.MethodGet, "/sessions?page_size=100", nil)
	if err != nil {
		return nil, err
	}
	page, err := decodeNative[struct {
		Items []json.RawMessage `json:"items"`
	}](raw, "会话列表")
	if err != nil {
		return nil, err
	}
	items := []NativeSession{}
	for _, raw := range page.Items {
		item, err := mapNativeSession(raw)
		if err == nil {
			items = append(items, item)
		}
	}
	return items, nil
}

func (k *KAPClient) Models(ctx context.Context) ([]map[string]string, error) {
	raw, err := k.Request(ctx, http.MethodGet, "/config", nil)
	if err != nil {
		return nil, err
	}
	config, err := decodeNative[map[string]any](raw, "模型配置")
	if err != nil {
		return nil, err
	}
	models, _ := config["models"].(map[string]any)
	items := []map[string]string{}
	for id, raw := range models {
		name := id
		if value, ok := raw.(map[string]any); ok {
			for _, key := range []string{"display_name", "displayName", "name"} {
				if candidate, ok := value[key].(string); ok && candidate != "" {
					name = candidate
					break
				}
			}
		}
		items = append(items, map[string]string{"id": id, "name": name})
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["name"] < items[j]["name"] })
	return items, nil
}

func (k *KAPClient) websocketURL() (string, string, error) {
	base, token, err := k.endpoint()
	if err != nil {
		return "", "", err
	}
	u, err := url.Parse(base + NativeAPI + "/ws")
	if err != nil {
		return "", "", err
	}
	u.Scheme = "ws"
	return u.String(), token, nil
}
