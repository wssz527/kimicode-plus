package credentials

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

const KimiAccountValueLimit = 64 << 10
const KimiAccountRefPrefix = "kimi-code-account/"

// KimiAccountVault stores encrypted Kimi Code subscription credential payloads
// under the shared Vault key in a dedicated table so plugin credential queries
// never list or access Kimi Code login tokens.
type KimiAccountVault struct{ vault *Vault }

func (v *Vault) KimiAccountStore() *KimiAccountVault { return &KimiAccountVault{vault: v} }

func (a *KimiAccountVault) Unlocked() bool {
	if a == nil || a.vault == nil {
		return false
	}
	return a.vault.Unlocked()
}

func validKimiAccountRef(ref string) bool {
	return strings.HasPrefix(ref, KimiAccountRefPrefix) && len(ref) > len(KimiAccountRefPrefix) && refPattern.MatchString(ref)
}

func (a *KimiAccountVault) Exists(ctx context.Context, ref string) (bool, error) {
	if a == nil || a.vault == nil {
		return false, ErrUnavailable
	}
	if !validKimiAccountRef(ref) {
		return false, errors.New("invalid kimi account credential ref")
	}
	v := a.vault
	v.mu.Lock()
	defer v.mu.Unlock()
	var one int
	err := v.db.QueryRowContext(ctx, `SELECT 1 FROM vault_kimi_accounts WHERE ref=?`, ref).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (a *KimiAccountVault) Put(ctx context.Context, ref string, value []byte) error {
	if a == nil || a.vault == nil {
		return ErrUnavailable
	}
	v := a.vault
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.key) == 0 {
		return ErrLocked
	}
	if !validKimiAccountRef(ref) || len(value) == 0 {
		return errors.New("invalid kimi account credential")
	}
	if len(value) > KimiAccountValueLimit {
		return ErrTooLarge
	}
	data, err := seal(v.key, value, ref)
	if err != nil {
		return err
	}
	_, err = v.db.ExecContext(ctx, `INSERT INTO vault_kimi_accounts(ref,value) VALUES(?,?) ON CONFLICT(ref) DO UPDATE SET value=excluded.value`, ref, data)
	return err
}

func (a *KimiAccountVault) Get(ctx context.Context, ref string) ([]byte, error) {
	if a == nil || a.vault == nil {
		return nil, ErrUnavailable
	}
	if !validKimiAccountRef(ref) {
		return nil, errors.New("invalid kimi account credential ref")
	}
	v := a.vault
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.key) == 0 {
		return nil, ErrLocked
	}
	var data []byte
	err := v.db.QueryRowContext(ctx, `SELECT value FROM vault_kimi_accounts WHERE ref=?`, ref).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return open(v.key, data, ref)
}

func (a *KimiAccountVault) Delete(ctx context.Context, ref string) error {
	if a == nil || a.vault == nil {
		return ErrUnavailable
	}
	if !validKimiAccountRef(ref) {
		return errors.New("invalid kimi account credential ref")
	}
	v := a.vault
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.key) == 0 {
		return ErrLocked
	}
	_, err := v.db.ExecContext(ctx, `DELETE FROM vault_kimi_accounts WHERE ref=?`, ref)
	return err
}
