package cliproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/apicost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotahistory"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	routingStateVersion = 1
	// routingStateInterval is how often quota history is sampled and the
	// state file is rewritten when it changed.
	routingStateInterval = time.Minute
	// routingStatePendingTTL bounds how long restored quota snapshots are
	// re-applied to credentials that are registered or replaced after startup.
	routingStatePendingTTL = 15 * time.Minute
)

// routingStateFile is the on-disk format of routing.state-file.
type routingStateFile struct {
	Version         int                                  `json:"version"`
	SavedAt         time.Time                            `json:"saved_at"`
	Quota           map[string]coreauth.QuotaObservation `json:"quota,omitempty"`
	SessionBindings []coreauth.SessionBindingRecord      `json:"session_bindings,omitempty"`
	History         map[string][]quotahistory.Point      `json:"history,omitempty"`
	APICost         []apicost.Bucket                     `json:"api_cost,omitempty"`
}

// sessionBindingSnapshotter is implemented by selectors that keep session
// affinity bindings.
type sessionBindingSnapshotter interface {
	SnapshotSessionBindings(now time.Time) []coreauth.SessionBindingRecord
	RestoreSessionBindings(records []coreauth.SessionBindingRecord, now time.Time) int
}

// routingStatePersister samples passive quota snapshots into the history
// store and, when a path is configured, persists quota snapshots, history,
// estimated API costs, and session bindings across restarts.
type routingStatePersister struct {
	path    string
	manager *coreauth.Manager
	history *quotahistory.Store
	cost    *apicost.Store
	now     func() time.Time

	mu           sync.Mutex
	pending      map[string]coreauth.QuotaObservation
	pendingUntil time.Time
	lastWritten  []byte
	stopOnce     sync.Once
	cancel       context.CancelFunc
	done         chan struct{}
}

func newRoutingStatePersister(path string, manager *coreauth.Manager, history *quotahistory.Store, cost *apicost.Store) *routingStatePersister {
	return &routingStatePersister{path: path, manager: manager, history: history, cost: cost, now: time.Now}
}

// resolveRoutingStatePath expands "~" and resolves relative paths against
// the config file directory.
func resolveRoutingStatePath(raw, configPath string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", nil
	}
	if strings.HasPrefix(path, "~") {
		home, errHome := os.UserHomeDir()
		if errHome != nil {
			return "", fmt.Errorf("resolve routing state file: %w", errHome)
		}
		path = filepath.Join(home, strings.TrimLeft(strings.TrimPrefix(path, "~"), "/\\"))
	}
	if !filepath.IsAbs(path) && strings.TrimSpace(configPath) != "" {
		path = filepath.Join(filepath.Dir(configPath), path)
	}
	return filepath.Clean(path), nil
}

// load reads the state file and restores history, quota snapshots of
// registered credentials, and session bindings. A missing file is not an
// error.
func (p *routingStatePersister) load() error {
	if p == nil || p.path == "" {
		return nil
	}
	data, errRead := os.ReadFile(p.path)
	if errRead != nil {
		if errors.Is(errRead, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read routing state: %w", errRead)
	}
	var state routingStateFile
	if errUnmarshal := json.Unmarshal(data, &state); errUnmarshal != nil {
		return fmt.Errorf("parse routing state: %w", errUnmarshal)
	}
	if state.Version != routingStateVersion {
		return fmt.Errorf("unsupported routing state version %d", state.Version)
	}
	now := p.now()
	if p.history != nil {
		p.history.Import(state.History)
		p.history.Prune(now, nil)
	}
	if p.cost != nil {
		p.cost.Import(state.APICost)
		p.cost.Prune(now)
	}
	p.mu.Lock()
	p.pending = state.Quota
	p.pendingUntil = now.Add(routingStatePendingTTL)
	p.mu.Unlock()
	restoredQuota := p.applyPendingQuota(now)
	restoredBindings := 0
	if selector, ok := p.manager.Selector().(sessionBindingSnapshotter); ok {
		restoredBindings = selector.RestoreSessionBindings(state.SessionBindings, now)
	}
	log.Infof("routing state restored from %s: %d quota snapshots, %d session bindings, %d history series, %d api cost buckets",
		p.path, restoredQuota, restoredBindings, len(state.History), len(state.APICost))
	return nil
}

// applyPendingQuota restores waiting quota snapshots into registered
// credentials and returns how many were applied.
func (p *routingStatePersister) applyPendingQuota(now time.Time) int {
	if p == nil || p.manager == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pending) == 0 {
		return 0
	}
	// Entries stay pending for the whole TTL: config-backed credentials are
	// re-synthesized by the watcher after startup, which drops their passive
	// quota, so the snapshot is re-applied while it is still the newest one.
	applied := 0
	for id, observation := range p.pending {
		if ok, _ := p.manager.RestoreQuotaObservation(id, observation); ok {
			applied++
		}
	}
	if !now.Before(p.pendingUntil) {
		p.pending = nil
	}
	return applied
}

// sample records the current quota windows of every credential into the
// history store and prunes expired points, removed credentials, and expired
// cost buckets. Cost buckets of removed credentials are kept.
func (p *routingStatePersister) sample(now time.Time) {
	if p == nil {
		return
	}
	if p.cost != nil {
		p.cost.Prune(now)
	}
	if p.manager == nil || p.history == nil {
		return
	}
	auths := p.manager.List()
	known := make(map[string]struct{}, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		known[auth.ID] = struct{}{}
		windows := historyWindows(coreauth.QuotaWindowsForAuth(auth))
		if len(windows) == 0 {
			continue
		}
		p.history.Record(auth.ID, auth.Quota.ObservedAt, windows)
	}
	p.mu.Lock()
	for id := range p.pending {
		known[id] = struct{}{}
	}
	p.mu.Unlock()
	p.history.Prune(now, known)
}

