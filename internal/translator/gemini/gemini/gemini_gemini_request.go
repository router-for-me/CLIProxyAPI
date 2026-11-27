// Package gemini provides in-provider request normalization for Gemini API.
// It ensures incoming v1beta requests meet minimal schema requirements
// expected by Google's Generative Language API.
package gemini

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

// ConvertGeminiRequestToGemini normalizes Gemini v1beta requests.
//   - Adds a default role for each content if missing or invalid.
//     The first message defaults to "user", then alternates user/model when needed.
//
// It keeps the payload otherwise unchanged.
func ConvertGeminiRequestToGemini(_ string, inputRawJSON []byte, _ bool) ([]byte, error) {
	rawJSON := inputRawJSON
	// Fast path: if no contents field, only attach safety settings
	contents := util.GetGJSONBytesNoCopy(rawJSON, "contents")
	if !contents.Exists() {
		return common.AttachDefaultSafetySettings(rawJSON, "safetySettings"), nil
	}

	toolsResult := ojson.GetBytesNoCopy(rawJSON, "tools")
	if toolsResult.Exists() && toolsResult.IsArray() {
		var toolItems [][]byte
		toolsChanged := false
		toolsResult.ForEach(func(_, toolResult ojson.Result) bool {
			var toolDoc *ojson.Doc
			ensureToolDoc := func() *ojson.Doc {
				if toolDoc == nil {
					toolDoc = ojson.NewDocString(toolResult.Raw)
				}
				return toolDoc
			}
			declarations := toolResult.Get("function_declarations")
			if camelDecls := toolResult.Get("functionDeclarations"); camelDecls.Exists() {
				d := ensureToolDoc()
				_ = d.SetRaw("function_declarations", camelDecls.Raw)
				_ = d.Delete("functionDeclarations")
				declarations = camelDecls
			}

			if declarations.IsArray() {
				var declarationItems [][]byte
				declarationsChanged := false
				declarations.ForEach(func(_, declarationResult ojson.Result) bool {
					declaration := []byte(declarationResult.Raw)
					if parameters := declarationResult.Get("parameters"); parameters.Exists() {
						normalizedParams := util.NormalizeJSONSchemaNumericConstraints(parameters.Raw)
						declDoc := ojson.NewDocString(declarationResult.Raw)
						_ = declDoc.SetRaw("parametersJsonSchema", normalizedParams)
						_ = declDoc.Delete("parameters")
						declaration = declDoc.Bytes()
						declarationsChanged = true
					} else if parametersJSONSchema := declarationResult.Get("parametersJsonSchema"); parametersJSONSchema.Exists() {
						if normalizedParams := util.NormalizeJSONSchemaNumericConstraints(parametersJSONSchema.Raw); normalizedParams != parametersJSONSchema.Raw {
							declDoc := ojson.NewDocString(declarationResult.Raw)
							_ = declDoc.SetRaw("parametersJsonSchema", normalizedParams)
							declaration = declDoc.Bytes()
							declarationsChanged = true
						}
					} else if parametersJSONSchema := declarationResult.Get("parameters_json_schema"); parametersJSONSchema.Exists() {
						if normalizedParams := util.NormalizeJSONSchemaNumericConstraints(parametersJSONSchema.Raw); normalizedParams != parametersJSONSchema.Raw {
							declDoc := ojson.NewDocString(declarationResult.Raw)
							_ = declDoc.SetRaw("parameters_json_schema", normalizedParams)
							declaration = declDoc.Bytes()
							declarationsChanged = true
						}
					}
					declarationItems = append(declarationItems, declaration)
					return true
				})
				if declarationsChanged {
					_ = ensureToolDoc().SetRawBytes("function_declarations", translatorcommon.JoinRawArray(declarationItems))
				}
			}
			if toolDoc != nil {
				toolsChanged = true
				toolItems = append(toolItems, toolDoc.Bytes())
			} else {
				toolItems = append(toolItems, []byte(toolResult.Raw))
			}
			return true
		})
		if toolsChanged {
			rawJSON, _ = ojson.SetRawBytes(rawJSON, "tools", translatorcommon.JoinRawArray(toolItems))
		}
	}

	// Walk contents and fix roles
	out := rawJSON
	prevRole := ""
	if contents.IsArray() {
		rolesChanged := false
		contents.ForEach(func(_, value ojson.Result) bool {
			role := value.Get("role").String()
			if role != "user" && role != "model" {
				if translatorcommon.ContentHasGeminiFunctionResponse([]byte(value.Raw)) {
					role = "user"
				} else {
					role = nextGeminiRole(prevRole)
				}
				rolesChanged = true
			}
			prevRole = role
			return true
		})
		if rolesChanged {
			prevRole = ""
			contentItems := translatorcommon.NewRawArrayItems(contents.Get("#").Int())
			contents.ForEach(func(_, value ojson.Result) bool {
				role := value.Get("role").String()
				item := []byte(value.Raw)
				if role != "user" && role != "model" {
					if translatorcommon.ContentHasGeminiFunctionResponse([]byte(value.Raw)) {
						role = "user"
					} else {
						role = nextGeminiRole(prevRole)
					}
					item, _ = ojson.SetBytes(item, "role", role)
				}
				prevRole = role
				contentItems = append(contentItems, item)
				return true
			})
			out, _ = ojson.SetRawBytes(out, "contents", translatorcommon.JoinRawArray(contentItems))
		}
	} else {
		idx := 0
		var outDoc *ojson.Doc
		contents.ForEach(func(_ ojson.Result, value ojson.Result) bool {
			role := value.Get("role").String()
			if role != "user" && role != "model" {
				if translatorcommon.ContentHasGeminiFunctionResponse([]byte(value.Raw)) {
					role = "user"
				} else {
					role = nextGeminiRole(prevRole)
				}
				if outDoc == nil {
					outDoc = ojson.NewDoc(out)
				}
				_ = outDoc.Set(fmt.Sprintf("contents.%d.role", idx), role)
			}
			prevRole = role
			idx++
			return true
		})
		if outDoc != nil {
			out = outDoc.Bytes()
		}
	}

	// Merge parallel functionResponse parts split across consecutive user/function contents
	// and align them with the preceding model turn's functionCall order before validation.
	out = groupGeminiFunctionResponses(out)

	out = signature.SanitizeGeminiRequestThoughtSignatures(out, "contents")

	var respSchemaDoc *ojson.Doc
	ensureRespSchemaDoc := func() *ojson.Doc {
		if respSchemaDoc == nil {
			respSchemaDoc = ojson.NewDoc(out)
		}
		return respSchemaDoc
	}
	for _, cfg := range []struct {
		container   string
		protoKey    string
		protoAltKey string
		jsonKey     string
		jsonAltKey  string
	}{
		{
			container:   "generationConfig",
			protoKey:    "responseSchema",
			protoAltKey: "response_schema",
			jsonKey:     "responseJsonSchema",
			jsonAltKey:  "response_json_schema",
		},
		{
			container:   "generation_config",
			protoKey:    "response_schema",
			protoAltKey: "responseSchema",
			jsonKey:     "response_json_schema",
			jsonAltKey:  "responseJsonSchema",
		},
	} {
		if !ojson.GetBytesNoCopy(out, cfg.container).IsObject() {
			continue
		}
		if responseSchema := ojson.GetBytesNoCopy(out, cfg.container+"."+cfg.protoKey); responseSchema.Exists() {
			normalizedSchema := util.NormalizeJSONSchemaNumericConstraints(responseSchema.Raw)
			d := ensureRespSchemaDoc()
			_ = d.SetRaw(cfg.container+"."+cfg.jsonKey, normalizedSchema)
			_ = d.Delete(cfg.container + "." + cfg.protoKey)
		} else if responseSchema := ojson.GetBytesNoCopy(out, cfg.container+"."+cfg.protoAltKey); responseSchema.Exists() {
			normalizedSchema := util.NormalizeJSONSchemaNumericConstraints(responseSchema.Raw)
			d := ensureRespSchemaDoc()
			_ = d.SetRaw(cfg.container+"."+cfg.jsonKey, normalizedSchema)
			_ = d.Delete(cfg.container + "." + cfg.protoAltKey)
		} else if responseJSONSchema := ojson.GetBytesNoCopy(out, cfg.container+"."+cfg.jsonKey); responseJSONSchema.Exists() {
			if normalizedSchema := util.NormalizeJSONSchemaNumericConstraints(responseJSONSchema.Raw); normalizedSchema != responseJSONSchema.Raw {
				_ = ensureRespSchemaDoc().SetRaw(cfg.container+"."+cfg.jsonKey, normalizedSchema)
			}
		} else if responseJSONSchema := ojson.GetBytesNoCopy(out, cfg.container+"."+cfg.jsonAltKey); responseJSONSchema.Exists() {
			if normalizedSchema := util.NormalizeJSONSchemaNumericConstraints(responseJSONSchema.Raw); normalizedSchema != responseJSONSchema.Raw {
				_ = ensureRespSchemaDoc().SetRaw(cfg.container+"."+cfg.jsonAltKey, normalizedSchema)
			}
		}
	}
	if respSchemaDoc != nil {
		out = respSchemaDoc.Bytes()
	}

	// Backfill empty functionResponse.name from the preceding functionCall.name.
	// Some clients send function responses with empty names; the Gemini API rejects these.
	out = backfillEmptyFunctionResponseNames(out)

	out = common.AttachDefaultSafetySettings(out, "safetySettings")
	return out, nil
}

