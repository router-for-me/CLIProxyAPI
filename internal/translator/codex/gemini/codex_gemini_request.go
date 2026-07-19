// Package gemini provides request translation functionality for Codex to Gemini API compatibility.
// It handles parsing and transforming Codex API requests into Gemini API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Codex API format and Gemini API's expected format.
package gemini

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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
func ConvertGeminiRequestToCodex(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON
	// Base template
	out := []byte(`{"model":"","instructions":"","input":[]}`)

	root := gjson.ParseBytes(rawJSON)
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

	// helper for generating paired call IDs in the form: call_<alphanum>
	// Gemini uses sequential pairing across possibly multiple in-flight
	// functionCalls, so we keep a FIFO queue of generated call IDs and
	// consume them in order when functionResponses arrive.
	var pendingCallIDs []string

	// genCallID creates a random call id like: call_<8chars>
	genCallID := func() string {
		const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		var b strings.Builder
		// 8 chars random suffix
		for i := 0; i < 24; i++ {
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
			b.WriteByte(letters[n.Int64()])
		}
		return "call_" + b.String()
	}

	getGeminiCallID := func(value gjson.Result) string {
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
	out, _ = sjson.SetBytes(out, "model", modelName)
	if serviceTier := normalizeGeminiCodexServiceTier(root.Get("service_tier")); serviceTier != "" {
		out, _ = sjson.SetBytes(out, "service_tier", serviceTier)
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
			if t := p.Get("text"); t.Exists() {
				part := []byte(`{}`)
				part, _ = sjson.SetBytes(part, "type", "input_text")
				part, _ = sjson.SetBytes(part, "text", t.String())
				contentItems = append(contentItems, part)
			}
		}
		if len(contentItems) > 0 {
			msg := []byte(`{"type":"message","role":"developer","content":[]}`)
			msg, _ = sjson.SetRawBytes(msg, "content", translatorcommon.JoinRawArray(contentItems))
			inputItems = append(inputItems, msg)
		}
	}

	// Contents -> messages and function calls/results
	contents := root.Get("contents")
	if contents.IsArray() {
		items := contents.Array()
		for i := 0; i < len(items); i++ {
			item := items[i]
			role := item.Get("role").String()
			if role == "model" {
				role = "assistant"
			}

			parts := item.Get("parts")
			if !parts.IsArray() {
				continue
			}
			parr := parts.Array()
			for j := 0; j < len(parr); j++ {
				p := parr[j]
				// text part
				if t := p.Get("text"); t.Exists() {
					partType := "input_text"
					if role == "assistant" {
						partType = "output_text"
					}
					part := []byte(`{}`)
					part, _ = sjson.SetBytes(part, "type", partType)
					part, _ = sjson.SetBytes(part, "text", t.String())
					inputItems = append(inputItems, codexMessageWithPart(role, part))
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
				if fc := p.Get("functionCall"); fc.Exists() {
					fn := []byte(`{"type":"function_call"}`)
					if name := fc.Get("name"); name.Exists() {
						n := name.String()
						if short, ok := shortMap[n]; ok {
							n = short
						} else {
							n = shortenNameIfNeeded(n)
						}
						fn, _ = sjson.SetBytes(fn, "name", n)
					}
					if args := fc.Get("args"); args.Exists() {
						fn, _ = sjson.SetBytes(fn, "arguments", args.Raw)
					}
					// Reuse gateway-provided IDs when present, otherwise generate one for pairing.
					id := getGeminiCallID(fc)
					if id == "" {
						id = genCallID()
					}
					fn, _ = sjson.SetBytes(fn, "call_id", id)
					pendingCallIDs = append(pendingCallIDs, id)
					inputItems = append(inputItems, fn)
					continue
				}

				// function response from user
				if fr := p.Get("functionResponse"); fr.Exists() {
					fno := []byte(`{"type":"function_call_output"}`)
					// Prefer a string result if present; otherwise embed the raw response as a string
					if res := fr.Get("response.result"); res.Exists() {
						fno, _ = sjson.SetBytes(fno, "output", res.String())
					} else if resp := fr.Get("response"); resp.Exists() {
						fno, _ = sjson.SetBytes(fno, "output", resp.Raw)
					}
					// fno, _ = sjson.SetBytes(fno, "call_id", "call_W6nRJzFXyPM2LFBbfo98qAbq")
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
						id = genCallID()
					}
					fno, _ = sjson.SetBytes(fno, "call_id", id)
					inputItems = append(inputItems, fno)
					continue
				}
			}
		}
	}

	out = translatorcommon.SetRawArrayItems(out, "input", inputItems)

	// Tools mapping: Gemini functionDeclarations -> Codex tools
	tools := root.Get("tools")
	if tools.IsArray() {
		var toolItems [][]byte
		out, _ = sjson.SetBytes(out, "tool_choice", "auto")
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
				tool := []byte(`{}`)
				tool, _ = sjson.SetBytes(tool, "type", "function")
				if v := fn.Get("name"); v.Exists() {
					name := v.String()
					if short, ok := shortMap[name]; ok {
						name = short
					} else {
						name = shortenNameIfNeeded(name)
					}
					tool, _ = sjson.SetBytes(tool, "name", name)
				}
				if v := fn.Get("description"); v.Exists() {
					tool, _ = sjson.SetBytes(tool, "description", v.String())
				}
				if prm := fn.Get("parameters"); prm.Exists() {
					// Remove optional $schema field if present
					cleaned := []byte(prm.Raw)
					cleaned, _ = sjson.DeleteBytes(cleaned, "$schema")
					cleaned, _ = sjson.SetBytes(cleaned, "additionalProperties", false)
					tool, _ = sjson.SetRawBytes(tool, "parameters", cleaned)
				} else if prm = fn.Get("parametersJsonSchema"); prm.Exists() {
					// Remove optional $schema field if present
					cleaned := []byte(prm.Raw)
					cleaned, _ = sjson.DeleteBytes(cleaned, "$schema")
					cleaned, _ = sjson.SetBytes(cleaned, "additionalProperties", false)
					tool, _ = sjson.SetRawBytes(tool, "parameters", cleaned)
				}
				tool, _ = sjson.SetBytes(tool, "strict", false)
				toolItems = append(toolItems, tool)
			}
		}
		out, _ = sjson.SetRawBytes(out, "tools", translatorcommon.JoinRawArray(toolItems))
	}

	// Fixed flags aligning with Codex expectations
	out, _ = sjson.SetBytes(out, "parallel_tool_calls", true)
	out = setCodexToolChoiceFromGeminiToolConfig(out, root.Get("toolConfig.functionCallingConfig"))

	// Convert Gemini thinkingConfig to Codex reasoning.effort.
	// Note: Google official Python SDK sends snake_case fields (thinking_level/thinking_budget).
	effortSet := false
	if genConfig := root.Get("generationConfig"); genConfig.Exists() {
		thinkingLevel := genConfig.Get("thinkingLevel")
		if !thinkingLevel.Exists() {
			thinkingLevel = genConfig.Get("thinking_level")
		}
		if thinkingLevel.Exists() {
			effort := strings.ToLower(strings.TrimSpace(thinkingLevel.String()))
			if effort != "" {
				out, _ = sjson.SetBytes(out, "reasoning.effort", effort)
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
					out, _ = sjson.SetBytes(out, "reasoning.effort", effort)
					effortSet = true
				}
			} else {
				thinkingBudget := thinkingConfig.Get("thinkingBudget")
				if !thinkingBudget.Exists() {
					thinkingBudget = thinkingConfig.Get("thinking_budget")
				}
				if thinkingBudget.Exists() {
					if effort, ok := thinking.ConvertBudgetToLevel(int(thinkingBudget.Int())); ok {
						out, _ = sjson.SetBytes(out, "reasoning.effort", effort)
						effortSet = true
					}
				}
			}
		}
	}
	if !effortSet {
		// No thinking config, set default effort
		out, _ = sjson.SetBytes(out, "reasoning.effort", "medium")
	}
	out, _ = sjson.SetBytes(out, "reasoning.summary", "auto")
	out, _ = sjson.SetBytes(out, "stream", true)
	out, _ = sjson.SetBytes(out, "store", false)
	out, _ = sjson.SetBytes(out, "include", []string{"reasoning.encrypted_content"})

	var pathsToLower []string
	toolsResult := gjson.GetBytes(out, "tools")
	util.Walk(toolsResult, "", "type", &pathsToLower)
	for _, p := range pathsToLower {
		fullPath := fmt.Sprintf("tools.%s", p)
		typeValue := gjson.GetBytes(out, fullPath)
		if typeValue.Type != gjson.String {
			continue
		}
		out, _ = sjson.SetBytes(out, fullPath, strings.ToLower(typeValue.String()))
	}

	return out
}

