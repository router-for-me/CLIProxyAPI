package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

var quotaCooldownDisabled atomic.Bool

var transientErrorCooldownSeconds atomic.Int64

// SetQuotaCooldownDisabled toggles auth/model cooldown scheduling globally.
func SetQuotaCooldownDisabled(disable bool) {
	quotaCooldownDisabled.Store(disable)
}

// SetTransientErrorCooldownSeconds configures cooldowns for 408/500/502/503/504.
// 0 keeps the legacy default; negative values disable transient error cooldowns.
func SetTransientErrorCooldownSeconds(seconds int) {
	transientErrorCooldownSeconds.Store(int64(seconds))
}

func quotaCooldownDisabledForAuth(auth *Auth) bool {
	return quotaCooldownDisabledForAuthWithConfig(auth, nil)
}

func quotaCooldownDisabledForAuthWithConfig(auth *Auth, cfg *internalconfig.Config) bool {
	// Home owns cooldown state, so downstream instances must not schedule local cooldowns.
	if cfg != nil && cfg.Home.Enabled {
		return true
	}
	if auth != nil {
		if override, ok := auth.DisableCoolingOverride(); ok {
			return override
		}
		if override, ok := providerCoolingOverrideForAuth(auth, cfg); ok {
			return override
		}
	}
	if cfg != nil && cfg.DisableCooling {
		return true
	}
	return quotaCooldownDisabled.Load()
}

func providerCoolingOverrideForAuth(auth *Auth, cfg *internalconfig.Config) (bool, bool) {
	if auth == nil || cfg == nil {
		return false, false
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if provider == "" {
		return false, false
	}
	providerKey := ""
	compatName := ""
	if auth.Attributes != nil {
		providerKey = strings.TrimSpace(auth.Attributes["provider_key"])
		compatName = strings.TrimSpace(auth.Attributes["compat_name"])
	}
	if providerKey == "" && compatName == "" && provider != "openai-compatibility" {
		return false, false
	}
	if providerKey == "" {
		providerKey = provider
	}
	entry := resolveOpenAICompatConfig(cfg, providerKey, compatName, provider)
	if entry == nil || entry.DisableCooling == nil {
		return false, false
	}
	return *entry.DisableCooling, true
}

func nextTransientErrorRetryAfter(now time.Time) time.Time {
	seconds := transientErrorCooldownSeconds.Load()
	if seconds < 0 {
		return time.Time{}
	}
	if seconds == 0 {
		return now.Add(transientErrorCooldown)
	}
	return now.Add(time.Duration(seconds) * time.Second)
}

func recoverableFailureRetryAfter(now time.Time, disableCooling bool) time.Time {
	if disableCooling {
		return time.Time{}
	}
	return nextTransientErrorRetryAfter(now)
}

// SetConfig updates the runtime config snapshot used by request-time helpers.
// Callers should provide the latest config on reload so per-credential alias mapping stays in sync.
func (m *Manager) SetConfig(cfg *internalconfig.Config) {
	if m == nil {
		return
	}
	m.configCooldownMu.Lock()
	defer m.configCooldownMu.Unlock()
	if m.setConfigSnapshotLocked(cfg) {
		m.persistCooldownStatesLocked(context.Background())
	}
}

// SetConfigSnapshot updates only in-memory configuration state. It reports whether
// a caller must persist cleared cooldown state after its commit critical section.
func (m *Manager) SetConfigSnapshot(cfg *internalconfig.Config) bool {
	if m == nil {
		return false
	}
	m.configCooldownMu.Lock()
	defer m.configCooldownMu.Unlock()
	return m.setConfigSnapshotLocked(cfg)
}

func (m *Manager) setConfigSnapshotLocked(cfg *internalconfig.Config) bool {
	if cfg == nil {
		cfg = &internalconfig.Config{}
	} else {
		cfg = cfg.CloneForRuntime()
	}
	m.mu.RLock()
	oldCooldownStore := m.cooldownStore
	m.mu.RUnlock()
	previousCfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if homeSessionAliasTTL(previousCfg) != homeSessionAliasTTL(cfg) {
		m.homeSessionAliases.clear()
	}
	m.runtimeConfig.Store(cfg)
	clearedCooldowns := m.clearDisabledCooldownStates(cfg)
	if clearedCooldowns && oldCooldownStore != nil {
		m.mu.Lock()
		if m.cooldownStore == oldCooldownStore {
			m.pendingCooldownStateStore = oldCooldownStore
		}
		m.mu.Unlock()
	}
	if !cfg.Home.Enabled {
		m.clearHomeRuntimeAuths()
	}
	m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	return clearedCooldowns
}

// ApplyConfigWithCooldownStateStore serializes a config update with its cooldown
// store transition. It persists the resulting state to the captured old store before
// exposing the resolved replacement store.
func (m *Manager) ApplyConfigWithCooldownStateStore(ctx context.Context, cfg *internalconfig.Config, store CooldownStateStore) bool {
	if m == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}

	m.configCooldownMu.Lock()
	defer m.configCooldownMu.Unlock()
	m.mu.RLock()
	oldStore := m.cooldownStore
	m.mu.RUnlock()
	m.setConfigSnapshotLocked(cfg)
	if oldStore != nil && !m.persistCooldownStatesToLocked(ctx, oldStore) {
		return false
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cooldownStore != oldStore {
		return false
	}
	if m.pendingCooldownStateStore == oldStore {
		m.pendingCooldownStateStore = nil
	}
	m.cooldownStore = store
	return true
}

// PersistCooldownStates writes the current cooldown snapshot using ctx.
func (m *Manager) PersistCooldownStates(ctx context.Context) {
	m.persistCooldownStates(ctx)
}

// SwapCooldownStateStore persists cleared state to the old store before replacing it.
// Persistence is deliberately performed without holding the manager lock.
func (m *Manager) SwapCooldownStateStore(ctx context.Context, store CooldownStateStore, persistOld bool) bool {
	if m == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	m.configCooldownMu.Lock()
	defer m.configCooldownMu.Unlock()
	m.mu.RLock()
	oldStore := m.cooldownStore
	pendingStore := m.pendingCooldownStateStore
	m.mu.RUnlock()
	storeToPersist := pendingStore
	if storeToPersist == nil && persistOld {
		storeToPersist = oldStore
	}
	if storeToPersist != nil && !m.persistCooldownStatesToLocked(ctx, storeToPersist) {
		return false
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cooldownStore != oldStore {
		return false
	}
	if m.pendingCooldownStateStore == storeToPersist {
		m.pendingCooldownStateStore = nil
	}
	m.cooldownStore = store
	return true
}

func (m *Manager) cooldownDisabledForAuth(auth *Auth) bool {
	if m == nil {
		return quotaCooldownDisabledForAuth(auth)
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	return quotaCooldownDisabledForAuthWithConfig(auth, cfg)
}

func (m *Manager) clearDisabledCooldownStates(cfg *internalconfig.Config) bool {
	if m == nil {
		return false
	}
	now := time.Now()
	snapshots := make([]*Auth, 0)
	m.mu.Lock()
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		if !quotaCooldownDisabledForAuthWithConfig(auth, cfg) && !auth.Disabled && auth.Status != StatusDisabled {
			continue
		}
		if clearCooldownStateForAuth(auth, now) {
			snapshots = append(snapshots, auth.Clone())
		}
	}
	m.mu.Unlock()

	if m.scheduler != nil {
		for _, snapshot := range snapshots {
			m.scheduler.upsertAuth(snapshot)
		}
	}
	return len(snapshots) > 0
}

// RestoreCooldownStates restores unexpired persisted cooldown records into registered auths.
func (m *Manager) RestoreCooldownStates(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.RLock()
	store := m.cooldownStore
	m.mu.RUnlock()
	if store == nil {
		return nil
	}
	records, errLoad := store.Load(ctx)
	if errLoad != nil {
		return errLoad
	}
	if len(records) == 0 {
		return nil
	}

	now := time.Now()
	authLevelRecords := make([]CooldownStateRecord, 0)
	snapshotsByID := make(map[string]*Auth)

	m.mu.Lock()
	for _, record := range records {
		if strings.TrimSpace(record.Model) == "" {
			authLevelRecords = append(authLevelRecords, record)
			continue
		}
		if m.restoreCooldownRecordLocked(record, now) {
			if auth := m.auths[strings.TrimSpace(record.AuthID)]; auth != nil {
				snapshotsByID[auth.ID] = auth.Clone()
			}
		}
	}
	for _, record := range authLevelRecords {
		if m.restoreCooldownRecordLocked(record, now) {
			if auth := m.auths[strings.TrimSpace(record.AuthID)]; auth != nil {
				snapshotsByID[auth.ID] = auth.Clone()
			}
		}
	}
	m.mu.Unlock()

	if m.scheduler != nil {
		for _, snapshot := range snapshotsByID {
			m.scheduler.upsertAuth(snapshot)
		}
	}
	m.persistCooldownStates(ctx)
	return nil
}

func (m *Manager) restoreCooldownRecordLocked(record CooldownStateRecord, now time.Time) bool {
	authID := strings.TrimSpace(record.AuthID)
	if authID == "" || record.NextRetryAfter.IsZero() || !record.NextRetryAfter.After(now) {
		return false
	}
	auth := m.auths[authID]
	if auth == nil || auth.Disabled || auth.Status == StatusDisabled || m.cooldownDisabledForAuth(auth) {
		return false
	}
	updatedAt := record.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = now
	}
	reason := strings.TrimSpace(record.Reason)
	model := strings.TrimSpace(record.Model)
	quota := record.Quota
	if quota.Exceeded && quota.NextRecoverAt.IsZero() {
		quota.NextRecoverAt = record.NextRetryAfter
	}

	if model == "" {
		auth.Unavailable = true
		auth.Status = StatusError
		auth.NextRetryAfter = record.NextRetryAfter
		applyCooldownFields(&auth.Quota, quota)
		auth.Quota = mergeQuotaObservation(auth.Quota, quota)
		auth.UpdatedAt = updatedAt
		if reason != "" {
			auth.StatusMessage = reason
		}
		auth.LastError = cloneError(record.LastError)
		return true
	}

	state := ensureModelState(auth, model)
	mergeModelState(state, &ModelState{
		Unavailable:    true,
		Status:         StatusError,
		StatusMessage:  reason,
		NextRetryAfter: record.NextRetryAfter,
		Quota:          quota,
		LastError:      cloneError(record.LastError),
		UpdatedAt:      updatedAt,
	})
	updateAggregatedAvailability(auth, now)
	return true
}

func clearCooldownStateForAuth(auth *Auth, now time.Time) bool {
	if auth == nil {
		return false
	}
	changed := false
	if auth.Unavailable || !auth.NextRetryAfter.IsZero() || auth.Quota.Exceeded || !auth.Quota.NextRecoverAt.IsZero() {
		auth.Unavailable = false
		auth.NextRetryAfter = time.Time{}
		applyCooldownFields(&auth.Quota, QuotaState{})
		auth.UpdatedAt = now
		changed = true
	}
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		if state.Unavailable || !state.NextRetryAfter.IsZero() || state.Quota.Exceeded || !state.Quota.NextRecoverAt.IsZero() {
			state.Unavailable = false
			state.NextRetryAfter = time.Time{}
			applyCooldownFields(&state.Quota, QuotaState{})
			state.UpdatedAt = now
			changed = true
		}
	}
	if len(auth.ModelStates) > 0 {
		updateAggregatedAvailability(auth, now)
	}
	return changed
}

func dedupeStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	seen := make(map[string]struct{}, len(values))
	out := values[:0]
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// ResetQuota clears quota/cooldown state for an auth and resumes registry routing.
func (m *Manager) ResetQuota(ctx context.Context, authID string) (*Auth, []string, error) {
	if m == nil {
		return nil, nil, nil
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return nil, nil, fmt.Errorf("auth id is required")
	}

	now := time.Now()
	var snapshot *Auth
	models := make([]string, 0)
	registeredModels := modelsForRegisteredAuth(authID)
	cooldownStateChanged := false

	m.mu.Lock()
	auth, ok := m.auths[authID]
	if !ok || auth == nil {
		m.mu.Unlock()
		return nil, nil, nil
	}

	var cooldownRecordsBefore []CooldownStateRecord
	trackCooldownState := m.cooldownStore != nil
	if trackCooldownState {
		cooldownRecordsBefore = m.cooldownStateRecordsForAuthLocked(auth, now)
	}

	for modelKey, state := range auth.ModelStates {
		if strings.TrimSpace(modelKey) == "" {
			continue
		}
		models = append(models, modelKey)
		if state != nil {
			resetModelState(state, now)
		}
	}
	if clearCooldownStateForAuth(auth, now) {
		if len(models) == 0 {
			models = append(models, registeredModels...)
		}
	} else if len(auth.ModelStates) > 0 {
		updateAggregatedAvailability(auth, now)
	}

	if len(models) == 0 {
		models = append(models, registeredModels...)
	}
	models = dedupeStrings(models)

	if !auth.Disabled && auth.Status != StatusDisabled && !hasModelError(auth, now) {
		auth.LastError = nil
		auth.StatusMessage = ""
		auth.Status = StatusActive
	}
	auth.UpdatedAt = now
	if errPersist := m.persist(ctx, auth); errPersist != nil {
		m.mu.Unlock()
		return nil, nil, errPersist
	}
	snapshot = auth.Clone()
	if trackCooldownState {
		cooldownRecordsAfter := m.cooldownStateRecordsForAuthLocked(auth, now)
		cooldownStateChanged = !cooldownStateRecordsEqual(cooldownRecordsBefore, cooldownRecordsAfter)
	}
	m.mu.Unlock()

	for _, modelKey := range models {
		registry.GetGlobalRegistry().ClearModelQuotaExceeded(authID, modelKey)
		registry.GetGlobalRegistry().ResumeClientModel(authID, modelKey)
	}
	if m.scheduler != nil && snapshot != nil {
		m.scheduler.upsertAuth(snapshot)
	}
	if snapshot != nil && cooldownStateChanged {
		m.persistCooldownStates(ctx)
	}
	return snapshot, models, nil
}

func modelsForRegisteredAuth(authID string) []string {
	supportedModels := registry.GetGlobalRegistry().GetModelsForClient(authID)
	models := make([]string, 0, len(supportedModels))
	for _, supportedModel := range supportedModels {
		if supportedModel == nil || strings.TrimSpace(supportedModel.ID) == "" {
			continue
		}
		models = append(models, canonicalModelKey(supportedModel.ID))
	}
	return models
}

func (m *Manager) persistCooldownStates(ctx context.Context) {
	if m == nil {
		return
	}
	m.configCooldownMu.Lock()
	defer m.configCooldownMu.Unlock()
	m.persistCooldownStatesLocked(ctx)
}

func (m *Manager) persistCooldownStatesLocked(ctx context.Context) {
	m.mu.RLock()
	store := m.cooldownStore
	m.mu.RUnlock()
	if m.persistCooldownStatesToLocked(ctx, store) {
		m.mu.Lock()
		if m.pendingCooldownStateStore == store {
			m.pendingCooldownStateStore = nil
		}
		m.mu.Unlock()
	}
}

