package handlers

import (
	"bytes"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type openAIResponsesStreamLifecycle struct {
	responseID string
	createdAt  int64
	modelName  string
	terminal   bool
}

func (l *openAIResponsesStreamLifecycle) Observe(chunk []byte) {
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 {
		return
	}
	for _, line := range bytes.Split(trimmed, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		switch {
		case bytes.HasPrefix(line, []byte("event:")):
			eventType := strings.TrimSpace(string(bytes.TrimSpace(line[len("event:"):])))
			if openAIResponsesTerminalEvent(eventType) {
				l.terminal = true
			}
		case bytes.HasPrefix(line, []byte("data:")):
			l.observePayload(bytes.TrimSpace(line[len("data:"):]))
		default:
			l.observePayload(line)
		}
	}
}

func (l *openAIResponsesStreamLifecycle) observePayload(payload []byte) {
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	root := gjson.ParseBytes(payload)
	eventType := strings.TrimSpace(root.Get("type").String())
	if openAIResponsesTerminalEvent(eventType) {
		l.terminal = true
	}
	if responseID := strings.TrimSpace(root.Get("response.id").String()); responseID != "" {
		l.responseID = responseID
	}
	if createdAt := root.Get("response.created_at").Int(); createdAt > 0 {
		l.createdAt = createdAt
	}
	if modelName := strings.TrimSpace(root.Get("response.model").String()); modelName != "" {
		l.modelName = modelName
	}
}

func (l *openAIResponsesStreamLifecycle) NeedsSyntheticCompletion() bool {
	return !l.terminal
}

func (l *openAIResponsesStreamLifecycle) SyntheticCompletionChunk() []byte {
	responseID := l.ensureResponseID()
	createdAt := l.ensureCreatedAt()
	completed := []byte(`{"type":"response.completed","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
	completed, _ = sjson.SetBytes(completed, "response.id", responseID)
	completed, _ = sjson.SetBytes(completed, "response.created_at", createdAt)
	if strings.TrimSpace(l.modelName) != "" {
		completed, _ = sjson.SetBytes(completed, "response.model", l.modelName)
	}
	chunk := make([]byte, 0, len(completed)+36)
	chunk = append(chunk, "event: response.completed\n"...)
	chunk = append(chunk, "data: "...)
	chunk = append(chunk, completed...)
	chunk = append(chunk, '\n', '\n')
	return chunk
}

func (l *openAIResponsesStreamLifecycle) ensureResponseID() string {
	if l == nil {
		return "resp_synth_" + uuid.NewString()
	}
	if responseID := strings.TrimSpace(l.responseID); responseID != "" {
		return responseID
	}
	l.responseID = "resp_synth_" + uuid.NewString()
	return l.responseID
}

func (l *openAIResponsesStreamLifecycle) ensureCreatedAt() int64 {
	if l == nil {
		return time.Now().Unix()
	}
	if l.createdAt > 0 {
		return l.createdAt
	}
	l.createdAt = time.Now().Unix()
	return l.createdAt
}

func openAIResponsesTerminalEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "response.completed", "response.failed", "error":
		return true
	default:
		return false
	}
}

func isAuthAvailabilityError(err error) bool {
	if err == nil {
		return false
	}
	var authErr *coreauth.Error
	if !errors.As(err, &authErr) || authErr == nil {
		return false
	}
	switch strings.TrimSpace(authErr.Code) {
	case "auth_not_found", "auth_unavailable":
		return true
	default:
		return false
	}
}
