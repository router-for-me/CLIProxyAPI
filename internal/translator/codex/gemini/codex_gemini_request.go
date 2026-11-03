// Package gemini provides request translation functionality for Codex to Gemini API compatibility.
// It handles parsing and transforming Codex API requests into Gemini API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Codex API format and Gemini API's expected format.
package gemini

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// ConvertGeminiRequestToCodex parses and transforms a Gemini API request into Codex API format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the Codex API.
// The function performs comprehensive transformation including:
// 1. Model name mapping and generation configuration extraction
// 2. System instruction conversion to Codex format
// 3. Message content conversion with proper role mapping
// 4. Tool call and tool result handling with FIFO queue for ID matching
// 5. Tool declaration and tool choice configuration mapping
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the Gemini API
//   - stream: A boolean indicating if the request is for a streaming response (unused in current implementation)
//
// Returns:
//   - []byte: The transformed request data in Codex API format
func ConvertGeminiRequestToCodex(modelName string, inputRawJSON []byte, _ bool) ([]byte, error) {
	rawJSON := inputRawJSON
	// Base template
	outDoc := ojson.NewDocString(`{"model":"","instructions":"","input":[]}`)

	root := util.ParseGJSONBytesNoCopy(rawJSON)
	inputItems := translatorcommon.NewRawArrayItems(root.Get("contents.#").Int())

	// Pre-compute tool name shortening map from declared functionDeclarations
	shortMap := map[string]string{}
	if tools := root.Get("tools"); tools.IsArray() {
		var names []string
		tarr := tools.Array()
		for i := 0; i < len(tarr); i++ {
			fns := tarr[i].Get("functionDeclarations")
			if !fns.IsArray() {
				continue
			}
			for _, fn := range fns.Array() {
				if v := fn.Get("name"); v.Exists() {
					names = append(names, v.String())
				}
			}
		}
		if len(names) > 0 {
			shortMap = buildShortNameMap(names)
		}
	}

	// helper for generating paired call IDs in the form: call_gemini_<seq>
	// Gemini uses sequential pairing across possibly multiple in-flight
	// functionCalls, so we keep a FIFO queue of generated call IDs and
	// consume them in order when functionResponses arrive.
	var pendingCallIDs []string
	callCounter := 0
	emittedThoughtSignatures := map[string]bool{}
	signatureTargetModel := translatorcommon.ResolveCodexReplayTargetModel(modelName, inputRawJSON)

	getGeminiCallID := func(value ojson.Result) string {
		if callID := strings.TrimSpace(value.Get("id").String()); callID != "" {
			return callID
		}
		return strings.TrimSpace(value.Get("call_id").String())
	}

	removePendingCallID := func(ids []string, callID string) []string {
		if callID == "" {
			return ids
		}
		for idx, pendingID := range ids {
			if pendingID == callID {
				return append(ids[:idx], ids[idx+1:]...)
			}
		}
		return ids
	}

	// Model
	_ = outDoc.Set("model", modelName)
	if serviceTier := normalizeGeminiCodexServiceTier(root.Get("service_tier")); serviceTier != "" {
		_ = outDoc.Set("service_tier", serviceTier)
	}

	// System instruction -> as a user message with input_text parts
	sysParts := root.Get("system_instruction.parts")
	if !sysParts.Exists() {
		sysParts = root.Get("systemInstruction.parts")
	}
	if sysParts.IsArray() {
		contentItems := make([][]byte, 0, 2)
		arr := sysParts.Array()
		for i := 0; i < len(arr); i++ {
			p := arr[i]
			if translatorcommon.IsGeminiThoughtPart(p) {
				continue
			}
			if t := p.Get("text"); t.Exists() {
				partDoc := ojson.NewDocString(`{}`)
				_ = partDoc.Set("type", "input_text")
				_ = partDoc.Set("text", t.String())
				contentItems = append(contentItems, partDoc.Bytes())
			}
		}
		if len(contentItems) > 0 {
			msg := []byte(`{"type":"message","role":"developer","content":[]}`)
			msg, _ = ojson.SetRawBytes(msg, "content", translatorcommon.JoinRawArray(contentItems))
			inputItems = append(inputItems, msg)
		}
	}

	// Contents -> messages and function calls/results
	contents := root.Get("contents")
	if contents.IsArray() {
		items := contents.Array()
		previousRole := ""
		for i := 0; i < len(items); i++ {
			item := items[i]
			parts := item.Get("parts")
			if !parts.IsArray() {
				continue
			}

			role := translatorcommon.NormalizeGeminiTurnRole(item.Get("role").String(), parts, previousRole)
			previousRole = role
			parr := parts.Array()
			var pendingThoughtSummaryParts [][]byte
			for j := 0; j < len(parr); j++ {
				p := parr[j]

				// A thoughtSignature is replayable reasoning state, not visible
				// content. Restore it as a reasoning item only when its detected
				// family matches this request's target model family, so a Muse
				// (or GPT/Grok) signature can never be replayed to a wrong upstream
				// through the Gemini ingress. Unmatched signatures fall through to
				// the existing hidden-thought drop below.
				if role == "assistant" {
					if rawSignature := geminiPartThoughtSignature(p); rawSignature != "" {
						if normalizedSignature, ok := normalizedGeminiThoughtSignatureForCodexTarget(signatureTargetModel, rawSignature); ok && !emittedThoughtSignatures[normalizedSignature] {
							reasoningItemDoc := ojson.NewDocString(`{"type":"reasoning","encrypted_content":"","summary":[]}`)
							_ = reasoningItemDoc.Set("encrypted_content", normalizedSignature)
							summaryParts := pendingThoughtSummaryParts
							pendingThoughtSummaryParts = nil
							if translatorcommon.IsGeminiThoughtPart(p) {
								if thoughtText := p.Get("text").String(); thoughtText != "" {
									summaryPart := []byte(`{"type":"summary_text","text":""}`)
									summaryPart, _ = ojson.SetBytes(summaryPart, "text", thoughtText)
									summaryParts = append(summaryParts, summaryPart)
								}
							}
							if len(summaryParts) > 0 {
								_ = reasoningItemDoc.SetRawBytes("summary", translatorcommon.JoinRawArray(summaryParts))
							}
							inputItems = append(inputItems, reasoningItemDoc.Bytes())
							emittedThoughtSignatures[normalizedSignature] = true
						} else {
							pendingThoughtSummaryParts = nil
						}
					} else if translatorcommon.IsGeminiThoughtPart(p) {
						if thoughtText := p.Get("text").String(); thoughtText != "" {
							summaryPart := []byte(`{"type":"summary_text","text":""}`)
							summaryPart, _ = ojson.SetBytes(summaryPart, "text", thoughtText)
							pendingThoughtSummaryParts = append(pendingThoughtSummaryParts, summaryPart)
						}
					} else {
						pendingThoughtSummaryParts = nil
					}
				}

				if translatorcommon.IsGeminiThoughtPart(p) {
					continue
				}

				// text part
				if t := p.Get("text"); t.Exists() {
					partType := "input_text"
					if role == "assistant" {
						partType = "output_text"
					}
					partDoc := ojson.NewDocString(`{}`)
					_ = partDoc.Set("type", partType)
					_ = partDoc.Set("text", t.String())
					inputItems = append(inputItems, codexMessageWithPart(role, partDoc.Bytes()))
					continue
				}

				if contentPart, ok := codexContentPartFromGeminiInlineData(p); ok {
					inputItems = append(inputItems, codexMessageWithPart(role, contentPart))
					continue
				}

				if contentPart, ok := codexContentPartFromGeminiFileData(p); ok {
					inputItems = append(inputItems, codexMessageWithPart(role, contentPart))
					continue
				}

				// function call from model
				if fc := translatorcommon.GeminiPartFunctionCall(p); fc.Exists() {
					fnDoc := ojson.NewDocString(`{"type":"function_call"}`)
					if name := fc.Get("name"); name.Exists() {
						n := name.String()
						if short, ok := shortMap[n]; ok {
							n = short
						} else {
							n = shortenNameIfNeeded(n)
						}
						_ = fnDoc.Set("name", n)
					}
					if args := fc.Get("args"); args.Exists() {
						_ = fnDoc.Set("arguments", args.Raw)
					}
					// Reuse gateway-provided IDs when present, otherwise generate one for pairing.
					id := getGeminiCallID(fc)
					if id == "" {
						callCounter++
						id = fmt.Sprintf("call_gemini_%016d", callCounter)
					}
					_ = fnDoc.Set("call_id", id)
					pendingCallIDs = append(pendingCallIDs, id)
					inputItems = append(inputItems, fnDoc.Bytes())
					continue
				}

				// function response from user
				if fr := translatorcommon.GeminiPartFunctionResponse(p); fr.Exists() {
					fnoDoc := ojson.NewDocString(`{"type":"function_call_output"}`)
					// Prefer a string result if present; otherwise embed the raw response as a string
					if res := fr.Get("response.result"); res.Exists() {
						_ = fnoDoc.Set("output", res.String())
					} else if resp := fr.Get("response"); resp.Exists() {
						_ = fnoDoc.Set("output", resp.Raw)
					}
					// attach the oldest queued call_id to pair the response
					// with its call. If the queue is empty, generate a new id.
					var id string
					if customID := getGeminiCallID(fr); customID != "" {
						id = customID
						pendingCallIDs = removePendingCallID(pendingCallIDs, id)
					} else if len(pendingCallIDs) > 0 {
						id = pendingCallIDs[0]
						// pop the first element
						pendingCallIDs = pendingCallIDs[1:]
					} else {
						callCounter++
						id = fmt.Sprintf("call_gemini_%016d", callCounter)
					}
					_ = fnoDoc.Set("call_id", id)
					inputItems = append(inputItems, fnoDoc.Bytes())
					continue
				}
			}
		}
	}

	translatorcommon.SetRawArrayItemsDoc(outDoc, "input", inputItems)

	// Tools mapping: Gemini functionDeclarations -> Codex tools
	tools := root.Get("tools")
	hasTools := false
	if tools.IsArray() {
		hasTools = true
		var toolItems [][]byte
		_ = outDoc.Set("tool_choice", "auto")
		tarr := tools.Array()
		for i := 0; i < len(tarr); i++ {
			td := tarr[i]
			fns := td.Get("functionDeclarations")
			if !fns.IsArray() {
				continue
			}
			farr := fns.Array()
			for j := 0; j < len(farr); j++ {
				fn := farr[j]
				toolDoc := ojson.NewDocString(`{}`)
				_ = toolDoc.Set("type", "function")
				if v := fn.Get("name"); v.Exists() {
					name := v.String()
					if short, ok := shortMap[name]; ok {
						name = short
					} else {
						name = shortenNameIfNeeded(name)
					}
					_ = toolDoc.Set("name", name)
				}
				if v := fn.Get("description"); v.Exists() {
					_ = toolDoc.Set("description", v.String())
				}
				if prm := fn.Get("parameters"); prm.Exists() {
					cleaned := cleanGeminiCodexToolParameters(prm)
					_ = toolDoc.SetRawBytes("parameters", cleaned)
				} else if prm = fn.Get("parametersJsonSchema"); prm.Exists() {
					cleaned := cleanGeminiCodexToolParameters(prm)
					_ = toolDoc.SetRawBytes("parameters", cleaned)
				}
				_ = toolDoc.Set("strict", false)
				toolItems = append(toolItems, toolDoc.Bytes())
			}
		}
		toolsRaw := translatorcommon.JoinRawArray(toolItems)
		var pathsToLower []string
		toolsResult := util.ParseGJSONBytesNoCopy(toolsRaw)
		util.Walk(toolsResult, "", "type", &pathsToLower)
		if len(pathsToLower) > 0 {
			toolsDoc := ojson.NewDoc(toolsRaw)
			mutatedTools := false
			for _, p := range pathsToLower {
				typeValue := toolsResult.Get(p)
				if typeValue.Type != ojson.String {
					continue
				}
				normalizedType := strings.ToLower(typeValue.String())
				if normalizedType == typeValue.String() {
					continue
				}
				_ = toolsDoc.Set(p, normalizedType)
				mutatedTools = true
			}
			if mutatedTools {
				toolsRaw = toolsDoc.Bytes()
			}
		}
		_ = outDoc.SetRawBytes("tools", toolsRaw)
	}

	// Fixed flags aligning with Codex expectations
	_ = outDoc.Set("parallel_tool_calls", true)
	setCodexToolChoiceFromGeminiToolConfigDoc(outDoc, hasTools, root.Get("toolConfig.functionCallingConfig"))

	genConfig := root.Get("generationConfig")
	if !genConfig.Exists() {
		genConfig = root.Get("generation_config")
	}
	for _, v := range []ojson.Result{
		genConfig.Get("maxOutputTokens"),
		genConfig.Get("max_output_tokens"),
		root.Get("maxOutputTokens"),
		root.Get("max_output_tokens"),
		root.Get("max_tokens"),
	} {
		if v.Type == ojson.Number {
			_ = outDoc.Set("max_output_tokens", v.Int())
			break
		}
	}

	// Convert Gemini thinkingConfig to Codex reasoning.effort.
	// Note: Google official Python SDK sends snake_case fields (thinking_level/thinking_budget).
	effortSet := false
	if genConfig.Exists() {
		thinkingLevel := genConfig.Get("thinkingLevel")
		if !thinkingLevel.Exists() {
			thinkingLevel = genConfig.Get("thinking_level")
		}
		if thinkingLevel.Exists() {
			effort := strings.ToLower(strings.TrimSpace(thinkingLevel.String()))
			if effort != "" {
				_ = outDoc.Set("reasoning.effort", effort)
				effortSet = true
			}
		} else if thinkingConfig := genConfig.Get("thinkingConfig"); thinkingConfig.Exists() && thinkingConfig.IsObject() {
			thinkingLevel := thinkingConfig.Get("thinkingLevel")
			if !thinkingLevel.Exists() {
				thinkingLevel = thinkingConfig.Get("thinking_level")
			}
			if thinkingLevel.Exists() {
				effort := strings.ToLower(strings.TrimSpace(thinkingLevel.String()))
				if effort != "" {
					_ = outDoc.Set("reasoning.effort", effort)
					effortSet = true
				}
			} else {
				thinkingBudget := thinkingConfig.Get("thinkingBudget")
				if !thinkingBudget.Exists() {
					thinkingBudget = thinkingConfig.Get("thinking_budget")
				}
				if thinkingBudget.Exists() {
					if effort, ok := thinking.ConvertBudgetToLevel(int(thinkingBudget.Int())); ok {
						_ = outDoc.Set("reasoning.effort", effort)
						effortSet = true
					}
				}
			}
		}
	}
	if !effortSet {
		// No thinking config, set default effort
		_ = outDoc.Set("reasoning.effort", "medium")
	}
	// OpenAI documents reasoning summaries as explicit opt-in output. Leave
	// reasoning.summary to the source request's canonical summary intent instead
	// of coupling it to reasoning effort.
	_ = outDoc.Set("stream", true)
	_ = outDoc.Set("store", false)
	_ = outDoc.Set("include", []string{"reasoning.encrypted_content"})

	return outDoc.Bytes(), nil
}