func (m *Manager) persistCooldownStatesToLocked(ctx context.Context, store CooldownStateStore) bool {
	if m == nil || store == nil {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	records := m.cooldownStateRecordsSnapshot()
	if errSave := store.Save(ctx, records); errSave != nil {
		logEntryWithRequestID(ctx).Warnf("failed to persist cooldown state: %v", errSave)
		return false
	}
	return ctx.Err() == nil
}

func (m *Manager) cooldownStateRecordsSnapshot() []CooldownStateRecord {
	now := time.Now()
	records := make([]CooldownStateRecord, 0)

	m.mu.RLock()
	for _, auth := range m.auths {
		records = append(records, m.cooldownStateRecordsForAuthLocked(auth, now)...)
	}
	m.mu.RUnlock()

	sort.Slice(records, func(i, j int) bool {
		if records[i].Provider != records[j].Provider {
			return records[i].Provider < records[j].Provider
		}
		if records[i].AuthID != records[j].AuthID {
			return records[i].AuthID < records[j].AuthID
		}
		return records[i].Model < records[j].Model
	})
	return records
}

func (m *Manager) cooldownStateRecordsForAuthLocked(auth *Auth, now time.Time) []CooldownStateRecord {
	if auth == nil || auth.ID == "" || auth.Disabled || auth.Status == StatusDisabled || m.cooldownDisabledForAuth(auth) {
		return nil
	}
	records := make([]CooldownStateRecord, 0, 1+len(auth.ModelStates))
	if record, ok := authCooldownStateRecord(auth, now); ok {
		records = append(records, record)
	}
	for model, state := range auth.ModelStates {
		if record, ok := modelCooldownStateRecord(auth, model, state, now); ok {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Model < records[j].Model
	})
	return records
}

func cooldownStateRecordsEqual(a, b []CooldownStateRecord) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !cooldownStateRecordEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func cooldownStateRecordEqual(a, b CooldownStateRecord) bool {
	if a.Provider != b.Provider ||
		a.AuthID != b.AuthID ||
		a.AuthFile != b.AuthFile ||
		a.Model != b.Model ||
		a.Status != b.Status ||
		a.Reason != b.Reason ||
		!a.NextRetryAfter.Equal(b.NextRetryAfter) ||
		!a.UpdatedAt.Equal(b.UpdatedAt) ||
		!cooldownQuotaEqual(a.Quota, b.Quota) {
		return false
	}
	return cooldownErrorEqual(a.LastError, b.LastError)
}

func cooldownQuotaEqual(a, b QuotaState) bool {
	return a.Exceeded == b.Exceeded &&
		a.Reason == b.Reason &&
		a.BackoffLevel == b.BackoffLevel &&
		a.NextRecoverAt.Equal(b.NextRecoverAt)
}

func cooldownErrorEqual(a, b *Error) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Code == b.Code &&
		a.Message == b.Message &&
		a.Retryable == b.Retryable &&
		a.HTTPStatus == b.HTTPStatus
}

func authCooldownStateRecord(auth *Auth, now time.Time) (CooldownStateRecord, bool) {
	if auth == nil || !auth.Unavailable || auth.NextRetryAfter.IsZero() || !auth.NextRetryAfter.After(now) {
		return CooldownStateRecord{}, false
	}
	return CooldownStateRecord{
		Provider:       strings.TrimSpace(auth.Provider),
		AuthID:         auth.ID,
		AuthFile:       cooldownAuthFile(auth),
		Status:         "cooling",
		NextRetryAfter: auth.NextRetryAfter,
		Reason:         cooldownReason(auth.StatusMessage, auth.Quota, auth.LastError),
		Quota:          cooldownFieldsOf(auth.Quota),
		LastError:      cloneError(auth.LastError),
		UpdatedAt:      auth.UpdatedAt,
	}, true
}

func modelCooldownStateRecord(auth *Auth, model string, state *ModelState, now time.Time) (CooldownStateRecord, bool) {
	model = strings.TrimSpace(model)
	if auth == nil || state == nil || model == "" || !state.Unavailable || state.NextRetryAfter.IsZero() || !state.NextRetryAfter.After(now) {
		return CooldownStateRecord{}, false
	}
	return CooldownStateRecord{
		Provider:       strings.TrimSpace(auth.Provider),
		AuthID:         auth.ID,
		AuthFile:       cooldownAuthFile(auth),
		Model:          model,
		Status:         "cooling",
		NextRetryAfter: state.NextRetryAfter,
		Reason:         cooldownReason(state.StatusMessage, state.Quota, state.LastError),
		Quota:          cooldownFieldsOf(state.Quota),
		LastError:      cloneError(state.LastError),
		UpdatedAt:      state.UpdatedAt,
	}, true
}

func cooldownReason(statusMessage string, quota QuotaState, lastErr *Error) string {
	if reason := strings.TrimSpace(quota.Reason); reason != "" {
		return reason
	}
	if statusMessage = strings.TrimSpace(statusMessage); statusMessage != "" {
		return statusMessage
	}
	if lastErr != nil {
		if code := strings.TrimSpace(lastErr.Code); code != "" {
			return code
		}
		if message := strings.TrimSpace(lastErr.Message); message != "" {
			return message
		}
	}
	return ""
}

