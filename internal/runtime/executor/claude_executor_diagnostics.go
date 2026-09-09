package executor

import (
	"bytes"
	"strings"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type claudeDiagnosticsRequestState struct {
	key      string
	sequence uint64
	promptID string
}

func injectClaudeDiagnostics(body []byte, auth *cliproxyauth.Auth, sessionID string) ([]byte, claudeDiagnosticsRequestState) {
	key, sequence, previousMessageID, _, promptID := helps.BeginClaudeContinuity(claudeDiagnosticsCredentialIdentity(auth), sessionID, false, "")
	return injectClaudeDiagnosticsWithState(body, key, sequence, previousMessageID, promptID)
}

func injectClaudeDiagnosticsWithState(body []byte, key string, sequence uint64, previousMessageID string, promptIDs ...string) ([]byte, claudeDiagnosticsRequestState) {
	if key == "" {
		return body, claudeDiagnosticsRequestState{}
	}
	promptID := ""
	if len(promptIDs) > 0 {
		promptID = promptIDs[0]
	}
	value := `{"previous_message_id":null}`
	if previousMessageID != "" {
		value = `{"previous_message_id":` + marshalJSONStringWithoutHTMLEscape(previousMessageID) + `}`
	}

	if diagnostics := gjson.GetBytes(body, "diagnostics"); diagnostics.Exists() {
		updated, errSet := sjson.SetRawBytes(body, "diagnostics", []byte(value))
		if errSet == nil {
			return updated, claudeDiagnosticsRequestState{key: key, sequence: sequence, promptID: promptID}
		}
	}
	if contextManagement := gjson.GetBytes(body, "context_management"); contextManagement.Exists() {
		start := contextManagement.Index
		insertAt := start + len(contextManagement.Raw)
		if start >= 0 && insertAt >= start && insertAt <= len(body) && bytes.Equal(body[start:insertAt], []byte(contextManagement.Raw)) {
			updated := make([]byte, 0, len(body)+len(value)+len(`,"diagnostics":`))
			updated = append(updated, body[:insertAt]...)
			updated = append(updated, `,"diagnostics":`...)
			updated = append(updated, value...)
			updated = append(updated, body[insertAt:]...)
			return updated, claudeDiagnosticsRequestState{key: key, sequence: sequence, promptID: promptID}
		}
	}
	updated, errSet := sjson.SetRawBytes(body, "diagnostics", []byte(value))
	if errSet != nil {
		return body, claudeDiagnosticsRequestState{}
	}
	return updated, claudeDiagnosticsRequestState{key: key, sequence: sequence, promptID: promptID}
}

func commitClaudeContinuity(state claudeDiagnosticsRequestState, messageID, requestID string) {
	helps.CommitClaudeContinuity(state.key, state.sequence, messageID, requestID, state.promptID)
}

func commitClaudeDiagnostics(state claudeDiagnosticsRequestState, messageID string) {
	commitClaudeContinuity(state, messageID, "")
}

func claudeDiagnosticsCredentialIdentity(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if id := strings.TrimSpace(auth.ID); id != "" {
		return "id:" + id
	}
	if index := strings.TrimSpace(auth.Index); index != "" {
		return "index:" + index
	}
	deviceIDs := claudeauth.NormalizeDeviceIDPool(claudeauth.ReadDeviceIDPool(&auth.Metadata))
	if len(deviceIDs) > 0 {
		return "device:" + deviceIDs[0]
	}
	if accountUUID := helps.ClaudeCredentialAccountUUID(auth); accountUUID != "" {
		return "account:" + accountUUID
	}
	return ""
}

func claudeMessageIDFromResponse(data []byte) string {
	return strings.TrimSpace(gjson.GetBytes(data, "id").String())
}

func observeClaudeStreamLine(line []byte, messageID *string, completed *bool) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	payload := bytes.TrimSpace(line[len("data:"):])
	if !gjson.ValidBytes(payload) {
		return
	}
	root := gjson.ParseBytes(payload)
	switch root.Get("type").String() {
	case "message_start":
		if id := strings.TrimSpace(root.Get("message.id").String()); id != "" {
			*messageID = id
		}
	case "message_stop":
		*completed = true
	}
}

func claudeMessageIDFromSSE(data []byte) string {
	var messageID string
	completed := false
	for _, line := range bytes.Split(data, []byte("\n")) {
		observeClaudeStreamLine(line, &messageID, &completed)
	}
	if !completed {
		return ""
	}
	return messageID
}