// geminiPartThoughtSignature extracts a part's thought signature under either
// the camelCase or snake_case field spelling.
func geminiPartThoughtSignature(part ojson.Result) string {
	if signature := strings.TrimSpace(part.Get("thoughtSignature").String()); signature != "" {
		return signature
	}
	return strings.TrimSpace(part.Get("thought_signature").String())
}

// decodeGeminiCodexThoughtSignature unwraps the carrier written by
// encodeGeminiCodexThoughtSignature. Values without the carrier magic are
// returned unchanged, preserving legacy raw-signature echoes.
func decodeGeminiCodexThoughtSignature(value string) string {
	value = strings.TrimSpace(value)
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || !strings.HasPrefix(string(decoded), geminiCodexThoughtSignatureCarrierMagic) {
		return value
	}
	return strings.TrimSpace(strings.TrimPrefix(string(decoded), geminiCodexThoughtSignatureCarrierMagic))
}

// normalizedGeminiThoughtSignatureForCodexTarget unwraps and normalizes a
// client-echoed thought signature for the Codex-format upstream serving
// modelName, returning the exact value that may be written as reasoning
// encrypted_content. The carrier magic written by
// encodeGeminiCodexThoughtSignature is stripped first; the family match and
// normalization rules live in translatorcommon.CodexReplaySignatureForTarget.
func normalizedGeminiThoughtSignatureForCodexTarget(modelName, rawSignature string) (string, bool) {
	return translatorcommon.CodexReplaySignatureForTarget(modelName, decodeGeminiCodexThoughtSignature(rawSignature))
}

