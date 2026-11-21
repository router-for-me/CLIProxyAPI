package auth

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	minContinuityPollInterval     = 10 * time.Millisecond
	defaultContinuityPollInterval = 500 * time.Millisecond
	maxContinuityPollInterval     = 5 * time.Second
	defaultContinuityWaitBudget   = 30 * time.Second
	maxContinuityWaitBudget       = 120 * time.Second
)

type continuityClock interface {
	Now() time.Time
	Wait(ctx context.Context, d time.Duration) error
}

type realContinuityClock struct{}

func (realContinuityClock) Now() time.Time {
	return time.Now()
}

func (realContinuityClock) Wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// SetContinuityClockForTest overrides the clock used by Codex continuity wait loops in tests.
func (m *Manager) SetContinuityClockForTest(clock continuityClock) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.continuityClock = clock
	m.mu.Unlock()
}

func (m *Manager) getContinuityClock() continuityClock {
	if m != nil {
		m.mu.RLock()
		clock := m.continuityClock
		m.mu.RUnlock()
		if clock != nil {
			return clock
		}
	}
	return realContinuityClock{}
}

func (m *Manager) hasContinuityClockOverride() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	has := m.continuityClock != nil
	m.mu.RUnlock()
	return has
}

func (m *Manager) continuityDeadlineExceeded(session *continuitySession) bool {
	if session == nil || session.deadline.IsZero() {
		return false
	}
	return !m.getContinuityClock().Now().Before(session.deadline)
}

func (m *Manager) waitForContinuityBoundedCooldown(ctx context.Context, session *continuitySession, wait, maxWait time.Duration) (bool, error) {
	if session != nil && !session.deadline.IsZero() {
		clock := m.getContinuityClock()
		remaining := session.deadline.Sub(clock.Now())
		if remaining <= 0 {
			session.budgetExhausted = true
			session.terminated = true
			return true, nil
		}
		if wait > 0 {
			if wait > remaining {
				wait = remaining
			}
			bounded := jitteredCooldownWait(wait, remaining)
			if bounded > remaining {
				bounded = remaining
			}
			if m.hasContinuityClockOverride() {
				if errWait := clock.Wait(ctx, bounded); errWait != nil {
					return false, errWait
				}
			} else if errWait := waitForCooldown(ctx, bounded, remaining); errWait != nil {
				return false, errWait
			}
		}
		if !clock.Now().Before(session.deadline) {
			session.budgetExhausted = true
			session.terminated = true
			return true, nil
		}
		return false, nil
	}
	if errWait := waitForCooldown(ctx, wait, maxWait); errWait != nil {
		return false, errWait
	}
	return false, nil
}

// HasSchedulerContinuity reports whether the active plugin scheduler supports bounded continuity.
func (m *Manager) HasSchedulerContinuity() bool {
	if m != nil && m.HomeEnabled() {
		_, supported := m.canRunCodexContinuity(context.Background(), []string{"codex"})
		return supported
	}
	return m.snapshotSchedulerContinuity() != nil
}

func (m *Manager) snapshotSchedulerContinuity() PluginSchedulerContinuity {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	scheduler := m.pluginScheduler
	m.mu.RUnlock()
	if scheduler == nil {
		return nil
	}
	if state, ok := scheduler.(pluginSchedulerState); ok && !state.HasScheduler() {
		return nil
	}
	continuity, ok := scheduler.(PluginSchedulerContinuity)
	if !ok || continuity == nil || !continuity.HasSchedulerContinuity() {
		return nil
	}
	return continuity
}

type continuityOutcome int

const (
	continuityOutcomeDisabled continuityOutcome = iota
	continuityOutcomeReady
	continuityOutcomeCanceled
)

type continuitySessionContextKey struct{}

type continuitySession struct {
	requestIDMu           sync.Mutex
	requestID             string
	traceID               string
	startedAt             time.Time
	deadline              time.Time
	budgetExhausted       bool
	terminated            bool
	terminalErr           error
	waitedLastGate        bool
	immediateReadyRetries int
	priorAttemptSeen      bool
	codexAttemptsUsed     int
	streamProviderMu      sync.Mutex
	lastStreamProvider    string
	lastStreamProviders   []string
	lastStreamModel       string
	lastStreamOpts        cliproxyexecutor.Options
}

func (s *continuitySession) setLastStreamContext(providers []string, model string, opts cliproxyexecutor.Options) {
	if s == nil {
		return
	}
	s.streamProviderMu.Lock()
	s.lastStreamProviders = append([]string(nil), providers...)
	s.lastStreamModel = model
	s.lastStreamOpts = opts
	s.streamProviderMu.Unlock()
}

