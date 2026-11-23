package executor

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	log "github.com/sirupsen/logrus"
)

const antigravityCredentialAcquisitionTimeout = 30 * time.Second

var antigravityInitialBaseTransport = antigravityBaseTransport

var antigravityNow = time.Now

// Refresh refreshes the authentication credentials using the refresh token.
func (e *AntigravityExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return auth, nil
	}
	updated, errRefresh := e.refreshToken(ctx, auth.Clone(), ctx)
	if errRefresh != nil {
		return nil, errRefresh
	}
	return updated, nil
}

func (e *AntigravityExecutor) ShouldPrepareRequestAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	return antigravityProjectIDFromAuth(auth) == ""
}

func antigravityNeedsAccessTokenRefresh(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	accessToken := metaStringValue(auth.Metadata, "access_token")
	if accessToken == "" {
		return true
	}
	expiry, _ := auth.ExpirationTime()
	return !expiry.After(antigravityNow().Add(antigravityRequestTokenSafetyWindow))
}

// detachedAntigravityCredentialContext preserves non-cancellation context
// values (such as transport decorators and injected round trippers) while
// detaching caller cancellation and stripping execution-scoped request proxy
// overrides so credential refresh and credential-wide metadata stay on the
// credential or global proxy.
func detachedAntigravityCredentialContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return cliproxyexecutor.WithoutRequestProxyURL(context.WithoutCancel(ctx))
}

func (e *AntigravityExecutor) PrepareRequestAuth(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil || !e.ShouldPrepareRequestAuth(auth) {
		return nil, nil
	}

	updated := auth.Clone()
	token, refreshedAuth, refreshedLocal, errToken := e.ensureAccessTokenWithOptions(ctx, updated, false)
	if errToken != nil {
		return nil, errToken
	}
	if refreshedAuth != nil {
		updated = refreshedAuth
	}
	if antigravityProjectIDFromAuth(updated) != "" {
		e.maybeRefreshAntigravityCreditsHint(ctx, updated, token)
		return updated, nil
	}

	projectID, errProject := e.fetchAntigravityProjectID(ctx, updated, token)
	if errProject != nil {
		if ctx != nil && ctx.Err() != nil && antigravityStatusCodeFromError(errProject) == 0 {
			return nil, errProject
		}
		preparedErr := prepareAntigravityProjectIDError(errProject)
		if preparedErr.tosViolation {
			closeAntigravityAuthIdleTransports(updated)
			e.applyAntigravityTOSState(e.cfg, updated, antigravityResponseBodyFromError(errProject))
		} else if preparedErr.code == http.StatusForbidden && cliproxyauth.IsAntigravityValidationRequiredError(preparedErr) {
			closeAntigravityAuthIdleTransports(updated)
		}
		return nil, preparedErr
	}
	if projectID == "" {
		return nil, prepareAntigravityProjectIDError(nil)
	}
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["project_id"] = projectID
	if refreshedLocal {
		e.queueAntigravityCreditsRefresh(detachedAntigravityCredentialContext(ctx), updated, token, 0)
	} else {
		e.maybeRefreshAntigravityCreditsHint(ctx, updated, token)
	}
	return updated, nil
}

func (e *AntigravityExecutor) ensureAccessToken(ctx context.Context, auth *cliproxyauth.Auth) (string, *cliproxyauth.Auth, error) {
	token, updated, _, errToken := e.ensureAccessTokenWithOptions(ctx, auth, true)
	return token, updated, errToken
}

