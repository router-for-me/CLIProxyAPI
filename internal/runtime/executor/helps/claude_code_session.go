package helps

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	cliproxysession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
)

const (
	ClaudeCodeSessionHeader = "X-Claude-Code-Session-Id"
	ClaudeCodeAgentHeader   = "X-Claude-Code-Agent-Id"
	ClaudeCodeMainAgentID   = "main"
)

// ExtractClaudeCodeSessionID resolves a Claude Code session ID, preferring X-Claude-Code-Session-Id over payload metadata.
func ExtractClaudeCodeSessionID(ctx context.Context, payload []byte, headers http.Header) string {
	if sessionID := claudeCodeHeader(ctx, headers, ClaudeCodeSessionHeader); sessionID != "" {
		return sessionID
	}
	return extractClaudeCodeSessionIDFromPayload(payload)
}

// ExtractClaudeCodeAgentID resolves the Claude Code agent ID and uses a stable sentinel for the root agent.
func ExtractClaudeCodeAgentID(ctx context.Context, headers http.Header) string {
	if agentID := claudeCodeHeader(ctx, headers, ClaudeCodeAgentHeader); agentID != "" {
		return agentID
	}
	return ClaudeCodeMainAgentID
}

// ClaudeCodeExecutionScope returns the stable root-session and agent identity used by Codex execution state.
func ClaudeCodeExecutionScope(ctx context.Context, payload []byte, headers http.Header) (string, bool) {
	sessionID := ExtractClaudeCodeSessionID(ctx, payload, headers)
	if sessionID == "" {
		return "", false
	}
	return "claude:" + sessionID + ":agent:" + ExtractClaudeCodeAgentID(ctx, headers), true
}

func claudeCodeHeader(ctx context.Context, headers http.Header, name string) string {
	if value := headerValueCaseInsensitive(headers, name); value != "" {
		return value
	}
	if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			return headerValueCaseInsensitive(ginCtx.Request.Header, name)
		}
	}
	return ""
}

func headerValueCaseInsensitive(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	if value := strings.TrimSpace(headers.Get(name)); value != "" {
		return value
	}
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}

// extractClaudeCodeSessionIDFromPayload delegates to the shared parser so the
// JSON and legacy metadata.user_id formats have a single implementation.
func extractClaudeCodeSessionIDFromPayload(payload []byte) string {
	return cliproxysession.ClaudeMetadataSessionID(payload)
}

// ClaudeCodePromptCache derives a deterministic upstream prompt_cache_key for one Claude Code agent.
func ClaudeCodePromptCache(ctx context.Context, modelName string, payload []byte, headers http.Header) (CodexCache, bool, error) {
	modelName = strings.TrimSpace(modelName)
	executionScope, ok := ClaudeCodeExecutionScope(ctx, payload, headers)
	if modelName == "" || !ok {
		return CodexCache{}, false, nil
	}
	identity := strings.Join([]string{"cli-proxy-api:codex:claude-code", modelName, executionScope}, "\x00")
	return CodexCache{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(identity)).String()}, true, nil
}
