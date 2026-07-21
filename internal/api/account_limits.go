package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/accountlimits"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type accountLimitsCredential struct {
	provider          string
	accountID         string
	upstreamAccountID string
	token             string
	baseURL           string
	proxyURL          string
}

func (s *Server) accountLimitsHandler(c *gin.Context) {
	credential, status, errMessage := s.resolveAccountLimitsCredential(c.Query("provider"), c.Query("account_id"))
	if errMessage != "" {
		c.JSON(status, gin.H{"error": gin.H{"message": errMessage, "type": "invalid_request_error"}})
		return
	}

	switch credential.provider {
	case accountlimits.ProviderAnthropic:
		c.JSON(http.StatusOK, accountlimits.ProviderLimitsForAccount(credential.accountID))
	case accountlimits.ProviderOpenAI:
		payload, upstreamStatus, upstreamError := s.fetchOpenAIAccountLimits(c.Request.Context(), credential)
		if upstreamError != "" {
			c.JSON(upstreamStatus, gin.H{"error": gin.H{"message": upstreamError, "type": "upstream_error"}})
			return
		}
		c.JSON(http.StatusOK, payload)
	case accountlimits.ProviderZai:
		payload, upstreamStatus, upstreamError := s.fetchZaiAccountLimits(c.Request.Context(), credential)
		if upstreamError != "" {
			c.JSON(upstreamStatus, gin.H{"error": gin.H{"message": upstreamError, "type": "upstream_error"}})
			return
		}
		c.JSON(http.StatusOK, payload)
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": "unsupported account limits provider", "type": "internal_error"}})
	}
}

func (s *Server) resolveAccountLimitsCredential(provider, accountID string) (accountLimitsCredential, int, string) {
	provider = normalizeLimitsProvider(provider)
	accountID = strings.TrimSpace(accountID)
	candidates := s.accountLimitsCredentials()
	filtered := candidates[:0]
	for _, candidate := range candidates {
		if provider != "" && candidate.provider != provider {
			continue
		}
		if accountID != "" && candidate.accountID != accountID {
			continue
		}
		filtered = append(filtered, candidate)
	}

	if len(filtered) == 1 {
		return filtered[0], http.StatusOK, ""
	}
	if len(filtered) == 0 {
		return accountLimitsCredential{}, http.StatusNotFound, "no matching local credential supports account limits"
	}
	return accountLimitsCredential{}, http.StatusConflict, "multiple credentials support account limits; specify provider and account_id"
}

func (s *Server) accountLimitsCredentials() []accountLimitsCredential {
	candidates := make([]accountLimitsCredential, 0)
	if s != nil && s.handlers != nil && s.handlers.AuthManager != nil {
		for _, entry := range s.handlers.AuthManager.List() {
			if entry == nil || entry.Disabled || entry.Status == cliproxyauth.StatusDisabled {
				continue
			}
			accountID := strings.TrimSpace(entry.ID)
			if accountID == "" {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(entry.Provider)) {
			case "claude":
				candidates = append(candidates, accountLimitsCredential{provider: accountlimits.ProviderAnthropic, accountID: accountID})
			case "codex":
				token := metadataString(entry.Metadata, "access_token")
				if token == "" {
					continue
				}
				candidates = append(candidates, accountLimitsCredential{
					provider:          accountlimits.ProviderOpenAI,
					accountID:         accountID,
					upstreamAccountID: metadataString(entry.Metadata, "account_id"),
					token:             token,
					baseURL:           attributeString(entry.Attributes, "base_url"),
					proxyURL:          strings.TrimSpace(entry.ProxyURL),
				})
			}
		}
	}
	cfgSnapshot := s.accountLimitsConfig()
	if cfgSnapshot != nil {
		for _, compatibility := range cfgSnapshot.OpenAICompatibility {
			if compatibility.Disabled || !strings.EqualFold(strings.TrimSpace(compatibility.Name), accountlimits.ProviderZai) {
				continue
			}
			for index, entry := range compatibility.APIKeyEntries {
				if token := strings.TrimSpace(entry.APIKey); token != "" {
					id := strings.TrimSpace(compatibility.Name)
					if len(compatibility.APIKeyEntries) > 1 {
						id = fmt.Sprintf("%s:%d", id, index+1)
					}
					candidates = append(candidates, accountLimitsCredential{
						provider:  accountlimits.ProviderZai,
						accountID: id,
						token:     token,
						baseURL:   strings.TrimSpace(compatibility.BaseURL),
						proxyURL:  strings.TrimSpace(entry.ProxyURL),
					})
				}
			}
		}
	}
	return candidates
}

func normalizeLimitsProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic", "claude":
		return accountlimits.ProviderAnthropic
	case "openai", "codex":
		return accountlimits.ProviderOpenAI
	case "zai", "z.ai":
		return accountlimits.ProviderZai
	default:
		return strings.ToLower(strings.TrimSpace(provider))
	}
}