// MarkResult records an execution result and notifies hooks.
func (m *Manager) MarkResult(ctx context.Context, result Result) {
	if result.AuthID == "" {
		return
	}
	modelKey := canonicalModelKey(result.Model)

	shouldResumeModel := false
	shouldSuspendModel := false
	suspendReason := ""
	clearModelQuota := false
	setModelQuota := false
	var authSnapshot *Auth
	cooldownStateChanged := false

	m.mu.Lock()
	if auth, ok := m.auths[result.AuthID]; ok && auth != nil {
		now := time.Now()
		responseHeaders := internallogging.GetResponseHeaders(ctx)
		modelState := existingModelState(auth, modelKey)
		var cooldownRecordsBefore []CooldownStateRecord
		trackCooldownState := m.cooldownStore != nil
		if trackCooldownState {
			cooldownRecordsBefore = m.cooldownStateRecordsForAuthLocked(auth, now)
		}
		auth.recordRecentRequest(now, result.Success)
		if result.Success {
			auth.Success++
		} else {
			auth.Failed++
		}

		if result.Success {
			if auth.Quota.Reason == "credential_quota" && auth.Quota.NextRecoverAt.After(now) {
				// Retain active credential-scoped cooldown
			} else if modelKey != "" {
				state := ensureModelState(auth, modelKey)
				modelState = state
				resetModelState(state, now)
				updateAggregatedAvailability(auth, now)
				if !hasModelError(auth, now) {
					auth.LastError = nil
					auth.StatusMessage = ""
					auth.Status = StatusActive
				}
				auth.UpdatedAt = now
				shouldResumeModel = true
				clearModelQuota = true
			} else {
				clearAuthStateOnSuccess(auth, now)
			}
		} else {
			if modelKey != "" {
				if !shouldSkipCredentialCooldown(result.Error) {
					disableCooling := m.cooldownDisabledForAuth(auth)
					if result.Error != nil && result.Error.Code == ErrorCodeForceCooldown {
						disableCooling = false
					}
					state := ensureModelState(auth, modelKey)
					modelState = state
					state.Unavailable = true
					state.Status = StatusError
					state.UpdatedAt = now
					if result.Error != nil {
						state.LastError = cloneError(result.Error)
						state.StatusMessage = result.Error.Message
						auth.LastError = cloneError(result.Error)
						auth.StatusMessage = result.Error.Message
					}

					statusCode := statusCodeFromResult(result.Error)
					if isModelSupportResultError(result.Error) {
						next := now.Add(12 * time.Hour)
						state.NextRetryAfter = next
						suspendReason = "model_not_supported"
						shouldSuspendModel = true
					} else if isCloudflareChallengeResultError(result.Error) {
						next, backoffLevel := nextCloudflareCooldown(state.Quota.BackoffLevel, disableCooling, now)
						state.NextRetryAfter = next
						state.StatusMessage = "cloudflare challenge"
						if auth.LastError != nil {
							auth.StatusMessage = "cloudflare challenge"
						}
						applyCooldownFields(&state.Quota, QuotaState{
							Exceeded:      true,
							Reason:        "cloudflare challenge",
							NextRecoverAt: next,
							BackoffLevel:  backoffLevel,
						})
					} else if isInvalidGrantResultError(result.Error) {
						if disableCooling {
							state.NextRetryAfter = time.Time{}
						} else {
							state.NextRetryAfter = now.Add(30 * time.Minute)
							suspendReason = "invalid_grant"
							shouldSuspendModel = true
						}
					} else {
						switch statusCode {
						case 401:
							if disableCooling {
								state.NextRetryAfter = time.Time{}
							} else {
								next := now.Add(30 * time.Minute)
								state.NextRetryAfter = next
								suspendReason = "unauthorized"
								shouldSuspendModel = true
							}
						case 402, 403:
							if disableCooling {
								state.NextRetryAfter = time.Time{}
							} else {
								next := now.Add(30 * time.Minute)
								state.NextRetryAfter = next
								suspendReason = "payment_required"
								shouldSuspendModel = true
							}
						case 404:
							if disableCooling {
								state.NextRetryAfter = time.Time{}
							} else {
								next := now.Add(12 * time.Hour)
								state.NextRetryAfter = next
								suspendReason = "not_found"
								shouldSuspendModel = true
							}
						case 429:
							var next time.Time
							backoffLevel := state.Quota.BackoffLevel
							if !disableCooling {
								if result.RetryAfter != nil {
									next = now.Add(*result.RetryAfter)
								} else {
									next, backoffLevel = quotaCooldownAfterFailure(state.Quota, now)
								}
								if state.Quota.Exceeded && state.Quota.NextRecoverAt.After(next) {
									next = state.Quota.NextRecoverAt
								}
							}
							state.NextRetryAfter = next
							applyCooldownFields(&state.Quota, QuotaState{
								Exceeded:      true,
								Reason:        "quota",
								NextRecoverAt: next,
								BackoffLevel:  backoffLevel,
							})
							if !disableCooling {
								suspendReason = "quota"
								shouldSuspendModel = true
								setModelQuota = true
							}
							if result.CredentialScope && !disableCooling {
								for _, otherState := range auth.ModelStates {
									if otherState != nil && otherState != state {
										otherState.Unavailable = true
										otherState.Status = StatusError
										otherNext := next
										if otherState.Quota.Exceeded && otherState.Quota.NextRecoverAt.After(otherNext) {
											otherNext = otherState.Quota.NextRecoverAt
										}
										otherState.NextRetryAfter = otherNext
										applyCooldownFields(&otherState.Quota, QuotaState{
											Exceeded:      true,
											Reason:        "credential_quota",
											NextRecoverAt: otherNext,
											BackoffLevel:  backoffLevel,
										})
									}
								}
								auth.Unavailable = true
								auth.Quota.Exceeded = true
								auth.Quota.Reason = "credential_quota"
								authNext := next
								if auth.Quota.NextRecoverAt.After(authNext) {
									authNext = auth.Quota.NextRecoverAt
								}
								auth.Quota.NextRecoverAt = authNext
								auth.NextRetryAfter = authNext
							}
						case 408, 500, 502, 503, 504:
							state.NextRetryAfter = recoverableFailureRetryAfter(now, disableCooling)
							state.Unavailable = !state.NextRetryAfter.IsZero()
						default:
							state.NextRetryAfter = recoverableFailureRetryAfter(now, disableCooling)
							state.Unavailable = !state.NextRetryAfter.IsZero()
						}
					}

					if disableCooling && state.NextRetryAfter.IsZero() && state.Quota.NextRecoverAt.IsZero() {
						state.Unavailable = false
						state.Quota.Exceeded = false
					}
					if result.Error != nil && result.Error.Code == ErrorCodeForceCooldown && state.NextRetryAfter.IsZero() {
						state.NextRetryAfter = now.Add(transientErrorCooldown)
						state.Unavailable = true
					}
					auth.Status = StatusError
					auth.UpdatedAt = now
					updateAggregatedAvailability(auth, now)
				}
			} else {
				disableCooling := m.cooldownDisabledForAuth(auth)
				if result.Error != nil && result.Error.Code == ErrorCodeForceCooldown {
					disableCooling = false
				}
				applyAuthFailureState(auth, result.Error, result.RetryAfter, now, disableCooling)
			}
		}

		if !result.SkipQuotaObservation {
			auth.Quota.ObserveResponseHeadersForProvider(result.Provider, responseHeaders, now)
			if modelState != nil {
				modelState.Quota.ObserveResponseHeadersForProvider(result.Provider, responseHeaders, now)
			}
		}

		_ = m.persist(ctx, auth)
		authSnapshot = auth.Clone()
		if trackCooldownState {
			cooldownRecordsAfter := m.cooldownStateRecordsForAuthLocked(auth, now)
			cooldownStateChanged = !cooldownStateRecordsEqual(cooldownRecordsBefore, cooldownRecordsAfter)
		}
	}
	m.mu.Unlock()
	if m.scheduler != nil && authSnapshot != nil {
		m.scheduler.upsertAuth(authSnapshot)
	}
	if authSnapshot != nil && cooldownStateChanged {
		m.persistCooldownStates(context.Background())
	}

	if clearModelQuota && modelKey != "" {
		registry.GetGlobalRegistry().ClearModelQuotaExceeded(result.AuthID, modelKey)
	}
	if setModelQuota && modelKey != "" {
		registry.GetGlobalRegistry().SetModelQuotaExceeded(result.AuthID, modelKey)
	}
	if shouldResumeModel {
		registry.GetGlobalRegistry().ResumeClientModel(result.AuthID, modelKey)
	} else if shouldSuspendModel {
		registry.GetGlobalRegistry().SuspendClientModel(result.AuthID, modelKey, suspendReason)
	}

	m.hook.OnResult(ctx, result)
	m.publishErrorEvent(result, authSnapshot)
	m.updateSessionAffinity(result)
}

func (m *Manager) updateSessionAffinity(result Result) {
	if m == nil || m.selector == nil {
		return
	}
	if affinity, ok := m.selector.(interface {
		OnResult(Result)
	}); ok && affinity != nil {
		affinity.OnResult(result)
	}
}

func (m *Manager) recordExecutionResult(ctx context.Context, result Result, auth *Auth, ephemeral bool) {
	if !ephemeral {
		m.MarkResult(ctx, result)
		return
	}
	m.reportHomeResult(ctx, result, auth)
}

// reportHomeResult only observes a Home dispatch result and never updates local auth state.
func (m *Manager) reportHomeResult(ctx context.Context, result Result, auth *Auth) {
	if m == nil || result.AuthID == "" {
		return
	}
	var snapshot *Auth
	if auth != nil {
		snapshot = auth.Clone()
	}
	m.hook.OnResult(ctx, result)
	m.publishErrorEvent(result, snapshot)
}

func (m *Manager) recordAvailabilityNeutralResult(ctx context.Context, result Result) {
	if result.AuthID == "" {
		return
	}

	var authSnapshot *Auth
	m.mu.Lock()
	if auth, ok := m.auths[result.AuthID]; ok && auth != nil {
		now := time.Now()
		auth.recordRecentRequest(now, result.Success)
		if result.Success {
			auth.Success++
		} else {
			auth.Failed++
		}
		_ = m.persist(ctx, auth)
		authSnapshot = auth.Clone()
	}
	m.mu.Unlock()

	m.hook.OnResult(ctx, result)
	m.publishErrorEvent(result, authSnapshot)
}

func existingModelState(auth *Auth, model string) *ModelState {
	model = canonicalModelKey(model)
	if auth == nil || model == "" {
		return nil
	}
	return auth.ModelStates[model]
}

