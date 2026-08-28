package helps

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

const (
	ClaudeCodeSessionHeader = "X-Claude-Code-Session-Id"
	ClaudeCodeAgentHeader   = "X-Claude-Code-Agent-Id"
	ClaudeCodeMainAgentID   = "main"
)

var claudeCodeSessionSuffixPattern = regexp.MustCompile(`_session_([a-f0-9-]+)$`)

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
// This scope is always agent-qualified: it keys reasoning-replay state, which must never be
// shared between sibling agents.
func ClaudeCodeExecutionScope(ctx context.Context, payload []byte, headers http.Header) (string, bool) {
	return claudeCodeScope(ctx, payload, headers, true)
}

// claudeCodeScope builds the Claude Code session identity, optionally qualified by agent.
func claudeCodeScope(ctx context.Context, payload []byte, headers http.Header, perAgent bool) (string, bool) {
	sessionID := ExtractClaudeCodeSessionID(ctx, payload, headers)
	if sessionID == "" {
		return "", false
	}
	if !perAgent {
		return "claude:" + sessionID, true
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

// HeaderValueCaseInsensitive returns the first non-empty header value matching name case-insensitively.
func HeaderValueCaseInsensitive(headers http.Header, name string) string {
	return headerValueCaseInsensitive(headers, name)
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

// ClaudeCodePromptCache derives a deterministic upstream prompt_cache_key for a Claude Code request.
//
// The key is agent-scoped by default. Set codex-cache-key-per-agent: false in the config to key
// on the session alone, so a subagent fan-out shares one upstream partition instead of opening a
// fresh one per agent. The optional cfg is variadic so existing callers without a config in scope
// keep the default; a nil or absent config means agent-scoped.
func ClaudeCodePromptCache(ctx context.Context, modelName string, payload []byte, headers http.Header, configs ...*config.Config) (CodexCache, bool, error) {
	var cfg *config.Config
	if len(configs) > 0 {
		cfg = configs[0]
	}
	return claudeCodeCacheIdentity(ctx, modelName, payload, headers, cfg.CodexCacheKeyPerAgentEnabled())
}

// ClaudeCodeConversationCache derives the same deterministic identity as ClaudeCodePromptCache but
// is always agent-scoped, ignoring codex-cache-key-per-agent.
//
// Callers use this value as an upstream conversation ID rather than as a cache key: sibling agents
// of one Claude Code session must never land in the same upstream conversation, because that mixes
// their conversation state. Only prompt-cache sharing is configurable; conversation identity is not.
func ClaudeCodeConversationCache(ctx context.Context, modelName string, payload []byte, headers http.Header) (CodexCache, bool, error) {
	return claudeCodeCacheIdentity(ctx, modelName, payload, headers, true)
}

// claudeCodeCacheIdentity hashes the model and the Claude Code scope into a stable UUID.
func claudeCodeCacheIdentity(ctx context.Context, modelName string, payload []byte, headers http.Header, perAgent bool) (CodexCache, bool, error) {
	modelName = strings.TrimSpace(modelName)
	executionScope, ok := claudeCodeScope(ctx, payload, headers, perAgent)
	if modelName == "" || !ok {
		return CodexCache{}, false, nil
	}
	identity := strings.Join([]string{"cli-proxy-api:codex:claude-code", modelName, executionScope}, "\x00")
	return CodexCache{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(identity)).String()}, true, nil
}
