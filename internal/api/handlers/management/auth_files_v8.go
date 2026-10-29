package management

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
)

const (
	oauthSessionProxyURLContextKey = "management.oauth.proxy_url"
	oauthSessionProviderContextKey = "management.oauth.provider"
	oauthSessionDomainContextKey   = "management.oauth.domain"
	oauthSessionIsWebUIContextKey  = "management.oauth.is_webui"
	oauthSessionMetadataContextKey = "management.oauth.metadata"
	oauthSessionTargetContextKey   = "management.oauth.target"
)

func oauthSessionTargetFromContext(c *gin.Context) (oauthRecoveryTarget, bool) {
	if c == nil {
		return oauthRecoveryTarget{}, false
	}
	val, ok := c.Get(oauthSessionTargetContextKey)
	if !ok {
		return oauthRecoveryTarget{}, false
	}
	target, okTarget := val.(oauthRecoveryTarget)
	return target, okTarget && target.AuthID != ""
}

func oauthSessionProxyURLFromContext(c *gin.Context) string {
	if c == nil {
		return ""
	}
	val, ok := c.Get(oauthSessionProxyURLContextKey)
	if !ok {
		return ""
	}
	proxyURL, _ := val.(string)
	return strings.TrimSpace(proxyURL)
}

func oauthSessionProviderFromContext(c *gin.Context) string {
	if c == nil {
		return ""
	}
	val, ok := c.Get(oauthSessionProviderContextKey)
	if !ok {
		return ""
	}
	provider, _ := val.(string)
	return strings.TrimSpace(provider)
}

func oauthSessionDomainFromContext(c *gin.Context) string {
	if c == nil {
		return ""
	}
	val, ok := c.Get(oauthSessionDomainContextKey)
	if !ok {
		return ""
	}
	domain, _ := val.(string)
	return strings.TrimSpace(domain)
}

func oauthSessionExtraMetadataFromContext(c *gin.Context) map[string]any {
	if c == nil {
		return nil
	}
	val, ok := c.Get(oauthSessionMetadataContextKey)
	if !ok {
		return nil
	}
	meta, _ := val.(map[string]any)
	return cloneOAuthSessionMetadata(meta)
}

func validateOAuthSessionProxyURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if !proxyutil.ValidRequestProxy(trimmed) {
		return "", fmt.Errorf("invalid proxy_url")
	}
	setting, errParse := proxyutil.Parse(trimmed)
	if errParse != nil || setting.Mode != proxyutil.ModeProxy || setting.URL == nil {
		return "", fmt.Errorf("invalid proxy_url")
	}
	if strings.HasSuffix(setting.URL.Host, ":") {
		return "", fmt.Errorf("invalid proxy_url")
	}
	if setting.URL.RawQuery != "" || setting.URL.Fragment != "" || (setting.URL.Path != "" && setting.URL.Path != "/") {
		return "", fmt.Errorf("invalid proxy_url")
	}
	if (setting.URL.Scheme == "socks5" || setting.URL.Scheme == "socks5h") && setting.URL.Port() == "" {
		return "", fmt.Errorf("invalid proxy_url")
	}
	if _, _, errTransport := proxyutil.BuildHTTPTransport(trimmed); errTransport != nil {
		return "", fmt.Errorf("invalid proxy_url")
	}
	if _, _, errDialer := proxyutil.BuildDialer(trimmed); errDialer != nil {
		return "", fmt.Errorf("invalid proxy_url")
	}
	return trimmed, nil
}

func applySessionProxyToAuthRecord(record *coreauth.Auth, proxyURL string) {
	proxyURL = strings.TrimSpace(proxyURL)
	if record == nil || proxyURL == "" {
		return
	}
	record.ProxyURL = proxyURL
	if record.Metadata == nil {
		record.Metadata = make(map[string]any)
	}
	record.Metadata["proxy_url"] = proxyURL
	if setter, ok := record.Storage.(interface{ SetMetadata(map[string]any) }); ok {
		setter.SetMetadata(record.Metadata)
	}
}