// backfillEmptyFunctionResponseNames walks the contents array and for each
// model turn containing functionCall parts, records the call names in order.
// For the immediately following user/function turn containing functionResponse
// parts, any empty name is replaced with the corresponding call name.
func backfillEmptyFunctionResponseNames(data []byte) []byte {
	contents := util.GetGJSONBytesNoCopy(data, "contents")
	if !contents.Exists() {
		return data
	}
	canBatch := contents.IsArray()
	if canBatch {
		contents.ForEach(func(_, content ojson.Result) bool {
			parts := content.Get("parts")
			if parts.Exists() && !parts.IsArray() {
				canBatch = false
				return false
			}
			return true
		})
	}
	if !canBatch {
		return backfillEmptyFunctionResponseNamesLegacy(data, contents)
	}
	needsBackfill, excessResponseIndexes := geminiFunctionResponseNamesNeedBackfill(contents)
	if !needsBackfill {
		for _, contentIndex := range excessResponseIndexes {
			log.Debugf("more function responses than calls at contents[%d], skipping name backfill", contentIndex)
		}
		return data
	}

	changed := false
	contentItems := translatorcommon.NewRawArrayItems(contents.Get("#").Int())
	var pendingCallNames []string

	contents.ForEach(func(contentIdx, content ojson.Result) bool {
		role := content.Get("role").String()
		contentRaw := []byte(content.Raw)

		// Collect functionCall/function_call names from model turns.
		if role == "model" {
			var names []string
			content.Get("parts").ForEach(func(_, part ojson.Result) bool {
				if fc := translatorcommon.GeminiPartFunctionCall(part); fc.Exists() {
					names = append(names, fc.Get("name").String())
				}
				return true
			})
			pendingCallNames = names
			contentItems = append(contentItems, contentRaw)
			return true
		}

		// Backfill empty functionResponse/function_response names from pending call names.
		if len(pendingCallNames) > 0 {
			responseIndex := 0
			partsChanged := false
			partItems := make([][]byte, 0, 4)
			content.Get("parts").ForEach(func(_, part ojson.Result) bool {
				partRaw := []byte(part.Raw)
				if frKey := geminiFunctionResponseFieldKey(part); frKey != "" {
					name := part.Get(frKey + ".name").String()
					if strings.TrimSpace(name) == "" {
						if responseIndex < len(pendingCallNames) {
							partDoc := ojson.NewDocString(part.Raw)
							_ = partDoc.Set(frKey+".name", pendingCallNames[responseIndex])
							partRaw = partDoc.Bytes()
							partsChanged = true
						} else {
							log.Debugf("more function responses than calls at contents[%d], skipping name backfill", contentIdx.Int())
						}
					}
					responseIndex++
				}
				partItems = append(partItems, partRaw)
				return true
			})
			if partsChanged {
				contentDoc := ojson.NewDocString(content.Raw)
				_ = contentDoc.SetRawBytes("parts", translatorcommon.JoinRawArray(partItems))
				contentRaw = contentDoc.Bytes()
				changed = true
			}
			pendingCallNames = nil
		}

		contentItems = append(contentItems, contentRaw)
		return true
	})

	if !changed {
		return data
	}
	outDoc := ojson.NewDoc(data)
	if errSetContents := outDoc.SetRawBytes("contents", translatorcommon.JoinRawArray(contentItems)); errSetContents != nil {
		return data
	}
	return outDoc.Bytes()
}

