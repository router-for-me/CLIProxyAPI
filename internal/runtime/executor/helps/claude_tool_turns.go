package helps

import (
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const interruptedClaudeToolResultContent = "[operation interrupted by user]"

type claudeClientToolUse struct {
	id          string
	toolsetName string
}

type claudeCarrierPart struct {
	raw          []byte
	isToolResult bool
}

type claudeResultCarrierRun struct {
	end                int
	count              int
	base               gjson.Result
	parts              []claudeCarrierPart
	replayAfterCarrier [][]byte
	followingInterrupt bool
	bridgedSystem      bool
}

// RepairDanglingClaudeToolUses closes client tool calls left incomplete by an
// interrupted agent turn. It also canonicalizes adjacent result carriers so the
// final Claude Messages payload contains one ordered user turn with exactly one
// result for every client tool call.
func RepairDanglingClaudeToolUses(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload
	}

	messageResults := messages.Array()
	out := make([][]byte, 0, len(messageResults)+2)
	changed := false

	for i := 0; i < len(messageResults); i++ {
		message := messageResults[i]
		toolUses := claudeAssistantClientToolUses(message)
		if len(toolUses) == 0 {
			repaired, repairedMessage := repairStandaloneClaudeToolResults(message)
			out = append(out, repaired)
			changed = changed || repairedMessage
			continue
		}

		run := collectClaudeResultCarrierRun(messageResults, i+1, toolUses)
		carrier := buildCanonicalClaudeResultCarrier(run.base, toolUses, run.parts)
		hasExtras := claudeContentHasNonToolResults(gjson.GetBytes(carrier, "content")) || run.followingInterrupt
		assistantRaw := []byte(message.Raw)
		droppedServerTool := false
		if hasExtras {
			// A pending server tool may only be resumed by a user turn containing
			// client tool results. Interrupt text ends that turn, so abandon only
			// unresolved server calls before preserving the user's new instruction.
			assistantRaw, droppedServerTool = dropUnresolvedClaudeServerToolUses(message)
			changed = changed || droppedServerTool
		}
		out = append(out, assistantRaw)

		if run.count == 1 && !droppedServerTool && isCanonicalClaudeResultCarrier(run.base, toolUses) {
			out = append(out, []byte(run.base.Raw))
			out = append(out, run.replayAfterCarrier...)
			changed = changed || len(run.replayAfterCarrier) > 0
			i = run.end - 1
			continue
		}

		out = append(out, carrier)
		out = append(out, run.replayAfterCarrier...)
		changed = true
		if run.count > 0 {
			i = run.end - 1
		}
	}

	if !changed {
		return payload
	}
	updated, errSet := sjson.SetRawBytes(payload, "messages", translatorcommon.JoinRawArray(out))
	if errSet != nil {
		return payload
	}
	return updated
}

func claudeAssistantClientToolUses(message gjson.Result) []claudeClientToolUse {
	if message.Get("role").String() != "assistant" {
		return nil
	}
	content := message.Get("content")
	if !content.IsArray() {
		return nil
	}
	toolUses := make([]claudeClientToolUse, 0)
	content.ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() != "tool_use" {
			return true
		}
		id := claudeStringField(part, "id")
		if id == "" {
			return true
		}
		toolUses = append(toolUses, claudeClientToolUse{
			id:          id,
			toolsetName: claudeStringField(part, "toolset_name"),
		})
		return true
	})
	return toolUses
}

