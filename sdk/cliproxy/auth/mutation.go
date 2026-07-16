package auth

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrAuthNotFound                        = errors.New("auth not found")
	ErrAuthRevisionConflict                = errors.New("auth revision conflict")
	ErrAuthSourceConflict                  = errors.New("auth source conflict")
	ErrPriorityMutationUnsupported         = errors.New("priority mutation unsupported")
	ErrPriorityMutationRoutingIncompatible = errors.New("priority mutation routing incompatible")
)

type PriorityMutationOperation string

const (
	PriorityMutationSet   PriorityMutationOperation = "set"
	PriorityMutationUnset PriorityMutationOperation = "unset"
)

type PriorityMutation struct {
	Operation PriorityMutationOperation
	Priority  int
}

type PriorityValue struct {
	Present bool
	Value   int
}

type PriorityMutationResult struct {
	Auth     *Auth
	Revision string
	Priority PriorityValue
}

// ConditionalMutationStore persists a mutation only while the backing source
// still matches the manager snapshot supplied as before.
type ConditionalMutationStore interface {
	PersistMutation(ctx context.Context, before, after *Auth) (string, error)
}

// SourceValidationStore validates an already-persisted watcher snapshot before publication.
type SourceValidationStore interface {
	ValidateSource(ctx context.Context, auth *Auth) error
}

type authMutationLock struct {
	mu sync.Mutex
}

func (m *Manager) mutationLock(id string) *authMutationLock {
	id = strings.TrimSpace(id)
	value, _ := m.mutationLocks.LoadOrStore(id, &authMutationLock{})
	lock, _ := value.(*authMutationLock)
	if lock == nil {
		lock = &authMutationLock{}
		m.mutationLocks.Store(id, lock)
	}
	return lock
}

func (m *Manager) updateLocked(ctx context.Context, auth *Auth) (*Auth, error) {
	skipPersist := shouldSkipPersist(ctx)
	m.mu.Lock()
	existing, ok := m.auths[auth.ID]
	if !ok || existing == nil {
		m.mu.Unlock()
		return nil, nil
	}
	if auth.revision != "" && auth.revision != existing.revision {
		m.mu.Unlock()
		return nil, ErrAuthRevisionConflict
	}
	if skipPersist {
		if store, ok := m.store.(SourceValidationStore); ok && store != nil {
			candidate := auth.Clone()
			m.mu.Unlock()
			if errValidate := store.ValidateSource(ctx, candidate); errValidate != nil {
				return nil, errValidate
			}
			m.mu.Lock()
			existing = m.auths[auth.ID]
			if existing == nil {
				m.mu.Unlock()
				return nil, ErrAuthNotFound
			}
		}
	}
	if skipPersist && auth.revision == "" && sameDurableAuthState(existing, auth) {
		result := existing.Clone()
		m.mu.Unlock()
		return result, nil
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
	clearedCooldown := false
	if m.cooldownDisabledForAuth(auth) || auth.Disabled || auth.Status == StatusDisabled {
		clearedCooldown = clearCooldownStateForAuth(auth, now)
	}
	auth.EnsureIndex()
	revision, errRevision := newAuthRevision()
	if errRevision != nil {
		m.mu.Unlock()
		return nil, errRevision
	}
	auth.revision = revision
	authClone := auth.Clone()
	existingClone := existing.Clone()
	m.mu.Unlock()
	if errPersist := m.persistUpdate(ctx, existingClone, authClone); errPersist != nil {
		return nil, errPersist
	}
	if errPublication := ctx.Err(); errPublication != nil {
		return nil, m.recoverAuthUpdate(ctx, existingClone, authClone, errPublication, !skipPersist)
	}
	if errPublication := m.publishAuthUpdate(ctx, existingClone, authClone, clearedCooldown); errPublication != nil {
		return nil, m.recoverAuthUpdate(ctx, existingClone, authClone, errPublication, !skipPersist)
	}
	return authClone.Clone(), nil
}

func (m *Manager) publishAuthUpdate(ctx context.Context, existing, candidate *Auth, clearedCooldown bool) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("publish auth update: %v", recovered)
		}
	}()
	candidateClone := candidate.Clone()
	m.mu.Lock()
	current := m.auths[candidate.ID]
	if current == nil || current.revision != existing.revision {
		m.mu.Unlock()
		return ErrAuthRevisionConflict
	}
	m.auths[candidate.ID] = candidateClone
	m.mu.Unlock()
	if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
		m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	}
	if m.scheduler != nil {
		m.scheduler.upsertAuth(candidateClone)
	}
	m.queueRefreshReschedule(candidate.ID)
	m.hook.OnAuthUpdated(ctx, candidateClone.Clone())
	if clearedCooldown {
		m.persistCooldownStates(ctx)
	}
	return nil
}

