package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"enhancedkimi/internal/credentials"
	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/security"
)

// importMaxBytes caps a user-provided credentials file. Anything larger is
// rejected before parsing so a mistaken path (a disk image, a database) cannot
// be pulled into memory.
const importMaxBytes = 64 * 1024

var sensitiveRefKeys = map[string]bool{
	"apikey": true, "api_key": true, "accesstoken": true, "access_token": true,
	"refreshtoken": true, "refresh_token": true, "token": true, "secret": true,
	"password": true, "key": true,
}

func sensitiveName(key string) bool {
	return sensitiveRefKeys[strings.ToLower(strings.TrimSpace(key))]
}

// checkImportPath enforces the read-only boundary: absolute, regular file, not
// a symlink, not a device/fifo, and within the size limit. Which file the user
// names is the user's decision, including an official client's own file: the
// boundary is explicit consent plus read-only access, not a path blocklist.
// Nothing is ever discovered by scanning; there is no ambient lookup.
func checkImportPath(raw string) (string, error) {
	cleaned := filepath.Clean(raw)
	if !filepath.IsAbs(cleaned) {
		return "", badRequest("INVALID_PATH", "必须提供绝对文件路径")
	}
	info, err := os.Lstat(cleaned)
	if err != nil {
		return "", badRequest("FILE_UNAVAILABLE", "无法读取该路径；请确认文件存在且可读")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", badRequest("SYMLINK_DENIED", "不接受符号链接，请提供真实文件路径")
	}
	if !info.Mode().IsRegular() {
		return "", badRequest("NOT_REGULAR_FILE", "只接受普通文件")
	}
	if info.Size() == 0 || info.Size() > importMaxBytes {
		return "", badRequest("FILE_SIZE", "文件必须非空且不超过 64 KiB")
	}
	return cleaned, nil
}

type parsedImport struct {
	Preview ImportPreview
	APIKey  string
	Access  string
	Refresh string
}

func parseImportFile(path string) (parsedImport, error) {
	file, err := os.Open(path)
	if err != nil {
		return parsedImport{}, badRequest("FILE_UNAVAILABLE", "无法读取该路径")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, importMaxBytes+1))
	if err != nil {
		return parsedImport{}, badRequest("FILE_UNAVAILABLE", "无法读取该路径")
	}
	if len(data) > importMaxBytes {
		return parsedImport{}, badRequest("FILE_SIZE", "文件超过 64 KiB")
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return parsedImport{}, badRequest("INVALID_IMPORT_FILE", "文件必须是单层 JSON 对象")
	}
	sum := sha256.Sum256(data)
	preview := ImportPreview{Path: path, Bytes: len(data), Fingerprint: "sha256:" + hex.EncodeToString(sum[:])}
	values := map[string]string{}
	fields := []string{}
	unknown := []string{}
	for key, raw := range document {
		if sensitiveName(key) {
			var value string
			if json.Unmarshal(raw, &value) == nil && value != "" {
				values[strings.ToLower(key)] = value
			}
			fields = append(fields, key)
			continue
		}
		if key == "expiresAt" || key == "expires_at" {
			fields = append(fields, key)
			continue
		}
		unknown = append(unknown, key)
	}
	sort.Strings(fields)
	sort.Strings(unknown)
	preview.Fields = fields
	preview.Unknown = unknown
	preview.HasAPIKey = values["apikey"] != "" || values["api_key"] != ""
	preview.HasTokenPair = (values["accesstoken"] != "" || values["access_token"] != "") &&
		(values["refreshtoken"] != "" || values["refresh_token"] != "")
	// expiry is reported only when it is understood. An unparsable value stays
	// absent instead of being reported as a guessed timestamp.
	for key, raw := range document {
		if !strings.EqualFold(key, "expiresAt") && !strings.EqualFold(key, "expires_at") {
			continue
		}
		if normalized, ok := parseExpiry(raw); ok {
			preview.ExpiresAt = &normalized
		}
	}
	parse := parsedImport{
		Preview: preview,
		APIKey:  first(values, "apikey", "api_key"),
		Access:  first(values, "accesstoken", "access_token"),
		Refresh: first(values, "refreshtoken", "refresh_token"),
	}
	if parse.APIKey == "" && parse.Access == "" {
		return parsedImport{}, badRequest("NO_CREDENTIAL", "文件中没有可识别的 apiKey 或 accessToken")
	}
	if parse.APIKey != "" && len(parse.APIKey) > 32768 {
		return parsedImport{}, badRequest("CREDENTIAL_TOO_LARGE", "凭据超过凭据库 32 KiB 上限")
	}
	return parse, nil
}

