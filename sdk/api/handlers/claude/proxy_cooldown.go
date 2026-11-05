package claude

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// proxyCooldownMarker signs only a concrete local all-candidate deadline. The
// key is the authenticated downstream client key, never an upstream credential.
// Ceil the absolute instant so a representable second never precedes it.
func proxyCooldownMarker(deadline time.Time, key string, now time.Time) string {
	if key == "" || !deadline.After(now) {
		return ""
	}
	epoch := deadline.Unix()
	if deadline.Nanosecond() > 0 {
		epoch++
	}
	if epoch <= 0 || epoch > 253402300799 {
		return ""
	}
	canonical := strconv.FormatInt(epoch, 10)
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte("CLIProxyAPI/T3/proxy_cooldown/v1\nnext_reevaluation_at=" + canonical + "\n"))
	return fmt.Sprintf("[[CPA_PROXY_COOLDOWN_V1:%s:%x]] ", canonical, mac.Sum(nil))
}

// toClaudeErrorForContext adds signed metadata only for explicit client opt-in.
// Default Claude errors and direct-response passthrough retain their format.
func (h *ClaudeCodeAPIHandler) toClaudeErrorForContext(c *gin.Context, msg *interfaces.ErrorMessage) claudeErrorResponse {
	body := h.toClaudeError(msg)
	if c == nil || c.Request == nil || c.GetHeader("X-CLIProxyAPI-Cooldown-Metadata") != "signed-v1" || msg == nil || msg.StatusCode != http.StatusTooManyRequests {
		return body
	}
	deadline, ok := coreauth.ProxyCooldownNextReevaluation(msg.Error)
	if !ok {
		return body
	}
	// Middleware's userApiKey is a provider principal, which may be a public
	// plugin account ID. Only the built-in validator establishes key provenance.
	if c.GetString("accessProvider") != sdkaccess.DefaultAccessProviderName {
		return body
	}
	key, ok := c.Get("userApiKey")
	if !ok {
		return body
	}
	token, ok := key.(string)
	if !ok || token == "" {
		return body
	}
	cfg := h.CurrentConfig()
	if cfg == nil {
		return body
	}
	configured := false
	for _, configuredKey := range cfg.APIKeys {
		if strings.TrimSpace(configuredKey) == token {
			configured = true
			break
		}
	}
	if !configured {
		return body
	}
	if marker := proxyCooldownMarker(deadline, token, time.Now()); marker != "" {
		body.Error.Message = marker + body.Error.Message
	}
	return body
}
