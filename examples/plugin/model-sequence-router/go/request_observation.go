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
	HasPreviousID      bool
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
	history := firstArray(root, "messages", "input", "contents")
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
	observation.HasPreviousID = nonEmptyJSONValue(root["previous_response_id"])
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

func firstArray(root map[string]any, keys ...string) []any {
	for _, key := range keys {
		if values, ok := root[key].([]any); ok {
			return values
		}
	}
	return nil
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
