package helps

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Capsules minted by an earlier fork build are still recognized, so that their ciphertext is
// deleted instead of being forwarded to an upstream that cannot decrypt it, but they can no longer
// be opened: the per-lane keys (v2) and the derived CPA-wide key (v3) were both dropped in favour
// of the single upstream format.
const (
	legacyCompatCompactionCapsulePrefix   = "cpa-compat-compact-v2:"
	legacyCompatCompactionCapsulePrefixV3 = "cpa-compat-compact-v3:"
)

// IsCPACompactionCapsule reports whether encrypted content was sealed by CPA itself. CPA uses one
// capsule format for every lane (the global format defined in antigravity_compaction.go), so any
// lane and any CPA instance can open it. Legacy prefixes are still recognized so that their
// ciphertext is never forwarded, even though they can no longer be opened.
func IsCPACompactionCapsule(encryptedContent string) bool {
	return strings.HasPrefix(encryptedContent, antigravityCompactionCapsulePrefix) ||
		strings.HasPrefix(encryptedContent, legacyCompatCompactionCapsulePrefix) ||
		strings.HasPrefix(encryptedContent, legacyCompatCompactionCapsulePrefixV3)
}

// HasResponsesCompactionTrigger checks whether input contains a compaction_trigger item.
func HasResponsesCompactionTrigger(payload []byte) bool {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if item.Get("type").String() == "compaction_trigger" {
			return true
		}
	}
	return false
}

// HasResponsesCompactionItem checks whether input contains a compaction item.
func HasResponsesCompactionItem(payload []byte) bool {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		if item.Get("type").String() == "compaction" {
			return true
		}
	}
	return false
}

// PrepareResponsesCompactionSummaryPayload prepares a payload for non-stream summary generation.
func PrepareResponsesCompactionSummaryPayload(payload []byte, modelName string) []byte {
	out := payload
	summaryPrompt := `{"type":"message","role":"user","content":[{"type":"input_text","text":"Please provide a concise and comprehensive summary of the preceding conversation and task progress so far, including user goals, key findings, actions taken, and current status, so that work can continue smoothly."}]}`

	input := gjson.GetBytes(out, "input")
	if input.IsArray() {
		var filtered []string
		for _, item := range input.Array() {
			itemType := item.Get("type").String()
			if itemType == "compaction_trigger" {
				continue
			}
			filtered = append(filtered, item.Raw)
		}
		filtered = append(filtered, summaryPrompt)
		newInput := "[" + strings.Join(filtered, ",") + "]"
		out, _ = sjson.SetRawBytes(out, "input", []byte(newInput))
	} else if input.Type == gjson.String && input.String() != "" {
		userMsg := []byte(`{"type":"message","role":"user","content":[{"type":"input_text"}]}`)
		userMsg, _ = sjson.SetBytes(userMsg, "content.0.text", input.String())
		newInput := "[" + string(userMsg) + "," + summaryPrompt + "]"
		out, _ = sjson.SetRawBytes(out, "input", []byte(newInput))
	} else {
		newInput := "[" + summaryPrompt + "]"
		out, _ = sjson.SetRawBytes(out, "input", []byte(newInput))
	}

	// Remove fields not needed or incompatible with summary generation
	for _, field := range []string{
		"stream",
		"tools",
		"tool_choice",
		"previous_response_id",
		"parallel_tool_calls",
		"additional_tools",
		"truncation",
		"metadata",
	} {
		out, _ = sjson.DeleteBytes(out, field)
	}

	out, _ = sjson.SetBytes(out, "stream", false)
	return out
}

// ExtractResponsesCompactionSummaryText extracts summary text from a supported response format.
func ExtractResponsesCompactionSummaryText(respPayload []byte) (string, error) {
	// 1. OpenAI Responses format: iterate through output array and skip reasoning
	output := gjson.GetBytes(respPayload, "output")
	if output.IsArray() {
		var textParts []string
		for _, item := range output.Array() {
			if item.Get("type").String() == "message" {
				content := item.Get("content")
				if content.IsArray() {
					for _, part := range content.Array() {
						if part.Get("type").String() == "output_text" {
							if t := part.Get("text").String(); t != "" {
								textParts = append(textParts, t)
							}
						}
					}
				} else if content.Type == gjson.String && content.String() != "" {
					textParts = append(textParts, content.String())
				}
			}
		}
		if len(textParts) > 0 {
			return strings.Join(textParts, "\n"), nil
		}
	}

	// 2. Antigravity Gemini format: response.candidates.0.content.parts or candidates.0.content.parts
	candidates := gjson.GetBytes(respPayload, "response.candidates")
	if !candidates.Exists() {
		candidates = gjson.GetBytes(respPayload, "candidates")
	}
	if candidates.IsArray() && len(candidates.Array()) > 0 {
		var textParts []string
		parts := candidates.Array()[0].Get("content.parts")
		if parts.IsArray() {
			for _, part := range parts.Array() {
				if part.Get("thought").Bool() {
					continue
				}
				if t := part.Get("text").String(); t != "" {
					textParts = append(textParts, t)
				}
			}
		}
		if len(textParts) > 0 {
			return strings.Join(textParts, "\n"), nil
		}
	}

	// 3. Claude format: content array
	content := gjson.GetBytes(respPayload, "content")
	if content.IsArray() {
		var textParts []string
		for _, block := range content.Array() {
			if block.Get("type").String() == "text" {
				if t := block.Get("text").String(); t != "" {
					textParts = append(textParts, t)
				}
			}
		}
		if len(textParts) > 0 {
			return strings.Join(textParts, "\n"), nil
		}
	}

	// 4. OpenAI Chat format: choices.0.message.content
	if text := gjson.GetBytes(respPayload, "choices.0.message.content").String(); text != "" {
		return text, nil
	}

	return "", fmt.Errorf("no summary text found in upstream response")
}