func (e *AntigravityExecutor) ensureAccessTokenWithOptions(ctx context.Context, auth *cliproxyauth.Auth, discoverProject bool) (string, *cliproxyauth.Auth, bool, error) {
	if auth == nil {
		return "", nil, false, statusErr{code: http.StatusUnauthorized, msg: "missing auth", credentialScoped: true}
	}
	if !antigravityNeedsAccessTokenRefresh(auth) {
		accessToken := metaStringValue(auth.Metadata, "access_token")
		if discoverProject {
			e.maybeRefreshAntigravityCreditsHint(ctx, auth, accessToken)
		}
		return accessToken, nil, false, nil
	}
	var refreshedLocal bool
	var refreshedViaHome bool
	updated, errRefresh := cliproxyauth.RefreshExecutionAuth(ctx, auth, func(callCtx context.Context, latest *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
		if !antigravityNeedsAccessTokenRefresh(latest) {
			return nil, nil
		}
		refreshCtx := detachedAntigravityCredentialContext(callCtx)
		if refreshed, handled, errHome := helps.RefreshAuthViaHome(refreshCtx, e.cfg, latest); handled {
			if errHome != nil {
				return nil, errHome
			}
			token := metaStringValue(refreshed.Metadata, "access_token")
			if strings.TrimSpace(token) == "" {
				return nil, statusErr{code: http.StatusUnauthorized, msg: "missing access token", credentialScoped: true}
			}
			refreshedViaHome = true
			if discoverProject {
				e.maybeRefreshAntigravityCreditsHint(callCtx, refreshed, token)
			}
			return refreshed, nil
		}
		refreshed, errToken := e.refreshTokenWithOptions(refreshCtx, latest.Clone(), callCtx, discoverProject)
		if errToken != nil {
			return nil, errToken
		}
		refreshedLocal = true
		return refreshed, nil
	})
	if errRefresh != nil {
		return "", nil, false, errRefresh
	}
	if updated == nil {
		accessToken := metaStringValue(auth.Metadata, "access_token")
		if discoverProject {
			e.maybeRefreshAntigravityCreditsHint(ctx, auth, accessToken)
		}
		return accessToken, nil, false, nil
	}
	token := metaStringValue(updated.Metadata, "access_token")
	if strings.TrimSpace(token) == "" {
		return "", nil, false, statusErr{code: http.StatusUnauthorized, msg: "missing access token", credentialScoped: true}
	}
	if discoverProject && !refreshedLocal && !refreshedViaHome {
		e.maybeRefreshAntigravityCreditsHint(ctx, updated, token)
	}
	return token, updated, refreshedLocal, nil
}

func (e *AntigravityExecutor) refreshToken(ctx context.Context, auth *cliproxyauth.Auth, requestCtx context.Context) (*cliproxyauth.Auth, error) {
	return e.refreshTokenWithOptions(ctx, auth, requestCtx, true)
}

func (e *AntigravityExecutor) refreshTokenWithOptions(ctx context.Context, auth *cliproxyauth.Auth, requestCtx context.Context, discoverProject bool) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing auth", credentialScoped: true}
	}
	refreshToken := metaStringValue(auth.Metadata, "refresh_token")
	if refreshToken == "" {
		return auth, statusErr{code: http.StatusUnauthorized, msg: "missing refresh token", credentialScoped: true}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = cliproxyexecutor.WithoutRequestProxyURL(ctx)
	refreshToken = strings.TrimSpace(refreshToken)

	result, errRefresh, _ := antigravityRefreshGroup.Do(refreshToken, func() (interface{}, error) {
		// A caller may leave, but shared credential acquisition must still be bounded.
		refreshCtx, cancelRefresh := context.WithTimeout(context.WithoutCancel(ctx), antigravityCredentialAcquisitionTimeout)
		defer cancelRefresh()
		return e.refreshTokenSingleFlight(refreshCtx, auth, refreshToken)
	})
	if errRefresh != nil {
		// Any error returned by the shared flight came from the real OAuth HTTP
		// call inside the closure (local missing-credential checks return above,
		// before the group call). Attribute it as an upstream attempt on THIS
		// request's tracker so concurrent callers sharing the flight each record
		// the upstream failure on their own tracker, not just the leader's.
		cliproxyexecutor.MarkUpstreamAttempt(requestCtx)
		return auth, errRefresh
	}
	tokenResp, ok := result.(*antigravityTokenRefreshData)
	if !ok || tokenResp == nil {
		return auth, fmt.Errorf("antigravity token refresh failed: invalid single-flight result")
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	grantIdentity := antigravitySignatureGrantIdentity(auth)
	grantRetained := strings.TrimSpace(metaStringValue(auth.Metadata, antigravitySignatureGrantScopeMetadataKey))
	grantBinding := strings.TrimSpace(metaStringValue(auth.Metadata, antigravitySignatureGrantBindingMetadataKey))
	currentBinding := antigravitySignatureGrantBinding(auth)
	if grantRetained != "" && grantBinding == currentBinding {
		// Preserve identity only when a prior refresh explicitly bound this exact token pair.
		if grantIdentity != "" {
			auth.Metadata[antigravitySignatureGrantScopeMetadataKey] = grantIdentity
		}
	} else {
		delete(auth.Metadata, antigravitySignatureGrantScopeMetadataKey)
	}
	auth.Metadata["access_token"] = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		auth.Metadata["refresh_token"] = tokenResp.RefreshToken
	}
	if retained := strings.TrimSpace(metaStringValue(auth.Metadata, antigravitySignatureGrantScopeMetadataKey)); retained != "" {
		auth.Metadata[antigravitySignatureGrantBindingMetadataKey] = antigravitySignatureGrantBinding(auth)
	} else {
		auth.Metadata[antigravitySignatureGrantScopeMetadataKey] = antigravitySignatureGrantIdentity(auth)
		auth.Metadata[antigravitySignatureGrantBindingMetadataKey] = antigravitySignatureGrantBinding(auth)
	}
	auth.Metadata["expires_in"] = tokenResp.ExpiresIn
	now := time.Now()
	auth.Metadata["timestamp"] = now.UnixMilli()
	auth.Metadata["expired"] = now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	auth.Metadata["type"] = antigravityAuthType
	if discoverProject && !isAntigravityAuthDisabled(auth) {
		if errProject := e.ensureAntigravityProjectID(ctx, auth, tokenResp.AccessToken); errProject != nil {
			log.Warnf("antigravity executor: ensure project id failed: %v", errProject)
		}
		e.queueAntigravityCreditsRefresh(ctx, auth, tokenResp.AccessToken, 0)
	}
	return auth, nil
}

