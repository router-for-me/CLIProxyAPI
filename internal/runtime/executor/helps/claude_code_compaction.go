package helps

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxClaudeCodeCompactionLanes           = 2048
	claudeCodeCompactionStateVersion       = 1
	claudeCodeCompactionStateFilePrefix    = "claude-code-compaction-v1-"
	claudeCodeCompactionStateFileSuffix    = ".json"
	defaultClaudeCodeCompactionStateTTL    = 7 * 24 * time.Hour
	claudeCodeCompactionSweepInterval      = time.Hour
	claudeCodeCompactionStateFileMode      = 0o600
	claudeCodeCompactionStateDirectoryMode = 0o700
	maxClaudeCodeCompactionStateHashes     = 4096
)

// ClaudeCodeCompactionLaneKey isolates compacted history by the Claude Code
// conversation, resolved Codex model, and concrete upstream credential. Opaque
// compaction items cannot safely cross any of those boundaries.
type ClaudeCodeCompactionLaneKey struct {
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	AuthID    string `json:"auth_id"`
}

// NewClaudeCodeCompactionLaneKey returns a normalized key when the request can
// be associated with a Claude Code conversation.
func NewClaudeCodeCompactionLaneKey(sessionID, model, authID string) (ClaudeCodeCompactionLaneKey, bool) {
	key := ClaudeCodeCompactionLaneKey{
		SessionID: strings.TrimSpace(sessionID),
		Model:     strings.TrimSpace(model),
		AuthID:    strings.TrimSpace(authID),
	}
	if key.SessionID == "" || key.Model == "" || key.AuthID == "" {
		return ClaudeCodeCompactionLaneKey{}, false
	}
	return key, true
}

// ClaudeCodeCompactionState is the durable portion of one proxy-side
// compaction lane. JSON items are stored exactly as sent upstream so subsequent
// requests append to the same cache root byte-for-byte.
type ClaudeCodeCompactionState struct {
	SourcePrefixHashes          []string `json:"source_prefix_hashes,omitempty"`
	ReplacementItems            [][]byte `json:"replacement_items,omitempty"`
	EnvelopeHash                string   `json:"envelope_hash,omitempty"`
	ClientInputTokens           int64    `json:"client_input_tokens,omitempty"`
	UpstreamInputTokens         int64    `json:"upstream_input_tokens,omitempty"`
	PendingContextTokens        int64    `json:"pending_context_tokens,omitempty"`
	CompactionTokens            int64    `json:"compaction_tokens,omitempty"`
	AbsorbedReplayItemHashes    []string `json:"absorbed_replay_item_hashes,omitempty"`
	RejectedEncryptedItemHashes []string `json:"rejected_encrypted_item_hashes,omitempty"`
	LegacyOnly                  bool     `json:"legacy_only,omitempty"`
	Revision                    uint64   `json:"revision"`
}

type persistedClaudeCodeCompactionPayload struct {
	Version   int                         `json:"version"`
	Key       ClaudeCodeCompactionLaneKey `json:"key"`
	UpdatedAt int64                       `json:"updated_at_unix_nano"`
	ExpiresAt int64                       `json:"expires_at_unix_nano"`
	State     ClaudeCodeCompactionState   `json:"state"`
}

type persistedClaudeCodeCompactionRecord struct {
	persistedClaudeCodeCompactionPayload
	Checksum string `json:"sha256"`
}

type claudeCodeCompactionEntry struct {
	mu           sync.Mutex
	state        ClaudeCodeCompactionState
	stateDir     string
	ttl          time.Duration
	loaded       bool
	loadErr      error
	lastWriteErr error
	pins         int
	lastUsed     atomic.Int64
}

// ClaudeCodeCompactionLane is a locked view of a lane. Call Unlock as soon as
// request rewriting (and any inline compaction request) has completed.
type ClaudeCodeCompactionLane struct {
	key                 ClaudeCodeCompactionLaneKey
	entry               *claudeCodeCompactionEntry
	lockErr             error
	unlockOnce          sync.Once
	observationRevision uint64
	observationActive   atomic.Bool
}

var claudeCodeCompactionLanes = struct {
	sync.Mutex
	entries map[ClaudeCodeCompactionLaneKey]*claudeCodeCompactionEntry
}{entries: make(map[ClaudeCodeCompactionLaneKey]*claudeCodeCompactionEntry)}