func historyWindows(windows []coreauth.QuotaWindow) []quotahistory.Window {
	out := make([]quotahistory.Window, 0, len(windows))
	for _, window := range windows {
		if window.UsedPercent == nil && window.ResetAt.IsZero() {
			continue
		}
		entry := quotahistory.Window{Name: window.Name, WindowMinutes: int(window.Duration / time.Minute)}
		if window.UsedPercent != nil {
			used := *window.UsedPercent
			entry.UsedPercent = &used
		}
		if !window.ResetAt.IsZero() {
			resetAt := window.ResetAt.UTC()
			entry.ResetAt = &resetAt
		}
		out = append(out, entry)
	}
	return out
}

// save writes the state file atomically when its content changed.
func (p *routingStatePersister) save(now time.Time) error {
	if p == nil || p.path == "" {
		return nil
	}
	state := routingStateFile{Version: routingStateVersion}
	if p.manager != nil {
		state.Quota = p.manager.QuotaObservations()
		if selector, ok := p.manager.Selector().(sessionBindingSnapshotter); ok {
			state.SessionBindings = selector.SnapshotSessionBindings(now)
		}
	}
	p.mu.Lock()
	for id, observation := range p.pending {
		if state.Quota == nil {
			state.Quota = make(map[string]coreauth.QuotaObservation)
		}
		if _, live := state.Quota[id]; !live {
			state.Quota[id] = observation
		}
	}
	p.mu.Unlock()
	if p.history != nil {
		state.History = p.history.Export()
	}
	if p.cost != nil {
		state.APICost = p.cost.Export()
	}
	body, errMarshal := json.Marshal(state)
	if errMarshal != nil {
		return fmt.Errorf("marshal routing state: %w", errMarshal)
	}
	p.mu.Lock()
	unchanged := bytes.Equal(body, p.lastWritten)
	p.mu.Unlock()
	if unchanged {
		return nil
	}
	// SavedAt is excluded from change detection so idle ticks do not rewrite.
	state.SavedAt = now.UTC()
	data, errMarshal := json.Marshal(state)
	if errMarshal != nil {
		return fmt.Errorf("marshal routing state: %w", errMarshal)
	}
	if errWrite := writeFileAtomic(p.path, append(data, '\n')); errWrite != nil {
		return errWrite
	}
	p.mu.Lock()
	p.lastWritten = body
	p.mu.Unlock()
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return fmt.Errorf("create routing state directory: %w", errMkdir)
	}
	tmpFile, errCreate := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if errCreate != nil {
		return fmt.Errorf("create routing state temp file: %w", errCreate)
	}
	tmp := tmpFile.Name()
	if _, errWrite := tmpFile.Write(data); errWrite != nil {
		if errClose := tmpFile.Close(); errClose != nil {
			log.Errorf("close routing state temp file: %v", errClose)
		}
		_ = os.Remove(tmp)
		return fmt.Errorf("write routing state temp file: %w", errWrite)
	}
	if errClose := tmpFile.Close(); errClose != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close routing state temp file: %w", errClose)
	}
	if errRename := os.Rename(tmp, path); errRename != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace routing state file: %w", errRename)
	}
	return nil
}

// tick runs one sampling and persistence round.
func (p *routingStatePersister) tick() {
	now := p.now()
	p.applyPendingQuota(now)
	p.sample(now)
	if errSave := p.save(now); errSave != nil {
		log.Warnf("failed to save routing state: %v", errSave)
	}
}

// start launches the periodic sampling loop.
func (p *routingStatePersister) start(ctx context.Context) {
	if p == nil {
		return
	}
	loopCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.done = make(chan struct{})
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(routingStateInterval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				p.tick()
			}
		}
	}()
}

// stop ends the loop and performs a final sampling and save.
func (p *routingStatePersister) stop() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() {
		if p.cancel != nil {
			p.cancel()
			<-p.done
		}
		p.tick()
	})
}

// carrySessionBindings moves live session bindings into a replacement
// selector so a routing config change does not drop established sessions.
func carrySessionBindings(previous, next coreauth.Selector, now time.Time) {
	from, okFrom := previous.(sessionBindingSnapshotter)
	to, okTo := next.(sessionBindingSnapshotter)
	if !okFrom || !okTo || from == to {
		return
	}
	to.RestoreSessionBindings(from.SnapshotSessionBindings(now), now)
}

// startRoutingState restores persisted routing state and starts sampling.
// Called once after credentials are loaded.
func (s *Service) startRoutingState(ctx context.Context) {
	if s == nil || s.coreManager == nil {
		return
	}
	cfg := s.currentConfig()
	raw := ""
	if cfg != nil {
		raw = cfg.Routing.StateFile
	}
	path, errResolve := resolveRoutingStatePath(raw, s.configPath)
	if errResolve != nil {
		log.Warnf("routing state persistence disabled: %v", errResolve)
		path = ""
	}
	persister := newRoutingStatePersister(path, s.coreManager, quotahistory.Default(), apicost.Default())
	if errLoad := persister.load(); errLoad != nil {
		log.Warnf("failed to restore routing state: %v", errLoad)
	}
	persister.start(ctx)
	s.routingState = persister
}

// stopRoutingState flushes routing state on shutdown.
func (s *Service) stopRoutingState() {
	if s == nil || s.routingState == nil {
		return
	}
	s.routingState.stop()
}
