package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// ProviderExecutor defines the contract required by Manager to execute provider calls.
type ProviderExecutor interface {
	// Identifier returns the provider key handled by this executor.
	Identifier() string
	// Execute handles non-streaming execution and returns the provider response payload.
	Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	// ExecuteStream handles streaming execution and returns a channel of provider chunks.
	ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error)
	// Refresh attempts to refresh provider credentials and returns the updated auth state.
	Refresh(ctx context.Context, auth *Auth) (*Auth, error)
	// CountTokens returns the token count for the given request.
	CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
}

// RefreshEvaluator allows runtime state to override refresh decisions.
type RefreshEvaluator interface {
	ShouldRefresh(now time.Time, auth *Auth) bool
}

const (
	refreshCheckInterval  = 5 * time.Second
	refreshPendingBackoff = time.Minute
	refreshFailureBackoff = 5 * time.Minute
	quotaBackoffBase      = time.Second
	quotaBackoffMax       = 30 * time.Minute
)

var quotaCooldownDisabled atomic.Bool

// SetQuotaCooldownDisabled toggles quota cooldown scheduling globally.
func SetQuotaCooldownDisabled(disable bool) {
	quotaCooldownDisabled.Store(disable)
}

// Result captures execution outcome used to adjust auth state.
type Result struct {
	// AuthID references the auth that produced this result.
	AuthID string
	// Provider is copied for convenience when emitting hooks.
	Provider string
	// Model is the upstream model identifier used for the request.
	Model string
	// Success marks whether the execution succeeded.
	Success bool
	// RetryAfter carries a provider supplied retry hint (e.g. 429 retryDelay).
	RetryAfter *time.Duration
	// Error describes the failure when Success is false.
	Error *Error
}

// Selector chooses an auth candidate for execution.
type Selector interface {
	Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error)
}

// Hook captures lifecycle callbacks for observing auth changes.
type Hook interface {
	// OnAuthRegistered fires when a new auth is registered.
	OnAuthRegistered(ctx context.Context, auth *Auth)
	// OnAuthUpdated fires when an existing auth changes state.
	OnAuthUpdated(ctx context.Context, auth *Auth)
	// OnResult fires when execution result is recorded.
	OnResult(ctx context.Context, result Result)
}

// NoopHook provides optional hook defaults.
type NoopHook struct{}

// OnAuthRegistered implements Hook.
func (NoopHook) OnAuthRegistered(context.Context, *Auth) {}

// OnAuthUpdated implements Hook.
func (NoopHook) OnAuthUpdated(context.Context, *Auth) {}

// OnResult implements Hook.
func (NoopHook) OnResult(context.Context, Result) {}

// Manager orchestrates auth lifecycle, selection, execution, and persistence.
type Manager struct {
	store     Store
	executors map[string]ProviderExecutor
	selector  Selector
	hook      Hook
	mu        sync.RWMutex
	auths     map[string]*Auth
	// providerOffsets tracks per-model provider rotation state for multi-provider routing.
	providerOffsets map[string]int

	// Retry controls request retry behavior.
	requestRetry     atomic.Int32
	maxRetryInterval atomic.Int64

	// Optional HTTP RoundTripper provider injected by host.
	rtProvider RoundTripperProvider

	// Auto refresh state
	refreshCancel context.CancelFunc
}

// NewManager constructs a manager with optional custom selector and hook.
func NewManager(store Store, selector Selector, hook Hook) *Manager {
	if selector == nil {
		selector = &RoundRobinSelector{}
	}
	if hook == nil {
		hook = NoopHook{}
	}
	return &Manager{
		store:           store,
		executors:       make(map[string]ProviderExecutor),
		selector:        selector,
		hook:            hook,
		auths:           make(map[string]*Auth),
		providerOffsets: make(map[string]int),
	}
}

// SetStore swaps the underlying persistence store.
func (m *Manager) SetStore(store Store) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store = store
}

// SetRoundTripperProvider register a provider that returns a per-auth RoundTripper.
func (m *Manager) SetRoundTripperProvider(p RoundTripperProvider) {
	m.mu.Lock()
	m.rtProvider = p
	m.mu.Unlock()
}

// SetRetryConfig updates retry attempts and cooldown wait interval.
func (m *Manager) SetRetryConfig(retry int, maxRetryInterval time.Duration) {
	if m == nil {
		return
	}
	if retry < 0 {
		retry = 0
	}
	if maxRetryInterval < 0 {
		maxRetryInterval = 0
	}
	m.requestRetry.Store(int32(retry))
	m.maxRetryInterval.Store(maxRetryInterval.Nanoseconds())
}