func geminiFunctionResponseFieldKey(part ojson.Result) string {
	if part.Get("functionResponse").Exists() {
		return "functionResponse"
	}
	if part.Get("function_response").Exists() {
		return "function_response"
	}
	return ""
}

func geminiFunctionResponseNamesNeedBackfill(contents ojson.Result) (bool, []int64) {
	var pendingCallNames []string
	var excessResponseIndexes []int64
	needsBackfill := false
	contents.ForEach(func(contentIdx, content ojson.Result) bool {
		if content.Get("role").String() == "model" {
			var names []string
			content.Get("parts").ForEach(func(_, part ojson.Result) bool {
				if fc := translatorcommon.GeminiPartFunctionCall(part); fc.Exists() {
					names = append(names, fc.Get("name").String())
				}
				return true
			})
			pendingCallNames = names
			return true
		}
		if len(pendingCallNames) == 0 {
			return true
		}
		responseIndex := 0
		content.Get("parts").ForEach(func(_, part ojson.Result) bool {
			if frKey := geminiFunctionResponseFieldKey(part); frKey != "" {
				if strings.TrimSpace(part.Get(frKey+".name").String()) == "" {
					if responseIndex < len(pendingCallNames) {
						needsBackfill = true
						return false
					}
					excessResponseIndexes = append(excessResponseIndexes, contentIdx.Int())
				}
				responseIndex++
			}
			return true
		})
		pendingCallNames = nil
		return !needsBackfill
	})
	return needsBackfill, excessResponseIndexes
}