func (s *Server) fetchOpenAIAccountLimits(ctx context.Context, credential accountLimitsCredential) (accountlimits.ProviderLimitsPayload, int, string) {
	upstreamAccountID := credential.upstreamAccountID
	if upstreamAccountID == "" {
		var err error
		upstreamAccountID, err = chatGPTAccountIDFromAccessToken(credential.token)
		if err != nil {
			return accountlimits.ProviderLimitsPayload{}, http.StatusUnauthorized, "local Codex credential has no ChatGPT account id"
		}
	}
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, codexUsageURL(credential.baseURL), nil)
	if errRequest != nil {
		return accountlimits.ProviderLimitsPayload{}, http.StatusBadRequest, errRequest.Error()
	}
	request.Header.Set("Authorization", "Bearer "+credential.token)
	request.Header.Set("ChatGPT-Account-Id", upstreamAccountID)
	request.Header.Set("User-Agent", "codex-cli")
	request.Header.Set("Accept", "application/json")

	response, status, errMessage := s.doLimitsRequest(ctx, request, credential.proxyURL, true)
	if errMessage != "" {
		return accountlimits.ProviderLimitsPayload{}, status, errMessage
	}
	var upstreamPayload map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(response)))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&upstreamPayload); errDecode != nil {
		return accountlimits.ProviderLimitsPayload{}, http.StatusBadGateway, "Codex usage response has unexpected shape"
	}
	payload := accountlimits.OpenAIProviderLimitsFromUsage(credential.accountID, upstreamPayload)
	capturedAt := unixNow()
	payload.CapturedAt = &capturedAt
	return payload, http.StatusOK, ""
}

func (s *Server) fetchZaiAccountLimits(ctx context.Context, credential accountLimitsCredential) (accountlimits.ProviderLimitsPayload, int, string) {
	request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, zaiQuotaURL(credential.baseURL), nil)
	if errRequest != nil {
		return accountlimits.ProviderLimitsPayload{}, http.StatusBadRequest, errRequest.Error()
	}
	request.Header.Set("Authorization", "Bearer "+credential.token)
	request.Header.Set("Accept", "application/json")

	response, status, errMessage := s.doLimitsRequest(ctx, request, credential.proxyURL, false)
	if errMessage != "" {
		return accountlimits.ProviderLimitsPayload{}, status, errMessage
	}
	var upstreamPayload struct {
		Data map[string]any `json:"data"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(response)))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&upstreamPayload); errDecode != nil {
		return accountlimits.ProviderLimitsPayload{}, http.StatusBadGateway, "Z.AI quota response has unexpected shape"
	}
	payload := accountlimits.ZaiProviderLimitsFromQuota(credential.accountID, upstreamPayload.Data)
	capturedAt := unixNow()
	payload.CapturedAt = &capturedAt
	return payload, http.StatusOK, ""
}

func (s *Server) doLimitsRequest(ctx context.Context, request *http.Request, proxyURL string, useUTLS bool) ([]byte, int, string) {
	auth := &cliproxyauth.Auth{ProxyURL: proxyURL}
	cfg := s.accountLimitsConfig()
	client := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0)
	if useUTLS {
		client = helps.NewUtlsHTTPClient(ctx, cfg, auth, 0)
	}
	response, errDo := client.Do(request)
	if errDo != nil {
		return nil, http.StatusBadGateway, errDo.Error()
	}
	defer func() {
		if errClose := response.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("failed to close account limits response body")
		}
	}()
	body, errRead := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if errRead != nil {
		return nil, http.StatusBadGateway, errRead.Error()
	}
	if response.StatusCode >= http.StatusBadRequest {
		return nil, response.StatusCode, fmt.Sprintf("limits upstream returned HTTP %d", response.StatusCode)
	}
	return body, http.StatusOK, ""
}

func chatGPTAccountIDFromAccessToken(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid JWT token format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
	}
	if err != nil {
		return "", err
	}
	var claims struct {
		OpenAIAuth struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if errUnmarshal := json.Unmarshal(payload, &claims); errUnmarshal != nil {
		return "", errUnmarshal
	}
	accountID := strings.TrimSpace(claims.OpenAIAuth.ChatGPTAccountID)
	if accountID == "" {
		return "", fmt.Errorf("missing ChatGPT account id")
	}
	return accountID, nil
}

func codexUsageURL(baseURL string) string {
	normalized := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if normalized == "" {
		normalized = "https://chatgpt.com/backend-api"
	}
	if (strings.HasPrefix(normalized, "https://chatgpt.com") || strings.HasPrefix(normalized, "https://chat.openai.com")) && !strings.Contains(normalized, "/backend-api") {
		normalized += "/backend-api"
	}
	if strings.Contains(normalized, "/backend-api") {
		normalized = strings.TrimSuffix(normalized, "/codex")
		return normalized + "/wham/usage"
	}
	return normalized + "/api/codex/usage"
}

func zaiQuotaURL(baseURL string) string {
	const quotaPath = "/api/monitor/usage/quota/limit"
	base := strings.TrimSpace(baseURL)
	if parsed, err := url.Parse(base); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return parsed.Scheme + "://" + parsed.Host + quotaPath
	}
	return "https://api.z.ai" + quotaPath
}

func metadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func attributeString(attributes map[string]string, key string) string {
	return strings.TrimSpace(attributes[key])
}

func (s *Server) accountLimitsConfig() *config.Config {
	if s == nil {
		return nil
	}
	cfg := s.accountLimitsCfg.Load()
	if cfg == nil {
		return nil
	}
	return cfg
}

var unixNow = func() int64 { return time.Now().Unix() }