func setCodexToolChoiceFromGeminiToolConfigDoc(doc *ojson.Doc, hasTools bool, functionCallingConfig ojson.Result) {
	if !functionCallingConfig.Exists() {
		return
	}
	mode := functionCallingConfig.Get("mode").String()
	switch mode {
	case "NONE":
		_ = doc.Set("tool_choice", "none")
	case "AUTO":
		if !hasTools {
			_ = doc.Set("tool_choice", "auto")
		}
	case "ANY":
		allowedNames := functionCallingConfig.Get("allowedFunctionNames")
		allowedNameItems := allowedNames.Array()
		if allowedNames.IsArray() && len(allowedNameItems) == 1 {
			choice := []byte(`{"type":"function","name":""}`)
			choice, _ = ojson.SetBytes(choice, "name", shortenNameIfNeeded(allowedNameItems[0].String()))
			_ = doc.SetRawBytes("tool_choice", choice)
		} else {
			_ = doc.Set("tool_choice", "required")
		}
	}
}

func setCodexToolChoiceFromGeminiToolConfig(out []byte, functionCallingConfig ojson.Result) []byte {
	if !functionCallingConfig.Exists() {
		return out
	}
	mode := functionCallingConfig.Get("mode").String()
	switch mode {
	case "NONE":
		out, _ = ojson.SetBytes(out, "tool_choice", "none")
	case "AUTO":
		current := ojson.GetBytesNoCopy(out, "tool_choice")
		if current.Type != ojson.String || current.String() != "auto" {
			out, _ = ojson.SetBytes(out, "tool_choice", "auto")
		}
	case "ANY":
		allowedNames := functionCallingConfig.Get("allowedFunctionNames")
		allowedNameItems := allowedNames.Array()
		if allowedNames.IsArray() && len(allowedNameItems) == 1 {
			choice := []byte(`{"type":"function","name":""}`)
			choice, _ = ojson.SetBytes(choice, "name", shortenNameIfNeeded(allowedNameItems[0].String()))
			out, _ = ojson.SetRawBytes(out, "tool_choice", choice)
		} else {
			out, _ = ojson.SetBytes(out, "tool_choice", "required")
		}
	}
	return out
}

