package auth

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// This file adds the one-shot execution primitives. They exist because the two
// execution entry points the manager already offers sit on opposite sides of a
// gap that a non-idempotent (paid) upstream create cannot straddle:
//
//   - Manager.Execute owns the full lifecycle (selection, refresh, request
//     preparation, interceptors, result marking) but replays: an outer attempt
//     loop, an inner credential-hop loop, a post-401 re-execute and a model pool
//     loop can each invoke a provider executor more than once per call.
//   - Manager.HttpRequest never replays, but performs no refresh, no request
//     preparation, no result marking and reports no attempt facts.
//
// ExecuteWithAuthOnce and DoHTTPOnce keep the lifecycle and drop every replay
// vector the manager owns, and both report typed attempt facts. How much those
// facts can claim differs by entry point: only DoHTTPOnce owns the http.Client,
// so only there can RequestWritten be false and mean "definitely not sent". On
// ExecuteWithAuthOnce a provider executor owns the client, so the facts describe
// what was observed and RequestWritten is never false.
//
// The two differ in how far the guarantee reaches, and the difference is a
// boundary rather than a gap in effort. DoHTTPOnce performs the request itself,
// so it can police redirects and cap the attempt outright. ExecuteWithAuthOnce
// hands the request to a provider executor that constructs and owns its own
// http.Client, which the manager cannot reach: the shared executor helper
// consults the auth proxy and the runtime config proxy before it looks at the
// manager transport, and no executor sets a CheckRedirect. So there the manager
// guarantees one executor invocation and reports what it observed, and
// HTTPAttemptFacts.RequestCount - not the absence of an error - is what a caller
// of a non-idempotent create reconciles against.

// maxSameOriginSafeRedirectHops bounds HTTPRedirectSameOriginSafe redirect following.
const maxSameOriginSafeRedirectHops = 3

// HTTPRedirectPolicy names the redirect contract applied to a one-shot request.
// It is only offered by DoHTTPOnce, the path where the manager owns the client
// and can therefore enforce it; see the file comment for why.
type HTTPRedirectPolicy string

const (
	// HTTPRedirectDeny never follows a redirect and surfaces the 3xx response to
	// the caller instead. This is the required policy for a non-idempotent create.
	HTTPRedirectDeny HTTPRedirectPolicy = "deny"
	// HTTPRedirectSameOriginSafe follows at most maxSameOriginSafeRedirectHops
	// redirects, and only for a bodyless GET or HEAD request whose target keeps
	// the same HTTPS origin. Credential headers are stripped before following, so
	// every hop after the first is unauthenticated even though the origin is
	// unchanged. That makes this policy suitable for following a signed URL or an
	// asset redirect, and unsuitable for polling an endpoint that requires the
	// credential on the followed hop - there the redirect target returns 401/403
	// rather than the resource.
	HTTPRedirectSameOriginSafe HTTPRedirectPolicy = "same_origin_safe"
)

// normalizedRedirectPolicy trims and lower-cases a caller supplied policy value.
func normalizedRedirectPolicy(policy HTTPRedirectPolicy) HTTPRedirectPolicy {
	return HTTPRedirectPolicy(strings.ToLower(strings.TrimSpace(string(policy))))
}

// validateRedirectPolicy reports whether the supplied policy is understood.
// An empty policy is accepted and means HTTPRedirectDeny: redirect policy is only
// offered where the manager owns the http.Client, which is DoHTTPOnce alone.
func validateRedirectPolicy(policy HTTPRedirectPolicy) error {
	switch normalizedRedirectPolicy(policy) {
	case "", HTTPRedirectDeny, HTTPRedirectSameOriginSafe:
		return nil
	default:
		return &Error{Code: "invalid_redirect_policy", Message: "redirect policy is invalid", HTTPStatus: http.StatusBadRequest}
	}
}

// HTTPAttemptFacts reports what is known about the upstream attempt a one-shot
// call made. It exists so a caller of a paid create can distinguish "definitely
// not sent" from "may have been sent" without parsing an error string.
//
// The facts are derived from the transport the manager installed and from
// net/http/httptrace, and both sources are optional. RequestWritten is therefore
// the pessimistic claim - it is true whenever bytes may have reached the wire -
// while RequestWrittenObserved is the positive one, true only when a write was
// actually observed. A false RequestWritten is the only statement of "definitely
// not sent", and it is never made on a path where a provider executor owned the
// http.Client, because an executor can issue requests the manager cannot see.
type HTTPAttemptFacts struct {
	// RequestCount counts upstream requests observed on this attempt, including
	// redirect hops and transport-internal resends, taking the larger of what the
	// manager transport and httptrace saw. A 0 means "nothing observed", which is
	// "unknown" rather than "not sent" whenever the client was not manager owned.
	RequestCount uint32
	// RequestWritten reports whether any request byte may have reached the wire.
	RequestWritten bool
	// RequestWrittenObserved reports that a write was positively observed. It is
	// the proof-carrying counterpart of RequestWritten.
	RequestWrittenObserved bool
	// ResponseStarted reports whether upstream response headers were received.
	ResponseStarted bool
	// StatusCode is the observed upstream status, or the status carried by the
	// executor error when the transport was not manager visible. It is 0 when
	// no status is known.
	StatusCode int
}

