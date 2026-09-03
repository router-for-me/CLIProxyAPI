package executor

import (
	"context"
	"fmt"
	"net/http"
	"os"
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
)

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
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute executes a non-streaming completion against the Meta API.
func (e *MetaExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	enriched := e.enrichAuth(auth)
	if _, token := metaCreds(enriched); token == "" {
		return cliproxyexecutor.Response{}, statusErr{
			code: http.StatusUnauthorized,
			msg:  "meta executor: missing API key or access token (set META_API_KEY, login via -meta-login, or configure ~/.config/muse/auth.json)",
		}
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
	enriched := e.enrichAuth(auth)
	if _, token := metaCreds(enriched); token == "" {
		return nil, statusErr{
			code: http.StatusUnauthorized,
			msg:  "meta executor: missing API key or access token (set META_API_KEY, login via -meta-login, or configure ~/.config/muse/auth.json)",
		}
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
	return e.compat.CountTokens(ctx, e.enrichAuth(auth), req, opts)
}

// Refresh checks for updated credentials in home or local CLI storage.
func (e *MetaExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("meta executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, statusErr{code: http.StatusInternalServerError, msg: "meta executor: auth is nil"}
	}
	token, baseURL, email, ok := metaauth.ReadLocalMuseCLIAuth()
	if ok && token != "" {
		if auth.Metadata == nil {
			auth.Metadata = make(map[string]any)
		}
		auth.Metadata["access_token"] = token
		auth.Metadata["base_url"] = baseURL
		if email != "" {
			auth.Metadata["email"] = email
		}
		if auth.Attributes == nil {
			auth.Attributes = make(map[string]string)
		}
		auth.Attributes["api_key"] = token
		auth.Attributes["base_url"] = baseURL
	}
	return auth, nil
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

func metaCreds(a *cliproxyauth.Auth) (baseURL, token string) {
	baseURL = metaauth.DefaultAPIBaseURL
	var dcaToken string

	if a != nil {
		if a.Attributes != nil {
			if b := strings.TrimSpace(a.Attributes["base_url"]); b != "" {
				baseURL = b
			}
			if k := strings.TrimSpace(a.Attributes["api_key"]); k != "" {
				token = k
			} else if t := strings.TrimSpace(a.Attributes["access_token"]); t != "" {
				if strings.HasPrefix(t, "dca:") {
					dcaToken = t
				} else {
					token = t
				}
			}
		}
		if token == "" && a.Metadata != nil {
			if b, ok := a.Metadata["base_url"].(string); ok && strings.TrimSpace(b) != "" {
				baseURL = strings.TrimSpace(b)
			} else if b, ok := a.Metadata["api_base_url"].(string); ok && strings.TrimSpace(b) != "" {
				baseURL = strings.TrimSpace(b)
			}
			if k, ok := a.Metadata["api_key"].(string); ok && strings.TrimSpace(k) != "" {
				token = strings.TrimSpace(k)
			}
			if t, ok := a.Metadata["dca_token"].(string); ok && strings.TrimSpace(t) != "" {
				dcaToken = strings.TrimSpace(t)
			}
			if token == "" {
				if t, ok := a.Metadata["access_token"].(string); ok && strings.TrimSpace(t) != "" {
					trimmed := strings.TrimSpace(t)
					if strings.HasPrefix(trimmed, "dca:") {
						dcaToken = trimmed
					} else {
						token = trimmed
					}
				}
			}
		}
	}
	if token == "" {
		if envKey := strings.TrimSpace(os.Getenv("META_API_KEY")); envKey != "" {
			token = envKey
		}
	}
	if token == "" {
		if localToken, localBase, _, ok := metaauth.ReadLocalMuseCLIAuth(); ok && localToken != "" {
			token = localToken
			if localBase != "" && baseURL == metaauth.DefaultAPIBaseURL {
				baseURL = localBase
			}
		}
	}
	if token == "" && dcaToken != "" {
		authSvc := metaauth.NewMetaAuth(nil)
		if minted, err := authSvc.MintAPIKey(context.Background(), dcaToken); err == nil && minted != nil {
			token = minted.APIKey
			if a != nil {
				if a.Metadata != nil {
					a.Metadata["api_key"] = minted.APIKey
					if minted.UserEmail != "" {
						a.Metadata["email"] = minted.UserEmail
					}
				}
				if a.Attributes != nil {
					a.Attributes["api_key"] = minted.APIKey
				}
			}
		} else if err != nil {
			log.Warnf("meta executor: mint API key from dca_token failed: %v", err)
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
