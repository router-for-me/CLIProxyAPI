package auth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

func claudeOAuthRequestCancellation(ctx context.Context, auth *Auth, err error) error {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") || !strings.EqualFold(strings.TrimSpace(auth.Attributes["auth_kind"]), "oauth") {
		return nil
	}
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// Execute performs a non-streaming execution using the configured selector and executor.
// It supports multiple providers for the same model and round-robins the starting provider per model.
func (m *Manager) Execute(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	req, opts = cliproxysession.Enrich(req, opts)
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	if m.HomeEnabled() {
		return m.executeHome(ctx, normalized, req, opts, false)
	}

	_, maxRetryCredentials, maxWait := m.retrySettings()

	var lastErr error
	var lastAuthID string
	retryModel := authSelectionModelFromOptions(opts, req.Model)
	attempts := 0
	for attempt := 0; ; attempt++ {
		resp, errExec, lastTriedAuth := m.executeMixedOnce(ctx, normalized, req, opts, maxRetryCredentials)
		if errExec == nil {
			return resp, nil
		}
		if isRequestTerminatedError(errExec) {
			return cliproxyexecutor.Response{}, errExec
		}
		lastErr = errExec
		if lastTriedAuth != nil {
			lastAuthID = lastTriedAuth.ID
		} else {
			lastAuthID = ""
		}
		attempts = attempt + 1
		wait, shouldRetry := m.shouldRetryAfterError(errExec, attempt, normalized, retryModel, maxWait)
		if !shouldRetry {
			break
		}
		if errWait := waitForCooldown(ctx, wait, maxWait, normalized, retryModel); errWait != nil {
			return cliproxyexecutor.Response{}, errWait
		}
	}
	if lastErr != nil {
		emitAttemptsExhausted(retryModel, lastAuthID, attempts)
		if hasAntigravityProvider(normalized) && shouldAttemptAntigravityCreditsFallback(m, lastErr, normalized) {
			if resp, ok, errCredits := m.tryAntigravityCreditsExecute(ctx, req, opts); errCredits != nil {
				return cliproxyexecutor.Response{}, errCredits
			} else if ok {
				return resp, nil
			}
		}
		return cliproxyexecutor.Response{}, lastErr
	}
	return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// It supports multiple providers for the same model and round-robins the starting provider per model.
func (m *Manager) ExecuteCount(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	req, opts = cliproxysession.Enrich(req, opts)
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	if m.HomeEnabled() {
		return m.executeHome(ctx, normalized, req, opts, true)
	}

	_, maxRetryCredentials, maxWait := m.retrySettings()

	var lastErr error
	var lastAuthID string
	retryModel := authSelectionModelFromOptions(opts, req.Model)
	attempts := 0
	for attempt := 0; ; attempt++ {
		resp, errExec, lastTriedAuth := m.executeCountMixedOnce(ctx, normalized, req, opts, maxRetryCredentials)
		if errExec == nil {
			return resp, nil
		}
		if isRequestTerminatedError(errExec) {
			return cliproxyexecutor.Response{}, errExec
		}
		lastErr = errExec
		if lastTriedAuth != nil {
			lastAuthID = lastTriedAuth.ID
		} else {
			lastAuthID = ""
		}
		attempts = attempt + 1
		wait, shouldRetry := m.shouldRetryAfterError(errExec, attempt, normalized, retryModel, maxWait)
		if !shouldRetry {
			break
		}
		if errWait := waitForCooldown(ctx, wait, maxWait, normalized, retryModel); errWait != nil {
			return cliproxyexecutor.Response{}, errWait
		}
	}
	if lastErr != nil {
		emitAttemptsExhausted(retryModel, lastAuthID, attempts)
		return cliproxyexecutor.Response{}, lastErr
	}
	return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// ExecuteStream performs a streaming execution using the configured selector and executor.
// It supports multiple providers for the same model and round-robins the starting provider per model.
func (m *Manager) ExecuteStream(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	req, opts = cliproxysession.Enrich(req, opts)
	if m.HomeEnabled() {
		if unlockSession := m.lockHomeWebsocketSession(ctx, opts); unlockSession != nil {
			defer unlockSession()
		}
	}
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return nil, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}

	_, maxRetryCredentials, maxWait := m.retrySettings()

	var lastErr error
	var lastAuthID string
	retryModel := authSelectionModelFromOptions(opts, req.Model)
	attempts := 0
	for attempt := 0; ; attempt++ {
		result, errStream, lastTriedAuth := m.executeStreamMixedOnce(ctx, normalized, req, opts, maxRetryCredentials)
		if errStream == nil {
			return result, nil
		}
		if isRequestTerminatedError(errStream) {
			return nil, errStream
		}
		lastErr = errStream
		if lastTriedAuth != nil {
			lastAuthID = lastTriedAuth.ID
		} else {
			lastAuthID = ""
		}
		attempts = attempt + 1
		wait, shouldRetry := m.shouldRetryAfterError(errStream, attempt, normalized, retryModel, maxWait)
		if !shouldRetry {
			break
		}
		if errWait := waitForCooldown(ctx, wait, maxWait, normalized, retryModel); errWait != nil {
			return nil, errWait
		}
	}
	if lastErr != nil {
		emitAttemptsExhausted(retryModel, lastAuthID, attempts)
		if hasAntigravityProvider(normalized) && shouldAttemptAntigravityCreditsFallback(m, lastErr, normalized) {
			if result, ok, errCredits := m.tryAntigravityCreditsExecuteStream(ctx, req, opts); errCredits != nil {
				return nil, errCredits
			} else if ok {
				return result, nil
			}
		}
		var bootstrapErr *streamBootstrapError
		if errors.As(lastErr, &bootstrapErr) && bootstrapErr != nil {
			return streamErrorResult(bootstrapErr.Headers(), bootstrapErr.cause), nil
		}
		return nil, lastErr
	}
	return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
}

type requestToFormatResolver interface {
	RequestToFormat(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format
}

func isRequestTerminatedError(err error) bool {
	var terminated *cliproxyexecutor.RequestTerminatedError
	return errors.As(err, &terminated) && terminated != nil
}

func applyRequestAfterAuthInterceptor(ctx context.Context, executor ProviderExecutor, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, requestedModel string) (cliproxyexecutor.Request, cliproxyexecutor.Options, error) {
	if opts.RequestAfterAuthInterceptor == nil {
		return req, opts, nil
	}
	toFormat := requestToFormat(provider, executor, req, opts)
	resp := opts.RequestAfterAuthInterceptor(ctx, cliproxyexecutor.RequestAfterAuthInterceptRequest{
		SourceFormat:   opts.SourceFormat,
		ToFormat:       toFormat,
		Model:          req.Model,
		RequestedModel: requestedModel,
		Stream:         opts.Stream,
		Headers:        cloneRequestHeaders(opts.Headers),
		Body:           bytes.Clone(req.Payload),
		Metadata:       opts.Metadata,
	})
	opts.Headers = mergeRequestHeaders(opts.Headers, resp.Headers, resp.ClearHeaders)
	if len(resp.Body) > 0 {
		req.Payload = bytes.Clone(resp.Body)
		opts.OriginalRequest = bytes.Clone(resp.Body)
	}
	if resp.Terminate {
		return req, opts, &cliproxyexecutor.RequestTerminatedError{
			HTTPStatus: resp.StatusCode,
			Header:     cloneRequestHeaders(resp.ResponseHeaders),
			Body:       bytes.Clone(resp.ResponseBody),
		}
	}
	return req, opts, nil
}

func requestToFormat(provider string, executor ProviderExecutor, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format {
	resolver, ok := executor.(requestToFormatResolver)
	if ok && resolver != nil {
		formatRequestTo := resolver.RequestToFormat(req, opts)
		if formatRequestTo != "" {
			return formatRequestTo
		}
	}
	source := opts.SourceFormat.String()
	if source == "openai-image" || source == "openai-video" {
		return opts.SourceFormat
	}
	if opts.Alt == "responses/compact" && !opts.Stream {
		return sdktranslator.FormatOpenAIResponse
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex":
		return sdktranslator.FormatCodex
	case "xai":
		return sdktranslator.FormatCodex
	case "claude":
		return sdktranslator.FormatClaude
	case "gemini", "vertex", "aistudio":
		return sdktranslator.FormatGemini
	case "kimi":
		return sdktranslator.FormatOpenAI
	case "antigravity":
		return sdktranslator.FormatAntigravity
	default:
		return sdktranslator.FormatOpenAI
	}
}

func cloneRequestHeaders(src http.Header) http.Header {
	if src == nil {
		return nil
	}
	dst := make(http.Header, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func mergeRequestHeaders(current, updates http.Header, clear []string) http.Header {
	if updates == nil && len(clear) == 0 {
		return current
	}
	out := cloneRequestHeaders(current)
	if out == nil && (len(updates) > 0 || len(clear) > 0) {
		out = make(http.Header)
	}
	for _, key := range clear {
		out.Del(key)
	}
	for key, values := range updates {
		out.Del(key)
		for _, value := range values {
			out.Add(key, value)
		}
	}
	return out
}

// acquireAuthInFlight increments the in-flight counter for a picked auth.
// Called at execution commit — after pool breaker admission — so probe-denied
// picks are never counted. No-op for nil manager/scheduler/auth and for auths
// unknown to the scheduler. The counter feeds the power-of-two-choices and
// least-used pick strategies (G4) and is otherwise informational.
func (m *Manager) acquireAuthInFlight(auth *Auth) {
	if m == nil || m.scheduler == nil || auth == nil {
		return
	}
	m.scheduler.adjustInFlight(auth.ID, 1)
}

// releaseAuthInFlight decrements the in-flight counter for a completed auth.
// Exactly one call must follow every acquire: the execution loops release at
// the next iteration boundary or function exit, and streaming attempts hand
// the release to the stream drain goroutine (the stream stays in flight until
// it drains). Calling it for an auth that was never acquired is a no-op.
func (m *Manager) releaseAuthInFlight(auth *Auth) {
	if m == nil || m.scheduler == nil || auth == nil {
		return
	}
	m.scheduler.adjustInFlight(auth.ID, -1)
}

// waitForCapacity performs one bounded wait on the Task-5 capacity path. It
// runs in the execution loop, never under the scheduler lock. Tests install
// the capacityWaitFn seam to stay deterministic; production sleeps at most
// min(budget, capacityWaitIncrement), honoring ctx cancellation. Returns true
// when the loop should re-pick (a slot may have freed); false only when ctx is
// canceled — the caller then falls through to the existing error return.
func (m *Manager) waitForCapacity(ctx context.Context, budget time.Duration) bool {
	if m != nil && m.capacityWaitFn != nil {
		return m.capacityWaitFn(ctx, budget)
	}
	wait := capacityWaitIncrement
	if budget > 0 && budget < wait {
		wait = budget
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// resolveCapacityWaitBudget resolves the request's capacity wait budget from
// the registered auths serving the requested providers: the first of them
// carrying a positive max_wait_ms wins, else DefaultCapacityWaitMS. Called
// lazily on the first ErrEntryCapacity pick of a request. Resolving from the
// candidate auth set (rather than the pick result, which carries none) keeps
// the budget sensible per request without scheduler changes.
func (m *Manager) resolveCapacityWaitBudget(providers []string) time.Duration {
	if m == nil {
		return time.Duration(DefaultCapacityWaitMS) * time.Millisecond
	}
	wanted := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		key := strings.TrimSpace(strings.ToLower(provider))
		if key != "" {
			wanted[key] = struct{}{}
		}
	}
	candidates := make([]*Auth, 0)
	m.mu.RLock()
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		if _, ok := wanted[strings.TrimSpace(strings.ToLower(auth.Provider))]; ok {
			candidates = append(candidates, auth)
		}
	}
	m.mu.RUnlock()
	// Locking discipline: the collected *Auth pointers are snapshots, and only
	// their attribute values are read afterwards (maxWaitForAuth reads
	// max_wait_ms off Attributes). Register/Update publish a fresh Clone() and
	// swap the map entry wholesale — they never mutate a registered auth's
	// Attributes in place — so a stale snapshot stays a valid, consistent read
	// even when a concurrent Register/Update replaces or removes the entry
	// after we drop the lock. Do not dereference any other field or mutate the
	// snapshot outside the lock.
	return maxWaitForAuth(candidates...)
}

// wrapStreamDrainRelease wraps a scheduler-picked stream so the attempt's
// in-flight hold is released exactly once when the stream finishes draining.
// On client cancellation it stops FORWARDING but keeps draining the wrapped
// stream until its underlying drain goroutine (which records results)
// closes the channel: releasing the hold before that close would let a
// result-observing caller read results that the drain has not recorded yet.
// A stalled upstream that never delivers another chunk or close keeps the
// hold — mirroring the pre-existing drain-goroutine leak rather than
// pretending the dispatch ended. A nil ctx is tolerated (no cancellation
// source), matching wrapStreamResult's nil-ctx handling.
func wrapStreamDrainRelease(ctx context.Context, result *cliproxyexecutor.StreamResult, release func()) *cliproxyexecutor.StreamResult {
	if release == nil {
		return result
	}
	if result == nil || result.Chunks == nil {
		release()
		return result
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer release()
		forward := true
		for chunk := range result.Chunks {
			if !forward {
				continue
			}
			if ctx == nil {
				out <- chunk
				continue
			}
			select {
			case <-ctx.Done():
				forward = false
			case out <- chunk:
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: result.Headers, Chunks: out}
}

func (m *Manager) executeMixedOnce(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, maxRetryCredentials int) (cliproxyexecutor.Response, error, *Auth) {
	if len(providers) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}, nil
	}
	routeModel := authSelectionModelFromOptions(opts, req.Model)
	executionModel, restoreExecutionModel := executionModelForAuthSelection(opts, req.Model)
	opts = ensureRequestedModelMetadata(opts, routeModel)
	homeMode := m.HomeEnabled()
	homeAuthCount := 1
	tried := make(map[string]struct{})
	attempted := make(map[string]struct{})
	var lastErr error
	// lastAuth tracks the most recently attempted auth so the outer retry
	// loop can stamp a non-empty AuthID onto routing.attempts_exhausted when
	// the inner loop gives up. Updated alongside attempted[auth.ID] below;
	// nil when the inner loop bails before picking any auth (e.g. no
	// candidates, executor_not_found), which is fine — the outer loop only
	// emits attempts_exhausted when lastErr != nil, and a no-candidates
	// failure carries no auth to report.
	var lastAuth *Auth
	pinnedPool := ""
	// capacityBudget is the Task-5 wait-then-failover budget: when the picker
	// reports ErrEntryCapacity (every eligible candidate at/over its per-entry
	// concurrency cap), the loop waits bounded increments then re-picks instead
	// of failing immediately. The budget is resolved lazily on the first
	// capacity pick (no cost on the common path) and decayed across picks so
	// the TOTAL wait stays within the entry's max_wait_ms. On expiry the loop
	// falls through to the existing error return unchanged (D6).
	var capacityBudget time.Duration
	capacityBudgetResolved := false
	// innerOpts projects routing.retry into the per-entry retry loop. The
	// loop is only entered when MaxAttempts >= 2; below that the inline
	// single-attempt path runs untouched, so a default config never
	// stamps the EntryBudget deadline onto execCtx (AGENTS.md: no timeouts
	// after the upstream connection).
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	innerOpts := innerLoopOptsFromConfig(cfg)
	// releaseInFlight releases the current attempt's in-flight hold exactly
	// once: at the top of the next loop iteration when the attempt rotates to
	// another auth, or at function exit via the deferred closure (covers
	// every return and panic after the acquire).
	var releaseInFlight func()
	defer func() {
		if releaseInFlight != nil {
			releaseInFlight()
		}
	}()
	for {
		if releaseInFlight != nil {
			releaseInFlight()
			releaseInFlight = nil
		}
		if !homeMode && maxRetryCredentials > 0 && len(attempted) >= maxRetryCredentials {
			if lastErr != nil {
				return cliproxyexecutor.Response{}, lastErr, lastAuth
			}
			return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}, lastAuth
		}
		pickOpts := opts
		if homeMode {
			pickOpts = withHomeAuthCount(opts, homeAuthCount)
		}
		auth, executor, provider, errPick := m.pickNextMixed(ctx, poolPickProviders(providers, pinnedPool), routeModel, pickOpts, tried)
		if errPick != nil {
			if isEntryCapacityError(errPick) {
				if !capacityBudgetResolved {
					capacityBudgetResolved = true
					capacityBudget = m.resolveCapacityWaitBudget(providers)
				}
				if capacityBudget > 0 {
					// Wait at most one increment (≤50ms) per re-pick, decaying
					// the budget by exactly what was waited so the TOTAL wait
					// stays bounded by the entry's max_wait_ms. Passing the
					// full budget here would sleep the whole budget in one
					// long shot, defeating the granularity.
					increment := capacityWaitIncrement
					if capacityBudget < increment {
						increment = capacityBudget
					}
					if !m.waitForCapacity(ctx, increment) {
						// Context canceled during the bounded wait: fall
						// through to the existing error return unchanged
						// (D6 keeps the no-auth / 503 shape).
						return cliproxyexecutor.Response{}, errPick, lastAuth
					}
					capacityBudget -= increment
					// Budget remains: re-pick immediately. A slot may have
					// freed while we waited.
					continue
				}
				// Budget exhausted: fall through to the existing error return
				// unchanged (D6).
			}
			if shouldReturnLastErrorOnPickFailure(homeMode, lastErr, errPick) {
				return cliproxyexecutor.Response{}, lastErr, lastAuth
			}
			return cliproxyexecutor.Response{}, errPick, lastAuth
		}

		entry := logEntryWithRequestID(ctx)
		debugLogAuthSelection(entry, auth, provider, routeModel)
		publishSelectedAuthMetadata(opts.Metadata, auth)

		tried[auth.ID] = struct{}{}
		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		execCtx = contextWithRequestedModelAlias(execCtx, opts, routeModel)

		models, pooled, aliasResult, routing := m.preparedExecutionModelsWithAlias(auth, routeModel)
		if len(models) == 0 {
			continue
		}
		attempted[auth.ID] = struct{}{}
		lastAuth = auth
		var errPrepare error
		auth, errPrepare = m.prepareRequestAuth(execCtx, executor, auth)
		if errPrepare != nil {
			if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errPrepare); errCancel != nil {
				return cliproxyexecutor.Response{}, errCancel, lastAuth
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: routeModel, Success: false, Error: resultErrorFromError(errPrepare)}
			m.MarkResult(execCtx, result)
			lastErr = errPrepare
			continue
		}
		if !poolBreakerAdmitsDispatch(auth) {
			// Another request holds the pool's probe slot, or the breaker
			// re-opened between pick and dispatch; rotate to another auth.
			// Admission happens only here — at execution commit — so an
			// admitted probe always corresponds to a real dispatch.
			continue
		}
		acquiredAuth := auth
		m.acquireAuthInFlight(acquiredAuth)
		releaseInFlight = func() { m.releaseAuthInFlight(acquiredAuth) }
		var authErr error
		didRefreshOnUnauthorized := false
		for _, upstreamModel := range models {
			resultModel := m.stateModelForExecution(auth, routeModel, upstreamModel, pooled)
			execReq := req
			execReq.Model = upstreamModel
			if restoreExecutionModel {
				execReq.Model = executionModel
			}
			execOpts := opts
			var errIntercept error
			execReq, execOpts, errIntercept = applyRequestAfterAuthInterceptor(execCtx, executor, provider, execReq, execOpts, requestedModelAliasFromOptions(execOpts, routeModel))
			if errIntercept != nil {
				return cliproxyexecutor.Response{}, errIntercept, lastAuth
			}
			if !restoreExecutionModel {
				execReq = attachResolvedAPIKeyModelInfo(routing, execReq, auth, routeModel, upstreamModel)
			}
			var resp cliproxyexecutor.Response
			var errExec error
			// runAttempt performs one upstream attempt, including the
			// unauthorized-refresh retry exactly as the inline path did.
			// The final error of the attempt lands in the captured errExec
			// and the response in resp; auth / didRefreshOnUnauthorized are
			// mutated across attempts just as they were before. execCtx is
			// threaded through on the inline path and the budget-bounded
			// child from RunInnerLoop on the retry path.
			runAttempt := func(attemptCtx context.Context) {
				attemptResp, attemptErr := executor.Execute(attemptCtx, auth, execReq, execOpts)
				if attemptErr != nil {
					if errCtx := attemptCtx.Err(); errCtx != nil {
						resp, errExec = attemptResp, attemptErr
						return
					}
					if refreshed, okRefresh := m.tryRefreshAfterUnauthorized(attemptCtx, auth, attemptErr, didRefreshOnUnauthorized); okRefresh {
						auth = refreshed
						didRefreshOnUnauthorized = true
						attemptResp, attemptErr = executor.Execute(attemptCtx, auth, execReq, execOpts)
						if attemptErr != nil {
							if errCtx := attemptCtx.Err(); errCtx != nil {
								resp, errExec = attemptResp, attemptErr
								return
							}
						}
					}
				}
				resp, errExec = attemptResp, attemptErr
			}
			if innerOpts.MaxAttempts >= 2 {
				// The retry loop writes resp/errExec from the FINAL attempt,
				// so MarkResult below still runs exactly once per credential
				// iteration. A success must carry an explicit 2xx status:
				// RunInnerLoop counts success only as Err==nil plus a 2xx
				// status, and a zero Status would be misread as a failure.
				// The loop is driven through runInnerLoopFn (seam: see
				// retry_loop.go) so tests can pin degenerate outcomes.
				loopStart := time.Now()
				outcome := runInnerLoopFn(execCtx, innerOpts, func(attemptCtx context.Context) InnerAttemptResult {
					runAttempt(attemptCtx)
					if errExec == nil {
						return InnerAttemptResult{Status: http.StatusOK}
					}
					return InnerAttemptResult{Status: statusCodeFromError(errExec), Err: errExec}
				})
				if outcome.Attempts == 0 {
					// The budget expired before any attempt ran; surface it
					// as a failed attempt rather than a false success.
					// Retryable mirrors the other conductor-synthesized
					// transient codes (home_unavailable, empty_stream).
					errExec = &Error{Code: "entry_budget_exhausted", Message: "per-entry retry budget expired before the first attempt", Retryable: true}
				}
				if outcome.Attempts != 1 && log.IsLevelEnabled(log.DebugLevel) {
					// Log multi-attempt exits plus the degenerate zero-attempt
					// exit so entry_retry_reason covers every non-trivial path.
					LogInnerLoopResult(logEntryWithRequestID(execCtx), upstreamModel, auth.ID, outcome, time.Since(loopStart), innerOpts.MaxAttempts, innerOpts.MaxTimeMS)
				}
			} else {
				runAttempt(execCtx)
			}
			// Parent-context check once after both branches, matching
			// today's placement semantics: only a failed attempt surfaces a
			// canceled execCtx, and budget expiry (deadline on the loop's
			// child context) never masquerades as client cancellation.
			if errExec != nil {
				if errCtx := execCtx.Err(); errCtx != nil {
					return cliproxyexecutor.Response{}, errCtx, lastAuth
				}
			}
			if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errExec); errCancel != nil {
				return cliproxyexecutor.Response{}, errCancel, lastAuth
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: errExec == nil}
			if errExec != nil {
				result.Error = resultErrorFromError(errExec)
				if ra := retryAfterFromError(errExec); ra != nil {
					result.RetryAfter = ra
				}
				m.MarkResult(execCtx, result)
				if isRequestInvalidError(errExec) && poolStrategyFromAuth(auth) == "" {
					return cliproxyexecutor.Response{}, errExec, lastAuth
				}
				authErr = errExec
				continue
			}
			m.MarkResult(execCtx, result)
			attemptAliasResult := resolveAttemptAliasResult(routing, auth, routeModel, upstreamModel, aliasResult)
			rewriteForceMappedResponse(&resp, attemptAliasResult)
			decision := m.decisionFor(auth, provider, routeModel, len(attempted))
			setResponseDecisionHeader(&resp, decision)
			emitRoutingDecision(logging.GetRequestID(execCtx), routeModel, auth, decision)
			return resp, nil, lastAuth
		}
		if authErr != nil {
			if isRequestInvalidError(authErr) && poolStrategyFromAuth(auth) == "" {
				return cliproxyexecutor.Response{}, authErr, lastAuth
			}
			lastErr = authErr
			pinPoolFromFailedAuth(auth, &pinnedPool)
			if homeMode {
				homeAuthCount++
			}
			continue
		}
	}
}

func (m *Manager) executeCountMixedOnce(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, maxRetryCredentials int) (cliproxyexecutor.Response, error, *Auth) {
	if len(providers) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}, nil
	}
	routeModel := authSelectionModelFromOptions(opts, req.Model)
	executionModel, restoreExecutionModel := executionModelForAuthSelection(opts, req.Model)
	opts = ensureRequestedModelMetadata(opts, routeModel)
	homeMode := m.HomeEnabled()
	homeAuthCount := 1
	tried := make(map[string]struct{})
	attempted := make(map[string]struct{})
	var lastErr error
	// See executeMixedOnce for the rationale behind tracking lastAuth here.
	var lastAuth *Auth
	pinnedPool := ""
	// capacityBudget: see executeMixedOnce. Bounded wait-then-failover on
	// ErrEntryCapacity, decayed across picks within the entry's max_wait_ms.
	var capacityBudget time.Duration
	capacityBudgetResolved := false
	// innerOpts: see executeMixedOnce for the retry-loop rationale (only
	// MaxAttempts >= 2 enters RunInnerLoop; below that the inline path runs
	// untouched with no added deadline).
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	innerOpts := innerLoopOptsFromConfig(cfg)
	// releaseInFlight releases the current attempt's in-flight hold exactly
	// once (see executeMixedOnce for the full rationale).
	var releaseInFlight func()
	defer func() {
		if releaseInFlight != nil {
			releaseInFlight()
		}
	}()
	for {
		if releaseInFlight != nil {
			releaseInFlight()
			releaseInFlight = nil
		}
		if !homeMode && maxRetryCredentials > 0 && len(attempted) >= maxRetryCredentials {
			if lastErr != nil {
				return cliproxyexecutor.Response{}, lastErr, lastAuth
			}
			return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}, lastAuth
		}
		pickOpts := opts
		if homeMode {
			pickOpts = withHomeAuthCount(opts, homeAuthCount)
		}
		auth, executor, provider, errPick := m.pickNextMixed(ctx, poolPickProviders(providers, pinnedPool), routeModel, pickOpts, tried)
		if errPick != nil {
			if isEntryCapacityError(errPick) {
				if !capacityBudgetResolved {
					capacityBudgetResolved = true
					capacityBudget = m.resolveCapacityWaitBudget(providers)
				}
				if capacityBudget > 0 {
					// Wait at most one increment (≤50ms) per re-pick, decaying
					// the budget by exactly what was waited so the TOTAL wait
					// stays bounded by the entry's max_wait_ms. Passing the
					// full budget here would sleep the whole budget in one
					// long shot, defeating the granularity.
					increment := capacityWaitIncrement
					if capacityBudget < increment {
						increment = capacityBudget
					}
					if !m.waitForCapacity(ctx, increment) {
						// Context canceled during the bounded wait: fall
						// through to the existing error return unchanged
						// (D6 keeps the no-auth / 503 shape).
						return cliproxyexecutor.Response{}, errPick, lastAuth
					}
					capacityBudget -= increment
					// Budget remains: re-pick immediately. A slot may have
					// freed while we waited.
					continue
				}
				// Budget exhausted: fall through to the existing error return
				// unchanged (D6).
			}
			if shouldReturnLastErrorOnPickFailure(homeMode, lastErr, errPick) {
				return cliproxyexecutor.Response{}, lastErr, lastAuth
			}
			return cliproxyexecutor.Response{}, errPick, lastAuth
		}

		entry := logEntryWithRequestID(ctx)
		debugLogAuthSelection(entry, auth, provider, routeModel)
		publishSelectedAuthMetadata(opts.Metadata, auth)

		tried[auth.ID] = struct{}{}
		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		execCtx = contextWithRequestedModelAlias(execCtx, opts, routeModel)

		models, pooled, aliasResult, routing := m.preparedExecutionModelsWithAlias(auth, routeModel)
		if len(models) == 0 {
			continue
		}
		attempted[auth.ID] = struct{}{}
		lastAuth = auth
		var errPrepare error
		auth, errPrepare = m.prepareRequestAuth(execCtx, executor, auth)
		if errPrepare != nil {
			if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errPrepare); errCancel != nil {
				return cliproxyexecutor.Response{}, errCancel, lastAuth
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: routeModel, Success: false, Error: resultErrorFromError(errPrepare)}
			m.MarkResult(execCtx, result)
			lastErr = errPrepare
			continue
		}
		if !poolBreakerAdmitsDispatch(auth) {
			// Another request holds the pool's probe slot, or the breaker
			// re-opened between pick and dispatch; rotate to another auth.
			// Admission happens only here — at execution commit — so an
			// admitted probe always corresponds to a real dispatch.
			continue
		}
		acquiredAuth := auth
		m.acquireAuthInFlight(acquiredAuth)
		releaseInFlight = func() { m.releaseAuthInFlight(acquiredAuth) }
		var authErr error
		didRefreshOnUnauthorized := false
		for _, upstreamModel := range models {
			resultModel := m.stateModelForExecution(auth, routeModel, upstreamModel, pooled)
			execReq := req
			execReq.Model = upstreamModel
			if restoreExecutionModel {
				execReq.Model = executionModel
			}
			execOpts := opts
			var errIntercept error
			execReq, execOpts, errIntercept = applyRequestAfterAuthInterceptor(execCtx, executor, provider, execReq, execOpts, requestedModelAliasFromOptions(execOpts, routeModel))
			if errIntercept != nil {
				return cliproxyexecutor.Response{}, errIntercept, lastAuth
			}
			if !restoreExecutionModel {
				execReq = attachResolvedAPIKeyModelInfo(routing, execReq, auth, routeModel, upstreamModel)
			}
			var resp cliproxyexecutor.Response
			var errExec error
			// runAttempt: see executeMixedOnce for the full rationale. The
			// final error lands in the captured errExec and the response in
			// resp; auth / didRefreshOnUnauthorized mutate across attempts.
			runAttempt := func(attemptCtx context.Context) {
				attemptResp, attemptErr := executor.CountTokens(attemptCtx, auth, execReq, execOpts)
				if attemptErr != nil {
					if errCtx := attemptCtx.Err(); errCtx != nil {
						resp, errExec = attemptResp, attemptErr
						return
					}
					if refreshed, okRefresh := m.tryRefreshAfterUnauthorized(attemptCtx, auth, attemptErr, didRefreshOnUnauthorized); okRefresh {
						auth = refreshed
						didRefreshOnUnauthorized = true
						attemptResp, attemptErr = executor.CountTokens(attemptCtx, auth, execReq, execOpts)
						if attemptErr != nil {
							if errCtx := attemptCtx.Err(); errCtx != nil {
								resp, errExec = attemptResp, attemptErr
								return
							}
						}
					}
				}
				resp, errExec = attemptResp, attemptErr
			}
			if innerOpts.MaxAttempts >= 2 {
				// The retry loop writes resp/errExec from the FINAL attempt,
				// so the single result marking below still runs exactly once
				// per credential iteration. Success must carry an explicit
				// 2xx status for RunInnerLoop classification. The loop is
				// driven through runInnerLoopFn (seam: see retry_loop.go) so
				// tests can pin degenerate outcomes.
				loopStart := time.Now()
				outcome := runInnerLoopFn(execCtx, innerOpts, func(attemptCtx context.Context) InnerAttemptResult {
					runAttempt(attemptCtx)
					if errExec == nil {
						return InnerAttemptResult{Status: http.StatusOK}
					}
					return InnerAttemptResult{Status: statusCodeFromError(errExec), Err: errExec}
				})
				if outcome.Attempts == 0 {
					// The budget expired before any attempt ran; surface it
					// as a failed attempt rather than a false success.
					// Retryable mirrors the other conductor-synthesized
					// transient codes (home_unavailable, empty_stream).
					errExec = &Error{Code: "entry_budget_exhausted", Message: "per-entry retry budget expired before the first attempt", Retryable: true}
				}
				if outcome.Attempts != 1 && log.IsLevelEnabled(log.DebugLevel) {
					// Log multi-attempt exits plus the degenerate zero-attempt
					// exit so entry_retry_reason covers every non-trivial path.
					LogInnerLoopResult(logEntryWithRequestID(execCtx), upstreamModel, auth.ID, outcome, time.Since(loopStart), innerOpts.MaxAttempts, innerOpts.MaxTimeMS)
				}
			} else {
				runAttempt(execCtx)
			}
			// Parent-context check once after both branches, matching
			// today's placement semantics: only a failed attempt surfaces a
			// canceled execCtx, and budget expiry (deadline on the loop's
			// child context) never masquerades as client cancellation.
			if errExec != nil {
				if errCtx := execCtx.Err(); errCtx != nil {
					return cliproxyexecutor.Response{}, errCtx, lastAuth
				}
			}
			if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errExec); errCancel != nil {
				return cliproxyexecutor.Response{}, errCancel, lastAuth
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: errExec == nil}
			if errExec != nil {
				result.Error = resultErrorFromError(errExec)
				if ra := retryAfterFromError(errExec); ra != nil {
					result.RetryAfter = ra
				}
				// Some Anthropic-compatible upstreams do not implement the
				// count_tokens route and return a generic endpoint 404. Record
				// the failure for hooks and metrics without suspending a model
				// that remains usable through the messages endpoint.
				if isCountTokensEndpointNotFoundError(errExec, execReq.Model) {
					m.recordAvailabilityNeutralResult(execCtx, result)
				} else {
					m.MarkResult(execCtx, result)
				}
				if isRequestInvalidError(errExec) && poolStrategyFromAuth(auth) == "" {
					return cliproxyexecutor.Response{}, errExec, lastAuth
				}
				authErr = errExec
				continue
			}
			m.MarkResult(execCtx, result)
			attemptAliasResult := resolveAttemptAliasResult(routing, auth, routeModel, upstreamModel, aliasResult)
			rewriteForceMappedResponse(&resp, attemptAliasResult)
			decision := m.decisionFor(auth, provider, routeModel, len(attempted))
			setResponseDecisionHeader(&resp, decision)
			emitRoutingDecision(logging.GetRequestID(execCtx), routeModel, auth, decision)
			return resp, nil, lastAuth
		}
		if authErr != nil {
			if isRequestInvalidError(authErr) && poolStrategyFromAuth(auth) == "" {
				return cliproxyexecutor.Response{}, authErr, lastAuth
			}
			lastErr = authErr
			pinPoolFromFailedAuth(auth, &pinnedPool)
			if homeMode {
				homeAuthCount++
			}
			continue
		}
	}
}

