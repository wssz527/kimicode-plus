//go:build !darwin && !windows

package credentials

import (
	"context"
	"errors"
)

type unavailableKeyStore struct{}

func NewOSKeyStore(string) OSKeyStore { return unavailableKeyStore{} }

func (unavailableKeyStore) Available() bool { return false }

func (unavailableKeyStore) BackendName() string { return "NONE" }

func (unavailableKeyStore) Load(context.Context) (string, bool, error) { return "", false, nil }

func (unavailableKeyStore) Save(context.Context, string) error {
	return errors.New("OS key store is not available on this platform")
}

func (unavailableKeyStore) Delete(context.Context) error { return nil }
