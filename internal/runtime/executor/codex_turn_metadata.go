package executor

import (
	"net/http"
	"strconv"
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
	sessionID = strings.TrimSpace(sessionID)
	threadSource = strings.TrimSpace(threadSource)
	turnID = strings.TrimSpace(turnID)
	sandbox = strings.TrimSpace(sandbox)

	var builder strings.Builder
	builder.Grow(len(sessionID) + len(threadSource) + len(turnID) + len(sandbox) + 64)
	builder.WriteByte('{')
	first := true
	appendQuotedJSONField := func(name string, value string) {
		if value == "" {
			return
		}
		if !first {
			builder.WriteByte(',')
		}
		first = false
		builder.WriteByte('"')
		builder.WriteString(name)
		builder.WriteString(`":`)
		builder.WriteString(strconv.Quote(value))
	}

	appendQuotedJSONField("session_id", sessionID)
	appendQuotedJSONField("thread_source", threadSource)
	appendQuotedJSONField("turn_id", turnID)
	appendQuotedJSONField("sandbox", sandbox)
	builder.WriteByte('}')
	return builder.String()
}

func codexTurnMetadataSessionID(target http.Header, source http.Header) string {
	raw := firstNonEmptyHeaderValue(target, source, codexHeaderTurnMetadata)
	if raw == "" {
		return ""
	}
	return strings.TrimSpace(gjson.Get(raw, "session_id").String())
}
