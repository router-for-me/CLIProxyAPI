// Package openai provides request translation functionality for OpenAI to Gemini CLI API compatibility.
// It converts OpenAI Chat Completions requests into Gemini CLI compatible JSON using gjson/sjson only.
package chat_completions

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const geminiCLIFunctionThoughtSignature = "skip_thought_signature_validator"

// ConvertOpenAIRequestToGeminiCLI converts an OpenAI Chat Completions request (raw JSON)
// into a complete Gemini CLI request JSON. All JSON construction uses sjson and lookups use gjson.
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the OpenAI API
//   - stream: A boolean indicating if the request is for a streaming response (unused in current implementation)
//
// Returns:
//   - []byte: The transformed request data in Gemini CLI API format
func ConvertOpenAIRequestToGeminiCLI(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON
	// Base envelope (no default thinkingConfig)
	out := []byte(`{"project":"","request":{"contents":[]},"model":"gemini-2.5-pro"}`)

	// Model
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Let user-provided generationConfig pass through
	if genConfig := gjson.GetBytes(rawJSON, "generationConfig"); genConfig.Exists() {
		out, _ = sjson.SetRawBytes(out, "request.generationConfig", []byte(genConfig.Raw))
	}

	// Apply thinking configuration: convert OpenAI reasoning_effort to Gemini CLI thinkingConfig.
	// Inline translation-only mapping; capability checks happen later in ApplyThinking.
	re := gjson.GetBytes(rawJSON, "reasoning_effort")
	if re.Exists() {
		effort := strings.ToLower(strings.TrimSpace(re.String()))
		if effort != "" {
			thinkingPath := "request.generationConfig.thinkingConfig"
			if effort == "auto" {
				out, _ = sjson.SetBytes(out, thinkingPath+".thinkingBudget", -1)
				out, _ = sjson.SetBytes(out, thinkingPath+".includeThoughts", true)
			} else {
				out, _ = sjson.SetBytes(out, thinkingPath+".thinkingLevel", effort)
				out, _ = sjson.SetBytes(out, thinkingPath+".includeThoughts", effort != "none")
			}
		}
	}

	// Temperature/top_p/top_k
	if tr := gjson.GetBytes(rawJSON, "temperature"); tr.Exists() && tr.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "request.generationConfig.temperature", tr.Num)
	}
	if tpr := gjson.GetBytes(rawJSON, "top_p"); tpr.Exists() && tpr.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "request.generationConfig.topP", tpr.Num)
	}
	if tkr := gjson.GetBytes(rawJSON, "top_k"); tkr.Exists() && tkr.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "request.generationConfig.topK", tkr.Num)
	}

	// Candidate count (OpenAI 'n' parameter)
	if n := gjson.GetBytes(rawJSON, "n"); n.Exists() && n.Type == gjson.Number {
		if val := n.Int(); val > 1 {
			out, _ = sjson.SetBytes(out, "request.generationConfig.candidateCount", val)
		}
	}

	// Max tokens (prefer max_completion_tokens, fallback to max_tokens)
	if mct := gjson.GetBytes(rawJSON, "max_completion_tokens"); mct.Exists() && mct.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "request.generationConfig.maxOutputTokens", mct.Int())
	} else if mt := gjson.GetBytes(rawJSON, "max_tokens"); mt.Exists() && mt.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "request.generationConfig.maxOutputTokens", mt.Int())
	}

	// Stop sequences: support both stop_sequences and stop
	stopSequences := make([]string, 0)
	appendStopSequences(&stopSequences, gjson.GetBytes(rawJSON, "stop_sequences"))
	if len(stopSequences) == 0 {
		appendStopSequences(&stopSequences, gjson.GetBytes(rawJSON, "stop"))
	}
	if len(stopSequences) > 0 {
		out, _ = sjson.SetBytes(out, "request.generationConfig.stopSequences", stopSequences)
	}

	// Map OpenAI modalities -> Gemini CLI request.generationConfig.responseModalities
	// e.g. "modalities": ["image", "text"] -> ["IMAGE", "TEXT"]
	if mods := gjson.GetBytes(rawJSON, "modalities"); mods.Exists() && mods.IsArray() {
		var responseMods []string
		for _, m := range mods.Array() {
			switch strings.ToLower(m.String()) {
			case "text":
				responseMods = append(responseMods, "TEXT")
			case "image":
				responseMods = append(responseMods, "IMAGE")
			}
		}
		if len(responseMods) > 0 {
			out, _ = sjson.SetBytes(out, "request.generationConfig.responseModalities", responseMods)
		}
	}

	// OpenRouter-style image_config support
	// If the input uses top-level image_config.aspect_ratio, map it into request.generationConfig.imageConfig.aspectRatio.
	if imgCfg := gjson.GetBytes(rawJSON, "image_config"); imgCfg.Exists() && imgCfg.IsObject() {
		if ar := imgCfg.Get("aspect_ratio"); ar.Exists() && ar.Type == gjson.String {
			out, _ = sjson.SetBytes(out, "request.generationConfig.imageConfig.aspectRatio", ar.Str)
		}
		if size := imgCfg.Get("image_size"); size.Exists() && size.Type == gjson.String {
			out, _ = sjson.SetBytes(out, "request.generationConfig.imageConfig.imageSize", size.Str)
		}
	}

	// messages -> systemInstruction + contents
	messages := gjson.GetBytes(rawJSON, "messages")
	if messages.IsArray() {
		arr := messages.Array()
		// First pass: assistant tool_calls id->name map
		tcID2Name := map[string]string{}
		for i := 0; i < len(arr); i++ {
			m := arr[i]
			if m.Get("role").String() == "assistant" {
				tcs := m.Get("tool_calls")
				if tcs.IsArray() {
					for _, tc := range tcs.Array() {
						if tc.Get("type").String() == "function" {
							id := tc.Get("id").String()
							name := tc.Get("function.name").String()
							if id != "" && name != "" {
								tcID2Name[id] = name
							}
						}
					}
				}
			}
		}

		// Second pass build systemInstruction/tool responses cache
		toolResponses := map[string]string{} // tool_call_id -> response text
		for i := 0; i < len(arr); i++ {
			m := arr[i]
			role := m.Get("role").String()
			if role == "tool" {
				toolCallID := m.Get("tool_call_id").String()
				if toolCallID != "" {
					c := m.Get("content")
					toolResponses[toolCallID] = c.Raw
				}
			}
		}

		systemPartIndex := 0
		for i := 0; i < len(arr); i++ {
			m := arr[i]
			role := m.Get("role").String()
			content := m.Get("content")

			if (role == "system" || role == "developer") && len(arr) > 1 {
				// system -> request.systemInstruction as a user message style
				if content.Type == gjson.String {
					out, _ = sjson.SetBytes(out, "request.systemInstruction.role", "user")
					out, _ = sjson.SetBytes(out, fmt.Sprintf("request.systemInstruction.parts.%d.text", systemPartIndex), content.String())
					systemPartIndex++
				} else if content.IsObject() && content.Get("type").String() == "text" {
					out, _ = sjson.SetBytes(out, "request.systemInstruction.role", "user")
					out, _ = sjson.SetBytes(out, fmt.Sprintf("request.systemInstruction.parts.%d.text", systemPartIndex), content.Get("text").String())
					systemPartIndex++
				} else if content.IsArray() {
					contents := content.Array()
					if len(contents) > 0 {
						out, _ = sjson.SetBytes(out, "request.systemInstruction.role", "user")
						for j := 0; j < len(contents); j++ {
							out, _ = sjson.SetBytes(out, fmt.Sprintf("request.systemInstruction.parts.%d.text", systemPartIndex), contents[j].Get("text").String())
							systemPartIndex++
						}
					}
				}
			} else if role == "user" || ((role == "system" || role == "developer") && len(arr) == 1) {
				// Build single user content node to avoid splitting into multiple contents
				node := []byte(`{"role":"user","parts":[]}`)
				if content.Type == gjson.String {
					node, _ = sjson.SetBytes(node, "parts.0.text", content.String())
				} else if content.IsArray() {
					items := content.Array()
					p := 0
					for _, item := range items {
						switch item.Get("type").String() {
						case "text":
							node, _ = sjson.SetBytes(node, "parts."+itoa(p)+".text", item.Get("text").String())
							p++
						case "image_url":
							imageURL := item.Get("image_url.url").String()
							nextNode, ok := setImageURLPart(node, "parts."+itoa(p), imageURL)
							if ok {
								node = nextNode
								if strings.HasPrefix(imageURL, "data:") {
									node, _ = sjson.SetBytes(node, "parts."+itoa(p)+".thoughtSignature", geminiCLIFunctionThoughtSignature)
								}
								p++
							}
						case "file":
							filename := item.Get("file.filename").String()
							fileData := item.Get("file.file_data").String()
							ext := ""
							if sp := strings.Split(filename, "."); len(sp) > 1 {
								ext = sp[len(sp)-1]
							}
							if mimeType, ok := misc.MimeTypes[ext]; ok {
								node, _ = sjson.SetBytes(node, "parts."+itoa(p)+".inlineData.mime_type", mimeType)
								node, _ = sjson.SetBytes(node, "parts."+itoa(p)+".inlineData.data", fileData)
								p++
							} else {
								log.Warnf("Unknown file name extension '%s' in user message, skip", ext)
							}
						}
					}
				}
				out, _ = sjson.SetRawBytes(out, "request.contents.-1", node)
			} else if role == "assistant" {
				p := 0
				node := []byte(`{"role":"model","parts":[]}`)
				if content.Type == gjson.String {
					// Assistant text -> single model content
					node, _ = sjson.SetBytes(node, "parts.-1.text", content.String())
					p++
				} else if content.IsArray() {
					// Assistant multimodal content (e.g. text + image) -> single model content with parts
					for _, item := range content.Array() {
						switch item.Get("type").String() {
						case "text":
							node, _ = sjson.SetBytes(node, "parts."+itoa(p)+".text", item.Get("text").String())
							p++
						case "image_url":
							// If the assistant returned an inline data URL, preserve it for history fidelity.
							imageURL := item.Get("image_url.url").String()
							nextNode, ok := setImageURLPart(node, "parts."+itoa(p), imageURL)
							if ok {
								node = nextNode
								if strings.HasPrefix(imageURL, "data:") {
									node, _ = sjson.SetBytes(node, "parts."+itoa(p)+".thoughtSignature", geminiCLIFunctionThoughtSignature)
								}
								p++
							}
						}
					}
				}

				// Tool calls -> single model content with functionCall parts
				tcs := m.Get("tool_calls")
				if tcs.IsArray() {
					fIDs := make([]string, 0)
					for _, tc := range tcs.Array() {
						if tc.Get("type").String() != "function" {
							continue
						}
						fid := tc.Get("id").String()
						fname := tc.Get("function.name").String()
						fargs := tc.Get("function.arguments").String()
						node, _ = sjson.SetBytes(node, "parts."+itoa(p)+".functionCall.name", fname)
						node, _ = sjson.SetRawBytes(node, "parts."+itoa(p)+".functionCall.args", []byte(fargs))
						node, _ = sjson.SetBytes(node, "parts."+itoa(p)+".thoughtSignature", geminiCLIFunctionThoughtSignature)
						p++
						if fid != "" {
							fIDs = append(fIDs, fid)
						}
					}
					out, _ = sjson.SetRawBytes(out, "request.contents.-1", node)

					// Append a single tool content combining name + response per function
					toolNode := []byte(`{"role":"user","parts":[]}`)
					pp := 0
					for _, fid := range fIDs {
						if name, ok := tcID2Name[fid]; ok {
							toolNode, _ = sjson.SetBytes(toolNode, "parts."+itoa(pp)+".functionResponse.name", name)
							resp := toolResponses[fid]
							if resp == "" {
								resp = "{}"
							}
							respResult := gjson.Parse(resp)
							if respResult.Type == gjson.JSON {
								toolNode, _ = sjson.SetRawBytes(toolNode, "parts."+itoa(pp)+".functionResponse.response.result", []byte(respResult.Raw))
							} else if parsedRaw, ok := parseRawJSONObjectString(respResult); ok {
								toolNode, _ = sjson.SetRawBytes(toolNode, "parts."+itoa(pp)+".functionResponse.response.result", []byte(parsedRaw))
							} else {
								toolNode, _ = sjson.SetBytes(toolNode, "parts."+itoa(pp)+".functionResponse.response.result", respResult.Value())
							}
							pp++
						}
					}
					if pp > 0 {
						out, _ = sjson.SetRawBytes(out, "request.contents.-1", toolNode)
					}
				} else {
					out, _ = sjson.SetRawBytes(out, "request.contents.-1", node)
				}
			}
		}
	}

	// tools -> request.tools[].functionDeclarations + request.tools[].googleSearch/codeExecution/urlContext passthrough
	tools := gjson.GetBytes(rawJSON, "tools")
	if tools.IsArray() && len(tools.Array()) > 0 {
		functionToolNode := []byte(`{}`)
		hasFunction := false
		googleSearchNodes := make([][]byte, 0)
		codeExecutionNodes := make([][]byte, 0)
		urlContextNodes := make([][]byte, 0)
		for _, t := range tools.Array() {
			if t.Get("type").String() == "function" {
				fn := t.Get("function")
				if fn.Exists() && fn.IsObject() {
					fnRaw := fn.Raw
					if fn.Get("parameters").Exists() {
						renamed, errRename := util.RenameKey(fnRaw, "parameters", "parametersJsonSchema")
						if errRename != nil {
							log.Warnf("Failed to rename parameters for tool '%s': %v", fn.Get("name").String(), errRename)
							var errSet error
							fnRaw, errSet = sjson.Set(fnRaw, "parametersJsonSchema.type", "object")
							if errSet != nil {
								log.Warnf("Failed to set default schema type for tool '%s': %v", fn.Get("name").String(), errSet)
								continue
							}
							fnRaw, errSet = sjson.SetRaw(fnRaw, "parametersJsonSchema.properties", `{}`)
							if errSet != nil {
								log.Warnf("Failed to set default schema properties for tool '%s': %v", fn.Get("name").String(), errSet)
								continue
							}
						} else {
							fnRaw = renamed
						}
					} else {
						var errSet error
						fnRaw, errSet = sjson.Set(fnRaw, "parametersJsonSchema.type", "object")
						if errSet != nil {
							log.Warnf("Failed to set default schema type for tool '%s': %v", fn.Get("name").String(), errSet)
							continue
						}
						fnRaw, errSet = sjson.SetRaw(fnRaw, "parametersJsonSchema.properties", `{}`)
						if errSet != nil {
							log.Warnf("Failed to set default schema properties for tool '%s': %v", fn.Get("name").String(), errSet)
							continue
						}
					}
					fnRaw, _ = sjson.Delete(fnRaw, "strict")
					if !hasFunction {
						functionToolNode, _ = sjson.SetRawBytes(functionToolNode, "functionDeclarations", []byte("[]"))
					}
					tmp, errSet := sjson.SetRawBytes(functionToolNode, "functionDeclarations.-1", []byte(fnRaw))
					if errSet != nil {
						log.Warnf("Failed to append tool declaration for '%s': %v", fn.Get("name").String(), errSet)
						continue
					}
					functionToolNode = tmp
					hasFunction = true
				}
			}
			if gs := t.Get("google_search"); gs.Exists() {
				googleToolNode := []byte(`{}`)
				var errSet error
				googleToolNode, errSet = sjson.SetRawBytes(googleToolNode, "googleSearch", []byte(gs.Raw))
				if errSet != nil {
					log.Warnf("Failed to set googleSearch tool: %v", errSet)
					continue
				}
				googleSearchNodes = append(googleSearchNodes, googleToolNode)
			}
			if ce := t.Get("code_execution"); ce.Exists() {
				codeToolNode := []byte(`{}`)
				var errSet error
				codeToolNode, errSet = sjson.SetRawBytes(codeToolNode, "codeExecution", []byte(ce.Raw))
				if errSet != nil {
					log.Warnf("Failed to set codeExecution tool: %v", errSet)
					continue
				}
				codeExecutionNodes = append(codeExecutionNodes, codeToolNode)
			}
			if uc := t.Get("url_context"); uc.Exists() {
				urlToolNode := []byte(`{}`)
				var errSet error
				urlToolNode, errSet = sjson.SetRawBytes(urlToolNode, "urlContext", []byte(uc.Raw))
				if errSet != nil {
					log.Warnf("Failed to set urlContext tool: %v", errSet)
					continue
				}
				urlContextNodes = append(urlContextNodes, urlToolNode)
			}
		}
		if hasFunction || len(googleSearchNodes) > 0 || len(codeExecutionNodes) > 0 || len(urlContextNodes) > 0 {
			toolsNode := []byte("[]")
			if hasFunction {
				toolsNode, _ = sjson.SetRawBytes(toolsNode, "-1", functionToolNode)
			}
			for _, googleNode := range googleSearchNodes {
				toolsNode, _ = sjson.SetRawBytes(toolsNode, "-1", googleNode)
			}
			for _, codeNode := range codeExecutionNodes {
				toolsNode, _ = sjson.SetRawBytes(toolsNode, "-1", codeNode)
			}
			for _, urlNode := range urlContextNodes {
				toolsNode, _ = sjson.SetRawBytes(toolsNode, "-1", urlNode)
			}
			out, _ = sjson.SetRawBytes(out, "request.tools", toolsNode)
		}
	}

	// tool_choice -> Gemini functionCallingConfig
	if toolChoice := gjson.GetBytes(rawJSON, "tool_choice"); toolChoice.Exists() {
		out = applyToolChoiceToGeminiCLI(out, toolChoice)
	}

	return common.AttachDefaultSafetySettings(out, "request.safetySettings")
}