func (m *Manager) executeStreamMixedOnce(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, maxRetryCredentials int) (*cliproxyexecutor.StreamResult, error, *Auth) {
	if len(providers) == 0 {
		return nil, &Error{Code: "provider_not_found", Message: "no provider supplied"}, nil
	}
	routeModel := authSelectionModelFromOptions(opts, req.Model)
	responseAlias := requestedModelAliasFromOptions(opts, routeModel)
	executionModel, restoreExecutionModel := executionModelForAuthSelection(opts, req.Model)
	opts = ensureRequestedModelMetadata(opts, routeModel)
	homeMode := m.HomeEnabled()
	homeAuthCount := 1
	tried := make(map[string]struct{})
	attempted := make(map[string]struct{})
	unauthorizedRefreshTried := make(map[string]struct{})
	var lastErr error
	// See executeMixedOnce for the rationale behind tracking lastAuth here.
	var lastAuth *Auth
	pinnedPool := ""
	// capacityBudget: see executeMixedOnce. Bounded wait-then-failover on
	// ErrEntryCapacity, decayed across picks within the entry's max_wait_ms.
	var capacityBudget time.Duration
	capacityBudgetResolved := false
	// releaseInFlight releases the current attempt's in-flight hold exactly
	// once: at the top of the next loop iteration, at function exit via the
	// deferred closure, or — for a successfully started stream — by handing
	// ownership to the stream drain wrapper, which releases when the stream
	// the client holds finishes draining.
	var releaseInFlight func()
	defer func() {
		if releaseInFlight != nil {
			releaseInFlight()
		}
	}()
	for {
		if releaseInFlight != nil {
			releaseInFlight()
			releaseInFlight = nil
		}
		if !homeMode && maxRetryCredentials > 0 && len(attempted) >= maxRetryCredentials {
			if lastErr != nil {
				return nil, lastErr, lastAuth
			}
			return nil, &Error{Code: "auth_not_found", Message: "no auth available"}, lastAuth
		}
		pickOpts := opts
		if homeMode {
			pickOpts = withHomeAuthCount(opts, homeAuthCount)
		}

		var selection *HomeDispatchSelection
		var auth *Auth
		var executor ProviderExecutor
		var provider string
		var errPick error
		if homeMode {
			selection, errPick = m.pickHomeDispatchSelection(ctx, routeModel, pickOpts)
			if selection != nil {
				auth = selection.CloneAuthForRoute(routeModel)
				executor = selection.Executor
				provider = selection.Provider
			}
		} else {
			auth, executor, provider, errPick = m.pickNextMixed(ctx, poolPickProviders(providers, pinnedPool), routeModel, pickOpts, tried)
		}
		if errPick != nil {
			if isEntryCapacityError(errPick) {
				if !capacityBudgetResolved {
					capacityBudgetResolved = true
					capacityBudget = m.resolveCapacityWaitBudget(providers)
				}
				if capacityBudget > 0 {
					// Wait at most one increment (≤50ms) per re-pick, decaying
					// the budget by exactly what was waited so the TOTAL wait
					// stays bounded by the entry's max_wait_ms. Passing the
					// full budget here would sleep the whole budget in one
					// long shot, defeating the granularity.
					increment := capacityWaitIncrement
					if capacityBudget < increment {
						increment = capacityBudget
					}
					if !m.waitForCapacity(ctx, increment) {
						// Context canceled during the bounded wait: fall
						// through to the existing error return unchanged
						// (D6 keeps the no-auth / 503 shape).
						return nil, errPick, lastAuth
					}
					capacityBudget -= increment
					// Budget remains: re-pick immediately. A slot may have
					// freed while we waited.
					continue
				}
				// Budget exhausted: fall through to the existing error return
				// unchanged (D6).
			}
			if shouldReturnLastErrorOnPickFailure(homeMode, lastErr, errPick) {
				return nil, lastErr, lastAuth
			}
			return nil, errPick, lastAuth
		}
		if auth == nil || executor == nil {
			if selection != nil {
				selection.End("missing_execution_target")
			}
			return nil, &Error{Code: "executor_not_found", Message: "executor not registered"}, lastAuth
		}
		if selection != nil {
			if _, refreshedAlready := unauthorizedRefreshTried[auth.ID]; refreshedAlready {
				selection.End("repeated_refresh_auth")
				if lastErr != nil {
					return nil, lastErr, lastAuth
				}
				return nil, repeatedHomeAuthError(), lastAuth
			}
		}

		entry := logEntryWithRequestID(ctx)
		debugLogAuthSelection(entry, auth, provider, routeModel)
		if selection != nil {
			if errRuntimeAuth := m.bindHomeSelectionRuntimeAuth(ctx, opts, selection); errRuntimeAuth != nil {
				selection.End("runtime_auth_bind_failed")
				return nil, errRuntimeAuth, lastAuth
			}
		}
		publishSelectedAuthMetadata(opts.Metadata, auth)

		tried[auth.ID] = struct{}{}
		execCtx := ctx
		releaseAttempt := func() {}
		if selection != nil {
			var errBind error
			execCtx, releaseAttempt, errBind = homeExecutionAttemptContext(ctx, selection)
			if errBind != nil {
				selection.End("attempt_bind_failed")
				return nil, errBind, lastAuth
			}
		}
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		// Enrich before auth preparation so prepare-stage usage records observe the client request.
		execCtx = contextWithRequestedModelAlias(execCtx, opts, routeModel)
		models, pooled, aliasResult, routing := m.preparedExecutionModelsWithAlias(auth, routeModel)
		if selection != nil && aliasResult.ForceMapping && responseAlias != "" {
			aliasResult.OriginalAlias = responseAlias
		}
		if len(models) == 0 {
			if selection != nil {
				releaseAttempt()
				if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, "no_execution_models"); errEnd != nil {
					return nil, errEnd, lastAuth
				}
			}
			continue
		}
		attempted[auth.ID] = struct{}{}
		lastAuth = auth
		var errPrepare error
		if selection != nil {
			auth, errPrepare = m.prepareHomeRequestAuth(execCtx, executor, selection)
		} else {
			auth, errPrepare = m.prepareRequestAuth(execCtx, executor, auth)
		}
		if errPrepare != nil {
			if selection == nil {
				if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errPrepare); errCancel != nil {
					return nil, errCancel, lastAuth
				}
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: routeModel, Success: false, Error: resultErrorFromError(errPrepare)}
			if selection != nil {
				m.reportHomeResult(execCtx, result, auth)
				releaseAttempt()
			} else {
				m.MarkResult(execCtx, result)
			}
			lastErr = errPrepare
			if selection != nil {
				if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, "prepare_failed"); errEnd != nil {
					return nil, errEnd, lastAuth
				}
			}
			continue
		}
		if !poolBreakerAdmitsDispatch(auth) {
			// Another request holds the pool's probe slot, or the breaker
			// re-opened between pick and dispatch; rotate to another auth.
			// Admission happens only here — at execution commit — so an
			// admitted probe always corresponds to a real dispatch.
			if selection != nil {
				releaseAttempt()
				if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, "pool_breaker_probe_denied"); errEnd != nil {
					return nil, errEnd, lastAuth
				}
			}
			if homeMode {
				// Advance the Home auth count so the next dispatch asks Home
				// for a different credential instead of re-picking this one
				// in a tight loop until the probe window expires.
				homeAuthCount++
			}
			continue
		}
		if selection == nil {
			// Scheduler-picked dispatch (home dispatches are out of scope for
			// the in-flight counter; see the G4 design). The hold is owned by
			// releaseInFlight until a successful stream start hands it to the
			// drain wrapper.
			m.acquireAuthInFlight(auth)
			acquiredAuth := auth
			releaseInFlight = func() { m.releaseAuthInFlight(acquiredAuth) }
		}
		execReq := sanitizeDownstreamWebsocketFallbackRequest(execCtx, auth, req)
		streamExecutionModel := ""
		if restoreExecutionModel {
			streamExecutionModel = executionModel
		}
		execOpts := opts
		if selection != nil {
			execOpts.ExecutionLifecycle = selection
		}
		if homeMode && len(models) > 1 {
			models = models[:1]
			pooled = false
		}
		streamResult, errStream := m.executeStreamWithModelPool(execCtx, executor, auth, provider, execReq, execOpts, routeModel, streamExecutionModel, models, pooled, aliasResult, routing, !homeMode || selection != nil, selection != nil, unauthorizedRefreshTried)
		if errStream != nil {
			// The attempt ends without a stream: releaseInFlight stays armed
			// for the next iteration / function exit.
			if selection != nil {
				releaseAttempt()
				if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, "stream_start_failed"); errEnd != nil {
					return nil, errEnd, lastAuth
				}
			}
			if errCtx := execCtx.Err(); errCtx != nil && ctx != nil && ctx.Err() != nil {
				return nil, errCtx, lastAuth
			}
			if isRequestInvalidError(errStream) && poolStrategyFromAuth(auth) == "" {
				return nil, errStream, lastAuth
			}
			lastErr = errStream
			pinPoolFromFailedAuth(auth, &pinnedPool)
			if homeMode {
				homeAuthCount++
			}
			continue
		}
		if selection != nil {
			if m.retainHomeWebsocketSelection(ctx, opts, routeModel, selection) {
				return wrapHomeStream(ctx, streamResult, nil, releaseAttempt), nil, lastAuth
			}
			return wrapHomeStream(ctx, streamResult, selection, releaseAttempt), nil, lastAuth
		}
		// Scheduler-picked stream: the in-flight hold transfers to the stream
		// drain wrapper and is released when the client-side stream finishes
		// draining (including on client disconnect, where no result is ever
		// recorded). Clear the local release so it is not released twice.
		streamRelease := releaseInFlight
		releaseInFlight = nil
		return wrapStreamDrainRelease(ctx, streamResult, streamRelease), nil, lastAuth
	}
}