func (e *AntigravityExecutor) refreshTokenSingleFlight(ctx context.Context, auth *cliproxyauth.Auth, refreshToken string) (*antigravityTokenRefreshData, error) {
	form := url.Values{}
	form.Set("client_id", antigravityClientID)
	form.Set("client_secret", antigravityClientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if errReq != nil {
		return nil, errReq
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Leave User-Agent unset so net/http emits Go-http-client/2.0 on HTTP/2
	// and Go-http-client/1.1 on HTTP/1.1, matching the negotiated wire protocol.

	httpClient, cleanup := newAntigravityOAuthHTTPClient(ctx, e.cfg, auth, 0)
	defer cleanup()
	httpResp, errDo := helps.WithAntigravityHTTPClientTrace(httpClient, auth, "oauth_refresh").Do(httpReq)
	if errDo != nil {
		return nil, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
	}()

	bodyBytes, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		return nil, errRead
	}

	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		// Token refresh failures are credential-scoped: the credential cannot be
		// used for any model until it recovers.
		sErr := statusErr{code: httpResp.StatusCode, msg: string(bodyBytes), credentialScoped: true}
		if httpResp.StatusCode == http.StatusTooManyRequests {
			if retryAfter, parseErr := helps.ParseRetryDelay(bodyBytes); parseErr == nil && retryAfter != nil {
				sErr.retryAfter = retryAfter
			}
		}
		return nil, sErr
	}

	var tokenResp antigravityTokenRefreshData
	if errUnmarshal := json.Unmarshal(bodyBytes, &tokenResp); errUnmarshal != nil {
		return nil, errUnmarshal
	}

	return &tokenResp, nil
}

