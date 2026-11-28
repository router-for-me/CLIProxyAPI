package common

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// HasAntigravityResponsePayload reports whether a chunk carries actual generated
// content or non-zero output token accounting. Presence alone is not enough: an
// envelope such as `{}`, `{"response":{}}`, `{"response":{"candidates":[]}}`, or
// a signature-only empty-text part with zero output tokens must not count as a
// started stream, otherwise a keepalive or zero-output event would be enough to
// finalize a stream that produced nothing.
func HasAntigravityResponsePayload(rawJSON []byte) bool {
	for _, path := range []string{"response.candidates", "candidates"} {
		if candidates := ojson.GetBytesNoCopy(rawJSON, path); candidates.IsArray() {
			for _, cand := range candidates.Array() {
				if strings.TrimSpace(cand.Get("finishReason").String()) != "" {
					return true
				}
				if chunks := cand.Get("groundingMetadata.groundingChunks"); chunks.IsArray() && len(chunks.Array()) > 0 {
					return true
				}
				for _, part := range cand.Get("content.parts").Array() {
					hasVisibleInlineData := !part.Get("thought").Bool() &&
						(part.Get("inlineData.data").String() != "" || part.Get("inline_data.data").String() != "")
					if part.Get("text").String() != "" ||
						part.Get("functionCall").Exists() || part.Get("function_call").Exists() ||
						hasVisibleInlineData ||
						part.Get("fileData").Exists() || part.Get("file_data").Exists() ||
						part.Get("executableCode").Exists() || part.Get("executable_code").Exists() ||
						part.Get("codeExecutionResult").Exists() || part.Get("code_execution_result").Exists() {
						return true
					}
				}
			}
		}
	}
	// FilterSSEUsageMetadata renames non-terminal usage to cpaUsageMetadata before
	// the translator sees it, so both spellings must be accepted.
	for _, path := range []string{
		"response.usageMetadata", "response.cpaUsageMetadata",
		"usageMetadata", "cpaUsageMetadata",
	} {
		if usage := ojson.GetBytesNoCopy(rawJSON, path); usage.IsObject() {
			if usage.Get("candidatesTokenCount").Int() > 0 || usage.Get("thoughtsTokenCount").Int() > 0 {
				return true
			}
		}
	}
	return false
}

// IsGeminiThoughtPart reports whether a Gemini part contains hidden model thought.
func IsGeminiThoughtPart(part ojson.Result) bool {
	return part.Get("thought").Bool()
}

// MergeAdjacentGeminiContents merges consecutive user Content turns.
// Mid-conversation system messages in Claude requests are downgraded to user
// reminder turns. When followed or preceded by other user turns or tool results,
// their parts are merged into a single user turn.
// Consecutive model turns are strictly kept unmerged to avoid shifting part
// indices and breaking cryptographic thought signatures and reasoning replay.
func MergeAdjacentGeminiContents(contents [][]byte) [][]byte {
	if len(contents) <= 1 {
		return contents
	}
	merged := make([][]byte, 0, len(contents))
	for _, content := range contents {
		if len(content) == 0 {
			continue
		}
		role := ojson.GetBytesNoCopy(content, "role").String()
		partsResult := ojson.GetBytesNoCopy(content, "parts")
		if !partsResult.IsArray() || len(partsResult.Array()) == 0 {
			continue
		}
		if len(merged) > 0 {
			lastIndex := len(merged) - 1
			lastJSON := merged[lastIndex]
			lastRole := ojson.GetBytesNoCopy(lastJSON, "role").String()
			if lastRole == "user" && role == "user" {
				lastParts := ojson.GetBytesNoCopy(lastJSON, "parts").Array()
				combinedParts := make([][]byte, 0, len(lastParts)+len(partsResult.Array()))
				for _, p := range lastParts {
					combinedParts = append(combinedParts, []byte(p.Raw))
				}
				for _, p := range partsResult.Array() {
					combinedParts = append(combinedParts, []byte(p.Raw))
				}
				combinedParts = ReorderGeminiUserParts(combinedParts)
				updated, err := ojson.SetRawBytes(lastJSON, "parts", JoinRawArray(combinedParts))
				if err == nil {
					merged[lastIndex] = updated
					continue
				}
			}
		}
		merged = append(merged, content)
	}
	return merged
}

