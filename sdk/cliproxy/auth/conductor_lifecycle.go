package auth

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	baseauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
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

func syncAuthStorageMetadata(auth *Auth) {
	if auth == nil || auth.Storage == nil {
		return
	}
	if setter, ok := auth.Storage.(interface{ SetMetadata(map[string]any) }); ok && setter != nil {
		setter.SetMetadata(auth.Metadata)
	}
}

func cloneAuthForConditionalPersistence(auth *Auth) *Auth {
	candidate := auth.Clone()
	if candidate == nil || candidate.Storage == nil {
		return candidate
	}
	source, ok := candidate.Storage.(baseauth.CredentialPersistenceSnapshotSource)
	if !ok || source == nil {
		return candidate
	}
	if storage := source.CredentialPersistenceSnapshot(); storage != nil {
		candidate.Storage = storage
	}
	return candidate
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
	syncAuthStorageMetadata(auth)
	auth.EnsureIndex()
	auth.credentialGeneration = m.authGeneration.Add(1)
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
	return auth.Clone(), nil
}

// Update replaces an existing auth entry and notifies hooks.
func (m *Manager) Update(ctx context.Context, auth *Auth) (*Auth, error) {
	updated, _, errUpdate := m.updateAuth(ctx, auth, nil)
	return updated, errUpdate
}

// updateAuth replaces auth only while expectedCurrent still owns its ID. A nil
// expectedCurrent preserves the unconditional Update behavior.
func (m *Manager) updateAuth(ctx context.Context, auth, expectedCurrent *Auth) (*Auth, bool, error) {
	if auth == nil || auth.ID == "" {
		return nil, false, nil
	}
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return nil, false, fmt.Errorf("update auth: %w", errWeight)
	}
	m.mu.Lock()
	existing, ok := m.auths[auth.ID]
	if !ok || existing == nil {
		m.mu.Unlock()
		return nil, false, nil
	}
	if expectedCurrent != nil && existing != expectedCurrent {
		current := existing.Clone()
		m.mu.Unlock()
		return current, false, nil
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
	syncAuthStorageMetadata(auth)
	auth.EnsureIndex()
	auth.credentialGeneration = m.authGeneration.Add(1)
	authClone := auth.Clone()
	persistedWithCAS := expectedCurrent != nil
	var persistenceCandidate *Auth
	if persistedWithCAS {
		persistenceCandidate = cloneAuthForConditionalPersistence(auth)
	}
	m.auths[auth.ID] = authClone
	m.mu.Unlock()
	if persistedWithCAS {
		m.persistConditionalUpdate(ctx, persistenceCandidate, authClone)
	}
	if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
		m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	}
	if persistedWithCAS {
		// Persistence can outlive this generation. Publish the update only while
		// authClone still owns the runtime ID so a concurrent replacement cannot
		// be overwritten in the scheduler or returned to a retrying request.
		m.mu.RLock()
		current := m.auths[auth.ID]
		if current != authClone {
			var currentClone *Auth
			if current != nil {
				currentClone = current.Clone()
			}
			m.mu.RUnlock()
			return currentClone, false, nil
		}
		if m.scheduler != nil {
			m.scheduler.upsertAuth(authClone)
		}
		if loop := m.refreshLoop; loop != nil {
			loop.queueReschedule(auth.ID)
		}
		m.mu.RUnlock()
	} else {
		if m.scheduler != nil {
			m.scheduler.upsertAuth(authClone)
		}
		m.queueRefreshReschedule(auth.ID)
		_ = m.persist(ctx, auth)
	}
	m.hook.OnAuthUpdated(ctx, auth.Clone())
	if cooldownStateChanged {
		m.persistCooldownStates(ctx)
	}
	return auth.Clone(), true, nil
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
	if invalidator, ok := m.selector.(interface{ InvalidateAuth(string) }); ok && invalidator != nil {
		invalidator.InvalidateAuth(authID)
	}
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
		auth.credentialGeneration = m.authGeneration.Add(1)
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

// shouldPersistAuth reports whether auth persistence is enabled for this record.
func (m *Manager) shouldPersistAuth(ctx context.Context, auth *Auth) bool {
	if m == nil || m.store == nil || auth == nil || shouldSkipPersist(ctx) {
		return false
	}
	if ValidateAuthWeight(auth) != nil {
		return false
	}
	if IsConfigAPIKeyAuth(auth) || IsPluginVirtualAuth(auth) {
		return false
	}
	if auth.Attributes != nil {
		if v := strings.ToLower(strings.TrimSpace(auth.Attributes["runtime_only"])); v == "true" {
			return false
		}
	}
	return auth.Metadata != nil
}