func collectClaudeResultCarrierRun(messages []gjson.Result, start int, toolUses []claudeClientToolUse) claudeResultCarrierRun {
	run := claudeResultCarrierRun{end: start}
	outstanding := make(map[string]struct{}, len(toolUses))
	for _, toolUse := range toolUses {
		outstanding[toolUse.id] = struct{}{}
	}
	for run.end < len(messages) {
		message := messages[run.end]
		if isClaudeResultCarrierMessage(message) {
			parts := claudeCarrierMessageParts(message, message.Get("role").String())
			if run.bridgedSystem {
				if !claudeCarrierPartsContainOutstandingResult(parts, outstanding) {
					run.followingInterrupt = true
					break
				}
				matched, remaining := extractClaudeOutstandingToolResults(outstanding, parts)
				run.parts = append(run.parts, matched...)
				if len(remaining) > 0 {
					run.replayAfterCarrier = append(run.replayAfterCarrier, buildDeferredClaudeCarrier(message, remaining))
					run.followingInterrupt = true
				}
				if run.count == 0 && len(remaining) == 0 {
					run.base = message
				}
				run.count++
				run.end++
				if len(remaining) > 0 {
					// Remaining user content ends the tool-result bridge. A later result
					// must not be hoisted across that instruction or diagnostic text.
					break
				}
				continue
			}
			if run.count == 0 {
				run.base = message
			}
			run.parts = append(run.parts, parts...)
			if claudeCarrierPartsContainInterrupt(parts, outstanding) {
				run.followingInterrupt = true
			}
			consumeClaudeOutstandingToolResults(outstanding, parts)
			run.count++
			run.end++
			continue
		}

		if message.Get("role").String() != "system" {
			break
		}
		if run.followingInterrupt {
			break
		}
		systemEnd := run.end
		for systemEnd < len(messages) && messages[systemEnd].Get("role").String() == "system" {
			systemEnd++
		}
		if systemEnd == len(messages) || !isClaudeResultCarrierMessage(messages[systemEnd]) {
			break
		}
		nextCarrier := messages[systemEnd]
		nextParts := claudeCarrierMessageParts(nextCarrier, nextCarrier.Get("role").String())
		if len(outstanding) == 0 || !claudeCarrierPartsContainOutstandingResult(nextParts, outstanding) {
			run.followingInterrupt = true
			break
		}

		// Anthropic requires the user result carrier immediately after an
		// assistant tool_use, while a mid-conversation system turn must follow a
		// user turn. Defer only system runs whose next carrier satisfies an
		// outstanding call, then replay them after the canonical carrier. This
		// keeps completed tool turns from absorbing a later ordinary user request.
		for index := run.end; index < systemEnd; index++ {
			run.replayAfterCarrier = append(run.replayAfterCarrier, []byte(messages[index].Raw))
		}
		run.end = systemEnd
		run.bridgedSystem = true
	}
	return run
}

func claudeCarrierPartsContainInterrupt(parts []claudeCarrierPart, outstanding map[string]struct{}) bool {
	matched := make(map[string]struct{})
	for _, part := range parts {
		if !part.isToolResult {
			return true
		}
		id := claudeStringField(gjson.ParseBytes(part.raw), "tool_use_id")
		if _, exists := outstanding[id]; !exists {
			return true
		}
		if _, duplicate := matched[id]; duplicate {
			return true
		}
		matched[id] = struct{}{}
	}
	return false
}

func claudeCarrierPartsContainOutstandingResult(parts []claudeCarrierPart, outstanding map[string]struct{}) bool {
	for _, part := range parts {
		if !part.isToolResult {
			continue
		}
		id := claudeStringField(gjson.ParseBytes(part.raw), "tool_use_id")
		if _, exists := outstanding[id]; exists {
			return true
		}
	}
	return false
}

func consumeClaudeOutstandingToolResults(outstanding map[string]struct{}, parts []claudeCarrierPart) {
	for _, part := range parts {
		if !part.isToolResult {
			continue
		}
		delete(outstanding, claudeStringField(gjson.ParseBytes(part.raw), "tool_use_id"))
	}
}

func extractClaudeOutstandingToolResults(outstanding map[string]struct{}, parts []claudeCarrierPart) (matched, remaining []claudeCarrierPart) {
	matched = make([]claudeCarrierPart, 0, len(parts))
	remaining = make([]claudeCarrierPart, 0, len(parts))
	for _, part := range parts {
		if part.isToolResult {
			id := claudeStringField(gjson.ParseBytes(part.raw), "tool_use_id")
			if _, exists := outstanding[id]; exists {
				matched = append(matched, part)
				delete(outstanding, id)
				continue
			}
		}
		remaining = append(remaining, part)
	}
	return matched, remaining
}

