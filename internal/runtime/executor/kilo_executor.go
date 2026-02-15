package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// KiloExecutor handles requests to Kilo API.
type KiloExecutor struct {
	cfg *config.Config
}

// NewKiloExecutor creates a new Kilo executor instance.
func NewKiloExecutor(cfg *config.Config) *KiloExecutor {
	return &KiloExecutor{cfg: cfg}
}

// Identifier returns the unique identifier for this executor.
func (e *KiloExecutor) Identifier() string { return "kilo" }

// PrepareRequest prepares the HTTP request before execution.
func (e *KiloExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	accessToken, _ := kiloCredentials(auth)
	if strings.TrimSpace(accessToken) == "" {
		return fmt.Errorf("kilo: missing access token")
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest executes a raw HTTP request.
func (e *KiloExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("kilo executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute performs a non-streaming request.
func (e *KiloExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("kilo: execution not fully implemented yet")
}

// ExecuteStream performs a streaming request.
func (e *KiloExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	return nil, fmt.Errorf("kilo: streaming execution not fully implemented yet")
}

// Refresh validates the Kilo token.
func (e *KiloExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("missing auth")
	}
	return auth, nil
}

// CountTokens returns the token count for the given request.
func (e *KiloExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("kilo: count tokens not supported")
}

// kiloCredentials extracts access token and other info from auth.
func kiloCredentials(auth *cliproxyauth.Auth) (accessToken, orgID string) {
	if auth == nil {
		return "", ""
	}
	if auth.Metadata != nil {
		if token, ok := auth.Metadata["access_token"].(string); ok {
			accessToken = token
		}
		if org, ok := auth.Metadata["organization_id"].(string); ok {
			orgID = org
		}
	}
	if accessToken == "" && auth.Attributes != nil {
		accessToken = auth.Attributes["access_token"]
		orgID = auth.Attributes["organization_id"]
	}
	return accessToken, orgID
}

// FetchKiloModels fetches models from Kilo API.
func FetchKiloModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	accessToken, orgID := kiloCredentials(auth)
	if accessToken == "" {
		log.Infof("kilo: no access token found, skipping dynamic model fetch (using static kilo-auto)")
		return registry.GetKiloModels()
	}

	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.kilo.ai/api/openrouter/models", nil)
	if err != nil {
		log.Warnf("kilo: failed to create model fetch request: %v", err)
		return registry.GetKiloModels()
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	if orgID != "" {
		req.Header.Set("X-Kilocode-OrganizationID", orgID)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Warnf("kilo: fetch models canceled: %v", err)
		} else {
			log.Warnf("kilo: using static models (API fetch failed: %v)", err)
		}
		return registry.GetKiloModels()
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Warnf("kilo: failed to read models response: %v", err)
		return registry.GetKiloModels()
	}

	if resp.StatusCode != http.StatusOK {
		log.Warnf("kilo: fetch models failed: status %d, body: %s", resp.StatusCode, string(body))
		return registry.GetKiloModels()
	}

	result := gjson.GetBytes(body, "data")
	if !result.Exists() {
		// Try root if data field is missing
		result = gjson.ParseBytes(body)
		if !result.IsArray() {
			log.Debugf("kilo: response body: %s", string(body))
			log.Warn("kilo: invalid API response format (expected array or data field with array)")
			return registry.GetKiloModels()
		}
	}

	var dynamicModels []*registry.ModelInfo
	now := time.Now().Unix()
	count := 0
	totalCount := 0

	result.ForEach(func(key, value gjson.Result) bool {
		totalCount++
		pIdxResult := value.Get("preferredIndex")
		preferredIndex := pIdxResult.Int()

		// Filter models where preferredIndex > 0 (Kilo-curated models)
		if preferredIndex <= 0 {
			return true
		}

		dynamicModels = append(dynamicModels, &registry.ModelInfo{
			ID:            value.Get("id").String(),
			DisplayName:   value.Get("name").String(),
			ContextLength: int(value.Get("context_length").Int()),
			OwnedBy:       "kilo",
			Type:          "kilo",
			Object:        "model",
			Created:       now,
		})
		count++
		return true
	})

	log.Infof("kilo: fetched %d models from API, %d curated (preferredIndex > 0)", totalCount, count)
	if count == 0 && totalCount > 0 {
		log.Warn("kilo: no curated models found (all preferredIndex <= 0). Check API response.")
	}

	staticModels := registry.GetKiloModels()
	// Always include kilo-auto (first static model)
	allModels := append(staticModels[:1], dynamicModels...)

	return allModels
}