func backfillEmptyFunctionResponseNamesLegacy(data []byte, contents ojson.Result) []byte {
	var outDoc *ojson.Doc
	var pendingCallNames []string
	contents.ForEach(func(contentIdx, content ojson.Result) bool {
		if content.Get("role").String() == "model" {
			var names []string
			content.Get("parts").ForEach(func(_, part ojson.Result) bool {
				if fc := translatorcommon.GeminiPartFunctionCall(part); fc.Exists() {
					names = append(names, fc.Get("name").String())
				}
				return true
			})
			pendingCallNames = names
			return true
		}
		if len(pendingCallNames) > 0 {
			responseIndex := 0
			content.Get("parts").ForEach(func(partIdx, part ojson.Result) bool {
				if frKey := geminiFunctionResponseFieldKey(part); frKey != "" {
					if strings.TrimSpace(part.Get(frKey+".name").String()) == "" {
						if responseIndex < len(pendingCallNames) {
							path := fmt.Sprintf("contents.%d.parts.%d.%s.name", contentIdx.Int(), partIdx.Int(), frKey)
							if outDoc == nil {
								outDoc = ojson.NewDoc(data)
							}
							_ = outDoc.Set(path, pendingCallNames[responseIndex])
						} else {
							log.Debugf("more function responses than calls at contents[%d], skipping name backfill", contentIdx.Int())
						}
					}
					responseIndex++
				}
				return true
			})
			pendingCallNames = nil
		}
		return true
	})
	if outDoc != nil {
		return outDoc.Bytes()
	}
	return data
}