func buildDeferredClaudeCarrier(base gjson.Result, parts []claudeCarrierPart) []byte {
	content := make([][]byte, 0, len(parts))
	for _, part := range parts {
		if !part.isToolResult {
			content = append(content, part.raw)
			continue
		}
		normalized, id := normalizeClaudeToolResult(part.raw)
		content = append(content, downgradeClaudeToolResult(normalized, id))
	}

	message := []byte(`{"role":"user","content":[]}`)
	if base.IsObject() {
		message = []byte(base.Raw)
		message, _ = sjson.SetBytes(message, "role", "user")
		for _, field := range []string{"tool_use_id", "tool_call_id", "call_id", "id", "name"} {
			message, _ = sjson.DeleteBytes(message, field)
		}
	}
	message, _ = sjson.SetRawBytes(message, "content", translatorcommon.JoinRawArray(content))
	return message
}

func isClaudeResultCarrierMessage(message gjson.Result) bool {
	role := message.Get("role").String()
	if role != "user" && role != "tool" {
		return false
	}
	return !claudeContentContainsType(message.Get("content"), "tool_use")
}

func claudeCarrierMessageParts(message gjson.Result, role string) []claudeCarrierPart {
	content := message.Get("content")
	if role == "tool" && !claudeContentContainsType(content, "tool_result") {
		if id := firstClaudeStringField(message, "tool_use_id", "tool_call_id", "call_id"); id != "" {
			result := []byte(`{"type":"tool_result","tool_use_id":"","content":""}`)
			result, _ = sjson.SetBytes(result, "tool_use_id", id)
			if content.Exists() && content.Type != gjson.Null {
				if content.Type == gjson.String {
					result, _ = sjson.SetBytes(result, "content", content.String())
				} else {
					result, _ = sjson.SetRawBytes(result, "content", []byte(content.Raw))
				}
			}
			return []claudeCarrierPart{{raw: result, isToolResult: true}}
		}
	}

	if content.Type == gjson.String {
		if strings.TrimSpace(content.String()) == "" {
			return nil
		}
		return []claudeCarrierPart{{raw: claudeTextPart(content.String())}}
	}
	if !content.IsArray() {
		return nil
	}

	parts := make([]claudeCarrierPart, 0, len(content.Array()))
	content.ForEach(func(_, part gjson.Result) bool {
		if !part.IsObject() {
			return true
		}
		parts = append(parts, claudeCarrierPart{
			raw:          []byte(part.Raw),
			isToolResult: part.Get("type").String() == "tool_result",
		})
		return true
	})
	return parts
}

func claudeContentHasNonToolResults(content gjson.Result) bool {
	if !content.IsArray() {
		return false
	}
	hasExtras := false
	content.ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() != "tool_result" {
			hasExtras = true
			return false
		}
		return true
	})
	return hasExtras
}

func buildCanonicalClaudeResultCarrier(base gjson.Result, toolUses []claudeClientToolUse, carrierParts []claudeCarrierPart) []byte {
	wanted := make(map[string]int, len(toolUses))
	for index, toolUse := range toolUses {
		if _, exists := wanted[toolUse.id]; !exists {
			wanted[toolUse.id] = index
		}
	}

	results := make([][]byte, len(toolUses))
	extras := make([][]byte, 0, len(carrierParts))
	for _, part := range carrierParts {
		if !part.isToolResult {
			extras = append(extras, part.raw)
			continue
		}

		normalized, id := normalizeClaudeToolResult(part.raw)
		index, expected := wanted[id]
		if !expected || results[index] != nil {
			// Orphan and duplicate result blocks are invalid at protocol level.
			// Keep their diagnostic payload as ordinary user text instead.
			extras = append(extras, downgradeClaudeToolResult(normalized, id))
			continue
		}
		results[index] = alignClaudeToolResultToolset(normalized, toolUses[index].toolsetName)
	}

	content := make([][]byte, 0, len(toolUses)+len(extras))
	for index, toolUse := range toolUses {
		result := results[index]
		if result == nil {
			result = interruptedClaudeToolResult(toolUse)
		}
		content = append(content, result)
	}
	content = append(content, extras...)

	message := []byte(`{"role":"user","content":[]}`)
	if base.IsObject() {
		message = []byte(base.Raw)
		message, _ = sjson.SetBytes(message, "role", "user")
		for _, field := range []string{"tool_use_id", "tool_call_id", "call_id", "id", "name"} {
			message, _ = sjson.DeleteBytes(message, field)
		}
	}
	message, _ = sjson.SetRawBytes(message, "content", translatorcommon.JoinRawArray(content))
	return message
}