var claudeCodeCompactionSweeps = struct {
	sync.Mutex
	lastByDirectory map[string]time.Time
}{lastByDirectory: make(map[string]time.Time)}

// LockClaudeCodeCompactionLane serializes compaction decisions for a logical
// Claude Code/Codex lane. When stateDir is supplied, committed state is loaded
// from and atomically persisted beneath that directory. Expired lanes are
// opportunistically removed.
func LockClaudeCodeCompactionLane(key ClaudeCodeCompactionLaneKey, ttl time.Duration, stateDir ...string) *ClaudeCodeCompactionLane {
	if ttl <= 0 {
		ttl = defaultClaudeCodeCompactionStateTTL
	}
	dir, dirErr := normalizeClaudeCodeCompactionStateDir(stateDir)
	pruneAt := time.Now()
	if dirErr == nil && dir != "" {
		maybeSweepExpiredClaudeCodeCompactionStates(dir, pruneAt)
	}

	claudeCodeCompactionLanes.Lock()
	entry := claudeCodeCompactionLanes.entries[key]
	if entry != nil {
		// Pin while holding the map lock, before waiting on the per-entry lock.
		// This prevents pruning from deleting the mapped entry and creating a
		// second mutex for the same key while a caller is active or waiting.
		entry.pins++
	}
	if len(claudeCodeCompactionLanes.entries) >= maxClaudeCodeCompactionLanes {
		pruneClaudeCodeCompactionLanesLocked(pruneAt, ttl)
	}
	if entry == nil {
		entry = &claudeCodeCompactionEntry{stateDir: dir, ttl: ttl, pins: 1}
		entry.lastUsed.Store(pruneAt.UnixNano())
		claudeCodeCompactionLanes.entries[key] = entry
	}
	claudeCodeCompactionLanes.Unlock()

	entry.mu.Lock()
	now := time.Now()
	lane := &ClaudeCodeCompactionLane{key: key, entry: entry, lockErr: dirErr}
	if lane.lockErr == nil && entry.stateDir != dir {
		lane.lockErr = fmt.Errorf("claude code compaction lane state directory changed from %q to %q", entry.stateDir, dir)
	}
	if !entry.loaded {
		entry.loaded = true
		if lane.lockErr == nil && dir != "" {
			state, err := loadClaudeCodeCompactionState(dir, key, now)
			if err != nil {
				entry.loadErr = err
				entry.state = ClaudeCodeCompactionState{}
			} else {
				entry.state = state
				entry.loadErr = nil
			}
		}
	}
	entry.lastUsed.Store(now.UnixNano())
	return lane
}

func pruneClaudeCodeCompactionLanesLocked(now time.Time, ttl time.Duration) {
	for key, entry := range claudeCodeCompactionLanes.entries {
		entryTTL := entry.ttl
		if entryTTL <= 0 {
			entryTTL = ttl
		}
		if entry.pins == 0 && now.Sub(time.Unix(0, entry.lastUsed.Load())) > entryTTL {
			delete(claudeCodeCompactionLanes.entries, key)
		}
	}
	if len(claudeCodeCompactionLanes.entries) < maxClaudeCodeCompactionLanes {
		return
	}

	// A pathological number of idle sessions should remain bounded even when
	// none has reached the configured TTL. Remove the oldest unpinned quarter.
	// If every entry is active or waiting, temporarily exceeding the cap is
	// safer than splitting a logical lane across two mutexes.
	removeCount := maxClaudeCodeCompactionLanes / 4
	for removeCount > 0 {
		var oldestKey ClaudeCodeCompactionLaneKey
		var oldest *claudeCodeCompactionEntry
		for key, entry := range claudeCodeCompactionLanes.entries {
			if entry.pins != 0 {
				continue
			}
			if oldest == nil || entry.lastUsed.Load() < oldest.lastUsed.Load() {
				oldestKey = key
				oldest = entry
			}
		}
		if oldest == nil {
			break
		}
		delete(claudeCodeCompactionLanes.entries, oldestKey)
		removeCount--
	}
}

// State returns a defensive copy. The lane must still be locked.
func (l *ClaudeCodeCompactionLane) State() ClaudeCodeCompactionState {
	if l == nil || l.entry == nil {
		return ClaudeCodeCompactionState{}
	}
	return cloneClaudeCodeCompactionState(l.entry.state)
}

