// Package openai provides request translation functionality for OpenAI to Antigravity API compatibility.
// It converts OpenAI Chat Completions requests into Antigravity compatible JSON using gjson/sjson only.
package chat_completions

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/antigravity/gemini"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const antigravityFunctionThoughtSignature = "skip_thought_signature_validator"

// ConvertOpenAIRequestToAntigravity converts an OpenAI Chat Completions request (raw JSON)
// into a complete Antigravity request JSON. All JSON construction uses sjson and lookups use gjson.
//
// Parameters:
//   - modelName: The name of the model to use for the request
//   - rawJSON: The raw JSON request data from the OpenAI API
//   - stream: A boolean indicating if the request is for a streaming response (unused in current implementation)
//
// Returns:
//   - []byte: The transformed request data in Antigravity API format
func ConvertOpenAIRequestToAntigravity(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON
	functionNameMap := util.SanitizedFunctionNameMap(rawJSON)
	// Base envelope (no default thinkingConfig)
	out := []byte(`{"project":"","request":{"contents":[]},"model":"gemini-2.5-pro"}`)

	// Model
	out, _ = sjson.SetBytes(out, "model", modelName)

	// Let user-provided generationConfig pass through
	if genConfig := gjson.GetBytes(rawJSON, "generationConfig"); genConfig.Exists() {
		out, _ = sjson.SetRawBytes(out, "request.generationConfig", []byte(genConfig.Raw))
	} else if genConfig := gjson.GetBytes(rawJSON, "generation_config"); genConfig.Exists() {
		out, _ = sjson.SetRawBytes(out, "request.generationConfig", []byte(genConfig.Raw))
	}

	// Apply thinking configuration: convert OpenAI reasoning_effort to Antigravity thinkingConfig.
	// Inline translation-only mapping; capability checks happen later in ApplyThinking.
	re := gjson.GetBytes(rawJSON, "reasoning_effort")
	if re.Exists() {
		effort := strings.ToLower(strings.TrimSpace(re.String()))
		if effort != "" {
			thinkingPath := "request.generationConfig.thinkingConfig"
			if effort == "auto" {
				out, _ = sjson.SetBytes(out, thinkingPath+".thinkingBudget", -1)
			} else {
				out, _ = sjson.SetBytes(out, thinkingPath+".thinkingLevel", effort)
			}
		}
	}
	out = applyOpenAIThinkingCompatibilityToAntigravity(out, rawJSON)

	// Temperature/top_p/top_k/max_tokens
	if tr := gjson.GetBytes(rawJSON, "temperature"); tr.Exists() && tr.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "request.generationConfig.temperature", tr.Num)
	}
	if tpr := gjson.GetBytes(rawJSON, "top_p"); tpr.Exists() && tpr.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "request.generationConfig.topP", tpr.Num)
	}
	if tkr := gjson.GetBytes(rawJSON, "top_k"); tkr.Exists() && tkr.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "request.generationConfig.topK", tkr.Num)
	}
	if maxTok := gjson.GetBytes(rawJSON, "max_tokens"); maxTok.Exists() && maxTok.Type == gjson.Number {
		out, _ = sjson.SetBytes(out, "request.generationConfig.maxOutputTokens", maxTok.Num)
	}

	// Map OpenAI response_format to Antigravity structured output settings.
	if responseFormat := gjson.GetBytes(rawJSON, "response_format"); responseFormat.Exists() {
		switch responseFormatType := strings.ToLower(strings.TrimSpace(responseFormat.Get("type").String())); responseFormatType {
		case "json_object", "json_schema":
			for _, schemaKey := range []string{"responseSchema", "responseJsonSchema", "response_schema", "response_json_schema"} {
				out, _ = sjson.DeleteBytes(out, "request.generationConfig."+schemaKey)
			}
			out, _ = sjson.SetBytes(out, "request.generationConfig.responseMimeType", "application/json")
			if responseFormatType == "json_schema" {
				if schema := responseFormat.Get("json_schema.schema"); schema.Exists() {
					out, _ = sjson.SetRawBytes(out, "request.generationConfig.responseSchema", []byte(schema.Raw))
				}
			}
		}
	}

	// Candidate count (OpenAI 'n' parameter)
	if n := gjson.GetBytes(rawJSON, "n"); n.Exists() && n.Type == gjson.Number {
		if val := n.Int(); val > 1 {
			out, _ = sjson.SetBytes(out, "request.generationConfig.candidateCount", val)
		}
	}

	// Map OpenAI modalities -> Antigravity request.generationConfig.responseModalities
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
		systemParts := make([][]byte, 0, 2)
		contentItems := make([][]byte, 0, len(arr))
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

		for i := 0; i < len(arr); i++ {
			m := arr[i]
			role := m.Get("role").String()
			content := m.Get("content")

			if (role == "system" || role == "developer") && len(arr) > 1 {
				// system -> request.systemInstruction as a user message style
				if content.Type == gjson.String {
					systemParts = append(systemParts, antigravityOpenAITextPart(content.String()))
				} else if content.IsObject() && content.Get("type").String() == "text" {
					systemParts = append(systemParts, antigravityOpenAITextPart(content.Get("text").String()))
				} else if content.IsArray() {
					for _, contentPart := range content.Array() {
						systemParts = append(systemParts, antigravityOpenAITextPart(contentPart.Get("text").String()))
					}
				}
			} else if role == "user" || ((role == "system" || role == "developer") && len(arr) == 1) {
				partItems := make([][]byte, 0, 4)
				if content.Type == gjson.String {
					partItems = append(partItems, antigravityOpenAITextPart(content.String()))
				} else if content.IsArray() {
					for _, item := range content.Array() {
						itemType := item.Get("type").String()
						switch itemType {
						case "text":
							partItems = append(partItems, antigravityOpenAITextPart(item.Get("text").String()))
						case "image_url":
							url := item.Get("image_url.url").String()
							if url != "" {
								if mimeType, data, ok := translatorcommon.ParseDataURL(url); ok {
									partItems = append(partItems, antigravityOpenAIInlineDataPart(mimeType, data, false))
								}
							}
						case "input_audio":
							format := item.Get("input_audio.format").String()
							data := item.Get("input_audio.data").String()
							if format != "" && data != "" {
								mimeType := "audio/" + format
								partItems = append(partItems, antigravityOpenAIInlineDataPart(mimeType, data, false))
							}
						case "file_data":
							fileData := item.Get("file_data")
							if fileData.Exists() && fileData.IsObject() {
								fileDataRaw := fileData.Raw
								if fileData.Get("file_uri").Exists() {
									if renamed, errRename := util.RenameKey(fileDataRaw, "file_uri", "fileUri"); errRename == nil {
										fileDataRaw = renamed
									}
								}
								if fileData.Get("mime_type").Exists() {
									if renamed, errRename := util.RenameKey(fileDataRaw, "mime_type", "mimeType"); errRename == nil {
										fileDataRaw = renamed
									}
								}
								if dataURL := fileData.Get("file_data").String(); dataURL != "" {
									if mimeType, base64Data, ok := translatorcommon.ParseDataURL(dataURL); ok {
										partItems = append(partItems, antigravityOpenAIInlineDataPart(mimeType, base64Data, false))
										continue
									}
								}
								part := []byte(`{"fileData":{}}`)
								part, _ = sjson.SetRawBytes(part, "fileData", []byte(fileDataRaw))
								partItems = append(partItems, part)
							}
						}
					}
				}
				if len(partItems) > 0 {
					contentItems = append(contentItems, antigravityOpenAIContent("user", partItems))
				}
			} else if role == "assistant" {
				partItems := make([][]byte, 0, 4)
				// Preserve thinking content if present in assistant message
				partItems = appendOpenAIThinkingPartsToAntigravity(partItems, m, modelName)
				if content.Type == gjson.String && content.String() != "" {
					partItems = append(partItems, antigravityOpenAITextPart(content.String()))
				} else if content.IsArray() {
					for _, item := range content.Array() {
						if item.Get("type").String() == "text" {
							partItems = append(partItems, antigravityOpenAITextPart(item.Get("text").String()))
						}
					}
				}
				tcs := m.Get("tool_calls")
				if tcs.IsArray() {
					for _, tc := range tcs.Array() {
						if tc.Get("type").String() == "function" {
							fnName := tc.Get("function.name").String()
							mappedFnName := util.MapSanitizedFunctionName(functionNameMap, fnName)
							fnArgs := tc.Get("function.arguments").String()
							part := []byte(`{"functionCall":{"name":""}}`)
							part, _ = sjson.SetBytes(part, "functionCall.name", mappedFnName)
							if fnArgs != "" {
								var argsMap map[string]any
								if errUnmarshal := util.UnmarshalJSONCaseFold([]byte(fnArgs), &argsMap); errUnmarshal == nil {
									part, _ = sjson.SetBytes(part, "functionCall.args", argsMap)
								} else {
									part, _ = sjson.SetBytes(part, "functionCall.args", map[string]any{})
								}
							}
							if thoughtSig := tc.Get("thought_signature").String(); thoughtSig != "" {
								part, _ = sjson.SetBytes(part, "thoughtSignature", thoughtSig)
							} else if thoughtSig := tc.Get("thoughtSignature").String(); thoughtSig != "" {
								part, _ = sjson.SetBytes(part, "thoughtSignature", thoughtSig)
							} else {
								part, _ = sjson.SetBytes(part, "thoughtSignature", antigravityFunctionThoughtSignature)
							}
							partItems = append(partItems, part)
						}
					}
				}
				if len(partItems) > 0 {
					contentItems = append(contentItems, antigravityOpenAIContent("model", partItems))
				}
			} else if role == "tool" {
				// tool role -> functionResponse in user role
				toolCallID := m.Get("tool_call_id").String()
				fnName := tcID2Name[toolCallID]
				if fnName == "" {
					fnName = "function"
				}
				mappedFnName := util.MapSanitizedFunctionName(functionNameMap, fnName)
				c := m.Get("content")
				response := c.Raw
				if response == "" {
					response = `""`
				}

				part := []byte(`{"functionResponse":{"name":"","response":{"result":null}}}`)
				part, _ = sjson.SetBytes(part, "functionResponse.name", mappedFnName)
				if response != "null" {
					parsed := gjson.Parse(response)
					if parsed.Type == gjson.JSON {
						part, _ = sjson.SetRawBytes(part, "functionResponse.response.result", []byte(parsed.Raw))
					} else {
						part, _ = sjson.SetBytes(part, "functionResponse.response.result", response)
					}
				}
				responseParts := [][]byte{part}

				// Look ahead for consecutive tool messages and group them into the same user content
				for i+1 < len(arr) && arr[i+1].Get("role").String() == "tool" {
					i++
					nextM := arr[i]
					nextToolCallID := nextM.Get("tool_call_id").String()
					nextFnName := tcID2Name[nextToolCallID]
					if nextFnName == "" {
						nextFnName = "function"
					}
					nextMappedFnName := util.MapSanitizedFunctionName(functionNameMap, nextFnName)
					nextC := nextM.Get("content")
					nextResponse := nextC.Raw
					if nextResponse == "" {
						nextResponse = `""`
					}
					nextPart := []byte(`{"functionResponse":{"name":"","response":{"result":null}}}`)
					nextPart, _ = sjson.SetBytes(nextPart, "functionResponse.name", nextMappedFnName)
					if nextResponse != "null" {
						parsed := gjson.Parse(nextResponse)
						if parsed.Type == gjson.JSON {
							nextPart, _ = sjson.SetRawBytes(nextPart, "functionResponse.response.result", []byte(parsed.Raw))
						} else {
							nextPart, _ = sjson.SetBytes(nextPart, "functionResponse.response.result", nextResponse)
						}
					}
					responseParts = append(responseParts, nextPart)
				}
				contentItems = append(contentItems, antigravityOpenAIContent("user", responseParts))
			}
		}
		if len(systemParts) > 0 {
			out, _ = sjson.SetRawBytes(out, "request.systemInstruction", antigravityOpenAIContent("user", systemParts))
		}
		out = translatorcommon.SetRawArrayItems(out, "request.contents", contentItems)
	}

	// tools -> request.tools[].functionDeclarations + request.tools[].googleSearch/codeExecution/urlContext passthrough
	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.Exists() || !tools.IsArray() || len(tools.Array()) == 0 {
		tools = gjson.GetBytes(rawJSON, "functions")
	}
	toolResults := tools.Array()
	if tools.IsArray() && len(toolResults) > 0 {
		functionDeclarations := make([][]byte, 0, len(toolResults))
		googleSearchNodes := make([][]byte, 0)
		codeExecutionNodes := make([][]byte, 0)
		urlContextNodes := make([][]byte, 0)
		for _, t := range toolResults {
			fn := t.Get("function")
			if !fn.Exists() || !fn.IsObject() {
				if c := t.Get("custom"); c.Exists() && c.IsObject() {
					fn = c
				} else if t.Get("name").Exists() {
					fn = t
				}
			}
			if fn.Exists() && (fn.IsObject() || t.Get("type").String() == "function" || t.Get("type").String() == "custom") {
				name := fn.Get("name").String()
				if name == "" {
					name = t.Get("name").String()
				}
				if name != "" {
					desc := fn.Get("description").String()
					if desc == "" {
						desc = t.Get("description").String()
					}
					decl := []byte(`{"name":"","parametersJsonSchema":{"type":"object","properties":{}}}`)
					decl, _ = sjson.SetBytes(decl, "name", name)
					if desc != "" {
						decl, _ = sjson.SetBytes(decl, "description", desc)
					}
					var schemaRaw []byte
					for _, k := range []string{"parameters", "parametersJsonSchema", "parameters_json_schema", "input_schema", "schema"} {
						if s := fn.Get(k); s.Exists() && s.Raw != "" && s.Raw != "null" {
							schemaRaw = []byte(s.Raw)
							break
						}
						if s := t.Get(k); s.Exists() && s.Raw != "" && s.Raw != "null" {
							schemaRaw = []byte(s.Raw)
							break
						}
					}
					if len(schemaRaw) > 0 {
						decl, _ = sjson.SetRawBytes(decl, "parametersJsonSchema", schemaRaw)
					}
					mappedName := util.MapSanitizedFunctionName(functionNameMap, name)
					if strings.Contains(strings.ToLower(modelName), "claude") {
						mappedName = util.SanitizeClaudeFunctionName(name)
					}
					if mappedName != name {
						decl, _ = sjson.SetBytes(decl, "name", mappedName)
					}
					functionDeclarations = append(functionDeclarations, decl)
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
		deduplicated := util.DeduplicateFunctionDeclarations(translatorcommon.JoinRawArray(functionDeclarations))
		hasFunction := len(deduplicated) > 2
		if hasFunction || len(googleSearchNodes) > 0 || len(codeExecutionNodes) > 0 || len(urlContextNodes) > 0 {
			toolItems := make([][]byte, 0, 1+len(googleSearchNodes)+len(codeExecutionNodes)+len(urlContextNodes))
			if hasFunction {
				functionToolNode := []byte(`{"functionDeclarations":[]}`)
				functionToolNode, _ = sjson.SetRawBytes(functionToolNode, "functionDeclarations", deduplicated)
				toolItems = append(toolItems, functionToolNode)
			}
			toolItems = append(toolItems, googleSearchNodes...)
			toolItems = append(toolItems, codeExecutionNodes...)
			toolItems = append(toolItems, urlContextNodes...)
			out, _ = sjson.SetRawBytes(out, "request.tools", translatorcommon.JoinRawArray(toolItems))
		}
	}

	out = applyOpenAIToolChoiceToAntigravity(out, rawJSON, functionNameMap)
	if strings.Contains(strings.ToLower(modelName), "claude") {
		out = gemini.SanitizeAntigravityClaudeGeminiRequestSignatures(modelName, out)
	}
	return common.AttachDefaultSafetySettings(out, "request.safetySettings")
}

func antigravityOpenAITextPart(text string) []byte {
	part := []byte(`{"text":""}`)
	part, _ = sjson.SetBytes(part, "text", text)
	return part
}

func antigravityOpenAIInlineDataPart(mimeType, data string, snakeCase bool) []byte {
	part := []byte(`{"inlineData":{"mimeType":"","data":""}}`)
	if snakeCase {
		part = []byte(`{"inlineData":{"mime_type":"","data":""}}`)
		part, _ = sjson.SetBytes(part, "inlineData.mime_type", mimeType)
	} else {
		part, _ = sjson.SetBytes(part, "inlineData.mimeType", mimeType)
	}
	part, _ = sjson.SetBytes(part, "inlineData.data", data)
	return part
}

func antigravityOpenAIContent(role string, parts [][]byte) []byte {
	content := []byte(`{"role":"","parts":[]}`)
	content, _ = sjson.SetBytes(content, "role", role)
	content, _ = sjson.SetRawBytes(content, "parts", translatorcommon.JoinRawArray(parts))
	return content
}