func authPersistenceDeleteID(auth *Auth, savedID string) string {
	if savedID = strings.TrimSpace(savedID); savedID != "" {
		return savedID
	}
	if auth == nil {
		return ""
	}
	if path := authAttribute(auth, AttributePath); path != "" {
		return path
	}
	if fileName := strings.TrimSpace(auth.FileName); fileName != "" {
		return fileName
	}
	return strings.TrimSpace(auth.ID)
}

func persistenceTargetsMatch(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	leftClean := filepath.Clean(left)
	rightClean := filepath.Clean(right)
	if leftClean == rightClean {
		return true
	}
	if filepath.IsAbs(leftClean) != filepath.IsAbs(rightClean) {
		return filepath.Base(leftClean) == filepath.Base(rightClean)
	}
	return false
}

func authOwnsPersistenceTarget(auth *Auth, target string) bool {
	if auth == nil {
		return false
	}
	if path := authAttribute(auth, AttributePath); path != "" {
		return persistenceTargetsMatch(path, target)
	}
	if fileName := strings.TrimSpace(auth.FileName); fileName != "" {
		return persistenceTargetsMatch(fileName, target)
	}
	return persistenceTargetsMatch(auth.ID, target)
}

func (m *Manager) persistenceTargetOwner(ctx context.Context, target string) (*Auth, *Auth) {
	if m == nil || strings.TrimSpace(target) == "" {
		return nil, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, owner := range m.auths {
		if authOwnsPersistenceTarget(owner, target) && m.shouldPersistAuth(ctx, owner) {
			return owner, cloneAuthForConditionalPersistence(owner)
		}
	}
	return nil, nil
}

// reconcilePersistenceTarget makes a stale save target reflect its current
// runtime owner. The owner is rechecked after each store operation so a rename,
// removal, or cross-ID target reuse that races with I/O is repaired rather than
// having a newer credential deleted.
func (m *Manager) reconcilePersistenceTarget(ctx context.Context, target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}
	for {
		owner, candidate := m.persistenceTargetOwner(ctx, target)
		if owner == nil {
			if errDelete := m.store.Delete(ctx, target); errDelete != nil {
				return errDelete
			}
		} else if _, errSave := m.store.Save(ctx, candidate); errSave != nil {
			return errSave
		}

		currentOwner, _ := m.persistenceTargetOwner(ctx, target)
		if currentOwner == owner {
			return nil
		}
	}
}

// persistConditionalUpdate persists a CAS-owned auth without holding the
// manager-wide auth lock. If ownership changes while I/O is in flight, the
// current same-ID generation is persisted again. If ownership disappears, the
// stale save is deleted; a new owner that appears during cleanup is then
// re-persisted so deletion cannot erase a legitimate replacement.
func (m *Manager) persistConditionalUpdate(ctx context.Context, auth, owner *Auth) {
	if auth == nil || owner == nil || auth.ID == "" || !m.shouldPersistAuth(ctx, auth) {
		return
	}
	candidate := auth.Clone()
	candidateOwner := owner
	staleSavedID := ""
	for {
		savedID, errPersist := m.store.Save(ctx, candidate)
		if errPersist != nil {
			return
		}
		savedID = authPersistenceDeleteID(candidate, savedID)
		if staleSavedID != "" && staleSavedID != savedID {
			if errDelete := m.reconcilePersistenceTarget(ctx, staleSavedID); errDelete != nil {
				return
			}
		}
		staleSavedID = ""

		m.mu.RLock()
		current := m.auths[auth.ID]
		if current == candidateOwner {
			m.mu.RUnlock()
			return
		}
		if current != nil && m.shouldPersistAuth(ctx, current) {
			candidate = cloneAuthForConditionalPersistence(current)
			candidateOwner = current
			staleSavedID = savedID
			m.mu.RUnlock()
			continue
		}
		m.mu.RUnlock()

		deleteID := savedID
		if deleteID == "" {
			return
		}
		if errDelete := m.reconcilePersistenceTarget(ctx, deleteID); errDelete != nil {
			return
		}

		m.mu.RLock()
		current = m.auths[auth.ID]
		if current == nil || !m.shouldPersistAuth(ctx, current) {
			m.mu.RUnlock()
			return
		}
		candidate = cloneAuthForConditionalPersistence(current)
		candidateOwner = current
		m.mu.RUnlock()
	}
}

func (m *Manager) persist(ctx context.Context, auth *Auth) error {
	if m.store == nil || auth == nil {
		return nil
	}
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return fmt.Errorf("persist auth: %w", errWeight)
	}
	if !m.shouldPersistAuth(ctx, auth) {
		return nil
	}
	_, err := m.store.Save(ctx, auth)
	return err
}