// ExecuteWithAuthOnceRequest describes a single pinned, non-replayed execution.
type ExecuteWithAuthOnceRequest struct {
	// AuthID identifies a manager-persisted credential. Selection never runs.
	AuthID string
	// Provider optionally overrides the executor key derived from the auth.
	// Leave it empty to use the same resolution the retrying path uses.
	Provider string
	// Model optionally overrides the routing model. Empty falls back to the
	// auth-selection model metadata and then to Request.Model.
	Model string
	// Request is the canonical executor request.
	Request cliproxyexecutor.Request
	// Options are the canonical executor options.
	Options cliproxyexecutor.Options
}

// HTTPOnceRequest describes a single pinned, non-replayed raw HTTP call.
type HTTPOnceRequest struct {
	// AuthID identifies a manager-persisted credential.
	AuthID string
	// Model names the model this call is attributed to. Credential availability
	// in this manager is model scoped, so a call that names no model is recorded
	// for observability only and never rewrites credential health - a raw poll
	// must not resurrect a rate-limited credential nor suspend a working one.
	Model string
	// Method is the HTTP method. Empty defaults to GET.
	Method string
	// URL is the absolute upstream URL.
	URL string
	// Header carries request headers applied before credential injection.
	Header http.Header
	// Body is the request payload. A body-bearing request is never replayed.
	Body []byte
	// RedirectPolicy constrains redirect handling. Empty means HTTPRedirectDeny.
	RedirectPolicy HTTPRedirectPolicy
}

// ExecuteWithAuthOnce executes a request against exactly one persisted credential
// and invokes its registered provider executor exactly once.
//
// It reuses the manager's existing lifecycle - proactive refresh under the
// per-auth refresh lock, request preparation under the per-auth prepare lock,
// alias and force-mapping resolution, the RequestAfterAuthInterceptor seam and
// MarkResult - while entering none of the retry machinery: no attempt loop, no
// credential hop, no post-401 re-execute, and a model pool collapsed to its
// first entry exactly as the Home execution path does.
//
// SCOPE OF THE ONCE GUARANTEE. Once-ness here is per executor invocation, not per
// upstream HTTP request. A provider executor constructs and owns its own
// http.Client, so the manager cannot install a CheckRedirect on it and cannot cap
// the requests it makes: an executor may follow redirects (net/http replays the
// body on 307 and 308 whenever GetBody is set, which it always is for a bytes
// body), may fall back across base URLs, and may run its own attempt loop. The
// manager does not offer a redirect policy on this path, because any policy it
// could advertise would be silently unenforced whenever a proxy is configured -
// the shared executor helper consults the auth proxy and the runtime config proxy
// before it ever looks at the manager transport. HTTPAttemptFacts.RequestCount is
// therefore the only authority on how many upstream requests were made, and a
// caller performing a non-idempotent create must treat RequestCount > 1 as a
// possible double spend and RequestCount == 0 as unknown rather than "not sent".
// DoHTTPOnce owns its client and is the path with an enforceable guarantee.
//
// MarkResult is called at most once. It is not called at all when the failure
// happened before the credential was exercised: unknown or non-durable auth,
// unregistered executor, refresh failure, or an interceptor termination.
//
// It never returns an *Auth, headers or any other credential material.
func (m *Manager) ExecuteWithAuthOnce(ctx context.Context, in ExecuteWithAuthOnceRequest) (cliproxyexecutor.Response, HTTPAttemptFacts, error) {
	var facts HTTPAttemptFacts
	if m == nil {
		return cliproxyexecutor.Response{}, facts, &Error{Code: "provider_not_found", Message: "manager is nil"}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	auth, executor, providerKey, errResolve := m.resolveDurableAuthForOnce(in.AuthID, in.Provider)
	if errResolve != nil {
		return cliproxyexecutor.Response{}, facts, errResolve
	}

	refreshed, errRefresh := m.refreshAuthForOnce(ctx, auth)
	if errRefresh != nil {
		// Fail closed: a credential that could not be refreshed must not be spent
		// on a paid attempt. refreshAuthForRequest already recorded the credential
		// state, so this path deliberately does not mark a second result.
		return cliproxyexecutor.Response{}, facts, errRefresh
	}
	auth = refreshed

	routeModel := strings.TrimSpace(in.Model)
	if routeModel == "" {
		routeModel = authSelectionModelFromOptions(in.Options, in.Request.Model)
	}
	executionModel, restoreExecutionModel := executionModelForAuthSelection(in.Options, in.Request.Model)
	opts := ensureRequestedModelMetadata(in.Options, routeModel)
	publishSelectedAuthMetadata(opts.Metadata, auth)

	// The executor owns its http.Client, so the recorder must never claim that a
	// request was definitely not written on this path.
	recorder := newOnceAttemptRecorder(false)
	execCtx := m.onceExecutionContext(ctx, auth, opts, routeModel, recorder)

	models, pooled, aliasResult, routing := m.preparedExecutionModelsWithAlias(auth, routeModel)
	if len(models) == 0 {
		// The pinned credential is cooling for this model. The retrying path would
		// hop to another credential here; a pinned one-shot fails closed instead.
		return cliproxyexecutor.Response{}, facts, &Error{Code: "auth_unavailable", Message: "auth is unavailable for model: " + routeModel, HTTPStatus: http.StatusServiceUnavailable}
	}
	if len(models) > 1 {
		// A model pool is a replay vector: the retrying path invokes the executor
		// once per pooled model. Collapse it exactly as the Home path does.
		models = models[:1]
		pooled = false
	}
	upstreamModel := models[0]
	resultModel := m.stateModelForExecution(auth, routeModel, upstreamModel, pooled)

	marker := &onceResultMarker{}

	var errPrepare error
	auth, errPrepare = m.prepareRequestAuth(execCtx, executor, auth)
	if errPrepare != nil {
		if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errPrepare); errCancel != nil {
			return cliproxyexecutor.Response{}, facts, errCancel
		}
		marker.mark(execCtx, m, Result{AuthID: auth.ID, Provider: providerKey, Model: routeModel, Success: false, Error: resultErrorFromError(errPrepare)})
		return cliproxyexecutor.Response{}, facts, errPrepare
	}

	execReq := in.Request
	execReq.Model = upstreamModel
	if restoreExecutionModel {
		execReq.Model = executionModel
	}
	execOpts := opts
	var errIntercept error
	execReq, execOpts, errIntercept = applyRequestAfterAuthInterceptor(execCtx, executor, providerKey, execReq, execOpts, requestedModelAliasFromOptions(execOpts, routeModel))
	if errIntercept != nil {
		// The interceptor terminated before the executor ran; nothing was spent.
		return cliproxyexecutor.Response{}, facts, errIntercept
	}
	if !restoreExecutionModel {
		execReq = attachResolvedAPIKeyModelInfo(routing, execReq, auth, routeModel, upstreamModel)
	}

	recorder.markDispatched()
	resp, errExec := executor.Execute(execCtx, auth, execReq, execOpts)
	facts = recorder.facts()
	if facts.StatusCode == 0 {
		facts.StatusCode = statusCodeFromError(errExec)
	}

	if errCancel := claudeOAuthRequestCancellation(execCtx, auth, errExec); errCancel != nil {
		return cliproxyexecutor.Response{}, facts, errCancel
	}

	result := Result{AuthID: auth.ID, Provider: providerKey, Model: resultModel, Success: errExec == nil}
	if errExec != nil {
		result.Error = resultErrorFromError(errExec)
		if retryAfter := retryAfterFromError(errExec); retryAfter != nil {
			result.RetryAfter = retryAfter
		}
	}
	marker.mark(execCtx, m, result)
	if errExec != nil {
		return cliproxyexecutor.Response{}, facts, errExec
	}

	rewriteForceMappedResponse(&resp, resolveAttemptAliasResult(routing, auth, routeModel, upstreamModel, aliasResult))
	return resp, facts, nil
}

