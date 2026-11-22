package credentials

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
)

const osKeyStoreService = "com.kimicode-plus.vault"
const osKeyStorePrefix = "v1:"

// OSKeyStore holds the vault passphrase in the platform's own secret store so a
// returning process can unlock the vault without asking the user for a password.
type OSKeyStore interface {
	Available() bool
	BackendName() string
	Load(ctx context.Context) (string, bool, error)
	Save(ctx context.Context, secret string) error
	Delete(ctx context.Context) error
}

// keychainAccount scopes the OS entry to one data directory, so an isolated
// vault (tests, a second profile, a portable copy) never reads or overwrites
// the user's primary vault key.
func keychainAccount(dataDir string) string {
	if dataDir == "" {
		return "default"
	}
	sum := sha256.Sum256([]byte(filepath.Clean(dataDir)))
	return "vault-" + hex.EncodeToString(sum[:])[:16]
}

// encodeKeyStoreSecret keeps the stored value printable ASCII. macOS `security`
// prints non-ASCII secrets as hex, so an opaque input could otherwise come back
// as a different string than the passphrase that was saved.
func encodeKeyStoreSecret(secret string) string {
	return osKeyStorePrefix + base64.StdEncoding.EncodeToString([]byte(secret))
}

func decodeKeyStoreSecret(stored string) (string, error) {
	if !strings.HasPrefix(stored, osKeyStorePrefix) {
		return stored, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, osKeyStorePrefix))
	if err != nil {
		return "", errors.New("OS key store secret is malformed")
	}
	return string(raw), nil
}

func randomVaultSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