// PersistenceError returns a state-directory or load error that makes the
// lane unsafe to use. Write errors are returned by the operation that observed
// them and are retried on the next write, so a transient filesystem failure
// cannot permanently brick a live lane. Corrupt or expired state is never
// returned by State.
func (l *ClaudeCodeCompactionLane) PersistenceError() error {
	if l == nil || l.entry == nil {
		return nil
	}
	if l.lockErr != nil {
		return l.lockErr
	}
	return l.entry.loadErr
}

// Commit transactionally replaces the lane state. The lane must still be
// locked. A revision bump prevents an older in-flight response from overwriting
// newer token observations. Durable state is installed in memory only after an
// atomic persistence write succeeds.
func (l *ClaudeCodeCompactionLane) Commit(state ClaudeCodeCompactionState) (uint64, error) {
	if l == nil || l.entry == nil {
		return 0, errors.New("claude code compaction lane is nil")
	}
	if l.lockErr != nil {
		return 0, l.lockErr
	}
	if l.entry.loadErr != nil {
		return 0, l.entry.loadErr
	}
	state = normalizeClaudeCodeCompactionStateHashes(state)
	state.Revision = l.entry.state.Revision + 1
	next := cloneClaudeCodeCompactionState(state)
	now := time.Now()
	if err := persistClaudeCodeCompactionState(l.entry.stateDir, l.key, next, now, l.entry.ttl); err != nil {
		l.entry.lastWriteErr = err
		return 0, err
	}
	l.entry.state = next
	l.entry.lastWriteErr = nil
	l.entry.lastUsed.Store(now.UnixNano())
	return state.Revision, nil
}

// ReplaceStateIfRevision atomically replaces a lane after an upstream request
// rejects encrypted state. It is safe after Unlock and refuses to overwrite a
// newer request's state.
func (l *ClaudeCodeCompactionLane) ReplaceStateIfRevision(revision uint64, state ClaudeCodeCompactionState) (bool, error) {
	if l == nil || l.entry == nil {
		return false, errors.New("claude code compaction lane is nil")
	}
	if l.lockErr != nil {
		return false, l.lockErr
	}
	l.entry.mu.Lock()
	defer l.entry.mu.Unlock()
	if l.entry.loadErr != nil {
		return false, l.entry.loadErr
	}
	if revision == 0 || l.entry.state.Revision != revision {
		return false, nil
	}
	state = normalizeClaudeCodeCompactionStateHashes(state)
	state.Revision = revision + 1
	next := cloneClaudeCodeCompactionState(state)
	now := time.Now()
	if err := persistClaudeCodeCompactionState(l.entry.stateDir, l.key, next, now, l.entry.ttl); err != nil {
		l.entry.lastWriteErr = err
		return false, err
	}
	l.entry.state = next
	l.entry.lastWriteErr = nil
	l.entry.lastUsed.Store(now.UnixNano())
	return true, nil
}

// ReplaceStateIfCurrentMatches atomically retires rejected replacement state
// even when a concurrent observation has advanced only the revision. A newer
// compaction or source boundary is never overwritten.
func (l *ClaudeCodeCompactionLane) ReplaceStateIfCurrentMatches(expected, state ClaudeCodeCompactionState) (bool, error) {
	if l == nil || l.entry == nil {
		return false, errors.New("claude code compaction lane is nil")
	}
	if l.lockErr != nil {
		return false, l.lockErr
	}
	l.entry.mu.Lock()
	defer l.entry.mu.Unlock()
	if l.entry.loadErr != nil {
		return false, l.entry.loadErr
	}
	current := l.entry.state
	if !sameClaudeCodeCompactionReplacement(current, expected) {
		return false, nil
	}
	state.RejectedEncryptedItemHashes = mergeClaudeCodeCompactionHashes(
		current.RejectedEncryptedItemHashes,
		state.RejectedEncryptedItemHashes,
	)
	state = normalizeClaudeCodeCompactionStateHashes(state)
	state.Revision = current.Revision + 1
	next := cloneClaudeCodeCompactionState(state)
	now := time.Now()
	if err := persistClaudeCodeCompactionState(l.entry.stateDir, l.key, next, now, l.entry.ttl); err != nil {
		l.entry.lastWriteErr = err
		return false, err
	}
	l.entry.state = next
	l.entry.lastWriteErr = nil
	l.entry.lastUsed.Store(now.UnixNano())
	return true, nil
}