// DoHTTPOnce resolves a persisted credential by ID, refreshes and prepares it,
// and then performs exactly one policed HTTP attempt against the supplied target.
//
// It is the raw-HTTP twin of ExecuteWithAuthOnce and shares its lifecycle: the ID
// must name a durable auth (Home runtime/session auths are rejected with
// auth_not_durable before any network I/O), the credential is refreshed under the
// existing per-auth refresh lock, and request preparation runs under the existing
// per-auth prepare lock. Selection never runs and nothing is ever replayed.
//
// Credential injection reuses the registered executor's RequestPreparer through
// PrepareHttpRequest, and the proxy chain is resolved with the same priority the
// shared executor helper uses (auth proxy, then runtime config proxy, then the
// context or per-auth RoundTripper, then the default transport). The client
// itself is manager owned because redirect policy cannot be expressed any other
// way: provider executors construct their own http.Client and none of them sets
// CheckRedirect, so the default client would follow up to ten hops, replay a
// body on 307/308 whenever GetBody is set, and carry executor-injected api-key
// style headers cross-host - net/http only strips Authorization, Cookie and
// WWW-Authenticate. Both of those are disqualifying for a paid create.
//
// The response body is not read or closed; the caller owns it. A redirect the
// policy refuses is returned as the unfollowed 3xx response together with a
// redirect_denied error, so a caller that branches on the error alone cannot read
// a refused 3xx as a success - with one degenerate exception: a 3xx carrying no
// Location header is not a followable redirect at all and is returned as a plain
// (3xx, nil error) result, like any other non-2xx status. A caller that must not
// treat any non-2xx as success should branch on the status code, not on the error.
// An empty RedirectPolicy means HTTPRedirectDeny.
//
// Exactly one execution result is recorded, and the recording is classified
// because a raw HTTP call addresses arbitrary endpoints rather than a model
// serving route. Only statuses that describe the credential - 401, 402, 403, 408,
// 429, 5xx - and transport failures reach MarkResult and may cool the credential;
// success is HTTP 2xx alone; every other outcome, including a 3xx and a
// request-shaped 4xx such as 404, is recorded through the availability-neutral
// path the count_tokens 404 case already uses, so polling a job URL cannot
// suspend a healthy credential. A call that names no Model is always neutral.
//
// It never returns an *Auth, request headers or any other credential material.
func (m *Manager) DoHTTPOnce(ctx context.Context, in HTTPOnceRequest) (*http.Response, HTTPAttemptFacts, error) {
	var facts HTTPAttemptFacts
	if m == nil {
		return nil, facts, &Error{Code: "provider_not_found", Message: "manager is nil"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errPolicy := validateRedirectPolicy(in.RedirectPolicy); errPolicy != nil {
		return nil, facts, errPolicy
	}
	if strings.TrimSpace(in.URL) == "" {
		return nil, facts, &Error{Code: "invalid_request", Message: "http request url is empty", HTTPStatus: http.StatusBadRequest}
	}

	auth, executor, _, errResolve := m.resolveDurableAuthForOnce(in.AuthID, "")
	if errResolve != nil {
		return nil, facts, errResolve
	}

	refreshed, errRefresh := m.refreshAuthForOnce(ctx, auth)
	if errRefresh != nil {
		return nil, facts, errRefresh
	}
	auth = refreshed

	prepared, errPrepare := m.prepareRequestAuth(ctx, executor, auth)
	if errPrepare != nil {
		return nil, facts, errPrepare
	}
	auth = prepared

	method := strings.TrimSpace(in.Method)
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if in.Body != nil {
		body = bytes.NewReader(in.Body)
	}
	req, errRequest := http.NewRequestWithContext(ctx, method, in.URL, body)
	if errRequest != nil {
		return nil, facts, errRequest
	}
	if in.Header != nil {
		req.Header = in.Header.Clone()
	}
	return m.httpOnce(ctx, auth, req, in.Model, in.RedirectPolicy)
}

// httpOnce injects credentials, performs one policed attempt and marks once.
func (m *Manager) httpOnce(ctx context.Context, auth *Auth, req *http.Request, model string, policy HTTPRedirectPolicy) (*http.Response, HTTPAttemptFacts, error) {
	var facts HTTPAttemptFacts
	if m == nil {
		return nil, facts, &Error{Code: "provider_not_found", Message: "manager is nil"}
	}
	if auth == nil {
		return nil, facts, &Error{Code: "auth_not_found", Message: "auth is nil"}
	}
	if req == nil {
		return nil, facts, &Error{Code: "invalid_request", Message: "http request is nil"}
	}
	if errPolicy := validateRedirectPolicy(policy); errPolicy != nil {
		return nil, facts, errPolicy
	}
	if ctx == nil {
		ctx = context.Background()
	}
	normalized := normalizedRedirectPolicy(policy)
	if normalized == "" {
		normalized = HTTPRedirectDeny
	}

	// The manager owns this client end to end, so the recorder may report a
	// negative: an attempt that never wrote is provably not sent.
	recorder := newOnceAttemptRecorder(true)
	tracedCtx := httptrace.WithClientTrace(ctx, recorder.clientTrace())
	httpReq := req.WithContext(tracedCtx)
	if httpReq.Body != nil || httpReq.ContentLength != 0 {
		// Belt and braces: with GetBody unset net/http refuses to replay a body on
		// 307/308 and hands the 3xx back instead of resending the payload.
		httpReq.GetBody = nil
	}

	if errPrepare := m.PrepareHttpRequest(tracedCtx, auth, httpReq); errPrepare != nil {
		return nil, facts, errPrepare
	}

	guard := &onceRedirectGuard{policy: normalized, origin: newRedirectOrigin(httpReq)}
	client := &http.Client{
		Transport:     m.clientTransportForOnce(tracedCtx, auth, recorder),
		CheckRedirect: guard.checkRedirect,
	}

	recorder.markDispatched()
	resp, errDo := client.Do(httpReq)
	facts = recorder.facts()
	if resp != nil {
		facts.StatusCode = resp.StatusCode
		facts.ResponseStarted = true
		facts.RequestWritten = true
		facts.RequestWrittenObserved = true
	} else if facts.StatusCode == 0 {
		facts.StatusCode = statusCodeFromError(errDo)
	}

	if errCancel := claudeOAuthRequestCancellation(tracedCtx, auth, errDo); errCancel != nil {
		return resp, facts, errCancel
	}

	marker := &onceResultMarker{}
	result := Result{AuthID: auth.ID, Provider: executorKeyFromAuth(auth), Model: model}
	availabilityRelevant := false
	switch {
	case resp == nil:
		result.Success = false
		result.Error = resultErrorFromError(errDo)
		if retryAfter := retryAfterFromError(errDo); retryAfter != nil {
			result.RetryAfter = retryAfter
		}
		availabilityRelevant = true
	case resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices:
		result.Success = true
		availabilityRelevant = true
	default:
		result.Success = false
		result.Error = resultErrorFromError(httpStatusError(resp.StatusCode))
		if retryAfter := retryAfterFromResponse(resp); retryAfter != nil {
			result.RetryAfter = retryAfter
		}
		availabilityRelevant = httpStatusCoolsCredential(resp.StatusCode)
	}
	if strings.TrimSpace(model) == "" {
		// Credential health is model scoped here. With no model, a success would
		// clear the whole credential's quota state and a failure would suspend the
		// credential outright, so an unattributed call is only ever observed.
		availabilityRelevant = false
	}
	marker.record(tracedCtx, m, result, availabilityRelevant)

	if errDo == nil && resp != nil {
		denied, target := guard.denied()
		if !denied && unfollowedRedirect(resp) {
			// net/http can refuse a redirect before CheckRedirect runs: with
			// GetBody cleared it hands a 307 or 308 straight back rather than
			// replaying the body. Either way a redirect was offered, not followed,
			// and the caller must not read the 3xx as a completed request.
			denied = true
			target = resp.Header.Get("Location")
		}
		if denied {
			return resp, facts, &Error{
				Code:       "redirect_denied",
				Message:    "redirect to " + target + " was not followed",
				HTTPStatus: resp.StatusCode,
			}
		}
	}
	return resp, facts, errDo
}

// unfollowedRedirect reports whether a response is a redirect the client handed
// back instead of following.
func unfollowedRedirect(resp *http.Response) bool {
	if resp == nil || strings.TrimSpace(resp.Header.Get("Location")) == "" {
		return false
	}
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

// httpStatusError converts an upstream HTTP status into the package error type
// so MarkResult keeps its status-driven cooldown classification.
func httpStatusError(status int) error {
	return &Error{Code: "upstream_http_status", Message: "upstream returned status " + strconv.Itoa(status), HTTPStatus: status}
}

// httpStatusCoolsCredential reports whether an upstream status says something
// about the credential rather than about the request that was addressed.
//
// A raw one-shot HTTP call targets arbitrary endpoints - a job status URL, an
// asset URL - so its statuses cannot be read the way a serving response is. Only
// credential-shaped statuses may change availability; everything else, including
// a policy-refused 3xx and request-shaped 4xx such as 404, stays neutral. This is
// the same distinction the count_tokens 404 case draws in conductor_execution.go.
func httpStatusCoolsCredential(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden,
		http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	return status >= http.StatusInternalServerError
}

// retryAfterFromResponse extracts a Retry-After hint from an upstream response.
func retryAfterFromResponse(resp *http.Response) *time.Duration {
	if resp == nil {
		return nil
	}
	raw := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if raw == "" {
		return nil
	}
	if seconds, errParse := strconv.Atoi(raw); errParse == nil {
		if seconds < 0 {
			return nil
		}
		value := time.Duration(seconds) * time.Second
		return &value
	}
	deadline, errParse := http.ParseTime(raw)
	if errParse != nil {
		return nil
	}
	value := time.Until(deadline)
	if value <= 0 {
		return nil
	}
	return &value
}

// resolveDurableAuthForOnce returns a durable credential and its registered
// executor without running selection. Home runtime/session auths and Home mode
// itself are rejected because their credentials are not manager persisted.
func (m *Manager) resolveDurableAuthForOnce(authID, providerOverride string) (*Auth, ProviderExecutor, string, error) {
	id := strings.TrimSpace(authID)
	if id == "" {
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "auth id is empty"}
	}
	if !m.localExecutionAllowed() {
		return nil, nil, "", &Error{Code: "auth_not_durable", Message: "pinned execution requires a locally persisted auth", HTTPStatus: http.StatusServiceUnavailable}
	}

	m.mu.RLock()
	// Clone under the read lock: MarkResult mutates the stored auth in place
	// while holding the write lock.
	cloned := m.auths[id].Clone()
	sessionScoped := false
	for _, sessionAuths := range m.homeRuntimeAuths {
		if _, ok := sessionAuths[id]; ok {
			sessionScoped = true
			break
		}
	}
	m.mu.RUnlock()

	if sessionScoped {
		return nil, nil, "", &Error{Code: "auth_not_durable", Message: "auth is session scoped", HTTPStatus: http.StatusServiceUnavailable}
	}
	if cloned == nil {
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "auth not found: " + id}
	}

	providerKey := strings.TrimSpace(providerOverride)
	if providerKey == "" {
		providerKey = executorKeyFromAuth(cloned)
	}
	if providerKey == "" {
		return nil, nil, "", &Error{Code: "provider_not_found", Message: "auth provider is empty"}
	}
	executor := m.executorFor(providerKey)
	if executor == nil {
		return nil, nil, "", &Error{Code: "executor_not_found", Message: "executor not registered for provider: " + providerKey}
	}
	return cloned, executor, providerKey, nil
}

// refreshAuthForOnce runs the manager's existing proactive refresh before a
// one-shot dispatch. It claims the refresh with markRefreshPending and performs
// it with refreshAuthForRequest, so it shares the per-auth refresh lock and the
// backoff bookkeeping with the auto-refresh loop instead of duplicating them.
//
// The refresh is skipped, without error, when the credential does not need one.
// Concurrent callers all enter refreshAuthForRequest with the access token they
// observed before claiming the refresh. Its per-auth lock makes the first caller
// the refresh owner; waiters then reload and reuse the replacement credential
// instead of dispatching with the stale clone. It is also skipped when no
// executor is registered under the auth's effective executor key. This matches
// refreshAuthForRequest, including OpenAI-compatible credentials whose raw
// Provider differs from compat_name/provider_key.
func (m *Manager) refreshAuthForOnce(ctx context.Context, auth *Auth) (*Auth, error) {
	if m == nil || auth == nil {
		return auth, nil
	}
	id := strings.TrimSpace(auth.ID)
	if id == "" {
		return auth, nil
	}
	now := time.Now()
	m.mu.RLock()
	current := m.auths[id]
	needsRefresh := current != nil && m.shouldRefresh(current, now)
	if !needsRefresh && current != nil && !current.NextRefreshAfter.IsZero() && now.Before(current.NextRefreshAfter) {
		// NextRefreshAfter also represents an in-flight refresh claim. Ignore it
		// for this due check so a concurrent paid call joins the per-auth refresh
		// lock instead of treating the stale credential as fresh. This also makes
		// a paid one-shot fail closed during refresh backoff rather than spending a
		// credential the evaluator still considers due.
		withoutBackoff := current.Clone()
		withoutBackoff.NextRefreshAfter = time.Time{}
		needsRefresh = m.shouldRefresh(withoutBackoff, now)
	}
	providerKey := ""
	if current != nil {
		providerKey = executorKeyFromAuth(current)
	}
	m.mu.RUnlock()
	if !needsRefresh {
		return auth, nil
	}
	if m.executorFor(providerKey) == nil {
		return auth, nil
	}
	observedAccessToken := authAccessToken(current)
	// markRefreshPending coordinates scheduling/backoff, but losing that claim
	// does not mean the refresh is complete. Enter the per-auth refresh lock in
	// either case and identify the credential version we observed so a waiter can
	// reuse the owner's replacement without refreshing twice.
	_ = m.markRefreshPending(id, now)
	refreshed, errRefresh := m.refreshAuthForRequest(ctx, id, observedAccessToken)
	if errRefresh != nil {
		return nil, errRefresh
	}
	if refreshed == nil {
		return auth, nil
	}
	return refreshed, nil
}

// onceResultMarker guarantees at most one result recording per one-shot request.
type onceResultMarker struct {
	once sync.Once
}

// mark records an availability-relevant execution result exactly once.
func (r *onceResultMarker) mark(ctx context.Context, m *Manager, result Result) {
	r.record(ctx, m, result, true)
}

// record records the execution result the first time it is called and never
// again. An availability-irrelevant result is reported through the neutral path
// so hooks, counters and error events still see it while credential cooldown,
// quota and suspension state stay untouched.
func (r *onceResultMarker) record(ctx context.Context, m *Manager, result Result, availabilityRelevant bool) {
	if r == nil || m == nil {
		return
	}
	r.once.Do(func() {
		if availabilityRelevant {
			m.MarkResult(ctx, result)
			return
		}
		m.recordAvailabilityNeutralResult(ctx, result)
	})
}

// onceExecutionContext builds the executor context for a one-shot execution. It
// mirrors the retrying path (per-auth transport under both round tripper keys,
// requested-model alias attribution) and adds the httptrace hooks the attempt
// facts are derived from.
func (m *Manager) onceExecutionContext(ctx context.Context, auth *Auth, opts cliproxyexecutor.Options, routeModel string, recorder *onceAttemptRecorder) context.Context {
	execCtx := contextWithRequestedModelAlias(ctx, opts, routeModel)
	transport := m.executorTransportForOnce(ctx, auth, recorder)
	execCtx = context.WithValue(execCtx, roundTripperContextKey{}, transport)
	execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", transport)
	return httptrace.WithClientTrace(execCtx, recorder.clientTrace())
}

// executorTransportForOnce wraps the transport the retrying execution path would
// hand to the executor - the context RoundTripper, then the per-auth one, then
// the default transport - so the manager can count requests and observe statuses.
//
// It deliberately does not resolve a proxy URL: the shared executor helper
// consults the auth and runtime config proxies before the context RoundTripper,
// so a proxy transport built here would never be used, and building one would
// also diverge from what the retrying path installs.
func (m *Manager) executorTransportForOnce(ctx context.Context, auth *Auth, recorder *onceAttemptRecorder) http.RoundTripper {
	return &onceRoundTripper{base: m.baseTransportForOnce(ctx, auth), recorder: recorder}
}

// clientTransportForOnce wraps the transport the manager-owned one-shot HTTP
// client rides. It applies the same priority the shared executor helper does -
// auth proxy, runtime config proxy, context or per-auth RoundTripper, default
// transport - because here the manager, not the executor, performs the request.
func (m *Manager) clientTransportForOnce(ctx context.Context, auth *Auth, recorder *onceAttemptRecorder) http.RoundTripper {
	base := m.proxyTransportForOnce(auth)
	if base == nil {
		base = m.baseTransportForOnce(ctx, auth)
	}
	return &onceRoundTripper{base: base, recorder: recorder}
}

// proxyTransportForOnce returns the transport implied by the auth proxy or the
// runtime config proxy, or nil when neither is configured or usable.
func (m *Manager) proxyTransportForOnce(auth *Auth) http.RoundTripper {
	proxyURL := ""
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" {
		if cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config); cfg != nil {
			proxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	if proxyURL == "" {
		return nil
	}
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil || transport == nil {
		log.Debugf("one-shot request: proxy transport unavailable for %s: %v", proxyutil.Redact(proxyURL), errBuild)
		return nil
	}
	return transport
}

// baseTransportForOnce resolves the non-proxy transport for a one-shot attempt.
func (m *Manager) baseTransportForOnce(ctx context.Context, auth *Auth) http.RoundTripper {
	if ctx != nil {
		if rt, ok := ctx.Value(roundTripperContextKey{}).(http.RoundTripper); ok && rt != nil {
			return rt
		}
		if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
			return rt
		}
	}
	if rt := m.roundTripperFor(auth); rt != nil {
		return rt
	}
	return http.DefaultTransport
}