// BuildResponsesCompactionResponse creates a JSON response for non-stream compaction.
func BuildResponsesCompactionResponse(modelName, capsule, idPrefix string, inputTokens, outputTokens, totalTokens int) []byte {
	now := time.Now().Unix()
	responseID := fmt.Sprintf("resp_%s_%d", idPrefix, time.Now().UnixNano())
	itemID := fmt.Sprintf("cmp_%s_%d", idPrefix, time.Now().UnixNano())

	itemJSON := []byte(`{"type":"compaction","status":"completed"}`)
	itemJSON, _ = sjson.SetBytes(itemJSON, "id", itemID)
	itemJSON, _ = sjson.SetBytes(itemJSON, "encrypted_content", capsule)

	usageJSON := []byte(`{"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}`)
	usageJSON, _ = sjson.SetBytes(usageJSON, "input_tokens", inputTokens)
	usageJSON, _ = sjson.SetBytes(usageJSON, "output_tokens", outputTokens)
	usageJSON, _ = sjson.SetBytes(usageJSON, "total_tokens", totalTokens)

	respJSON := []byte(`{"object":"response.compaction","status":"completed"}`)
	respJSON, _ = sjson.SetBytes(respJSON, "id", responseID)
	respJSON, _ = sjson.SetBytes(respJSON, "created_at", now)
	respJSON, _ = sjson.SetBytes(respJSON, "model", modelName)
	respJSON, _ = sjson.SetRawBytes(respJSON, "output", []byte("["+string(itemJSON)+"]"))
	respJSON, _ = sjson.SetRawBytes(respJSON, "usage", usageJSON)

	return respJSON
}

// BuildResponsesCompactionTriggerResponse creates a normal Responses response for a trigger.
func BuildResponsesCompactionTriggerResponse(modelName, capsule, idPrefix string, inputTokens, outputTokens, totalTokens int) []byte {
	now := time.Now().Unix()
	responseID := fmt.Sprintf("resp_%s_%d", idPrefix, time.Now().UnixNano())
	itemID := fmt.Sprintf("cmp_%s_%d", idPrefix, time.Now().UnixNano())

	itemJSON := []byte(`{"type":"compaction","status":"completed"}`)
	itemJSON, _ = sjson.SetBytes(itemJSON, "id", itemID)
	itemJSON, _ = sjson.SetBytes(itemJSON, "encrypted_content", capsule)

	usageJSON := []byte(`{"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}`)
	usageJSON, _ = sjson.SetBytes(usageJSON, "input_tokens", inputTokens)
	usageJSON, _ = sjson.SetBytes(usageJSON, "output_tokens", outputTokens)
	usageJSON, _ = sjson.SetBytes(usageJSON, "total_tokens", totalTokens)

	respJSON := []byte(`{"object":"response","status":"completed","background":false,"error":null}`)
	respJSON, _ = sjson.SetBytes(respJSON, "id", responseID)
	respJSON, _ = sjson.SetBytes(respJSON, "created_at", now)
	respJSON, _ = sjson.SetBytes(respJSON, "completed_at", now)
	respJSON, _ = sjson.SetBytes(respJSON, "model", modelName)
	respJSON, _ = sjson.SetRawBytes(respJSON, "output", []byte("["+string(itemJSON)+"]"))
	respJSON, _ = sjson.SetRawBytes(respJSON, "usage", usageJSON)

	return respJSON
}

