//go:build windows

package credentials

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

const dpapiFileName = "vault.key.dpapi"

const cryptProtectUIForbidden = 0x1

type WindowsKeyStore struct{ dataDir string }

func NewOSKeyStore(dataDir string) OSKeyStore { return &WindowsKeyStore{dataDir: dataDir} }

func (k *WindowsKeyStore) Available() bool { return k.dataDir != "" }

func (k *WindowsKeyStore) BackendName() string { return "WINDOWS_DPAPI" }

func (k *WindowsKeyStore) path() string { return filepath.Join(k.dataDir, dpapiFileName) }

func (k *WindowsKeyStore) Load(_ context.Context) (string, bool, error) {
	if !k.Available() {
		return "", false, errors.New("credential vault: data directory is not configured")
	}
	data, err := os.ReadFile(k.path())
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	plain, err := dpapiUnprotect(data)
	if err != nil {
		return "", false, err
	}
	secret, err := decodeKeyStoreSecret(string(plain))
	if err != nil {
		return "", false, err
	}
	return secret, true, nil
}

// Save seals the secret to the current Windows user; the ciphertext is useless
// from another account or machine, so the file does not need Keychain-grade ACLs.
func (k *WindowsKeyStore) Save(_ context.Context, secret string) error {
	if !k.Available() {
		return errors.New("credential vault: data directory is not configured")
	}
	protected, err := dpapiProtect([]byte(encodeKeyStoreSecret(secret)))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(k.dataDir, 0700); err != nil {
		return err
	}
	return writeFileAtomic(k.path(), protected)
}

func (k *WindowsKeyStore) Delete(_ context.Context) error {
	if !k.Available() {
		return nil
	}
	err := os.Remove(k.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

type dataBlob struct {
	size uint32
	data *byte
}

var (
	crypt32DLL         = syscall.NewLazyDLL("crypt32.dll")
	kernel32DLL        = syscall.NewLazyDLL("kernel32.dll")
	cryptProtectData   = crypt32DLL.NewProc("CryptProtectData")
	cryptUnprotectData = crypt32DLL.NewProc("CryptUnprotectData")
	localFree          = kernel32DLL.NewProc("LocalFree")
)

func dpapiProtect(plain []byte) ([]byte, error) {
	in := dataBlob{size: uint32(len(plain))}
	if len(plain) > 0 {
		in.data = &plain[0]
	}
	var out dataBlob
	ok, _, err := cryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0,
		0,
		0,
		0,
		cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	runtime.KeepAlive(plain)
	if ok == 0 {
		return nil, dpapiError("CryptProtectData", err)
	}
	defer freeDataBlob(out)
	return append([]byte(nil), unsafe.Slice(out.data, int(out.size))...), nil
}

func dpapiUnprotect(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 {
		return nil, errors.New("DPAPI ciphertext is empty")
	}
	in := dataBlob{size: uint32(len(ciphertext)), data: &ciphertext[0]}
	var out dataBlob
	ok, _, err := cryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0,
		0,
		0,
		0,
		cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&out)),
	)
	runtime.KeepAlive(ciphertext)
	if ok == 0 {
		return nil, dpapiError("CryptUnprotectData", err)
	}
	defer freeDataBlob(out)
	return append([]byte(nil), unsafe.Slice(out.data, int(out.size))...), nil
}

func freeDataBlob(blob dataBlob) {
	if blob.data != nil {
		localFree.Call(uintptr(unsafe.Pointer(blob.data)))
	}
}

func dpapiError(op string, err error) error {
	if err == nil || err == syscall.Errno(0) {
		return fmt.Errorf("%s failed", op)
	}
	return fmt.Errorf("%s: %w", op, err)
}

func writeFileAtomic(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".vault-key-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