// expirySecondsLimit keeps a numeric expiry inside a plausible Unix-second
// range. The official client stores Unix seconds; a millisecond value or a
// garbage number must not become a confident timestamp.
const (
	expiryMinSeconds = 1
	expiryMaxSeconds = 4102444800 // 2100-01-01T00:00:00Z
)

// parseExpiry normalizes the official numeric Unix-second expiry to a UTC
// RFC3339 string, and keeps an RFC3339 string as a compatibility input. The
// value is informational: this build never refreshes or writes it back.
func parseExpiry(raw json.RawMessage) (string, bool) {
	var seconds float64
	if err := json.Unmarshal(raw, &seconds); err == nil {
		if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds != math.Trunc(seconds) {
			return "", false
		}
		if seconds < expiryMinSeconds || seconds > expiryMaxSeconds {
			return "", false
		}
		return time.Unix(int64(seconds), 0).UTC().Format(time.RFC3339), true
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(text)); err == nil {
			return parsed.UTC().Format(time.RFC3339), true
		}
	}
	return "", false
}

func first(values map[string]string, keys ...string) string {
	for _, key := range keys {
		if values[key] != "" {
			return values[key]
		}
	}
	return ""
}

// previewImport reads and parses a user-named file without storing anything.
func (s *Service) previewImport(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path string `json:"path"`
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	path, err := checkImportPath(in.Path)
	if err != nil {
		fail(w, err)
		return
	}
	parsed, err := parseImportFile(path)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"preview": parsed.Preview, "contract": importContract()})
}

// applyImport stores the parsed credential as a new disabled account. The
// account starts disabled on purpose: the user reviews the preview and enables
// it explicitly, so an import can never silently take over traffic.
func (s *Service) applyImport(secrets credentials.InsertStore) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Path        string `json:"path"`
			Label       string `json:"label"`
			Fingerprint string `json:"fingerprint"`
			AccountID   string `json:"accountId"`
		}
		if !httpx.Decode(w, r, &in) {
			return
		}
		providerID := r.PathValue("id")
		path, err := checkImportPath(in.Path)
		if err != nil {
			fail(w, err)
			return
		}
		parsed, err := parseImportFile(path)
		if err != nil {
			fail(w, err)
			return
		}
		if strings.TrimSpace(in.Fingerprint) != parsed.Preview.Fingerprint {
			fail(w, badRequest("IMPORT_CHANGED", "文件与预览时的内容不一致，请重新预览后再导入"))
			return
		}
		label := strings.TrimSpace(in.Label)
		if label == "" {
			label = "官方登录导入（只读）"
		}
		id := strings.TrimSpace(in.AccountID)
		if id == "" {
			id = "import-" + security.Random()[:16]
		}
		ref := "provider/" + providerID + "/" + id
		if !credentials.ValidRef(ref) {
			fail(w, badRequest("INVALID_SECRET_REF", "账号 ID 无法构成有效的凭据引用"))
			return
		}
		if secrets == nil {
			fail(w, errf(503, "VAULT_UNAVAILABLE", "凭据库未启用"))
			return
		}
		secret := []byte(parsed.APIKey)
		if len(secret) == 0 {
			secret = []byte(parsed.Access)
		}
		if err := secrets.PutNew(r.Context(), ref, secret); err != nil {
			clear(secret)
			if errors.Is(err, credentials.ErrAlreadyExists) {
				fail(w, conflict("ACCOUNT_CONFLICT", "账号 ID 或凭据引用已被占用"))
				return
			}
			if errors.Is(err, credentials.ErrLocked) {
				fail(w, errf(423, "VAULT_LOCKED", "请先解锁凭据库再导入"))
				return
			}
			fail(w, errf(422, "VAULT_WRITE", "无法保存导入的凭据"))
			return
		}
		clear(secret)
		disabled := false
		account, err := s.CreateAccount(r.Context(), providerID, accountInput{ID: id, Label: label, SecretRef: &ref, Enabled: &disabled})
		if err != nil {
			_ = secrets.Delete(r.Context(), ref)
			fail(w, err)
			return
		}
		httpx.JSON(w, 201, map[string]any{"account": account, "preview": parsed.Preview, "writesOfficial": false, "note": "已保存为停用账号；请核对后手动启用。刷新令牌仅记录存在性，未解释或存储。"})
	}
}
