package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

// SetRetryConfig updates retry attempts, credential retry limit and cooldown wait interval.
func (m *Manager) SetRetryConfig(retry int, maxRetryInterval time.Duration, maxRetryCredentials int) {
	if m == nil {
		return
	}
	if retry < 0 {
		retry = 0
	}
	if maxRetryCredentials < 0 {
		maxRetryCredentials = 0
	}
	if maxRetryInterval < 0 {
		maxRetryInterval = 0
	}
	m.requestRetry.Store(int32(retry))
	m.maxRetryCredentials.Store(int32(maxRetryCredentials))
	m.maxRetryInterval.Store(maxRetryInterval.Nanoseconds())
}

// RegisterExecutor registers a provider executor with the manager.
func (m *Manager) RegisterExecutor(executor ProviderExecutor) {
	if executor == nil {
		return
	}
	provider := strings.TrimSpace(executor.Identifier())
	if provider == "" {
		return
	}

	var replaced ProviderExecutor
	m.mu.Lock()
	replaced = m.executors[provider]
	m.executors[provider] = executor
	m.mu.Unlock()

	if replaced == nil || replaced == executor {
		return
	}
	if closer, ok := replaced.(ExecutionSessionCloser); ok && closer != nil {
		closer.CloseExecutionSession(CloseAllExecutionSessionsID)
	}
}

// UnregisterExecutor removes the executor associated with the provider key.
func (m *Manager) UnregisterExecutor(provider string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return
	}
	m.mu.Lock()
	delete(m.executors, provider)
	m.mu.Unlock()
}

// Register inserts a new auth entry into the manager.
func (m *Manager) Register(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth == nil {
		return nil, nil
	}
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return nil, fmt.Errorf("register auth: %w", errWeight)
	}
	if auth.ID == "" {
		auth.ID = uuid.NewString()
	}
	now := time.Now()
	cooldownStateChanged := normalizeModelStates(auth)
	if m.cooldownDisabledForAuth(auth) || auth.Disabled || auth.Status == StatusDisabled {
		cooldownStateChanged = clearCooldownStateForAuth(auth, now) || cooldownStateChanged
	}
	auth.EnsureIndex()
	authClone := auth.Clone()
	m.mu.Lock()
	m.auths[auth.ID] = authClone
	m.mu.Unlock()
	if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
		m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	}
	if m.scheduler != nil {
		m.scheduler.upsertAuth(authClone)
	}
	m.queueRefreshReschedule(auth.ID)
	_ = m.persist(ctx, auth)
	m.hook.OnAuthRegistered(ctx, auth.Clone())
	if cooldownStateChanged {
		m.persistCooldownStates(ctx)
	}
	m.applyPendingAffinityMigrations(auth)
	return auth.Clone(), nil
}

// Update replaces an existing auth entry and notifies hooks.
func (m *Manager) Update(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth == nil || auth.ID == "" {
		return nil, nil
	}
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return nil, fmt.Errorf("update auth: %w", errWeight)
	}
	m.mu.Lock()
	existing, ok := m.auths[auth.ID]
	if !ok || existing == nil {
		m.mu.Unlock()
		return nil, nil
	}
	if !auth.indexAssigned && auth.Index == "" {
		auth.Index = existing.Index
		auth.indexAssigned = existing.indexAssigned
	}
	auth.Success = existing.Success
	auth.Failed = existing.Failed
	auth.recentRequests = existing.recentRequests
	if !existing.Disabled && existing.Status != StatusDisabled && !auth.Disabled && auth.Status != StatusDisabled {
		if len(auth.ModelStates) == 0 && len(existing.ModelStates) > 0 {
			auth.ModelStates = existing.ModelStates
		}
	}
	now := time.Now()
	cooldownStateChanged := normalizeModelStates(auth)
	if m.cooldownDisabledForAuth(auth) || auth.Disabled || auth.Status == StatusDisabled {
		cooldownStateChanged = clearCooldownStateForAuth(auth, now) || cooldownStateChanged
	}
	auth.EnsureIndex()
	authClone := auth.Clone()
	m.auths[auth.ID] = authClone
	m.mu.Unlock()
	if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
		m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	}
	if m.scheduler != nil {
		m.scheduler.upsertAuth(authClone)
	}
	m.queueRefreshReschedule(auth.ID)
	_ = m.persist(ctx, auth)
	m.hook.OnAuthUpdated(ctx, auth.Clone())
	if cooldownStateChanged {
		m.persistCooldownStates(ctx)
	}
	return auth.Clone(), nil
}