func sameClaudeCodeCompactionReplacement(left, right ClaudeCodeCompactionState) bool {
	if left.EnvelopeHash != right.EnvelopeHash || len(left.SourcePrefixHashes) != len(right.SourcePrefixHashes) || len(left.ReplacementItems) != len(right.ReplacementItems) {
		return false
	}
	for i := range left.SourcePrefixHashes {
		if left.SourcePrefixHashes[i] != right.SourcePrefixHashes[i] {
			return false
		}
	}
	for i := range left.ReplacementItems {
		if !bytes.Equal(left.ReplacementItems[i], right.ReplacementItems[i]) {
			return false
		}
	}
	return true
}

func mergeClaudeCodeCompactionHashes(left, right []string) []string {
	merged := make([]string, 0, len(left)+len(right))
	seen := make(map[string]struct{}, len(left)+len(right))
	for _, group := range [][]string{left, right} {
		for _, value := range group {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, duplicate := seen[value]; duplicate {
				continue
			}
			seen[value] = struct{}{}
			merged = append(merged, value)
		}
	}
	return boundClaudeCodeCompactionHashes(merged)
}

func normalizeClaudeCodeCompactionStateHashes(state ClaudeCodeCompactionState) ClaudeCodeCompactionState {
	state.AbsorbedReplayItemHashes = boundClaudeCodeCompactionHashes(state.AbsorbedReplayItemHashes)
	state.RejectedEncryptedItemHashes = boundClaudeCodeCompactionHashes(state.RejectedEncryptedItemHashes)
	return state
}

func boundClaudeCodeCompactionHashes(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	// Keep the newest unique hashes. They are the ones most likely to be
	// referenced by the current replay cache or recent client history.
	reversed := make([]string, 0, min(len(values), maxClaudeCodeCompactionStateHashes))
	seen := make(map[string]struct{}, cap(reversed))
	for i := len(values) - 1; i >= 0 && len(reversed) < maxClaudeCodeCompactionStateHashes; i-- {
		value := strings.TrimSpace(values[i])
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		reversed = append(reversed, value)
	}
	bounded := make([]string, len(reversed))
	for i := range reversed {
		bounded[len(reversed)-1-i] = reversed[i]
	}
	return bounded
}

// Clear invalidates a lane after an exact-prefix or request-envelope mismatch.
// The lane must still be locked.
func (l *ClaudeCodeCompactionLane) Clear() error {
	if l == nil || l.entry == nil {
		return errors.New("claude code compaction lane is nil")
	}
	if l.lockErr != nil {
		return l.lockErr
	}
	if l.entry.loadErr != nil {
		return l.entry.loadErr
	}
	revision := l.entry.state.Revision + 1
	next := ClaudeCodeCompactionState{Revision: revision}
	now := time.Now()
	if err := persistClaudeCodeCompactionState(l.entry.stateDir, l.key, next, now, l.entry.ttl); err != nil {
		l.entry.lastWriteErr = err
		return err
	}
	l.entry.state = next
	l.entry.lastWriteErr = nil
	l.entry.lastUsed.Store(now.UnixNano())
	return nil
}

// BeginObservation reserves a revision for the generation request about to be
// sent. Only its terminal response may install the corresponding token usage.
// The lane must still be locked.
func (l *ClaudeCodeCompactionLane) BeginObservation() uint64 {
	if l == nil || l.entry == nil {
		return 0
	}
	if l.observationRevision != 0 {
		return l.observationRevision
	}
	claudeCodeCompactionLanes.Lock()
	l.entry.pins++
	claudeCodeCompactionLanes.Unlock()
	l.entry.state.Revision++
	l.entry.lastUsed.Store(time.Now().UnixNano())
	l.observationRevision = l.entry.state.Revision
	l.observationActive.Store(true)
	return l.observationRevision
}