// RegisterExecutor registers a provider executor with the manager.
func (m *Manager) RegisterExecutor(executor ProviderExecutor) {
	if executor == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executors[executor.Identifier()] = executor
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
	auth.EnsureIndex()
	if auth.ID == "" {
		auth.ID = uuid.NewString()
	}
	m.mu.Lock()
	m.auths[auth.ID] = auth.Clone()
	m.mu.Unlock()
	_ = m.persist(ctx, auth)
	m.hook.OnAuthRegistered(ctx, auth.Clone())
	return auth.Clone(), nil
}

// Update replaces an existing auth entry and notifies hooks.
func (m *Manager) Update(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth == nil || auth.ID == "" {
		return nil, nil
	}
	m.mu.Lock()
	if existing, ok := m.auths[auth.ID]; ok && existing != nil && !auth.indexAssigned && auth.Index == 0 {
		auth.Index = existing.Index
		auth.indexAssigned = existing.indexAssigned
	}
	auth.EnsureIndex()
	m.auths[auth.ID] = auth.Clone()
	m.mu.Unlock()
	_ = m.persist(ctx, auth)
	m.hook.OnAuthUpdated(ctx, auth.Clone())
	return auth.Clone(), nil
}

// Load resets manager state from the backing store.
func (m *Manager) Load(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.store == nil {
		return nil
	}
	items, err := m.store.List(ctx)
	if err != nil {
		return err
	}
	m.auths = make(map[string]*Auth, len(items))
	for _, auth := range items {
		if auth == nil || auth.ID == "" {
			continue
		}
		auth.EnsureIndex()
		m.auths[auth.ID] = auth.Clone()
	}
	return nil
}

// Execute performs a non-streaming execution using the configured selector and executor.
// It supports multiple providers for the same model and round-robins the starting provider per model.
func (m *Manager) Execute(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	rotated := m.rotateProviders(req.Model, normalized)
	defer m.advanceProviderCursor(req.Model, normalized)

	retryTimes, maxWait := m.retrySettings()
	attempts := retryTimes + 1
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		resp, errExec := m.executeProvidersOnce(ctx, rotated, func(execCtx context.Context, provider string) (cliproxyexecutor.Response, error) {
			return m.executeWithProvider(execCtx, provider, req, opts)
		})
		if errExec == nil {
			return resp, nil
		}
		lastErr = errExec
		wait, shouldRetry := m.shouldRetryAfterError(errExec, attempt, attempts, rotated, req.Model, maxWait)
		if !shouldRetry {
			break
		}
		if errWait := waitForCooldown(ctx, wait); errWait != nil {
			return cliproxyexecutor.Response{}, errWait
		}
	}
	if lastErr != nil {
		return cliproxyexecutor.Response{}, lastErr
	}
	return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// ExecuteCount performs a non-streaming execution using the configured selector and executor.
// It supports multiple providers for the same model and round-robins the starting provider per model.
func (m *Manager) ExecuteCount(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	rotated := m.rotateProviders(req.Model, normalized)
	defer m.advanceProviderCursor(req.Model, normalized)

	retryTimes, maxWait := m.retrySettings()
	attempts := retryTimes + 1
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		resp, errExec := m.executeProvidersOnce(ctx, rotated, func(execCtx context.Context, provider string) (cliproxyexecutor.Response, error) {
			return m.executeCountWithProvider(execCtx, provider, req, opts)
		})
		if errExec == nil {
			return resp, nil
		}
		lastErr = errExec
		wait, shouldRetry := m.shouldRetryAfterError(errExec, attempt, attempts, rotated, req.Model, maxWait)
		if !shouldRetry {
			break
		}
		if errWait := waitForCooldown(ctx, wait); errWait != nil {
			return cliproxyexecutor.Response{}, errWait
		}
	}
	if lastErr != nil {
		return cliproxyexecutor.Response{}, lastErr
	}
	return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// ExecuteStream performs a streaming execution using the configured selector and executor.
