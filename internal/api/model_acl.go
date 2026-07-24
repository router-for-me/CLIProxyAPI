// Per-API-key model ACL enforcement.
//
// AuthMiddleware identifies the calling client by api key value. ModelACLMiddleware
// then checks the model targeted by the request against that key's allowed-models
// policy (configured via SDKConfig.APIKeyPolicies / APIKeyDefaultPolicy) and
// rejects the request with HTTP 403 on mismatch.
//
// The middleware extracts the model identifier from whichever location the
// upstream route uses:
//
//   - JSON body field "model" (chat completions, messages, responses, codex)
//   - URL path segment after /v1beta/models/ (Gemini generative endpoints)
//
// When no model can be determined (e.g. listing endpoints, websocket upgrades),
// the middleware allows the request through; access enforcement remains the
// responsibility of the route's own logic in those cases.

package api

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/tidwall/gjson"
)

// ModelACLMiddleware enforces SDKConfig.APIKeyPolicies for the routes it is
// installed on. The cfgFn closure is evaluated on every request so that hot
// config reloads take effect immediately.
func ModelACLMiddleware(cfgFn func() *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := cfgFn()
		if cfg == nil {
			c.Next()
			return
		}

		// Skip enforcement when no policies are configured AND the default
		// policy permits everything; the per-key check would be a no-op.
		if len(cfg.APIKeyPolicies) == 0 && !strings.EqualFold(strings.TrimSpace(cfg.APIKeyDefaultPolicy), config.APIKeyDefaultPolicyDenyAll) {
			c.Next()
			return
		}

		raw, exists := c.Get("apiKey")
		if !exists {
			// AuthMiddleware did not run or did not identify a key. We do not
			// enforce in that case to preserve the legacy "no auth provider =>
			// allow" behavior of AuthMiddleware.
			c.Next()
			return
		}
		apiKey, ok := raw.(string)
		if !ok || strings.TrimSpace(apiKey) == "" {
			c.Next()
			return
		}

		model, found := extractRequestedModel(c)
		if !found {
			// No model in this request shape (listing, ping, etc.) — allow.
			c.Next()
			return
		}

		if cfg.IsModelAllowedForKey(apiKey, model) {
			c.Next()
			return
		}

		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"type":    "model_not_allowed_for_key",
				"message": "this api key is not permitted to use the requested model",
				"model":   model,
			},
		})
	}
}

// extractRequestedModel returns the model identifier the current request is
// targeting, or ok=false when none can be determined for the route.
//
// The function intentionally consumes and restores the request body so that
// downstream handlers see an unmodified io.Reader.
func extractRequestedModel(c *gin.Context) (model string, ok bool) {
	if c == nil || c.Request == nil {
		return "", false
	}

	// Gemini-style: /v1beta/models/<model>:<action>
	if strings.HasPrefix(c.Request.URL.Path, "/v1beta/models/") {
		rest := strings.TrimPrefix(c.Request.URL.Path, "/v1beta/models/")
		// Drop everything from the first ':' (action) onward.
		if idx := strings.Index(rest, ":"); idx >= 0 {
			rest = rest[:idx]
		}
		// Drop everything from the first '/' onward (no nested paths today,
		// but be defensive).
		if idx := strings.Index(rest, "/"); idx >= 0 {
			rest = rest[:idx]
		}
		rest = strings.TrimSpace(rest)
		if rest != "" {
			return rest, true
		}
	}

	// JSON body: "model" field. Only POST/PUT/PATCH carry bodies we care about.
	method := c.Request.Method
	if method != http.MethodPost && method != http.MethodPut && method != http.MethodPatch {
		return "", false
	}
	if c.Request.Body == nil {
		return "", false
	}

	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return "", false
	}
	// Always restore the body so downstream handlers can re-read it.
	c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	if len(bodyBytes) == 0 {
		return "", false
	}
	res := gjson.GetBytes(bodyBytes, "model")
	if !res.Exists() || res.Type != gjson.String {
		return "", false
	}
	model = strings.TrimSpace(res.String())
	if model == "" {
		return "", false
	}
	return model, true
}
