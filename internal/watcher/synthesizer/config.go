package synthesizer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/cursorstorage"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/auth/diff"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// ConfigSynthesizer generates Auth entries from configuration API keys.
// It handles Gemini, Claude, Codex, OpenAI-compat, and Vertex-compat providers.
type ConfigSynthesizer struct{}

// NewConfigSynthesizer creates a new ConfigSynthesizer instance.
func NewConfigSynthesizer() *ConfigSynthesizer {
	return &ConfigSynthesizer{}
}

// synthesizeOAICompatFromDedicatedBlocks creates Auth entries from dedicated provider blocks
// (minimax, roo, kilo, deepseek, etc.) using a generic synthesizer path.
func (s *ConfigSynthesizer) synthesizeOAICompatFromDedicatedBlocks(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0)
	for _, p := range config.GetDedicatedProviders() {
		entries := s.getDedicatedProviderEntries(p, cfg)
		if len(entries) == 0 {
			continue
		}

		for i := range entries {
			entry := &entries[i]
			apiKey := s.resolveAPIKeyFromEntry(entry.TokenFile, entry.APIKey, i, p.Name)
			if apiKey == "" {
				continue
			}
			baseURL := strings.TrimSpace(entry.BaseURL)
			if baseURL == "" {
				baseURL = p.BaseURL
			}
			baseURL = strings.TrimSuffix(baseURL, "/")

			id, _ := idGen.Next(p.Name+":key", apiKey, baseURL)
			attrs := map[string]string{
				"source":   fmt.Sprintf("config:%s[%d]", p.Name, i),
				"base_url": baseURL,
				"api_key":  apiKey,
			}
			if entry.Priority != 0 {
				attrs["priority"] = strconv.Itoa(entry.Priority)
			}
			if hash := diff.ComputeOpenAICompatModelsHash(entry.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(entry.Headers, attrs)

			a := &coreauth.Auth{
				ID:         id,
				Provider:   p.Name,
				Label:      p.Name + "-key",
				Prefix:     entry.Prefix,
				Status:     coreauth.StatusActive,
				ProxyURL:   strings.TrimSpace(entry.ProxyURL),
				Attributes: attrs,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			ApplyAuthExcludedModelsMeta(a, cfg, entry.ExcludedModels, "key")
			out = append(out, a)
		}
	}
	return out
}

// Synthesize generates Auth entries from config API keys.
func (s *ConfigSynthesizer) Synthesize(ctx *SynthesisContext) ([]*coreauth.Auth, error) {
	out := make([]*coreauth.Auth, 0, 32)
	if ctx == nil || ctx.Config == nil {
		return out, nil
	}

	// Gemini API Keys
	out = append(out, s.synthesizeGeminiKeys(ctx)...)
	// Claude API Keys
	out = append(out, s.synthesizeClaudeKeys(ctx)...)
	// Codex API Keys
	out = append(out, s.synthesizeCodexKeys(ctx)...)
	// Kiro (AWS CodeWhisperer)
	out = append(out, s.synthesizeKiroKeys(ctx)...)
	// Cursor (via cursor-api)
	out = append(out, s.synthesizeCursorKeys(ctx)...)
	// Dedicated OpenAI-compatible blocks (minimax, roo, kilo, deepseek, groq, etc.)
	out = append(out, s.synthesizeOAICompatFromDedicatedBlocks(ctx)...)
	// Generic OpenAI-compat
	out = append(out, s.synthesizeOpenAICompat(ctx)...)
	// Vertex-compat
	out = append(out, s.synthesizeVertexCompat(ctx)...)

	return out, nil
}

// synthesizeGeminiKeys creates Auth entries for Gemini API keys.
func (s *ConfigSynthesizer) synthesizeGeminiKeys(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0, len(cfg.GeminiKey))
	for i := range cfg.GeminiKey {
		entry := cfg.GeminiKey[i]
		key := strings.TrimSpace(entry.APIKey)
		if key == "" {
			continue
		}
		prefix := strings.TrimSpace(entry.Prefix)
		base := strings.TrimSpace(entry.BaseURL)
		proxyURL := strings.TrimSpace(entry.ProxyURL)
		id, token := idGen.Next("gemini:apikey", key, base)
		attrs := map[string]string{
			"source":  fmt.Sprintf("config:gemini[%s]", token),
			"api_key": key,
		}
		if entry.Priority != 0 {
			attrs["priority"] = strconv.Itoa(entry.Priority)
		}
		if base != "" {
			attrs["base_url"] = base
		}
		if hash := diff.ComputeGeminiModelsHash(entry.Models); hash != "" {
			attrs["models_hash"] = hash
		}
		addConfigHeadersToAttrs(entry.Headers, attrs)
		a := &coreauth.Auth{
			ID:         id,
			Provider:   "gemini",
			Label:      "gemini-apikey",
			Prefix:     prefix,
			Status:     coreauth.StatusActive,
			ProxyURL:   proxyURL,
			Attributes: attrs,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		ApplyAuthExcludedModelsMeta(a, cfg, entry.ExcludedModels, "apikey")
		out = append(out, a)
	}
	return out
}

// synthesizeClaudeKeys creates Auth entries for Claude API keys.
func (s *ConfigSynthesizer) synthesizeClaudeKeys(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0, len(cfg.ClaudeKey))
	for i := range cfg.ClaudeKey {
		ck := cfg.ClaudeKey[i]
		key := strings.TrimSpace(ck.APIKey)
		if key == "" {
			continue
		}
		prefix := strings.TrimSpace(ck.Prefix)
		base := strings.TrimSpace(ck.BaseURL)
		id, token := idGen.Next("claude:apikey", key, base)
		attrs := map[string]string{
			"source":  fmt.Sprintf("config:claude[%s]", token),
			"api_key": key,
		}
		if ck.Priority != 0 {
			attrs["priority"] = strconv.Itoa(ck.Priority)
		}
		if base != "" {
			attrs["base_url"] = base
		}
		if hash := diff.ComputeClaudeModelsHash(ck.Models); hash != "" {
			attrs["models_hash"] = hash
		}
		addConfigHeadersToAttrs(ck.Headers, attrs)
		proxyURL := strings.TrimSpace(ck.ProxyURL)
		a := &coreauth.Auth{
			ID:         id,
			Provider:   "claude",
			Label:      "claude-apikey",
			Prefix:     prefix,
			Status:     coreauth.StatusActive,
			ProxyURL:   proxyURL,
			Attributes: attrs,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		ApplyAuthExcludedModelsMeta(a, cfg, ck.ExcludedModels, "apikey")
		out = append(out, a)
	}
	return out
}

// synthesizeCodexKeys creates Auth entries for Codex API keys.
func (s *ConfigSynthesizer) synthesizeCodexKeys(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0, len(cfg.CodexKey))
	for i := range cfg.CodexKey {
		ck := cfg.CodexKey[i]
		key := strings.TrimSpace(ck.APIKey)
		if key == "" {
			continue
		}
		prefix := strings.TrimSpace(ck.Prefix)
		id, token := idGen.Next("codex:apikey", key, ck.BaseURL)
		attrs := map[string]string{
			"source":  fmt.Sprintf("config:codex[%s]", token),
			"api_key": key,
		}
		if ck.Priority != 0 {
			attrs["priority"] = strconv.Itoa(ck.Priority)
		}
		if ck.BaseURL != "" {
			attrs["base_url"] = ck.BaseURL
		}
		if ck.Websockets {
			attrs["websockets"] = "true"
		}
		if hash := diff.ComputeCodexModelsHash(ck.Models); hash != "" {
			attrs["models_hash"] = hash
		}
		addConfigHeadersToAttrs(ck.Headers, attrs)
		proxyURL := strings.TrimSpace(ck.ProxyURL)
		a := &coreauth.Auth{
			ID:         id,
			Provider:   "codex",
			Label:      "codex-apikey",
			Prefix:     prefix,
			Status:     coreauth.StatusActive,
			ProxyURL:   proxyURL,
			Attributes: attrs,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		ApplyAuthExcludedModelsMeta(a, cfg, ck.ExcludedModels, "apikey")
		out = append(out, a)
	}
	return out
}

// synthesizeOpenAICompat creates Auth entries for OpenAI-compatible providers.
func (s *ConfigSynthesizer) synthesizeOpenAICompat(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0)
	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		prefix := strings.TrimSpace(compat.Prefix)
		providerName := strings.ToLower(strings.TrimSpace(compat.Name))
		if providerName == "" {
			providerName = "openai-compatibility"
		}
		base := strings.TrimSpace(compat.BaseURL)

		// Handle new APIKeyEntries format (preferred)
		createdEntries := 0
		for j := range compat.APIKeyEntries {
			entry := &compat.APIKeyEntries[j]
			apiKey := s.resolveAPIKeyFromEntry(entry.TokenFile, entry.APIKey, j, providerName)
			if apiKey == "" {
				continue
			}
			proxyURL := strings.TrimSpace(entry.ProxyURL)
			idKind := fmt.Sprintf("openai-compatibility:%s", providerName)
			id, token := idGen.Next(idKind, apiKey, base, proxyURL)
			attrs := map[string]string{
				"source":       fmt.Sprintf("config:%s[%s]", providerName, token),
				"base_url":     base,
				"compat_name":  compat.Name,
				"provider_key": providerName,
			}
			if compat.Priority != 0 {
				attrs["priority"] = strconv.Itoa(compat.Priority)
			}
			if apiKey != "" {
				attrs["api_key"] = apiKey
			}
			if hash := diff.ComputeOpenAICompatModelsHash(compat.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(compat.Headers, attrs)
			a := &coreauth.Auth{
				ID:         id,
				Provider:   providerName,
				Label:      compat.Name,
				Prefix:     prefix,
				Status:     coreauth.StatusActive,
				ProxyURL:   proxyURL,
				Attributes: attrs,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			out = append(out, a)
			createdEntries++
		}
		// Fallback: create entry without API key if no APIKeyEntries
		if createdEntries == 0 {
			idKind := fmt.Sprintf("openai-compatibility:%s", providerName)
			id, token := idGen.Next(idKind, base)
			attrs := map[string]string{
				"source":       fmt.Sprintf("config:%s[%s]", providerName, token),
				"base_url":     base,
				"compat_name":  compat.Name,
				"provider_key": providerName,
			}
			if compat.Priority != 0 {
				attrs["priority"] = strconv.Itoa(compat.Priority)
			}
			if hash := diff.ComputeOpenAICompatModelsHash(compat.Models); hash != "" {
				attrs["models_hash"] = hash
			}
			addConfigHeadersToAttrs(compat.Headers, attrs)
			a := &coreauth.Auth{
				ID:         id,
				Provider:   providerName,
				Label:      compat.Name,
				Prefix:     prefix,
				Status:     coreauth.StatusActive,
				Attributes: attrs,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			out = append(out, a)
		}
	}
	return out
}

// synthesizeVertexCompat creates Auth entries for Vertex-compatible providers.
func (s *ConfigSynthesizer) synthesizeVertexCompat(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	out := make([]*coreauth.Auth, 0, len(cfg.VertexCompatAPIKey))
	for i := range cfg.VertexCompatAPIKey {
		compat := &cfg.VertexCompatAPIKey[i]
		providerName := "vertex"
		base := strings.TrimSpace(compat.BaseURL)

		key := strings.TrimSpace(compat.APIKey)
		prefix := strings.TrimSpace(compat.Prefix)
		proxyURL := strings.TrimSpace(compat.ProxyURL)
		idKind := "vertex:apikey"
		id, token := idGen.Next(idKind, key, base, proxyURL)
		attrs := map[string]string{
			"source":       fmt.Sprintf("config:vertex-apikey[%s]", token),
			"base_url":     base,
			"provider_key": providerName,
		}
		if compat.Priority != 0 {
			attrs["priority"] = strconv.Itoa(compat.Priority)
		}
		if key != "" {
			attrs["api_key"] = key
		}
		if hash := diff.ComputeVertexCompatModelsHash(compat.Models); hash != "" {
			attrs["models_hash"] = hash
		}
		addConfigHeadersToAttrs(compat.Headers, attrs)
		a := &coreauth.Auth{
			ID:         id,
			Provider:   providerName,
			Label:      "vertex-apikey",
			Prefix:     prefix,
			Status:     coreauth.StatusActive,
			ProxyURL:   proxyURL,
			Attributes: attrs,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		ApplyAuthExcludedModelsMeta(a, cfg, nil, "apikey")
		out = append(out, a)
	}
	return out
}

// synthesizeCursorKeys creates Auth entries for Cursor (via cursor-api).
// Precedence: token-file > auto-detected IDE token (zero-action flow).
func (s *ConfigSynthesizer) synthesizeCursorKeys(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	if len(cfg.CursorKey) == 0 {
		return nil
	}

	out := make([]*coreauth.Auth, 0, len(cfg.CursorKey))
	for i := range cfg.CursorKey {
		ck := cfg.CursorKey[i]
		cursorAPIURL := strings.TrimSpace(ck.CursorAPIURL)
		if cursorAPIURL == "" {
			cursorAPIURL = "http://127.0.0.1:3000"
		}
		baseURL := strings.TrimSuffix(cursorAPIURL, "/") + "/v1"

		var apiKey, source string
		if ck.TokenFile != "" {
			// token-file path: read sk-... from file (current behavior)
			tokenPath := ck.TokenFile
			if strings.HasPrefix(tokenPath, "~") {
				home, err := os.UserHomeDir()
				if err != nil {
					log.Warnf("cursor config[%d] failed to expand ~: %v", i, err)
					continue
				}
				tokenPath = filepath.Join(home, tokenPath[1:])
			}
			data, err := os.ReadFile(tokenPath)
			if err != nil {
				log.Warnf("cursor config[%d] failed to read token file %s: %v", i, ck.TokenFile, err)
				continue
			}
			apiKey = strings.TrimSpace(string(data))
			if apiKey == "" || !strings.HasPrefix(apiKey, "sk-") {
				log.Warnf("cursor config[%d] token file must contain sk-... key from cursor-api /build-key", i)
				continue
			}
			source = fmt.Sprintf("config:cursor[%s]", ck.TokenFile)
		} else {
			// zero-action: read from Cursor IDE storage, POST /tokens/add, use auth-token for chat
			ideToken, err := cursorstorage.ReadAccessToken()
			if err != nil {
				log.Warnf("cursor config[%d] %v", i, err)
				continue
			}
			if ideToken == "" {
				log.Warnf("cursor config[%d] Cursor IDE not found or not logged in; ensure Cursor IDE is installed and you are logged in", i)
				continue
			}
			authToken := strings.TrimSpace(ck.AuthToken)
			if authToken == "" {
				log.Warnf("cursor config[%d] cursor-api auth required: set auth-token to match cursor-api AUTH_TOKEN (required for zero-action flow)", i)
				continue
			}
			if err := s.cursorAddToken(cursorAPIURL, authToken, ideToken); err != nil {
				log.Warnf("cursor config[%d] failed to add token to cursor-api: %v", i, err)
				continue
			}
			apiKey = authToken
			source = "config:cursor[ide-zero-action]"
		}

		id, _ := idGen.Next("cursor:token", apiKey, baseURL)
		attrs := map[string]string{
			"source":   source,
			"base_url": baseURL,
			"api_key":  apiKey,
		}
		proxyURL := strings.TrimSpace(ck.ProxyURL)
		a := &coreauth.Auth{
			ID:         id,
			Provider:   "cursor",
			Label:      "cursor-token",
			Status:     coreauth.StatusActive,
			ProxyURL:   proxyURL,
			Attributes: attrs,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		out = append(out, a)
	}
	return out
}

// cursorAddToken POSTs the IDE access token to cursor-api /tokens/add.
func (s *ConfigSynthesizer) cursorAddToken(baseURL, authToken, ideToken string) error {
	url := strings.TrimSuffix(baseURL, "/") + "/tokens/add"
	body := map[string]any{
		"tokens":  []map[string]string{{"token": ideToken}},
		"enabled": true,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("cursor-api auth required: set auth-token to match cursor-api AUTH_TOKEN")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("tokens/add returned %d", resp.StatusCode)
	}
	return nil
}

func (s *ConfigSynthesizer) resolveAPIKeyFromEntry(tokenFile, apiKey string, _ int, _ string) string {
	if apiKey != "" {
		return strings.TrimSpace(apiKey)
	}
	if tokenFile == "" {
		return ""
	}
	tokenPath := tokenFile
	if strings.HasPrefix(tokenPath, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		tokenPath = filepath.Join(home, tokenPath[1:])
	}
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return ""
	}
	var parsed struct {
		AccessToken string `json:"access_token"`
		APIKey      string `json:"api_key"`
	}
	if err := json.Unmarshal(data, &parsed); err == nil {
		if v := strings.TrimSpace(parsed.AccessToken); v != "" {
			return v
		}
		if v := strings.TrimSpace(parsed.APIKey); v != "" {
			return v
		}
	}
	return strings.TrimSpace(string(data))
}

// synthesizeKiroKeys creates Auth entries for Kiro (AWS CodeWhisperer) tokens.
func (s *ConfigSynthesizer) synthesizeKiroKeys(ctx *SynthesisContext) []*coreauth.Auth {
	cfg := ctx.Config
	now := ctx.Now
	idGen := ctx.IDGenerator

	if len(cfg.KiroKey) == 0 {
		return nil
	}

	out := make([]*coreauth.Auth, 0, len(cfg.KiroKey))
	kAuth := kiroauth.NewKiroAuth(cfg)

	for i := range cfg.KiroKey {
		kk := cfg.KiroKey[i]
		var accessToken, profileArn, refreshToken string

		// Try to load from token file first
		if kk.TokenFile != "" && kAuth != nil {
			tokenData, err := kAuth.LoadTokenFromFile(kk.TokenFile)
			if err != nil {
				log.Warnf("failed to load kiro token file %s: %v", kk.TokenFile, err)
			} else {
				accessToken = tokenData.AccessToken
				profileArn = tokenData.ProfileArn
				refreshToken = tokenData.RefreshToken
			}
		}

		// Override with direct config values if provided
		if kk.AccessToken != "" {
			accessToken = kk.AccessToken
		}
		if kk.ProfileArn != "" {
			profileArn = kk.ProfileArn
		}
		if kk.RefreshToken != "" {
			refreshToken = kk.RefreshToken
		}

		if accessToken == "" {
			log.Warnf("kiro config[%d] missing access_token, skipping", i)
			continue
		}

		// profileArn is optional for AWS Builder ID users
		id, token := idGen.Next("kiro:token", accessToken, profileArn)
		attrs := map[string]string{
			"source":       fmt.Sprintf("config:kiro[%s]", token),
			"access_token": accessToken,
		}
		if profileArn != "" {
			attrs["profile_arn"] = profileArn
		}
		if kk.Region != "" {
			attrs["region"] = kk.Region
		}
		if kk.AgentTaskType != "" {
			attrs["agent_task_type"] = kk.AgentTaskType
		}
		if kk.PreferredEndpoint != "" {
			attrs["preferred_endpoint"] = kk.PreferredEndpoint
		} else if cfg.KiroPreferredEndpoint != "" {
			// Apply global default if not overridden by specific key
			attrs["preferred_endpoint"] = cfg.KiroPreferredEndpoint
		}
		if refreshToken != "" {
			attrs["refresh_token"] = refreshToken
		}
		proxyURL := strings.TrimSpace(kk.ProxyURL)
		a := &coreauth.Auth{
			ID:         id,
			Provider:   "kiro",
			Label:      "kiro-token",
			Status:     coreauth.StatusActive,
			ProxyURL:   proxyURL,
			Attributes: attrs,
			CreatedAt:  now,
			UpdatedAt:  now,
		}

		if refreshToken != "" {
			if a.Metadata == nil {
				a.Metadata = make(map[string]any)
			}
			a.Metadata["refresh_token"] = refreshToken
		}

		out = append(out, a)
	}
	return out
}
