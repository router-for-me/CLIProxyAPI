package executor

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func applyCodexPromptCacheHeaders(from sdktranslator.Format, req cliproxyexecutor.Request, rawJSON []byte) ([]byte, http.Header) {
	body, headers, _ := applyCodexPromptCacheHeadersWithContext(context.Background(), from, req, rawJSON)
	return body, headers
}

func applyCodexPromptCacheHeadersWithContext(ctx context.Context, from sdktranslator.Format, req cliproxyexecutor.Request, rawJSON []byte, headerSets ...http.Header) ([]byte, http.Header, error) {
	headers := http.Header{}
	if len(rawJSON) == 0 {
		return rawJSON, headers, nil
	}

	var requestHeaders http.Header
	if len(headerSets) > 0 {
		requestHeaders = headerSets[0]
	}
	var cache helps.CodexCache
	if sourceFormatEqual(from, sdktranslator.FormatClaude) {
		modelName := strings.TrimSpace(gjson.GetBytes(rawJSON, "model").String())
		if modelName == "" {
			modelName = thinking.ParseSuffix(req.Model).ModelName
		}
		cached, ok, errCache := helps.ClaudeCodePromptCache(ctx, modelName, req.Payload, requestHeaders)
		if errCache != nil {
			return nil, nil, errCache
		}
		if ok {
			cache = cached
		}
	} else if sourceFormatEqual(from, sdktranslator.FormatOpenAIResponse) {
		if promptCacheKey := gjson.GetBytes(req.Payload, "prompt_cache_key"); promptCacheKey.Exists() {
			cache.ID = promptCacheKey.String()
		}
	}
	if cache.ID == "" {
		cache.ID = helps.ProviderSessionUUID("codex", req.Metadata)
	}

	if cache.ID != "" {
		rawJSON = helps.SetStringIfDifferent(rawJSON, "prompt_cache_key", cache.ID)
		setHeaderCasePreserved(headers, "session_id", cache.ID)
		headers.Set("Conversation_id", cache.ID)
	}

	return rawJSON, headers, nil
}

func applyCodexWebsocketHeaders(ctx context.Context, headers http.Header, auth *cliproxyauth.Auth, token string, cfg *config.Config) http.Header {
	if headers == nil {
		headers = http.Header{}
	}
	if strings.TrimSpace(token) != "" {
		headers.Set("Authorization", "Bearer "+token)
	}

	var ginHeaders http.Header
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		ginHeaders = ginCtx.Request.Header.Clone()
	}

	isAPIKey := codexAuthUsesAPIKey(auth)
	cfgUserAgent, cfgBetaFeatures := codexHeaderDefaults(cfg, auth)
	ensureHeaderWithPriority(headers, ginHeaders, "x-codex-beta-features", cfgBetaFeatures, "")
	misc.EnsureHeader(headers, ginHeaders, "x-codex-turn-state", "")
	misc.EnsureHeader(headers, ginHeaders, "x-codex-turn-metadata", "")
	misc.EnsureHeader(headers, ginHeaders, "x-client-request-id", "")
	misc.EnsureHeader(headers, ginHeaders, "x-responsesapi-include-timing-metrics", "")
	misc.EnsureHeader(headers, ginHeaders, "Version", "")
	if isAPIKey {
		ensureHeaderWithPriority(headers, ginHeaders, "User-Agent", "", "")
	} else {
		ensureHeaderWithConfigPrecedence(headers, ginHeaders, "User-Agent", cfgUserAgent, codexUserAgent)
	}

	betaHeader := strings.TrimSpace(headers.Get("OpenAI-Beta"))
	if betaHeader == "" && ginHeaders != nil {
		betaHeader = strings.TrimSpace(ginHeaders.Get("OpenAI-Beta"))
	}
	if betaHeader == "" || !strings.Contains(betaHeader, "responses_websockets=") {
		betaHeader = codexResponsesWebsocketBetaHeaderValue
	}
	headers.Set("OpenAI-Beta", betaHeader)
	sessionFallback := ""
	if strings.Contains(headers.Get("User-Agent"), "Mac OS") {
		sessionFallback = uuid.NewString()
	}
	ensureCodexWebsocketSessionHeader(headers, ginHeaders, sessionFallback)
	if originator := strings.TrimSpace(ginHeaders.Get("Originator")); originator != "" {
		headers.Set("Originator", originator)
	} else if !isAPIKey {
		headers.Set("Originator", codexOriginator)
	}
	if !isAPIKey {
		if auth != nil && auth.Metadata != nil {
			if accountID, ok := auth.Metadata["account_id"].(string); ok {
				if trimmed := strings.TrimSpace(accountID); trimmed != "" {
					setHeaderCasePreserved(headers, "ChatGPT-Account-ID", trimmed)
				}
			}
		}
	}

	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(&http.Request{Header: headers}, attrs)
	applyCodexCloakingHeaders(headers, cfg)

	return headers
}