type geminiPendingCallGroup struct {
	callIDs   []string
	callNames []string
}

type geminiCallIDNameKey struct {
	id   string
	name string
}

func geminiResponsesNeedReordering(responses []ojson.Result, group geminiPendingCallGroup) bool {
	if len(responses) != len(group.callNames) || len(responses) <= 1 {
		return false
	}
	for i, resp := range responses {
		fr := translatorcommon.GeminiPartFunctionResponse(resp)
		callID := ""
		if i < len(group.callIDs) {
			callID = group.callIDs[i]
		}
		if callID != "" && fr.Get("id").String() != callID {
			return true
		}
		callName := group.callNames[i]
		respName := strings.TrimSpace(fr.Get("name").String())
		if callName != "" && respName != "" && respName != callName {
			return true
		}
	}
	return false
}

func geminiFunctionResponsesNeedGrouping(contents ojson.Result) bool {
	if !contents.IsArray() {
		return false
	}
	var pending geminiPendingCallGroup
	responseContentCount := 0
	hasDeferredOther := false
	needsGrouping := false

	resetPending := func() {
		pending = geminiPendingCallGroup{}
		responseContentCount = 0
		hasDeferredOther = false
	}

	contents.ForEach(func(_, content ojson.Result) bool {
		if !content.IsObject() {
			resetPending()
			return true
		}
		role := content.Get("role").String()
		parts := content.Get("parts")
		if role == "model" {
			var callIDs []string
			var callNames []string
			if parts.IsArray() {
				parts.ForEach(func(_, part ojson.Result) bool {
					if fc := translatorcommon.GeminiPartFunctionCall(part); fc.Exists() {
						callIDs = append(callIDs, fc.Get("id").String())
						callNames = append(callNames, fc.Get("name").String())
					}
					return true
				})
			}
			pending = geminiPendingCallGroup{callIDs: callIDs, callNames: callNames}
			responseContentCount = 0
			hasDeferredOther = false
			return true
		}
		if len(pending.callNames) == 0 {
			return true
		}
		// Empty, non-array, or functionCall-bearing non-model contents are invalid
		// boundaries in pairing validation; preserve them in place rather than
		// splitting or deferring them past pending responses.
		if !parts.IsArray() || !parts.Get("0").Exists() || translatorcommon.GeminiPartsHaveFunctionCall(parts) {
			resetPending()
			return true
		}

		var responses []ojson.Result
		hasOtherParts := false
		parts.ForEach(func(_, part ojson.Result) bool {
			if translatorcommon.GeminiPartFunctionResponse(part).Exists() {
				responses = append(responses, part)
			} else {
				hasOtherParts = true
			}
			return true
		})
		if len(responses) == 0 {
			hasDeferredOther = true
			return true
		}

		responseContentCount++
		if responseContentCount > 1 || hasDeferredOther || hasOtherParts {
			needsGrouping = true
			return false
		}
		if len(responses) == len(pending.callNames) && geminiResponsesNeedReordering(responses, pending) {
			needsGrouping = true
			return false
		}
		return true
	})

	return needsGrouping
}

