package responses

import (
	"bytes"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const geminiResponsesThoughtSignature = "skip_thought_signature_validator"

func ConvertOpenAIResponsesRequestToGemini(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := bytes.Clone(inputRawJSON)

	// Note: modelName and stream parameters are part of the fixed method signature
	_ = modelName // Unused but required by interface
	_ = stream    // Unused but required by interface

	// Base Gemini API template (do not include thinkingConfig by default)
	out := `{"contents":[]}`

	root := gjson.ParseBytes(rawJSON)

	// Extract system instruction from OpenAI "instructions" field
	if instructions := root.Get("instructions"); instructions.Exists() {
		systemInstr := `{"parts":[{"text":""}]}`
		systemInstr, _ = sjson.Set(systemInstr, "parts.0.text", instructions.String())
		out, _ = sjson.SetRaw(out, "system_instruction", systemInstr)
	}

	// Convert input messages to Gemini contents format
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		items := input.Array()

		// Normalize consecutive function calls and outputs so each call is immediately followed by its response
		normalized := make([]gjson.Result, 0, len(items))
		for i := 0; i < len(items); {
			item := items[i]
			itemType := item.Get("type").String()
			itemRole := item.Get("role").String()
			if itemType == "" && itemRole != "" {
				itemType = "message"
			}

			if itemType == "function_call" {
				var calls []gjson.Result
				var outputs []gjson.Result

				for i < len(items) {
					next := items[i]
					nextType := next.Get("type").String()
					nextRole := next.Get("role").String()
					if nextType == "" && nextRole != "" {
						nextType = "message"
					}
					if nextType != "function_call" {
						break
					}
					calls = append(calls, next)
					i++
				}

				for i < len(items) {
					next := items[i]
					nextType := next.Get("type").String()
					nextRole := next.Get("role").String()
					if nextType == "" && nextRole != "" {
						nextType = "message"
					}
					if nextType != "function_call_output" {
						break
					}
					outputs = append(outputs, next)
					i++
				}

				if len(calls) > 0 {
					outputMap := make(map[string]gjson.Result, len(outputs))
					for _, out := range outputs {
						outputMap[out.Get("call_id").String()] = out
					}
					for _, call := range calls {
						normalized = append(normalized, call)
						callID := call.Get("call_id").String()
						if resp, ok := outputMap[callID]; ok {
							normalized = append(normalized, resp)
							delete(outputMap, callID)
						}
					}
					for _, out := range outputs {
						if _, ok := outputMap[out.Get("call_id").String()]; ok {
							normalized = append(normalized, out)
						}
					}
					continue
				}
			}

			if itemType == "function_call_output" {
				normalized = append(normalized, item)
				i++
				continue
			}

			normalized = append(normalized, item)
			i++
		}

		for _, item := range normalized {
			itemType := item.Get("type").String()
			itemRole := item.Get("role").String()
			if itemType == "" && itemRole != "" {
				itemType = "message"
			}

			switch itemType {
			case "message":
				if strings.EqualFold(itemRole, "system") {
					if contentArray := item.Get("content"); contentArray.Exists() && contentArray.IsArray() {
						var builder strings.Builder
						contentArray.ForEach(func(_, contentItem gjson.Result) bool {
							text := contentItem.Get("text").String()
							if builder.Len() > 0 && text != "" {
								builder.WriteByte('\n')
							}
							builder.WriteString(text)
							return true
						})
						if !gjson.Get(out, "system_instruction").Exists() {
							systemInstr := `{"parts":[{"text":""}]}`
							systemInstr, _ = sjson.Set(systemInstr, "parts.0.text", builder.String())
							out, _ = sjson.SetRaw(out, "system_instruction", systemInstr)
						}
					}
					continue
				}

				// Handle regular messages
				// Note: In Responses format, model outputs may appear as content items with type "output_text"
				// even when the message.role is "user". We split such items into distinct Gemini messages
				// with roles derived from the content type to match docs/convert-2.md.
				if contentArray := item.Get("content"); contentArray.Exists() && contentArray.IsArray() {
					currentRole := ""
					var currentParts []string

					flush := func() {
						if currentRole == "" || len(currentParts) == 0 {
							currentParts = nil
							return
						}
						one := `{"role":"","parts":[]}`
						one, _ = sjson.Set(one, "role", currentRole)
						for _, part := range currentParts {
							one, _ = sjson.SetRaw(one, "parts.-1", part)
						}
						out, _ = sjson.SetRaw(out, "contents.-1", one)
						currentParts = nil
					}

					contentArray.ForEach(func(_, contentItem gjson.Result) bool {
						contentType := contentItem.Get("type").String()
						if contentType == "" {
							contentType = "input_text"
						}

						effRole := "user"
						if itemRole != "" {
							switch strings.ToLower(itemRole) {
							case "assistant", "model":
								effRole = "model"
							default:
								effRole = strings.ToLower(itemRole)
							}
						}
						if contentType == "output_text" {
							effRole = "model"
						}
						if effRole == "assistant" {
							effRole = "model"
						}

						if currentRole != "" && effRole != currentRole {
							flush()
							currentRole = ""
						}
						if currentRole == "" {
							currentRole = effRole
						}

						var partJSON string
						switch contentType {
						case "input_text", "output_text":
							if text := contentItem.Get("text"); text.Exists() {
								partJSON = `{"text":""}`
								partJSON, _ = sjson.Set(partJSON, "text", text.String())
							}
						case "input_image":
							imageURL := contentItem.Get("image_url").String()
							if imageURL == "" {
								imageURL = contentItem.Get("url").String()
							}
							if imageURL != "" {
								mimeType := "application/octet-stream"
								data := ""
								if strings.HasPrefix(imageURL, "data:") {
									trimmed := strings.TrimPrefix(imageURL, "data:")
									mediaAndData := strings.SplitN(trimmed, ";base64,", 2)
									if len(mediaAndData) == 2 {
										if mediaAndData[0] != "" {
											mimeType = mediaAndData[0]
										}
										data = mediaAndData[1]
									} else {
										mediaAndData = strings.SplitN(trimmed, ",", 2)
										if len(mediaAndData) == 2 {
											if mediaAndData[0] != "" {
												mimeType = mediaAndData[0]
											}
											data = mediaAndData[1]
										}
									}
								}
								if data != "" {
									partJSON = `{"inline_data":{"mime_type":"","data":""}}`
									partJSON, _ = sjson.Set(partJSON, "inline_data.mime_type", mimeType)
									partJSON, _ = sjson.Set(partJSON, "inline_data.data", data)
								}
							}
						}

						if partJSON != "" {
							currentParts = append(currentParts, partJSON)
						}
						return true
					})

					flush()
				}

			case "function_call":
				// Handle function calls - convert to model message with functionCall
				name := item.Get("name").String()
				arguments := item.Get("arguments").String()

				modelContent := `{"role":"model","parts":[]}`
				functionCall := `{"functionCall":{"name":"","args":{}}}`
				functionCall, _ = sjson.Set(functionCall, "functionCall.name", name)
				functionCall, _ = sjson.Set(functionCall, "thoughtSignature", geminiResponsesThoughtSignature)
				functionCall, _ = sjson.Set(functionCall, "functionCall.id", item.Get("call_id").String())

				// Parse arguments JSON string and set as args object
				if arguments != "" {
					argsResult := gjson.Parse(arguments)
					functionCall, _ = sjson.SetRaw(functionCall, "functionCall.args", argsResult.Raw)
				}

				modelContent, _ = sjson.SetRaw(modelContent, "parts.-1", functionCall)
				out, _ = sjson.SetRaw(out, "contents.-1", modelContent)

			case "function_call_output":
				// Handle function call outputs - convert to function message with functionResponse
				callID := item.Get("call_id").String()
				// Use .Raw to preserve the JSON encoding (includes quotes for strings)
				outputRaw := item.Get("output").Str

				functionContent := `{"role":"function","parts":[]}`
				functionResponse := `{"functionResponse":{"name":"","response":{}}}`

				// We need to extract the function name from the previous function_call
				// For now, we'll use a placeholder or extract from context if available
				functionName := "unknown" // This should ideally be matched with the corresponding function_call

				// Find the corresponding function call name by matching call_id
				// We need to look back through the input array to find the matching call
				if inputArray := root.Get("input"); inputArray.Exists() && inputArray.IsArray() {
					inputArray.ForEach(func(_, prevItem gjson.Result) bool {
						if prevItem.Get("type").String() == "function_call" && prevItem.Get("call_id").String() == callID {
							functionName = prevItem.Get("name").String()
							return false // Stop iteration
						}
						return true
					})
				}

				functionResponse, _ = sjson.Set(functionResponse, "functionResponse.name", functionName)
				functionResponse, _ = sjson.Set(functionResponse, "functionResponse.id", callID)

				// Set the raw JSON output directly (preserves string encoding)
				if outputRaw != "" && outputRaw != "null" {
					output := gjson.Parse(outputRaw)
					if output.Type == gjson.JSON {
						functionResponse, _ = sjson.SetRaw(functionResponse, "functionResponse.response.result", output.Raw)
					} else {
						functionResponse, _ = sjson.Set(functionResponse, "functionResponse.response.result", outputRaw)
					}
				}
				functionContent, _ = sjson.SetRaw(functionContent, "parts.-1", functionResponse)
				out, _ = sjson.SetRaw(out, "contents.-1", functionContent)
			}
		}
	} else if input.Exists() && input.Type == gjson.String {
		// Simple string input conversion to user message
		userContent := `{"role":"user","parts":[{"text":""}]}`
		userContent, _ = sjson.Set(userContent, "parts.0.text", input.String())
		out, _ = sjson.SetRaw(out, "contents.-1", userContent)
	}

	// Convert tools to Gemini functionDeclarations format
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		geminiTools := `[{"functionDeclarations":[]}]`

		tools.ForEach(func(_, tool gjson.Result) bool {
			if tool.Get("type").String() == "function" {
				funcDecl := `{"name":"","description":"","parametersJsonSchema":{}}`

				if name := tool.Get("name"); name.Exists() {
					funcDecl, _ = sjson.Set(funcDecl, "name", name.String())
				}
				if desc := tool.Get("description"); desc.Exists() {
					funcDecl, _ = sjson.Set(funcDecl, "description", desc.String())
				}
				if params := tool.Get("parameters"); params.Exists() {
					// Convert parameter types from OpenAI format to Gemini format
					cleaned := params.Raw
					// Convert type values to uppercase for Gemini
					paramsResult := gjson.Parse(cleaned)
					if properties := paramsResult.Get("properties"); properties.Exists() {
						properties.ForEach(func(key, value gjson.Result) bool {
							if propType := value.Get("type"); propType.Exists() {
								upperType := strings.ToUpper(propType.String())
								cleaned, _ = sjson.Set(cleaned, "properties."+key.String()+".type", upperType)
							}
							return true
						})
					}
					// Set the overall type to OBJECT
					cleaned, _ = sjson.Set(cleaned, "type", "OBJECT")
					funcDecl, _ = sjson.SetRaw(funcDecl, "parametersJsonSchema", cleaned)
				}

				geminiTools, _ = sjson.SetRaw(geminiTools, "0.functionDeclarations.-1", funcDecl)
			}
			return true
		})

		// Only add tools if there are function declarations
		if funcDecls := gjson.Get(geminiTools, "0.functionDeclarations"); funcDecls.Exists() && len(funcDecls.Array()) > 0 {
			out, _ = sjson.SetRaw(out, "tools", geminiTools)
		}
	}

	// Handle generation config from OpenAI format
	if maxOutputTokens := root.Get("max_output_tokens"); maxOutputTokens.Exists() {
		genConfig := `{"maxOutputTokens":0}`
		genConfig, _ = sjson.Set(genConfig, "maxOutputTokens", maxOutputTokens.Int())
		out, _ = sjson.SetRaw(out, "generationConfig", genConfig)
	}

	// Handle temperature if present
	if temperature := root.Get("temperature"); temperature.Exists() {
		if !gjson.Get(out, "generationConfig").Exists() {
			out, _ = sjson.SetRaw(out, "generationConfig", `{}`)
		}
		out, _ = sjson.Set(out, "generationConfig.temperature", temperature.Float())
	}

	// Handle top_p if present
	if topP := root.Get("top_p"); topP.Exists() {
		if !gjson.Get(out, "generationConfig").Exists() {
			out, _ = sjson.SetRaw(out, "generationConfig", `{}`)
		}
		out, _ = sjson.Set(out, "generationConfig.topP", topP.Float())
	}

	// Handle stop sequences
	if stopSequences := root.Get("stop_sequences"); stopSequences.Exists() && stopSequences.IsArray() {
		if !gjson.Get(out, "generationConfig").Exists() {
			out, _ = sjson.SetRaw(out, "generationConfig", `{}`)
		}
		var sequences []string
		stopSequences.ForEach(func(_, seq gjson.Result) bool {
			sequences = append(sequences, seq.String())
			return true
		})
		out, _ = sjson.Set(out, "generationConfig.stopSequences", sequences)
	}

	// OpenAI official reasoning fields take precedence
	// Only convert for models that use numeric budgets (not discrete levels).
	hasOfficialThinking := root.Get("reasoning.effort").Exists()
	if hasOfficialThinking && util.ModelSupportsThinking(modelName) && !util.ModelUsesThinkingLevels(modelName) {
		reasoningEffort := root.Get("reasoning.effort")
		out = string(util.ApplyReasoningEffortToGemini([]byte(out), reasoningEffort.String()))
	}

	// Cherry Studio extension (applies only when official fields are missing)
	// Only apply for models that use numeric budgets, not discrete levels.
	if !hasOfficialThinking && util.ModelSupportsThinking(modelName) && !util.ModelUsesThinkingLevels(modelName) {
		if tc := root.Get("extra_body.google.thinking_config"); tc.Exists() && tc.IsObject() {
			var setBudget bool
			var budget int
			if v := tc.Get("thinking_budget"); v.Exists() {
				budget = int(v.Int())
				out, _ = sjson.Set(out, "generationConfig.thinkingConfig.thinkingBudget", budget)
				setBudget = true
			}
			if v := tc.Get("include_thoughts"); v.Exists() {
				out, _ = sjson.Set(out, "generationConfig.thinkingConfig.include_thoughts", v.Bool())
			} else if setBudget {
				if budget != 0 {
					out, _ = sjson.Set(out, "generationConfig.thinkingConfig.include_thoughts", true)
				}
			}
		}
	}

	result := []byte(out)
	result = common.AttachDefaultSafetySettings(result, "safetySettings")
	return result
}