func ensureRequestedModelMetadata(opts cliproxyexecutor.Options, requestedModel string) cliproxyexecutor.Options {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return opts
	}
	if hasRequestedModelMetadata(opts.Metadata) {
		return opts
	}
	if len(opts.Metadata) == 0 {
		opts.Metadata = map[string]any{cliproxyexecutor.RequestedModelMetadataKey: requestedModel}
		return opts
	}
	meta := make(map[string]any, len(opts.Metadata)+1)
	for k, v := range opts.Metadata {
		meta[k] = v
	}
	meta[cliproxyexecutor.RequestedModelMetadataKey] = requestedModel
	opts.Metadata = meta
	return opts
}

func authSelectionModelFromOptions(opts cliproxyexecutor.Options, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if len(opts.Metadata) == 0 {
		return fallback
	}
	raw, ok := opts.Metadata[cliproxyexecutor.AuthSelectionModelMetadataKey]
	if !ok || raw == nil {
		return fallback
	}
	switch value := raw.(type) {
	case string:
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	case []byte:
		if strings.TrimSpace(string(value)) != "" {
			return strings.TrimSpace(string(value))
		}
	}
	return fallback
}

func executionModelForAuthSelection(opts cliproxyexecutor.Options, model string) (string, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", false
	}
	selectionModel := authSelectionModelFromOptions(opts, model)
	if selectionModel == model {
		return "", false
	}
	return model, true
}

