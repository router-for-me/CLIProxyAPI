package multiagentv2

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codexAppNamespace            = "codex_app"
	codexCreateThreadName        = "create_thread"
	codexSendMessageToThreadName = "send_message_to_thread"
	codexAppCreateThreadTool     = "codex_app__create_thread"
	codexAppSendMessageTool      = "codex_app__send_message_to_thread"
)

// RewriteCodexOrphanDelegationInput converts orphan Codex delegation outputs into
// standard user messages when orphan delegation compatibility is enabled.
func RewriteCodexOrphanDelegationInput(ctx context.Context, headers http.Header, payload []byte, enabled bool) []byte {
	return RewriteCodexOrphanDelegationInputWithPendingToolCallIDs(ctx, headers, payload, enabled, nil)
}

// RewriteCodexOrphanDelegationInputWithPendingToolCallIDs converts orphan Codex
// delegation outputs while preserving outputs paired with pending calls from the
// preceding response.
func RewriteCodexOrphanDelegationInputWithPendingToolCallIDs(ctx context.Context, headers http.Header, payload []byte, enabled bool, pendingToolCallIDs []string) []byte {
	if !enabled || len(payload) == 0 {
		return payload
	}

	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return payload
	}

	inputItems := input.Array()
	availableCalls := make(map[string]int)
	for _, item := range inputItems {
		if item.Get("type").String() == "function_call" {
			callID := item.Get("call_id").String()
			if strings.TrimSpace(callID) != "" {
				availableCalls[callID]++
			}
		}
	}
	pendingCalls := make(map[string]int, len(pendingToolCallIDs))
	for _, callID := range pendingToolCallIDs {
		if strings.TrimSpace(callID) != "" {
			pendingCalls[callID]++
		}
	}
	hasPreviousResponseID := strings.TrimSpace(gjson.GetBytes(payload, "previous_response_id").String()) != ""

	updated := payload
	for itemIndex, item := range inputItems {
		if item.Get("type").String() != "function_call_output" {
			continue
		}

		callID := item.Get("call_id").String()
		if strings.TrimSpace(callID) != "" && availableCalls[callID] > 0 {
			// Paired with a function call in the same request; consume and preserve.
			availableCalls[callID]--
			continue
		}
		if pendingCalls[callID] > 0 {
			// Paired with a pending function call from the preceding response.
			pendingCalls[callID]--
			continue
		}

		toolLabel, isTarget := matchCodexDelegationTool(item)
		if !isTarget {
			continue
		}
		if hasPreviousResponseID && strings.TrimSpace(callID) != "" {
			// Without response state, a non-empty call_id may still be a valid continuation.
			continue
		}

		// Orphan delegation output: downgrade to user message preserving exact output.
		itemPath := fmt.Sprintf("input.%d", itemIndex)
		userMessage := buildCodexOrphanUserMessage(toolLabel, item.Get("output"))
		var errSet error
		updated, errSet = sjson.SetRawBytes(updated, itemPath, userMessage)
		if errSet != nil {
			return payload
		}
	}

	return updated
}

func matchCodexDelegationTool(item gjson.Result) (string, bool) {
	if item.Get("namespace").String() != codexAppNamespace {
		return "", false
	}

	switch item.Get("name").String() {
	case codexCreateThreadName:
		return codexAppCreateThreadTool, true
	case codexSendMessageToThreadName:
		return codexAppSendMessageTool, true
	default:
		return "", false
	}
}

func buildCodexOrphanUserMessage(toolLabel string, output gjson.Result) []byte {
	outputText := ""
	if output.Exists() {
		if output.Type == gjson.String {
			outputText = output.String()
		} else {
			outputText = output.Raw
		}
	}

	fullText := fmt.Sprintf("Tool output from %s:\n%s", toolLabel, outputText)

	msg := []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}`)
	msg, _ = sjson.SetBytes(msg, "content.0.text", fullText)
	return msg
}
