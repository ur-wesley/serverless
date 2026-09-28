package artifacts

import "context"
import "sync"

// MemStore is an in-memory Store for tests and offline dev.
type MemStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func NewMemory() *MemStore { return &MemStore{data: map[string][]byte{}} }

func (m *MemStore) Put(_ context.Context, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := append([]byte{}, data...)
	m.data[key] = cp
	return nil
}

func (m *MemStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[key]
	if !ok {
		return nil, errNotFound(key)
	}
	return append([]byte{}, v...), nil
}

func (m *MemStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

type notFoundError struct{ key string }

func (e *notFoundError) Error() string { return "not found: " + e.key }

func errNotFound(key string) error { return &notFoundError{key: key} }
