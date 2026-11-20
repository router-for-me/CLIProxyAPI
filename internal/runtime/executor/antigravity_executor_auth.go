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
	return antigravityProjectIDFromAuth(auth) == ""
}

func (e *AntigravityExecutor) PrepareRequestAuth(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil || !e.ShouldPrepareRequestAuth(auth) {
		return nil, nil
	}

	updated := auth.Clone()
	token, refreshedAuth, errToken := e.ensureAccessToken(ctx, updated)
	if errToken != nil {
		return nil, errToken
	}
	if refreshedAuth != nil {
		updated = refreshedAuth
	}
	if antigravityProjectIDFromAuth(updated) != "" {
		return updated, nil
	}

	projectID, errProject := e.fetchAntigravityProjectID(ctx, updated, token)
	if errProject != nil {
		return nil, prepareAntigravityProjectIDError(errProject)
	}
	if projectID == "" {
		return nil, prepareAntigravityProjectIDError(nil)
	}
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["project_id"] = projectID
	return updated, nil
}

func (e *AntigravityExecutor) ensureAccessToken(ctx context.Context, auth *cliproxyauth.Auth) (string, *cliproxyauth.Auth, error) {
	if auth == nil {
		return "", nil, statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}
	accessToken := metaStringValue(auth.Metadata, "access_token")
	expiry, _ := auth.ExpirationTime()
	if accessToken != "" && expiry.After(time.Now().Add(antigravityRequestTokenSafetyWindow)) {
		e.maybeRefreshAntigravityCreditsHint(ctx, auth, accessToken)
		return accessToken, nil, nil
	}
	refreshCtx := context.Background()
	if ctx != nil {
		if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
			refreshCtx = context.WithValue(refreshCtx, "cliproxy.roundtripper", rt)
		}
	}
	if refreshed, handled, err := helps.RefreshAuthViaHome(refreshCtx, e.cfg, auth); handled {
		if err != nil {
			return "", nil, err
		}
		token := metaStringValue(refreshed.Metadata, "access_token")
		if strings.TrimSpace(token) == "" {
			return "", nil, statusErr{code: http.StatusUnauthorized, msg: "missing access token"}
		}
		e.maybeRefreshAntigravityCreditsHint(ctx, refreshed, token)
		return token, refreshed, nil
	}

	updated, errRefresh := e.refreshToken(refreshCtx, auth.Clone(), ctx)
	if errRefresh != nil {
		return "", nil, errRefresh
	}
	return metaStringValue(updated.Metadata, "access_token"), updated, nil
}

func (e *AntigravityExecutor) refreshToken(ctx context.Context, auth *cliproxyauth.Auth, requestCtx context.Context) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}
	refreshToken := metaStringValue(auth.Metadata, "refresh_token")
	if refreshToken == "" {
		return auth, statusErr{code: http.StatusUnauthorized, msg: "missing refresh token"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
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
	auth.Metadata["access_token"] = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		auth.Metadata["refresh_token"] = tokenResp.RefreshToken
	}
	auth.Metadata["expires_in"] = tokenResp.ExpiresIn
	now := time.Now()
	auth.Metadata["timestamp"] = now.UnixMilli()
	auth.Metadata["expired"] = now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	auth.Metadata["type"] = antigravityAuthType
	if errProject := e.ensureAntigravityProjectID(ctx, auth, tokenResp.AccessToken); errProject != nil {
		log.Warnf("antigravity executor: ensure project id failed: %v", errProject)
	}
	e.queueAntigravityCreditsRefresh(ctx, auth, tokenResp.AccessToken, 0)
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

func (e *AntigravityExecutor) ensureAntigravityProjectID(ctx context.Context, auth *cliproxyauth.Auth, accessToken string) error {
	if auth == nil {
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
	acquisitionCtx, cancelAcquisition := context.WithTimeout(ctx, antigravityCredentialAcquisitionTimeout)
	defer cancelAcquisition()
	httpClient := newAntigravityHTTPClient(acquisitionCtx, e.cfg, auth, 0)
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
	statusCode := http.StatusBadRequest
	var retryAfter *time.Duration
	if cause != nil {
		msg = fmt.Sprintf("%s: %v", msg, cause)
		type statusCoder interface {
			StatusCode() int
		}
		var sc statusCoder
		if errors.As(cause, &sc) && sc != nil {
			if code := sc.StatusCode(); code > 0 {
				statusCode = code
			}
		}
		type retryAfterProvider interface {
			RetryAfter() *time.Duration
		}
		var rap retryAfterProvider
		if errors.As(cause, &rap) && rap != nil {
			retryAfter = rap.RetryAfter()
		}
	}
	return statusErr{code: statusCode, msg: msg, retryAfter: retryAfter}
}

// prepareAntigravityProjectIDError wraps a project-discovery failure during
// PrepareRequestAuth. A real upstream HTTP status is preserved verbatim so
// genuine 400/403/429 responses keep their classification; a cause with no
// HTTP status (transport/proxy failure, or no project_id resolved without an
// upstream response) is reported as a transient transport problem rather than
// an invalid-request 400, so credential rotation is not halted prematurely.
// The failure is credential-scoped because the credential cannot serve any
// model until discovery succeeds.
func prepareAntigravityProjectIDError(cause error) statusErr {
	base := missingAntigravityProjectIDError(cause)
	base.credentialScoped = true
	type statusCoder interface {
		StatusCode() int
	}
	var sc statusCoder
	hasUpstreamStatus := cause != nil && errors.As(cause, &sc) && sc != nil && sc.StatusCode() > 0
	if !hasUpstreamStatus {
		base.code = http.StatusServiceUnavailable
		base.transientTransport = true
	}
	return base
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