// ObserveTerminal records exact upstream input usage and pending reasoning
// context only when no newer request has advanced the lane. It is safe to call
// after Unlock.
func (l *ClaudeCodeCompactionLane) ObserveTerminal(revision uint64, clientInputTokens, upstreamInputTokens, pendingContextTokens int64) error {
	if l == nil || l.entry == nil {
		return nil
	}
	if !l.observationActive.CompareAndSwap(true, false) {
		return nil
	}
	defer l.releaseObservationPin()
	if revision == 0 || upstreamInputTokens <= 0 {
		return nil
	}
	l.entry.mu.Lock()
	defer l.entry.mu.Unlock()
	if l.entry.state.Revision != revision {
		return nil
	}
	if l.entry.loadErr != nil {
		return l.entry.loadErr
	}
	next := cloneClaudeCodeCompactionState(l.entry.state)
	next.ClientInputTokens = clientInputTokens
	next.UpstreamInputTokens = upstreamInputTokens
	next.PendingContextTokens = pendingContextTokens
	now := time.Now()
	if err := persistClaudeCodeCompactionState(l.entry.stateDir, l.key, next, now, l.entry.ttl); err != nil {
		l.entry.lastWriteErr = err
		return err
	}
	l.entry.state = next
	l.entry.lastWriteErr = nil
	l.entry.lastUsed.Store(now.UnixNano())
	return nil
}

// AbandonObservation releases the in-flight observation pin without changing
// token state. Call it when a generation fails or ends without a terminal
// usage event. It is safe to call after ObserveTerminal and is idempotent.
func (l *ClaudeCodeCompactionLane) AbandonObservation() {
	if l == nil || l.entry == nil || l.observationRevision == 0 {
		return
	}
	if l.observationActive.CompareAndSwap(true, false) {
		l.releaseObservationPin()
	}
}

func (l *ClaudeCodeCompactionLane) releaseObservationPin() {
	claudeCodeCompactionLanes.Lock()
	if l.entry.pins > 0 {
		l.entry.pins--
	}
	claudeCodeCompactionLanes.Unlock()
}

// Unlock releases the lane. It is idempotent so deferred cleanup can coexist
// with early returns.
func (l *ClaudeCodeCompactionLane) Unlock() {
	if l == nil || l.entry == nil {
		return
	}
	l.unlockOnce.Do(func() {
		l.entry.mu.Unlock()
		claudeCodeCompactionLanes.Lock()
		if l.entry.pins > 0 {
			l.entry.pins--
		}
		claudeCodeCompactionLanes.Unlock()
	})
}

func normalizeClaudeCodeCompactionStateDir(stateDirs []string) (string, error) {
	if len(stateDirs) > 1 {
		return "", errors.New("only one claude code compaction state directory may be supplied")
	}
	if len(stateDirs) == 0 || strings.TrimSpace(stateDirs[0]) == "" {
		return "", nil
	}
	dir, err := filepath.Abs(strings.TrimSpace(stateDirs[0]))
	if err != nil {
		return "", fmt.Errorf("resolve claude code compaction state directory: %w", err)
	}
	return filepath.Clean(dir), nil
}

func claudeCodeCompactionStatePath(stateDir string, key ClaudeCodeCompactionLaneKey) string {
	keyBytes, _ := json.Marshal(key)
	digest := sha256.Sum256(keyBytes)
	return filepath.Join(stateDir, claudeCodeCompactionStateFilePrefix+hex.EncodeToString(digest[:])+claudeCodeCompactionStateFileSuffix)
}

func maybeSweepExpiredClaudeCodeCompactionStates(stateDir string, now time.Time) {
	claudeCodeCompactionSweeps.Lock()
	last := claudeCodeCompactionSweeps.lastByDirectory[stateDir]
	elapsed := now.Sub(last)
	if !last.IsZero() && elapsed >= 0 && elapsed < claudeCodeCompactionSweepInterval {
		claudeCodeCompactionSweeps.Unlock()
		return
	}
	// Reserve the sweep while holding the lock so concurrent requests do not
	// all scan the same directory. Cleanup is deliberately best effort: a
	// transient enumeration or removal failure must not block inference.
	claudeCodeCompactionSweeps.lastByDirectory[stateDir] = now
	claudeCodeCompactionSweeps.Unlock()

	if ensureClaudeCodeCompactionStateDir(stateDir) != nil {
		return
	}
	_ = sweepExpiredClaudeCodeCompactionStates(stateDir, now)
}