// itoa converts int to string without strconv import for few usages.
func itoa(i int) string { return fmt.Sprintf("%d", i) }

func appendStopSequences(dst *[]string, value gjson.Result) {
	if !value.Exists() {
		return
	}
	if value.Type == gjson.String {
		trimmed := strings.TrimSpace(value.String())
		if trimmed != "" {
			*dst = append(*dst, trimmed)
		}
		return
	}
	if value.IsArray() {
		value.ForEach(func(_, item gjson.Result) bool {
			trimmed := strings.TrimSpace(item.String())
			if trimmed != "" {
				*dst = append(*dst, trimmed)
			}
			return true
		})
	}
}

func setImageURLPart(node []byte, basePath string, imageURL string) ([]byte, bool) {
	if imageURL == "" {
		return node, false
	}
	if strings.HasPrefix(imageURL, "data:") {
		payload := strings.TrimPrefix(imageURL, "data:")
		pieces := strings.SplitN(payload, ";base64,", 2)
		if len(pieces) != 2 || pieces[0] == "" || pieces[1] == "" {
			return node, false
		}
		node, _ = sjson.SetBytes(node, basePath+".inlineData.mime_type", pieces[0])
		node, _ = sjson.SetBytes(node, basePath+".inlineData.data", pieces[1])
		return node, true
	}
	if strings.HasPrefix(imageURL, "http://") || strings.HasPrefix(imageURL, "https://") {
		node, _ = sjson.SetBytes(node, basePath+".fileData.fileUri", imageURL)
		return node, true
	}
	return node, false
}

