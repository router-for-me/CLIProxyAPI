package executor

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const openAIModelsFetchTimeout = 10 * time.Second

// FetchOpenAIModels retrieves available models from an OpenAI-compatible /v1/models endpoint.
// Returns nil on any failure; callers should fall back to static model lists.
func FetchOpenAIModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config, provider string) []*registry.ModelInfo {
	if auth == nil || auth.Attributes == nil {
		return nil
	}
	baseURL := strings.TrimSpace(auth.Attributes["base_url"])
	apiKey := strings.TrimSpace(auth.Attributes["api_key"])
	if baseURL == "" || apiKey == "" {
		return nil
	}
	modelsURL := resolveOpenAIModelsURL(baseURL, auth.Attributes)

	reqCtx, cancel := context.WithTimeout(ctx, openAIModelsFetchTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodGet, modelsURL, nil)
	if err != nil {
		log.Debugf("%s: failed to create models request: %v", provider, err)
		return nil
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	client := newProxyAwareHTTPClient(reqCtx, cfg, auth, openAIModelsFetchTimeout)
	resp, err := client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		log.Debugf("%s: models request failed: %v", provider, err)
		return nil
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		log.Debugf("%s: models request returned %d", provider, resp.StatusCode)
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Debugf("%s: failed to read models response: %v", provider, err)
		return nil
	}

	data := gjson.GetBytes(body, "data")
	if !data.Exists() || !data.IsArray() {
		return nil
	}

	now := time.Now().Unix()
	providerType := strings.ToLower(strings.TrimSpace(provider))
	if providerType == "" {
		providerType = "openai"
	}

	models := make([]*registry.ModelInfo, 0, len(data.Array()))
	data.ForEach(func(_, v gjson.Result) bool {
		id := strings.TrimSpace(v.Get("id").String())
		if id == "" {
			return true
		}
		created := v.Get("created").Int()
		if created == 0 {
			created = now
		}
		ownedBy := strings.TrimSpace(v.Get("owned_by").String())
		if ownedBy == "" {
			ownedBy = providerType
		}
		models = append(models, &registry.ModelInfo{
			ID:          id,
			Object:      "model",
			Created:     created,
			OwnedBy:     ownedBy,
			Type:        providerType,
			DisplayName: id,
		})
		return true
	})

	if len(models) == 0 {
		return nil
	}
	return models
}

func resolveOpenAIModelsURL(baseURL string, attrs map[string]string) string {
	if attrs != nil {
		if modelsURL := strings.TrimSpace(attrs["models_url"]); modelsURL != "" {
			return modelsURL
		}
	}

	trimmedBaseURL := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmedBaseURL == "" {
		return ""
	}

	parsed, err := url.Parse(trimmedBaseURL)
	if err != nil {
		return trimmedBaseURL + "/v1/models"
	}
	if parsed.Path == "" || parsed.Path == "/" {
		return trimmedBaseURL + "/v1/models"
	}

	segment := path.Base(parsed.Path)
	if isVersionSegment(segment) {
		return trimmedBaseURL + "/models"
	}

	return trimmedBaseURL + "/v1/models"
}

func isVersionSegment(segment string) bool {
	if len(segment) < 2 || segment[0] != 'v' {
		return false
	}
	for i := 1; i < len(segment); i++ {
		if segment[i] < '0' || segment[i] > '9' {
			return false
		}
	}
	return true
}