// ContentHasGeminiFunctionCall reports whether a Gemini content turn contains any functionCall part.
func ContentHasGeminiFunctionCall(content []byte) bool {
	return GeminiPartsHaveFunctionCall(ojson.GetBytesNoCopy(content, "parts"))
}

// GeminiPartsHaveFunctionCall reports whether a parsed Gemini parts array contains any functionCall part
// without copying or re-parsing the enclosing content raw bytes.
func GeminiPartsHaveFunctionCall(parts ojson.Result) bool {
	hasFC := false
	parts.ForEach(func(_, part ojson.Result) bool {
		if part.Get("functionCall").Exists() || part.Get("function_call").Exists() {
			hasFC = true
			return false
		}
		return true
	})
	return hasFC
}

// ContentHasGeminiFunctionResponse reports whether a Gemini content turn contains any functionResponse part.
func ContentHasGeminiFunctionResponse(content []byte) bool {
	return GeminiPartsHaveFunctionResponse(ojson.GetBytesNoCopy(content, "parts"))
}

// GeminiPartsHaveFunctionResponse reports whether a parsed Gemini parts array contains any functionResponse part
// without copying or re-parsing the enclosing content raw bytes.
func GeminiPartsHaveFunctionResponse(parts ojson.Result) bool {
	hasFR := false
	parts.ForEach(func(_, part ojson.Result) bool {
		if part.Get("functionResponse").Exists() || part.Get("function_response").Exists() {
			hasFR = true
			return false
		}
		return true
	})
	return hasFR
}

// GeminiPartFunctionCall returns the functionCall or snake_case function_call object from a Gemini part.
func GeminiPartFunctionCall(part ojson.Result) ojson.Result {
	if fc := part.Get("functionCall"); fc.Exists() {
		return fc
	}
	return part.Get("function_call")
}

// GeminiPartFunctionResponse returns the functionResponse or snake_case function_response object from a Gemini part.
func GeminiPartFunctionResponse(part ojson.Result) ojson.Result {
	if fr := part.Get("functionResponse"); fr.Exists() {
		return fr
	}
	return part.Get("function_response")
}

// NormalizeGeminiTurnRole maps a Gemini content role to "user" or "assistant",
// inferring missing or unrecognized roles from functionCall/functionResponse parts
// or alternating from previousRole (matching Gemini ingress normalization).
func NormalizeGeminiTurnRole(rawRole string, parts ojson.Result, previousRole string) string {
	switch strings.ToLower(strings.TrimSpace(rawRole)) {
	case "model", "assistant":
		return "assistant"
	case "user", "function", "tool":
		return "user"
	default:
		if GeminiPartsHaveFunctionCall(parts) {
			return "assistant"
		}
		if GeminiPartsHaveFunctionResponse(parts) {
			return "user"
		}
		if previousRole == "user" {
			return "assistant"
		}
		return "user"
	}
}

// ReorderGeminiUserParts reorders parts within a Gemini user turn so that
// text parts (such as prompt text and system reminders) precede functionResponse
// parts. This resolves upstream provider validation failures (such as Google Cloud
// Vertex AI returning 400 "Requests ending with a model turn are not supported" when
// functionResponse is followed by text in the same turn).
func ReorderGeminiUserParts(parts [][]byte) [][]byte {
	hasFR := false
	hasTrailingText := false
	for _, p := range parts {
		isFR := ojson.GetBytesNoCopy(p, "functionResponse").Exists() || ojson.GetBytesNoCopy(p, "function_response").Exists()
		if isFR {
			hasFR = true
		} else if hasFR && ojson.GetBytesNoCopy(p, "text").Exists() {
			hasTrailingText = true
			break
		}
	}
	if !hasFR || !hasTrailingText {
		return parts
	}

	promptParts := make([][]byte, 0, len(parts))
	toolParts := make([][]byte, 0, len(parts))
	for _, p := range parts {
		if ojson.GetBytesNoCopy(p, "text").Exists() {
			promptParts = append(promptParts, p)
		} else {
			toolParts = append(toolParts, p)
		}
	}
	return append(promptParts, toolParts...)
}