func orderGeminiFunctionResponses(collected []ojson.Result, group geminiPendingCallGroup) ([]ojson.Result, []ojson.Result) {
	needed := len(group.callNames)
	if needed == 0 || len(collected) == 0 {
		return nil, collected
	}

	byIDAndName := make(map[geminiCallIDNameKey][]int, len(collected))
	byID := make(map[string][]int, len(collected))
	byName := make(map[string][]int, len(collected))
	var emptyNameIdxs []int
	for idx, resp := range collected {
		fr := translatorcommon.GeminiPartFunctionResponse(resp)
		id := fr.Get("id").String()
		name := strings.TrimSpace(fr.Get("name").String())
		if id != "" {
			byID[id] = append(byID[id], idx)
			if name != "" {
				key := geminiCallIDNameKey{id: id, name: name}
				byIDAndName[key] = append(byIDAndName[key], idx)
			}
		}
		if name != "" {
			byName[name] = append(byName[name], idx)
		} else {
			emptyNameIdxs = append(emptyNameIdxs, idx)
		}
	}

	used := make([]bool, len(collected))
	slots := make([]ojson.Result, needed)
	matchedSlot := make([]bool, needed)
	popUnused := func(queue []int) (int, []int, bool) {
		for len(queue) > 0 {
			idx := queue[0]
			queue = queue[1:]
			if !used[idx] {
				return idx, queue, true
			}
		}
		return 0, queue, false
	}

	// Prefer exact (id, name) matches first so parallel calls sharing a placeholder ID
	// still align with their corresponding tool names.
	for slot := 0; slot < needed; slot++ {
		if slot >= len(group.callIDs) || group.callIDs[slot] == "" || group.callNames[slot] == "" {
			continue
		}
		key := geminiCallIDNameKey{id: group.callIDs[slot], name: group.callNames[slot]}
		if idx, rest, ok := popUnused(byIDAndName[key]); ok {
			byIDAndName[key] = rest
			used[idx] = true
			slots[slot] = collected[idx]
			matchedSlot[slot] = true
		} else {
			delete(byIDAndName, key)
		}
	}

	for slot := 0; slot < needed; slot++ {
		if matchedSlot[slot] || slot >= len(group.callIDs) || group.callIDs[slot] == "" {
			continue
		}
		callID := group.callIDs[slot]
		if idx, rest, ok := popUnused(byID[callID]); ok {
			byID[callID] = rest
			used[idx] = true
			slots[slot] = collected[idx]
			matchedSlot[slot] = true
		} else {
			delete(byID, callID)
		}
	}

	for slot := 0; slot < needed; slot++ {
		if matchedSlot[slot] || group.callNames[slot] == "" {
			continue
		}
		callName := group.callNames[slot]
		if idx, rest, ok := popUnused(byName[callName]); ok {
			byName[callName] = rest
			used[idx] = true
			slots[slot] = collected[idx]
			matchedSlot[slot] = true
			continue
		}
		delete(byName, callName)
		if idx, rest, ok := popUnused(emptyNameIdxs); ok {
			emptyNameIdxs = rest
			used[idx] = true
			slots[slot] = collected[idx]
			matchedSlot[slot] = true
		} else {
			emptyNameIdxs = nil
		}
	}

	freeCursor := 0
	for slot := 0; slot < needed; slot++ {
		if matchedSlot[slot] {
			continue
		}
		for freeCursor < len(collected) && used[freeCursor] {
			freeCursor++
		}
		if freeCursor < len(collected) {
			used[freeCursor] = true
			slots[slot] = collected[freeCursor]
			matchedSlot[slot] = true
			freeCursor++
		}
	}

	selected := make([]ojson.Result, 0, len(collected))
	for slot := 0; slot < needed; slot++ {
		if matchedSlot[slot] {
			selected = append(selected, slots[slot])
		}
	}
	remaining := make([]ojson.Result, 0)
	for idx, resp := range collected {
		if !used[idx] {
			remaining = append(remaining, resp)
		}
	}
	return selected, remaining
}

