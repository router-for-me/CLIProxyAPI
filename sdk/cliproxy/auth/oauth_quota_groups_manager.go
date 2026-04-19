package auth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

// oauthQuotaGroupPersistMu serializes config.yaml writes triggered by quota-
// group state changes so concurrent goroutines never race on the same file.
// The actual write is still atomic (temp+rename) in SaveConfigPreserveComments,
// but this lock ensures a deterministic ordering and avoids unnecessary
// re-entrant load/merge work piling up.
var oauthQuotaGroupPersistMu sync.Mutex

// defaultOAuthQuotaGroupCleanupInterval is the cadence at which expired auto
// suspensions are reaped in the background. Chosen to be short enough that
// users rarely observe a stale cooldown banner yet large enough that the
// cleanup never dominates CPU or I/O.
const defaultOAuthQuotaGroupCleanupInterval = 30 * time.Second

func (m *Manager) configFilePathValue() string {
	if m == nil {
		return ""
	}
	if raw, ok := m.configFilePath.Load().(string); ok {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			return trimmed
		}
	}
	wd, err := os.Getwd()
	if err != nil || strings.TrimSpace(wd) == "" {
		return ""
	}
	return filepath.Join(wd, "config.yaml")
}

func (m *Manager) persistRuntimeConfigSnapshot(cfg *internalconfig.Config) error {
	if m == nil || cfg == nil {
		return nil
	}
	path := m.configFilePathValue()
	if path == "" {
		return nil
	}
	oauthQuotaGroupPersistMu.Lock()
	defer oauthQuotaGroupPersistMu.Unlock()
	return internalconfig.SaveConfigPreserveComments(path, cfg)
}

// cloneRuntimeConfigForQuotaGroups returns a shallow copy of the currently
// published runtime config that is safe to mutate without affecting readers
// that already hold the previous pointer. Only the slice headers that the
// quota-group code actually writes to are re-aliased; every other field
// aliases the previous snapshot, which is acceptable because the publisher
// replaces the whole struct via SetConfig once mutation completes.
func (m *Manager) cloneRuntimeConfigForQuotaGroups() *internalconfig.Config {
	var prev *internalconfig.Config
	if m != nil {
		prev, _ = m.runtimeConfig.Load().(*internalconfig.Config)
	}
	if prev == nil {
		return &internalconfig.Config{}
	}
	next := *prev
	if len(prev.OAuthAccountQuotaGroupState) > 0 {
		next.OAuthAccountQuotaGroupState = append(
			make([]internalconfig.OAuthAccountQuotaGroupState, 0, len(prev.OAuthAccountQuotaGroupState)),
			prev.OAuthAccountQuotaGroupState...,
		)
	} else {
		next.OAuthAccountQuotaGroupState = nil
	}
	return &next
}

func findOAuthQuotaGroupState(entries []internalconfig.OAuthAccountQuotaGroupState, authID, groupID string) (internalconfig.OAuthAccountQuotaGroupState, bool) {
	authID = strings.TrimSpace(authID)
	groupID = strings.ToLower(strings.TrimSpace(groupID))
	for _, entry := range entries {
		if strings.TrimSpace(entry.AuthID) == authID && strings.EqualFold(strings.TrimSpace(entry.GroupID), groupID) {
			return entry, true
		}
	}
	return internalconfig.OAuthAccountQuotaGroupState{}, false
}

func (m *Manager) setOAuthQuotaGroupAutoStateLocked(auth *Auth, model, provider string, until time.Time, resetTimeSource string, now time.Time) (*internalconfig.Config, bool) {
	if m == nil || auth == nil || until.IsZero() {
		return nil, false
	}
	group, ok := resolveOAuthQuotaGroup(auth, model)
	if !ok {
		return nil, false
	}
	cfg := m.cloneRuntimeConfigForQuotaGroups()

	current, _ := findOAuthQuotaGroupState(cfg.OAuthAccountQuotaGroupState, auth.ID, group.ID)
	next := current
	next.AuthID = auth.ID
	next.GroupID = group.ID
	next.AutoSuspendedUntil = until.UTC()
	next.AutoReason = "quota_exhausted"
	next.SourceModel = strings.TrimSpace(model)
	next.SourceProvider = strings.ToLower(strings.TrimSpace(provider))
	next.ResetTimeSource = strings.TrimSpace(resetTimeSource)
	next.UpdatedAt = now.UTC()
	next.UpdatedBy = "system:auto"

	if current.ManualSuspended == next.ManualSuspended &&
		current.ManualReason == next.ManualReason &&
		current.AutoSuspendedUntil.Equal(next.AutoSuspendedUntil) &&
		current.AutoReason == next.AutoReason &&
		current.SourceModel == next.SourceModel &&
		current.SourceProvider == next.SourceProvider &&
		current.ResetTimeSource == next.ResetTimeSource {
		return nil, false
	}

	cfg.OAuthAccountQuotaGroupState = internalconfig.UpsertOAuthAccountQuotaGroupState(cfg.OAuthAccountQuotaGroupState, next)
	return cfg, true
}

