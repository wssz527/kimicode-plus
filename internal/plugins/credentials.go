package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"enhancedkimi/internal/credentials"
)

// A plugin cannot read a credential value, and the core never returns one. What
// a plugin needs to build a working settings page is smaller than that: which of
// the credential names it declared exist in the vault, and whether the vault is
// unlocked right now. Everything else stays behind the Go boundary.
//
// `credentials.save` writes one value into the vault on the user's behalf. The
// plaintext travels from the plugin's settings form to this function and into
// the encrypted vault; it is never echoed in a result, an activity row or an
// error message. The plugin can only write a name its manifest declared, so a
// compromised plugin cannot plant a secret another plugin is allowed to use.

// credentialStatus is the whole credential surface a plugin may observe.
type credentialStatus struct {
	Name     string `json:"name"`
	Stored   bool   `json:"stored"`
	Unlocked bool   `json:"unlocked"`
	// Usable means a request with this credential would be able to inject a
	// value now: the vault is unlocked and the entry exists.
	Usable bool `json:"usable"`
}

func (m *Manager) credentialsAction(ctx context.Context, id string, grants Grants, action string, r *http.Request, params json.RawMessage) (any, error) {
	switch action {
	case "credentials.list":
		return m.listCredentials(ctx, grants)
	case "credentials.save":
		return m.saveCredential(ctx, id, grants, r, params)
	case "credentials.delete":
		return m.deleteCredential(ctx, id, grants, r, params)
	}
	return nil, apiError(http.StatusBadRequest, "PLUGIN_ACTION_UNKNOWN", "未知的凭据动作")
}

func (m *Manager) listCredentials(ctx context.Context, grants Grants) (any, error) {
	names := grants.Credentials()
	if len(names) == 0 {
		return nil, capabilityDenied("credentials.list", capCredentialPre+"<name>")
	}
	vault := m.vaultValue()
	unlocked := vault != nil && vault.Unlocked()
	out := make([]credentialStatus, 0, len(names))
	for _, name := range names {
		status := credentialStatus{Name: name, Unlocked: unlocked}
		if vault != nil {
			stored, err := vault.Exists(ctx, name)
			if err != nil {
				return nil, apiError(http.StatusInternalServerError, "PLUGIN_CREDENTIAL_UNAVAILABLE", "无法读取凭据状态")
			}
			status.Stored = stored
		}
		status.Usable = status.Stored && unlocked
		out = append(out, status)
	}
	return map[string]any{"items": out, "unlocked": unlocked}, nil
}

func (m *Manager) saveCredential(ctx context.Context, id string, grants Grants, r *http.Request, params json.RawMessage) (any, error) {
	var input struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := decodeInto(ctx, r, params, &input); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_CREDENTIAL_NAME", "缺少凭据名")
	}
	if err := grants.AuthorizeCredential("credentials.save", name); err != nil {
		return nil, err
	}
	value := []byte(input.Value)
	if len(value) == 0 {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_CREDENTIAL_VALUE", "凭据值不能为空")
	}
	if len(value) > 32768 {
		return nil, apiError(http.StatusRequestEntityTooLarge, "PLUGIN_CREDENTIAL_VALUE", "凭据值超过 32KiB")
	}
	vault := m.vaultValue()
	if vault == nil {
		return nil, apiError(http.StatusServiceUnavailable, "PLUGIN_CREDENTIAL_UNAVAILABLE", "凭据库不可用")
	}
	if err := vault.Put(ctx, name, value); err != nil {
		if errors.Is(err, credentials.ErrLocked) {
			return nil, apiError(http.StatusLocked, "PLUGIN_CREDENTIAL_LOCKED", "凭据库已锁定，请先在凭据页解锁")
		}
		return nil, apiError(http.StatusInternalServerError, "PLUGIN_CREDENTIAL_WRITE", "无法写入凭据库")
	}
	_ = recordActivity(ctx, m.db, id, ActivityCredential, "插件保存了凭据 "+name, m.now())
	// The answer deliberately carries no value and no length: the settings page
	// only needs to know the entry is there now.
	return map[string]any{"saved": true, "name": name}, nil
}

// deleteCredential removes a value the plugin itself saved. A missing entry is
// not an error: the settings page asks for removal because its user wants the
// reference gone, and it already is.
func (m *Manager) deleteCredential(ctx context.Context, id string, grants Grants, r *http.Request, params json.RawMessage) (any, error) {
	var input struct {
		Name string `json:"name"`
	}
	if err := decodeInto(ctx, r, params, &input); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_CREDENTIAL_NAME", "缺少凭据名")
	}
	if err := grants.AuthorizeCredential("credentials.delete", name); err != nil {
		return nil, err
	}
	vault := m.vaultValue()
	if vault == nil {
		return nil, apiError(http.StatusServiceUnavailable, "PLUGIN_CREDENTIAL_UNAVAILABLE", "凭据库不可用")
	}
	if err := vault.Delete(ctx, name); err != nil {
		if errors.Is(err, credentials.ErrLocked) {
			return nil, apiError(http.StatusLocked, "PLUGIN_CREDENTIAL_LOCKED", "凭据库已锁定，请先在凭据页解锁")
		}
		return nil, apiError(http.StatusInternalServerError, "PLUGIN_CREDENTIAL_DELETE", "无法删除凭据")
	}
	_ = recordActivity(ctx, m.db, id, ActivityCredential, "插件删除了凭据 "+name, m.now())
	return map[string]any{"deleted": true, "name": name}, nil
}

// vaultValue reads the attached vault under the manager lock. The vault itself
// is safe for concurrent use; the lock here only makes the pointer read safe
// against UseVault.
func (m *Manager) vaultValue() *credentials.Vault {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.vault
}
