package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/ojson"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const defaultFallbackExhaustionCooldown = 15 * time.Minute

// FallbackChainConductor retains only candidate-level exhaustion, never a failed
// request's credential exclusions. Its zero value is ready for concurrent use.
// Each request exhausts a candidate's eligible credential pool before advancing;
// streaming fallback is limited to bootstrap, before any payload is committed.
// Request envelopes stay in their source protocol so the existing translators
// and canonical thinking pipeline adapt them for each selected executor.
type FallbackChainConductor struct {
	mu     sync.Mutex
	config *internalconfig.Config
	chains map[string]*fallbackChainState
}

type fallbackChainState struct {
	cooldowns map[int]time.Time
	preferred int
}

type fallbackChainAttemptKey struct{}

type fallbackChainAttempt struct {
	// Execution and bootstrap observation are synchronous. Once a stream is
	// returned, no further chain routing or observation is performed.
	failures map[string]error
	eligible map[string]struct{}
}

func fallbackAttempt(ctx context.Context) *fallbackChainAttempt {
	if ctx == nil {
		return nil
	}
	attempt, _ := ctx.Value(fallbackChainAttemptKey{}).(*fallbackChainAttempt)
	return attempt
}

func recordFallbackFailure(ctx context.Context, auth *Auth, err error) {
	if attempt := fallbackAttempt(ctx); attempt != nil && auth != nil {
		if attempt.eligible == nil {
			attempt.eligible = make(map[string]struct{})
		}
		attempt.eligible[auth.ID] = struct{}{}
		attempt.failures[auth.ID] = err
	}
}

func fallbackExecutionBoundaryError(ctx context.Context, err error) error {
	if fallbackAttempt(ctx) != nil && isRequestStopError(err) {
		return err
	}
	return unwrapExecutionBoundaryError(err)
}

func (m *Manager) fallbackChainForRequest(providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (string, internalconfig.FallbackChainConfig, bool) {
	cfg := m.runtimeConfigSnapshot()
	if cfg == nil || len(cfg.FallbackChain) == 0 {
		return "", internalconfig.FallbackChainConfig{}, false
	}
	model := authSelectionModelFromOptions(opts, req.Model)
	// A provider-qualified entry is more specific than a plain model entry,
	// but may only be inferred when the inbound route has one provider.
	if len(providers) == 1 {
		key := strings.ToLower(strings.TrimSpace(providers[0])) + ":" + model
		if chain, ok := cfg.FallbackChain[key]; ok {
			return key, chain, true
		}
	}
	if chain, ok := cfg.FallbackChain[model]; ok {
		return model, chain, true
	}
	return "", internalconfig.FallbackChainConfig{}, false
}

func (c *FallbackChainConductor) stateLocked(cfg *internalconfig.Config, route string) *fallbackChainState {
	// Reloading config invalidates remembered preferences and cooldowns and
	// bounds retained state to routes in the current configuration snapshot.
	if c.config != cfg {
		c.config = cfg
		c.chains = make(map[string]*fallbackChainState)
	}
	state := c.chains[route]
	if state == nil {
		state = &fallbackChainState{cooldowns: make(map[int]time.Time)}
		c.chains[route] = state
	}
	return state
}

func (c *FallbackChainConductor) start(cfg *internalconfig.Config, route string, sticky bool) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.stateLocked(cfg, route)
	if sticky {
		return 0
	}
	return state.preferred
}

func (c *FallbackChainConductor) cooling(cfg *internalconfig.Config, route string, index int, now time.Time) (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.stateLocked(cfg, route)
	until := state.cooldowns[index]
	if until.After(now) {
		return until.Sub(now), true
	}
	delete(state.cooldowns, index)
	return 0, false
}

func (c *FallbackChainConductor) finish(cfg *internalconfig.Config, route string, index int, cooldown time.Duration, success bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.config != cfg {
		return
	}
	state := c.stateLocked(cfg, route)
	if success {
		delete(state.cooldowns, index)
		state.preferred = index
	} else if cooldown > 0 {
		until := time.Now().Add(cooldown)
		if until.After(state.cooldowns[index]) {
			state.cooldowns[index] = until
		}
	}
}