func ensureCodexWebsocketSessionHeader(target http.Header, source http.Header, fallbackValue string) {
	if target == nil {
		return
	}
	sessionID := codexSessionHeaderValue(target)
	if sessionID == "" {
		sessionID = codexSessionHeaderValue(source)
	}
	if sessionID == "" {
		sessionID = strings.TrimSpace(fallbackValue)
	}
	if sessionID != "" {
		setHeaderCasePreserved(target, "session_id", sessionID)
	}
	deleteHeaderCaseInsensitive(target, "Session-Id")
}

func codexSessionHeaderValue(headers http.Header) string {
	for _, key := range []string{"Session-Id", "Session_id", "session_id"} {
		if value := strings.TrimSpace(headerValueCaseInsensitive(headers, key)); value != "" {
			return value
		}
	}
	return ""
}

func codexAuthUsesAPIKey(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	return strings.TrimSpace(auth.Attributes["api_key"]) != ""
}

func ensureHeaderCasePreserved(target http.Header, source http.Header, key, configValue, fallbackValue string) {
	if target == nil {
		return
	}
	if strings.TrimSpace(headerValueCaseInsensitive(target, key)) != "" {
		return
	}
	if source != nil {
		if val := strings.TrimSpace(headerValueCaseInsensitive(source, key)); val != "" {
			setHeaderCasePreserved(target, key, val)
			return
		}
	}
	if val := strings.TrimSpace(configValue); val != "" {
		setHeaderCasePreserved(target, key, val)
		return
	}
	if val := strings.TrimSpace(fallbackValue); val != "" {
		setHeaderCasePreserved(target, key, val)
	}
}

func setHeaderCasePreserved(headers http.Header, key string, value string) {
	if headers == nil {
		return
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return
	}
	deleteHeaderCaseInsensitive(headers, key)
	headers[key] = []string{value}
}

func setCodexSessionHeaderCasePreserved(headers http.Header, fallbackKey string, value string) {
	if headers == nil {
		return
	}
	fallbackKey = strings.TrimSpace(fallbackKey)
	value = strings.TrimSpace(value)
	if fallbackKey == "" || value == "" {
		return
	}

	selectedKey := ""
	if _, ok := headers[fallbackKey]; ok && codexSessionHeaderKeyUsesUnderscore(fallbackKey) {
		selectedKey = fallbackKey
	} else {
		for existingKey := range headers {
			if codexSessionHeaderKeyUsesUnderscore(existingKey) {
				selectedKey = existingKey
				break
			}
		}
	}
	if selectedKey == "" {
		selectedKey = fallbackKey
	}
	for existingKey := range headers {
		if codexSessionHeaderKey(existingKey) && existingKey != selectedKey {
			delete(headers, existingKey)
		}
	}
	headers[selectedKey] = []string{value}
}

func codexSessionHeaderKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	return normalized == "session_id" || normalized == "session-id"
}

func codexSessionHeaderKeyUsesUnderscore(key string) bool {
	return strings.ToLower(strings.TrimSpace(key)) == "session_id"
}

func headerValueCaseInsensitive(headers http.Header, key string) string {
	key = strings.TrimSpace(key)
	if headers == nil || key == "" {
		return ""
	}
	if val := strings.TrimSpace(headers.Get(key)); val != "" {
		return val
	}
	for existingKey, values := range headers {
		if !strings.EqualFold(existingKey, key) {
			continue
		}
		for _, value := range values {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func deleteHeaderCaseInsensitive(headers http.Header, key string) {
	for existingKey := range headers {
		if strings.EqualFold(existingKey, key) {
			delete(headers, existingKey)
		}
	}
}

func codexHeaderDefaults(cfg *config.Config, auth *cliproxyauth.Auth) (string, string) {
	if cfg == nil || auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["api_key"]); v != "" {
			return "", ""
		}
	}
	return strings.TrimSpace(cfg.CodexHeaderDefaults.UserAgent), strings.TrimSpace(cfg.CodexHeaderDefaults.BetaFeatures)
}

func ensureHeaderWithPriority(target http.Header, source http.Header, key, configValue, fallbackValue string) {
	if target == nil {
		return
	}
	if strings.TrimSpace(target.Get(key)) != "" {
		return
	}
	if source != nil {
		if val := strings.TrimSpace(source.Get(key)); val != "" {
			target.Set(key, val)
			return
		}
	}
	if val := strings.TrimSpace(configValue); val != "" {
		target.Set(key, val)
		return
	}
	if val := strings.TrimSpace(fallbackValue); val != "" {
		target.Set(key, val)
	}
}

func ensureHeaderWithConfigPrecedence(target http.Header, source http.Header, key, configValue, fallbackValue string) {
	if target == nil {
		return
	}
	if strings.TrimSpace(target.Get(key)) != "" {
		return
	}
	if val := strings.TrimSpace(configValue); val != "" {
		target.Set(key, val)
		return
	}
	if source != nil {
		if val := strings.TrimSpace(source.Get(key)); val != "" {
			target.Set(key, val)
			return
		}
	}
	if val := strings.TrimSpace(fallbackValue); val != "" {
		target.Set(key, val)
	}
}