func setCodexToolChoiceFromGeminiToolConfig(out []byte, functionCallingConfig gjson.Result) []byte {
	if !functionCallingConfig.Exists() {
		return out
	}
	mode := functionCallingConfig.Get("mode").String()
	switch mode {
	case "NONE":
		out, _ = sjson.SetBytes(out, "tool_choice", "none")
	case "AUTO":
		out, _ = sjson.SetBytes(out, "tool_choice", "auto")
	case "ANY":
		allowedNames := functionCallingConfig.Get("allowedFunctionNames")
		allowedNameItems := allowedNames.Array()
		if allowedNames.IsArray() && len(allowedNameItems) == 1 {
			choice := []byte(`{"type":"function","name":""}`)
			choice, _ = sjson.SetBytes(choice, "name", shortenNameIfNeeded(allowedNameItems[0].String()))
			out, _ = sjson.SetRawBytes(out, "tool_choice", choice)
		} else {
			out, _ = sjson.SetBytes(out, "tool_choice", "required")
		}
	}
	return out
}

func codexMessageWithPart(role string, part []byte) []byte {
	msg := []byte(`{"type":"message","role":"","content":[]}`)
	msg, _ = sjson.SetBytes(msg, "role", role)
	msg, _ = sjson.SetRawBytes(msg, "content", translatorcommon.JoinRawArray([][]byte{part}))
	return msg
}