// executeFallbackChain shares ordering and exhaustion rules across execution,
// token counting, and streaming without changing their response contracts.
func executeFallbackChain[T any](m *Manager, ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, execute func(context.Context, []string, cliproxyexecutor.Request, cliproxyexecutor.Options) (T, error)) (T, error) {
	var zero T
	route, chain, configured := m.fallbackChainForRequest(providers, req, opts)
	if !configured {
		return execute(ctx, providers, req, opts)
	}
	if len(chain.Candidates) == 0 {
		return zero, &Error{Code: "invalid_fallback_chain", Message: "fallback chain has no candidates", HTTPStatus: http.StatusBadRequest}
	}
	cooldown := defaultFallbackExhaustionCooldown
	if chain.ExhaustionCooldown != "" {
		parsed, err := time.ParseDuration(chain.ExhaustionCooldown)
		if err != nil || parsed <= 0 {
			return zero, &Error{Code: "invalid_fallback_chain", Message: "fallback exhaustion cooldown must be a positive duration", HTTPStatus: http.StatusBadRequest}
		}
		cooldown = parsed
	}
	for _, candidate := range chain.Candidates {
		if strings.TrimSpace(candidate.Provider) == "" || strings.TrimSpace(candidate.Model) == "" {
			return zero, &Error{Code: "invalid_fallback_chain", Message: "fallback candidates require a provider and model", HTTPStatus: http.StatusBadRequest}
		}
	}
	cfg := m.runtimeConfigSnapshot()
	start := m.fallbackChain.start(cfg, route, chain.StickyPrimary)
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	cacheExhaustion := pinnedAuthIDFromMetadata(opts.Metadata) == "" && len(eligibility.excludedAuthIDs) == 0 && eligibility.requiredKind == "" && eligibility.credentialPolicy == "" && !eligibility.disallowFreeAuth
	var lastErr error
	var shortestCooldown time.Duration
	for offset := range chain.Candidates {
		if ctx != nil && ctx.Err() != nil {
			return zero, ctx.Err()
		}
		index := (start + offset) % len(chain.Candidates)
		if wait, cooling := m.fallbackChain.cooling(cfg, route, index, time.Now()); cooling {
			if shortestCooldown == 0 || wait < shortestCooldown {
				shortestCooldown = wait
			}
			continue
		}
		candidate := chain.Candidates[index]
		candidate.Provider = strings.ToLower(strings.TrimSpace(candidate.Provider))
		candidate.Model = strings.TrimSpace(candidate.Model)
		candidateReq, candidateOpts, errAdapt := adaptFallbackEnvelope(req, opts, candidate)
		if errAdapt != nil {
			return zero, errAdapt
		}
		attempt := m.newFallbackAttempt(ctx, candidate, candidateOpts)
		attemptCtx := context.WithValue(ctx, fallbackChainAttemptKey{}, attempt)
		response, err := execute(attemptCtx, []string{candidate.Provider}, candidateReq, candidateOpts)
		if err == nil {
			m.fallbackChain.finish(cfg, route, index, 0, true)
			return response, nil
		}
		lastErr = err
		if isRequestTerminatedError(err) || isRequestStopError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return zero, unwrapExecutionBoundaryError(err)
		}
		if !m.fallbackPoolExhausted(attempt, candidate) {
			return zero, err
		}
		// A constrained request may exhaust its eligible subset without exhausting
		// the shared candidate pool. Do not cool healthy credentials for others.
		if cacheExhaustion {
			m.fallbackChain.finish(cfg, route, index, cooldown, false)
		}
	}
	if lastErr != nil {
		return zero, lastErr
	}
	return zero, &fallbackChainCooldownError{wait: shortestCooldown}
}

type fallbackChainCooldownError struct{ wait time.Duration }

func (e *fallbackChainCooldownError) Error() string {
	return "all fallback candidates are in exhaustion cooldown"
}
func (e *fallbackChainCooldownError) StatusCode() int            { return http.StatusTooManyRequests }
func (e *fallbackChainCooldownError) RetryAfter() *time.Duration { value := e.wait; return &value }