// newAntigravityOAuthHTTPClient creates a standard Go HTTP/2-capable client for
// Antigravity OAuth token refresh on oauth2.googleapis.com, keeping HTTP/1.1
// without ALPN scoped to Cloud Code PA endpoints. Any resolved *http.Transport
// is cloned into an isolated per-refresh transport whose cleanup closes idle
// connections so distinct credentials never share a TCP/TLS connection and
// one-off refreshes do not retain idle sockets.
func newAntigravityOAuthHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) (clientOut *http.Client, cleanup func()) {
	cleanup = func() {}
	defer func() { clientOut = httptransport.Decorate(ctx, clientOut) }()

	client := helps.NewProxyAwareHTTPClient(httptransport.WithoutDecorator(ctx), cfg, auth, timeout)
	var base *http.Transport
	if client.Transport != nil {
		transport, ok := client.Transport.(*http.Transport)
		if !ok {
			// A custom non-*http.Transport RoundTripper owns its own protocol behavior.
			return client, cleanup
		}
		base = transport
	}
	if base == nil {
		if antigravityBaseTransport != nil && antigravityBaseTransport != antigravityInitialBaseTransport {
			base = antigravityBaseTransport
		} else if defaultTransport, okDefault := http.DefaultTransport.(*http.Transport); okDefault && defaultTransport != nil {
			base = defaultTransport
		} else if http.DefaultTransport != nil && !okDefault {
			// Preserve a custom non-*http.Transport installed on http.DefaultTransport.
			client.Transport = nil
			return client, cleanup
		}
	}

	var owned *http.Transport
	if base != nil {
		owned = base.Clone()
		owned.MaxIdleConnsPerHost = -1
		if len(owned.TLSNextProto) > 0 {
			if owned.TLSNextProto["h2"] != nil {
				// Clear externally installed TLSNextProto handlers (such as x/net/http2.ConfigureTransport)
				// whose closures capture a shared HTTP/2 connection pool, so net/http installs a fresh
				// per-refresh HTTP/2 transport owned by this clone.
				owned.TLSNextProto = nil
				owned.ForceAttemptHTTP2 = true
			} else {
				// Keep HTTP/2 disabled while dropping any custom handler closures that could share pools.
				owned.TLSNextProto = make(map[string]func(authority string, c *tls.Conn) http.RoundTripper)
			}
		}
	} else {
		owned = &http.Transport{
			ForceAttemptHTTP2:   true,
			MaxIdleConnsPerHost: -1,
		}
	}
	client.Transport = owned
	cleanup = owned.CloseIdleConnections
	return client, cleanup
}

