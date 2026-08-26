package cliproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const (
	xaiModelsMaxResponseBytes   = int64(4 << 20)
	xaiCLIModelsTokenAuthHeader = "X-XAI-Token-Auth"
	xaiCLIModelsTokenAuthValue  = "xai-grok-cli"
	xaiCLIModelsVersionHeader   = "x-grok-client-version"
	xaiCLIModelsVersionValue    = "0.2.93"
)

type xaiModelsResponse struct {
	Data   *[]xaiRemoteModel `json:"data"`
	Models *[]xaiRemoteModel `json:"models"`
}

type xaiRemoteModel struct {
	ID                  string            `json:"id"`
	Model               string            `json:"model"`
	Object              string            `json:"object"`
	Created             int64             `json:"created"`
	OwnedBy             string            `json:"owned_by"`
	Name                string            `json:"name"`
	Description         string            `json:"description"`
	ContextWindow       int               `json:"context_window"`
	ContextLength       int               `json:"context_length"`
	MaxCompletionTokens int               `json:"max_completion_tokens"`
	SupportsReasoning   *bool             `json:"supports_reasoning_effort"`
	ReasoningEffort     string            `json:"reasoning_effort"`
	ReasoningEfforts    []xaiRemoteEffort `json:"reasoning_efforts"`
}

type xaiRemoteEffort struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

func (s *Service) xaiModelsForAuth(ctx context.Context, auth *coreauth.Auth) ([]*registry.ModelInfo, error) {
	if auth == nil || auth.ID == "" {
		return nil, fmt.Errorf("xai models: auth is required")
	}
	token := xaiAuthString(auth, "access_token", "api_key")
	if token == "" {
		return nil, fmt.Errorf("xai models: access token is missing")
	}
	baseURL, cli := resolveXAIModelsRoute(s, auth)
	models, errFetch := fetchXAIModels(ctx, s, auth, token, baseURL, cli)
	if errFetch != nil {
		if ctx.Err() != nil {
			return nil, errFetch
		}
		log.Warnf("xai models: discovery failed for auth %s: %v", auth.ID, errFetch)
		GlobalModelRegistry().UnregisterClient(auth.ID)
	}
	return models, errFetch
}

func fetchXAIModels(ctx context.Context, s *Service, auth *coreauth.Auth, token, baseURL string, cli bool) ([]*registry.ModelInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if errReq != nil {
		return nil, fmt.Errorf("xai models: create request: %w", errReq)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if cli {
		req.Header.Set(xaiCLIModelsTokenAuthHeader, xaiCLIModelsTokenAuthValue)
		req.Header.Set(xaiCLIModelsVersionHeader, xaiCLIModelsVersionValue)
	}
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
	}

	client := &http.Client{}
	proxyURL := ""
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && s != nil {
		s.cfgMu.RLock()
		if s.cfg != nil {
			proxyURL = strings.TrimSpace(s.cfg.ProxyURL)
		}
		s.cfgMu.RUnlock()
	}
	if proxyURL != "" {
		if transport, _, errProxy := proxyutil.BuildHTTPTransport(proxyURL); errProxy == nil && transport != nil {
			client.Transport = transport
		}
	}

	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("xai models: request failed: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("xai models: close response body: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, xaiModelsMaxResponseBytes+1))
	if errRead != nil {
		return nil, fmt.Errorf("xai models: read response: %w", errRead)
	}
	if int64(len(body)) > xaiModelsMaxResponseBytes {
		return nil, fmt.Errorf("xai models: response exceeds %d bytes", xaiModelsMaxResponseBytes)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("xai models: upstream returned status %d: %s", resp.StatusCode, summarizeXAIModelError(body))
	}

	var payload xaiModelsResponse
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return nil, fmt.Errorf("xai models: parse response: %w", errUnmarshal)
	}
	var remote []xaiRemoteModel
	switch {
	case payload.Data != nil:
		remote = *payload.Data
	case payload.Models != nil:
		remote = *payload.Models
	default:
		return nil, fmt.Errorf("xai models: response missing data/models field")
	}
	return mergeXAIModels(remote, registry.GetXAIModels()), nil
}

func resolveXAIModelsRoute(s *Service, auth *coreauth.Auth) (string, bool) {
	baseURL := xaiAuthString(auth, "base_url")
	if baseURL != "" && !strings.EqualFold(strings.TrimRight(baseURL, "/"), "https://api.x.ai/v1") && !strings.EqualFold(strings.TrimRight(baseURL, "/"), "https://cli-chat-proxy.grok.com/v1") {
		return baseURL, false
	}
	if xaiUsingAPIForModels(auth) {
		if baseURL == "" || strings.EqualFold(strings.TrimRight(baseURL, "/"), "https://cli-chat-proxy.grok.com/v1") {
			return "https://api.x.ai/v1", false
		}
		return baseURL, false
	}
	return "https://cli-chat-proxy.grok.com/v1", true
}

