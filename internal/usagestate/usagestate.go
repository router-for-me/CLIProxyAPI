// Package usagestate persists runtime usage state to a single file so the
// management UI survives restarts. It stores per-credential request counters and
// the token distribution (input/cache/output/thinking) together.
//
// Writes are deliberately kept off the request path: producers only advance a
// revision counter, and a background loop flushes at most once per interval and
// only when something actually changed.
package usagestate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/usageaggregate"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	// DefaultInterval is how often dirty state is flushed to disk. The flush runs
	// asynchronously from in-memory state, so it never blocks the request path.
	// It bounds both write frequency under load and how much is lost on an
	// unclean exit.
	DefaultInterval = 10 * time.Second

	// FileName is the state file written inside the usage directory.
	//
	// It deliberately does NOT end in ".json": the auth token store walks the
	// auth directory recursively and loads every ".json" file as a credential,
	// so a JSON-named state file would surface in the management panel as a bogus
	// credential. The bundled cooldown state uses the same convention (.cds).
	FileName = "usage.state"

	// stateVersion is the on-disk schema version of FileName.
	stateVersion = 1
)

// CountersSource provides and accepts per-credential request counters.
type CountersSource interface {
	// RequestCountersRevision advances whenever any counter changes.
	RequestCountersRevision() uint64
	// RequestCountersSnapshot returns the current counters.
	RequestCountersSnapshot() []coreauth.AuthRequestCounts
	// ApplyRequestCounters seeds persisted counters into registered credentials.
	ApplyRequestCounters(records []coreauth.AuthRequestCounts) int
}

// TokensSource provides and accepts the token distribution aggregate.
type TokensSource interface {
	// Revision advances whenever the aggregate changes.
	Revision() uint64
	// Snapshot returns the current aggregate.
	Snapshot() usageaggregate.UsageSnapshot
	// Restore replaces the aggregate with a persisted snapshot.
	Restore(snapshot usageaggregate.UsageSnapshot)
}

// fileState is the single-file payload.
type fileState struct {
	Version   int                           `json:"version"`
	UpdatedAt time.Time                     `json:"updated_at"`
	Counters  []coreauth.AuthRequestCounts  `json:"counters,omitempty"`
	Tokens    *usageaggregate.UsageSnapshot `json:"tokens,omitempty"`
}

// Manager loads, periodically flushes, and finally flushes the usage state file.
type Manager struct {
	path     string
	interval time.Duration

	counters CountersSource
	tokens   TokensSource

	flushMu sync.Mutex

	mu              sync.Mutex
	lastCountersRev uint64
	lastTokensRev   uint64
	cancel          context.CancelFunc
	done            chan struct{}
}

// New creates a manager for path. A non-positive interval uses DefaultInterval.
func New(path string, interval time.Duration, counters CountersSource, tokens TokensSource) *Manager {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Manager{
		path:     path,
		interval: interval,
		counters: counters,
		tokens:   tokens,
	}
}

// UsageStatePath returns the state file path for an auth directory.
func UsageStatePath(authDir string) string {
	authDir = filepath.Clean(authDir)
	if authDir == "" || authDir == "." {
		return FileName
	}
	return filepath.Join(authDir, "usage", FileName)
}

