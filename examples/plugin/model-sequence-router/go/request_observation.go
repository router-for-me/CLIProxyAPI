package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type requestObservation struct {
	SystemFingerprint  string
	ToolsFingerprint   string
	HistoryFingerprint string
	HistoryItems       []string
	InputKind          string
	HasToolResult      bool
	PreviousResponseID string
	HasConversationID  bool
	HasContainer       bool
	ThinkingSignatures int
	EncryptedReasoning int
}

func inspectRequest(body []byte, salt []byte) requestObservation {
	observation := requestObservation{InputKind: "unknown"}
	if len(bytes.TrimSpace(body)) == 0 {
		return observation
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var root map[string]any
	if errDecode := decoder.Decode(&root); errDecode != nil {
		return observation
	}
	system, hasSystem := root["system"]
	if !hasSystem {
		system, hasSystem = root["instructions"]
	}
	if hasSystem {
		observation.SystemFingerprint = fingerprintJSON(system, salt)
	}
	if tools, ok := root["tools"]; ok {
		observation.ToolsFingerprint = fingerprintJSON(tools, salt)
	}
	history := firstHistory(root, "messages", "input", "contents")
	if len(history) > 0 {
		observation.HistoryItems = make([]string, 0, len(history))
		for _, item := range history {
			observation.HistoryItems = append(observation.HistoryItems, fingerprintJSON(item, salt))
		}
		observation.HistoryFingerprint = fingerprintStrings(observation.HistoryItems, salt)
		last := history[len(history)-1]
		observation.HasToolResult = containsToolResult(last)
		switch {
		case observation.HasToolResult:
			observation.InputKind = "tool_result"
		case containsRole(last, "user"):
			observation.InputKind = "user"
		default:
			observation.InputKind = "history"
		}
	}
	observation.PreviousResponseID = stringJSONValue(root["previous_response_id"])
	observation.HasConversationID = nonEmptyJSONValue(root["conversation_id"]) || nonEmptyJSONValue(root["conversation"])
	observation.HasContainer = nonEmptyJSONValue(root["container_id"]) || nonEmptyJSONValue(root["container"])
	walkJSON(root, func(node map[string]any) {
		typeName, _ := node["type"].(string)
		if (typeName == "thinking" || typeName == "redacted_thinking") && nonEmptyJSONValue(node["signature"]) {
			observation.ThinkingSignatures++
		}
		if typeName == "reasoning" && nonEmptyJSONValue(node["encrypted_content"]) {
			observation.EncryptedReasoning++
		}
	})
	return observation
}

// firstHistory selects the first supported history field and folds the shorthand
// input encodings into the collection shape carried by array input. Scalar input
// becomes one user message, and a single turn sent as an object becomes that one
// turn, so every encoding of one conversation state observes the same history.
func firstHistory(root map[string]any, keys ...string) []any {
	for _, key := range keys {
		value, exists := root[key]
		if !exists {
			continue
		}
		switch typed := value.(type) {
		case []any:
			return typed
		case string:
			if key != "input" || strings.TrimSpace(typed) == "" {
				continue
			}
			return []any{map[string]any{"role": "user", "content": typed}}
		case map[string]any:
			if key != "input" || len(typed) == 0 {
				continue
			}
			return []any{typed}
		default:
			continue
		}
	}
	return nil
}

// hasOpaqueContinuation reports whether the request continues a conversation
// through an identifier the provider resolves rather than through the transcript
// the request carries.
func (o requestObservation) hasOpaqueContinuation() bool {
	return o.PreviousResponseID != "" || o.HasConversationID || o.HasContainer
}

func containsToolResult(value any) bool {
	found := false
	walkJSON(value, func(node map[string]any) {
		if found {
			return
		}
		if role, _ := node["role"].(string); strings.EqualFold(strings.TrimSpace(role), "tool") {
			found = true
			return
		}
		typeName, _ := node["type"].(string)
		switch strings.ToLower(strings.TrimSpace(typeName)) {
		case "tool_result", "function_call_output", "custom_tool_call_output":
			found = true
		}
	})
	return found
}

func containsRole(value any, want string) bool {
	found := false
	walkJSON(value, func(node map[string]any) {
		if role, _ := node["role"].(string); strings.EqualFold(strings.TrimSpace(role), want) {
			found = true
		}
	})
	return found
}

func walkJSON(value any, visit func(map[string]any)) {
	switch typed := value.(type) {
	case map[string]any:
		visit(typed)
		for _, child := range typed {
			walkJSON(child, visit)
		}
	case []any:
		for _, child := range typed {
			walkJSON(child, visit)
		}
	}
}

func nonEmptyJSONValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

// stringJSONValue reads one decoded JSON value as trimmed text. A value of any
// other shape reads as absent.
func stringJSONValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func fingerprintJSON(value any, salt []byte) string {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return ""
	}
	return saltedHash(raw, salt)
}

func fingerprintStrings(values []string, salt []byte) string {
	return saltedHash([]byte(strings.Join(values, "\x00")), salt)
}

func saltedHash(value, salt []byte) string {
	hash := sha256.New()
	_, _ = hash.Write(salt)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(value)
	return hex.EncodeToString(hash.Sum(nil)[:8])
}

func shortValueHash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:6])
}

func newFingerprintSalt() []byte {
	salt := make([]byte, 32)
	if _, errRead := rand.Read(salt); errRead == nil {
		return salt
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	return sum[:]
}