func ensureModelState(auth *Auth, model string) *ModelState {
	model = canonicalModelKey(model)
	if auth == nil || model == "" {
		return nil
	}
	normalizeModelStates(auth)
	if auth.ModelStates == nil {
		auth.ModelStates = make(map[string]*ModelState)
	}
	if state, ok := auth.ModelStates[model]; ok && state != nil {
		return state
	}
	state := &ModelState{Status: StatusActive}
	auth.ModelStates[model] = state
	return state
}

func normalizeModelStates(auth *Auth) bool {
	if auth == nil || len(auth.ModelStates) == 0 {
		return false
	}
	normalized := make(map[string]*ModelState, len(auth.ModelStates))
	changed := false
	for model, state := range auth.ModelStates {
		modelKey := canonicalModelKey(model)
		if modelKey == "" {
			modelKey = strings.TrimSpace(model)
		}
		if modelKey != model {
			changed = true
		}
		if existing, ok := normalized[modelKey]; ok {
			normalized[modelKey] = mergeModelState(existing, state)
			changed = true
			continue
		}
		normalized[modelKey] = state
	}
	if changed {
		auth.ModelStates = normalized
	}
	return changed
}

func mergeModelState(target, source *ModelState) *ModelState {
	if target == nil {
		return source
	}
	if source == nil {
		return target
	}

	preferred := target
	fallback := source
	if source.UpdatedAt.After(target.UpdatedAt) {
		preferred = source
		fallback = target
	}
	merged := ModelState{
		Status:         preferred.Status,
		StatusMessage:  preferred.StatusMessage,
		Unavailable:    target.Unavailable || source.Unavailable,
		NextRetryAfter: target.NextRetryAfter,
		LastError:      cloneError(preferred.LastError),
		Quota: QuotaState{
			Exceeded:      target.Quota.Exceeded || source.Quota.Exceeded,
			Reason:        preferred.Quota.Reason,
			NextRecoverAt: target.Quota.NextRecoverAt,
			BackoffLevel:  target.Quota.BackoffLevel,
		},
		UpdatedAt: target.UpdatedAt,
	}
	merged.Quota = mergeQuotaObservation(merged.Quota, fallback.Quota)
	merged.Quota = mergeQuotaObservation(merged.Quota, preferred.Quota)
	if source.NextRetryAfter.After(merged.NextRetryAfter) {
		merged.NextRetryAfter = source.NextRetryAfter
	}
	if source.Quota.NextRecoverAt.After(merged.Quota.NextRecoverAt) {
		merged.Quota.NextRecoverAt = source.Quota.NextRecoverAt
	}
	if source.Quota.BackoffLevel > merged.Quota.BackoffLevel {
		merged.Quota.BackoffLevel = source.Quota.BackoffLevel
	}
	if source.UpdatedAt.After(merged.UpdatedAt) {
		merged.UpdatedAt = source.UpdatedAt
	}
	if merged.StatusMessage == "" {
		merged.StatusMessage = fallback.StatusMessage
	}
	if merged.LastError == nil {
		merged.LastError = cloneError(fallback.LastError)
	}
	if merged.Quota.Reason == "" {
		merged.Quota.Reason = fallback.Quota.Reason
	}
	if target.Status == StatusDisabled || source.Status == StatusDisabled {
		merged.Status = StatusDisabled
	} else if merged.Unavailable || merged.Quota.Exceeded {
		merged.Status = StatusError
	}
	*target = merged
	return target
}

func resetModelState(state *ModelState, now time.Time) {
	if state == nil {
		return
	}
	state.Unavailable = false
	state.Status = StatusActive
	state.StatusMessage = ""
	state.NextRetryAfter = time.Time{}
	state.LastError = nil
	applyCooldownFields(&state.Quota, QuotaState{})
	state.UpdatedAt = now
}

func modelStateIsClean(state *ModelState) bool {
	if state == nil {
		return true
	}
	if state.Status != StatusActive {
		return false
	}
	if state.Unavailable || state.StatusMessage != "" || !state.NextRetryAfter.IsZero() || state.LastError != nil {
		return false
	}
	if state.Quota.Exceeded || state.Quota.Reason != "" || !state.Quota.NextRecoverAt.IsZero() || state.Quota.BackoffLevel != 0 {
		return false
	}
	return true
}

func updateAggregatedAvailability(auth *Auth, now time.Time) {
	if auth == nil {
		return
	}
	if auth.Quota.Exceeded && auth.Quota.Reason == "credential_quota" && auth.Quota.NextRecoverAt.After(now) {
		auth.Unavailable = true
		return
	}
	if len(auth.ModelStates) == 0 {
		clearAggregatedAvailability(auth)
		return
	}
	allUnavailable := true
	earliestRetry := time.Time{}
	quotaExceeded := false
	quotaRecover := time.Time{}
	maxBackoffLevel := 0
	hasState := false
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		hasState = true
		stateUnavailable := false
		if state.Status == StatusDisabled {
			stateUnavailable = true
		} else if state.Unavailable {
			if state.NextRetryAfter.IsZero() {
				stateUnavailable = false
			} else if state.NextRetryAfter.After(now) {
				stateUnavailable = true
				if earliestRetry.IsZero() || state.NextRetryAfter.Before(earliestRetry) {
					earliestRetry = state.NextRetryAfter
				}
			} else {
				state.Unavailable = false
				state.NextRetryAfter = time.Time{}
			}
		}
		if !stateUnavailable {
			allUnavailable = false
		}
		if state.Quota.Exceeded {
			quotaExceeded = true
			if quotaRecover.IsZero() || (!state.Quota.NextRecoverAt.IsZero() && state.Quota.NextRecoverAt.Before(quotaRecover)) {
				quotaRecover = state.Quota.NextRecoverAt
			}
			if state.Quota.BackoffLevel > maxBackoffLevel {
				maxBackoffLevel = state.Quota.BackoffLevel
			}
		}
	}
	if !hasState {
		clearAggregatedAvailability(auth)
		return
	}
	auth.Unavailable = allUnavailable
	if allUnavailable {
		auth.NextRetryAfter = earliestRetry
	} else {
		auth.NextRetryAfter = time.Time{}
	}
	if quotaExceeded {
		auth.Quota.Exceeded = true
		auth.Quota.Reason = "quota"
		if auth.Quota.NextRecoverAt.After(quotaRecover) {
			quotaRecover = auth.Quota.NextRecoverAt
		}
		auth.Quota.NextRecoverAt = quotaRecover
		auth.Quota.BackoffLevel = maxBackoffLevel
	} else if auth.Quota.Exceeded && auth.Quota.NextRecoverAt.After(now) {
		// Retain active auth-level quota cooldown
	} else {
		auth.Quota.Exceeded = false
		auth.Quota.Reason = ""
		auth.Quota.NextRecoverAt = time.Time{}
		auth.Quota.BackoffLevel = 0
	}
}

func clearAggregatedAvailability(auth *Auth) {
	if auth == nil {
		return
	}
	auth.Unavailable = false
	auth.NextRetryAfter = time.Time{}
	applyCooldownFields(&auth.Quota, QuotaState{})
}

func hasModelError(auth *Auth, now time.Time) bool {
	if auth == nil || len(auth.ModelStates) == 0 {
		return false
	}
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		if state.LastError != nil {
			return true
		}
		if state.Status == StatusError {
			if state.Unavailable && (state.NextRetryAfter.IsZero() || state.NextRetryAfter.After(now)) {
				return true
			}
		}
	}
	return false
}

func clearAuthStateOnSuccess(auth *Auth, now time.Time) {
	if auth == nil {
		return
	}
	auth.Unavailable = false
	auth.Status = StatusActive
	auth.StatusMessage = ""
	auth.Quota.Exceeded = false
	auth.Quota.Reason = ""
	auth.Quota.NextRecoverAt = time.Time{}
	auth.Quota.BackoffLevel = 0
	auth.LastError = nil
	auth.NextRetryAfter = time.Time{}
	auth.UpdatedAt = now
}

