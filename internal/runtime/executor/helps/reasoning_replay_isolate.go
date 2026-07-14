package helps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
)

// AccessProviderFromContext returns the frontend access provider name stored by
// AuthMiddleware (gin key "accessProvider").
func AccessProviderFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return ""
	}
	if v, exists := ginCtx.Get("accessProvider"); exists {
		switch value := v.(type) {
		case string:
			return strings.TrimSpace(value)
		case fmt.Stringer:
			return strings.TrimSpace(value.String())
		default:
			return strings.TrimSpace(fmt.Sprintf("%v", value))
		}
	}
	return ""
}

// IsolateClientReasoningReplaySessionKey namespaces client-controlled session
// keys by the downstream access provider and principal so two callers cannot
// share encrypted reasoning by reusing prompt_cache_key / window / session
// headers across auth realms. Trusted execution session keys keep their
// existing form. Client-controlled sessions without a caller principal are
// disabled rather than shared globally.
func IsolateClientReasoningReplaySessionKey(ctx context.Context, sessionKey string) string {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return ""
	}
	if strings.HasPrefix(sessionKey, "execution:") {
		return sessionKey
	}
	// Hash the provider-issued principal as an opaque value: AuthMiddleware stores
	// principals verbatim, so two distinct principals that differ only in
	// surrounding whitespace (e.g. "alice" vs " alice ") must not be trimmed into
	// the same replay namespace. Only a truly empty principal counts as missing.
	principal := APIKeyFromContext(ctx)
	if principal == "" {
		return ""
	}
	provider := AccessProviderFromContext(ctx)
	material := provider + "\x00" + principal
	sum := sha256.Sum256([]byte(material))
	return "caller:" + hex.EncodeToString(sum[:8]) + ":" + sessionKey
}
