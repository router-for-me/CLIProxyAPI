package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const XAIClaudeRepeatedToolMessage = "The upstream model repeated the immediately completed tool call with unchanged arguments. The gateway blocked the duplicate execution."

type XAIClaudeToolRepeatGuard struct {
	fingerprints map[string]struct{}
	blockedIDs   map[string]struct{}
}

const (
	XAIClaudeToolResultSuccessPrefix = "[tool_result status=success] The tool call completed successfully. Treat this result as consumed. Do not repeat the same tool call with unchanged arguments unless the result explicitly asks for a retry.\n"
	XAIClaudeToolResultErrorPrefix   = "[tool_result status=error] The tool call failed. Use the error details below to change the approach or arguments. Do not repeat the same tool call unchanged.\n"
)

// annotateXAIClaudeToolResults preserves Anthropic tool_result status on the
// Responses wire format, which has no is_error field. This is deliberately an
// xAI-only compatibility shim and can be removed when the upstream accepts an
// explicit tool output status.
func AnnotateXAIClaudeToolResults(body, source []byte, from sdktranslator.Format) []byte {
	if !strings.EqualFold(strings.TrimSpace(from.String()), sdktranslator.FormatClaude.String()) {
		return body
	}

	statuses := xaiClaudeToolResultStatuses(source)
	if len(statuses) == 0 {
		return body
	}

	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}
	for index, item := range input.Array() {
		if item.Get("type").String() != "function_call_output" {
			continue
		}
		isError, ok := statuses[strings.TrimSpace(item.Get("call_id").String())]
		if !ok {
			continue
		}
		body = annotateXAIClaudeToolOutputAt(body, index, item.Get("output"), isError)
	}
	return body
}

func xaiClaudeToolResultStatuses(source []byte) map[string]bool {
	statuses := make(map[string]bool)
	gjson.GetBytes(source, "messages").ForEach(func(_, message gjson.Result) bool {
		content := message.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() != "tool_result" {
				return true
			}
			callID := strings.TrimSpace(part.Get("tool_use_id").String())
			if callID != "" {
				statuses[callID] = part.Get("is_error").Bool()
			}
			return true
		})
		return true
	})
	return statuses
}

func annotateXAIClaudeToolOutputAt(body []byte, index int, output gjson.Result, isError bool) []byte {
	prefix := XAIClaudeToolResultSuccessPrefix
	if isError {
		prefix = XAIClaudeToolResultErrorPrefix
	}
	path := fmt.Sprintf("input.%d.output", index)

	if output.IsArray() {
		items := output.Array()
		if len(items) > 0 && xaiClaudeToolResultAlreadyAnnotated(items[0].Get("text").String()) {
			return body
		}
		marker := []byte(`{"type":"input_text","text":""}`)
		marker, _ = sjson.SetBytes(marker, "text", strings.TrimSuffix(prefix, "\n"))
		rawItems := make([]string, 0, len(items)+1)
		rawItems = append(rawItems, string(marker))
		for _, item := range items {
			rawItems = append(rawItems, item.Raw)
		}
		updated, err := sjson.SetRawBytes(body, path, []byte("["+strings.Join(rawItems, ",")+"]"))
		if err == nil {
			return updated
		}
		return body
	}

	text := output.String()
	if xaiClaudeToolResultAlreadyAnnotated(text) {
		return body
	}
	updated, err := sjson.SetBytes(body, path, prefix+text)
	if err != nil {
		return body
	}
	return updated
}

func xaiClaudeToolResultAlreadyAnnotated(text string) bool {
	return strings.HasPrefix(text, XAIClaudeToolResultSuccessPrefix) ||
		strings.HasPrefix(text, XAIClaudeToolResultErrorPrefix) ||
		strings.HasPrefix(text, strings.TrimSuffix(XAIClaudeToolResultSuccessPrefix, "\n")) ||
		strings.HasPrefix(text, strings.TrimSuffix(XAIClaudeToolResultErrorPrefix, "\n"))
}

func NewXAIClaudeToolRepeatGuard(source, body []byte, from sdktranslator.Format) *XAIClaudeToolRepeatGuard {
	if !strings.EqualFold(strings.TrimSpace(from.String()), sdktranslator.FormatClaude.String()) {
		return nil
	}
	fingerprints := xaiClaudeCompletedToolFingerprints(source)
	if len(fingerprints) == 0 {
		fingerprints = xaiResponsesCompletedToolFingerprints(body)
	}
	if len(fingerprints) == 0 {
		return nil
	}
	return &XAIClaudeToolRepeatGuard{
		fingerprints: fingerprints,
		blockedIDs:   make(map[string]struct{}),
	}
}