type geminiAlignCallRef struct {
	id   string
	name string
}

// AlignGeminiFunctionResponses aligns functionResponse parts in a non-model turn
// to the order of the preceding model turn's functionCall parts while preserving
// the indices and relative order of any non-functionResponse parts. If a complete
// one-to-one match is unavailable or the responses are already aligned, the turn
// is left unchanged.
func AlignGeminiFunctionResponses(contents [][]byte) [][]byte {
	if len(contents) <= 1 {
		return contents
	}

	var pending []geminiAlignCallRef
	var out [][]byte
	for idx, content := range contents {
		if len(content) == 0 {
			pending = nil
			if out != nil {
				out[idx] = content
			}
			continue
		}
		role := strings.ToLower(strings.TrimSpace(ojson.GetBytesNoCopy(content, "role").String()))
		if role != "model" && len(pending) == 0 {
			if out != nil {
				out[idx] = content
			}
			continue
		}
		parts := ojson.GetBytesNoCopy(content, "parts")
		if !parts.IsArray() || !parts.Get("0").Exists() {
			pending = nil
			if out != nil {
				out[idx] = content
			}
			continue
		}

		if role == "model" {
			var calls []geminiAlignCallRef
			hasResponse := false
			parts.ForEach(func(_, part ojson.Result) bool {
				if GeminiPartFunctionResponse(part).Exists() {
					hasResponse = true
					return false
				}
				if fc := GeminiPartFunctionCall(part); fc.Exists() {
					calls = append(calls, geminiAlignCallRef{
						id:   fc.Get("id").String(),
						name: strings.TrimSpace(fc.Get("name").String()),
					})
				}
				return true
			})
			// Single-call turns can never be out of order; only track parallel call groups.
			if len(calls) > 1 && !hasResponse {
				pending = calls
			} else {
				pending = nil
			}
			if out != nil {
				out[idx] = content
			}
			continue
		}

		var responseIndices []int
		var responses []geminiAlignCallRef
		hasCall := false
		partIdx := 0
		parts.ForEach(func(_, part ojson.Result) bool {
			if GeminiPartFunctionCall(part).Exists() {
				hasCall = true
				return false
			}
			if fr := GeminiPartFunctionResponse(part); fr.Exists() {
				if responses == nil {
					responseIndices = make([]int, 0, len(pending))
					responses = make([]geminiAlignCallRef, 0, len(pending))
				}
				responseIndices = append(responseIndices, partIdx)
				responses = append(responses, geminiAlignCallRef{
					id:   fr.Get("id").String(),
					name: strings.TrimSpace(fr.Get("name").String()),
				})
			}
			partIdx++
			return true
		})

		if hasCall {
			pending = nil
			if out != nil {
				out[idx] = content
			}
			continue
		}
		if len(responses) == 0 {
			if out != nil {
				out[idx] = content
			}
			continue
		}

		if len(responses) == len(pending) {
			if order, changed := matchGeminiFunctionResponseOrder(responses, pending); changed {
				partList := parts.Array()
				orderedParts := make([][]byte, len(partList))
				for pIdx, part := range partList {
					orderedParts[pIdx] = []byte(part.Raw)
				}
				for slotIdx, srcResponseIdx := range order {
					orderedParts[responseIndices[slotIdx]] = []byte(partList[responseIndices[srcResponseIdx]].Raw)
				}
				contentDoc := ojson.NewDoc(content)
				if errSet := contentDoc.SetRawBytes("parts", JoinRawArray(orderedParts)); errSet == nil {
					if out == nil {
						out = make([][]byte, len(contents))
						copy(out[:idx], contents[:idx])
					}
					out[idx] = contentDoc.Bytes()
					pending = nil
					continue
				}
			}
		}

		pending = nil
		if out != nil {
			out[idx] = content
		}
	}

	if out == nil {
		return contents
	}
	return out
}