// onceRoundTripper observes a one-shot attempt and never alters it.
//
// It counts requests and records statuses so HTTPAttemptFacts can be derived even
// when httptrace is ignored. It deliberately performs no redirect enforcement: a
// transport sits below http.Client's redirect handling and cannot tell a followed
// redirect from an executor's own second request, so any enforcement attempted
// here would either corrupt an upstream response or strip credentials from a
// legitimate request. Redirect policy lives where the manager owns the client,
// which is DoHTTPOnce.
type onceRoundTripper struct {
	base     http.RoundTripper
	recorder *onceAttemptRecorder
}

// RoundTrip implements http.RoundTripper.
func (rt *onceRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.recorder.observeRequest()
	resp, err := rt.base.RoundTrip(req)
	if resp != nil {
		rt.recorder.observeResponse(resp.StatusCode)
	}
	return resp, err
}

// redirectOrigin snapshots the properties of the original request that decide
// whether a redirect may be followed. It is captured before dispatch because the
// request body is consumed by then and cannot be inspected afterwards.
type redirectOrigin struct {
	method  string
	url     *url.URL
	hasBody bool
}

// newRedirectOrigin snapshots a request for redirect decisions.
func newRedirectOrigin(req *http.Request) *redirectOrigin {
	if req == nil {
		return nil
	}
	return &redirectOrigin{
		method:  strings.ToUpper(strings.TrimSpace(req.Method)),
		url:     req.URL,
		hasBody: (req.Body != nil && req.Body != http.NoBody) || req.ContentLength > 0,
	}
}

