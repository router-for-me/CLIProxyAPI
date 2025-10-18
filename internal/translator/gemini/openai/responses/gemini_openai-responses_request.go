package responses

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertOpenAIResponsesRequestToGemini(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := bytes.Clone(inputRawJSON)

	// Note: modelName and stream parameters are part of the fixed method signature
	_ = modelName // Unused but required by interface
	_ = stream    // Unused but required by interface

	// Base Gemini API template
	out := `{"contents":[],"generationConfig":{"thinkingConfig":{"include_thoughts":true}}}`

	root := gjson.ParseBytes(rawJSON)

	// Extract system instruction from OpenAI "instructions" field
	if instructions := root.Get("instructions"); instructions.Exists() {
		systemInstr := `{"parts":[{"text":""}]}`
		systemInstr, _ = sjson.Set(systemInstr, "parts.0.text", instructions.String())
		out, _ = sjson.SetRaw(out, "system_instruction", systemInstr)
	}

	// Convert input messages to Gemini contents format
	if input := root.Get("input"); input.Exists() && input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
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
					return true
				}

				// Handle regular messages
				// Note: In Responses format, model outputs may appear as content items with type "output_text"
				// even when the message.role is "user". We split such items into distinct Gemini messages
				// with roles derived from the content type to match docs/convert-2.md.
				if contentArray := item.Get("content"); contentArray.Exists() && contentArray.IsArray() {
					contentArray.ForEach(func(_, contentItem gjson.Result) bool {
						contentType := contentItem.Get("type").String()
						if contentType == "" {
							contentType = "input_text"
						}
						switch contentType {
						case "input_text", "output_text":
							if text := contentItem.Get("text"); text.Exists() {
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
								one := `{"role":"","parts":[]}`
								one, _ = sjson.Set(one, "role", effRole)
								textPart := `{"text":""}`
								textPart, _ = sjson.Set(textPart, "text", text.String())
								one, _ = sjson.SetRaw(one, "parts.-1", textPart)
								out, _ = sjson.SetRaw(out, "contents.-1", one)
							}
						}
						return true
					})
				}

			case "function_call":
				// Handle function calls - convert to model message with functionCall
				name := item.Get("name").String()
				arguments := item.Get("arguments").String()

				modelContent := `{"role":"model","parts":[]}`
				functionCall := `{"functionCall":{"name":"","args":{}}}`
				functionCall, _ = sjson.Set(functionCall, "functionCall.name", name)

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
				output := item.Get("output").String()

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
				// Also set response.name to align with docs/convert-2.md
				functionResponse, _ = sjson.Set(functionResponse, "functionResponse.response.name", functionName)

				// Parse output JSON string and set as response content
				if output != "" {
					outputResult := gjson.Parse(output)
					if outputResult.IsObject() {
						functionResponse, _ = sjson.SetRaw(functionResponse, "functionResponse.response.content", outputResult.String())
					} else {
						functionResponse, _ = sjson.Set(functionResponse, "functionResponse.response.content", output)
					}
				}

				functionContent, _ = sjson.SetRaw(functionContent, "parts.-1", functionResponse)
				out, _ = sjson.SetRaw(out, "contents.-1", functionContent)
			}

			return true
		})
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

	if reasoningEffort := root.Get("reasoning.effort"); reasoningEffort.Exists() {
		switch reasoningEffort.String() {
		case "none":
			out, _ = sjson.Set(out, "generationConfig.thinkingConfig.include_thoughts", false)
			out, _ = sjson.Set(out, "generationConfig.thinkingConfig.thinkingBudget", 0)
		case "auto":
			out, _ = sjson.Set(out, "generationConfig.thinkingConfig.thinkingBudget", -1)
		case "minimal":
			out, _ = sjson.Set(out, "generationConfig.thinkingConfig.thinkingBudget", 1024)
		case "low":
			out, _ = sjson.Set(out, "generationConfig.thinkingConfig.thinkingBudget", 4096)
		case "medium":
			out, _ = sjson.Set(out, "generationConfig.thinkingConfig.thinkingBudget", 8192)
		case "high":
			out, _ = sjson.Set(out, "generationConfig.thinkingConfig.thinkingBudget", 24576)
		default:
			out, _ = sjson.Set(out, "generationConfig.thinkingConfig.thinkingBudget", -1)
		}
	}
	return []byte(out)
}
