package cliproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const (
	// qwenDefaultModelBaseURL is the default DashScope OpenAI-compatible endpoint
	// used for model discovery when an auth has no explicit base_url.
	qwenDefaultModelBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
)

// qwenModelsResponse is the OpenAI-compatible /models listing returned by DashScope.
type qwenModelsResponse struct {
	Data []struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	} `json:"data"`
}

// fetchQwenModelsForAuth discovers available models from a Qwen/DashScope endpoint
// using the OpenAI-compatible GET /models API. Returns nil when the fetch fails or
// yields no models so callers can fall back to the static catalog.
func (s *Service) fetchQwenModelsForAuth(ctx context.Context, auth *coreauth.Auth) []*ModelInfo {
	if auth == nil {
		return nil
	}
	apiKey := ""
	baseURL := ""
	if auth.Attributes != nil {
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
	}
	if apiKey == "" {
		return nil
	}
	if baseURL == "" {
		baseURL = qwenDefaultModelBaseURL
	}

	// Model discovery uses the caller's context without an added timeout, matching
	// normal upstream execution (repo policy permits timeouts only for credential
	// acquisition). A slow /models endpoint must not silently drop to the static
	// catalog after a fixed deadline.
	client := &http.Client{}
	if transport, _, errProxy := proxyutil.BuildHTTPTransport(qwenModelFetchProxyURL(s, auth)); errProxy == nil && transport != nil {
		client.Transport = transport
	}

	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if errReq != nil {
		log.Debugf("qwen model fetch: build request: %v", errReq)
		return nil
	}
	req.Close = true
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cli-proxy-qwen")

	resp, errDo := client.Do(req)
	if errDo != nil {
		log.Debugf("qwen model fetch: request failed: %v", errDo)
		return nil
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("qwen model fetch: close response body: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		log.Debugf("qwen model fetch: read body: %v", errRead)
		return nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		log.Debugf("qwen model fetch: unexpected status %d", resp.StatusCode)
		return nil
	}

	return parseQwenModelsResponse(body)
}

func qwenModelFetchProxyURL(s *Service, auth *coreauth.Auth) string {
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if s != nil && s.cfg != nil {
		return strings.TrimSpace(s.cfg.ProxyURL)
	}
	return ""
}

// parseQwenModelsResponse converts a DashScope /models listing into ModelInfo entries.
// Non-text models (image/audio/video generation) are skipped since the Qwen executor
// only routes chat completions.
func parseQwenModelsResponse(body []byte) []*ModelInfo {
	var parsed qwenModelsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		log.Debugf("qwen model fetch: parse response: %v", err)
		return nil
	}
	if len(parsed.Data) == 0 {
		return nil
	}

	models := make([]*ModelInfo, 0, len(parsed.Data))
	seen := make(map[string]struct{}, len(parsed.Data))
	for _, item := range parsed.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if isQwenNonTextModel(id) {
			continue
		}
		key := strings.ToLower(id)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}

		ownedBy := strings.TrimSpace(item.OwnedBy)
		if ownedBy == "" || ownedBy == "system" {
			ownedBy = "alibaba"
		}
		info := &ModelInfo{
			ID:          id,
			Object:      "model",
			Created:     item.Created,
			OwnedBy:     ownedBy,
			Type:        "qwen",
			DisplayName: id,
		}
		if qwenModelSupportsThinking(id) {
			info.Thinking = &registry.ThinkingSupport{
				ZeroAllowed: true,
				Levels:      []string{"low", "medium", "high"},
			}
		}
		models = append(models, info)
	}
	if len(models) == 0 {
		return nil
	}
	return models
}

// isQwenNonTextModel reports whether a model ID is a non-text generation model
// (image/video/audio) that should not be routed through chat completions.
func isQwenNonTextModel(modelID string) bool {
	lower := strings.ToLower(modelID)
	nonTextPrefixes := []string{
		"qwen-image",
		"wan2.",
		"wan-",
		"qwen-audio",
		"qwen-tts",
		"qwen-video",
	}
	for _, prefix := range nonTextPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// qwenModelSupportsThinking applies a heuristic for hybrid-thinking Qwen text models.
// Qwen3.x and newer chat models support enable_thinking; legacy/turbo models do not,
// so turbo is deliberately excluded to match the static catalog (qwen-turbo has no
// thinking support) and avoid advertising reasoning the upstream will reject.
func qwenModelSupportsThinking(modelID string) bool {
	lower := strings.ToLower(modelID)
	if strings.HasPrefix(lower, "qwen3") {
		return true
	}
	thinkingFamilies := []string{"-max", "-plus", "-flash", "-coder"}
	for _, family := range thinkingFamilies {
		if strings.Contains(lower, family) && strings.HasPrefix(lower, "qwen") {
			return true
		}
	}
	return false
}