// onceRedirectGuard is the CheckRedirect implementation for a one-shot HTTP
// attempt. It records the refusal so the caller receives an explicit error
// alongside the unfollowed 3xx response instead of a silent success.
type onceRedirectGuard struct {
	policy HTTPRedirectPolicy
	origin *redirectOrigin

	mu           sync.Mutex
	refused      bool
	refusedAfter string
}

// checkRedirect implements http.Client.CheckRedirect.
func (g *onceRedirectGuard) checkRedirect(req *http.Request, via []*http.Request) error {
	target := ""
	if req != nil && req.URL != nil {
		target = req.URL.String()
	}
	if !sameOriginSafeRedirectAllowed(g.policy, g.origin, target, len(via)) {
		g.mu.Lock()
		if !g.refused {
			g.refused = true
			g.refusedAfter = target
		}
		g.mu.Unlock()
		return http.ErrUseLastResponse
	}
	if req != nil {
		stripSensitiveRedirectHeaders(req.Header)
	}
	return nil
}

// denied reports whether a redirect was refused and where it pointed.
func (g *onceRedirectGuard) denied() (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.refused, g.refusedAfter
}

// sameOriginSafeRedirectAllowed reports whether a redirect may be followed.
//
// HTTPRedirectDeny never allows one. HTTPRedirectSameOriginSafe allows at most
// maxSameOriginSafeRedirectHops hops, and only when the original request is a
// bodyless GET or HEAD over HTTPS and the target keeps the same normalized
// origin. hop is the number of requests already made, so the first redirect
// decision sees hop == 1.
func sameOriginSafeRedirectAllowed(policy HTTPRedirectPolicy, origin *redirectOrigin, target string, hop int) bool {
	if normalizedRedirectPolicy(policy) != HTTPRedirectSameOriginSafe {
		return false
	}
	if origin == nil || origin.url == nil {
		return false
	}
	if hop < 1 || hop > maxSameOriginSafeRedirectHops {
		return false
	}
	switch origin.method {
	case http.MethodGet, http.MethodHead:
	default:
		return false
	}
	if origin.hasBody {
		return false
	}
	if !strings.EqualFold(origin.url.Scheme, "https") {
		return false
	}
	if strings.TrimSpace(target) == "" {
		return false
	}
	parsed, errParse := url.Parse(target)
	if errParse != nil {
		return false
	}
	if parsed.Host == "" {
		// A relative Location stays on the current origin.
		return true
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	return strings.EqualFold(normalizedOriginHost(parsed), normalizedOriginHost(origin.url))
}

// normalizedOriginHost returns the comparable host[:port] of a URL.
func normalizedOriginHost(target *url.URL) string {
	if target == nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(target.Host))
	return strings.TrimSuffix(host, ":443")
}