func sweepExpiredClaudeCodeCompactionStates(stateDir string, now time.Time) error {
	entries, err := os.ReadDir(stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scan claude code compaction state directory %q: %w", stateDir, err)
	}

	var firstErr error
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !isClaudeCodeCompactionStateFileName(entry.Name()) {
			continue
		}
		path := filepath.Join(stateDir, entry.Name())
		if secureClaudeCodeCompactionStateFile(path) != nil {
			continue
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var record persistedClaudeCodeCompactionRecord
		if json.Unmarshal(data, &record) != nil || validateClaudeCodeCompactionStateRecord(record, path) != nil {
			// Never delete a file we cannot authenticate as one of our records.
			continue
		}
		if filepath.Base(claudeCodeCompactionStatePath(stateDir, record.Key)) != entry.Name() {
			continue
		}
		if record.ExpiresAt <= 0 || now.Before(time.Unix(0, record.ExpiresAt)) {
			continue
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && firstErr == nil {
			firstErr = fmt.Errorf("remove expired claude code compaction state %q: %w", path, removeErr)
		}
	}
	return firstErr
}

func isClaudeCodeCompactionStateFileName(name string) bool {
	if !strings.HasPrefix(name, claudeCodeCompactionStateFilePrefix) || !strings.HasSuffix(name, claudeCodeCompactionStateFileSuffix) {
		return false
	}
	digest := strings.TrimSuffix(strings.TrimPrefix(name, claudeCodeCompactionStateFilePrefix), claudeCodeCompactionStateFileSuffix)
	if len(digest) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func validateClaudeCodeCompactionStateRecord(record persistedClaudeCodeCompactionRecord, path string) error {
	if record.Version != claudeCodeCompactionStateVersion {
		return fmt.Errorf("unsupported claude code compaction state version %d in %q", record.Version, path)
	}
	if len(record.State.AbsorbedReplayItemHashes) > maxClaudeCodeCompactionStateHashes || len(record.State.RejectedEncryptedItemHashes) > maxClaudeCodeCompactionStateHashes {
		return fmt.Errorf("claude code compaction state hash limit exceeded in %q", path)
	}
	payload, err := json.Marshal(record.persistedClaudeCodeCompactionPayload)
	if err != nil {
		return fmt.Errorf("encode claude code compaction checksum payload %q: %w", path, err)
	}
	digest := sha256.Sum256(payload)
	wantChecksum := hex.EncodeToString(digest[:])
	if record.Checksum == "" || !strings.EqualFold(record.Checksum, wantChecksum) {
		return fmt.Errorf("claude code compaction state checksum mismatch in %q", path)
	}
	return nil
}

func loadClaudeCodeCompactionState(stateDir string, key ClaudeCodeCompactionLaneKey, now time.Time) (ClaudeCodeCompactionState, error) {
	if err := ensureClaudeCodeCompactionStateDir(stateDir); err != nil {
		return ClaudeCodeCompactionState{}, err
	}
	path := claudeCodeCompactionStatePath(stateDir, key)
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ClaudeCodeCompactionState{}, nil
	}
	if err != nil {
		return ClaudeCodeCompactionState{}, fmt.Errorf("stat claude code compaction state %q: %w", path, err)
	}
	if err := secureClaudeCodeCompactionStateFile(path); err != nil {
		return ClaudeCodeCompactionState{}, fmt.Errorf("secure claude code compaction state %q: %w", path, err)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ClaudeCodeCompactionState{}, nil
	}
	if err != nil {
		return ClaudeCodeCompactionState{}, fmt.Errorf("read claude code compaction state %q: %w", path, err)
	}
	var record persistedClaudeCodeCompactionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return ClaudeCodeCompactionState{}, fmt.Errorf("decode claude code compaction state %q: %w", path, err)
	}
	if err := validateClaudeCodeCompactionStateRecord(record, path); err != nil {
		return ClaudeCodeCompactionState{}, err
	}
	if record.Key != key {
		return ClaudeCodeCompactionState{}, fmt.Errorf("claude code compaction state key mismatch in %q", path)
	}
	if record.ExpiresAt <= 0 || !now.Before(time.Unix(0, record.ExpiresAt)) {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return ClaudeCodeCompactionState{}, fmt.Errorf("remove expired claude code compaction state %q: %w", path, err)
		}
		return ClaudeCodeCompactionState{}, nil
	}
	return cloneClaudeCodeCompactionState(record.State), nil
}

