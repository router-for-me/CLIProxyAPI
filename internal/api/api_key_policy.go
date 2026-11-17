package api

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/apikeylimits"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
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
		rejectAPIKeyPolicy(c, principal, policy, http.StatusUnauthorized, "authentication_error", "api_key_disabled", "API key is disabled")
		return false
	}
	if policy.ExpiredAt(time.Now()) {
		rejectAPIKeyPolicy(c, principal, policy, http.StatusUnauthorized, "authentication_error", "api_key_expired", "API key has expired")
		return false
	}
	if exceeded, hit := apikeylimits.Default().Check(principal, policy.Limits); hit {
		retryAfter := int(math.Ceil(time.Until(exceeded.ResetAt).Seconds()))
		if retryAfter < 1 {
			retryAfter = 1
		}
		c.Header("Retry-After", strconv.Itoa(retryAfter))
		rejectAPIKeyPolicy(c, principal, policy, http.StatusTooManyRequests, "rate_limit_error", "api_key_quota_exceeded",
			fmt.Sprintf("API key %s %s limit reached; resets at %s", exceeded.Window, exceeded.Metric, exceeded.ResetAt.Format(time.RFC3339)))
		return false
	}
	if len(policy.AllowedModels) > 0 {
		c.Set(coreauth.AllowedModelsGinKey, policy.AllowedModels)
		c.Request = c.Request.WithContext(coreauth.WithAllowedModels(c.Request.Context(), policy.AllowedModels))
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

// rejectAPIKeyPolicy counts and logs a policy refusal for the key, then aborts the request.
func rejectAPIKeyPolicy(c *gin.Context, principal string, policy config.APIKeyPolicy, status int, errorType, code, message string) {
	apikeylimits.Default().AddRejected(principal, code)
	label := policy.Name
	if label == "" {
		label = "key " + apikeylimits.Fingerprint(principal)[:8]
	}
	path := ""
	if c.Request != nil && c.Request.URL != nil {
		path = c.Request.URL.Path
	}
	// The log formatter prints only allow-listed fields, so key and path go in the message.
	log.WithField("reason", code).Warnf("api key policy rejected request: api_key=%q path=%s", label, path)
	abortAPIKeyPolicy(c, status, errorType, code, message)
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