// redirectSensitiveHeaders lists credential-bearing headers that must never
// travel to a redirect target. net/http only strips the first three of these on
// its own, and only cross-host, which leaves executor-injected api-key style
// headers in place.
var redirectSensitiveHeaders = []string{
	"Authorization",
	"Cookie",
	"Cookie2",
	"Proxy-Authorization",
	"Www-Authenticate",
}

// stripSensitiveRedirectHeaders removes credential-bearing headers in place. It
// reuses the manager's own sensitivity vocabulary so provider-specific key
// headers (x-api-key, api-key, x-goog-api-key, ...) are covered too.
func stripSensitiveRedirectHeaders(header http.Header) {
	if len(header) == 0 {
		return
	}
	for _, name := range redirectSensitiveHeaders {
		header.Del(name)
	}
	for name := range header {
		if schedulerAttributeSensitive(name) {
			header.Del(name)
		}
	}
}

// onceAttemptRecorder collects attempt facts from the manager transport and from
// net/http/httptrace. Both sources are optional: a host supplied RoundTripper may
// bypass the manager transport, and a non stdlib transport may ignore httptrace.
//
// clientOwned records whether the manager owned the http.Client for the whole
// attempt. Only then may the recorder state that a request was not written: when
// a provider executor owns the client it can issue requests on a detached context
// and an unwrapped transport, so silence proves nothing.
type onceAttemptRecorder struct {
	mu                sync.Mutex
	clientOwned       bool
	dispatched        bool
	traceObserved     bool
	transportRequests uint32
	traceRequests     uint32
	wroteHeaders      bool
	wroteRequest      bool
	responseStarted   bool
	statusCode        int
}

