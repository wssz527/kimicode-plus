package credentials

import (
	"context"
	"errors"
	"sync"
)

var ErrUnavailable = errors.New("OS credential backend is not enabled in this phase")
var ErrNotFound = errors.New("credential reference not found")
var ErrAlreadyExists = errors.New("credential reference already exists")

type Store interface {
	Put(context.Context, string, []byte) error
	Get(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
}
type InsertStore interface {
	Store
	PutNew(context.Context, string, []byte) error
}
type Memory struct {
	mu      sync.Mutex
	entries map[string][]byte
}

func NewMemory() *Memory { return &Memory{entries: make(map[string][]byte)} }
func (m *Memory) Put(_ context.Context, ref string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[ref] = append([]byte(nil), value...)
	return nil
}
func (m *Memory) PutNew(_ context.Context, ref string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.entries[ref]; exists {
		return ErrAlreadyExists
	}
	m.entries[ref] = append([]byte(nil), value...)
	return nil
}
func (m *Memory) Get(_ context.Context, ref string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.entries[ref]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), v...), nil
}
func (m *Memory) Delete(_ context.Context, ref string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.entries[ref] {
		m.entries[ref][i] = 0
	}
	delete(m.entries, ref)
	return nil
}

// ValidRef reports whether ref is acceptable for the vault. Account and
// provider configuration share this rule so a stored reference is always a
// reference the vault can actually serve.
func ValidRef(ref string) bool { return refPattern.MatchString(ref) }
