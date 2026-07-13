package helps

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const (
	ClaudeCodeSessionHeader = "X-Claude-Code-Session-Id"
	ClaudeCodeAgentHeader   = "X-Claude-Code-Agent-Id"
)

var claudeCodeSessionSuffixPattern = regexp.MustCompile(`_session_([a-f0-9-]+)$`)

// ExtractClaudeCodeSessionID resolves a Claude Code session ID, preferring X-Claude-Code-Session-Id over payload metadata.
func ExtractClaudeCodeSessionID(ctx context.Context, payload []byte, headers http.Header) string {
	if headers != nil {
		if sessionID := strings.TrimSpace(headers.Get(ClaudeCodeSessionHeader)); sessionID != "" {
			return sessionID
		}
	}
	if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			if sessionID := strings.TrimSpace(ginCtx.Request.Header.Get(ClaudeCodeSessionHeader)); sessionID != "" {
				return sessionID
			}
		}
	}
	return extractClaudeCodeSessionIDFromPayload(payload)
}

// ExtractClaudeCodeAgentID resolves the stable Claude Code agent identity from
// X-Claude-Code-Agent-Id. An empty result denotes the session's main/headerless
// lane and preserves compatibility with clients that do not send the header.
func ExtractClaudeCodeAgentID(ctx context.Context, headers http.Header) string {
	if headers != nil {
		if agentID := strings.TrimSpace(headers.Get(ClaudeCodeAgentHeader)); agentID != "" {
			return agentID
		}
	}
	if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			if agentID := strings.TrimSpace(ginCtx.Request.Header.Get(ClaudeCodeAgentHeader)); agentID != "" {
				return agentID
			}
		}
	}
	return ""
}

func extractClaudeCodeSessionIDFromPayload(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	userID := gjson.GetBytes(payload, "metadata.user_id").String()
	if userID == "" {
		return ""
	}
	if matches := claudeCodeSessionSuffixPattern.FindStringSubmatch(userID); len(matches) >= 2 {
		return matches[1]
	}
	if len(userID) > 0 && userID[0] == '{' {
		return strings.TrimSpace(gjson.Get(userID, "session_id").String())
	}
	return ""
}

// ClaudeCodePromptCache maps a Claude Code session to a stable upstream prompt_cache_key.
func ClaudeCodePromptCache(ctx context.Context, modelName string, payload []byte, headers http.Header) (CodexCache, bool, error) {
	sessionID := ExtractClaudeCodeSessionID(ctx, payload, headers)
	if sessionID == "" {
		return CodexCache{}, false, nil
	}
	parts := []string{
		"cli-proxy-api",
		"claude-code",
		"prompt-cache",
		strings.TrimSpace(modelName),
		strings.TrimSpace(sessionID),
	}
	if agentID := ExtractClaudeCodeAgentID(ctx, headers); agentID != "" {
		parts = append(parts, "agent", agentID)
	}
	name := strings.Join(parts, ":")
	return CodexCache{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()}, true, nil
}
