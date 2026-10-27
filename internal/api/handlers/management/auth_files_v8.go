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
)

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

			extraMeta := make(map[string]any)
			for k, v := range bodyMap {
				switch k {
				case "provider", "proxy_url", "proxy-url", "is_webui":
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