// BuildResponsesCompactionStreamChunks creates SSE frames for a compaction stream response.
func BuildResponsesCompactionStreamChunks(modelName, capsule, idPrefix string, inputTokens, outputTokens, totalTokens int) [][]byte {
	now := time.Now().Unix()
	responseID := fmt.Sprintf("resp_%s_%d", idPrefix, time.Now().UnixNano())
	itemID := fmt.Sprintf("cmp_%s_%d", idPrefix, time.Now().UnixNano())

	itemInProgress := []byte(`{"type":"compaction","status":"in_progress"}`)
	itemInProgress, _ = sjson.SetBytes(itemInProgress, "id", itemID)
	itemInProgress, _ = sjson.SetBytes(itemInProgress, "encrypted_content", capsule)

	itemCompleted := []byte(`{"type":"compaction","status":"completed"}`)
	itemCompleted, _ = sjson.SetBytes(itemCompleted, "id", itemID)
	itemCompleted, _ = sjson.SetBytes(itemCompleted, "encrypted_content", capsule)

	usageNode := []byte(`{"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}`)
	usageNode, _ = sjson.SetBytes(usageNode, "input_tokens", inputTokens)
	usageNode, _ = sjson.SetBytes(usageNode, "output_tokens", outputTokens)
	usageNode, _ = sjson.SetBytes(usageNode, "total_tokens", totalTokens)

	createdResponse := []byte(`{"object":"response","status":"in_progress","background":false,"error":null,"output":[]}`)
	createdResponse, _ = sjson.SetBytes(createdResponse, "id", responseID)
	createdResponse, _ = sjson.SetBytes(createdResponse, "created_at", now)
	createdResponse, _ = sjson.SetBytes(createdResponse, "model", modelName)

	inProgressResponse := createdResponse

	completedResponse := []byte(`{"object":"response","status":"completed","background":false,"error":null}`)
	completedResponse, _ = sjson.SetBytes(completedResponse, "id", responseID)
	completedResponse, _ = sjson.SetBytes(completedResponse, "created_at", now)
	completedResponse, _ = sjson.SetBytes(completedResponse, "completed_at", now)
	completedResponse, _ = sjson.SetBytes(completedResponse, "model", modelName)
	completedResponse, _ = sjson.SetRawBytes(completedResponse, "output", []byte("["+string(itemCompleted)+"]"))
	completedResponse, _ = sjson.SetRawBytes(completedResponse, "usage", usageNode)

	createdPayload := []byte(`{"type":"response.created","sequence_number":0}`)
	createdPayload, _ = sjson.SetRawBytes(createdPayload, "response", createdResponse)

	inProgressPayload := []byte(`{"type":"response.in_progress","sequence_number":1}`)
	inProgressPayload, _ = sjson.SetRawBytes(inProgressPayload, "response", inProgressResponse)

	addedPayload := []byte(`{"type":"response.output_item.added","sequence_number":2,"output_index":0}`)
	addedPayload, _ = sjson.SetRawBytes(addedPayload, "item", itemInProgress)

	donePayload := []byte(`{"type":"response.output_item.done","sequence_number":3,"output_index":0}`)
	donePayload, _ = sjson.SetRawBytes(donePayload, "item", itemCompleted)

	completedPayload := []byte(`{"type":"response.completed","sequence_number":4}`)
	completedPayload, _ = sjson.SetRawBytes(completedPayload, "response", completedResponse)

	frames := [][]byte{
		buildSSEFrame("response.created", createdPayload),
		buildSSEFrame("response.in_progress", inProgressPayload),
		buildSSEFrame("response.output_item.added", addedPayload),
		buildSSEFrame("response.output_item.done", donePayload),
		buildSSEFrame("response.completed", completedPayload),
	}
	return frames
}

// NormalizeCPACompactionItems rewrites compaction items in a request input so that
// CPA-sealed capsules are never forwarded to an upstream that cannot decrypt them.
// Forwarding our own ciphertext makes the upstream reject the whole request, and
// clients treat that rejection as non-retryable.
//
// A capsule that can be opened is inlined as a developer context message so the summary survives
// the request. A capsule that cannot be opened is dropped with a warning. Items that are not
// CPA-sealed are preserved because they may belong to the target upstream, which can read them.
//
// An unreadable capsule only affects context completeness, so it never fails the request.
func NormalizeCPACompactionItems(ctx context.Context, payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}
	// Fast path: this runs on every request, and almost none of them carry a
	// compaction item. A byte scan is far cheaper than a JSON walk.
	if !bytes.Contains(payload, []byte(`"compaction"`)) {
		return payload
	}
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return payload
	}

	items := make([]string, 0, len(input.Array()))
	changed := false
	for _, item := range input.Array() {
		if item.Get("type").String() != "compaction" {
			items = append(items, item.Raw)
			continue
		}

		encryptedContent := item.Get("encrypted_content").String()
		if !IsCPACompactionCapsule(encryptedContent) {
			// Not CPA-sealed: it may be the upstream's own capsule, which the upstream can read.
			items = append(items, item.Raw)
			continue
		}

		summary, errUnseal := UnsealAntigravityCompaction(encryptedContent)
		if errUnseal != nil {
			LogWithRequestID(ctx).Warnf("responses compaction: unreadable capsule dropped before upstream")
			changed = true
			continue
		}
		items = append(items, string(responsesCompactionContextMessage(summary)))
		changed = true
	}

	if !changed {
		return payload
	}
	updated, errSet := sjson.SetRawBytes(payload, "input", []byte("["+strings.Join(items, ",")+"]"))
	if errSet != nil {
		LogWithRequestID(ctx).Warnf("responses compaction: could not rewrite input: %v", errSet)
		return payload
	}
	return updated
}

// responsesCompactionContextMessage builds the developer message that carries an
// unsealed compaction summary as plain context.
func responsesCompactionContextMessage(summary string) []byte {
	message := []byte(`{"type":"message","role":"developer","content":[{"type":"input_text"}]}`)
	message, _ = sjson.SetBytes(message, "content.0.text", "Context summary from previous turns:\n"+summary)
	return message
}