func (s *continuitySession) getLastStreamContext() ([]string, string, cliproxyexecutor.Options) {
	if s == nil {
		return nil, "", cliproxyexecutor.Options{}
	}
	s.streamProviderMu.Lock()
	providers := append([]string(nil), s.lastStreamProviders...)
	model := s.lastStreamModel
	opts := s.lastStreamOpts
	s.streamProviderMu.Unlock()
	return providers, model, opts
}

func (s *continuitySession) setLastStreamProvider(provider string) {
	if s == nil {
		return
	}
	s.streamProviderMu.Lock()
	s.lastStreamProvider = strings.TrimSpace(provider)
	s.streamProviderMu.Unlock()
}

func (s *continuitySession) getLastStreamProvider() string {
	if s == nil {
		return ""
	}
	s.streamProviderMu.Lock()
	p := s.lastStreamProvider
	s.streamProviderMu.Unlock()
	return p
}

func continuitySessionFromContext(ctx context.Context) *continuitySession {
	if ctx == nil {
		return nil
	}
	session, _ := ctx.Value(continuitySessionContextKey{}).(*continuitySession)
	return session
}

type continuityTerminatedError struct {
	cause error
}

func (e *continuityTerminatedError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *continuityTerminatedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func markContinuityTerminatedError(err error) error {
	if err == nil || IsContinuityTerminatedError(err) {
		return err
	}
	return &continuityTerminatedError{cause: err}
}

// IsContinuityTerminatedError reports whether err terminated via the Codex continuity gate
// (for example disabled or wait-budget exhaustion) and must not be bypassed by outer retries.
func IsContinuityTerminatedError(err error) bool {
	var marked *continuityTerminatedError
	return errors.As(err, &marked) && marked != nil
}

// WithRequestContinuitySession binds a single continuity budget and termination state to ctx
// if none is present yet, so outer retry loops (such as stream bootstrap retries or a single
// WebSocket turn's in-place failover) share the same request budget.
func WithRequestContinuitySession(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if existing, ok := ctx.Value(continuitySessionContextKey{}).(*continuitySession); ok && existing != nil {
		return ctx
	}
	return context.WithValue(ctx, continuitySessionContextKey{}, &continuitySession{})
}

// WithFreshRequestContinuitySession binds a new, isolated continuity session to ctx,
// replacing any ancestor request's continuity state for independent or nested model executions.
func WithFreshRequestContinuitySession(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, continuitySessionContextKey{}, &continuitySession{})
}

// ContinuitySessionRequestID returns the request identity already bound to the
// context's continuity session, if any, so retries and in-place failovers share one RequestID.
func ContinuitySessionRequestID(ctx context.Context) string {
	session := continuitySessionFromContext(ctx)
	if session == nil {
		return ""
	}
	session.requestIDMu.Lock()
	reqID := session.requestID
	session.requestIDMu.Unlock()
	return reqID
}

func ensureContinuitySession(ctx context.Context, opts *cliproxyexecutor.Options) *continuitySession {
	var session *continuitySession
	if ctx != nil {
		if existing, ok := ctx.Value(continuitySessionContextKey{}).(*continuitySession); ok && existing != nil {
			session = existing
		}
	}
	if session == nil {
		session = &continuitySession{}
	}
	session.requestIDMu.Lock()
	if session.requestID == "" {
		if opts != nil {
			session.requestID = stringMetadataValue(opts.Metadata, cliproxyexecutor.RequestIDMetadataKey)
		}
		if session.requestID == "" {
			session.requestID = uuid.NewString()
		}
	}
	reqID := session.requestID
	if session.traceID == "" {
		if opts != nil {
			if trID := stringMetadataValue(opts.Metadata, cliproxyexecutor.TraceIDMetadataKey); trID != "" {
				session.traceID = trID
			}
		}
		if session.traceID == "" {
			if trID := logging.GetRequestID(ctx); trID != "" {
				session.traceID = trID
			}
		}
	}
	traceID := session.traceID
	session.requestIDMu.Unlock()
	if opts != nil {
		opts.EnsureMetadata()
		opts.Metadata[cliproxyexecutor.RequestIDMetadataKey] = reqID
		if traceID != "" && stringMetadataValue(opts.Metadata, cliproxyexecutor.TraceIDMetadataKey) == "" {
			opts.Metadata[cliproxyexecutor.TraceIDMetadataKey] = traceID
		}
	}
	return session
}

