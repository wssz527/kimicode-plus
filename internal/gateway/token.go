package gateway

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"enhancedkimi/internal/security"
)

const tokenFile = "gateway.json"

// tokenRecord is the on-disk form. Only the token lives here; no supplier key
// is ever derived from or stored next to it.
type tokenRecord struct {
	Token     string `json:"token"`
	CreatedAt string `json:"createdAt"`
	Note      string `json:"note"`
}

// LoadOrCreateToken returns the stable local gateway token from dir. A new token
// is generated only when no valid record exists, so a restarted process keeps
// working with a configuration the user already approved. The file is 0600 in a
// 0700 directory, like the existing runtime file.
func LoadOrCreateToken(dir string) (string, error) {
	path := filepath.Join(dir, tokenFile)
	data, err := os.ReadFile(path)
	if err == nil {
		var record tokenRecord
		if json.Unmarshal(data, &record) == nil && len(record.Token) >= 32 {
			return record.Token, nil
		}
		// A corrupt file must be replaced, not trusted partially.
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	token := security.Random()
	payload, err := json.MarshalIndent(tokenRecord{Token: token, Note: "本机网关令牌；仅用于 Kimi Code 与本地网关之间，不写入供应商密钥"}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(dir, "gateway-*")
	if err != nil {
		return "", err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return "", err
	}
	if _, err := temp.Write(payload); err != nil {
		temp.Close()
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tempName, path); err != nil {
		return "", err
	}
	return token, nil
}

// MaskToken renders a token for logs and health output. The full value is shown
// only through an explicit authenticated endpoint.
func MaskToken(token string) string {
	if len(token) < 12 {
		return "****"
	}
	return token[:4] + "…" + token[len(token)-4:]
}