func cloneError(err *Error) *Error {
	if err == nil {
		return nil
	}
	return &Error{
		Code:       err.Code,
		Message:    err.Message,
		Retryable:  err.Retryable,
		HTTPStatus: err.HTTPStatus,
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func statusCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	type statusCoder interface {
		StatusCode() int
	}
	var sc statusCoder
	if errors.As(err, &sc) && sc != nil {
		return sc.StatusCode()
	}
	return 0
}

func isRequestScopedError(err error) bool {
	if err == nil {
		return false
	}
	requestErr, ok := errors.AsType[cliproxyexecutor.RequestScopedError](err)
	return ok && requestErr != nil && requestErr.IsRequestScoped()
}

func resultErrorFromError(err error) *Error {
	if err == nil {
		return nil
	}
	var sourceErr *Error
	var resultErr *Error
	if errors.As(err, &sourceErr) && sourceErr != nil {
		resultErr = cloneError(sourceErr)
	} else {
		resultErr = &Error{Message: err.Error()}
	}
	if resultErr.HTTPStatus == 0 {
		resultErr.HTTPStatus = statusCodeFromError(err)
	}
	switch {
	case isRequestScopedError(err) || isRequestInvalidError(err):
		// Prefer true request-scoped faults (including Claude OAuth cancellation)
		// over the broader connection-lifecycle classification.
		resultErr.Code = requestScopedErrorCode
	case isConnectionLifecycleError(err):
		// Preserve lifecycle classification for MarkResult without making the error
		// request-scoped (which would also stop credential fallback).
		if resultErr.Code == "" || resultErr.Code == connectionLifecycleErrorCode {
			resultErr.Code = connectionLifecycleErrorCode
		}
	}
	return resultErr
}

// shouldSkipCredentialCooldown reports failures that must not mark auth/model cooling.
// Connection lifecycle is intentionally separate from request_scoped so transport
// drops do not also stop credential rotation via isRequestInvalidError.
func shouldSkipCredentialCooldown(err *Error) bool {
	if err != nil && err.Code == ErrorCodeForceCooldown {
		return false
	}
	return isRequestScopedResultError(err) || isConnectionLifecycleResultError(err)
}

// isConnectionLifecycleError reports transport/session lifecycle failures that must
// not cool credentials: client cancellation and WebSocket close/EOF disconnects.
func isConnectionLifecycleError(err error) bool {
	if err == nil {
		return false
	}
	// Typed WebSocket close codes are an unambiguous connection lifecycle signal.
	var closeErr *websocket.CloseError
	if errors.As(err, &closeErr) && closeErr != nil {
		switch closeErr.Code {
		case websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure:
			return true
		}
	}
	// Credential/auth/quota statuses must never be reclassified from response text.
	if statusCodeFromError(err) != 0 {
		return false
	}
	// Client abort and request-scoped timeouts are not credential faults.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	return isConnectionLifecycleMessage(err.Error())
}

func isConnectionLifecycleResultError(err *Error) bool {
	if err == nil {
		return false
	}
	if err.Code == connectionLifecycleErrorCode {
		return true
	}
	// Message fallback only when no HTTP status is attached, so 401/429/5xx
	// response bodies cannot suppress credential cooldown.
	if statusCodeFromResult(err) != 0 {
		return false
	}
	return isConnectionLifecycleMessage(err.Message)
}

func isConnectionLifecycleMessage(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	switch lower {
	case "context canceled", "context deadline exceeded", "eof", "unexpected eof":
		return true
	}
	// gorilla/websocket CloseError.Error() and common wrappers.
	if strings.Contains(lower, "websocket: close 1000") ||
		strings.Contains(lower, "websocket: close 1001") ||
		strings.Contains(lower, "websocket: close 1006") {
		return true
	}
	// Wrapped transport EOF phrasing (e.g. "read tcp ...: unexpected EOF").
	if strings.Contains(lower, "unexpected eof") {
		return true
	}
	return false
}

func isUnauthorizedError(err error) bool {
	if err == nil {
		return false
	}
	if statusCodeFromError(err) == http.StatusUnauthorized {
		return true
	}
	raw := strings.ToLower(err.Error())
	return strings.Contains(raw, "status 401") || strings.Contains(raw, "401 unauthorized")
}

func hasUnauthorizedAuthFailure(auth *Auth) bool {
	if auth == nil || auth.LastError == nil {
		return false
	}
	return auth.LastError.StatusCode() == http.StatusUnauthorized || strings.EqualFold(auth.LastError.Code, "unauthorized")
}

func refreshErrorFromError(err error) *Error {
	if err == nil {
		return nil
	}
	statusCode := statusCodeFromError(err)
	if statusCode == 0 && isUnauthorizedError(err) {
		statusCode = http.StatusUnauthorized
	}
	authErr := &Error{Message: err.Error(), HTTPStatus: statusCode}
	if statusCode == http.StatusUnauthorized {
		authErr.Code = "unauthorized"
		authErr.Retryable = false
	}
	return authErr
}

func retryAfterFromError(err error) *time.Duration {
	if err == nil {
		return nil
	}
	type retryAfterProvider interface {
		RetryAfter() *time.Duration
	}
	var rap retryAfterProvider
	if !errors.As(err, &rap) || rap == nil {
		return nil
	}
	retryAfter := rap.RetryAfter()
	if retryAfter == nil {
		return nil
	}
	value := *retryAfter
	return &value
}

func isCredentialScopedError(err error) bool {
	if err == nil {
		return false
	}
	type credentialScopedProvider interface {
		IsCredentialScoped() bool
	}
	var csp credentialScopedProvider
	return errors.As(err, &csp) && csp != nil && csp.IsCredentialScoped()
}

func statusCodeFromResult(err *Error) int {
	if err == nil {
		return 0
	}
	return err.StatusCode()
}

func isModelSupportErrorMessage(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	patterns := [...]string{
		"model_not_supported",
		"requested model is not supported",
		"requested model is unsupported",
		"requested model is unavailable",
		"model is not supported",
		"model not supported",
		"unsupported model",
		"model unavailable",
		"not available for your plan",
		"not available for your account",
	}
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func isModelSupportError(err error) bool {
	if err == nil {
		return false
	}
	status := statusCodeFromError(err)
	if status != http.StatusBadRequest && status != http.StatusUnprocessableEntity {
		return false
	}
	return isModelSupportErrorMessage(err.Error())
}

func isInvalidGrantErrorMessage(message string) bool {
	return strings.Contains(strings.ToLower(message), "invalid_grant")
}

func isInvalidGrantError(err error) bool {
	if err == nil {
		return false
	}
	status := statusCodeFromError(err)
	if status != http.StatusBadRequest && status != http.StatusUnauthorized {
		return false
	}
	return isInvalidGrantErrorMessage(err.Error())
}

func isInvalidGrantResultError(err *Error) bool {
	if err == nil {
		return false
	}
	status := statusCodeFromResult(err)
	if status != http.StatusBadRequest && status != http.StatusUnauthorized {
		return false
	}
	return isInvalidGrantErrorMessage(err.Code) || isInvalidGrantErrorMessage(err.Message)
}

func isModelSupportResultError(err *Error) bool {
	if err == nil {
		return false
	}
	status := statusCodeFromResult(err)
	if status != http.StatusBadRequest && status != http.StatusUnprocessableEntity {
		return false
	}
	return isModelSupportErrorMessage(err.Message)
}

func isCloudflareChallengeErrorMessage(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(lower, "challenge-platform") ||
		strings.Contains(lower, "cf-mitigated") ||
		strings.Contains(lower, "cloudflare challenge") ||
		(strings.Contains(lower, "cloudflare") && strings.Contains(lower, "<html"))
}

func isCloudflareChallengeError(err error) bool {
	if err == nil {
		return false
	}
	return isCloudflareChallengeErrorMessage(err.Error())
}

func isCloudflareChallengeResultError(err *Error) bool {
	if err == nil {
		return false
	}
	return isCloudflareChallengeErrorMessage(err.Message)
}

func nextCloudflareCooldown(backoffLevel int, disableCooling bool, now time.Time) (time.Time, int) {
	var next time.Time
	if !disableCooling {
		cooldown, nextLevel := nextQuotaCooldown(backoffLevel, disableCooling)
		if cooldown < 10*time.Second {
			cooldown = 10 * time.Second
		}
		if cooldown > 0 {
			next = now.Add(cooldown)
		}
		backoffLevel = nextLevel
	}
	return next, backoffLevel
}

func isRequestScopedNotFoundResultError(err *Error) bool {
	if err == nil || statusCodeFromResult(err) != http.StatusNotFound {
		return false
	}
	return clienterror.IsItemNotPersisted(err.Message)
}

func isRequestScopedResultError(err *Error) bool {
	if err == nil {
		return false
	}
	if err.IsRequestScoped() || isRequestScopedNotFoundResultError(err) {
		return true
	}
	return isRequestInvalidError(err)
}

func isCountTokensEndpointNotFoundError(err error, requestedModel string) bool {
	if err == nil || statusCodeFromError(err) != http.StatusNotFound {
		return false
	}
	baseModel := thinking.ParseSuffix(requestedModel).ModelName
	return !isExplicitModelNotFoundError(err, baseModel)
}

func isResponsesCompactRequest(opts cliproxyexecutor.Options) bool {
	return opts.Alt == "responses/compact"
}

func isResponsesCompactRequestFaultError(opts cliproxyexecutor.Options, err error) bool {
	if !isResponsesCompactRequest(opts) || err == nil {
		return false
	}
	if isCredentialScopedError(err) || isCloudflareChallengeError(err) || isInvalidGrantError(err) {
		return false
	}
	status := statusCodeFromError(err)
	if clienterror.IsRequestFault(status, err) {
		return true
	}
	switch status {
	case http.StatusBadRequest,
		http.StatusNotFound,
		http.StatusMethodNotAllowed,
		http.StatusConflict,
		http.StatusRequestEntityTooLarge,
		http.StatusUnprocessableEntity,
		http.StatusNotImplemented:
		return true
	default:
		return false
	}
}

func isResponsesCompactAvailabilityNeutralError(opts cliproxyexecutor.Options, err error, resultErr *Error) bool {
	if !isResponsesCompactRequest(opts) {
		return false
	}
	if resultErr != nil && resultErr.Code == ErrorCodeForceCooldown {
		return false
	}
	if isCredentialScopedError(err) || isCloudflareChallengeError(err) || isInvalidGrantError(err) {
		return false
	}
	if resultErr != nil && (isCloudflareChallengeResultError(resultErr) || isInvalidGrantResultError(resultErr)) {
		return false
	}
	status := statusCodeFromError(err)
	if status == 0 && resultErr != nil {
		status = statusCodeFromResult(resultErr)
	}
	if status == http.StatusUnauthorized || status == http.StatusPaymentRequired || status == http.StatusForbidden || status == http.StatusTooManyRequests {
		return false
	}
	return true
}

func isExplicitModelNotFoundError(err error, requestedModel string) bool {
	if err == nil {
		return false
	}
	if authErr, ok := err.(*Error); ok && authErr != nil {
		if isModelNotFoundIdentifier(authErr.Code) || isStructuredModelNotFoundError(authErr.Message, requestedModel) {
			return true
		}
	} else if isStructuredModelNotFoundError(err.Error(), requestedModel) {
		return true
	}

	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		for _, nested := range wrapped.Unwrap() {
			if isExplicitModelNotFoundError(nested, requestedModel) {
				return true
			}
		}
	case interface{ Unwrap() error }:
		return isExplicitModelNotFoundError(wrapped.Unwrap(), requestedModel)
	}
	return false
}

func isStructuredModelNotFoundError(message, requestedModel string) bool {
	var payload any
	if errJSON := json.Unmarshal([]byte(strings.TrimSpace(message)), &payload); errJSON != nil {
		return false
	}
	return containsStructuredModelNotFound(payload, requestedModel)
}

func containsStructuredModelNotFound(value any, requestedModel string) bool {
	switch typed := value.(type) {
	case map[string]any:
		notFoundType := false
		exactModelReference := false
		for key, item := range typed {
			text, isString := item.(string)
			if isString {
				switch strings.ToLower(strings.TrimSpace(key)) {
				case "code":
					if isModelNotFoundIdentifier(text) {
						return true
					}
				case "type":
					if isModelNotFoundIdentifier(text) {
						return true
					}
					notFoundType = notFoundType || isNotFoundErrorIdentifier(text)
				case "error", "message", "detail", "error_description", "title":
					if isExplicitModelNotFoundMessage(text, requestedModel) {
						return true
					}
					exactModelReference = exactModelReference || isExactRequestedModelReference(text, requestedModel)
				}
			}
			switch item.(type) {
			case map[string]any, []any:
				if containsStructuredModelNotFound(item, requestedModel) {
					return true
				}
			}
		}
		return notFoundType && exactModelReference
	case []any:
		for _, item := range typed {
			if text, isString := item.(string); isString && isExplicitModelNotFoundMessage(text, requestedModel) {
				return true
			}
			if containsStructuredModelNotFound(item, requestedModel) {
				return true
			}
		}
	}
	return false
}

func isModelNotFoundIdentifier(value string) bool {
	candidate := strings.ToLower(strings.TrimSpace(value))
	if fragment := strings.LastIndex(candidate, "#"); fragment >= 0 && fragment+1 < len(candidate) {
		candidate = candidate[fragment+1:]
	} else {
		if query := strings.Index(candidate, "?"); query >= 0 {
			candidate = candidate[:query]
		}
		candidate = strings.TrimRight(candidate, "/")
		if separator := strings.LastIndexAny(candidate, "/:"); separator >= 0 {
			candidate = candidate[separator+1:]
		}
	}
	normalized := strings.NewReplacer("-", "_", " ", "_").Replace(candidate)
	switch normalized {
	case "model_not_found", "model_not_found_error", "unknown_model", "model_does_not_exist", "model_not_exist":
		return true
	default:
		return false
	}
}

func isNotFoundErrorIdentifier(value string) bool {
	normalized := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(value)))
	return normalized == "not_found" || normalized == "not_found_error"
}