func matchGeminiFunctionResponseOrder(responses, pending []geminiAlignCallRef) ([]int, bool) {
	if len(responses) != len(pending) || len(responses) <= 1 {
		return nil, false
	}

	alreadyAligned := true
	for i, call := range pending {
		resp := responses[i]
		if call.id != "" {
			if resp.id != call.id || (call.name != "" && resp.name != "" && resp.name != call.name) {
				alreadyAligned = false
				break
			}
		} else if call.name != "" && resp.name != call.name {
			alreadyAligned = false
			break
		}
	}
	if alreadyAligned {
		return nil, false
	}

	used := make([]bool, len(responses))
	order := make([]int, len(pending))
	matched := make([]bool, len(pending))

	// First pass: prefer exact (id, name) matches.
	for slot, call := range pending {
		if call.id == "" || call.name == "" {
			continue
		}
		for respIdx, resp := range responses {
			if !used[respIdx] && resp.id == call.id && resp.name == call.name {
				used[respIdx] = true
				order[slot] = respIdx
				matched[slot] = true
				break
			}
		}
	}

	// Second pass: match remaining calls with non-empty id by id.
	for slot, call := range pending {
		if matched[slot] || call.id == "" {
			continue
		}
		for respIdx, resp := range responses {
			if !used[respIdx] && resp.id == call.id {
				used[respIdx] = true
				order[slot] = respIdx
				matched[slot] = true
				break
			}
		}
	}

	// Third pass: match remaining id-less calls by name.
	for slot, call := range pending {
		if matched[slot] || call.id != "" || call.name == "" {
			continue
		}
		for respIdx, resp := range responses {
			if !used[respIdx] && resp.name == call.name {
				used[respIdx] = true
				order[slot] = respIdx
				matched[slot] = true
				break
			}
		}
	}

	changed := false
	for slot := range pending {
		if !matched[slot] {
			return nil, false
		}
		if order[slot] != slot {
			changed = true
		}
	}
	return order, changed
}

// MergeAdjacentGeminiUserContents merges consecutive user Content turns,
// but leaves turns containing functionResponse unmerged to preserve tool-call/response boundaries.
func MergeAdjacentGeminiUserContents(contents [][]byte) [][]byte {
	if len(contents) <= 1 {
		return contents
	}
	merged := make([][]byte, 0, len(contents))
	pendingContents := make([][]byte, 0, len(contents))
	pendingParts := make([][]byte, 0, len(contents))
	flushPending := func() {
		if len(pendingContents) == 0 {
			return
		}
		if len(pendingContents) == 1 {
			merged = append(merged, pendingContents[0])
		} else if updated, errSet := ojson.SetRawBytes(pendingContents[0], "parts", JoinRawArray(pendingParts)); errSet == nil {
			merged = append(merged, updated)
		} else {
			merged = append(merged, pendingContents...)
		}
		pendingContents = pendingContents[:0]
		pendingParts = pendingParts[:0]
	}

	for _, content := range contents {
		if len(content) == 0 {
			continue
		}
		role := ojson.GetBytesNoCopy(content, "role").String()
		partsResult := ojson.GetBytesNoCopy(content, "parts")
		if !partsResult.IsArray() || partsResult.Raw == "[]" || !partsResult.Get("0").Exists() {
			continue
		}
		if role == "user" && !ContentHasGeminiFunctionResponse(content) {
			pendingContents = append(pendingContents, content)
			for _, part := range partsResult.Array() {
				pendingParts = append(pendingParts, []byte(part.Raw))
			}
			continue
		}
		flushPending()
		merged = append(merged, content)
	}
	flushPending()
	return merged
}

