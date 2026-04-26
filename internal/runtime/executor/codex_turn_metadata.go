package executor

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const (
	codexDefaultSandboxTag   = "none"
	codexDefaultThreadSource = "user"
)

type codexTurnMetadata struct {
	SessionID    string `json:"session_id,omitempty"`
	ThreadSource string `json:"thread_source,omitempty"`
	TurnID       string `json:"turn_id,omitempty"`
	Sandbox      string `json:"sandbox,omitempty"`
}

type codexTurnMetadataDefaults struct {
	sessionID    string
	threadSource string
	turnID       string
	sandbox      string
}

func codexEnsureTurnMetadataHeader(target http.Header, source http.Header, defaults codexTurnMetadataDefaults) {
	if target == nil {
		return
	}
	if value := firstNonEmptyHeaderValue(target, source, codexHeaderTurnMetadata); value != "" {
		target.Set(codexHeaderTurnMetadata, value)
		return
	}
	target.Set(codexHeaderTurnMetadata, codexBuildTurnMetadataHeader(
		defaults.sessionID,
		defaults.threadSource,
		defaults.turnID,
		defaults.sandbox,
	))
}

func codexDefaultTurnMetadataHeader(sessionID string) string {
	return codexBuildTurnMetadataHeader(
		sessionID,
		codexDefaultThreadSource,
		uuid.NewString(),
		codexDefaultSandboxTag,
	)
}

func codexBuildTurnMetadataHeader(sessionID string, threadSource string, turnID string, sandbox string) string {
	payload, err := json.Marshal(codexTurnMetadata{
		SessionID:    strings.TrimSpace(sessionID),
		ThreadSource: strings.TrimSpace(threadSource),
		TurnID:       strings.TrimSpace(turnID),
		Sandbox:      strings.TrimSpace(sandbox),
	})
	if err != nil {
		return `{"thread_source":"user","sandbox":"none"}`
	}
	return string(payload)
}

func codexTurnMetadataSessionID(target http.Header, source http.Header) string {
	raw := firstNonEmptyHeaderValue(target, source, codexHeaderTurnMetadata)
	if raw == "" {
		return ""
	}
	return strings.TrimSpace(gjson.Get(raw, "session_id").String())
}