func (m *Manager) canRunCodexContinuity(ctx context.Context, providers []string) (PluginSchedulerContinuity, bool) {
	if m == nil {
		return nil, false
	}
	if cliproxyexecutor.RequiredUpstreamWebsocket(ctx) {
		return nil, false
	}
	if !providersIncludeCodex(providers) {
		return nil, false
	}
	if m.HomeEnabled() {
		bundle := m.HomeDispatchBundle()
		if bundle == nil {
			return nil, false
		}
		client, ok := bundle.client.(homeContinuityClient)
		if !ok || client.SchedulerProtocol() < 1 {
			return nil, false
		}
		return &homeSchedulerContinuity{client: client, manager: m}, true
	}
	continuity := m.snapshotSchedulerContinuity()
	if continuity == nil {
		return nil, false
	}
	return continuity, true
}

func providersIncludeCodex(providers []string) bool {
	for _, provider := range providers {
		if canonicalSchedulingProvider(provider) == "codex" {
			return true
		}
	}
	return false
}

func (m *Manager) isCodexAttemptFailure(err error, attempted map[string]struct{}) bool {
	if provider := upstreamAttemptProviderFromError(err); provider != "" {
		return canonicalSchedulingProvider(provider) == "codex"
	}
	if m == nil || len(attempted) == 0 {
		return false
	}
	codexCount := 0
	nonCodexCount := 0
	m.mu.RLock()
	for authID := range attempted {
		candidate := m.auths[authID]
		if candidate == nil {
			continue
		}
		if canonicalSchedulingProvider(executorKeyFromAuth(candidate)) == "codex" {
			codexCount++
		} else {
			nonCodexCount++
		}
	}
	m.mu.RUnlock()
	return codexCount > 0 && nonCodexCount == 0
}

func isPreAdmissionContinuityError(err error) bool {
	if err == nil || IsTerminalAuthError(err) || isRequestInvalidError(err) || isRequestStopError(err) || isRequestTerminatedError(err) || isCredentialTerminatedError(err) {
		return false
	}
	if isSchedulerRejectError(err) {
		return true
	}
	var cooldownErr *modelCooldownError
	if errors.As(err, &cooldownErr) && cooldownErr != nil {
		return true
	}
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil {
		return authErr.Code == "auth_unavailable" || authErr.Code == "model_cooldown"
	}
	return false
}

func isPositiveQuotaRejectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if isRequestInvalidError(err) || isRequestStopError(err) || isRequestTerminatedError(err) || isCredentialTerminatedError(err) ||
		isStreamTruncatedError(err) || cliproxyexecutor.IsUpstreamWebsocketTurnError(err) ||
		IsTerminalAuthError(err) {
		return false
	}
	if isTransientRateLimitError(err) {
		return false
	}
	if statusCodeFromError(err) != http.StatusTooManyRequests {
		return false
	}
	if isCredentialScopedError(err) {
		return true
	}
	type modelCooldownMarker interface {
		IsModelCooldown() bool
	}
	var cooldownMarker modelCooldownMarker
	if errors.As(err, &cooldownMarker) && cooldownMarker != nil && cooldownMarker.IsModelCooldown() {
		return true
	}
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil && isPositiveQuotaClassificationCode(authErr.Code) {
		return true
	}
	return errorBodyHasPositiveQuotaClassification(err)
}

func isPositiveQuotaClassificationCode(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case credentialQuotaErrorCode, "model_cooldown", "usage_limit_reached", "quota_exceeded":
		return true
	default:
		return false
	}
}

func errorBodyHasPositiveQuotaClassification(err error) bool {
	if isPositiveQuotaClassificationCode(errorCodeFromError(err)) {
		return true
	}
	type responseBodyProvider interface {
		ResponseBody() []byte
	}
	body := strings.TrimSpace(err.Error())
	var bodyProvider responseBodyProvider
	if errors.As(err, &bodyProvider) && bodyProvider != nil && len(bodyProvider.ResponseBody()) > 0 {
		body = strings.TrimSpace(string(bodyProvider.ResponseBody()))
	}
	if body == "" || !ojson.Valid(body) {
		return false
	}
	for _, path := range []string{
		"error.type", "type", "response.error.type", "body.error.type",
		"error.code", "code", "response.error.code", "body.error.code",
	} {
		if isPositiveQuotaClassificationCode(ojson.Get(body, path).String()) {
			return true
		}
	}
	return false
}