func normalizeGeminiCodexServiceTier(serviceTier gjson.Result) string {
	if !serviceTier.Exists() || serviceTier.Type != gjson.String {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(serviceTier.String())) {
	case "priority", "fast":
		return "priority"
	}
	return ""
}

func codexContentPartFromGeminiInlineData(part gjson.Result) ([]byte, bool) {
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
		contentPart, _ = sjson.SetBytes(contentPart, "image_url", fmt.Sprintf("data:%s;base64,%s", mimeType, data))
		return contentPart, true
	case strings.HasPrefix(lowerMimeType, "audio/"):
		contentPart := []byte(`{"type":"input_audio","input_audio":{"data":"","format":""}}`)
		contentPart, _ = sjson.SetBytes(contentPart, "input_audio.data", data)
		contentPart, _ = sjson.SetBytes(contentPart, "input_audio.format", codexInputAudioFormatFromMIME(mimeType))
		return contentPart, true
	default:
		contentPart := []byte(`{"type":"input_file","file_data":"","filename":""}`)
		contentPart, _ = sjson.SetBytes(contentPart, "file_data", data)
		contentPart, _ = sjson.SetBytes(contentPart, "filename", codexFileNameFromMIME(mimeType))
		return contentPart, true
	}
}

func codexContentPartFromGeminiFileData(part gjson.Result) ([]byte, bool) {
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
		contentPart, _ = sjson.SetBytes(contentPart, "image_url", fileURI)
		return contentPart, true
	}
	if strings.HasPrefix(lowerMimeType, "video/") || strings.HasPrefix(lowerMimeType, "application/") || strings.HasPrefix(lowerMimeType, "text/") {
		contentPart := []byte(`{"type":"input_file","file_url":"","filename":""}`)
		contentPart, _ = sjson.SetBytes(contentPart, "file_url", fileURI)
		contentPart, _ = sjson.SetBytes(contentPart, "filename", codexFileNameFromMIME(mimeType))
		return contentPart, true
	}
	fileInfo := "File: " + fileURI
	if mimeType != "" {
		fileInfo += " (Type: " + mimeType + ")"
	}
	contentPart := []byte(`{"type":"input_text","text":""}`)
	contentPart, _ = sjson.SetBytes(contentPart, "text", fileInfo)
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
