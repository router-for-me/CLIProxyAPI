package responses

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

func ConvertOpenAIResponsesRequestToCodex(modelName string, inputRawJSON []byte, _ bool) ([]byte, error) {
	rawJSON := inputRawJSON
	var doc *ojson.Doc
	ensureDoc := func() *ojson.Doc {
		if doc == nil {
			doc = ojson.NewDoc(rawJSON)
		}
		return doc
	}

	inputResult := util.GetGJSONBytesNoCopy(rawJSON, "input")
	if inputResult.Type == ojson.String {
		input, _ := ojson.SetBytes([]byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}]`), "0.content.0.text", inputResult.String())
		_ = ensureDoc().SetRawBytes("input", input)
		inputResult = util.ParseGJSONBytesNoCopy(input)
	}

	setCodexRequiredBoolDoc(rawJSON, ensureDoc, "stream", true)
	setCodexRequiredBoolDoc(rawJSON, ensureDoc, "store", false)
	if ojson.GetBytesNoCopy(rawJSON, "parallel_tool_calls").Type != ojson.False {
		setCodexRequiredBoolDoc(rawJSON, ensureDoc, "parallel_tool_calls", true)
	}
	setCodexRequiredIncludeDoc(rawJSON, ensureDoc)
	// Preserve max_output_tokens and fill it from legacy token limits when unset.
	if maxOutputTokens := ojson.GetBytesNoCopy(rawJSON, "max_output_tokens"); !maxOutputTokens.Exists() || maxOutputTokens.Type == ojson.Null {
		for _, path := range []string{"max_completion_tokens", "max_tokens"} {
			if v := ojson.GetBytesNoCopy(rawJSON, path); v.Type == ojson.Number {
				_ = ensureDoc().Set("max_output_tokens", v.Int())
				break
			}
		}
	}
	deleteCodexRequestFieldsDoc(rawJSON, ensureDoc, "max_completion_tokens", "max_tokens", "temperature", "top_p")
	if serviceTier := ojson.GetBytesNoCopy(rawJSON, "service_tier"); serviceTier.Exists() {
		if serviceTier.Type == ojson.String {
			switch strings.ToLower(strings.TrimSpace(serviceTier.String())) {
			case "priority", "fast":
				if serviceTier.String() != "priority" {
					_ = ensureDoc().Set("service_tier", "priority")
				}
			case "ultrafast":
				if serviceTier.String() != "ultrafast" {
					_ = ensureDoc().Set("service_tier", "ultrafast")
				}
			default:
				_ = ensureDoc().Delete("service_tier")
			}
		} else {
			_ = ensureDoc().Delete("service_tier")
		}
	}

	deleteCodexRequestFieldsDoc(rawJSON, ensureDoc, "truncation", "prompt_cache_options", "prompt_cache_retention")
	inputResult = stripCodexResponsesCacheBreakpointsDoc(rawJSON, inputResult, ensureDoc)
	applyResponsesCompactionCompatibilityDoc(rawJSON, ensureDoc)

	// Delete the user field as it is not supported by the Codex upstream.
	deleteCodexRequestFieldsDoc(rawJSON, ensureDoc, "user")

	// Convert role "system" to "developer" in input array to comply with Codex API requirements.
	inputResult = convertSystemRoleToDeveloperDoc(inputResult, ensureDoc)
	normalizeCodexBuiltinToolsDoc(rawJSON, ensureDoc)
	normalizeEmptyFunctionCallArgumentsDoc(inputResult, ensureDoc)

	if doc != nil {
		return doc.Bytes(), nil
	}
	return rawJSON, nil
}

func normalizeCodexServiceTier(result ojson.Result) string {
	if !result.Exists() || result.Type != ojson.String {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(result.String())) {
	case "fast", "priority":
		return "priority"
	default:
		return ""
	}
}

// normalizeEmptyFunctionCallArgumentsDoc rewrites blank string arguments on
// history function_call items to "{}". Some Responses clients serialize
// parameter-less tool calls as an empty string, which strict Codex Responses
// upstreams reject with "`arguments` must be valid JSON". Only blank strings
// are touched: non-empty strings (even invalid JSON) pass through unchanged
// so other errors keep their original shape, and custom_tool_call items use
// the separate input field and are never affected.
func normalizeEmptyFunctionCallArgumentsDoc(inputResult ojson.Result, ensureDoc func() *ojson.Doc) {
	if !inputResult.IsArray() {
		return
	}
	items := inputResult.Array()
	if len(items) == 0 {
		return
	}
	changed := false
	rebuilt := make([][]byte, 0, len(items))
	for _, item := range items {
		itemRaw := []byte(item.Raw)
		if item.IsObject() && item.Get("type").String() == "function_call" {
			if args := item.Get("arguments"); args.Type == ojson.String && strings.TrimSpace(args.String()) == "" {
				if updated, errSet := ojson.SetBytes(itemRaw, "arguments", "{}"); errSet == nil {
					itemRaw = updated
					changed = true
				}
			}
		}
		rebuilt = append(rebuilt, itemRaw)
	}
	if !changed {
		return
	}
	_ = ensureDoc().SetRawBytes("input", translatorcommon.JoinRawArray(rebuilt))
}

func setCodexRequiredBoolDoc(rawJSON []byte, ensureDoc func() *ojson.Doc, path string, value bool) {
	current := ojson.GetBytesNoCopy(rawJSON, path)
	if value && current.Type == ojson.True || !value && current.Type == ojson.False {
		return
	}
	_ = ensureDoc().Set(path, value)
}

func setCodexRequiredIncludeDoc(rawJSON []byte, ensureDoc func() *ojson.Doc) {
	current := ojson.GetBytesNoCopy(rawJSON, "include")
	includeSources := false
	if current.IsArray() {
		values := current.Array()
		for _, value := range values {
			if value.Type == ojson.String && value.String() == "web_search_call.action.sources" {
				includeSources = true
				break
			}
		}
		if !includeSources && len(values) == 1 && values[0].Type == ojson.String && values[0].String() == "reasoning.encrypted_content" {
			return
		}
		if includeSources && len(values) == 2 &&
			values[0].Type == ojson.String && values[0].String() == "reasoning.encrypted_content" &&
			values[1].Type == ojson.String && values[1].String() == "web_search_call.action.sources" {
			return
		}
	}

	encoded := []byte(`["reasoning.encrypted_content"]`)
	if includeSources {
		encoded = []byte(`["reasoning.encrypted_content","web_search_call.action.sources"]`)
	}
	_ = ensureDoc().SetRawBytes("include", encoded)
}

func deleteCodexRequestFieldsDoc(rawJSON []byte, ensureDoc func() *ojson.Doc, paths ...string) {
	for _, path := range paths {
		if !ojson.GetBytesNoCopy(rawJSON, path).Exists() {
			continue
		}
		_ = ensureDoc().Delete(path)
	}
}

// stripCodexResponsesCacheBreakpointsDoc removes any "prompt_cache_breakpoint" hint
// attached to input items: inside content-part arrays (message input[].content[]
// and function_call_output input[].output[]) or as an item-level field. Some
// clients (e.g. GitHub Copilot CLI) attach this field per content item when
// targeting the OpenAI Responses format. Codex Responses rejects it outright:
// {"error":{"message":"prompt_cache_breakpoint is not supported on this model", ...}}.
// The top-level prompt_cache_options strip above does not cover these nested cases.
func stripCodexResponsesCacheBreakpointsDoc(rawJSON []byte, input ojson.Result, ensureDoc func() *ojson.Doc) ojson.Result {
	if !bytes.Contains(rawJSON, []byte(`"prompt_cache_breakpoint"`)) {
		return input
	}
	if !input.IsArray() {
		return input
	}

	inputItems := input.Array()
	if len(inputItems) == 0 {
		return input
	}

	changed := false
	rebuiltInput := make([][]byte, 0, len(inputItems))
	for _, item := range inputItems {
		var itemDoc *ojson.Doc
		ensureItemDoc := func() *ojson.Doc {
			if itemDoc == nil {
				itemDoc = ojson.NewDocString(item.Raw)
			}
			return itemDoc
		}
		for _, arrayPath := range []string{"content", "output"} {
			arrayResult := item.Get(arrayPath)
			if !arrayResult.IsArray() {
				continue
			}
			updatedArray, arrayChanged := stripPromptCacheBreakpointFromContent(arrayResult)
			if !arrayChanged {
				continue
			}
			if errSet := ensureItemDoc().SetRawBytes(arrayPath, updatedArray); errSet == nil {
				changed = true
			}
		}
		if item.Get("prompt_cache_breakpoint").Exists() {
			if errDelete := ensureItemDoc().Delete("prompt_cache_breakpoint"); errDelete == nil {
				changed = true
			}
		}
		if itemDoc != nil {
			rebuiltInput = append(rebuiltInput, itemDoc.Bytes())
		} else {
			rebuiltInput = append(rebuiltInput, []byte(item.Raw))
		}
	}
	if !changed {
		return input
	}

	joined := translatorcommon.JoinRawArray(rebuiltInput)
	_ = ensureDoc().SetRawBytes("input", joined)
	return util.ParseGJSONBytesNoCopy(joined)
}

// stripPromptCacheBreakpointFromContent removes "prompt_cache_breakpoint" from each
// content part that carries it and reports whether anything changed.
func stripPromptCacheBreakpointFromContent(content ojson.Result) ([]byte, bool) {
	parts := content.Array()
	hasBreakpoint := false
	for _, part := range parts {
		if part.Get("prompt_cache_breakpoint").Exists() {
			hasBreakpoint = true
			break
		}
	}
	if !hasBreakpoint {
		return nil, false
	}

	changed := false
	rebuiltParts := make([][]byte, 0, len(parts))
	for _, part := range parts {
		partRaw := []byte(part.Raw)
		if part.Get("prompt_cache_breakpoint").Exists() {
			if updated, errDelete := ojson.DeleteBytes(partRaw, "prompt_cache_breakpoint"); errDelete == nil {
				partRaw = updated
				changed = true
			}
		}
		rebuiltParts = append(rebuiltParts, partRaw)
	}
	if !changed {
		return nil, false
	}
	return translatorcommon.JoinRawArray(rebuiltParts), true
}

// applyResponsesCompactionCompatibility handles OpenAI Responses context_management.compaction
// for Codex upstream compatibility.
//
// Codex /responses currently rejects context_management with:
// {"detail":"Unsupported parameter: context_management"}.
//
// Compatibility strategy:
// 1) Remove context_management before forwarding to Codex upstream.
func applyResponsesCompactionCompatibilityDoc(rawJSON []byte, ensureDoc func() *ojson.Doc) {
	if !ojson.GetBytesNoCopy(rawJSON, "context_management").Exists() {
		return
	}
	_ = ensureDoc().Delete("context_management")
}

// convertSystemRoleToDeveloper traverses the input array and converts any message items
// with role "system" to role "developer". This is necessary because Codex API does not
// accept "system" role in the input array.
func convertSystemRoleToDeveloper(rawJSON []byte) []byte {
	return convertSystemRoleToDeveloperWithInput(rawJSON, util.GetGJSONBytesNoCopy(rawJSON, "input"))
}

func convertSystemRoleToDeveloperDoc(inputResult ojson.Result, ensureDoc func() *ojson.Doc) ojson.Result {
	if !inputResult.IsArray() {
		return inputResult
	}

	inputItems := inputResult.Array()
	if len(inputItems) == 0 {
		return inputResult
	}

	hasSystemRole := false
	for _, item := range inputItems {
		if item.IsObject() && item.Get("role").String() == "system" {
			hasSystemRole = true
			break
		}
	}
	if !hasSystemRole {
		return inputResult
	}

	changed := false
	rebuiltInput := make([]json.RawMessage, 0, len(inputItems))
	for _, item := range inputItems {
		itemRaw := []byte(item.Raw)
		if item.IsObject() && item.Get("role").String() == "system" {
			updatedItem, errSetItem := ojson.SetRawBytes(itemRaw, "role", []byte(`"developer"`))
			if errSetItem != nil {
				return inputResult
			}
			itemRaw = updatedItem
			changed = true
		}
		rebuiltInput = append(rebuiltInput, json.RawMessage(itemRaw))
	}
	if !changed {
		return inputResult
	}

	inputRaw, errMarshalInput := json.Marshal(rebuiltInput)
	if errMarshalInput != nil {
		return inputResult
	}
	_ = ensureDoc().SetRawBytes("input", inputRaw)
	return util.ParseGJSONBytesNoCopy(inputRaw)
}

func convertSystemRoleToDeveloperWithInput(rawJSON []byte, inputResult ojson.Result) []byte {
	if !inputResult.IsArray() {
		return rawJSON
	}

	inputItems := inputResult.Array()
	if len(inputItems) == 0 {
		return rawJSON
	}

	hasSystemRole := false
	for _, item := range inputItems {
		if item.IsObject() && item.Get("role").String() == "system" {
			hasSystemRole = true
			break
		}
	}
	if !hasSystemRole {
		return rawJSON
	}

	changed := false
	rebuiltInput := make([]json.RawMessage, 0, len(inputItems))
	for _, item := range inputItems {
		itemRaw := []byte(item.Raw)
		if item.IsObject() && item.Get("role").String() == "system" {
			updatedItem, errSetItem := ojson.SetRawBytes(itemRaw, "role", []byte(`"developer"`))
			if errSetItem != nil {
				return rawJSON
			}
			itemRaw = updatedItem
			changed = true
		}
		rebuiltInput = append(rebuiltInput, json.RawMessage(itemRaw))
	}
	if !changed {
		return rawJSON
	}

	inputRaw, errMarshalInput := json.Marshal(rebuiltInput)
	if errMarshalInput != nil {
		return rawJSON
	}
	updated, errSetInput := ojson.SetRawBytes(rawJSON, "input", inputRaw)
	if errSetInput != nil {
		return rawJSON
	}
	return updated
}

// normalizeCodexBuiltinToolsDoc rewrites legacy/preview built-in tool variants to the
// stable names expected by the current Codex upstream.
func normalizeCodexBuiltinToolsDoc(rawJSON []byte, ensureDoc func() *ojson.Doc) {
	normalizeCodexBuiltinToolArrayDoc(rawJSON, ensureDoc, "tools")
	normalizeCodexBuiltinToolAtPathDoc(rawJSON, ensureDoc, "tool_choice.type")
	normalizeCodexBuiltinToolArrayDoc(rawJSON, ensureDoc, "tool_choice.tools")
}

func normalizeCodexBuiltinToolArrayDoc(rawJSON []byte, ensureDoc func() *ojson.Doc, path string) {
	tools := ojson.GetBytesNoCopy(rawJSON, path)
	if !tools.IsArray() {
		return
	}

	changed := false
	var toolItems [][]byte
	tools.ForEach(func(_, tool ojson.Result) bool {
		item := []byte(tool.Raw)
		currentType := tool.Get("type").String()
		normalizedType := normalizeCodexBuiltinToolType(currentType)
		if normalizedType != "" {
			updated, errSetType := ojson.SetBytes(item, "type", normalizedType)
			if errSetType == nil {
				item = updated
				changed = true
				log.Debugf("codex responses: normalized builtin tool type at %s.%d.type from %q to %q", path, len(toolItems), currentType, normalizedType)
			}
		}
		toolItems = append(toolItems, item)
		return true
	})
	if !changed {
		return
	}

	_ = ensureDoc().SetRawBytes(path, translatorcommon.JoinRawArray(toolItems))
}

func normalizeCodexBuiltinToolAtPathDoc(rawJSON []byte, ensureDoc func() *ojson.Doc, path string) {
	currentType := ojson.GetBytesNoCopy(rawJSON, path).String()
	normalizedType := normalizeCodexBuiltinToolType(currentType)
	if normalizedType == "" {
		return
	}

	if errSet := ensureDoc().Set(path, normalizedType); errSet != nil {
		return
	}

	log.Debugf("codex responses: normalized builtin tool type at %s from %q to %q", path, currentType, normalizedType)
}

// normalizeCodexBuiltinToolType centralizes the current known Codex Responses
// built-in tool alias compatibility. If Codex introduces more legacy aliases,
// extend this helper instead of adding path-specific rewrite logic elsewhere.
func normalizeCodexBuiltinToolType(toolType string) string {
	switch toolType {
	case "web_search_preview", "web_search_preview_2025_03_11":
		return "web_search"
	default:
		return ""
	}
}