func isExplicitModelNotFoundMessage(message, requestedModel string) bool {
	lower := strings.Trim(strings.ToLower(strings.TrimSpace(message)), " .!;\t\r\n")
	if lower == "" {
		return false
	}
	normalized := strings.NewReplacer("-", "_", " ", "_").Replace(lower)
	if strings.Contains(normalized, "model_not_found") || strings.Contains(normalized, "unknown_model") {
		return true
	}
	for _, prefix := range []string{"no such model", "unknown model"} {
		if lower != prefix && !strings.HasPrefix(lower, prefix+" ") && !strings.HasPrefix(lower, prefix+":") {
			continue
		}
		remainder := strings.TrimSpace(strings.TrimPrefix(lower, prefix))
		remainder = strings.TrimSpace(strings.TrimPrefix(remainder, ":"))
		if remainder == "" {
			return true
		}
		missingSuffix, matches := trimRequestedModelReference(remainder, requestedModel)
		return matches && missingSuffix == ""
	}
	for _, prefix := range []string{"the requested model", "requested model", "the model", "model"} {
		if lower != prefix && !strings.HasPrefix(lower, prefix+" ") && !strings.HasPrefix(lower, prefix+":") {
			continue
		}
		remainder := strings.TrimSpace(strings.TrimPrefix(lower, prefix))
		remainder = strings.TrimSpace(strings.TrimPrefix(remainder, ":"))
		if isMissingModelPhrase(remainder) {
			return true
		}
		missingSuffix, matches := trimRequestedModelReference(remainder, requestedModel)
		return matches && isMissingModelPhrase(missingSuffix)
	}
	return false
}