func xaiUsingAPIForModels(auth *coreauth.Auth) bool {
	if auth == nil {
		return true
	}
	if auth.Attributes != nil {
		if raw := strings.TrimSpace(auth.Attributes["using_api"]); raw != "" {
			if value, errParse := strconv.ParseBool(raw); errParse == nil {
				return value
			}
		}
	}
	if auth.Metadata != nil {
		switch value := auth.Metadata["using_api"].(type) {
		case bool:
			return value
		case string:
			if parsed, errParse := strconv.ParseBool(strings.TrimSpace(value)); errParse == nil {
				return parsed
			}
		}
	}
	authKind := strings.ToLower(strings.TrimSpace(auth.AuthKind()))
	return authKind != coreauth.AuthKindOAuth
}

func sameXAIModelRegistrationInputs(current, latest *coreauth.Auth) bool {
	if current == nil || latest == nil || current.ID == "" || current.ID != latest.ID {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(current.Provider), "xai") || current.AuthKind() != coreauth.AuthKindOAuth || latest.AuthKind() != coreauth.AuthKindOAuth {
		return false
	}
	if current.Prefix != latest.Prefix || current.Disabled != latest.Disabled || current.ProxyURL != latest.ProxyURL {
		return false
	}
	if !reflect.DeepEqual(current.Attributes, latest.Attributes) {
		return false
	}
	for _, key := range []string{"access_token", "api_key", "base_url"} {
		if xaiAuthString(current, key) != xaiAuthString(latest, key) {
			return false
		}
	}
	return xaiUsingAPIForModels(current) == xaiUsingAPIForModels(latest)
}

func xaiAuthString(auth *coreauth.Auth, keys ...string) string {
	if auth == nil {
		return ""
	}
	for _, key := range keys {
		if auth.Attributes != nil {
			if value := strings.TrimSpace(auth.Attributes[key]); value != "" {
				return value
			}
		}
		if auth.Metadata != nil {
			if value, ok := auth.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func mergeXAIModels(remote []xaiRemoteModel, catalog []*registry.ModelInfo) []*registry.ModelInfo {
	byID := make(map[string]*registry.ModelInfo, len(catalog))
	for _, model := range catalog {
		if model != nil {
			byID[strings.ToLower(strings.TrimSpace(model.ID))] = model
		}
	}
	result := make([]*registry.ModelInfo, 0, len(remote))
	seen := make(map[string]struct{}, len(remote))
	for _, item := range remote {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = strings.TrimSpace(item.Model)
		}
		key := strings.ToLower(id)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		model := &registry.ModelInfo{ID: id, Object: item.Object, Created: item.Created, OwnedBy: item.OwnedBy, Type: "xai", DisplayName: item.Name, Name: item.Name, Description: item.Description, ContextLength: item.ContextWindow, MaxCompletionTokens: item.MaxCompletionTokens}
		if model.Object == "" {
			model.Object = "model"
		}
		if model.OwnedBy == "" {
			model.OwnedBy = "xAI"
		}
		if model.DisplayName == "" {
			model.DisplayName = id
		}
		if model.Name == "" {
			model.Name = id
		}
		if model.ContextLength == 0 {
			model.ContextLength = item.ContextLength
		}
		if static := byID[key]; static != nil {
			model = mergeXAIModelMetadata(static, model)
		}
		levels := make([]string, 0, len(item.ReasoningEfforts))
		for _, effort := range item.ReasoningEfforts {
			value := strings.TrimSpace(effort.Value)
			if value == "" {
				value = strings.TrimSpace(effort.ID)
			}
			if value != "" {
				levels = append(levels, value)
			}
		}
		if len(levels) == 0 && strings.TrimSpace(item.ReasoningEffort) != "" {
			levels = []string{strings.TrimSpace(item.ReasoningEffort)}
		}
		if len(levels) > 0 {
			model.Thinking = &registry.ThinkingSupport{Levels: levels}
		}
		if item.SupportsReasoning != nil && !*item.SupportsReasoning {
			model.Thinking = nil
		}
		result = append(result, model)
	}
	return result
}

func mergeXAIModelMetadata(static, remote *registry.ModelInfo) *registry.ModelInfo {
	merged := *static
	if static.Thinking != nil {
		thinking := *static.Thinking
		thinking.Levels = append([]string(nil), static.Thinking.Levels...)
		merged.Thinking = &thinking
	}
	if remote.ID != "" {
		merged.ID = remote.ID
	}
	if remote.Object != "" {
		merged.Object = remote.Object
	}
	if remote.Created != 0 {
		merged.Created = remote.Created
	}
	if remote.OwnedBy != "" {
		merged.OwnedBy = remote.OwnedBy
	}
	if remote.Type != "" {
		merged.Type = remote.Type
	}
	if remote.DisplayName != "" {
		merged.DisplayName = remote.DisplayName
	}
	if remote.Name != "" {
		merged.Name = remote.Name
	}
	if remote.Description != "" {
		merged.Description = remote.Description
	}
	if remote.ContextLength != 0 {
		merged.ContextLength = remote.ContextLength
	}
	if remote.MaxCompletionTokens != 0 {
		merged.MaxCompletionTokens = remote.MaxCompletionTokens
	}
	return &merged
}

func summarizeXAIModelError(body []byte) string {
	const maxErrorBytes = 512
	body = []byte(strings.TrimSpace(string(body)))
	if len(body) > maxErrorBytes {
		body = body[:maxErrorBytes]
	}
	return string(body)
}
