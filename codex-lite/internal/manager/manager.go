package manager

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/codex-lite/internal/auth"
)

type Manager struct {
	authDir string
	tokens  []*auth.TokenStorage
	mu      sync.RWMutex
	index   uint64
}

func NewManager(authDir string) *Manager {
	return &Manager{authDir: authDir}
}

func (m *Manager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	entries, err := os.ReadDir(m.authDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	m.tokens = nil
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(m.authDir, e.Name())
		t, err := auth.LoadToken(path)
		if err != nil {
			continue
		}
		if t.Type == "codex" {
			m.tokens = append(m.tokens, t)
		}
	}
	return nil
}

func (m *Manager) Pick() *auth.TokenStorage {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.tokens) == 0 {
		return nil
	}
	idx := atomic.AddUint64(&m.index, 1)
	return m.tokens[idx%uint64(len(m.tokens))]
}

func (m *Manager) List() []*auth.TokenStorage {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tokens
}

func (m *Manager) Add(t *auth.TokenStorage) error {
	filename := t.Email + ".json"
	if t.Email == "" {
		filename = t.AccountID + ".json"
	}
	path := filepath.Join(m.authDir, filename)
	if err := t.Save(path); err != nil {
		return err
	}
	return m.Load()
}