func withHomeAuthCount(opts cliproxyexecutor.Options, count int) cliproxyexecutor.Options {
	if count <= 0 {
		count = 1
	}
	meta := make(map[string]any, len(opts.Metadata)+1)
	for k, v := range opts.Metadata {
		meta[k] = v
	}
	meta[homeAuthCountMetadataKey] = count
	opts.Metadata = meta
	return opts
}

func homeAuthCountFromMetadata(meta map[string]any) int {
	if len(meta) == 0 {
		return 1
	}
	switch value := meta[homeAuthCountMetadataKey].(type) {
	case int:
		if value > 0 {
			return value
		}
	case int64:
		if value > 0 {
			return int(value)
		}
	case float64:
		if value > 0 {
			return int(value)
		}
	}
	return 1
}

func hasRequestedModelMetadata(meta map[string]any) bool {
	if len(meta) == 0 {
		return false
	}
	raw, ok := meta[cliproxyexecutor.RequestedModelMetadataKey]
	if !ok || raw == nil {
		return false
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v) != ""
	case []byte:
		return strings.TrimSpace(string(v)) != ""
	default:
		return false
	}
}

type requestAuthPrepareLock struct {
	mu sync.Mutex
}

// prepareHomeRequestAuth prepares a dispatch auth without reading or updating local auth state.
func (m *Manager) prepareHomeRequestAuth(ctx context.Context, executor ProviderExecutor, selection *HomeDispatchSelection) (*Auth, error) {
	if selection == nil {
		return nil, nil
	}
	return m.prepareHomeAuthSnapshot(ctx, executor, selection.CloneAuth())
}