func adaptFallbackEnvelope(req cliproxyexecutor.Request, opts cliproxyexecutor.Options, candidate internalconfig.FallbackCandidate) (cliproxyexecutor.Request, cliproxyexecutor.Options, error) {
	opts = ensureRequestedModelMetadata(opts, req.Model)
	// Selection and execution must both use the candidate model, not an alias
	// override left over from the primary route. Keep RequestedModel for usage.
	opts.Metadata[cliproxyexecutor.AuthSelectionModelMetadataKey] = candidate.Model
	if opts.SourceFormat == "" {
		opts.SourceFormat = req.Format
	}
	// Executors translate from SourceFormat and translate back to ResponseFormat.
	// Do not relabel a source payload as the candidate's target protocol: that
	// would bypass reasoning/thinking conversion and corrupt response mapping.
	if req.Format != "" && opts.SourceFormat != "" && req.Format != opts.SourceFormat {
		if !sdktranslator.HasRequestTransformer(req.Format, opts.SourceFormat) {
			return req, opts, &Error{Code: "fallback_translation_unavailable", Message: "cannot restore the fallback request source envelope", HTTPStatus: http.StatusBadRequest}
		}
		req.Payload = sdktranslator.TranslateRequest(req.Format, opts.SourceFormat, candidate.Model, req.Payload, opts.Stream)
	}
	if len(req.Payload) > 0 {
		if !ojson.ValidBytes(req.Payload) || !ojson.ParseBytes(req.Payload).IsObject() {
			return req, opts, &Error{Code: "invalid_request", Message: "fallback request payload must be a JSON object", HTTPStatus: http.StatusBadRequest}
		}
		if model := ojson.GetBytes(req.Payload, "model"); model.Exists() && model.String() != candidate.Model {
			var err error
			req.Payload, err = ojson.SetBytes(req.Payload, "model", candidate.Model)
			if err != nil {
				return req, opts, err
			}
		}
	}
	req.Model = candidate.Model
	req.Format = opts.SourceFormat
	return req, opts, nil
}

func (m *Manager) newFallbackAttempt(ctx context.Context, candidate internalconfig.FallbackCandidate, opts cliproxyexecutor.Options) *fallbackChainAttempt {
	attempt := &fallbackChainAttempt{
		failures: make(map[string]error),
		eligible: make(map[string]struct{}),
	}
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	pinned := pinnedAuthIDFromMetadata(opts.Metadata)
	now := time.Now()
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, auth := range m.auths {
		if auth.Disabled || auth.Status == StatusDisabled || !eligibility.allows(auth) || (pinned != "" && auth.ID != pinned) || !strings.EqualFold(executorKeyFromAuth(auth), candidate.Provider) || !m.authSupportsRouteModel(registry.GetGlobalRegistry(), auth, candidate.Model) {
			continue
		}
		model := m.selectionModelKeyForAuth(auth, candidate.Model)
		if blocked, _, until := isAuthBlockedForModel(auth, model, now); blocked {
			lastErr := auth.LastError
			for stateModel, state := range auth.ModelStates {
				if state != nil && canonicalModelKey(stateModel) == canonicalModelKey(model) {
					lastErr = state.LastError
					break
				}
			}
			// The original Retry-After is not persisted in Error. The active
			// recovery deadline supplies it for a pre-cooled credential.
			failure := &fallbackPersistedFailure{cause: lastErr, wait: until.Sub(now)}
			if fallbackExhaustionKind(failure) == fallbackNotExhausted {
				continue
			}
			attempt.failures[auth.ID] = failure
		} else {
			attempt.failures[auth.ID] = nil
		}
	}
	return attempt
}

type fallbackPersistedFailure struct {
	cause *Error
	wait  time.Duration
}

func (e *fallbackPersistedFailure) RetryAfter() *time.Duration { return &e.wait }
func (e *fallbackPersistedFailure) Error() string              { return e.cause.Error() }
func (e *fallbackPersistedFailure) Unwrap() error {
	if e.cause == nil {
		return nil
	}
	return e.cause
}