// Remove deletes an auth from runtime state without persisting.
// Disk and token-store deletion must be handled by the caller.
func (m *Manager) Remove(ctx context.Context, id string) {
	if m == nil {
		return
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	_ = ctx

	m.mu.Lock()
	existing := m.auths[id]
	if existing == nil {
		m.mu.Unlock()
		return
	}
	provider := strings.TrimSpace(existing.Provider)
	delete(m.auths, id)
	if m.modelPoolOffsets != nil {
		delete(m.modelPoolOffsets, id)
	}
	for sessionID, sessionAuths := range m.homeRuntimeAuths {
		if sessionAuths == nil {
			continue
		}
		delete(sessionAuths, id)
		if len(sessionAuths) == 0 {
			delete(m.homeRuntimeAuths, sessionID)
		}
	}
	m.mu.Unlock()

	if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
		m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	}
	if m.scheduler != nil {
		m.scheduler.removeAuth(id)
	}
	m.queueRefreshUnschedule(id)
	m.stashPendingAffinityMigrations(existing, id)
	m.invalidateSessionAffinity(id)

	if provider != "" {
		if exec, ok := m.Executor(provider); ok && exec != nil {
			if closer, okCloser := exec.(ExecutionSessionCloser); okCloser {
				closer.CloseExecutionSession(CloseAllExecutionSessionsID)
			}
		}
	}
	m.persistCooldownStates(ctx)
}

func (m *Manager) invalidateSessionAffinity(authID string) {
	if m == nil || authID == "" {
		return
	}
	m.mu.RLock()
	selector := m.selector
	m.mu.RUnlock()
	if invalidator, ok := selector.(SessionAffinityView); ok && invalidator != nil {
		if removed := invalidator.InvalidateAuth(authID); removed > 0 {
			log.Warnf("session-affinity: invalidated %d binding(s) for auth %s", removed, authID)
		}
	}
}

// pendingAffinityMigrationTTL bounds how long a removed auth's session
// bindings stay eligible for rebinding onto a re-registered equivalent.
const pendingAffinityMigrationTTL = 30 * time.Second

// pendingAffinityMigration is one removed auth's stashed bindings plus the
// expiry that bounds how long a replacement may claim them.
type pendingAffinityMigration struct {
	bindings  []SessionAffinityBinding
	expiresAt time.Time
}

// affinityEquivalenceKey identifies the logical credential behind an auth for
// binding-migration purposes: the upstream base URL plus the compat entry
// name, disambiguated by config_index because every entry of one
// OpenAI-compat pool shares base_url and compat_name. config_index is stamped
// by the synthesizer and stable across API-key edits, unlike the auth ID,
// which hashes the API key. Auths without both base_url and compat_name
// (built-in OAuth channels, plugin executors) never migrate — their removals
// keep the plain invalidation behavior — even if a config_index is present.
// Empty return means "do not migrate".
func affinityEquivalenceKey(a *Auth) string {
	if a == nil || a.Attributes == nil {
		return ""
	}
	baseURL := strings.TrimSpace(a.Attributes["base_url"])
	compatName := strings.TrimSpace(a.Attributes["compat_name"])
	if baseURL == "" || compatName == "" {
		return ""
	}
	return strings.ToLower(baseURL) + "|" + strings.ToLower(compatName) + "|" + strings.TrimSpace(a.Attributes["config_index"])
}

