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
// vector, and they report typed attempt facts so a caller can tell "definitely
// not sent" from "may have been sent".

// maxSameOriginSafeRedirectHops bounds HTTPRedirectSameOriginSafe redirect following.
const maxSameOriginSafeRedirectHops = 3

// HTTPRedirectPolicy names the redirect contract applied to a one-shot request.
type HTTPRedirectPolicy string

const (
	// HTTPRedirectDeny never follows a redirect and surfaces the 3xx response to
	// the caller instead. This is the required policy for a non-idempotent create.
	HTTPRedirectDeny HTTPRedirectPolicy = "deny"
	// HTTPRedirectSameOriginSafe follows at most maxSameOriginSafeRedirectHops
	// redirects, and only for a bodyless GET or HEAD request whose target keeps
	// the same HTTPS origin. Credential headers are stripped before following.
	HTTPRedirectSameOriginSafe HTTPRedirectPolicy = "same_origin_safe"
)

// normalizedRedirectPolicy trims and lower-cases a caller supplied policy value.
func normalizedRedirectPolicy(policy HTTPRedirectPolicy) HTTPRedirectPolicy {
	return HTTPRedirectPolicy(strings.ToLower(strings.TrimSpace(string(policy))))
}

// validateRedirectPolicy reports whether the supplied policy is understood.
// An empty policy is accepted; each caller documents what it means for that path.
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
// net/http/httptrace. When neither source reports anything - which happens when
// a host supplied RoundTripper ignores httptrace - RequestWritten is reported as
// true for any attempt that reached the executor, because an ambiguous "may have
// been sent" is safe while a false "not sent" causes a double charge.
type HTTPAttemptFacts struct {
	// RequestCount counts upstream requests observed by the manager transport,
	// including redirect hops. It stays 0 when the attempt never dispatched.
	RequestCount uint32
	// RequestWritten reports whether any request byte may have reached the wire.
	RequestWritten bool
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
	// RedirectPolicy optionally constrains redirects for the executor's HTTP
	// client. Empty keeps the executor's own behavior, which is what every
	// existing caller gets today. Enforcement is only possible while the
	// manager-installed transport is in effect; see redirectPolicyRoundTripper.
	RedirectPolicy HTTPRedirectPolicy
}