// xaiClaudeCompletedToolFingerprints derives the guard from the unmodified
// downstream request. The translated xAI body is not authoritative here: the
// reasoning replay and namespace normalization stages may reorder or omit the
// matching function_call while retaining its output.
func xaiClaudeCompletedToolFingerprints(source []byte) map[string]struct{} {
	messages := gjson.GetBytes(source, "messages")
	if !messages.IsArray() {
		return nil
	}
	items := messages.Array()
	if len(items) == 0 {
		return nil
	}
	last := items[len(items)-1]
	if last.Get("role").String() != "user" || !last.Get("content").IsArray() {
		return nil
	}
	completedIDs := make(map[string]struct{})
	last.Get("content").ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() == "tool_result" {
			if callID := strings.TrimSpace(part.Get("tool_use_id").String()); callID != "" {
				completedIDs[callID] = struct{}{}
			}
		}
		return true
	})
	if len(completedIDs) == 0 {
		return nil
	}

	fingerprints := make(map[string]struct{})
	for index := len(items) - 2; index >= 0 && len(completedIDs) > 0; index-- {
		message := items[index]
		if message.Get("role").String() != "assistant" || !message.Get("content").IsArray() {
			continue
		}
		message.Get("content").ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() != "tool_use" {
				return true
			}
			callID := strings.TrimSpace(part.Get("id").String())
			if _, completed := completedIDs[callID]; !completed {
				return true
			}
			fingerprint := xaiToolCallFingerprint(part.Get("name").String(), part.Get("input").Raw)
			if fingerprint != "" {
				fingerprints[fingerprint] = struct{}{}
			}
			delete(completedIDs, callID)
			return true
		})
	}
	return fingerprints
}

func xaiResponsesCompletedToolFingerprints(body []byte) map[string]struct{} {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return nil
	}
	items := input.Array()
	lastOutput := -1
	for index := len(items) - 1; index >= 0; index-- {
		if items[index].Get("type").String() == "function_call_output" {
			lastOutput = index
			break
		}
	}
	if lastOutput < 0 {
		return nil
	}

	calls := make(map[string]gjson.Result)
	for index := 0; index < lastOutput; index++ {
		if items[index].Get("type").String() == "function_call" {
			calls[strings.TrimSpace(items[index].Get("call_id").String())] = items[index]
		}
	}
	callID := strings.TrimSpace(items[lastOutput].Get("call_id").String())
	call, ok := calls[callID]
	if !ok {
		return nil
	}
	fingerprint := xaiToolCallFingerprint(call.Get("name").String(), call.Get("arguments").String())
	if fingerprint == "" {
		return nil
	}
	return map[string]struct{}{fingerprint: {}}
}

func (g *XAIClaudeToolRepeatGuard) PatchCompleted(data []byte) ([]byte, bool) {
	if g == nil || len(g.fingerprints) == 0 || gjson.GetBytes(data, "type").String() != "response.completed" {
		return data, false
	}
	output := gjson.GetBytes(data, "response.output")
	if !output.IsArray() {
		return data, false
	}

	kept := make([]string, 0, len(output.Array())+1)
	blocked := false
	for _, item := range output.Array() {
		if item.Get("type").String() == "function_call" {
			fingerprint := xaiToolCallFingerprint(item.Get("name").String(), item.Get("arguments").String())
			if _, duplicate := g.fingerprints[fingerprint]; duplicate {
				blocked = true
				if callID := strings.TrimSpace(item.Get("call_id").String()); callID != "" {
					g.blockedIDs[callID] = struct{}{}
				}
				if itemID := strings.TrimSpace(item.Get("id").String()); itemID != "" {
					g.blockedIDs[itemID] = struct{}{}
				}
				continue
			}
		}
		kept = append(kept, item.Raw)
	}
	if !blocked {
		return data, false
	}
	message := xaiClaudeRepeatedToolOutputItem()
	kept = append(kept, string(message))
	updated, err := sjson.SetRawBytes(data, "response.output", []byte("["+strings.Join(kept, ",")+"]"))
	if err != nil {
		return data, false
	}
	return updated, true
}

func (g *XAIClaudeToolRepeatGuard) IsBlockedCallEvent(event gjson.Result) bool {
	if g == nil {
		return false
	}
	callID := strings.TrimSpace(event.Get("call_id").String())
	if callID == "" {
		callID = strings.TrimSpace(event.Get("item_id").String())
	}
	_, blocked := g.blockedIDs[callID]
	return blocked
}

func (g *XAIClaudeToolRepeatGuard) IsDuplicateItem(item gjson.Result) bool {
	if g == nil || item.Get("type").String() != "function_call" {
		return false
	}
	fingerprint := xaiToolCallFingerprint(item.Get("name").String(), item.Get("arguments").String())
	_, duplicate := g.fingerprints[fingerprint]
	return duplicate
}

func xaiToolCallFingerprint(name, arguments string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	canonical := arguments
	var value any
	if json.Unmarshal([]byte(arguments), &value) == nil {
		if encoded, err := json.Marshal(value); err == nil {
			canonical = string(encoded)
		}
	}
	sum := sha256.Sum256([]byte(name + "\x00" + canonical))
	return hex.EncodeToString(sum[:])
}

func xaiClaudeRepeatedToolOutputItem() []byte {
	item := []byte(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":""}]}`)
	item, _ = sjson.SetBytes(item, "content.0.text", XAIClaudeRepeatedToolMessage)
	return item
}

func XAIClaudeRepeatedToolOutputEvent() []byte {
	event := []byte(`{"type":"response.output_item.done","output_index":0,"item":{}}`)
	event, _ = sjson.SetRawBytes(event, "item", xaiClaudeRepeatedToolOutputItem())
	return event
}
