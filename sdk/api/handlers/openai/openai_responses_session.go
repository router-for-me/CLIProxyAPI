package openai

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

func responsesExplicitExecutionSessionID(req *http.Request, rawJSON []byte) string {
	if req != nil {
		if sessionID := strings.TrimSpace(req.Header.Get("Session_id")); sessionID != "" {
			return sessionID
		}
		if conversationID := strings.TrimSpace(req.Header.Get("Conversation_id")); conversationID != "" {
			return conversationID
		}
		if raw := strings.TrimSpace(req.Header.Get("X-Codex-Turn-Metadata")); raw != "" {
			if sessionID := strings.TrimSpace(gjson.Get(raw, "session_id").String()); sessionID != "" {
				return sessionID
			}
		}
	}

	if len(rawJSON) == 0 {
		return ""
	}
	for _, path := range []string{
		"prompt_cache_key",
		"metadata.prompt_cache_key",
		"metadata.conversation_id",
		"metadata.conversationId",
		"metadata.thread_id",
		"metadata.threadId",
		"metadata.session_id",
		"metadata.sessionId",
		"conversation_id",
		"conversationId",
		"thread_id",
		"threadId",
		"session_id",
		"sessionId",
	} {
		if sessionID := strings.TrimSpace(gjson.GetBytes(rawJSON, path).String()); sessionID != "" {
			return sessionID
		}
	}

	return ""
}