// StartOAuthV8 dispatches login using the provider query parameter or POST JSON body.
func (h *Handler) StartOAuthV8(c *gin.Context) {
	if c.Request != nil && c.Request.URL != nil {
		if query := c.Request.URL.Query(); query.Has("proxy_url") || query.Has("proxy-url") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "proxy_url must be provided in the request body"})
			return
		}
	}

	provider := strings.TrimSpace(c.Query("provider"))
	targetName := firstNonEmpty(strings.TrimSpace(c.Query("name")), strings.TrimSpace(c.Query("target_auth_id")), strings.TrimSpace(c.Query("auth_id")))
	targetAuthIndex := strings.TrimSpace(c.Query("auth_index"))
	verifyAfterLogin := strings.EqualFold(strings.TrimSpace(c.Query("verify")), "true") || strings.TrimSpace(c.Query("verify")) == "1"
	verifyModel := strings.TrimSpace(c.Query("verify_model"))
	if c.Request != nil && c.Request.Body != nil {
		rawBody, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil {
			c.JSON(clienterror.HTTPStatusFromErrorOr(errRead, http.StatusBadRequest), gin.H{"error": "invalid request body"})
			return
		}
		if len(bytes.TrimSpace(rawBody)) > 0 {
			if c.Request.Method != http.MethodPost {
				c.JSON(http.StatusBadRequest, gin.H{"error": "request body requires POST"})
				return
			}
			var bodyMap map[string]any
			if errUnmarshal := json.Unmarshal(rawBody, &bodyMap); errUnmarshal != nil || bodyMap == nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
				return
			}

			if rawProvider, exists := bodyMap["provider"]; exists && rawProvider != nil {
				providerStr, ok := rawProvider.(string)
				if !ok {
					c.JSON(http.StatusBadRequest, gin.H{"error": "invalid provider"})
					return
				}
				bodyProvider := strings.TrimSpace(providerStr)
				if provider != "" && bodyProvider != "" && !strings.EqualFold(provider, bodyProvider) {
					c.JSON(http.StatusBadRequest, gin.H{"error": "conflicting provider parameter"})
					return
				}
				if provider == "" {
					provider = bodyProvider
				}
			}
			for _, key := range []string{"name", "target_auth_id", "auth_id"} {
				if rawVal, exists := bodyMap[key]; exists && rawVal != nil {
					strVal, ok := rawVal.(string)
					if !ok {
						c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + key})
						return
					}
					if trimmed := strings.TrimSpace(strVal); trimmed != "" && targetName == "" {
						targetName = trimmed
					}
				}
			}
			if rawIdx, exists := bodyMap["auth_index"]; exists && rawIdx != nil {
				idxStr, ok := rawIdx.(string)
				if !ok {
					c.JSON(http.StatusBadRequest, gin.H{"error": "invalid auth_index"})
					return
				}
				if trimmed := strings.TrimSpace(idxStr); trimmed != "" && targetAuthIndex == "" {
					targetAuthIndex = trimmed
				}
			}

			var rawProxySnake, rawProxyKebab string
			if v, exists := bodyMap["proxy_url"]; exists && v != nil {
				s, ok := v.(string)
				if !ok {
					c.JSON(http.StatusBadRequest, gin.H{"error": "invalid proxy_url"})
					return
				}
				rawProxySnake = strings.TrimSpace(s)
			}
			if v, exists := bodyMap["proxy-url"]; exists && v != nil {
				s, ok := v.(string)
				if !ok {
					c.JSON(http.StatusBadRequest, gin.H{"error": "invalid proxy_url"})
					return
				}
				rawProxyKebab = strings.TrimSpace(s)
			}
			if rawProxySnake != "" && rawProxyKebab != "" && rawProxySnake != rawProxyKebab {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid proxy_url"})
				return
			}
			rawProxy := rawProxySnake
			if rawProxy == "" {
				rawProxy = rawProxyKebab
			}
			validatedProxy, errProxy := validateOAuthSessionProxyURL(rawProxy)
			if errProxy != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid proxy_url"})
				return
			}
			if validatedProxy != "" {
				c.Set(oauthSessionProxyURLContextKey, validatedProxy)
			}

			if rawDomain, exists := bodyMap["domain"]; exists && rawDomain != nil {
				domainStr, ok := rawDomain.(string)
				if !ok {
					c.JSON(http.StatusBadRequest, gin.H{"error": "invalid domain"})
					return
				}
				if trimmed := strings.TrimSpace(domainStr); trimmed != "" {
					c.Set(oauthSessionDomainContextKey, trimmed)
				}
			} else if rawChan, exists := bodyMap["channel"]; exists && rawChan != nil {
				chanStr, ok := rawChan.(string)
				if !ok {
					c.JSON(http.StatusBadRequest, gin.H{"error": "invalid channel"})
					return
				}
				if trimmed := strings.TrimSpace(chanStr); trimmed != "" {
					c.Set(oauthSessionDomainContextKey, trimmed)
				}
			}

			if rawWebUI, exists := bodyMap["is_webui"]; exists && rawWebUI != nil {
				switch v := rawWebUI.(type) {
				case bool:
					c.Set(oauthSessionIsWebUIContextKey, v)
				case string:
					switch strings.ToLower(strings.TrimSpace(v)) {
					case "1", "true", "yes", "on":
						c.Set(oauthSessionIsWebUIContextKey, true)
					case "0", "false", "no", "off", "":
						c.Set(oauthSessionIsWebUIContextKey, false)
					default:
						c.JSON(http.StatusBadRequest, gin.H{"error": "invalid is_webui"})
						return
					}
				default:
					c.JSON(http.StatusBadRequest, gin.H{"error": "invalid is_webui"})
					return
				}
			}

			if rawVerify, exists := bodyMap["verify"]; exists && rawVerify != nil {
				switch v := rawVerify.(type) {
				case bool:
					verifyAfterLogin = v
				case string:
					switch strings.ToLower(strings.TrimSpace(v)) {
					case "1", "true", "yes", "on":
						verifyAfterLogin = true
					case "0", "false", "no", "off", "":
						verifyAfterLogin = false
					default:
						c.JSON(http.StatusBadRequest, gin.H{"error": "invalid verify"})
						return
					}
				default:
					c.JSON(http.StatusBadRequest, gin.H{"error": "invalid verify"})
					return
				}
			}
			if rawVerifyModel, exists := bodyMap["verify_model"]; exists && rawVerifyModel != nil {
				vmStr, ok := rawVerifyModel.(string)
				if !ok {
					c.JSON(http.StatusBadRequest, gin.H{"error": "invalid verify_model"})
					return
				}
				if trimmed := strings.TrimSpace(vmStr); trimmed != "" {
					verifyModel = trimmed
				}
			}

			extraMeta := make(map[string]any)
			for k, v := range bodyMap {
				switch k {
				case "provider", "proxy_url", "proxy-url", "is_webui", "name", "target_auth_id", "auth_id", "auth_index", "verify", "verify_model":
					continue
				default:
					extraMeta[k] = v
				}
			}
			if len(extraMeta) > 0 {
				c.Set(oauthSessionMetadataContextKey, extraMeta)
			}
		}
	}

	if targetName != "" || targetAuthIndex != "" {
		targetAuth, okTarget := h.lookupAuthFile(targetName, targetAuthIndex)
		if !okTarget || targetAuth == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
			return
		}
		if provider != "" && !strings.EqualFold(strings.TrimSpace(targetAuth.Provider), strings.TrimSpace(provider)) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "provider does not match target credential"})
			return
		}
		if provider == "" {
			provider = strings.TrimSpace(targetAuth.Provider)
		}
		targetAccountID := ""
		if targetAuth.Metadata != nil {
			targetAccountID, _ = targetAuth.Metadata["account_id"].(string)
		}
		c.Set(oauthSessionTargetContextKey, oauthRecoveryTarget{
			AuthID:           targetAuth.ID,
			FileName:         firstNonEmpty(strings.TrimSpace(targetAuth.FileName), targetAuth.ID),
			Provider:         strings.TrimSpace(targetAuth.Provider),
			Email:            authEmail(targetAuth),
			AccountID:        strings.TrimSpace(targetAccountID),
			VerifyAfterLogin: verifyAfterLogin,
			VerifyModel:      verifyModel,
		})
	}

	c.Set(oauthSessionProviderContextKey, provider)

	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "":
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider is required"})
	case "claude":
		h.RequestAnthropicToken(c)
	case "codex":
		h.RequestCodexToken(c)
	case "antigravity":
		h.RequestAntigravityToken(c)
	case "kimi":
		h.RequestKimiToken(c)
	case "kimi-ai":
		h.RequestKimiAIToken(c)
	case "xai":
		h.RequestXAIToken(c)
	case "devin":
		h.RequestDevinToken(c)
	case "meta":
		h.RequestMetaToken(c)
	default:
		if !h.ServePluginAuthURL(c) {
			c.JSON(http.StatusNotFound, gin.H{"error": "provider_not_found"})
		}
	}
}