// SplitGeminiFunctionResponseTurns separates functionResponse parts from
// other user parts. Gemini/Antigravity requires a function response turn to
// immediately follow the model turn containing the corresponding call; text
// or reminders in the same user turn or intervening reminder turns can otherwise
// make the response appear orphaned to the upstream validator.
func SplitGeminiFunctionResponseTurns(contents [][]byte) [][]byte {
	if len(contents) == 0 {
		return contents
	}
	split := make([][]byte, 0, len(contents))
	for _, content := range contents {
		if ojson.GetBytesNoCopy(content, "role").String() != "user" || !ContentHasGeminiFunctionResponse(content) {
			split = append(split, content)
			continue
		}
		parts := ojson.GetBytesNoCopy(content, "parts").Array()
		responseParts := make([][]byte, 0, len(parts))
		otherParts := make([][]byte, 0, len(parts))
		for _, part := range parts {
			if part.Get("functionResponse").Exists() || part.Get("function_response").Exists() {
				responseParts = append(responseParts, []byte(part.Raw))
			} else {
				otherParts = append(otherParts, []byte(part.Raw))
			}
		}
		var responseTurn, otherTurn []byte
		var responseErr, otherErr error
		if len(responseParts) > 0 {
			responseTurn, responseErr = ojson.SetRawBytes(content, "parts", JoinRawArray(responseParts))
		}
		if len(otherParts) > 0 {
			otherTurn, otherErr = ojson.SetRawBytes(content, "parts", JoinRawArray(otherParts))
		}
		if responseErr != nil || otherErr != nil {
			// Preserve the original turn if rebuilding either split turn fails.
			split = append(split, content)
			continue
		}
		if len(responseTurn) > 0 {
			split = append(split, responseTurn)
		}
		if len(otherTurn) > 0 {
			split = append(split, otherTurn)
		}
	}

	// For any consecutive block of user turns immediately following a model turn
	// containing function calls, ensure that turns containing functionResponse
	// precede pure text/reminder turns. This guarantees that function responses
	// immediately follow the preceding model turn even when mid-session system
	// or developer reminders were inserted between the model and tool_result turns.
	out := make([][]byte, 0, len(split))
	n := len(split)
	for i := 0; i < n; {
		if ojson.GetBytesNoCopy(split[i], "role").String() != "user" {
			out = append(out, split[i])
			i++
			continue
		}
		precedingModelHasFC := len(out) > 0 &&
			ojson.GetBytesNoCopy(out[len(out)-1], "role").String() == "model" &&
			ContentHasGeminiFunctionCall(out[len(out)-1])

		j := i
		hasFR := false
		for j < n && ojson.GetBytesNoCopy(split[j], "role").String() == "user" {
			if ContentHasGeminiFunctionResponse(split[j]) {
				hasFR = true
			}
			j++
		}
		userRun := split[i:j]
		if precedingModelHasFC && hasFR && len(userRun) > 1 {
			var combinedFRParts [][]byte
			otherTurns := make([][]byte, 0, len(userRun))
			for _, turn := range userRun {
				if ContentHasGeminiFunctionResponse(turn) {
					parts := ojson.GetBytesNoCopy(turn, "parts").Array()
					for _, p := range parts {
						combinedFRParts = append(combinedFRParts, []byte(p.Raw))
					}
				} else {
					otherTurns = append(otherTurns, turn)
				}
			}
			if len(combinedFRParts) > 0 {
				frTurn := []byte(`{"role":"user","parts":[]}`)
				frTurn, _ = ojson.SetRawBytes(frTurn, "parts", JoinRawArray(combinedFRParts))
				out = append(out, frTurn)
			}
			out = append(out, otherTurns...)
		} else {
			out = append(out, userRun...)
		}
		i = j
	}
	return out
}

// ContainsJSONRef reports whether value (recursively) contains a string-valued "$ref" property.
func ContainsJSONRef(value ojson.Result) bool {
	if !value.IsObject() && !value.IsArray() {
		return false
	}
	found := false
	value.ForEach(func(key, child ojson.Result) bool {
		if value.IsObject() && key.String() == "$ref" && child.Type == ojson.String {
			found = true
			return false
		}
		if ContainsJSONRef(child) {
			found = true
			return false
		}
		return true
	})
	return found
}

