package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// apiKeyPolicyConfigFunc returns the live configuration used to resolve client key policies.
type apiKeyPolicyConfigFunc func() *config.Config

// applyAPIKeyPolicy enforces access.api-key-policies for an authenticated request.
// It returns false when the request has been aborted.
func applyAPIKeyPolicy(c *gin.Context, cfgFn apiKeyPolicyConfigFunc, principal, provider string) bool {
	if c == nil || cfgFn == nil || principal == "" || !coreusage.IsNativeAccessProvider(provider) {
		return true
	}
	cfg := cfgFn()
	if cfg == nil {
		return true
	}
	policy, ok := cfg.FindAPIKeyPolicy(principal)
	if !ok {
		return true
	}
	if policy.Disabled {
		abortAPIKeyPolicy(c, http.StatusUnauthorized, "authentication_error", "api_key_disabled", "API key is disabled")
		return false
	}
	allowed := coreauth.NewAllowedAuthSet(policy.AllowedAuths)
	if allowed == nil {
		return true
	}
	if cfg.Home.Enabled {
		// Home dispatch picks credentials remotely; fail closed instead of ignoring the restriction.
		abortAPIKeyPolicy(c, http.StatusServiceUnavailable, "server_error", "api_key_policy_unsupported", "credential restrictions for this API key are not supported while Home is enabled")
		return false
	}
	c.Set(coreauth.AllowedAuthsGinKey, allowed)
	c.Request = c.Request.WithContext(coreauth.WithAllowedAuths(c.Request.Context(), policy.AllowedAuths))
	return true
}

func abortAPIKeyPolicy(c *gin.Context, status int, errorType, code, message string) {
	closeUnconsumedRequestOnAbort(c)
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{
		"message": message,
		"type":    errorType,
		"param":   nil,
		"code":    code,
	}})
}