// It supports multiple providers for the same model and round-robins the starting provider per model.
func (m *Manager) ExecuteStream(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return nil, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	rotated := m.rotateProviders(req.Model, normalized)
	defer m.advanceProviderCursor(req.Model, normalized)

	retryTimes, maxWait := m.retrySettings()
	attempts := retryTimes + 1
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		chunks, errStream := m.executeStreamProvidersOnce(ctx, rotated, func(execCtx context.Context, provider string) (<-chan cliproxyexecutor.StreamChunk, error) {
			return m.executeStreamWithProvider(execCtx, provider, req, opts)
		})
		if errStream == nil {
			return chunks, nil
		}
		lastErr = errStream
		wait, shouldRetry := m.shouldRetryAfterError(errStream, attempt, attempts, rotated, req.Model, maxWait)
		if !shouldRetry {
			break
		}
		if errWait := waitForCooldown(ctx, wait); errWait != nil {
			return nil, errWait
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
}

func (m *Manager) executeWithProvider(ctx context.Context, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if provider == "" {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "provider identifier is empty"}
	}
	tried := make(map[string]struct{})
	var lastErr error
	for {
		auth, executor, errPick := m.pickNext(ctx, provider, req.Model, opts, tried)
		if errPick != nil {
			if lastErr != nil {
				return cliproxyexecutor.Response{}, lastErr
			}
			return cliproxyexecutor.Response{}, errPick
		}

		accountType, accountInfo := auth.AccountInfo()
		if accountType == "api_key" {
			log.Debugf("Use API key %s for model %s", util.HideAPIKey(accountInfo), req.Model)
		} else if accountType == "oauth" {
			log.Debugf("Use OAuth %s for model %s", accountInfo, req.Model)
		}

		tried[auth.ID] = struct{}{}
		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		resp, errExec := executor.Execute(execCtx, auth, req, opts)
		result := Result{AuthID: auth.ID, Provider: provider, Model: req.Model, Success: errExec == nil}
		if errExec != nil {
			result.Error = &Error{Message: errExec.Error()}
			var se cliproxyexecutor.StatusError
			if errors.As(errExec, &se) && se != nil {
				result.Error.HTTPStatus = se.StatusCode()
			}
			if ra := retryAfterFromError(errExec); ra != nil {
				result.RetryAfter = ra
			}
			m.MarkResult(execCtx, result)
			lastErr = errExec
			continue
		}
		m.MarkResult(execCtx, result)
		return resp, nil
	}
}

func (m *Manager) executeCountWithProvider(ctx context.Context, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if provider == "" {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "provider identifier is empty"}
	}
	tried := make(map[string]struct{})
	var lastErr error
	for {
		auth, executor, errPick := m.pickNext(ctx, provider, req.Model, opts, tried)
		if errPick != nil {
			if lastErr != nil {
				return cliproxyexecutor.Response{}, lastErr
			}
			return cliproxyexecutor.Response{}, errPick
		}

		accountType, accountInfo := auth.AccountInfo()
		if accountType == "api_key" {
			log.Debugf("Use API key %s for model %s", util.HideAPIKey(accountInfo), req.Model)
		} else if accountType == "oauth" {
			log.Debugf("Use OAuth %s for model %s", accountInfo, req.Model)
		}

		tried[auth.ID] = struct{}{}
		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		resp, errExec := executor.CountTokens(execCtx, auth, req, opts)
		result := Result{AuthID: auth.ID, Provider: provider, Model: req.Model, Success: errExec == nil}
		if errExec != nil {
			result.Error = &Error{Message: errExec.Error()}
			var se cliproxyexecutor.StatusError
			if errors.As(errExec, &se) && se != nil {
				result.Error.HTTPStatus = se.StatusCode()
			}
			if ra := retryAfterFromError(errExec); ra != nil {
				result.RetryAfter = ra
			}
			m.MarkResult(execCtx, result)
			lastErr = errExec
			continue
		}
		m.MarkResult(execCtx, result)
		return resp, nil
	}
}

func (m *Manager) executeStreamWithProvider(ctx context.Context, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	if provider == "" {
		return nil, &Error{Code: "provider_not_found", Message: "provider identifier is empty"}
	}
	tried := make(map[string]struct{})
	var lastErr error
	for {
		auth, executor, errPick := m.pickNext(ctx, provider, req.Model, opts, tried)
		if errPick != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, errPick
		}

		accountType, accountInfo := auth.AccountInfo()
		if accountType == "api_key" {
			log.Debugf("Use API key %s for model %s", util.HideAPIKey(accountInfo), req.Model)
		} else if accountType == "oauth" {
			log.Debugf("Use OAuth %s for model %s", accountInfo, req.Model)
		}

		tried[auth.ID] = struct{}{}
		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		chunks, errStream := executor.ExecuteStream(execCtx, auth, req, opts)
		if errStream != nil {
			rerr := &Error{Message: errStream.Error()}
			var se cliproxyexecutor.StatusError
			if errors.As(errStream, &se) && se != nil {
				rerr.HTTPStatus = se.StatusCode()
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: req.Model, Success: false, Error: rerr}
			result.RetryAfter = retryAfterFromError(errStream)
			m.MarkResult(execCtx, result)
			lastErr = errStream
			continue
		}
		out := make(chan cliproxyexecutor.StreamChunk)
		go func(streamCtx context.Context, streamAuth *Auth, streamProvider string, streamChunks <-chan cliproxyexecutor.StreamChunk) {
			defer close(out)
			var failed bool
			for chunk := range streamChunks {
				if chunk.Err != nil && !failed {
					failed = true
					rerr := &Error{Message: chunk.Err.Error()}
					var se cliproxyexecutor.StatusError
					if errors.As(chunk.Err, &se) && se != nil {
						rerr.HTTPStatus = se.StatusCode()
					}
					m.MarkResult(streamCtx, Result{AuthID: streamAuth.ID, Provider: streamProvider, Model: req.Model, Success: false, Error: rerr})
				}
				out <- chunk
			}
			if !failed {
				m.MarkResult(streamCtx, Result{AuthID: streamAuth.ID, Provider: streamProvider, Model: req.Model, Success: true})
			}
		}(execCtx, auth.Clone(), provider, chunks)
		return out, nil
	}
}