func isCanonicalClaudeResultCarrier(message gjson.Result, toolUses []claudeClientToolUse) bool {
	if message.Get("role").String() != "user" {
		return false
	}
	content := message.Get("content")
	if !content.IsArray() {
		return false
	}

	resultIndex := 0
	extrasStarted := false
	canonical := true
	content.ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() != "tool_result" {
			extrasStarted = true
			return true
		}
		if extrasStarted || resultIndex >= len(toolUses) || part.Get("id").Exists() {
			canonical = false
			return false
		}
		if claudeStringField(part, "tool_use_id") != toolUses[resultIndex].id {
			canonical = false
			return false
		}
		toolsetName := part.Get("toolset_name")
		if toolUses[resultIndex].toolsetName == "" && toolsetName.Exists() {
			canonical = false
			return false
		}
		if toolUses[resultIndex].toolsetName != "" && claudeStringField(part, "toolset_name") != toolUses[resultIndex].toolsetName {
			canonical = false
			return false
		}
		resultIndex++
		return true
	})
	return canonical && resultIndex == len(toolUses)
}

func normalizeClaudeToolResult(raw []byte) ([]byte, string) {
	result := gjson.ParseBytes(raw)
	// Only tool_use_id links an Anthropic result to a call. A generic id is
	// untrusted metadata and must not make a malformed result look complete.
	id := claudeStringField(result, "tool_use_id")
	updated := raw
	if result.Get("id").Exists() {
		updated, _ = sjson.DeleteBytes(updated, "id")
	}
	return updated, id
}

func alignClaudeToolResultToolset(result []byte, toolsetName string) []byte {
	if toolsetName == "" {
		updated, _ := sjson.DeleteBytes(result, "toolset_name")
		return updated
	}
	updated, _ := sjson.SetBytes(result, "toolset_name", toolsetName)
	return updated
}

func interruptedClaudeToolResult(toolUse claudeClientToolUse) []byte {
	result := []byte(`{"type":"tool_result","tool_use_id":"","content":"","is_error":true}`)
	result, _ = sjson.SetBytes(result, "tool_use_id", toolUse.id)
	if toolUse.toolsetName != "" {
		result, _ = sjson.SetBytes(result, "toolset_name", toolUse.toolsetName)
		result, _ = sjson.SetRawBytes(result, "content", translatorcommon.JoinRawArray([][]byte{claudeTextPart(interruptedClaudeToolResultContent)}))
	} else {
		result, _ = sjson.SetBytes(result, "content", interruptedClaudeToolResultContent)
	}
	return result
}

func downgradeClaudeToolResult(result []byte, id string) []byte {
	label := "[unmatched tool result"
	if id != "" {
		label += " " + id
	}
	label += "]"
	if text := strings.TrimSpace(claudeToolResultText(gjson.GetBytes(result, "content"))); text != "" {
		label += "\n" + text
	}
	return claudeTextPart(label)
}

func claudeToolResultText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if content.IsArray() {
		text := make([]string, 0, len(content.Array()))
		content.ForEach(func(_, part gjson.Result) bool {
			if value := part.Get("text"); value.Type == gjson.String && value.String() != "" {
				text = append(text, value.String())
			}
			return true
		})
		if len(text) > 0 {
			return strings.Join(text, "\n")
		}
	}
	if content.Exists() && content.Type != gjson.Null {
		return content.Raw
	}
	return ""
}