func persistClaudeCodeCompactionState(stateDir string, key ClaudeCodeCompactionLaneKey, state ClaudeCodeCompactionState, now time.Time, ttl time.Duration) error {
	if stateDir == "" {
		return nil
	}
	if ttl <= 0 {
		ttl = defaultClaudeCodeCompactionStateTTL
	}
	if err := ensureClaudeCodeCompactionStateDir(stateDir); err != nil {
		return err
	}
	state = normalizeClaudeCodeCompactionStateHashes(state)
	payload := persistedClaudeCodeCompactionPayload{
		Version:   claudeCodeCompactionStateVersion,
		Key:       key,
		UpdatedAt: now.UnixNano(),
		ExpiresAt: now.Add(ttl).UnixNano(),
		State:     cloneClaudeCodeCompactionState(state),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode claude code compaction state: %w", err)
	}
	digest := sha256.Sum256(payloadBytes)
	record := persistedClaudeCodeCompactionRecord{
		persistedClaudeCodeCompactionPayload: payload,
		Checksum:                             hex.EncodeToString(digest[:]),
	}
	recordBytes, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode checksummed claude code compaction state: %w", err)
	}

	path := claudeCodeCompactionStatePath(stateDir, key)
	temp, err := os.CreateTemp(stateDir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("create temporary claude code compaction state: %w", err)
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if err := secureClaudeCodeCompactionStateFile(tempPath); err != nil {
		cleanup()
		return fmt.Errorf("secure temporary claude code compaction state %q: %w", tempPath, err)
	}
	if _, err := temp.Write(recordBytes); err != nil {
		cleanup()
		return fmt.Errorf("write temporary claude code compaction state %q: %w", tempPath, err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync temporary claude code compaction state %q: %w", tempPath, err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close temporary claude code compaction state %q: %w", tempPath, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("atomically replace claude code compaction state %q: %w", path, err)
	}
	if err := secureClaudeCodeCompactionStateFile(path); err != nil {
		removeErr := os.Remove(path)
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("secure persisted claude code compaction state %q: %v (remove unsecured state: %w)", path, err, removeErr)
		}
		return fmt.Errorf("secure persisted claude code compaction state %q: %w", path, err)
	}
	return nil
}

func ensureClaudeCodeCompactionStateDir(stateDir string) error {
	if err := os.MkdirAll(stateDir, claudeCodeCompactionStateDirectoryMode); err != nil {
		return fmt.Errorf("create claude code compaction state directory %q: %w", stateDir, err)
	}
	if err := secureClaudeCodeCompactionStateDirectory(stateDir); err != nil {
		return fmt.Errorf("secure claude code compaction state directory %q: %w", stateDir, err)
	}
	return nil
}

func cloneClaudeCodeCompactionState(state ClaudeCodeCompactionState) ClaudeCodeCompactionState {
	cloned := state
	if state.SourcePrefixHashes != nil {
		cloned.SourcePrefixHashes = append([]string{}, state.SourcePrefixHashes...)
	}
	if state.ReplacementItems != nil {
		cloned.ReplacementItems = make([][]byte, len(state.ReplacementItems))
		for i := range state.ReplacementItems {
			if state.ReplacementItems[i] != nil {
				cloned.ReplacementItems[i] = append([]byte{}, state.ReplacementItems[i]...)
			}
		}
	}
	if state.AbsorbedReplayItemHashes != nil {
		cloned.AbsorbedReplayItemHashes = append([]string{}, state.AbsorbedReplayItemHashes...)
	}
	if state.RejectedEncryptedItemHashes != nil {
		cloned.RejectedEncryptedItemHashes = append([]string{}, state.RejectedEncryptedItemHashes...)
	}
	return cloned
}

func resetClaudeCodeCompactionLanesForTest() {
	claudeCodeCompactionLanes.Lock()
	claudeCodeCompactionLanes.entries = make(map[ClaudeCodeCompactionLaneKey]*claudeCodeCompactionEntry)
	claudeCodeCompactionLanes.Unlock()

	claudeCodeCompactionSweeps.Lock()
	claudeCodeCompactionSweeps.lastByDirectory = make(map[string]time.Time)
	claudeCodeCompactionSweeps.Unlock()
}
