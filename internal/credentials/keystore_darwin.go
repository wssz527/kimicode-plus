//go:build darwin

package credentials

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const securityCLI = "/usr/bin/security"

// keychainTimeout bounds one `security` invocation. A locked screen or a
// Keychain prompt would otherwise block the caller (and core startup) forever.
const keychainTimeout = 5 * time.Second

func keychainContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.WithTimeout(context.Background(), keychainTimeout)
	}
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, keychainTimeout)
}

type DarwinKeyStore struct{ account string }

func NewOSKeyStore(dataDir string) OSKeyStore {
	return &DarwinKeyStore{account: keychainAccount(dataDir)}
}

func (k *DarwinKeyStore) Available() bool {
	info, err := os.Stat(securityCLI)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0
}

func (k *DarwinKeyStore) BackendName() string { return "MACOS_KEYCHAIN" }

func (k *DarwinKeyStore) Load(ctx context.Context) (string, bool, error) {
	stored, found, err := k.loadRaw(ctx)
	if err != nil || !found {
		return "", found, err
	}
	secret, err := decodeKeyStoreSecret(stored)
	if err != nil {
		return "", false, err
	}
	return secret, true, nil
}

// loadRaw reads the stored value without interpreting it, so Save can compare
// what it wrote without decoding.
func (k *DarwinKeyStore) loadRaw(ctx context.Context) (string, bool, error) {
	ctx, cancel := keychainContext(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, securityCLI, "find-generic-password", "-s", osKeyStoreService, "-a", k.account, "-w")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 44 {
			return "", false, nil
		}
		if strings.Contains(stderr.String(), "could not be found") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("keychain load: %w", err)
	}
	return strings.TrimRight(stdout.String(), "\r\n"), true, nil
}

// Save writes through `security -i` so the secret arrives on stdin instead of
// appearing in the process arguments. Interactive mode exits 0 even when a
// command fails, so the value is read back and compared before reporting
// success.
func (k *DarwinKeyStore) Save(ctx context.Context, secret string) error {
	ctx, cancel := keychainContext(ctx)
	defer cancel()
	stored := encodeKeyStoreSecret(secret)
	command := fmt.Sprintf("add-generic-password -U -s %s -a %s -w %s\n",
		quoteKeychainArg(osKeyStoreService), quoteKeychainArg(k.account), quoteKeychainArg(stored))
	cmd := exec.CommandContext(ctx, securityCLI, "-i")
	cmd.Stdin = strings.NewReader(command)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("keychain save: %w: %s", err, strings.TrimSpace(string(out)))
	}
	saved, found, err := k.loadRaw(ctx)
	if err != nil {
		return err
	}
	if !found || saved != stored {
		return errors.New("keychain save did not persist the secret")
	}
	return nil
}

func (k *DarwinKeyStore) Delete(ctx context.Context) error {
	ctx, cancel := keychainContext(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, securityCLI, "delete-generic-password", "-s", osKeyStoreService, "-a", k.account)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 44 {
			return nil
		}
		if strings.Contains(stderr.String(), "could not be found") {
			return nil
		}
		return fmt.Errorf("keychain delete: %w", err)
	}
	return nil
}

func quoteKeychainArg(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}