func (m *Manager) runCodexContinuityGate(
	ctx context.Context,
	session *continuitySession,
	phase string,
	providers []string,
	model string,
	opts cliproxyexecutor.Options,
	retryRound int,
	applyRetryRoundExclusions bool,
	defaultRequestRetry int,
) (continuityOutcome, error) {
	if session == nil {
		return continuityOutcomeDisabled, nil
	}
	session.waitedLastGate = false
	continuity, ok := m.canRunCodexContinuity(ctx, providers)
	if !ok {
		return continuityOutcomeDisabled, nil
	}
	if session.terminated || session.budgetExhausted {
		session.terminated = true
		return continuityOutcomeDisabled, nil
	}

	clock := m.getContinuityClock()
	waitNotified := false
	defer func() {
		if waitNotified {
			cliproxyexecutor.NotifyContinuityWaitEnd(ctx)
		}
	}()

	for pollAttempt := 0; ; pollAttempt++ {
		if ctx != nil {
			if errCtx := ctx.Err(); errCtx != nil {
				return continuityOutcomeCanceled, errCtx
			}
		}
		now := clock.Now()
		if !session.deadline.IsZero() && !now.Before(session.deadline) {
			session.budgetExhausted = true
			session.terminated = true
			return continuityOutcomeDisabled, nil
		}

		var candidates []pluginapi.SchedulerContinuityCandidate
		if !m.HomeEnabled() {
			candidates = m.buildCodexContinuityCandidates(ctx, providers, model, opts, retryRound, applyRetryRoundExclusions, defaultRequestRetry, now)
		}
		if len(candidates) == 0 && !m.HomeEnabled() {
			if pollAttempt > 0 || phase == "pre_output_429" {
				session.terminated = true
			}
			return continuityOutcomeDisabled, nil
		}

		var elapsedMs int64
		if !session.startedAt.IsZero() {
			elapsedMs = max(now.Sub(session.startedAt).Milliseconds(), 0)
		}

		req := pluginapi.SchedulerContinuityRequest{
			RequestID:          session.requestID,
			TraceID:            session.traceID,
			ExecutionSessionID: stringMetadataValue(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey),
			CanonicalSessionID: stringMetadataValue(opts.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey),
			Provider:           "codex",
			Providers:          schedulerProviders("codex", providers),
			Model:              model,
			RequestedModel:     stringMetadataValue(opts.Metadata, cliproxyexecutor.RequestedModelMetadataKey),
			Stream:             opts.Stream,
			Phase:              phase,
			Attempt:            pollAttempt,
			ElapsedMs:          elapsedMs,
			Options:            schedulerOptions(opts),
			Candidates:         candidates,
		}
		if m.HomeEnabled() {
			req.Options.Metadata = cloneRequestMetadata(req.Options.Metadata)
			req.Options.Metadata[homeRetryRoundMetadataKey] = retryRound
			req.Options.Metadata["apply_retry_round_exclusions"] = applyRetryRoundExclusions
		}
		resp, handled, errEval := continuity.EvaluateSchedulerContinuity(ctx, req)
		if ctx != nil {
			if errCtx := ctx.Err(); errCtx != nil {
				return continuityOutcomeCanceled, errCtx
			}
		}
		if errEval != nil && m.HomeEnabled() {
			return continuityOutcomeDisabled, wrapRequestStopError(errEval)
		}
		if !handled && m.HomeEnabled() {
			// Home can report no eligible observation candidates before a gate starts.
			// Preserve ordinary retry behavior just as the local empty-candidate path does.
			if pollAttempt > 0 || phase == "pre_output_429" {
				session.terminated = true
			}
			return continuityOutcomeDisabled, nil
		}
		if errEval != nil || !handled {
			session.terminated = true
			return continuityOutcomeDisabled, nil
		}
		now = clock.Now()
		if !session.deadline.IsZero() && !now.Before(session.deadline) {
			session.budgetExhausted = true
			session.terminated = true
			return continuityOutcomeDisabled, nil
		}

		decision := pluginapi.SchedulerContinuityDecision(strings.ToLower(strings.TrimSpace(string(resp.Decision))))
		switch decision {
		case pluginapi.SchedulerContinuityReady:
			return continuityOutcomeReady, nil
		case pluginapi.SchedulerContinuityWait:
			now = clock.Now()
			if session.startedAt.IsZero() {
				if resp.WaitBudgetMs < 0 {
					session.budgetExhausted = true
					session.terminated = true
					return continuityOutcomeDisabled, nil
				}
				budget := clampContinuityWaitBudget(resp.WaitBudgetMs)
				session.startedAt = now
				session.deadline = session.startedAt.Add(budget)
			} else if resp.WaitBudgetMs > 0 {
				proposed := session.startedAt.Add(clampContinuityWaitBudget(resp.WaitBudgetMs))
				if proposed.Before(session.deadline) {
					session.deadline = proposed
				}
			}
			remaining := session.deadline.Sub(now)
			if remaining <= 0 {
				session.budgetExhausted = true
				session.terminated = true
				return continuityOutcomeDisabled, nil
			}
			pollInterval := clampContinuityPollInterval(resp.PollIntervalMs)
			if pollInterval > remaining {
				pollInterval = remaining
			}
			if !waitNotified {
				cliproxyexecutor.NotifyContinuityWaitStart(ctx)
				waitNotified = true
			}
			if errWait := clock.Wait(ctx, pollInterval); errWait != nil {
				return continuityOutcomeCanceled, errWait
			}
			session.waitedLastGate = true
		default:
			session.terminated = true
			return continuityOutcomeDisabled, nil
		}
	}
}