func (m *Manager) recoverAuthUpdate(ctx context.Context, existing, candidate *Auth, cause error, persisted bool) error {
	recoveryCtx := context.WithoutCancel(ctx)
	if !persisted {
		if errReconcile := m.reconcileAuthFromStore(recoveryCtx, existing.ID); errReconcile != nil {
			return errors.Join(cause, fmt.Errorf("reconcile source auth update: %w", errReconcile))
		}
		return cause
	}

	var errRollback error
	if store, ok := m.store.(ConditionalMutationStore); ok && store != nil {
		_, errRollback = store.PersistMutation(recoveryCtx, candidate.Clone(), existing.Clone())
	} else {
		errRollback = m.persist(recoveryCtx, existing.Clone())
	}
	if errRollback == nil {
		if errRuntime := m.restoreUpdatedAuthRuntime(existing); errRuntime != nil {
			return errors.Join(cause, fmt.Errorf("restore auth update runtime: %w", errRuntime))
		}
		return cause
	}
	if errReconcile := m.reconcileAuthFromStore(recoveryCtx, existing.ID); errReconcile != nil {
		return errors.Join(cause, fmt.Errorf("rollback persisted auth update: %w", errRollback), fmt.Errorf("reconcile persisted auth update: %w", errReconcile))
	}
	return errors.Join(cause, fmt.Errorf("rollback persisted auth update: %w", errRollback))
}

func (m *Manager) restoreUpdatedAuthRuntime(auth *Auth) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("restore auth update runtime: %v", recovered)
		}
	}()
	authClone := auth.Clone()
	m.mu.Lock()
	m.auths[auth.ID] = authClone
	m.mu.Unlock()
	m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	if m.scheduler != nil {
		m.scheduler.upsertAuth(authClone)
	}
	m.queueRefreshReschedule(auth.ID)
	return nil
}

func sameDurableAuthState(left, right *Auth) bool {
	normalize := func(auth *Auth) *Auth {
		if auth == nil {
			return nil
		}
		clone := auth.Clone()
		clone.revision = ""
		clone.Index = ""
		clone.indexAssigned = false
		clone.Storage = nil
		clone.Runtime = nil
		clone.CreatedAt = time.Time{}
		clone.UpdatedAt = time.Time{}
		clone.LastRefreshedAt = time.Time{}
		clone.NextRefreshAfter = time.Time{}
		clone.NextRetryAfter = time.Time{}
		clone.LastError = nil
		clone.ModelStates = nil
		clone.Quota = QuotaState{}
		clone.Unavailable = false
		clone.StatusMessage = ""
		clone.Success = 0
		clone.Failed = 0
		clone.recentRequests = recentRequestRing{}
		return clone
	}
	return reflect.DeepEqual(normalize(left), normalize(right))
}

func (m *Manager) persistUpdate(ctx context.Context, before, after *Auth) error {
	if shouldSkipPersist(ctx) {
		return nil
	}
	if store, ok := m.store.(ConditionalMutationStore); ok && store != nil {
		_, err := store.PersistMutation(ctx, before, after)
		return err
	}
	return m.persist(ctx, after)
}

func (m *Manager) MutatePriority(ctx context.Context, id, expectedRevision string, mutation PriorityMutation) (*PriorityMutationResult, error) {
	if m == nil {
		return nil, ErrAuthNotFound
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, ErrAuthNotFound
	}
	lock := m.mutationLock(id)
	lock.mu.Lock()
	defer lock.mu.Unlock()

	m.mu.RLock()
	current := m.auths[id]
	if current != nil {
		current = current.Clone()
	}
	m.mu.RUnlock()
	if current == nil {
		return nil, ErrAuthNotFound
	}
	if strings.TrimSpace(expectedRevision) == "" || expectedRevision != current.revision {
		return nil, ErrAuthRevisionConflict
	}
	if errCompatibility := m.priorityMutationCompatibilityError(current); errCompatibility != nil {
		return nil, errCompatibility
	}
	store, ok := m.store.(ConditionalMutationStore)
	if !ok || store == nil {
		return nil, ErrPriorityMutationUnsupported
	}

	candidate := current.Clone()
	if candidate.Metadata == nil {
		candidate.Metadata = make(map[string]any)
	}
	if candidate.Attributes == nil {
		candidate.Attributes = make(map[string]string)
	}
	priority := PriorityValue{}
	switch mutation.Operation {
	case PriorityMutationSet:
		candidate.Metadata["priority"] = float64(mutation.Priority)
		candidate.Attributes["priority"] = strconv.Itoa(mutation.Priority)
		priority = PriorityValue{Present: true, Value: mutation.Priority}
	case PriorityMutationUnset:
		delete(candidate.Metadata, "priority")
		delete(candidate.Attributes, "priority")
	default:
		return nil, fmt.Errorf("invalid priority operation %q", mutation.Operation)
	}
	revision, errRevision := newAuthRevision()
	if errRevision != nil {
		return nil, errRevision
	}
	candidate.revision = revision
	if _, errPersist := store.PersistMutation(ctx, current.Clone(), candidate.Clone()); errPersist != nil {
		return nil, errPersist
	}
	if errPublication := ctx.Err(); errPublication != nil {
		return nil, m.recoverPriorityMutation(ctx, store, current, candidate, errPublication)
	}
	if errPublication := m.publishPriorityMutation(ctx, current, candidate); errPublication != nil {
		return nil, m.recoverPriorityMutation(ctx, store, current, candidate, errPublication)
	}
	return &PriorityMutationResult{
		Auth:     candidate.Clone(),
		Revision: revision,
		Priority: priority,
	}, nil
}

