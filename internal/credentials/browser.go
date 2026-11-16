package credentials

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const BrowserValueLimit = 8 << 20
const BrowserPreviewTTL = 24 * time.Hour

var ErrTooLarge = errors.New("encrypted value exceeds storage limit")

type BrowserVault struct{ vault *Vault }

func (v *Vault) BrowserStore() *BrowserVault { return &BrowserVault{vault: v} }

func (b *BrowserVault) CollectExpired(ctx context.Context) error {
	_, err := b.vault.db.ExecContext(ctx, `DELETE FROM vault_browser_entries WHERE expires_at > 0 AND expires_at <= ?`, time.Now().Unix())
	return err
}

func (b *BrowserVault) Put(ctx context.Context, ref string, value []byte) error {
	v := b.vault
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.key) == 0 {
		return ErrLocked
	}
	if !refPattern.MatchString(ref) || len(value) == 0 {
		return errors.New("invalid browser data")
	}
	if len(value) > BrowserValueLimit {
		return ErrTooLarge
	}
	data, err := seal(v.key, value, ref)
	if err != nil {
		return err
	}
	expires := int64(0)
	if strings.HasPrefix(ref, "migration/") {
		expires = time.Now().Add(BrowserPreviewTTL).Unix()
	}
	_, err = v.db.ExecContext(ctx, `INSERT INTO vault_browser_entries(ref,value,expires_at) VALUES(?,?,?) ON CONFLICT(ref) DO UPDATE SET value=excluded.value,expires_at=excluded.expires_at`, ref, data, expires)
	return err
}

func (b *BrowserVault) Get(ctx context.Context, ref string) ([]byte, error) {
	v := b.vault
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.key) == 0 {
		return nil, ErrLocked
	}
	var data []byte
	err := v.db.QueryRowContext(ctx, `SELECT value FROM vault_browser_entries WHERE ref=? AND (expires_at=0 OR expires_at>?)`, ref, time.Now().Unix()).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return open(v.key, data, ref)
}

func (b *BrowserVault) Delete(ctx context.Context, ref string) error {
	v := b.vault
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.key) == 0 {
		return ErrLocked
	}
	_, err := v.db.ExecContext(ctx, `DELETE FROM vault_browser_entries WHERE ref=?`, ref)
	return err
}