// SetGeminiFunctionResponseResult sets the Gemini functionResponse result or response field.
// If the result contains a JSON Schema or OpenAPI "$ref" property with a string value, it is
// preserved as opaque JSON text with ojson.SetBytes. This prevents Gemini/Vertex AI from
// interpreting "$ref" as a reference to a media part in function_response.parts and rejecting
// the request with HTTP 400.
// If path points to "functionResponse.response" or "response", a stringified result is placed
// under the ".result" child object to satisfy Gemini's requirement that "response" is an object.
func SetGeminiFunctionResponseResult(part []byte, path string, result ojson.Result) []byte {
	if !result.Exists() {
		part, _ = ojson.SetBytes(part, path, "")
		return part
	}
	if ContainsJSONRef(result) {
		targetPath := path
		if strings.HasSuffix(path, "response") {
			targetPath = path + ".result"
		}
		part, _ = ojson.SetBytes(part, targetPath, result.Raw)
		return part
	}
	part, _ = ojson.SetRawBytes(part, path, []byte(result.Raw))
	return part
}

// SetGeminiFunctionResponseRaw is a convenience wrapper around SetGeminiFunctionResponseResult
// for raw JSON strings.
func SetGeminiFunctionResponseRaw(part []byte, path string, rawJSON string) []byte {
	trimmed := strings.TrimSpace(rawJSON)
	if trimmed == "" {
		part, _ = ojson.SetBytes(part, path, "")
		return part
	}
	return SetGeminiFunctionResponseResult(part, path, ojson.Parse(trimmed))
}

// SetGeminiFunctionResponseResultDoc sets the Gemini functionResponse result or response field on doc.
func SetGeminiFunctionResponseResultDoc(doc *ojson.Doc, path string, result ojson.Result) {
	if doc == nil {
		return
	}
	if !result.Exists() {
		_ = doc.Set(path, "")
		return
	}
	if ContainsJSONRef(result) {
		targetPath := path
		if strings.HasSuffix(path, "response") {
			targetPath = path + ".result"
		}
		_ = doc.Set(targetPath, result.Raw)
		return
	}
	_ = doc.SetRaw(path, result.Raw)
}

// SetGeminiFunctionResponseRawDoc is a convenience wrapper around SetGeminiFunctionResponseResultDoc
// for raw JSON strings.
func SetGeminiFunctionResponseRawDoc(doc *ojson.Doc, path string, rawJSON string) {
	if doc == nil {
		return
	}
	trimmed := strings.TrimSpace(rawJSON)
	if trimmed == "" {
		_ = doc.Set(path, "")
		return
	}
	SetGeminiFunctionResponseResultDoc(doc, path, ojson.Parse(trimmed))
}

// SetGeminiFunctionCallArgsDoc writes a valid JSON object to path on doc for Gemini functionCall.args.
// Valid JSON-object strings are unwrapped; all other non-object or invalid inputs fall back to {}.
func SetGeminiFunctionCallArgsDoc(doc *ojson.Doc, path string, value ojson.Result) {
	if doc == nil {
		return
	}
	if value.IsObject() {
		raw := strings.TrimSpace(value.Raw)
		if ojson.Valid(raw) {
			_ = doc.SetRaw(path, raw)
			return
		}
	}
	if value.Type == ojson.String {
		raw := strings.TrimSpace(value.String())
		if raw != "" && ojson.Valid(raw) && ojson.Parse(raw).IsObject() {
			_ = doc.SetRaw(path, raw)
			return
		}
	}
	_ = doc.SetRaw(path, `{}`)
}

// NormalizeGeminiFunctionCallPart ensures a native Gemini functionCall part carries a valid JSON object
// in functionCall.args while preserving all other fields on the part.
func NormalizeGeminiFunctionCallPart(part ojson.Result) []byte {
	if args := part.Get("functionCall.args"); args.IsObject() && ojson.Valid(strings.TrimSpace(args.Raw)) {
		return []byte(part.Raw)
	}
	partDoc := ojson.NewDoc([]byte(part.Raw))
	SetGeminiFunctionCallArgsDoc(partDoc, "functionCall.args", part.Get("functionCall.args"))
	return partDoc.Bytes()
}