func clampContinuityPollInterval(ms int64) time.Duration {
	if ms <= 0 {
		return defaultContinuityPollInterval
	}
	d := time.Duration(ms) * time.Millisecond
	if d < minContinuityPollInterval {
		return minContinuityPollInterval
	}
	if d > maxContinuityPollInterval {
		return maxContinuityPollInterval
	}
	return d
}

func clampContinuityWaitBudget(ms int64) time.Duration {
	if ms <= 0 {
		return defaultContinuityWaitBudget
	}
	d := time.Duration(ms) * time.Millisecond
	if d > maxContinuityWaitBudget {
		return maxContinuityWaitBudget
	}
	return d
}

func (m *Manager) buildCodexContinuityCandidates(
	ctx context.Context,
	providers []string,
	model string,
	opts cliproxyexecutor.Options,
	retryRound int,
	applyRetryRoundExclusions bool,
	defaultRequestRetry int,
	now time.Time,
) []pluginapi.SchedulerContinuityCandidate {
	if m == nil {
		return nil
	}
	if !providersIncludeCodex(providers) {
		return nil
	}
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	excludedIDs := homeExcludedAuthIDsFromMetadata(opts.Metadata)
	var excludedSet map[string]struct{}
	if len(excludedIDs) > 0 {
		excludedSet = make(map[string]struct{}, len(excludedIDs))
		for _, id := range excludedIDs {
			excludedSet[id] = struct{}{}
		}
	}
	modelKey := strings.TrimSpace(model)
	if modelKey != "" {
		if parsed := thinking.ParseSuffix(modelKey); parsed.ModelName != "" {
			modelKey = strings.TrimSpace(parsed.ModelName)
		}
	}
	registryRef := registry.GetGlobalRegistry()

	m.mu.RLock()
	defer m.mu.RUnlock()

	if _, ok := m.executorLocked("codex"); !ok {
		return nil
	}
	weightedSelection := UnwrapWeightedRoundRobinSelector(m.selector) != nil

	out := make([]pluginapi.SchedulerContinuityCandidate, 0, len(m.auths))
	for _, candidate := range m.auths {
		if candidate == nil || candidate.Disabled || candidate.Status == StatusDisabled {
			continue
		}
		if canonicalSchedulingProvider(executorKeyFromAuth(candidate)) != "codex" {
			continue
		}
		if pinnedAuthID != "" && candidate.ID != pinnedAuthID {
			continue
		}
		if len(excludedSet) > 0 {
			if _, excluded := excludedSet[candidate.ID]; excluded {
				continue
			}
		}
		if !eligibility.allows(candidate) {
			continue
		}
		if weightedSelection && authWeight(candidate) <= 0 {
			continue
		}
		if applyRetryRoundExclusions && retryRound > 0 && effectiveRequestRetryLimit(candidate, defaultRequestRetry) < retryRound {
			continue
		}
		if modelKey != "" && !m.authSupportsRouteModel(registryRef, candidate, model) {
			continue
		}
		if hasUnauthorizedAuthFailure(candidate) {
			continue
		}
		checkModel := m.selectionModelForAuth(candidate, model)
		blocked, reason, next := isAuthBlockedForModelForRequest(ctx, candidate, checkModel, now, opts.Headers)
		if blocked && (reason != blockReasonCooldown || !isQuotaCooldownBlock(candidate, checkModel, now)) {
			continue
		}
		item := pluginapi.SchedulerContinuityCandidate{
			ID:         candidate.ID,
			Provider:   "codex",
			Priority:   authPriority(candidate),
			Status:     string(candidate.Status),
			InCooldown: blocked && reason == blockReasonCooldown,
			Attributes: schedulerSafeAttributes(candidate.Attributes),
		}
		if item.InCooldown {
			item.CooldownReason = continuityCooldownReason(candidate, checkModel)
			if !next.IsZero() {
				nextUTC := next.UTC()
				item.CooldownUntil = &nextUTC
			}
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func continuityCooldownReason(auth *Auth, checkModel string) string {
	if auth == nil {
		return ""
	}
	if auth.Quota.Exceeded && strings.TrimSpace(auth.Quota.Reason) != "" {
		return strings.TrimSpace(auth.Quota.Reason)
	}
	if checkModel != "" && len(auth.ModelStates) > 0 {
		if state := existingModelState(auth, checkModel); state != nil {
			if reason := cooldownReason(state.StatusMessage, state.Quota, state.LastError); reason != "" {
				return reason
			}
		}
	}
	if reason := cooldownReason(auth.StatusMessage, auth.Quota, auth.LastError); reason != "" {
		return reason
	}
	return "quota"
}

func isQuotaCooldownResultError(err *Error) bool {
	if err == nil {
		return false
	}
	if isCloudflareChallengeResultError(err) ||
		isTransientRateLimitResultError(err) ||
		isInvalidGrantResultError(err) ||
		isCreditBalanceTooLowResultError(err) ||
		isTOSViolationResultError(err) {
		return false
	}
	return statusCodeFromResult(err) == http.StatusTooManyRequests
}

// EvaluatePreOutputStreamQuotaGate enforces the Codex continuity pre_output_429
// gate and retry budget before an outer streaming bootstrap retry replays a
// stream whose upstream connection opened and then failed with a positive quota
// 429 prior to emitting any downstream SSE frame.
// EvaluatePreOutputStreamFailoverQuotaGate enforces the Codex continuity
// pre_output_429 gate and retry budget before an outer failover replay after
// unpinning and excluding failedAuthID from candidate selection.
func (m *Manager) EvaluatePreOutputStreamFailoverQuotaGate(
	ctx context.Context,
	model string,
	failedAuthID string,
	err error,
) (bool, error) {
	return m.evaluatePreOutputStreamQuotaGate(ctx, nil, model, cliproxyexecutor.Options{}, failedAuthID, true, err)
}

// EvaluatePreOutputStreamQuotaGate enforces the Codex continuity pre_output_429
// gate and retry budget before an outer streaming bootstrap retry replays a
// stream whose upstream connection opened and then failed with a positive quota
// 429 prior to emitting any downstream SSE frame.
func (m *Manager) EvaluatePreOutputStreamQuotaGate(
	ctx context.Context,
	providers []string,
	model string,
	opts cliproxyexecutor.Options,
	err error,
) (bool, error) {
	return m.evaluatePreOutputStreamQuotaGate(ctx, providers, model, opts, "", false, err)
}

func (m *Manager) evaluatePreOutputStreamQuotaGate(
	ctx context.Context,
	providers []string,
	model string,
	opts cliproxyexecutor.Options,
	failedAuthID string,
	unpin bool,
	err error,
) (bool, error) {
	if m == nil || err == nil || !isPositiveQuotaRejectionError(err) {
		return true, nil
	}
	if existingSession := continuitySessionFromContext(ctx); existingSession != nil && (len(providers) == 0 || (len(opts.Headers) == 0 && len(opts.Metadata) == 0)) {
		savedProviders, savedModel, savedOpts := existingSession.getLastStreamContext()
		if len(providers) == 0 && len(savedProviders) > 0 {
			providers = savedProviders
		}
		if strings.TrimSpace(model) == "" {
			model = savedModel
		}
		if len(opts.Headers) == 0 && len(opts.Metadata) == 0 {
			opts = savedOpts
		}
	}
	if unpin {
		clonedMeta := cloneMetadataMap(opts.Metadata)
		delete(clonedMeta, cliproxyexecutor.PinnedAuthMetadataKey)
		opts.Metadata = clonedMeta
		if strings.TrimSpace(failedAuthID) != "" {
			opts = withHomeExcludedAuthIDs(opts, map[string]struct{}{strings.TrimSpace(failedAuthID): {}})
		}
	}
	session := ensureContinuitySession(ctx, &opts)
	normalized := m.normalizeProviders(providers)
	if _, okContinuity := m.canRunCodexContinuity(ctx, normalized); !okContinuity {
		return true, nil
	}
	provider := upstreamAttemptProviderFromError(err)
	if provider == "" {
		provider = session.getLastStreamProvider()
	}
	if provider != "" {
		if canonicalSchedulingProvider(provider) != "codex" {
			return true, nil
		}
	} else {
		for _, p := range normalized {
			if canonicalSchedulingProvider(p) != "codex" {
				return true, nil
			}
		}
	}
	if session.terminated {
		termErr := session.terminalErr
		if termErr == nil {
			termErr = err
		}
		return false, markContinuityTerminatedError(termErr)
	}
	if m.continuityDeadlineExceeded(session) {
		session.budgetExhausted = true
		session.terminated = true
		return false, markContinuityTerminatedError(err)
	}
	retryModel := authSelectionModelFromOptions(opts, model)
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	defaultRequestRetry, _, _ := m.retrySettings()
	// Home applies its authoritative per-credential retry limits to observation candidates.
	if !m.HomeEnabled() && !m.retryAllowedForRequest(ctx, session.codexAttemptsUsed, normalized, retryModel, eligibility, pinnedAuthID, defaultRequestRetry, opts.Headers) {
		session.terminated = true
		return false, markContinuityTerminatedError(err)
	}
	outcome, errContinuity := m.runCodexContinuityGate(ctx, session, "pre_output_429", normalized, retryModel, opts, session.codexAttemptsUsed+1, true, defaultRequestRetry)
	if errContinuity != nil {
		return false, errContinuity
	}
	if outcome == continuityOutcomeReady {
		session.immediateReadyRetries = 0
		session.codexAttemptsUsed++
		session.priorAttemptSeen = true
		return true, nil
	}
	session.terminated = true
	if session.terminalErr != nil {
		return false, markContinuityTerminatedError(session.terminalErr)
	}
	return false, markContinuityTerminatedError(err)
}

func isQuotaCooldownBlock(auth *Auth, checkModel string, now time.Time) bool {
	if auth == nil {
		return false
	}
	if !auth.credentialRetryAfter.IsZero() && auth.credentialRetryAfter.After(now) {
		return false
	}
	if auth.nonQuotaCooldownUntil.After(now) {
		return false
	}
	authForce := effectiveAuthForceUntil(auth)
	authQuotaForceOnly := hasActiveQuotaForceAfterExpiredNonQuota(auth.Quota, auth.nonQuotaCooldownUntil, auth.quotaForceCooldownUntil, auth.NextRetryAfter, now)
	if authForce.After(now) && auth.NextRetryAfter.After(now) && !authQuotaForceOnly {
		if !auth.Quota.Exceeded || !auth.Quota.NextRecoverAt.After(now) {
			return false
		}
		if len(auth.ModelStates) > 0 && auth.Quota.Reason != "credential_quota" {
			return false
		}
		if isNonQuotaChallengeReason(auth.Quota.Reason) {
			return false
		}
		if auth.LastError != nil && !isQuotaCooldownLastError(auth.LastError) && authForce.After(auth.quotaForceCooldownUntil) {
			return false
		}
	}
	if auth.Quota.Exceeded && auth.Quota.Reason == "credential_quota" && auth.Quota.NextRecoverAt.After(now) {
		if auth.NextRetryAfter.After(auth.Quota.NextRecoverAt) && auth.LastError != nil && !isQuotaCooldownLastError(auth.LastError) {
			return false
		}
		if checkModel != "" && len(auth.ModelStates) > 0 {
			modelKey := canonicalModelLookupKey(checkModel)
			for stateModel, state := range auth.ModelStates {
				if state == nil || modelStateLookupKey(auth, stateModel) != modelKey {
					continue
				}
				if state.nonQuotaCooldownUntil.After(now) {
					return false
				}
				if state.credentialQuotaShadow != nil && isModelStateActiveCooldown(state.credentialQuotaShadow, now) {
					if !isModelStateQuotaCooldown(state.credentialQuotaShadow, now) {
						return false
					}
				}
				if modelForce := effectiveModelForceUntil(state); modelForce.After(now) && state.LastError != nil && !isQuotaCooldownLastError(state.LastError) && modelForce.After(state.quotaForceCooldownUntil) {
					return false
				}
				if state.NextRetryAfter.After(auth.Quota.NextRecoverAt) {
					if !state.Quota.Exceeded || isNonQuotaChallengeReason(state.Quota.Reason) {
						return false
					}
					if !state.Quota.NextRecoverAt.IsZero() && state.NextRetryAfter.After(state.Quota.NextRecoverAt) && state.LastError != nil && !isQuotaCooldownLastError(state.LastError) {
						return false
					}
				}
			}
		}
		return true
	}
	if checkModel != "" && len(auth.ModelStates) > 0 {
		modelKey := canonicalModelLookupKey(checkModel)
		matched := false
		hasQuota := false
		for stateModel, state := range auth.ModelStates {
			if state == nil || modelStateLookupKey(auth, stateModel) != modelKey {
				continue
			}
			matched = true
			if state.Status == StatusDisabled || state.nonQuotaCooldownUntil.After(now) {
				return false
			}
			if state.credentialQuotaShadow != nil && isModelStateActiveCooldown(state.credentialQuotaShadow, now) {
				if !isModelStateQuotaCooldown(state.credentialQuotaShadow, now) {
					return false
				}
			}
			stateBlocked, stateReason, _ := availabilityBlock(state.Unavailable, state.Quota.Exceeded, state.NextRetryAfter, state.Quota.NextRecoverAt, now)
			if !stateBlocked {
				continue
			}
			if stateReason != blockReasonCooldown || !isModelStateQuotaCooldown(state, now) {
				return false
			}
			hasQuota = true
		}
		if matched {
			return hasQuota || authQuotaForceOnly
		}
		return authQuotaForceOnly
	}
	if !authQuotaForceOnly {
		if !auth.Quota.Exceeded || isNonQuotaChallengeReason(auth.Quota.Reason) {
			return false
		}
		if authForce.After(now) && auth.LastError != nil && !isQuotaCooldownLastError(auth.LastError) && authForce.After(auth.quotaForceCooldownUntil) {
			return false
		}
		if !auth.Quota.NextRecoverAt.IsZero() && auth.NextRetryAfter.After(auth.Quota.NextRecoverAt) && auth.LastError != nil && !isQuotaCooldownLastError(auth.LastError) {
			return false
		}
	}
	return true
}

func hasActiveQuotaForceAfterExpiredNonQuota(quota QuotaState, nonQuotaUntil, quotaForceUntil, nextRetryAfter time.Time, now time.Time) bool {
	if !quotaForceUntil.After(now) || nonQuotaUntil.After(now) {
		return false
	}
	if isNonQuotaChallengeReason(quota.Reason) && quota.NextRecoverAt.After(now) {
		return false
	}
	return !nextRetryAfter.After(quotaForceUntil)
}

func isModelStateQuotaCooldown(state *ModelState, now time.Time) bool {
	if state == nil {
		return false
	}
	if state.Status == StatusDisabled {
		return false
	}
	if state.nonQuotaCooldownUntil.After(now) {
		return false
	}
	if state.credentialQuotaShadow != nil && isModelStateActiveCooldown(state.credentialQuotaShadow, now) {
		if !isModelStateQuotaCooldown(state.credentialQuotaShadow, now) {
			return false
		}
	}
	if modelForce := effectiveModelForceUntil(state); modelForce.After(now) && state.LastError != nil && !isQuotaCooldownLastError(state.LastError) && modelForce.After(state.quotaForceCooldownUntil) {
		return false
	}
	stateBlocked, stateReason, _ := availabilityBlock(state.Unavailable, state.Quota.Exceeded, state.NextRetryAfter, state.Quota.NextRecoverAt, now)
	if !stateBlocked {
		return true
	}
	if hasActiveQuotaForceAfterExpiredNonQuota(state.Quota, state.nonQuotaCooldownUntil, state.quotaForceCooldownUntil, state.NextRetryAfter, now) {
		return stateReason == blockReasonCooldown
	}
	if stateReason != blockReasonCooldown || !state.Quota.Exceeded || isNonQuotaChallengeReason(state.Quota.Reason) {
		return false
	}
	if !state.Quota.NextRecoverAt.IsZero() && state.NextRetryAfter.After(state.Quota.NextRecoverAt) && state.LastError != nil && !isQuotaCooldownLastError(state.LastError) {
		return false
	}
	return true
}

func isNonQuotaChallengeReason(reason string) bool {
	return strings.EqualFold(strings.TrimSpace(reason), "cloudflare challenge")
}

func isQuotaCooldownLastError(lastErr *Error) bool {
	if lastErr == nil {
		return true
	}
	if lastErr.Code == ErrorCodeForceCooldown {
		return lastErr.HTTPStatus == http.StatusTooManyRequests
	}
	return lastErr.HTTPStatus == http.StatusTooManyRequests
}