// stashPendingAffinityMigrations snapshots the removed auth's live affinity
// bindings so a re-rendered equivalent auth can rebind them. Genuinely
// deleted credentials are unaffected: nothing rebinds unless a Register
// arrives with the same equivalence key inside the TTL window. Expired
// stashes are pruned on every remove, migratable or not. A known benign
// race exists: a Register applying between this Remove's delete and its
// stash finds nothing, and the stash may then be consumed by a later
// Register with the same key — today's re-render dispatch is sequential
// (coalesced updates), and a mis-consumption only rebinds within the same
// pool, self-healing on the next pick.
func (m *Manager) stashPendingAffinityMigrations(existing *Auth, id string) {
	key := affinityEquivalenceKey(existing)
	m.mu.Lock()
	now := time.Now()
	for k, pending := range m.pendingAffinityMigrations {
		if now.After(pending.expiresAt) {
			delete(m.pendingAffinityMigrations, k)
		}
	}
	m.mu.Unlock()
	if key == "" {
		return
	}
	m.mu.RLock()
	selector := m.selector
	m.mu.RUnlock()
	view, ok := selector.(interface {
		BindingsForAuth(string) []SessionAffinityBinding
	})
	if !ok {
		return
	}
	bindings := view.BindingsForAuth(id)
	if len(bindings) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingAffinityMigrations == nil {
		m.pendingAffinityMigrations = map[string]pendingAffinityMigration{}
	}
	m.pendingAffinityMigrations[key] = pendingAffinityMigration{
		bindings:  bindings,
		expiresAt: now.Add(pendingAffinityMigrationTTL),
	}
}

// applyPendingAffinityMigrations rebinds sessions stashed by a recent Remove
// when the newly registered auth is the same logical credential under a new
// ID. Only exact equivalence-key matches inside the 30s window migrate; the
// stash is always consumed so a late lookalike cannot resurrect stale pins.
// Rebinding onto a disabled credential is safe: Pick's availability gate
// reselects on unavailable bindings, and a disabled pool produces no
// replacement Register at all.
func (m *Manager) applyPendingAffinityMigrations(auth *Auth) {
	key := affinityEquivalenceKey(auth)
	if key == "" {
		return
	}
	m.mu.Lock()
	pending, ok := m.pendingAffinityMigrations[key]
	if ok {
		delete(m.pendingAffinityMigrations, key)
	}
	m.mu.Unlock()
	if !ok || time.Now().After(pending.expiresAt) {
		return
	}
	m.mu.RLock()
	selector := m.selector
	m.mu.RUnlock()
	view, okView := selector.(interface {
		RebindBindings([]SessionAffinityBinding, string)
	})
	if !okView {
		return
	}
	view.RebindBindings(pending.bindings, auth.ID)
	log.WithField("auth_id", auth.ID).Infof("session-affinity: migrated %d binding(s) onto re-registered auth", len(pending.bindings))
}

// Load resets manager state from the backing store.
func (m *Manager) Load(ctx context.Context) error {
	m.mu.Lock()
	if m.store == nil {
		m.mu.Unlock()
		return nil
	}
	items, err := m.store.List(ctx)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	m.auths = make(map[string]*Auth, len(items))
	for _, auth := range items {
		if auth == nil || auth.ID == "" {
			continue
		}
		if errWeight := ValidateAuthWeight(auth); errWeight != nil {
			continue
		}
		auth.EnsureIndex()
		m.auths[auth.ID] = auth.Clone()
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	m.rebuildAPIKeyModelAliasLocked(cfg)
	m.mu.Unlock()
	m.syncScheduler()
	return nil
}

func (m *Manager) persist(ctx context.Context, auth *Auth) error {
	if m.store == nil || auth == nil {
		return nil
	}
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return fmt.Errorf("persist auth: %w", errWeight)
	}
	if shouldSkipPersist(ctx) {
		return nil
	}
	if IsConfigAPIKeyAuth(auth) {
		return nil
	}
	if auth.Attributes != nil {
		if v := strings.ToLower(strings.TrimSpace(auth.Attributes["runtime_only"])); v == "true" {
			return nil
		}
	}
	if IsPluginVirtualAuth(auth) {
		return nil
	}
	// Skip persistence when metadata is absent (e.g., runtime-only auths).
	if auth.Metadata == nil {
		return nil
	}
	_, err := m.store.Save(ctx, auth)
	return err
}