// newOnceAttemptRecorder returns a recorder ready for a single attempt.
func newOnceAttemptRecorder(clientOwned bool) *onceAttemptRecorder {
	return &onceAttemptRecorder{clientOwned: clientOwned}
}

// markDispatched records that the attempt was handed to the executor or client.
func (r *onceAttemptRecorder) markDispatched() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.dispatched = true
	r.mu.Unlock()
}

// observeRequest records one request reaching the manager transport and returns
// the number of requests observed so far.
func (r *onceAttemptRecorder) observeRequest() uint32 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transportRequests++
	return r.transportRequests
}

// observeResponse records the most recent status seen by the manager transport.
func (r *onceAttemptRecorder) observeResponse(status int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.responseStarted = true
	r.statusCode = status
	r.mu.Unlock()
}

// clientTrace returns the httptrace hooks used to derive attempt facts.
func (r *onceAttemptRecorder) clientTrace() *httptrace.ClientTrace {
	if r == nil {
		return nil
	}
	return &httptrace.ClientTrace{
		GetConn: func(string) {
			r.mu.Lock()
			r.traceObserved = true
			r.mu.Unlock()
		},
		ConnectStart: func(string, string) {
			r.mu.Lock()
			r.traceObserved = true
			r.mu.Unlock()
		},
		WroteHeaders: func() {
			r.mu.Lock()
			r.traceObserved = true
			r.wroteHeaders = true
			r.traceRequests++
			r.mu.Unlock()
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			r.mu.Lock()
			r.traceObserved = true
			r.wroteRequest = true
			r.mu.Unlock()
		},
		GotFirstResponseByte: func() {
			r.mu.Lock()
			r.traceObserved = true
			r.responseStarted = true
			r.mu.Unlock()
		},
	}
}