func cleanGeminiCodexToolParameters(parameters ojson.Result) []byte {
	cleaned := util.NormalizeJSONSchemaNumericConstraints(parameters.Raw)
	hasSchema := parameters.Get("$schema").Exists()
	needsAdditionalProps := parameters.Get("additionalProperties").Type != ojson.False
	if !hasSchema && !needsAdditionalProps {
		return []byte(cleaned)
	}
	doc := ojson.NewDocString(cleaned)
	if hasSchema {
		_ = doc.Delete("$schema")
	}
	if needsAdditionalProps {
		_ = doc.Set("additionalProperties", false)
	}
	return doc.Bytes()
}

func codexMessageWithPart(role string, part []byte) []byte {
	msgDoc := ojson.NewDocString(`{"type":"message","role":"","content":[]}`)
	_ = msgDoc.Set("role", role)
	_ = msgDoc.SetRawBytes("content", translatorcommon.JoinRawArray([][]byte{part}))
	return msgDoc.Bytes()
}

func normalizeGeminiCodexServiceTier(serviceTier ojson.Result) string {
	if !serviceTier.Exists() || serviceTier.Type != ojson.String {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(serviceTier.String())) {
	case "priority", "fast":
		return "priority"
	}
	return ""
}

func codexContentPartFromGeminiInlineData(part ojson.Result) ([]byte, bool) {
	inlineData := part.Get("inlineData")
	if !inlineData.Exists() {
		inlineData = part.Get("inline_data")
	}
	if !inlineData.Exists() {
		return nil, false
	}
	mimeType := inlineData.Get("mimeType").String()
	if mimeType == "" {
		mimeType = inlineData.Get("mime_type").String()
	}
	data := inlineData.Get("data").String()
	if mimeType == "" || data == "" {
		return nil, false
	}
	lowerMimeType := strings.ToLower(mimeType)
	switch {
	case strings.HasPrefix(lowerMimeType, "image/"):
		contentPart := []byte(`{"type":"input_image","image_url":""}`)
		contentPart, _ = ojson.SetBytes(contentPart, "image_url", fmt.Sprintf("data:%s;base64,%s", mimeType, data))
		return contentPart, true
	case strings.HasPrefix(lowerMimeType, "audio/"):
		contentPartDoc := ojson.NewDocString(`{"type":"input_audio","input_audio":{"data":"","format":""}}`)
		_ = contentPartDoc.Set("input_audio.data", data)
		_ = contentPartDoc.Set("input_audio.format", codexInputAudioFormatFromMIME(mimeType))
		return contentPartDoc.Bytes(), true
	default:
		contentPartDoc := ojson.NewDocString(`{"type":"input_file","file_data":"","filename":""}`)
		_ = contentPartDoc.Set("file_data", data)
		_ = contentPartDoc.Set("filename", codexFileNameFromMIME(mimeType))
		return contentPartDoc.Bytes(), true
	}
}