// Load reads the state file and applies it to the sources. A missing or empty
// file is not an error.
func (m *Manager) Load() error {
	if m == nil || m.path == "" {
		return nil
	}
	data, errRead := os.ReadFile(m.path)
	if errRead != nil {
		if errors.Is(errRead, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read usage state: %w", errRead)
	}
	if len(data) == 0 {
		return nil
	}

	var state fileState
	if errUnmarshal := json.Unmarshal(data, &state); errUnmarshal != nil {
		return fmt.Errorf("parse usage state: %w", errUnmarshal)
	}

	if m.counters != nil && len(state.Counters) > 0 {
		m.counters.ApplyRequestCounters(state.Counters)
	}
	if m.tokens != nil && state.Tokens != nil {
		m.tokens.Restore(*state.Tokens)
	}

	// Treat the loaded state as already persisted so we only rewrite on change.
	m.mu.Lock()
	m.lastCountersRev = m.countersRevision()
	m.lastTokensRev = m.tokensRevision()
	m.mu.Unlock()
	return nil
}

// Run starts the background flush loop. It is a no-op when already running.
func (m *Manager) Run() {
	if m == nil || m.path == "" {
		return
	}

	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.cancel = cancel
	m.done = done
	m.mu.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if errFlush := m.Flush(ctx); errFlush != nil && ctx.Err() == nil {
					log.Warnf("failed to persist usage state: %v", errFlush)
				}
			}
		}
	}()
}

// Close stops the flush loop and performs a final flush.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	cancel := m.cancel
	done := m.done
	m.cancel = nil
	m.done = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	if errFlush := m.Flush(context.Background()); errFlush != nil {
		log.Warnf("failed to persist usage state on shutdown: %v", errFlush)
	}
}

// Flush writes the state file when either source changed since the last write.
// It is safe to call concurrently and never blocks producers: snapshots are
// taken outside the manager lock and disk I/O happens outside it too.
func (m *Manager) Flush(ctx context.Context) error {
	if m == nil || m.path == "" {
		return nil
	}
	if ctx != nil {
		if errCtx := ctx.Err(); errCtx != nil {
			return errCtx
		}
	}

	countersRev := m.countersRevision()
	tokensRev := m.tokensRevision()

	m.mu.Lock()
	unchanged := countersRev == m.lastCountersRev && tokensRev == m.lastTokensRev
	m.mu.Unlock()
	if unchanged {
		return nil
	}

	m.flushMu.Lock()
	defer m.flushMu.Unlock()

	state := fileState{Version: stateVersion, UpdatedAt: time.Now().UTC()}
	if m.counters != nil {
		state.Counters = m.counters.RequestCountersSnapshot()
	}
	if m.tokens != nil {
		tokens := m.tokens.Snapshot()
		state.Tokens = &tokens
	}

	data, errMarshal := json.MarshalIndent(state, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("marshal usage state: %w", errMarshal)
	}
	data = append(data, '\n')

	if errWrite := writeFileAtomic(m.path, data); errWrite != nil {
		return errWrite
	}

	// Only record the revisions after a successful write so a failure retries.
	m.mu.Lock()
	if m.lastCountersRev < countersRev {
		m.lastCountersRev = countersRev
	}
	if m.lastTokensRev < tokensRev {
		m.lastTokensRev = tokensRev
	}
	m.mu.Unlock()
	return nil
}

func (m *Manager) countersRevision() uint64 {
	if m == nil || m.counters == nil {
		return 0
	}
	return m.counters.RequestCountersRevision()
}

func (m *Manager) tokensRevision() uint64 {
	if m == nil || m.tokens == nil {
		return 0
	}
	return m.tokens.Revision()
}

// writeFileAtomic writes data to path via a temp file and rename, matching the
// cooldown state store. The directory is created with 0700.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return fmt.Errorf("create usage state directory: %w", errMkdir)
	}

	tmpFile, errCreate := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if errCreate != nil {
		return fmt.Errorf("create usage state temp file: %w", errCreate)
	}
	tmp := tmpFile.Name()
	if _, errWrite := tmpFile.Write(data); errWrite != nil {
		if errClose := tmpFile.Close(); errClose != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("write usage state temp file: %w; close temp file: %v", errWrite, errClose)
		}
		_ = os.Remove(tmp)
		return fmt.Errorf("write usage state temp file: %w", errWrite)
	}
	if errClose := tmpFile.Close(); errClose != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close usage state temp file: %w", errClose)
	}
	if errRename := os.Rename(tmp, path); errRename != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace usage state file: %w", errRename)
	}
	return nil
}