// facts collapses the observations into the caller-visible attempt facts.
//
// RequestCount takes the larger of the two counts rather than preferring one:
// http.Transport and the bundled HTTP/2 transport resend a request internally,
// below any wrapping RoundTripper, and httptrace's WroteHeaders fires once per
// wire attempt inside them. Under-reporting the number of upstream sends is the
// dangerous direction for a caller reconciling a non-idempotent create.
//
// RequestWritten is deliberately biased: an attempt that dispatched but produced
// no conclusive observation is reported as written, because a false "not sent"
// would invite a duplicate paid create while a false "sent" only invites a
// reconcile. The negative is stated only when the manager owned the client and
// httptrace reported connection progress without a write.
func (r *onceAttemptRecorder) facts() HTTPAttemptFacts {
	if r == nil {
		return HTTPAttemptFacts{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	facts := HTTPAttemptFacts{
		RequestCount:    r.transportRequests,
		ResponseStarted: r.responseStarted,
		StatusCode:      r.statusCode,
	}
	if r.traceRequests > facts.RequestCount {
		facts.RequestCount = r.traceRequests
	}
	facts.RequestWrittenObserved = r.wroteHeaders || r.wroteRequest || r.responseStarted
	switch {
	case facts.RequestWrittenObserved:
		facts.RequestWritten = true
	case r.clientOwned && r.traceObserved:
		// The manager owned the whole attempt and httptrace, which it honored,
		// never reported request bytes on the wire.
		facts.RequestWritten = false
	default:
		facts.RequestWritten = r.dispatched
	}
	return facts
}