func codexContentPartFromGeminiFileData(part ojson.Result) ([]byte, bool) {
	fileData := part.Get("fileData")
	if !fileData.Exists() {
		fileData = part.Get("file_data")
	}
	if !fileData.Exists() {
		return nil, false
	}
	fileURI := fileData.Get("fileUri").String()
	if fileURI == "" {
		fileURI = fileData.Get("file_uri").String()
	}
	if fileURI == "" {
		return nil, false
	}
	mimeType := fileData.Get("mimeType").String()
	if mimeType == "" {
		mimeType = fileData.Get("mime_type").String()
	}
	lowerMimeType := strings.ToLower(mimeType)
	if strings.HasPrefix(lowerMimeType, "image/") {
		contentPart := []byte(`{"type":"input_image","image_url":""}`)
		contentPart, _ = ojson.SetBytes(contentPart, "image_url", fileURI)
		return contentPart, true
	}
	if strings.HasPrefix(lowerMimeType, "video/") || strings.HasPrefix(lowerMimeType, "application/") || strings.HasPrefix(lowerMimeType, "text/") {
		contentPartDoc := ojson.NewDocString(`{"type":"input_file","file_url":"","filename":""}`)
		_ = contentPartDoc.Set("file_url", fileURI)
		_ = contentPartDoc.Set("filename", codexFileNameFromMIME(mimeType))
		return contentPartDoc.Bytes(), true
	}
	fileInfo := "File: " + fileURI
	if mimeType != "" {
		fileInfo += " (Type: " + mimeType + ")"
	}
	contentPart := []byte(`{"type":"input_text","text":""}`)
	contentPart, _ = ojson.SetBytes(contentPart, "text", fileInfo)
	return contentPart, true
}