func (m *Manager) normalizeProviders(providers []string) []string {
	if len(providers) == 0 {
		return nil
	}
	result := make([]string, 0, len(providers))
	seen := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		p := strings.TrimSpace(strings.ToLower(provider))
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		result = append(result, p)
	}
	return result
}

func (m *Manager) rotateProviders(model string, providers []string) []string {
	if len(providers) == 0 {
		return nil
	}
	m.mu.RLock()
	offset := m.providerOffsets[model]
	m.mu.RUnlock()
	if len(providers) > 0 {
		offset %= len(providers)
	}
	if offset < 0 {
		offset = 0
	}
	if offset == 0 {
		return providers
	}
	rotated := make([]string, 0, len(providers))
	rotated = append(rotated, providers[offset:]...)
	rotated = append(rotated, providers[:offset]...)
	return rotated
}

func (m *Manager) advanceProviderCursor(model string, providers []string) {
	if len(providers) == 0 {
		m.mu.Lock()
		delete(m.providerOffsets, model)
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	current := m.providerOffsets[model]
	m.providerOffsets[model] = (current + 1) % len(providers)
	m.mu.Unlock()
}

func (m *Manager) retrySettings() (int, time.Duration) {
	if m == nil {
		return 0, 0
	}
	return int(m.requestRetry.Load()), time.Duration(m.maxRetryInterval.Load())
}

func (m *Manager) closestCooldownWait(providers []string, model string) (time.Duration, bool) {
	if m == nil || len(providers) == 0 {
		return 0, false
	}
	now := time.Now()
	providerSet := make(map[string]struct{}, len(providers))
	for i := range providers {
		key := strings.TrimSpace(strings.ToLower(providers[i]))
		if key == "" {
			continue
		}
		providerSet[key] = struct{}{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var (
		found   bool
		minWait time.Duration
	)
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		providerKey := strings.TrimSpace(strings.ToLower(auth.Provider))
		if _, ok := providerSet[providerKey]; !ok {
			continue
		}
		blocked, reason, next := isAuthBlockedForModel(auth, model, now)
		if !blocked || next.IsZero() || reason == blockReasonDisabled {
			continue
		}
		wait := next.Sub(now)
		if wait < 0 {
			continue
		}
		if !found || wait < minWait {
			minWait = wait
			found = true
		}
	}
	return minWait, found
}

func (m *Manager) shouldRetryAfterError(err error, attempt, maxAttempts int, providers []string, model string, maxWait time.Duration) (time.Duration, bool) {
	if err == nil || attempt >= maxAttempts-1 {
		return 0, false
	}
	if maxWait <= 0 {
		return 0, false
	}
	if status := statusCodeFromError(err); status == http.StatusOK {
		return 0, false
	}
	wait, found := m.closestCooldownWait(providers, model)
	if !found || wait > maxWait {
		return 0, false
	}
	return wait, true
}

func waitForCooldown(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (m *Manager) executeProvidersOnce(ctx context.Context, providers []string, fn func(context.Context, string) (cliproxyexecutor.Response, error)) (cliproxyexecutor.Response, error) {
	if len(providers) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	var lastErr error
	for _, provider := range providers {
		resp, errExec := fn(ctx, provider)
		if errExec == nil {
			return resp, nil
		}
		lastErr = errExec
	}
	if lastErr != nil {
		return cliproxyexecutor.Response{}, lastErr
	}
	return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

func (m *Manager) executeStreamProvidersOnce(ctx context.Context, providers []string, fn func(context.Context, string) (<-chan cliproxyexecutor.StreamChunk, error)) (<-chan cliproxyexecutor.StreamChunk, error) {
	if len(providers) == 0 {
		return nil, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	var lastErr error
	for _, provider := range providers {
		chunks, errExec := fn(ctx, provider)
		if errExec == nil {
			return chunks, nil
		}
		lastErr = errExec
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// MarkResult records an execution result and notifies hooks.
func (m *Manager) MarkResult(ctx context.Context, result Result) {
	if result.AuthID == "" {
		return
	}

	shouldResumeModel := false
	shouldSuspendModel := false
	suspendReason := ""
	clearModelQuota := false
	setModelQuota := false

	m.mu.Lock()
	if auth, ok := m.auths[result.AuthID]; ok && auth != nil {
		now := time.Now()

		if result.Success {
			if result.Model != "" {
				state := ensureModelState(auth, result.Model)
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
			if result.Model != "" {
				state := ensureModelState(auth, result.Model)
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
				switch statusCode {
				case 401:
					next := now.Add(30 * time.Minute)
					state.NextRetryAfter = next
					suspendReason = "unauthorized"
					shouldSuspendModel = true
				case 402, 403:
					next := now.Add(30 * time.Minute)
					state.NextRetryAfter = next
					suspendReason = "payment_required"
					shouldSuspendModel = true
				case 404:
					next := now.Add(12 * time.Hour)
					state.NextRetryAfter = next
					suspendReason = "not_found"
					shouldSuspendModel = true
				case 429:
					var next time.Time
					backoffLevel := state.Quota.BackoffLevel
					if result.RetryAfter != nil {
						next = now.Add(*result.RetryAfter)
					} else {
						cooldown, nextLevel := nextQuotaCooldown(backoffLevel)
						if cooldown > 0 {
							next = now.Add(cooldown)
						}
						backoffLevel = nextLevel
					}
					state.NextRetryAfter = next
					state.Quota = QuotaState{
						Exceeded:      true,
						Reason:        "quota",
						NextRecoverAt: next,
						BackoffLevel:  backoffLevel,
					}
					suspendReason = "quota"
					shouldSuspendModel = true
					setModelQuota = true
				case 408, 500, 502, 503, 504:
					next := now.Add(1 * time.Minute)
					state.NextRetryAfter = next
				default:
					state.NextRetryAfter = time.Time{}
				}

				auth.Status = StatusError
				auth.UpdatedAt = now
				updateAggregatedAvailability(auth, now)
			} else {
				applyAuthFailureState(auth, result.Error, result.RetryAfter, now)
			}
		}

		_ = m.persist(ctx, auth)
	}
	m.mu.Unlock()

	if clearModelQuota && result.Model != "" {
		registry.GetGlobalRegistry().ClearModelQuotaExceeded(result.AuthID, result.Model)
	}
	if setModelQuota && result.Model != "" {
		registry.GetGlobalRegistry().SetModelQuotaExceeded(result.AuthID, result.Model)
	}
	if shouldResumeModel {
		registry.GetGlobalRegistry().ResumeClientModel(result.AuthID, result.Model)
	} else if shouldSuspendModel {
		registry.GetGlobalRegistry().SuspendClientModel(result.AuthID, result.Model, suspendReason)
	}

	m.hook.OnResult(ctx, result)
}

func ensureModelState(auth *Auth, model string) *ModelState {
	if auth == nil || model == "" {
		return nil
	}
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

func resetModelState(state *ModelState, now time.Time) {
	if state == nil {
		return
	}
	state.Unavailable = false
	state.Status = StatusActive
	state.StatusMessage = ""
	state.NextRetryAfter = time.Time{}
	state.LastError = nil
	state.Quota = QuotaState{}
	state.UpdatedAt = now
}

func updateAggregatedAvailability(auth *Auth, now time.Time) {
	if auth == nil || len(auth.ModelStates) == 0 {
		return
	}
	allUnavailable := true
	earliestRetry := time.Time{}
	quotaExceeded := false
	quotaRecover := time.Time{}
	maxBackoffLevel := 0
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		stateUnavailable := false
		if state.Status == StatusDisabled {
			stateUnavailable = true
		} else if state.Unavailable {
			if state.NextRetryAfter.IsZero() {
				stateUnavailable = true
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
	auth.Unavailable = allUnavailable
	if allUnavailable {
		auth.NextRetryAfter = earliestRetry
	} else {
		auth.NextRetryAfter = time.Time{}
	}
	if quotaExceeded {
		auth.Quota.Exceeded = true
		auth.Quota.Reason = "quota"
		auth.Quota.NextRecoverAt = quotaRecover
		auth.Quota.BackoffLevel = maxBackoffLevel
	} else {
		auth.Quota.Exceeded = false
		auth.Quota.Reason = ""
		auth.Quota.NextRecoverAt = time.Time{}
		auth.Quota.BackoffLevel = 0
	}
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

func retryAfterFromError(err error) *time.Duration {
	if err == nil {
		return nil
	}
	type retryAfterProvider interface {
		RetryAfter() *time.Duration
	}
	rap, ok := err.(retryAfterProvider)
	if !ok || rap == nil {
		return nil
	}
	retryAfter := rap.RetryAfter()
	if retryAfter == nil {
		return nil
	}
	val := *retryAfter
	return &val
}

func statusCodeFromResult(err *Error) int {
	if err == nil {
		return 0
	}
	return err.StatusCode()
}

func applyAuthFailureState(auth *Auth, resultErr *Error, retryAfter *time.Duration, now time.Time) {
	if auth == nil {
		return
	}
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
	switch statusCode {
	case 401:
		auth.StatusMessage = "unauthorized"
		auth.NextRetryAfter = now.Add(30 * time.Minute)
	case 402, 403:
		auth.StatusMessage = "payment_required"
		auth.NextRetryAfter = now.Add(30 * time.Minute)
	case 404:
		auth.StatusMessage = "not_found"
		auth.NextRetryAfter = now.Add(12 * time.Hour)
	case 429:
		auth.StatusMessage = "quota exhausted"
		auth.Quota.Exceeded = true
		auth.Quota.Reason = "quota"
		var next time.Time
		if retryAfter != nil {
			next = now.Add(*retryAfter)
		} else {
			cooldown, nextLevel := nextQuotaCooldown(auth.Quota.BackoffLevel)
			if cooldown > 0 {
				next = now.Add(cooldown)
			}
			auth.Quota.BackoffLevel = nextLevel
		}
		auth.Quota.NextRecoverAt = next
		auth.NextRetryAfter = next
	case 408, 500, 502, 503, 504:
		auth.StatusMessage = "transient upstream error"
		auth.NextRetryAfter = now.Add(1 * time.Minute)
	default:
		if auth.StatusMessage == "" {
			auth.StatusMessage = "request failed"
		}
	}
}

// nextQuotaCooldown returns the next cooldown duration and updated backoff level for repeated quota errors.
func nextQuotaCooldown(prevLevel int) (time.Duration, int) {
	if prevLevel < 0 {
		prevLevel = 0
	}
	if quotaCooldownDisabled.Load() {
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

// List returns all auth entries currently known by the manager.
func (m *Manager) List() []*Auth {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]*Auth, 0, len(m.auths))
	for _, auth := range m.auths {
		list = append(list, auth.Clone())
	}
	return list
}

// GetByID retrieves an auth entry by its ID.

func (m *Manager) GetByID(id string) (*Auth, bool) {
	if id == "" {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	auth, ok := m.auths[id]
	if !ok {
		return nil, false
	}
	return auth.Clone(), true
}

func (m *Manager) pickNext(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, error) {
	m.mu.RLock()
	executor, okExecutor := m.executors[provider]
	if !okExecutor {
		m.mu.RUnlock()
		return nil, nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	candidates := make([]*Auth, 0, len(m.auths))
	modelKey := strings.TrimSpace(model)
	registryRef := registry.GetGlobalRegistry()
	for _, candidate := range m.auths {
		if candidate.Provider != provider || candidate.Disabled {
			continue
		}
		if _, used := tried[candidate.ID]; used {
			continue
		}
		if modelKey != "" && registryRef != nil && !registryRef.ClientSupportsModel(candidate.ID, modelKey) {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		m.mu.RUnlock()
		return nil, nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	selected, errPick := m.selector.Pick(ctx, provider, model, opts, candidates)
	if errPick != nil {
		m.mu.RUnlock()
		return nil, nil, errPick
	}
	if selected == nil {
		m.mu.RUnlock()
		return nil, nil, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}
	authCopy := selected.Clone()
	m.mu.RUnlock()
	return authCopy, executor, nil
}

func (m *Manager) persist(ctx context.Context, auth *Auth) error {
	if m.store == nil || auth == nil {
		return nil
	}
	if auth.Attributes != nil {
		if v := strings.ToLower(strings.TrimSpace(auth.Attributes["runtime_only"])); v == "true" {
			return nil
		}
	}
	// Skip persistence when metadata is absent (e.g., runtime-only auths).
	if auth.Metadata == nil {
		return nil
	}
	_, err := m.store.Save(ctx, auth)
	return err
}

// StartAutoRefresh launches a background loop that evaluates auth freshness
// every few seconds and triggers refresh operations when required.
// Only one loop is kept alive; starting a new one cancels the previous run.
func (m *Manager) StartAutoRefresh(parent context.Context, interval time.Duration) {
	if interval <= 0 || interval > refreshCheckInterval {
		interval = refreshCheckInterval
	} else {
		interval = refreshCheckInterval
	}
	if m.refreshCancel != nil {
		m.refreshCancel()
		m.refreshCancel = nil
	}
	ctx, cancel := context.WithCancel(parent)
	m.refreshCancel = cancel
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		m.checkRefreshes(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.checkRefreshes(ctx)
			}
		}
	}()
}

// StopAutoRefresh cancels the background refresh loop, if running.
func (m *Manager) StopAutoRefresh() {
	if m.refreshCancel != nil {
		m.refreshCancel()
		m.refreshCancel = nil
	}
}

func (m *Manager) checkRefreshes(ctx context.Context) {
	// log.Debugf("checking refreshes")
	now := time.Now()
	snapshot := m.snapshotAuths()
	for _, a := range snapshot {
		typ, _ := a.AccountInfo()
		if typ != "api_key" {
			if !m.shouldRefresh(a, now) {
				continue
			}
			log.Debugf("checking refresh for %s, %s, %s", a.Provider, a.ID, typ)

			if exec := m.executorFor(a.Provider); exec == nil {
				continue
			}
			if !m.markRefreshPending(a.ID, now) {
				continue
			}
			go m.refreshAuth(ctx, a.ID)
		}
	}
}

func (m *Manager) snapshotAuths() []*Auth {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Auth, 0, len(m.auths))
	for _, a := range m.auths {
		out = append(out, a.Clone())
	}
	return out
}

func (m *Manager) shouldRefresh(a *Auth, now time.Time) bool {
	if a == nil || a.Disabled {
		return false
	}
	if !a.NextRefreshAfter.IsZero() && now.Before(a.NextRefreshAfter) {
		return false
	}
	if evaluator, ok := a.Runtime.(RefreshEvaluator); ok && evaluator != nil {
		return evaluator.ShouldRefresh(now, a)
	}

	lastRefresh := a.LastRefreshedAt
	if lastRefresh.IsZero() {
		if ts, ok := authLastRefreshTimestamp(a); ok {
			lastRefresh = ts
		}
	}

	expiry, hasExpiry := a.ExpirationTime()

	if interval := authPreferredInterval(a); interval > 0 {
		if hasExpiry && !expiry.IsZero() {
			if !expiry.After(now) {
				return true
			}
			if expiry.Sub(now) <= interval {
				return true
			}
		}
		if lastRefresh.IsZero() {
			return true
		}
		return now.Sub(lastRefresh) >= interval
	}

	provider := strings.ToLower(a.Provider)
	lead := ProviderRefreshLead(provider, a.Runtime)
	if lead == nil {
		return false
	}
	if *lead <= 0 {
		if hasExpiry && !expiry.IsZero() {
			return now.After(expiry)
		}
		return false
	}
	if hasExpiry && !expiry.IsZero() {
		return time.Until(expiry) <= *lead
	}
	if !lastRefresh.IsZero() {
		return now.Sub(lastRefresh) >= *lead
	}
	return true
}

func authPreferredInterval(a *Auth) time.Duration {
	if a == nil {
		return 0
	}
	if d := durationFromMetadata(a.Metadata, "refresh_interval_seconds", "refreshIntervalSeconds", "refresh_interval", "refreshInterval"); d > 0 {
		return d
	}
	if d := durationFromAttributes(a.Attributes, "refresh_interval_seconds", "refreshIntervalSeconds", "refresh_interval", "refreshInterval"); d > 0 {
		return d
	}
	return 0
}

func durationFromMetadata(meta map[string]any, keys ...string) time.Duration {
	if len(meta) == 0 {
		return 0
	}
	for _, key := range keys {
		if val, ok := meta[key]; ok {
			if dur := parseDurationValue(val); dur > 0 {
				return dur
			}
		}
	}
	return 0
}

func durationFromAttributes(attrs map[string]string, keys ...string) time.Duration {
	if len(attrs) == 0 {
		return 0
	}
	for _, key := range keys {
		if val, ok := attrs[key]; ok {
			if dur := parseDurationString(val); dur > 0 {
				return dur
			}
		}
	}
	return 0
}

func parseDurationValue(val any) time.Duration {
	switch v := val.(type) {
	case time.Duration:
		if v <= 0 {
			return 0
		}
		return v
	case int:
		if v <= 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case int32:
		if v <= 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case int64:
		if v <= 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case uint:
		if v == 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case uint32:
		if v == 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case uint64:
		if v == 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case float32:
		if v <= 0 {
			return 0
		}
		return time.Duration(float64(v) * float64(time.Second))
	case float64:
		if v <= 0 {
			return 0
		}
		return time.Duration(v * float64(time.Second))
	case json.Number:
		if i, err := v.Int64(); err == nil {
			if i <= 0 {
				return 0
			}
			return time.Duration(i) * time.Second
		}
		if f, err := v.Float64(); err == nil && f > 0 {
			return time.Duration(f * float64(time.Second))
		}
	case string:
		return parseDurationString(v)
	}
	return 0
}

func parseDurationString(raw string) time.Duration {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	if dur, err := time.ParseDuration(s); err == nil && dur > 0 {
		return dur
	}
	if secs, err := strconv.ParseFloat(s, 64); err == nil && secs > 0 {
		return time.Duration(secs * float64(time.Second))
	}
	return 0
}

func authLastRefreshTimestamp(a *Auth) (time.Time, bool) {
	if a == nil {
		return time.Time{}, false
	}
	if a.Metadata != nil {
		if ts, ok := lookupMetadataTime(a.Metadata, "last_refresh", "lastRefresh", "last_refreshed_at", "lastRefreshedAt"); ok {
			return ts, true
		}
	}
	if a.Attributes != nil {
		for _, key := range []string{"last_refresh", "lastRefresh", "last_refreshed_at", "lastRefreshedAt"} {
			if val := strings.TrimSpace(a.Attributes[key]); val != "" {
				if ts, ok := parseTimeValue(val); ok {
					return ts, true
				}
			}
		}
	}
	return time.Time{}, false
}

func lookupMetadataTime(meta map[string]any, keys ...string) (time.Time, bool) {
	for _, key := range keys {
		if val, ok := meta[key]; ok {
			if ts, ok1 := parseTimeValue(val); ok1 {
				return ts, true
			}
		}
	}
	return time.Time{}, false
}

func (m *Manager) markRefreshPending(id string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	auth, ok := m.auths[id]
	if !ok || auth == nil || auth.Disabled {
		return false
	}
	if !auth.NextRefreshAfter.IsZero() && now.Before(auth.NextRefreshAfter) {
		return false
	}
	auth.NextRefreshAfter = now.Add(refreshPendingBackoff)
	m.auths[id] = auth
	return true
}

func (m *Manager) refreshAuth(ctx context.Context, id string) {
	m.mu.RLock()
	auth := m.auths[id]
	var exec ProviderExecutor
	if auth != nil {
		exec = m.executors[auth.Provider]
	}
	m.mu.RUnlock()
	if auth == nil || exec == nil {
		return
	}
	cloned := auth.Clone()
	updated, err := exec.Refresh(ctx, cloned)
	log.Debugf("refreshed %s, %s, %v", auth.Provider, auth.ID, err)
	now := time.Now()
	if err != nil {
		m.mu.Lock()
		if current := m.auths[id]; current != nil {
			current.NextRefreshAfter = now.Add(refreshFailureBackoff)
			current.LastError = &Error{Message: err.Error()}
			m.auths[id] = current
		}
		m.mu.Unlock()
		return
	}
	if updated == nil {
		updated = cloned
	}
	// Preserve runtime created by the executor during Refresh.
	// If executor didn't set one, fall back to the previous runtime.
	if updated.Runtime == nil {
		updated.Runtime = auth.Runtime
	}
	updated.LastRefreshedAt = now
	updated.NextRefreshAfter = time.Time{}
	updated.LastError = nil
	updated.UpdatedAt = now
	_, _ = m.Update(ctx, updated)
}

func (m *Manager) executorFor(provider string) ProviderExecutor {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.executors[provider]
}

// roundTripperContextKey is an unexported context key type to avoid collisions.
type roundTripperContextKey struct{}

// roundTripperFor retrieves an HTTP RoundTripper for the given auth if a provider is registered.
func (m *Manager) roundTripperFor(auth *Auth) http.RoundTripper {
	m.mu.RLock()
	p := m.rtProvider
	m.mu.RUnlock()
	if p == nil || auth == nil {
		return nil
	}
	return p.RoundTripperFor(auth)
}

// RoundTripperProvider defines a minimal provider of per-auth HTTP transports.
type RoundTripperProvider interface {
	RoundTripperFor(auth *Auth) http.RoundTripper
}

// RequestPreparer is an optional interface that provider executors can implement
// to mutate outbound HTTP requests with provider credentials.
type RequestPreparer interface {
	PrepareRequest(req *http.Request, auth *Auth) error
}

// InjectCredentials delegates per-provider HTTP request preparation when supported.
// If the registered executor for the auth provider implements RequestPreparer,
// it will be invoked to modify the request (e.g., add headers).
func (m *Manager) InjectCredentials(req *http.Request, authID string) error {
	if req == nil || authID == "" {
		return nil
	}
	m.mu.RLock()
	a := m.auths[authID]
	var exec ProviderExecutor
	if a != nil {
		exec = m.executors[a.Provider]
	}
	m.mu.RUnlock()
	if a == nil || exec == nil {
		return nil
	}
	if p, ok := exec.(RequestPreparer); ok && p != nil {
		return p.PrepareRequest(req, a)
	}
	return nil
}
