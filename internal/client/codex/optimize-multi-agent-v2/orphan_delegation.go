package multiagentv2

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codexAppNamespace            = "codex_app"
	codexCreateThreadName        = "create_thread"
	codexSendMessageToThreadName = "send_message_to_thread"
	codexAppCreateThreadTool     = "codex_app__create_thread"
	codexAppSendMessageTool      = "codex_app__send_message_to_thread"
	codexOpenAISubagentHeader    = "X-Openai-Subagent"
	codexCollabSpawnSubagent     = "collab_spawn"
)

// RewriteCodexOrphanDelegationInput converts orphan Codex delegation outputs into
// standard user messages when orphan delegation compatibility is enabled and the
// request carries the X-Openai-Subagent: collab_spawn header.
func RewriteCodexOrphanDelegationInput(ctx context.Context, headers http.Header, payload []byte, enabled bool) []byte {
	if !enabled || len(payload) == 0 || !isCodexCollabSpawnSubagent(ctx, headers) {
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

		toolLabel, isTarget := matchCodexDelegationTool(item)
		if !isTarget {
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

func codexSubagentHeader(ctx context.Context, headers http.Header) string {
	if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			return headerValueCaseInsensitive(ginCtx.Request.Header, codexOpenAISubagentHeader)
		}
	}
	return headerValueCaseInsensitive(headers, codexOpenAISubagentHeader)
}

func isCodexCollabSpawnSubagent(ctx context.Context, headers http.Header) bool {
	return strings.EqualFold(codexSubagentHeader(ctx, headers), codexCollabSpawnSubagent)
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