func codexInputAudioFormatFromMIME(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "audio/wav", "audio/wave", "audio/x-wav":
		return "wav"
	case "audio/flac":
		return "flac"
	case "audio/opus", "audio/ogg":
		return "opus"
	case "audio/pcm", "audio/l16":
		return "pcm16"
	default:
		return "mp3"
	}
}

func codexFileNameFromMIME(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "application/pdf":
		return "document.pdf"
	case "text/plain":
		return "document.txt"
	case "text/csv":
		return "document.csv"
	case "application/json":
		return "document.json"
	case "application/xml", "text/xml":
		return "document.xml"
	default:
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(mimeType)), "video/") {
			return "video"
		}
		return "document"
	}
}

// shortenNameIfNeeded applies the simple shortening rule for a single name.
func shortenNameIfNeeded(name string) string {
	const limit = 64
	if len(name) <= limit {
		return name
	}
	if strings.HasPrefix(name, "mcp__") {
		idx := strings.LastIndex(name, "__")
		if idx > 0 {
			cand := "mcp__" + name[idx+2:]
			if len(cand) > limit {
				return cand[:limit]
			}
			return cand
		}
	}
	return name[:limit]
}

// buildShortNameMap ensures uniqueness of shortened names within a request.
func buildShortNameMap(names []string) map[string]string {
	const limit = 64
	used := map[string]struct{}{}
	m := map[string]string{}

	baseCandidate := func(n string) string {
		if len(n) <= limit {
			return n
		}
		if strings.HasPrefix(n, "mcp__") {
			idx := strings.LastIndex(n, "__")
			if idx > 0 {
				cand := "mcp__" + n[idx+2:]
				if len(cand) > limit {
					cand = cand[:limit]
				}
				return cand
			}
		}
		return n[:limit]
	}

	makeUnique := func(cand string) string {
		if _, ok := used[cand]; !ok {
			return cand
		}
		base := cand
		for i := 1; ; i++ {
			suffix := "_" + strconv.Itoa(i)
			allowed := limit - len(suffix)
			if allowed < 0 {
				allowed = 0
			}
			tmp := base
			if len(tmp) > allowed {
				tmp = tmp[:allowed]
			}
			tmp = tmp + suffix
			if _, ok := used[tmp]; !ok {
				return tmp
			}
		}
	}

	for _, n := range names {
		cand := baseCandidate(n)
		uniq := makeUnique(cand)
		used[uniq] = struct{}{}
		m[n] = uniq
	}
	return m
}