// ImportOAuthV8 dispatches credential import using the provider query parameter.
func (h *Handler) ImportOAuthV8(c *gin.Context) {
	switch strings.ToLower(strings.TrimSpace(c.Query("provider"))) {
	case "":
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider is required"})
	case "vertex":
		h.ImportVertexCredential(c)
	default:
		c.JSON(http.StatusNotFound, gin.H{"error": "provider_not_found"})
	}
}

// VerifyAuthFileV8 runs an operator-triggered, account-pinned inference verification
// using an isolated session and minimal non-user prompt/token budget.
func (h *Handler) VerifyAuthFileV8(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth manager is not available"})
		return
	}
	targetName := firstNonEmpty(strings.TrimSpace(c.Query("name")), strings.TrimSpace(c.Query("target_auth_id")), strings.TrimSpace(c.Query("auth_id")))
	targetAuthIndex := strings.TrimSpace(c.Query("auth_index"))
	model := strings.TrimSpace(c.Query("model"))

	if c.Request != nil && c.Request.Body != nil {
		rawBody, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil {
			c.JSON(clienterror.HTTPStatusFromErrorOr(errRead, http.StatusBadRequest), gin.H{"error": "invalid request body"})
			return
		}
		if len(bytes.TrimSpace(rawBody)) > 0 {
			var bodyMap map[string]any
			if errUnmarshal := json.Unmarshal(rawBody, &bodyMap); errUnmarshal != nil || bodyMap == nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
				return
			}
			for _, key := range []string{"name", "target_auth_id", "auth_id"} {
				if rawVal, exists := bodyMap[key]; exists && rawVal != nil {
					strVal, ok := rawVal.(string)
					if !ok {
						c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + key})
						return
					}
					if trimmed := strings.TrimSpace(strVal); trimmed != "" && targetName == "" {
						targetName = trimmed
					}
				}
			}
			if rawIdx, exists := bodyMap["auth_index"]; exists && rawIdx != nil {
				idxStr, ok := rawIdx.(string)
				if !ok {
					c.JSON(http.StatusBadRequest, gin.H{"error": "invalid auth_index"})
					return
				}
				if trimmed := strings.TrimSpace(idxStr); trimmed != "" && targetAuthIndex == "" {
					targetAuthIndex = trimmed
				}
			}
			if rawModel, exists := bodyMap["model"]; exists && rawModel != nil {
				modelStr, ok := rawModel.(string)
				if !ok {
					c.JSON(http.StatusBadRequest, gin.H{"error": "invalid model"})
					return
				}
				if trimmed := strings.TrimSpace(modelStr); trimmed != "" && model == "" {
					model = trimmed
				}
			}
		}
	}

	if targetName == "" && targetAuthIndex == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name or auth_index is required"})
		return
	}
	targetAuth, okTarget := h.lookupAuthFile(targetName, targetAuthIndex)
	if !okTarget || targetAuth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}

	updated, errVerify := h.authManager.VerifyInference(c.Request.Context(), targetAuth.ID, model)
	var entry gin.H
	if updated != nil {
		entry = h.buildAuthFileEntry(updated, authFileQuotaContext{v8: true})
	}
	if errVerify != nil {
		status := clienterror.HTTPStatusFromErrorOr(errVerify, http.StatusBadGateway)
		if status < 400 {
			status = http.StatusBadGateway
		}
		if statusCoder, okCode := errVerify.(interface{ StatusCode() int }); okCode && statusCoder != nil && statusCoder.StatusCode() >= 400 {
			status = statusCoder.StatusCode()
		}
		resp := gin.H{
			"status":   "error",
			"verified": false,
			"error":    coreauth.SanitizeRenewalMessage(errVerify.Error()),
		}
		if entry != nil {
			resp["recovery_stage"] = entry["recovery_stage"]
			resp["renewal_state"] = entry["renewal_state"]
			resp["quota_blocked"] = entry["quota_blocked"]
			resp["credential"] = entry
		}
		c.JSON(status, resp)
		return
	}
	verified := updated != nil && updated.InferenceVerified
	resp := gin.H{
		"status":   "ok",
		"verified": verified,
	}
	if entry != nil {
		resp["recovery_stage"] = entry["recovery_stage"]
		resp["renewal_state"] = entry["renewal_state"]
		resp["inference_verified"] = entry["inference_verified"]
		resp["credential"] = entry
	}
	c.JSON(http.StatusOK, resp)
}