func (m *Manager) clearOAuthQuotaGroupAutoStateLocked(auth *Auth, model string, now time.Time) (*internalconfig.Config, bool) {
	if m == nil || auth == nil {
		return nil, false
	}
	group, ok := resolveOAuthQuotaGroup(auth, model)
	if !ok {
		return nil, false
	}
	cfg := m.cloneRuntimeConfigForQuotaGroups()
	current, ok := findOAuthQuotaGroupState(cfg.OAuthAccountQuotaGroupState, auth.ID, group.ID)
	if !ok || current.AutoSuspendedUntil.IsZero() {
		return nil, false
	}

	current.AutoSuspendedUntil = time.Time{}
	current.AutoReason = ""
	current.SourceModel = ""
	current.SourceProvider = ""
	current.ResetTimeSource = ""
	current.UpdatedAt = now.UTC()
	current.UpdatedBy = "system:auto-clear"

	if current.ManualSuspended {
		cfg.OAuthAccountQuotaGroupState = internalconfig.UpsertOAuthAccountQuotaGroupState(cfg.OAuthAccountQuotaGroupState, current)
	} else {
		cfg.OAuthAccountQuotaGroupState = internalconfig.RemoveOAuthAccountQuotaGroupState(cfg.OAuthAccountQuotaGroupState, auth.ID, group.ID)
	}
	return cfg, true
}

func (m *Manager) OAuthQuotaGroupStateSnapshot() []internalconfig.OAuthAccountQuotaGroupState {
	if m == nil {
		return nil
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		return nil
	}
	return internalconfig.NormalizeOAuthAccountQuotaGroupState(cfg.OAuthAccountQuotaGroupState)
}