func isExactRequestedModelReference(message, requestedModel string) bool {
	lower := strings.Trim(strings.ToLower(strings.TrimSpace(message)), " .!;\t\r\n")
	for _, prefix := range []string{"the requested model", "requested model", "the model", "model"} {
		if lower != prefix && !strings.HasPrefix(lower, prefix+" ") && !strings.HasPrefix(lower, prefix+":") {
			continue
		}
		remainder := strings.TrimSpace(strings.TrimPrefix(lower, prefix))
		remainder = strings.TrimSpace(strings.TrimPrefix(remainder, ":"))
		suffix, matches := trimRequestedModelReference(remainder, requestedModel)
		return matches && suffix == ""
	}
	return false
}

func trimRequestedModelReference(value, requestedModel string) (string, bool) {
	model := strings.ToLower(strings.TrimSpace(requestedModel))
	if model == "" {
		return "", false
	}
	for _, candidate := range []string{model, "'" + model + "'", `"` + model + `"`, "`" + model + "`"} {
		if value == candidate {
			return "", true
		}
		if !strings.HasPrefix(value, candidate) {
			continue
		}
		remainder := value[len(candidate):]
		if remainder == "" || strings.ContainsRune(" :,", rune(remainder[0])) {
			return strings.TrimLeft(remainder, " :,"), true
		}
	}
	return "", false
}

func isMissingModelPhrase(value string) bool {
	switch strings.Trim(value, " .!;\t\r\n") {
	case "not found", "was not found", "could not be found", "does not exist", "doesn't exist", "not exist", "is unknown":
		return true
	default:
		return false
	}
}

// isRequestInvalidError returns true if the error represents a client request
// error that should neither rotate nor penalize credentials. Model-support
// errors remain eligible for alternate routing and keep their model-level state.
func isRequestInvalidError(err error) bool {
	if err == nil {
		return false
	}
	if isRequestScopedError(err) {
		return true
	}
	if isCloudflareChallengeError(err) {
		return false
	}
	if isInvalidGrantError(err) {
		return false
	}
	if isModelSupportError(err) {
		return false
	}
	status := statusCodeFromError(err)
	if clienterror.IsRequestFault(status, err) {
		return true
	}
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil && authErr.Message != "" {
		// When authErr.Code is non-empty, Error() formats as "Code: Message" which
		// breaks JSON parsing in clienterror. Re-evaluate against the raw Message body.
		if clienterror.IsRequestFault(status, errors.New(authErr.Message)) {
			return true
		}
	}
	return false
}

func applyAuthFailureState(auth *Auth, resultErr *Error, retryAfter *time.Duration, now time.Time, disableCooling bool) {
	if auth == nil {
		return
	}
	if shouldSkipCredentialCooldown(resultErr) {
		return
	}
	defer func() {
		if disableCooling && auth.NextRetryAfter.IsZero() && auth.Quota.NextRecoverAt.IsZero() {
			auth.Unavailable = false
			auth.Quota.Exceeded = false
		}
	}()
	auth.Unavailable = true
	auth.Status = StatusError
	auth.UpdatedAt = now
	if resultErr != nil {
		auth.LastError = cloneError(resultErr)
		if resultErr.Message != "" {
			auth.StatusMessage = resultErr.Message
		}
	}
	statusCode := statusCodeFromResult(resultErr)
	if isCloudflareChallengeResultError(resultErr) {
		auth.StatusMessage = "cloudflare challenge"
		next, backoffLevel := nextCloudflareCooldown(auth.Quota.BackoffLevel, disableCooling, now)
		applyCooldownFields(&auth.Quota, QuotaState{
			Exceeded:      true,
			Reason:        "cloudflare challenge",
			NextRecoverAt: next,
			BackoffLevel:  backoffLevel,
		})
		auth.NextRetryAfter = next
		return
	}
	if isInvalidGrantResultError(resultErr) {
		auth.StatusMessage = "invalid_grant"
		if disableCooling {
			auth.NextRetryAfter = time.Time{}
		} else {
			auth.NextRetryAfter = now.Add(30 * time.Minute)
		}
		return
	}
	switch statusCode {
	case 401:
		auth.StatusMessage = "unauthorized"
		if disableCooling {
			auth.NextRetryAfter = time.Time{}
		} else {
			auth.NextRetryAfter = now.Add(30 * time.Minute)
		}
	case 402, 403:
		auth.StatusMessage = "payment_required"
		if disableCooling {
			auth.NextRetryAfter = time.Time{}
		} else {
			auth.NextRetryAfter = now.Add(30 * time.Minute)
		}
	case 404:
		auth.StatusMessage = "not_found"
		if disableCooling {
			auth.NextRetryAfter = time.Time{}
		} else {
			auth.NextRetryAfter = now.Add(12 * time.Hour)
		}
	case 429:
		auth.StatusMessage = "quota exhausted"
		auth.Quota.Exceeded = true
		auth.Quota.Reason = "quota"
		var next time.Time
		if !disableCooling {
			if retryAfter != nil {
				next = now.Add(*retryAfter)
			} else {
				next, auth.Quota.BackoffLevel = quotaCooldownAfterFailure(auth.Quota, now)
			}
			if auth.Quota.Exceeded && auth.Quota.NextRecoverAt.After(next) {
				next = auth.Quota.NextRecoverAt
			}
		}
		auth.Quota.NextRecoverAt = next
		auth.NextRetryAfter = next
	case 408, 500, 502, 503, 504:
		auth.StatusMessage = "transient upstream error"
		auth.NextRetryAfter = recoverableFailureRetryAfter(now, disableCooling)
		auth.Unavailable = !auth.NextRetryAfter.IsZero()
	default:
		if auth.StatusMessage == "" {
			auth.StatusMessage = "request failed"
		}
		auth.NextRetryAfter = recoverableFailureRetryAfter(now, disableCooling)
		auth.Unavailable = !auth.NextRetryAfter.IsZero()
	}
	if resultErr != nil && resultErr.Code == ErrorCodeForceCooldown && auth.NextRetryAfter.IsZero() {
		auth.NextRetryAfter = now.Add(transientErrorCooldown)
		auth.Unavailable = true
	}
}

// quotaCooldownAfterFailure returns the recovery deadline and backoff level for
// a quota failure observed at now. Failures that land while a previous quota
// window is still open reuse that window instead of escalating, so a burst of
// concurrent in-flight failures advances the backoff ladder at most once per
// window.
func quotaCooldownAfterFailure(quota QuotaState, now time.Time) (time.Time, int) {
	if quota.NextRecoverAt.After(now) {
		return quota.NextRecoverAt, quota.BackoffLevel
	}
	cooldown, nextLevel := nextQuotaCooldown(quota.BackoffLevel, false)
	var next time.Time
	if cooldown > 0 {
		next = now.Add(cooldown)
	}
	return next, nextLevel
}

// nextQuotaCooldown returns the next cooldown duration and updated backoff level for repeated quota errors.
func nextQuotaCooldown(prevLevel int, disableCooling bool) (time.Duration, int) {
	if prevLevel < 0 {
		prevLevel = 0
	}
	if disableCooling {
		return 0, prevLevel
	}
	cooldown := quotaBackoffBase * time.Duration(1<<prevLevel)
	if cooldown < quotaBackoffBase {
		cooldown = quotaBackoffBase
	}
	if cooldown >= quotaBackoffMax {
		return quotaBackoffMax, prevLevel
	}
	return cooldown, prevLevel + 1
}
