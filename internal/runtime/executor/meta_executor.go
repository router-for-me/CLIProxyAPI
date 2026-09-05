package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	metaauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/singleflight"
)

var metaRefreshGroup singleflight.Group

// MetaExecutor implements the cliproxyauth.ProviderExecutor for Meta Muse models (api.meta.ai).
type MetaExecutor struct {
	cfg    *config.Config
	compat *OpenAICompatExecutor
}

// NewMetaExecutor constructs a new Meta executor.
func NewMetaExecutor(cfg *config.Config) *MetaExecutor {
	return &MetaExecutor{
		cfg:    cfg,
		compat: NewOpenAICompatExecutor("meta", cfg),
	}
}

// Identifier returns the provider identifier "meta".
func (e *MetaExecutor) Identifier() string {
	return "meta"
}

// PrepareRequest injects Meta credentials into the outgoing HTTP request.
func (e *MetaExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	_, token := metaCreds(auth)
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else {
		req.Header.Del("Authorization")
	}
	req.Header.Set("User-Agent", "muse-code/1.0.2")

	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Meta credentials into the request and executes it.
func (e *MetaExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("meta executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	enriched, err := e.ensureAuth(ctx, auth)
	if err != nil {
		return nil, err
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, enriched); err != nil {
		return nil, err
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, enriched, 0)
	return httpClient.Do(httpReq)
}

// Execute executes a non-streaming completion against the Meta API.
func (e *MetaExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	enriched, err := e.ensureAuth(ctx, auth)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	resp, err := e.compat.Execute(ctx, enriched, req, opts)
	if err != nil {
		if se, ok := err.(statusErr); ok && se.code == http.StatusTooManyRequests {
			if retryAfter := parseMetaRetryAfter(se.code, []byte(se.msg), time.Now()); retryAfter != nil {
				se.retryAfter = retryAfter
				return resp, se
			}
		}
	}
	return resp, err
}

// ExecuteStream executes a streaming request against the Meta API.
func (e *MetaExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	enriched, err := e.ensureAuth(ctx, auth)
	if err != nil {
		return nil, err
	}
	streamRes, err := e.compat.ExecuteStream(ctx, enriched, req, opts)
	if err != nil {
		if se, ok := err.(statusErr); ok && se.code == http.StatusTooManyRequests {
			if retryAfter := parseMetaRetryAfter(se.code, []byte(se.msg), time.Now()); retryAfter != nil {
				se.retryAfter = retryAfter
				return streamRes, se
			}
		}
	}
	return streamRes, err
}

// CountTokens counts tokens using standard OpenAI token counting.
func (e *MetaExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	enriched, err := e.ensureAuth(ctx, auth)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return e.compat.CountTokens(ctx, enriched, req, opts)
}

// Refresh mints an API key from a DCA token if needed.
func (e *MetaExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("meta executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, statusErr{code: http.StatusInternalServerError, msg: "meta executor: auth is nil"}
	}

	dcaToken := extractDCAToken(auth)
	if dcaToken == "" {
		if _, token := metaCreds(auth); token != "" {
			return auth, nil
		}
		return nil, statusErr{
			code: http.StatusUnauthorized,
			msg:  "meta executor: missing API key or DCA token",
		}
	}

	mintRes, err, _ := metaRefreshGroup.Do(dcaToken, func() (any, error) {
		authSvc := metaauth.NewMetaAuthWithProxyURL(e.cfg, auth.ProxyURL)
		return authSvc.MintAPIKey(ctx, dcaToken)
	})
	if err != nil {
		return nil, fmt.Errorf("meta executor: mint API key failed: %w", err)
	}

	minted, ok := mintRes.(*metaauth.MintedKeyResponse)
	if !ok || minted == nil || minted.APIKey == "" {
		return nil, fmt.Errorf("meta executor: mint API key returned empty key")
	}

	baseURL, _ := metaCreds(auth)
	if mintedURL := strings.TrimSpace(minted.BaseURL); mintedURL != "" {
		baseURL = mintedURL
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["base_url"] = baseURL
	auth.Metadata["api_key"] = minted.APIKey
	auth.Metadata["access_token"] = minted.APIKey
	auth.Metadata["dca_token"] = dcaToken
	delete(auth.Metadata, "expired")
	if minted.UserEmail != "" {
		auth.Metadata["email"] = minted.UserEmail
	}
	if minted.UserFullName != "" {
		auth.Metadata["name"] = minted.UserFullName
	}
	auth.Metadata["type"] = "meta"
	nowStr := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = nowStr

	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["base_url"] = baseURL
	auth.Attributes["api_key"] = minted.APIKey
	auth.Attributes["access_token"] = minted.APIKey

	var storage *metaauth.MetaTokenStorage
	if ms, ok := auth.Storage.(*metaauth.MetaTokenStorage); ok && ms != nil {
		storage = ms
		storage.APIKey = minted.APIKey
		storage.AccessToken = minted.APIKey
		storage.DCAToken = dcaToken
		storage.Expired = ""
		if minted.UserEmail != "" {
			storage.Email = minted.UserEmail
		}
		if minted.UserFullName != "" {
			storage.Name = minted.UserFullName
		}
		storage.LastRefresh = nowStr
	} else {
		storage = &metaauth.MetaTokenStorage{
			Type:        "meta",
			AuthKind:    "oauth",
			AccessToken: minted.APIKey,
			APIKey:      minted.APIKey,
			DCAToken:    dcaToken,
			Email:       minted.UserEmail,
			Name:        minted.UserFullName,
			LastRefresh: nowStr,
			Metadata:    auth.Metadata,
		}
		auth.Storage = storage
	}

	storage.BaseURL = baseURL

	filePath := strings.TrimSpace(auth.Attributes[cliproxyauth.AttributePath])
	if filePath == "" {
		filePath = strings.TrimSpace(auth.FileName)
	}
	if filePath != "" {
		if errSave := storage.SaveTokenToFile(filePath); errSave != nil {
			log.Warnf("meta executor: failed to persist refreshed token to %s: %v", filePath, errSave)
		}
	}

	return auth, nil
}

func (e *MetaExecutor) ensureAuth(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{
			code: http.StatusUnauthorized,
			msg:  "meta executor: missing auth",
		}
	}

	_, token := metaCreds(auth)
	if token == "" {
		if dcaToken := extractDCAToken(auth); dcaToken != "" {
			refreshed, err := e.Refresh(ctx, auth)
			if err != nil {
				return nil, err
			}
			auth = refreshed
			_, token = metaCreds(auth)
		}
	}

	if token == "" {
		return nil, statusErr{
			code: http.StatusUnauthorized,
			msg:  "meta executor: missing API key or access token",
		}
	}

	return e.enrichAuth(auth), nil
}

func (e *MetaExecutor) enrichAuth(auth *cliproxyauth.Auth) *cliproxyauth.Auth {
	baseURL, token := metaCreds(auth)
	var cloned *cliproxyauth.Auth
	if auth != nil {
		cloned = auth.Clone()
	} else {
		cloned = &cliproxyauth.Auth{
			Provider: "meta",
		}
	}
	if cloned.Attributes == nil {
		cloned.Attributes = make(map[string]string)
	}
	if strings.TrimSpace(cloned.Attributes["base_url"]) == "" {
		cloned.Attributes["base_url"] = baseURL
	}
	if strings.TrimSpace(cloned.Attributes["api_key"]) == "" {
		cloned.Attributes["api_key"] = token
	}
	if _, hasHeader := cloned.Attributes["header:User-Agent"]; !hasHeader {
		cloned.Attributes["header:User-Agent"] = "muse-code/1.0.2"
	}
	return cloned
}

func extractDCAToken(a *cliproxyauth.Auth) string {
	if a == nil {
		return ""
	}
	if a.Attributes != nil {
		if d := strings.TrimSpace(a.Attributes["dca_token"]); d != "" {
			return d
		}
		if t := strings.TrimSpace(a.Attributes["access_token"]); strings.HasPrefix(t, "dca:") {
			return t
		}
		if k := strings.TrimSpace(a.Attributes["api_key"]); strings.HasPrefix(k, "dca:") {
			return k
		}
	}
	if a.Metadata != nil {
		if d, ok := a.Metadata["dca_token"].(string); ok && strings.TrimSpace(d) != "" {
			return strings.TrimSpace(d)
		}
		if t, ok := a.Metadata["access_token"].(string); ok && strings.HasPrefix(strings.TrimSpace(t), "dca:") {
			return strings.TrimSpace(t)
		}
		if k, ok := a.Metadata["api_key"].(string); ok && strings.HasPrefix(strings.TrimSpace(k), "dca:") {
			return strings.TrimSpace(k)
		}
	}
	if a.Storage != nil {
		if ms, ok := a.Storage.(*metaauth.MetaTokenStorage); ok && ms != nil {
			if ms.DCAToken != "" {
				return ms.DCAToken
			}
			if strings.HasPrefix(ms.AccessToken, "dca:") {
				return ms.AccessToken
			}
		}
	}
	return ""
}

func metaCreds(a *cliproxyauth.Auth) (baseURL, token string) {
	baseURL = metaauth.DefaultAPIBaseURL
	if a == nil {
		return baseURL, ""
	}

	if a.Attributes != nil {
		if b := strings.TrimSpace(a.Attributes["base_url"]); b != "" {
			baseURL = b
		}
		if k := strings.TrimSpace(a.Attributes["api_key"]); k != "" && !strings.HasPrefix(k, "dca:") {
			token = k
		} else if t := strings.TrimSpace(a.Attributes["access_token"]); t != "" && !strings.HasPrefix(t, "dca:") {
			token = t
		}
	}
	if a.Metadata != nil {
		if baseURL == metaauth.DefaultAPIBaseURL {
			if b, ok := a.Metadata["base_url"].(string); ok && strings.TrimSpace(b) != "" {
				baseURL = strings.TrimSpace(b)
			} else if b, ok := a.Metadata["api_base_url"].(string); ok && strings.TrimSpace(b) != "" {
				baseURL = strings.TrimSpace(b)
			}
		}
		if token == "" {
			if k, ok := a.Metadata["api_key"].(string); ok && strings.TrimSpace(k) != "" && !strings.HasPrefix(strings.TrimSpace(k), "dca:") {
				token = strings.TrimSpace(k)
			} else if t, ok := a.Metadata["access_token"].(string); ok && strings.TrimSpace(t) != "" && !strings.HasPrefix(strings.TrimSpace(t), "dca:") {
				token = strings.TrimSpace(t)
			}
		}
	}
	if token == "" && a.Storage != nil {
		if ms, ok := a.Storage.(*metaauth.MetaTokenStorage); ok && ms != nil {
			if ms.APIKey != "" {
				token = ms.APIKey
			} else if ms.AccessToken != "" && !strings.HasPrefix(ms.AccessToken, "dca:") {
				token = ms.AccessToken
			}
			if ms.BaseURL != "" && baseURL == metaauth.DefaultAPIBaseURL {
				baseURL = ms.BaseURL
			}
		}
	}
	return baseURL, token
}

func parseMetaRetryAfter(statusCode int, errorBody []byte, now time.Time) *time.Duration {
	if statusCode != http.StatusTooManyRequests || len(errorBody) == 0 {
		return nil
	}
	if resetsAt := gjson.GetBytes(errorBody, "error.resets_at").Int(); resetsAt > 0 {
		resetAtTime := time.Unix(resetsAt, 0)
		if resetAtTime.After(now) {
			retryAfter := resetAtTime.Sub(now)
			return &retryAfter
		}
	}
	return nil
}
