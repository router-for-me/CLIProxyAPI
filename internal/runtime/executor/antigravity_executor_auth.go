package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	antigravityauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Refresh refreshes the authentication credentials using the refresh token.
func (e *AntigravityExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return auth, nil
	}
	updated, errRefresh := e.refreshToken(ctx, auth.Clone())
	if errRefresh != nil {
		return nil, errRefresh
	}
	return updated, nil
}

func (e *AntigravityExecutor) ShouldPrepareRequestAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	return antigravityProjectIDFromAuth(auth) == "" || antigravityEnterpriseTier(auth) == ""
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
	if !e.ShouldPrepareRequestAuth(updated) {
		return updated, nil
	}

	projectID, errProject := e.fetchAntigravityProjectID(ctx, updated, token)
	if errProject != nil {
		if antigravityProjectIDFromAuth(updated) != "" {
			log.Warnf("antigravity executor: fetch project id/tier failed, keeping existing project_id %s: %v", antigravityProjectIDFromAuth(updated), errProject)
			return updated, nil
		}
		return nil, missingAntigravityProjectIDError(errProject)
	}
	if projectID == "" {
		if antigravityProjectIDFromAuth(updated) != "" {
			return updated, nil
		}
		return nil, missingAntigravityProjectIDError(nil)
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

	updated, errRefresh := e.refreshToken(refreshCtx, auth.Clone())
	if errRefresh != nil {
		return "", nil, errRefresh
	}
	return metaStringValue(updated.Metadata, "access_token"), updated, nil
}

func (e *AntigravityExecutor) refreshToken(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
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
		return e.refreshTokenSingleFlight(context.WithoutCancel(ctx), auth, refreshToken)
	})
	if errRefresh != nil {
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
	// On refresh, periodically re-check license/project discovery if not an enterprise tier yet
	if !isAntigravityEnterpriseTier(auth) {
		if _, errFetch := e.fetchAntigravityProjectID(ctx, auth, tokenResp.AccessToken); errFetch != nil {
			log.Warnf("antigravity executor: refresh project id/tier check failed: %v", errFetch)
		}
	} else if errProject := e.ensureAntigravityProjectID(ctx, auth, tokenResp.AccessToken); errProject != nil {
		log.Warnf("antigravity executor: ensure project id failed: %v", errProject)
	}
	e.updateAntigravityCreditsBalance(ctx, auth, tokenResp.AccessToken)
	return auth, nil
}

func (e *AntigravityExecutor) refreshTokenSingleFlight(ctx context.Context, auth *cliproxyauth.Auth, refreshToken string) (*antigravityTokenRefreshData, error) {
	clientID := antigravityauth.ClientID
	clientSecret := antigravityauth.ClientSecret
	if auth != nil && auth.Metadata != nil {
		if cid, ok := auth.Metadata["client_id"].(string); ok && strings.TrimSpace(cid) != "" {
			clientID = strings.TrimSpace(cid)
		}
		if csec, ok := auth.Metadata["client_secret"].(string); ok && strings.TrimSpace(csec) != "" {
			clientSecret = strings.TrimSpace(csec)
		}
	}

	tokenResp, err := e.doRefreshTokenRequest(ctx, auth, refreshToken, clientID, clientSecret)
	if err == nil {
		return tokenResp, nil
	}

	// If the refresh token was issued by the legacy client, retry with legacy credentials
	if clientID != antigravityauth.LegacyClientID && isOAuthClientMismatchError(err) {
		legacyResp, errLegacy := e.doRefreshTokenRequest(ctx, auth, refreshToken, antigravityauth.LegacyClientID, antigravityauth.LegacyClientSecret)
		if errLegacy == nil {
			if auth != nil {
				if auth.Metadata == nil {
					auth.Metadata = make(map[string]any)
				}
				auth.Metadata["client_id"] = antigravityauth.LegacyClientID
				auth.Metadata["client_secret"] = antigravityauth.LegacyClientSecret
			}
			return legacyResp, nil
		}
	}

	return nil, err
}

func (e *AntigravityExecutor) doRefreshTokenRequest(ctx context.Context, auth *cliproxyauth.Auth, refreshToken, clientID, clientSecret string) (*antigravityTokenRefreshData, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if errReq != nil {
		return nil, errReq
	}
	httpReq.Header.Set("Host", "oauth2.googleapis.com")
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Real Antigravity uses Go's default User-Agent for OAuth token refresh
	httpReq.Header.Set("User-Agent", "Go-http-client/2.0")

	httpClient := newAntigravityHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, errDo := httpClient.Do(httpReq)
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
		sErr := statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
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

func isOAuthClientMismatchError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unauthorized_client") ||
		strings.Contains(msg, "invalid_client") ||
		strings.Contains(msg, "invalid_grant") ||
		strings.Contains(msg, "the oauth client was not found")
}

func (e *AntigravityExecutor) ensureAntigravityProjectID(ctx context.Context, auth *cliproxyauth.Auth, accessToken string) error {
	if auth == nil {
		return nil
	}

	if !e.ShouldPrepareRequestAuth(auth) {
		return nil
	}

	projectID, errFetch := e.fetchAntigravityProjectID(ctx, auth, accessToken)
	if errFetch != nil {
		if antigravityProjectIDFromAuth(auth) != "" {
			return nil
		}
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

// fetchAntigravityProjectID resolves the account's GCP project ID (and, for Gemini Enterprise
// (BAIC) accounts, its onboarded tier and required region), persisting the tier/region onto
// auth.Metadata so BAIC generation requests get routed correctly.
func (e *AntigravityExecutor) fetchAntigravityProjectID(ctx context.Context, auth *cliproxyauth.Auth, accessToken string) (string, error) {
	token := strings.TrimSpace(accessToken)
	if token == "" {
		token = metaStringValue(auth.Metadata, "access_token")
	}
	if token == "" {
		return "", nil
	}

	httpClient := newAntigravityHTTPClient(ctx, e.cfg, auth, 0)
	result, errFetch := sdkAuth.FetchAntigravityProjectIDWithTier(ctx, token, httpClient)
	if errFetch != nil {
		return "", errFetch
	}
	if auth != nil {
		if auth.Metadata == nil {
			auth.Metadata = make(map[string]any)
		}
		tier := strings.TrimSpace(result.Tier)
		if tier == "" {
			tier = "free-tier"
		}
		auth.Metadata["user_tier"] = tier
		if region := strings.TrimSpace(result.Region); region != "" {
			auth.Metadata["region"] = region
		}
	}
	return strings.TrimSpace(result.ProjectID), nil
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