// HTTPOnceRequest describes a single pinned, non-replayed raw HTTP call.
type HTTPOnceRequest struct {
	// AuthID identifies a manager-persisted credential.
	AuthID string
	// Model is recorded with the execution result. It may be empty.
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
	if errPolicy := validateRedirectPolicy(in.RedirectPolicy); errPolicy != nil {
		return cliproxyexecutor.Response{}, facts, errPolicy
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

	recorder := newOnceAttemptRecorder()
	execCtx := m.onceExecutionContext(ctx, auth, opts, routeModel, in.RedirectPolicy, recorder)

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

// DoHTTPOnce injects the credentials of the supplied auth into req and performs
// exactly one policed HTTP attempt.
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
// The response body is not read or closed; the caller owns it. Exactly one
// execution result is marked: success is HTTP 2xx alone, so a 3xx is never
// marked as success.
//
// An empty policy means HTTPRedirectDeny.
func (m *Manager) DoHTTPOnce(ctx context.Context, auth *Auth, req *http.Request, policy HTTPRedirectPolicy) (*http.Response, HTTPAttemptFacts, error) {
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
	return m.httpOnce(ctx, auth, req, "", policy)
}

// HTTPOnce resolves a persisted credential by ID, refreshes and prepares it, and
// then performs exactly one policed HTTP attempt against the supplied target.
//
// It is DoHTTPOnce plus the credential lifecycle: the ID must name a durable
// auth (Home runtime/session auths are rejected with auth_not_durable before any
// network I/O), the credential is refreshed under the existing per-auth refresh
// lock, and request preparation runs under the existing per-auth prepare lock.
func (m *Manager) HTTPOnce(ctx context.Context, in HTTPOnceRequest) (*http.Response, HTTPAttemptFacts, error) {
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

	recorder := newOnceAttemptRecorder()
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

	client := &http.Client{
		Transport:     m.transportForOnce(tracedCtx, auth, recorder, ""),
		CheckRedirect: redirectGuard(normalized, newRedirectOrigin(httpReq)),
	}

	recorder.markDispatched()
	resp, errDo := client.Do(httpReq)
	facts = recorder.facts()
	if resp != nil {
		facts.StatusCode = resp.StatusCode
		facts.ResponseStarted = true
		facts.RequestWritten = true
	} else if facts.StatusCode == 0 {
		facts.StatusCode = statusCodeFromError(errDo)
	}

	if errCancel := claudeOAuthRequestCancellation(tracedCtx, auth, errDo); errCancel != nil {
		return resp, facts, errCancel
	}

	marker := &onceResultMarker{}
	result := Result{AuthID: auth.ID, Provider: executorKeyFromAuth(auth), Model: model}
	switch {
	case resp == nil:
		result.Success = false
		result.Error = resultErrorFromError(errDo)
		if retryAfter := retryAfterFromError(errDo); retryAfter != nil {
			result.RetryAfter = retryAfter
		}
	case resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices:
		result.Success = true
	default:
		result.Success = false
		result.Error = resultErrorFromError(httpStatusError(resp.StatusCode))
		if retryAfter := retryAfterFromResponse(resp); retryAfter != nil {
			result.RetryAfter = retryAfter
		}
	}
	marker.mark(tracedCtx, m, result)
	return resp, facts, errDo
}

// httpStatusError converts an upstream HTTP status into the package error type
// so MarkResult keeps its status-driven cooldown classification.
func httpStatusError(status int) error {
	return &Error{Code: "upstream_http_status", Message: "upstream returned status " + strconv.Itoa(status), HTTPStatus: status}
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
// The refresh is skipped, without error, when the credential does not need one
// or when another goroutine already owns it. It is also skipped when no executor
// is registered under the raw provider string, because that is the only key the
// manager refresh path resolves; failing there would reject openai-compatibility
// credentials that never had a refreshable executor to begin with.
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
	provider := ""
	if current != nil {
		provider = strings.TrimSpace(current.Provider)
	}
	m.mu.RUnlock()
	if !needsRefresh {
		return auth, nil
	}
	if m.executorFor(provider) == nil {
		return auth, nil
	}
	if !m.markRefreshPending(id, now) {
		return auth, nil
	}
	refreshed, errRefresh := m.refreshAuthForRequest(ctx, id, "")
	if errRefresh != nil {
		return nil, errRefresh
	}
	if refreshed == nil {
		return auth, nil
	}
	return refreshed, nil
}

// onceResultMarker guarantees at most one MarkResult call per one-shot request.
type onceResultMarker struct {
	once sync.Once
}

// mark records the execution result the first time it is called and never again.
func (r *onceResultMarker) mark(ctx context.Context, m *Manager, result Result) {
	if r == nil || m == nil {
		return
	}
	r.once.Do(func() {
		m.MarkResult(ctx, result)
	})
}

// onceExecutionContext builds the executor context for a one-shot execution. It
// mirrors the retrying path (per-auth transport under both round tripper keys,
// requested-model alias attribution) and adds the httptrace hooks the attempt
// facts are derived from.
func (m *Manager) onceExecutionContext(ctx context.Context, auth *Auth, opts cliproxyexecutor.Options, routeModel string, policy HTTPRedirectPolicy, recorder *onceAttemptRecorder) context.Context {
	execCtx := contextWithRequestedModelAlias(ctx, opts, routeModel)
	transport := m.transportForOnce(ctx, auth, recorder, policy)
	execCtx = context.WithValue(execCtx, roundTripperContextKey{}, transport)
	execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", transport)
	return httptrace.WithClientTrace(execCtx, recorder.clientTrace())
}

// transportForOnce resolves the transport for a one-shot attempt using the same
// priority the shared executor helper applies - auth proxy, runtime config
// proxy, context or per-auth RoundTripper, default transport - and wraps it so
// the manager can count requests, observe statuses and, when a policy is set,
// keep a shared http.Client from following a redirect.
func (m *Manager) transportForOnce(ctx context.Context, auth *Auth, recorder *onceAttemptRecorder, policy HTTPRedirectPolicy) http.RoundTripper {
	var base http.RoundTripper

	proxyURL := ""
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" {
		if cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config); cfg != nil {
			proxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	if proxyURL != "" {
		transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
		if errBuild != nil || transport == nil {
			log.Debugf("one-shot request: proxy transport unavailable for %s: %v", proxyutil.Redact(proxyURL), errBuild)
		} else {
			base = transport
		}
	}
	if base == nil && ctx != nil {
		if rt, ok := ctx.Value(roundTripperContextKey{}).(http.RoundTripper); ok && rt != nil {
			base = rt
		} else if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
			base = rt
		}
	}
	if base == nil {
		if rt := m.roundTripperFor(auth); rt != nil {
			base = rt
		}
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &onceRoundTripper{base: base, recorder: recorder, policy: normalizedRedirectPolicy(policy)}
}

// onceRoundTripper records attempt facts and, for executor-owned clients, keeps
// a redirect from being followed.
//
// A provider executor builds its own http.Client, so the manager cannot install
// a CheckRedirect on it. What it can do is remove the Location header from a
// disallowed 3xx: net/http returns a 3xx without Location to its caller instead
// of following it, so the paid request is never replayed. The policy is only
// enforced while this transport is in effect; an executor that resolves a proxy
// from the auth or the runtime config builds its own transport and governs its
// own redirects, which is why the reliable guarantee lives in DoHTTPOnce.
type onceRoundTripper struct {
	base     http.RoundTripper
	recorder *onceAttemptRecorder
	policy   HTTPRedirectPolicy
	mu       sync.Mutex
	origin   *redirectOrigin
}

// RoundTrip implements http.RoundTripper.
func (rt *onceRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	hop := rt.recorder.observeRequest()
	origin := rt.rememberOrigin(req)

	if rt.policy != "" && origin != nil && req.URL != nil && req.URL.String() != origin.target() {
		// A followed redirect reached the transport; strip credentials so they
		// never travel to a target the caller did not address.
		stripSensitiveRedirectHeaders(req.Header)
	}

	resp, err := rt.base.RoundTrip(req)
	if resp != nil {
		rt.recorder.observeResponse(resp.StatusCode)
		if rt.policy != "" && isRedirectStatus(resp.StatusCode) && !sameOriginSafeRedirectAllowed(rt.policy, origin, resp.Header.Get("Location"), int(hop)) {
			resp.Header.Del("Location")
		}
	}
	return resp, err
}

// rememberOrigin records the first request the transport observed.
func (rt *onceRoundTripper) rememberOrigin(req *http.Request) *redirectOrigin {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.origin == nil {
		rt.origin = newRedirectOrigin(req)
	}
	return rt.origin
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
		hasBody: req.Body != nil || req.ContentLength > 0,
	}
}

// target returns the original absolute URL as a string.
func (o *redirectOrigin) target() string {
	if o == nil || o.url == nil {
		return ""
	}
	return o.url.String()
}

// redirectGuard builds the CheckRedirect implementation for a policy. Denied
// redirects surface the 3xx response to the caller rather than raising an error.
func redirectGuard(policy HTTPRedirectPolicy, origin *redirectOrigin) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		target := ""
		if req != nil && req.URL != nil {
			target = req.URL.String()
		}
		if !sameOriginSafeRedirectAllowed(policy, origin, target, len(via)) {
			return http.ErrUseLastResponse
		}
		if req != nil {
			stripSensitiveRedirectHeaders(req.Header)
		}
		return nil
	}
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

// isRedirectStatus reports whether a status triggers redirect handling.
func isRedirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
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
type onceAttemptRecorder struct {
	mu                sync.Mutex
	dispatched        bool
	transportObserved bool
	traceObserved     bool
	transportRequests uint32
	traceRequests     uint32
	wroteHeaders      bool
	wroteRequest      bool
	responseStarted   bool
	statusCode        int
}

// newOnceAttemptRecorder returns a recorder ready for a single attempt.
func newOnceAttemptRecorder() *onceAttemptRecorder {
	return &onceAttemptRecorder{}
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
	r.transportObserved = true
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
// RequestWritten is deliberately biased: an attempt that dispatched but produced
// no observation at all is reported as written, because a false "not sent" would
// invite a duplicate paid create while a false "sent" only invites a reconcile.
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
	if !r.transportObserved {
		facts.RequestCount = r.traceRequests
	}
	switch {
	case r.wroteHeaders || r.wroteRequest || r.responseStarted:
		facts.RequestWritten = true
	case r.traceObserved:
		// httptrace was honored and never reported request bytes on the wire.
		facts.RequestWritten = false
	default:
		facts.RequestWritten = r.dispatched
	}
	return facts
}