func (m *Manager) prepareHomeAuthSnapshot(ctx context.Context, executor ProviderExecutor, auth *Auth) (*Auth, error) {
	if m == nil || executor == nil || auth == nil {
		return auth, nil
	}
	preparer, ok := executor.(RequestAuthPreparer)
	if !ok || preparer == nil || !preparer.ShouldPrepareRequestAuth(auth) {
		return auth, nil
	}

	prepare := func() (*Auth, error) {
		target := auth.Clone()
		if !preparer.ShouldPrepareRequestAuth(target) {
			return target, nil
		}
		updated, errPrepare := preparer.PrepareRequestAuth(ctx, target)
		if errPrepare != nil {
			return auth, errPrepare
		}
		if updated == nil {
			return target, nil
		}
		return updated, nil
	}

	id := strings.TrimSpace(auth.ID)
	if id == "" {
		return prepare()
	}
	lockValue, _ := m.requestPrepareLocks.LoadOrStore(id, &requestAuthPrepareLock{})
	lock, ok := lockValue.(*requestAuthPrepareLock)
	if !ok || lock == nil {
		return prepare()
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	return prepare()
}

func (m *Manager) prepareRequestAuth(ctx context.Context, executor ProviderExecutor, auth *Auth) (*Auth, error) {
	if m == nil || executor == nil || auth == nil {
		return auth, nil
	}
	preparer, ok := executor.(RequestAuthPreparer)
	if !ok || preparer == nil || !preparer.ShouldPrepareRequestAuth(auth) {
		return auth, nil
	}

	id := strings.TrimSpace(auth.ID)
	if id == "" {
		return preparer.PrepareRequestAuth(ctx, auth.Clone())
	}

	lockValue, _ := m.requestPrepareLocks.LoadOrStore(id, &requestAuthPrepareLock{})
	lock, ok := lockValue.(*requestAuthPrepareLock)
	if !ok || lock == nil {
		return preparer.PrepareRequestAuth(ctx, auth.Clone())
	}

	lock.mu.Lock()
	defer lock.mu.Unlock()

	target := auth.Clone()
	m.mu.RLock()
	if current := m.auths[id]; current != nil {
		target = current.Clone()
	}
	m.mu.RUnlock()

	if !preparer.ShouldPrepareRequestAuth(target) {
		return target, nil
	}

	updated, errPrepare := preparer.PrepareRequestAuth(ctx, target)
	if errPrepare != nil {
		return auth, errPrepare
	}
	if updated == nil {
		return target, nil
	}

	saved, errUpdate := m.Update(ctx, updated)
	if errUpdate != nil {
		return updated, errUpdate
	}
	if saved != nil {
		return saved, nil
	}
	return updated, nil
}

func contextWithRequestedModelAlias(ctx context.Context, opts cliproxyexecutor.Options, fallback string) context.Context {
	alias := requestedModelAliasFromOptions(opts, fallback)
	ctx = coreusage.WithRequestedModelAlias(ctx, alias)
	effort := reasoningEffortFromOptions(opts)
	if effort != "" {
		ctx = coreusage.WithReasoningEffort(ctx, effort)
	}
	serviceTier := serviceTierFromOptions(opts)
	if serviceTier != "" {
		ctx = coreusage.WithServiceTier(ctx, serviceTier)
	}
	if generate, ok := generateFromOptions(opts); ok {
		ctx = coreusage.WithGenerate(ctx, generate)
	}
	return ctx
}

func requestedModelAliasFromOptions(opts cliproxyexecutor.Options, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if len(opts.Metadata) == 0 {
		return fallback
	}
	raw, ok := opts.Metadata[cliproxyexecutor.RequestedModelMetadataKey]
	if !ok || raw == nil {
		return fallback
	}
	switch value := raw.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return fallback
		}
		return strings.TrimSpace(value)
	case []byte:
		if len(value) == 0 {
			return fallback
		}
		return strings.TrimSpace(string(value))
	default:
		return fallback
	}
}