// newAntigravityProjectDiscoveryHTTPClient clones the credential's HTTP/1.1
// Antigravity transport into an isolated per-discovery client whose cleanup
// closes idle sockets and cancels abandoned in-flight dials (such as a stalled
// SOCKS5 handshake after net/http.Transport.getConn detaches req.Context())
// without disturbing the credential's shared connection pool.
func newAntigravityProjectDiscoveryHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) (clientOut *http.Client, cleanup func()) {
	cleanup = func() {}
	defer func() { clientOut = httptransport.Decorate(ctx, clientOut) }()

	client := newAntigravityHTTPClient(httptransport.WithoutDecorator(ctx), cfg, auth, 0)
	if client == nil {
		return &http.Client{}, cleanup
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport == nil {
		return client, cleanup
	}
	owned := transport.Clone()
	owned.MaxIdleConnsPerHost = -1
	clone := *client
	clone.Transport = owned
	cleanup = owned.CloseIdleConnections
	return &clone, cleanup
}

func (e *AntigravityExecutor) ensureAntigravityProjectID(ctx context.Context, auth *cliproxyauth.Auth, accessToken string) error {
	if isAntigravityAuthDisabled(auth) {
		return nil
	}

	if antigravityProjectIDFromAuth(auth) != "" {
		return nil
	}

	projectID, errFetch := e.fetchAntigravityProjectID(ctx, auth, accessToken)
	if errFetch != nil {
		return errFetch
	}
	if projectID == "" {
		return nil
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["project_id"] = projectID

	return nil
}

func (e *AntigravityExecutor) fetchAntigravityProjectID(ctx context.Context, auth *cliproxyauth.Auth, accessToken string) (string, error) {
	token := strings.TrimSpace(accessToken)
	if token == "" {
		token = metaStringValue(auth.Metadata, "access_token")
	}
	if token == "" {
		return "", nil
	}

	if ctx == nil {
		ctx = context.Background()
	}
	// Project discovery is required credential preparation, not a generation request.
	// Exclude execution-scoped request proxy overrides while preserving caller cancellation.
	acquisitionCtx, cancelAcquisition := context.WithTimeout(cliproxyexecutor.WithoutRequestProxyURL(ctx), antigravityCredentialAcquisitionTimeout)
	defer cancelAcquisition()
	httpClient, cleanup := newAntigravityProjectDiscoveryHTTPClient(acquisitionCtx, e.cfg, auth)
	defer cleanup()
	tracedClient := helps.WithAntigravityHTTPClientTrace(httpClient, auth, "project_discovery")
	projectID, errFetch := sdkAuth.FetchAntigravityProjectID(acquisitionCtx, token, tracedClient)
	if errFetch != nil {
		cliproxyexecutor.MarkUpstreamAttempt(ctx)
		return "", errFetch
	}
	return strings.TrimSpace(projectID), nil
}

func (e *AntigravityExecutor) projectIDForRequest(_ context.Context, auth *cliproxyauth.Auth, _ string) (string, error) {
	if projectID := antigravityProjectIDFromAuth(auth); projectID != "" {
		return projectID, nil
	}
	return "", missingAntigravityProjectIDError(nil)
}

func antigravityProjectIDFromAuth(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if pid, ok := auth.Metadata["project_id"].(string); ok {
		return strings.TrimSpace(pid)
	}
	return ""
}

func missingAntigravityProjectIDError(cause error) statusErr {
	msg := "antigravity auth missing project_id"
	if cause == nil {
		return statusErr{
			code:             http.StatusServiceUnavailable,
			msg:              msg,
			credentialScoped: true,
		}
	}

	msg = fmt.Sprintf("%s: %v", msg, cause)
	if errors.Is(cause, antigravity.ErrProjectIDNotFound) {
		return statusErr{
			code:             http.StatusServiceUnavailable,
			msg:              msg,
			cause:            cause,
			credentialScoped: true,
		}
	}

	statusCode := antigravityStatusCodeFromError(cause)

	if statusCode == 0 {
		if isCauseCredentialScoped(cause) {
			return statusErr{
				code:             http.StatusServiceUnavailable,
				msg:              msg,
				cause:            cause,
				credentialScoped: true,
			}
		}
		if errors.Is(cause, context.Canceled) {
			return statusErr{
				code:  0,
				msg:   msg,
				cause: cause,
			}
		}
		return statusErr{
			code:               0,
			msg:                msg,
			cause:              cause,
			transientTransport: true,
		}
	}

	sErr := statusErr{
		code:             statusCode,
		msg:              msg,
		cause:            cause,
		credentialScoped: isCauseCredentialScoped(cause),
	}

	type retryAfterProvider interface {
		RetryAfter() *time.Duration
	}
	var rap retryAfterProvider
	if errors.As(cause, &rap) && rap != nil {
		sErr.retryAfter = rap.RetryAfter()
	}

	type transientRateLimitMarker interface {
		IsTransientRateLimit() bool
	}
	var trl transientRateLimitMarker
	if errors.As(cause, &trl) && trl != nil && trl.IsTransientRateLimit() {
		sErr.transientRateLimit = true
	}

	type streamTruncatedMarker interface {
		IsStreamTruncated() bool
	}
	var stm streamTruncatedMarker
	if errors.As(cause, &stm) && stm != nil && stm.IsStreamTruncated() {
		sErr.streamTruncated = true
	}

	responseBody := antigravityResponseBodyFromError(cause)
	if statusCode == http.StatusTooManyRequests && sErr.retryAfter == nil && len(responseBody) > 0 {
		if retryAfter, parseErr := helps.ParseRetryDelay(responseBody); parseErr == nil && retryAfter != nil {
			sErr.retryAfter = retryAfter
		}
	}

	preserveAntigravityCauseTOSMetadata(&sErr, cause, statusCode, responseBody)
	if !sErr.credentialScoped && !sErr.tosViolation {
		type errorCoder interface {
			ErrorCode() string
		}
		var ec errorCoder
		if errors.As(cause, &ec) && ec != nil && strings.EqualFold(strings.TrimSpace(ec.ErrorCode()), cliproxyauth.ErrorCodeTransientTransport) {
			sErr.transientTransport = true
		}
	}
	return sErr
}

// prepareAntigravityProjectIDError wraps a project-discovery failure during
// PrepareRequestAuth. Upstream HTTP responses keep their status, error chain,
// and TOS classification as credential-scoped failures; genuinely missing
// project_id responses map to a credential-scoped 503 so broken credentials
// enter cooldown instead of halting rotation with HTTP 400; status-less
// transport/timeout failures preserve transient transport classification and
// Unwrap() without cooling healthy credentials.
func prepareAntigravityProjectIDError(cause error) statusErr {
	base := missingAntigravityProjectIDError(cause)
	if !base.transientTransport && !errors.Is(cause, context.Canceled) {
		base.credentialScoped = true
	}
	return base
}

func antigravityStatusCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	type statusCoder interface {
		StatusCode() int
	}
	var sc statusCoder
	if errors.As(err, &sc) && sc != nil {
		if code := sc.StatusCode(); code > 0 {
			return code
		}
	}
	return 0
}

func isCauseCredentialScoped(cause error) bool {
	if cause == nil {
		return false
	}
	type credentialScopedProvider interface {
		IsCredentialScoped() bool
	}
	var csp credentialScopedProvider
	if errors.As(cause, &csp) && csp != nil && csp.IsCredentialScoped() {
		return true
	}
	type errorCoder interface {
		ErrorCode() string
	}
	var ec errorCoder
	if errors.As(cause, &ec) && ec != nil && strings.EqualFold(strings.TrimSpace(ec.ErrorCode()), cliproxyauth.ErrorCodeCredentialQuota) {
		return true
	}
	return false
}

func preserveAntigravityCauseTOSMetadata(dst *statusErr, cause error, statusCode int, responseBody []byte) {
	if dst == nil || cause == nil {
		return
	}
	type tosViolationMarker interface {
		IsTerminalTOSViolation() bool
	}
	var tvm tosViolationMarker
	if errors.As(cause, &tvm) && tvm != nil && tvm.IsTerminalTOSViolation() {
		dst.tosViolation = true
		dst.credentialScoped = true
	}
	type errorCoder interface {
		ErrorCode() string
	}
	var ec errorCoder
	if errors.As(cause, &ec) && ec != nil && strings.EqualFold(strings.TrimSpace(ec.ErrorCode()), "tos_violation") {
		dst.tosViolation = true
		dst.credentialScoped = true
	}
	type tosMetadataProvider interface {
		TOSAppealMetadata() map[string]string
	}
	var tmp tosMetadataProvider
	if errors.As(cause, &tmp) && tmp != nil {
		if meta := tmp.TOSAppealMetadata(); len(meta) > 0 {
			urlVal := strings.TrimSpace(meta["tos_appeal_url"])
			textVal := strings.TrimSpace(meta["tos_appeal_url_text"])
			if urlVal != "" || textVal != "" {
				dst.tosViolation = true
				dst.credentialScoped = true
				dst.tosAppealURL = urlVal
				dst.tosAppealURLText = textVal
			}
		}
	}
	var authErr *cliproxyauth.Error
	if errors.As(cause, &authErr) && authErr != nil {
		if strings.EqualFold(strings.TrimSpace(authErr.Code), "tos_violation") {
			dst.tosViolation = true
			dst.credentialScoped = true
		}
		if len(authErr.Metadata) > 0 {
			if dst.tosAppealURL == "" {
				dst.tosAppealURL = strings.TrimSpace(authErr.Metadata["tos_appeal_url"])
			}
			if dst.tosAppealURLText == "" {
				dst.tosAppealURLText = strings.TrimSpace(authErr.Metadata["tos_appeal_url_text"])
			}
		}
	}
	if statusCode == http.StatusForbidden && (!dst.tosViolation || (dst.tosAppealURL == "" && dst.tosAppealURLText == "")) {
		if violation := parseAntigravityTOSViolation(responseBody); violation != nil {
			dst.tosViolation = true
			dst.credentialScoped = true
			if dst.tosAppealURL == "" {
				dst.tosAppealURL = violation.AppealURL
			}
			if dst.tosAppealURLText == "" {
				dst.tosAppealURLText = violation.AppealURLText
			}
		}
	}
}

func antigravityResponseBodyFromError(err error) []byte {
	if err == nil {
		return nil
	}
	type responseBodyProvider interface {
		ResponseBody() []byte
	}
	var rbp responseBodyProvider
	if errors.As(err, &rbp) && rbp != nil {
		if body := rbp.ResponseBody(); len(body) > 0 {
			return body
		}
	}
	raw := strings.TrimSpace(err.Error())
	if raw == "" {
		return nil
	}
	if idx := strings.IndexByte(raw, '{'); idx >= 0 {
		candidate := []byte(strings.TrimSpace(raw[idx:]))
		if ojson.ValidBytes(candidate) {
			return candidate
		}
	}
	return nil
}

func metaStringValue(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if v, ok := metadata[key]; ok {
		switch typed := v.(type) {
		case string:
			return strings.TrimSpace(typed)
		case []byte:
			return strings.TrimSpace(string(typed))
		}
	}
	return ""
}