func repairStandaloneClaudeToolResults(message gjson.Result) ([]byte, bool) {
	role := message.Get("role").String()
	if role != "user" && role != "tool" {
		return []byte(message.Raw), false
	}
	content := message.Get("content")
	if content.Type == gjson.String {
		if role != "tool" {
			return []byte(message.Raw), false
		}
		text := "[unmatched tool result]"
		if strings.TrimSpace(content.String()) != "" {
			text += "\n" + content.String()
		}
		updated := []byte(message.Raw)
		updated, _ = sjson.SetBytes(updated, "role", "user")
		updated, _ = sjson.SetRawBytes(updated, "content", translatorcommon.JoinRawArray([][]byte{claudeTextPart(text)}))
		return updated, true
	}
	if !content.IsArray() {
		if role != "tool" {
			return []byte(message.Raw), false
		}
		updated := []byte(message.Raw)
		updated, _ = sjson.SetBytes(updated, "role", "user")
		return updated, true
	}

	parts := make([][]byte, 0, len(content.Array()))
	hasToolResult := false
	content.ForEach(func(_, part gjson.Result) bool {
		if !part.IsObject() {
			return true
		}
		if part.Get("type").String() == "tool_result" {
			hasToolResult = true
			normalized, id := normalizeClaudeToolResult([]byte(part.Raw))
			parts = append(parts, downgradeClaudeToolResult(normalized, id))
			return true
		}
		parts = append(parts, []byte(part.Raw))
		return true
	})
	if !hasToolResult && role != "tool" {
		return []byte(message.Raw), false
	}

	updated := []byte(message.Raw)
	updated, _ = sjson.SetBytes(updated, "role", "user")
	for _, field := range []string{"tool_use_id", "tool_call_id", "call_id", "id", "name"} {
		updated, _ = sjson.DeleteBytes(updated, field)
	}
	updated, _ = sjson.SetRawBytes(updated, "content", translatorcommon.JoinRawArray(parts))
	return updated, true
}

func dropUnresolvedClaudeServerToolUses(message gjson.Result) ([]byte, bool) {
	content := message.Get("content")
	if !content.IsArray() {
		return []byte(message.Raw), false
	}

	unresolved := make(map[string]struct{})
	content.ForEach(func(_, part gjson.Result) bool {
		switch part.Get("type").String() {
		case "server_tool_use", "mcp_tool_use":
			if id := claudeStringField(part, "id"); id != "" {
				unresolved[id] = struct{}{}
			}
		}
		return true
	})
	if len(unresolved) == 0 {
		return []byte(message.Raw), false
	}
	content.ForEach(func(_, part gjson.Result) bool {
		partType := part.Get("type").String()
		if partType != "tool_result" && strings.Contains(partType, "tool_result") {
			delete(unresolved, claudeStringField(part, "tool_use_id"))
		}
		return true
	})
	if len(unresolved) == 0 {
		return []byte(message.Raw), false
	}

	parts := make([][]byte, 0, len(content.Array()))
	content.ForEach(func(_, part gjson.Result) bool {
		partType := part.Get("type").String()
		if partType == "server_tool_use" || partType == "mcp_tool_use" {
			if _, drop := unresolved[claudeStringField(part, "id")]; drop {
				return true
			}
		}
		parts = append(parts, []byte(part.Raw))
		return true
	})
	updated := []byte(message.Raw)
	updated, _ = sjson.SetRawBytes(updated, "content", translatorcommon.JoinRawArray(parts))
	return updated, true
}

func claudeContentContainsType(content gjson.Result, partType string) bool {
	if !content.IsArray() {
		return false
	}
	found := false
	content.ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() == partType {
			found = true
			return false
		}
		return true
	})
	return found
}

func claudeTextPart(text string) []byte {
	part := []byte(`{"type":"text","text":""}`)
	part, _ = sjson.SetBytes(part, "text", text)
	return part
}

func firstClaudeStringField(result gjson.Result, fields ...string) string {
	for _, field := range fields {
		if value := claudeStringField(result, field); value != "" {
			return value
		}
	}
	return ""
}

func claudeStringField(result gjson.Result, field string) string {
	value := result.Get(field)
	if value.Type != gjson.String {
		return ""
	}
	return value.String()
}