func reasoningEffortFromOptions(opts cliproxyexecutor.Options) string {
	if len(opts.Metadata) == 0 {
		return ""
	}
	raw, ok := opts.Metadata[cliproxyexecutor.ReasoningEffortMetadataKey]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

func serviceTierFromOptions(opts cliproxyexecutor.Options) string {
	return stringMetadataValue(opts.Metadata, cliproxyexecutor.ServiceTierMetadataKey)
}

func generateFromOptions(opts cliproxyexecutor.Options) (bool, bool) {
	if len(opts.Metadata) == 0 {
		return false, false
	}
	raw, ok := opts.Metadata[cliproxyexecutor.GenerateMetadataKey]
	if !ok || raw == nil {
		return false, false
	}
	switch value := raw.(type) {
	case bool:
		return value, true
	default:
		return false, false
	}
}

func stringMetadataValue(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	raw, ok := metadata[key]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

func pinnedAuthIDFromMetadata(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	raw, ok := meta[cliproxyexecutor.PinnedAuthMetadataKey]
	if !ok || raw == nil {
		return ""
	}
	switch val := raw.(type) {
	case string:
		return strings.TrimSpace(val)
	case []byte:
		return strings.TrimSpace(string(val))
	default:
		return ""
	}
}

func disallowFreeAuthFromMetadata(meta map[string]any) bool {
	if len(meta) == 0 {
		return false
	}
	raw, ok := meta[cliproxyexecutor.DisallowFreeAuthMetadataKey]
	if !ok || raw == nil {
		return false
	}
	switch val := raw.(type) {
	case bool:
		return val
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(val))
		return err == nil && parsed
	case []byte:
		parsed, err := strconv.ParseBool(strings.TrimSpace(string(val)))
		return err == nil && parsed
	default:
		return false
	}
}

func isFreeCodexAuth(auth *Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes["plan_type"]), "free")
}

