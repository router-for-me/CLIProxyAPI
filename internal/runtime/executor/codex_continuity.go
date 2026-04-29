package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type codexContinuity struct {
	Key string
}

func ginContextFrom(ctx context.Context) *gin.Context {
	if ctx == nil {
		return nil
	}
	ginCtx, _ := ctx.Value("gin").(*gin.Context)
	return ginCtx
}

func principalString(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", raw))
	}
}

func continuityMetadataString(meta map[string]any, key string) string {
	if len(meta) == 0 {
		return ""
	}
	raw, ok := meta[key]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}

func resolveCodexContinuity(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) codexContinuity {
	if promptCacheKey := strings.TrimSpace(gjson.GetBytes(req.Payload, "prompt_cache_key").String()); promptCacheKey != "" {
		return codexContinuity{Key: promptCacheKey}
	}
	if executionSession := continuityMetadataString(opts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey); executionSession != "" {
		return codexContinuity{Key: executionSession}
	}
	if apiKey := strings.TrimSpace(helps.APIKeyFromContext(ctx)); apiKey != "" {
		return codexContinuity{Key: uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:"+apiKey)).String()}
	}
	if ginCtx := ginContextFrom(ctx); ginCtx != nil {
		if v, exists := ginCtx.Get("apiKey"); exists && v != nil {
			if trimmed := principalString(v); trimmed != "" {
				return codexContinuity{Key: uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:"+trimmed)).String()}
			}
		}
	}
	if auth != nil {
		if authID := strings.TrimSpace(auth.ID); authID != "" {
			return codexContinuity{Key: uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:auth:"+authID)).String()}
		}
	}
	return codexContinuity{}
}

func applyCodexContinuityBody(rawJSON []byte, continuity codexContinuity) []byte {
	if continuity.Key == "" {
		return rawJSON
	}
	rawJSON, _ = sjson.SetBytes(rawJSON, "prompt_cache_key", continuity.Key)
	return rawJSON
}

func applyCodexContinuityHeaders(headers http.Header, continuity codexContinuity) {
	if headers == nil || continuity.Key == "" {
		return
	}
	headers.Set("session_id", continuity.Key)
}