func (m *Manager) publishPriorityMutation(ctx context.Context, current, candidate *Auth) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("publish priority mutation: %v", recovered)
		}
	}()
	candidateClone := candidate.Clone()
	m.mu.Lock()
	live := m.auths[current.ID]
	if live == nil || live.revision != current.revision {
		m.mu.Unlock()
		return ErrAuthRevisionConflict
	}
	m.auths[current.ID] = candidateClone
	m.mu.Unlock()
	if m.scheduler != nil {
		m.scheduler.upsertAuth(candidateClone)
	}
	m.queueRefreshReschedule(current.ID)
	m.hook.OnAuthUpdated(ctx, candidateClone.Clone())
	return nil
}

func (m *Manager) recoverPriorityMutation(ctx context.Context, store ConditionalMutationStore, current, candidate *Auth, cause error) error {
	recoveryCtx := context.WithoutCancel(ctx)
	if _, errRollback := store.PersistMutation(recoveryCtx, candidate.Clone(), current.Clone()); errRollback == nil {
		if errRuntime := m.restorePriorityRuntime(current); errRuntime != nil {
			return errors.Join(cause, fmt.Errorf("restore priority runtime: %w", errRuntime))
		}
		return cause
	} else {
		errReconcile := m.reconcileAuthFromStore(recoveryCtx, current.ID)
		if errReconcile != nil {
			return errors.Join(cause, fmt.Errorf("rollback persisted priority mutation: %w", errRollback), fmt.Errorf("reconcile persisted priority mutation: %w", errReconcile))
		}
		return errors.Join(cause, fmt.Errorf("rollback persisted priority mutation: %w", errRollback))
	}
}

func (m *Manager) restorePriorityRuntime(auth *Auth) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("restore priority runtime: %v", recovered)
		}
	}()
	authClone := auth.Clone()
	m.mu.Lock()
	m.auths[auth.ID] = authClone
	m.mu.Unlock()
	if m.scheduler != nil {
		m.scheduler.upsertAuth(authClone)
	}
	m.queueRefreshReschedule(auth.ID)
	return nil
}

func (m *Manager) reconcileAuthFromStore(ctx context.Context, id string) (resultErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			resultErr = fmt.Errorf("reconcile auth from store: %v", recovered)
		}
	}()
	if m.store == nil {
		return errors.New("auth store unavailable")
	}
	items, err := m.store.List(ctx)
	if err != nil {
		return err
	}
	for _, auth := range items {
		if auth == nil || auth.ID != id {
			continue
		}
		snapshot := auth.Clone()
		snapshot.revision = ""
		m.mu.RLock()
		current := m.auths[id]
		if current != nil {
			current = current.Clone()
		}
		m.mu.RUnlock()
		alreadyPublished := current != nil && sameDurableAuthState(current, snapshot)
		reconciled, errUpdate := m.updateLocked(WithSkipPersist(ctx), snapshot)
		if errUpdate != nil {
			return errUpdate
		}
		if alreadyPublished && reconciled != nil {
			if m.scheduler != nil {
				m.scheduler.upsertAuth(reconciled)
			}
			m.queueRefreshReschedule(id)
			m.hook.OnAuthUpdated(ctx, reconciled.Clone())
		}
		return nil
	}
	return ErrAuthNotFound
}

func (m *Manager) priorityMutationCompatibilityError(target *Auth) error {
	if target == nil || target.Metadata == nil || IsPluginVirtualAuth(target) || IsConfigAPIKeyAuth(target) {
		return ErrPriorityMutationUnsupported
	}
	if target.Attributes != nil {
		if strings.EqualFold(strings.TrimSpace(target.Attributes["runtime_only"]), "true") {
			return ErrPriorityMutationUnsupported
		}
	}
	if m.hasPluginScheduler() {
		return ErrPriorityMutationUnsupported
	}
	if strings.EqualFold(strings.TrimSpace(target.Provider), "codex") {
		m.mu.RLock()
		defer m.mu.RUnlock()
		for _, auth := range m.auths {
			if auth != nil && strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") && authWebsocketsEnabled(auth) {
				return ErrPriorityMutationRoutingIncompatible
			}
		}
	}
	return nil
}