func (m *Manager) SetOAuthQuotaGroupManualState(authID, groupID string, manualSuspended bool, reason, updatedBy string, now time.Time) *internalconfig.Config {
	if m == nil {
		return nil
	}
	authID = strings.TrimSpace(authID)
	groupID = strings.ToLower(strings.TrimSpace(groupID))
	if authID == "" || groupID == "" {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()

	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	current, _ := findOAuthQuotaGroupState(cfg.OAuthAccountQuotaGroupState, authID, groupID)
	current.AuthID = authID
	current.GroupID = groupID
	current.ManualSuspended = manualSuspended
	current.ManualReason = strings.TrimSpace(reason)
	current.UpdatedAt = now
	current.UpdatedBy = strings.TrimSpace(updatedBy)
	if current.UpdatedBy == "" {
		current.UpdatedBy = "management:manual"
	}
	if !current.ManualSuspended {
		current.ManualReason = ""
	}
	if !current.ManualSuspended && current.AutoSuspendedUntil.IsZero() {
		cfg.OAuthAccountQuotaGroupState = internalconfig.RemoveOAuthAccountQuotaGroupState(cfg.OAuthAccountQuotaGroupState, authID, groupID)
	} else {
		cfg.OAuthAccountQuotaGroupState = internalconfig.UpsertOAuthAccountQuotaGroupState(cfg.OAuthAccountQuotaGroupState, current)
	}
	return cfg
}

func (m *Manager) ClearOAuthQuotaGroupAutoState(authID, groupID, updatedBy string, now time.Time) *internalconfig.Config {
	if m == nil {
		return nil
	}
	authID = strings.TrimSpace(authID)
	groupID = strings.ToLower(strings.TrimSpace(groupID))
	if authID == "" || groupID == "" {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()

	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	current, ok := findOAuthQuotaGroupState(cfg.OAuthAccountQuotaGroupState, authID, groupID)
	if !ok {
		return cfg
	}
	current.AutoSuspendedUntil = time.Time{}
	current.AutoReason = ""
	current.SourceModel = ""
	current.SourceProvider = ""
	current.ResetTimeSource = ""
	current.UpdatedAt = now
	current.UpdatedBy = strings.TrimSpace(updatedBy)
	if current.UpdatedBy == "" {
		current.UpdatedBy = "management:auto-clear"
	}
	if current.ManualSuspended {
		cfg.OAuthAccountQuotaGroupState = internalconfig.UpsertOAuthAccountQuotaGroupState(cfg.OAuthAccountQuotaGroupState, current)
	} else {
		cfg.OAuthAccountQuotaGroupState = internalconfig.RemoveOAuthAccountQuotaGroupState(cfg.OAuthAccountQuotaGroupState, authID, groupID)
	}
	return cfg
}

// ClearExpiredOAuthQuotaGroupAutoStates removes auto cooldown state that has
// already passed its reset time while preserving manual suspensions.
func (m *Manager) ClearExpiredOAuthQuotaGroupAutoStates(now time.Time) bool {
	if m == nil {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()

	var (
		cfgToPublish  *internalconfig.Config
		authSnapshots []*Auth
	)

	m.mu.Lock()
	cfg := m.cloneRuntimeConfigForQuotaGroups()

	entries := internalconfig.NormalizeOAuthAccountQuotaGroupState(cfg.OAuthAccountQuotaGroupState)
	if len(entries) == 0 {
		m.mu.Unlock()
		return false
	}

	nextEntries := make([]internalconfig.OAuthAccountQuotaGroupState, 0, len(entries))
	affectedAuthIDs := make(map[string]struct{})
	changed := false

	for _, entry := range entries {
		if entry.AutoSuspendedUntil.IsZero() || entry.AutoSuspendedUntil.After(now) {
			nextEntries = append(nextEntries, entry)
			continue
		}

		changed = true
		if authID := strings.TrimSpace(entry.AuthID); authID != "" {
			affectedAuthIDs[authID] = struct{}{}
		}

		entry.AutoSuspendedUntil = time.Time{}
		entry.AutoReason = ""
		entry.SourceModel = ""
		entry.SourceProvider = ""
		entry.ResetTimeSource = ""
		entry.UpdatedAt = now
		entry.UpdatedBy = "system:auto-expire"

		if entry.ManualSuspended {
			nextEntries = append(nextEntries, entry)
		}
	}

	if changed {
		cfg.OAuthAccountQuotaGroupState = internalconfig.NormalizeOAuthAccountQuotaGroupState(nextEntries)
		cfgToPublish = cfg
		for authID := range affectedAuthIDs {
			auth := m.auths[authID]
			if auth == nil {
				continue
			}
			updateAggregatedAvailability(auth, now)
			auth.UpdatedAt = now
			authSnapshots = append(authSnapshots, auth.Clone())
		}
	}
	m.mu.Unlock()

	if cfgToPublish == nil {
		return false
	}

	m.SetConfig(cfgToPublish)
	if err := m.persistRuntimeConfigSnapshot(cfgToPublish); err != nil {
		log.WithError(err).Warn("failed to persist expired oauth-account-quota-group-state cleanup")
	}
	if m.scheduler != nil {
		for _, authSnapshot := range authSnapshots {
			if authSnapshot != nil {
				m.scheduler.upsertAuth(authSnapshot)
			}
		}
	}
	return true
}

// StartOAuthQuotaGroupCleanup launches a background goroutine that periodically
// reaps expired auto cooldowns. Keeping cleanup off the request path avoids the
// original implementation's issue where every Execute/ExecuteStream/CountTokens
// call took the global Manager lock and potentially re-wrote config.yaml.
//
// Only one loop is kept alive; starting a new one cancels the previous run.
// Pass a non-positive interval to use the default cadence.
func (m *Manager) StartOAuthQuotaGroupCleanup(parent context.Context, interval time.Duration) {
	if m == nil {
		return
	}
	if interval <= 0 {
		interval = defaultOAuthQuotaGroupCleanupInterval
	}

	m.mu.Lock()
	if m.quotaGroupCleanupCancel != nil {
		m.quotaGroupCleanupCancel()
	}
	ctx, cancel := context.WithCancel(parent)
	m.quotaGroupCleanupCancel = cancel
	m.mu.Unlock()

	go func() {
		// Run once shortly after start so any expired entries loaded from disk
		// are reaped without waiting for the first tick.
		primer := time.NewTimer(time.Second)
		defer primer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-primer.C:
			m.ClearExpiredOAuthQuotaGroupAutoStates(time.Now())
		}

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.ClearExpiredOAuthQuotaGroupAutoStates(time.Now())
			}
		}
	}()
}

// StopOAuthQuotaGroupCleanup cancels any running background cleanup loop.
func (m *Manager) StopOAuthQuotaGroupCleanup() {
	if m == nil {
		return
	}
	m.mu.Lock()
	cancel := m.quotaGroupCleanupCancel
	m.quotaGroupCleanupCancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