func (m *Manager) fallbackPoolExhausted(attempt *fallbackChainAttempt, candidate internalconfig.FallbackCandidate) bool {
	if len(attempt.failures) == 0 {
		return false
	}
	for id, err := range attempt.failures {
		kind := fallbackExhaustionKind(err)
		if kind == fallbackNotExhausted {
			return false
		}
		if kind == fallbackIntermediate {
			auth, ok := m.GetByID(id)
			if !ok {
				return false
			}
			model := m.selectionModelKeyForAuth(auth, candidate.Model)
			if blocked, _, until := isAuthBlockedForModel(auth, model, time.Now()); !blocked || !until.After(time.Now()) {
				return false
			}
		}
		if kind == fallbackAmbiguous {
			// Ambiguous 429: every credential in pool must have been attempted in this request.
			if _, attempted := attempt.eligible[id]; !attempted {
				return false
			}
		}
	}
	return true
}

func (m *Manager) fallbackCurrentPoolExhausted(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) bool {
	attempt := fallbackAttempt(ctx)
	return attempt != nil && len(providers) == 1 && m.fallbackPoolExhausted(attempt, internalconfig.FallbackCandidate{Provider: providers[0], Model: authSelectionModelFromOptions(opts, req.Model)})
}

type fallbackExhaustion uint8

const (
	fallbackNotExhausted fallbackExhaustion = iota
	fallbackIntermediate
	fallbackAmbiguous
	fallbackTerminal
)

func fallbackExhaustionKind(err error) fallbackExhaustion {
	if err == nil || isRequestScopedError(err) || isRequestTerminatedError(err) || isRequestStopError(err) {
		return fallbackNotExhausted
	}
	switch statusCodeFromError(err) {
	case http.StatusPaymentRequired:
		return fallbackTerminal
	case http.StatusTooManyRequests:
	default:
		return fallbackNotExhausted
	}
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil && fallbackQuotaCode(authErr.Code) {
		return fallbackTerminal
	}
	body := err.Error()
	if authErr != nil {
		body = authErr.Message
	} else {
		for cause := errors.Unwrap(err); cause != nil; cause = errors.Unwrap(cause) {
			body = cause.Error()
		}
	}
	for _, path := range []string{"error.code", "error.type", "code", "type"} {
		if fallbackQuotaCode(ojson.Get(body, path).String()) {
			return fallbackTerminal
		}
	}
	hasExplicitWait := false
	var wait time.Duration
	if retry := retryAfterFromError(err); retry != nil {
		wait = *retry
		hasExplicitWait = true
	}
	for _, path := range []string{"error.resets_in_seconds", "resets_in_seconds", "error.retry_after_seconds", "retry_after_seconds"} {
		node := ojson.Get(body, path)
		if node.Exists() {
			seconds := node.Float()
			if seconds > defaultFallbackExhaustionCooldown.Seconds() {
				return fallbackTerminal
			}
			hasExplicitWait = true
			if seconds > wait.Seconds() {
				wait = time.Duration(seconds * float64(time.Second))
			}
		}
	}
	var headers interface{ ResponseHeaders() http.Header }
	if errors.As(err, &headers) {
		value := headers.ResponseHeaders().Get("Retry-After")
		if seconds, errParse := strconv.ParseFloat(value, 64); errParse == nil {
			if seconds > defaultFallbackExhaustionCooldown.Seconds() {
				return fallbackTerminal
			}
			hasExplicitWait = true
			if seconds > wait.Seconds() {
				wait = time.Duration(seconds * float64(time.Second))
			}
		} else if until, errTime := http.ParseTime(value); errTime == nil {
			hasExplicitWait = true
			if d := time.Until(until); d > wait {
				wait = d
			}
		}
	}
	if wait > defaultFallbackExhaustionCooldown {
		return fallbackTerminal
	}
	if wait > 10*time.Second {
		return fallbackIntermediate
	}
	if !hasExplicitWait {
		// HTTP 429 with missing or unparseable Retry-After and without explicit quota code:
		// Ambiguous rate limit. Will retry/rotate across pool first, but can advance once
		// all credentials in active candidate pool have been attempted.
		return fallbackAmbiguous
	}
	// Explicit short throttle (<= 10s): transient rate limit, keep within intra-provider pool.
	return fallbackNotExhausted
}

func fallbackQuotaCode(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "usage_limit_reached", "quota_exhausted", "quota_exceeded", "insufficient_quota", "billing_hard_limit_reached", "monthly_spend_limit_reached":
		return true
	default:
		return false
	}
}
