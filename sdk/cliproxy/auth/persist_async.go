package auth

import (
	"context"
	"strings"

	log "github.com/sirupsen/logrus"
)

func (m *Manager) enqueuePersist(auth *Auth) {
	if m == nil || auth == nil || m.store == nil {
		return
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return
	}

	m.persistStartOnce.Do(func() {
		m.persistWake = make(chan struct{}, 1)
		go m.persistWorker()
	})

	m.persistMu.Lock()
	if m.persistPending == nil {
		m.persistPending = make(map[string]*Auth)
	}
	m.persistPending[authID] = auth
	m.persistMu.Unlock()

	select {
	case m.persistWake <- struct{}{}:
	default:
	}
}

func (m *Manager) persistWorker() {
	for range m.persistWake {
		for {
			batch := m.takePendingPersistBatch()
			if len(batch) == 0 {
				break
			}
			for _, auth := range batch {
				if auth == nil {
					continue
				}
				if err := m.persist(context.Background(), auth); err != nil {
					log.WithError(err).Warnf("auth manager: async persist failed for %s", strings.TrimSpace(auth.ID))
				}
			}
		}
	}
}

func (m *Manager) takePendingPersistBatch() map[string]*Auth {
	if m == nil {
		return nil
	}
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
	if len(m.persistPending) == 0 {
		return nil
	}
	batch := m.persistPending
	m.persistPending = make(map[string]*Auth)
	return batch
}