// routeStrategyFromMetadata maps the per-model routing strategy carried in
// execution metadata to the scheduler strategy. "priority" pins to the
// highest-priority provider (fill-first); "failover" enables round-robin
// across providers with the existing inner-loop failover on error. The
// canonical pool strategy spellings ("round-robin", "fill-first",
// "weighted-round-robin", "power-of-two-choices", "least-used") map to their
// scheduler counterparts so a pool row's strategy can serve as a route's
// default without translation. Any other value (including absence) yields
// schedulerStrategyCurrent, which means the configured global
// routing.strategy applies unchanged.
func routeStrategyFromMetadata(meta map[string]any) schedulerStrategy {
	if len(meta) == 0 {
		return schedulerStrategyCurrent
	}
	raw, ok := meta[cliproxyexecutor.RouteStrategyMetadataKey]
	if !ok || raw == nil {
		return schedulerStrategyCurrent
	}
	var strategy string
	switch val := raw.(type) {
	case string:
		strategy = strings.TrimSpace(val)
	case []byte:
		strategy = strings.TrimSpace(string(val))
	default:
		return schedulerStrategyCurrent
	}
	switch strings.ToLower(strategy) {
	case "priority", "fill-first":
		return schedulerStrategyFillFirst
	case "failover", "round-robin":
		return schedulerStrategyRoundRobin
	case "weighted-round-robin":
		return schedulerStrategyWeightedRoundRobin
	case "power-of-two-choices", "p2c", "two-random-choices":
		return schedulerStrategyP2C
	case "least-used", "least-busy":
		return schedulerStrategyLeastUsed
	default:
		return schedulerStrategyCurrent
	}
}

// poolStrategyFromAuth returns the in-pool routing strategy stamped by the
// synthesizer for entry-bearing provider pools. Non-empty means the auth's
// pool opted into aggressive failover: any entry error — including
// request-fault-classified ones — rotates to another entry of the same pool
// before the error surfaces to the client. Empty keeps the historical
// hard-stop on request-fault errors.
func poolStrategyFromAuth(a *Auth) string {
	if a == nil {
		return ""
	}
	return strings.TrimSpace(a.Attributes[AttributePoolStrategy])
}

// pinPoolFromFailedAuth records the routing key of a failed pool auth so the
// remaining iterations of the execution loop stay confined to that pool.
// Only compound routing keys (e.g. "claude:7") are pinned: bare channel keys
// (OpenAI-compat pools like "openai-compatible-foo") are left unpinned, so
// after a compat-pool failure the pick may legitimately cross into other
// providers serving the same model — the aggressive-failover intent. Note a
// bare-key test cannot be a naive colon check, because compat provider names
// may themselves contain colons; the manager's compound-key detection is the
// authoritative discriminator. The pin is never cleared once set: a request
// that already rotated through one pool stays on it until the pool is
// exhausted or the request ends.
func pinPoolFromFailedAuth(auth *Auth, pinnedPool *string) {
	if pinnedPool == nil || *pinnedPool != "" {
		return
	}
	if poolStrategyFromAuth(auth) == "" {
		return
	}
	key := routingKeyFromAuth(auth)
	if key == "" {
		return
	}
	// pinPoolFromFailedAuth is a free function without manager access, so it
	// mirrors the compound-key shape directly: a compound key carries a
	// channel prefix and a positive row id after the last colon
	// ("<channel>:<rowID>", "<channel>:<rowID>:key-<entryID>"). Bare keys never
	// end in a positive numeric row id, so this check keeps compat names that
	// embed colons (e.g. "openai-compatible-foo:bar") unpinned.
	if !isCompoundPoolRoutingKey(key) {
		return
	}
	*pinnedPool = key
}

// isCompoundPoolRoutingKey reports whether key is a per-row compound routing
// key of the form "<channel>:<rowID>" (or its per-entry extension), using the
// same shape the manager's isCompoundRoutingKey detects. It exists as a
// package-level helper because pinPoolFromFailedAuth has no manager receiver
// to call the method form. Bare channel keys — including colon-bearing compat
// names — report false.
func isCompoundPoolRoutingKey(key string) bool {
	idx := strings.LastIndex(key, ":")
	if idx <= 0 || idx == len(key)-1 {
		return false
	}
	suffix := key[idx+1:]
	if suffix == "" {
		return false
	}
	for i := 0; i < len(suffix); i++ {
		if suffix[i] < '0' || suffix[i] > '9' {
			return false
		}
	}
	// A row id of 0 is never a valid upstream_providers id; treat it as bare.
	if len(suffix) > 1 && suffix[0] == '0' {
		return false
	}
	return true
}

// poolPickProviders narrows the candidate provider list to the pinned pool
// key once a pool auth has failed and its pool opted into aggressive
// failover. With no pin, the original list is returned unchanged.
func poolPickProviders(providers []string, pinnedPool string) []string {
	if pinnedPool == "" {
		return providers
	}
	return []string{pinnedPool}
}

func publishSelectedAuthMetadata(meta map[string]any, auth *Auth) {
	if len(meta) == 0 || auth == nil {
		return
	}
	if authID := strings.TrimSpace(auth.ID); authID != "" {
		meta[cliproxyexecutor.SelectedAuthMetadataKey] = authID
		if callback, ok := meta[cliproxyexecutor.SelectedAuthCallbackMetadataKey].(func(string)); ok && callback != nil {
			callback(authID)
		}
	}
	if authIndex := strings.TrimSpace(auth.EnsureIndex()); authIndex != "" {
		meta[cliproxyexecutor.SelectedAuthIndexMetadataKey] = authIndex
		if callback, ok := meta[cliproxyexecutor.SelectedAuthIndexCallbackMetadataKey].(func(string)); ok && callback != nil {
			callback(authIndex)
		}
	}
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

func executorKeyFromAuth(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		providerKey := strings.TrimSpace(auth.Attributes["provider_key"])
		compatName := strings.TrimSpace(auth.Attributes["compat_name"])
		if compatName != "" {
			if providerKey == "" {
				providerKey = compatName
			}
			return util.OpenAICompatibleProviderKey(providerKey)
		}
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "openai-compatibility") {
		providerKey := strings.TrimSpace(auth.Label)
		if providerKey == "" {
			providerKey = "openai-compatibility"
		}
		return util.OpenAICompatibleProviderKey(providerKey)
	}
	return strings.ToLower(strings.TrimSpace(auth.Provider))
}

// routingKeyFromAuth returns the routing identifier the per-model router
// uses to pin a request to a specific upstream row. Built-in api-key
// channels (claude, gemini, codex, xai, vertex, interactions) all share a
// single executor per channel, but each upstream row gets its own routing
// key encoded in the auth's `provider_key` attribute (e.g. "claude:42") so
// operators can pin a model to one specific row instead of every row of
// that channel. When the attribute is absent (legacy YAML-only configs
// and pre-v7.2.138-0.1.2 PG rows that were never re-rendered) the routing
// key falls back to the bare executor key, preserving the prior
// "all-rows-collapsed" semantics. OAuth channels and OpenAI-compat
// already encode their identity into provider_key today; this helper
// returns it directly so the conductor's auth filters and the
// per-model routing picker stay symmetric.
func routingKeyFromAuth(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if k := strings.TrimSpace(auth.Attributes["provider_key"]); k != "" {
			return strings.ToLower(k)
		}
	}
	return executorKeyFromAuth(auth)
}