// groupGeminiFunctionResponses merges parallel functionResponse parts that clients split across
// consecutive user/function content turns into a single user turn aligned with the preceding
// model turn's functionCall order. Incomplete response groups are preserved rather than dropped
// so downstream pairing validation still reports genuine history corruption accurately.
func groupGeminiFunctionResponses(data []byte) []byte {
	contents := util.GetGJSONBytesNoCopy(data, "contents")
	if !geminiFunctionResponsesNeedGrouping(contents) {
		return data
	}

	contentItems := translatorcommon.NewRawArrayItems(contents.Get("#").Int())
	var pending geminiPendingCallGroup
	var collectedResponses []ojson.Result
	var deferredOtherContents [][]byte

	buildUserResponseContent := func(responses []ojson.Result) []byte {
		partItems := make([][]byte, 0, len(responses))
		for _, resp := range responses {
			partItems = append(partItems, []byte(resp.Raw))
		}
		contentDoc := ojson.NewDocString(`{"role":"user","parts":[]}`)
		_ = contentDoc.SetRawBytes("parts", translatorcommon.JoinRawArray(partItems))
		return contentDoc.Bytes()
	}

	flushDeferred := func() {
		if len(deferredOtherContents) == 0 {
			return
		}
		contentItems = append(contentItems, deferredOtherContents...)
		deferredOtherContents = nil
	}

	emitResponses := func(collected []ojson.Result, group geminiPendingCallGroup) {
		ordered, remaining := orderGeminiFunctionResponses(collected, group)
		if len(remaining) > 0 {
			ordered = append(ordered, remaining...)
		}
		if len(ordered) > 0 {
			contentItems = append(contentItems, buildUserResponseContent(ordered))
		}
	}

	flushPending := func() {
		if len(collectedResponses) > 0 {
			emitResponses(collectedResponses, pending)
			collectedResponses = nil
		}
		pending = geminiPendingCallGroup{}
		flushDeferred()
	}

	contents.ForEach(func(_, value ojson.Result) bool {
		if !value.IsObject() {
			flushPending()
			contentItems = append(contentItems, []byte(value.Raw))
			return true
		}
		parts := value.Get("parts")
		role := value.Get("role").String()

		if role == "model" {
			flushPending()
			var callIDs []string
			var callNames []string
			if parts.IsArray() {
				parts.ForEach(func(_, part ojson.Result) bool {
					if fc := translatorcommon.GeminiPartFunctionCall(part); fc.Exists() {
						callIDs = append(callIDs, fc.Get("id").String())
						callNames = append(callNames, fc.Get("name").String())
					}
					return true
				})
			}
			contentItems = append(contentItems, []byte(value.Raw))
			if len(callNames) > 0 {
				pending = geminiPendingCallGroup{callIDs: callIDs, callNames: callNames}
			}
			return true
		}

		if len(pending.callNames) == 0 {
			contentItems = append(contentItems, []byte(value.Raw))
			return true
		}

		// Preserve empty, non-array, or functionCall-bearing non-model contents in place
		// as boundaries so pairing validation still rejects interleaved or premature calls.
		if !parts.IsArray() || !parts.Get("0").Exists() || translatorcommon.GeminiPartsHaveFunctionCall(parts) {
			flushPending()
			contentItems = append(contentItems, []byte(value.Raw))
			return true
		}

		var responses []ojson.Result
		var otherParts [][]byte
		parts.ForEach(func(_, part ojson.Result) bool {
			if translatorcommon.GeminiPartFunctionResponse(part).Exists() {
				responses = append(responses, part)
			} else {
				otherParts = append(otherParts, []byte(part.Raw))
			}
			return true
		})

		if len(responses) == 0 {
			deferredOtherContents = append(deferredOtherContents, []byte(value.Raw))
			return true
		}

		collectedResponses = append(collectedResponses, responses...)
		if len(otherParts) > 0 {
			otherDoc := ojson.NewDocString(value.Raw)
			_ = otherDoc.Set("role", "user")
			_ = otherDoc.SetRawBytes("parts", translatorcommon.JoinRawArray(otherParts))
			deferredOtherContents = append(deferredOtherContents, otherDoc.Bytes())
		}
		return true
	})

	flushPending()

	outDoc := ojson.NewDoc(data)
	if errSet := outDoc.SetRawBytes("contents", translatorcommon.JoinRawArray(contentItems)); errSet != nil {
		return data
	}
	return outDoc.Bytes()
}

func nextGeminiRole(previousRole string) string {
	if previousRole == "" || previousRole == "model" {
		return "user"
	}
	return "model"
}