func parseRawJSONObjectString(respResult gjson.Result) (string, bool) {
	if respResult.Type != gjson.String {
		return "", false
	}
	trimmed := strings.TrimSpace(respResult.String())
	if len(trimmed) < 2 {
		return "", false
	}
	if !((strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
		(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"))) {
		return "", false
	}
	if !gjson.Valid(trimmed) {
		return "", false
	}
	return trimmed, true
}

func applyToolChoiceToGeminiCLI(out []byte, toolChoice gjson.Result) []byte {
	mode := ""
	allowedFunctionNames := make([]string, 0)

	if toolChoice.Type == gjson.String {
		switch strings.ToLower(strings.TrimSpace(toolChoice.String())) {
		case "none":
			mode = "NONE"
		case "required":
			mode = "ANY"
		case "auto":
			mode = "AUTO"
		}
	} else if toolChoice.IsObject() {
		choiceType := strings.ToLower(strings.TrimSpace(toolChoice.Get("type").String()))
		switch choiceType {
		case "none":
			mode = "NONE"
		case "required", "any":
			mode = "ANY"
		case "auto":
			mode = "AUTO"
		case "function":
			mode = "ANY"
		}
		if name := strings.TrimSpace(toolChoice.Get("function.name").String()); name != "" {
			allowedFunctionNames = append(allowedFunctionNames, name)
			if mode == "" {
				mode = "ANY"
			}
		}
	}

	if mode == "" {
		return out
	}
	out, _ = sjson.SetBytes(out, "request.toolConfig.functionCallingConfig.mode", mode)
	if len(allowedFunctionNames) > 0 {
		out, _ = sjson.SetBytes(out, "request.toolConfig.functionCallingConfig.allowedFunctionNames", allowedFunctionNames)
	}
	return out
}