// routingKeyHasChannelPrefix reports whether the auth's routing key starts
// with "<channel>" followed by a colon (i.e. is a per-row compound key
// belonging to that channel). Auths without a per-row compound key report
// false even if their executor key equals channel, because we only want to
// treat the bare-equals-compound case as a wildcard match when the bare key
// has been opted-in by the route author (see authMatchesProvider).
func routingKeyHasChannelPrefix(auth *Auth, channel string) bool {
	if auth == nil || channel == "" {
		return false
	}
	rk := routingKeyFromAuth(auth)
	channel = strings.ToLower(strings.TrimSpace(channel))
	return channel != "" && executorKeyFromRoutingKey(rk) == channel
}

// authMatchesProvider reports whether the given auth is eligible to serve
// requests pinned to `provider` by the per-model router.
//
// Three cases match:
//
//  1. Exact: provider == routingKeyFromAuth(auth) (covers OpenAI-compat's
//     per-name keys, OAuth's per-channel keys, and per-row compound keys
//     like "claude:42").
//  2. Legacy wildcard: provider is the bare channel key and the auth has
//     no per-row provider_key attribute. Keeps v7.2.138-0.1.1-style
//     routes working for operators who never re-rendered their config.
//  3. Cross-version wildcard: provider is the bare channel key and the
//     auth is a per-row compound key for the same channel. An operator
//     who pinned to "claude" before upgrading still expects every Claude
//     API key auth (legacy AND per-row) to serve that route; the picker
//     no longer shows the bare key, but a saved route may still carry it.
//
// authMatchesProvider never returns true when provider names a different
// channel than the auth's executor key.
func authMatchesProvider(auth *Auth, provider string) bool {
	if auth == nil {
		return false
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return false
	}
	rk := routingKeyFromAuth(auth)
	if rk == provider {
		return true
	}
	// Entry-level match for OpenAI-compat entries. Built-in channels never set
	// this attribute, so this branch cannot accidentally broaden a built-in
	// channel match to a different provider.
	if auth.Attributes != nil {
		if entryKey := strings.ToLower(strings.TrimSpace(auth.Attributes[AttributeEntryProviderKey])); entryKey != "" && entryKey == provider {
			return true
		}
	}
	exec := executorKeyFromAuth(auth)
	if provider != exec {
		return false
	}
	// provider matches the auth's executor key. The auth is eligible under
	// either of two wildcard cases:
	//   (a) the auth has no per-row provider_key attribute (legacy),
	//   (b) the auth is a canonical per-row compound key for this channel.
	return !strings.Contains(rk, ":") || executorKeyFromRoutingKey(rk) == provider
}

// authMatchesAnyProvider reports whether the auth is eligible to serve
// requests pinned to any provider in the set. Equivalent to looping
// authMatchesProvider over the set.
func authMatchesAnyProvider(auth *Auth, providerSet map[string]struct{}) bool {
	if auth == nil || len(providerSet) == 0 {
		return false
	}
	for p := range providerSet {
		if authMatchesProvider(auth, p) {
			return true
		}
	}
	return false
}

// executorKeyFromRoutingKey returns the executor identifier for a routing
// key. Only built-in API-key channel prefixes use the compound
// "<channel>:<rowID>" form; provider names for OpenAI-compat and plugins may
// legitimately contain a colon and must remain intact. Malformed input (a
// leading colon, or just ":") yields empty so callers use their normal
// "executor_not_found" path.
func executorKeyFromRoutingKey(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return ""
	}
	i := strings.IndexByte(provider, ':')
	if i <= 0 {
		if i == 0 {
			return ""
		}
		return provider
	}
	channel := provider[:i]
	suffix := provider[i+1:]
	switch channel {
	case "claude", "codex", "gemini", "gemini-interactions", "vertex", "xai", "meta", "opencode-go":
		if isPositiveRowIDSuffix(suffix) {
			return channel
		}
	}
	return provider
}

func isPositiveRowIDSuffix(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

// logEntryWithRequestID returns a logrus entry with request_id field if available in context.
func logEntryWithRequestID(ctx context.Context) *log.Entry {
	if ctx == nil {
		return log.NewEntry(log.StandardLogger())
	}
	if reqID := logging.GetRequestID(ctx); reqID != "" {
		return log.WithField("request_id", reqID)
	}
	return log.NewEntry(log.StandardLogger())
}

func debugLogAuthSelection(entry *log.Entry, auth *Auth, provider string, model string) {
	if !log.IsLevelEnabled(log.DebugLevel) {
		return
	}
	if entry == nil || auth == nil {
		return
	}
	accountType, accountInfo := auth.AccountInfo()
	proxyInfo := auth.ProxyInfo()
	suffix := ""
	if proxyInfo != "" {
		suffix = " " + proxyInfo
	}
	switch accountType {
	case "api_key":
		entry.Debugf("Use API key %s for model %s%s", util.HideAPIKey(accountInfo), model, suffix)
	case "oauth":
		ident := formatOauthIdentity(auth, provider, accountInfo)
		entry.Debugf("Use OAuth %s for model %s%s", ident, model, suffix)
	}
}

func formatOauthIdentity(auth *Auth, provider string, accountInfo string) string {
	if auth == nil {
		return ""
	}
	// Prefer the auth's provider when available.
	providerName := strings.TrimSpace(auth.Provider)
	if providerName == "" {
		providerName = strings.TrimSpace(provider)
	}
	// Only log the basename to avoid leaking host paths.
	// FileName may be unset for some auth backends; fall back to ID.
	authFile := strings.TrimSpace(auth.FileName)
	if authFile == "" {
		authFile = strings.TrimSpace(auth.ID)
	}
	if authFile != "" {
		authFile = filepath.Base(authFile)
	}
	parts := make([]string, 0, 3)
	if providerName != "" {
		parts = append(parts, "provider="+providerName)
	}
	if authFile != "" {
		parts = append(parts, "auth_file="+authFile)
	}
	if len(parts) == 0 {
		return accountInfo
	}
	return strings.Join(parts, " ")
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
		exec = m.executors[executorKeyFromAuth(a)]
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

// PrepareHttpRequest injects provider credentials into the supplied HTTP request.
func (m *Manager) PrepareHttpRequest(ctx context.Context, auth *Auth, req *http.Request) error {
	if m == nil {
		return &Error{Code: "provider_not_found", Message: "manager is nil"}
	}
	if auth == nil {
		return &Error{Code: "auth_not_found", Message: "auth is nil"}
	}
	if req == nil {
		return &Error{Code: "invalid_request", Message: "http request is nil"}
	}
	if ctx != nil {
		*req = *req.WithContext(ctx)
	}
	providerKey := executorKeyFromAuth(auth)
	if providerKey == "" {
		return &Error{Code: "provider_not_found", Message: "auth provider is empty"}
	}
	exec := m.executorFor(providerKey)
	if exec == nil {
		return &Error{Code: "provider_not_found", Message: "executor not registered for provider: " + providerKey}
	}
	preparer, ok := exec.(RequestPreparer)
	if !ok || preparer == nil {
		return &Error{Code: "not_supported", Message: "executor does not support http request preparation"}
	}
	return preparer.PrepareRequest(req, auth)
}

// NewHttpRequest constructs a new HTTP request and injects provider credentials into it.
func (m *Manager) NewHttpRequest(ctx context.Context, auth *Auth, method, targetURL string, body []byte, headers http.Header) (*http.Request, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	method = strings.TrimSpace(method)
	if method == "" {
		method = http.MethodGet
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, targetURL, reader)
	if err != nil {
		return nil, err
	}
	if headers != nil {
		httpReq.Header = headers.Clone()
	}
	if errPrepare := m.PrepareHttpRequest(ctx, auth, httpReq); errPrepare != nil {
		return nil, errPrepare
	}
	return httpReq, nil
}

// HttpRequest injects provider credentials into the supplied HTTP request and executes it.
func (m *Manager) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	if m == nil {
		return nil, &Error{Code: "provider_not_found", Message: "manager is nil"}
	}
	if auth == nil {
		return nil, &Error{Code: "auth_not_found", Message: "auth is nil"}
	}
	if req == nil {
		return nil, &Error{Code: "invalid_request", Message: "http request is nil"}
	}
	providerKey := executorKeyFromAuth(auth)
	if providerKey == "" {
		return nil, &Error{Code: "provider_not_found", Message: "auth provider is empty"}
	}
	exec := m.executorFor(providerKey)
	if exec == nil {
		return nil, &Error{Code: "provider_not_found", Message: "executor not registered for provider: " + providerKey}
	}
	return exec.HttpRequest(ctx, auth, req)
}
