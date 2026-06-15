package responses

import (
	"encoding/json"
	"strings"

	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const geminiResponsesThoughtSignature = "skip_thought_signature_validator"

func ConvertOpenAIResponsesRequestToGemini(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON

	// Note: modelName and stream parameters are part of the fixed method signature
	_ = modelName // Unused but required by interface
	_ = stream    // Unused but required by interface

	// Base Gemini API template (do not include thinkingConfig by default)
	out := []byte(`{"contents":[]}`)

	root := gjson.ParseBytes(rawJSON)

	// Extract system instruction from OpenAI "instructions" field
	if instructions := root.Get("instructions"); instructions.Exists() {
		systemInstr := []byte(`{"parts":[{"text":""}]}`)
		systemInstr, _ = sjson.SetBytes(systemInstr, "parts.0.text", instructions.String())
		out, _ = sjson.SetRawBytes(out, "systemInstruction", systemInstr)
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
					for _, outItem := range outputs {
						outputMap[outItem.Get("call_id").String()] = outItem
					}
					for _, call := range calls {
						normalized = append(normalized, call)
						callID := call.Get("call_id").String()
						if resp, ok := outputMap[callID]; ok {
							normalized = append(normalized, resp)
							delete(outputMap, callID)
						}
					}
					for _, outItem := range outputs {
						if _, ok := outputMap[outItem.Get("call_id").String()]; ok {
							normalized = append(normalized, outItem)
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
				if strings.EqualFold(itemRole, "system") || strings.EqualFold(itemRole, "developer") {
					if contentArray := item.Get("content"); contentArray.Exists() {
						systemInstr := []byte(`{"parts":[]}`)
						if systemInstructionResult := gjson.GetBytes(out, "systemInstruction"); systemInstructionResult.Exists() {
							systemInstr = []byte(systemInstructionResult.Raw)
						}

						if contentArray.IsArray() {
							contentArray.ForEach(func(_, contentItem gjson.Result) bool {
								part := []byte(`{"text":""}`)
								text := contentItem.Get("text").String()
								part, _ = sjson.SetBytes(part, "text", text)
								systemInstr, _ = sjson.SetRawBytes(systemInstr, "parts.-1", part)
								return true
							})
						} else if contentArray.Type == gjson.String {
							part := []byte(`{"text":""}`)
							part, _ = sjson.SetBytes(part, "text", contentArray.String())
							systemInstr, _ = sjson.SetRawBytes(systemInstr, "parts.-1", part)
						}

						if gjson.GetBytes(systemInstr, "parts.#").Int() > 0 {
							out, _ = sjson.SetRawBytes(out, "systemInstruction", systemInstr)
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
					currentParts := make([][]byte, 0)

					flush := func() {
						if currentRole == "" || len(currentParts) == 0 {
							currentParts = currentParts[:0]
							return
						}
						one := []byte(`{"role":"","parts":[]}`)
						one, _ = sjson.SetBytes(one, "role", currentRole)
						for _, part := range currentParts {
							one, _ = sjson.SetRawBytes(one, "parts.-1", part)
						}
						out, _ = sjson.SetRawBytes(out, "contents.-1", one)
						currentParts = currentParts[:0]
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

						var partJSON []byte
						switch contentType {
						case "input_text", "output_text":
							if text := contentItem.Get("text"); text.Exists() {
								partJSON = []byte(`{"text":""}`)
								partJSON, _ = sjson.SetBytes(partJSON, "text", text.String())
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
									partJSON = []byte(`{"inline_data":{"mime_type":"","data":""}}`)
									partJSON, _ = sjson.SetBytes(partJSON, "inline_data.mime_type", mimeType)
									partJSON, _ = sjson.SetBytes(partJSON, "inline_data.data", data)
								}
							}
						case "input_audio":
							audioData := contentItem.Get("data").String()
							audioFormat := contentItem.Get("format").String()
							if audioData != "" {
								audioMimeMap := map[string]string{
									"mp3":       "audio/mpeg",
									"wav":       "audio/wav",
									"ogg":       "audio/ogg",
									"flac":      "audio/flac",
									"aac":       "audio/aac",
									"webm":      "audio/webm",
									"pcm16":     "audio/pcm",
									"g711_ulaw": "audio/basic",
									"g711_alaw": "audio/basic",
								}
								mimeType := "audio/wav"
								if audioFormat != "" {
									if mapped, ok := audioMimeMap[audioFormat]; ok {
										mimeType = mapped
									} else {
										mimeType = "audio/" + audioFormat
									}
								}
								partJSON = []byte(`{"inline_data":{"mime_type":"","data":""}}`)
								partJSON, _ = sjson.SetBytes(partJSON, "inline_data.mime_type", mimeType)
								partJSON, _ = sjson.SetBytes(partJSON, "inline_data.data", audioData)
							}
						}

						if len(partJSON) > 0 {
							currentParts = append(currentParts, partJSON)
						}
						return true
					})

					flush()
				} else if contentArray.Type == gjson.String {
					effRole := "user"
					if itemRole != "" {
						switch strings.ToLower(itemRole) {
						case "assistant", "model":
							effRole = "model"
						default:
							effRole = strings.ToLower(itemRole)
						}
					}

					one := []byte(`{"role":"","parts":[{"text":""}]}`)
					one, _ = sjson.SetBytes(one, "role", effRole)
					one, _ = sjson.SetBytes(one, "parts.0.text", contentArray.String())
					out, _ = sjson.SetRawBytes(out, "contents.-1", one)
				}

			case "function_call":
				// Handle function calls - convert to model message with functionCall
				name := util.SanitizeFunctionName(item.Get("name").String())
				arguments := item.Get("arguments").String()

				modelContent := []byte(`{"role":"model","parts":[]}`)
				functionCall := []byte(`{"functionCall":{"name":"","args":{}}}`)
				functionCall, _ = sjson.SetBytes(functionCall, "functionCall.name", name)
				functionCall, _ = sjson.SetBytes(functionCall, "thoughtSignature", geminiResponsesThoughtSignature)
				functionCall, _ = sjson.SetBytes(functionCall, "functionCall.id", item.Get("call_id").String())

				// Parse arguments JSON string and set as args object
				if arguments != "" {
					argsResult := gjson.Parse(arguments)
					functionCall, _ = sjson.SetRawBytes(functionCall, "functionCall.args", []byte(argsResult.Raw))
				}

				modelContent, _ = sjson.SetRawBytes(modelContent, "parts.-1", functionCall)
				out, _ = sjson.SetRawBytes(out, "contents.-1", modelContent)

			case "function_call_output":
				// Handle function call outputs - convert to function message with functionResponse
				callID := item.Get("call_id").String()
				// Use .Raw to preserve the JSON encoding (includes quotes for strings)
				outputRaw := item.Get("output").Str

				functionContent := []byte(`{"role":"function","parts":[]}`)
				functionResponse := []byte(`{"functionResponse":{"name":"","response":{}}}`)

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
				functionName = util.SanitizeFunctionName(functionName)

				functionResponse, _ = sjson.SetBytes(functionResponse, "functionResponse.name", functionName)
				functionResponse, _ = sjson.SetBytes(functionResponse, "functionResponse.id", callID)

				// Set the raw JSON output directly (preserves string encoding)
				if outputRaw != "" && outputRaw != "null" {
					output := gjson.Parse(outputRaw)
					if output.Type == gjson.JSON && json.Valid([]byte(output.Raw)) {
						functionResponse, _ = sjson.SetRawBytes(functionResponse, "functionResponse.response.result", []byte(output.Raw))
					} else {
						functionResponse, _ = sjson.SetBytes(functionResponse, "functionResponse.response.result", outputRaw)
					}
				}
				functionContent, _ = sjson.SetRawBytes(functionContent, "parts.-1", functionResponse)
				out, _ = sjson.SetRawBytes(out, "contents.-1", functionContent)

			case "reasoning":
				thoughtContent := []byte(`{"role":"model","parts":[]}`)
				thought := []byte(`{"text":"","thoughtSignature":"","thought":true}`)
				thought, _ = sjson.SetBytes(thought, "text", item.Get("summary.0.text").String())
				thought, _ = sjson.SetBytes(thought, "thoughtSignature", openAIResponsesGeminiThoughtSignature(item.Get("encrypted_content").String()))

				thoughtContent, _ = sjson.SetRawBytes(thoughtContent, "parts.-1", thought)
				out, _ = sjson.SetRawBytes(out, "contents.-1", thoughtContent)
			}
		}
	} else if input.Exists() && input.Type == gjson.String {
		// Simple string input conversion to user message
		userContent := []byte(`{"role":"user","parts":[{"text":""}]}`)
		userContent, _ = sjson.SetBytes(userContent, "parts.0.text", input.String())
		out, _ = sjson.SetRawBytes(out, "contents.-1", userContent)
	}

	// Convert tools to Gemini functionDeclarations format
	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		geminiTools := []byte(`[{"functionDeclarations":[]}]`)

		tools.ForEach(func(_, tool gjson.Result) bool {
			if tool.Get("type").String() == "function" {
				funcDecl := []byte(`{"name":"","description":"","parametersJsonSchema":{}}`)

				if name := tool.Get("name"); name.Exists() {
					funcDecl, _ = sjson.SetBytes(funcDecl, "name", util.SanitizeFunctionName(name.String()))
				}
				if desc := tool.Get("description"); desc.Exists() {
					funcDecl, _ = sjson.SetBytes(funcDecl, "description", desc.String())
				}
				if params := tool.Get("parameters"); params.Exists() {
					funcDecl, _ = sjson.SetRawBytes(funcDecl, "parametersJsonSchema", []byte(params.Raw))
				}

				geminiTools, _ = sjson.SetRawBytes(geminiTools, "0.functionDeclarations.-1", funcDecl)
			}
			return true
		})

		// Only add tools if there are function declarations
		if funcDecls := gjson.GetBytes(geminiTools, "0.functionDeclarations"); funcDecls.Exists() && len(funcDecls.Array()) > 0 {
			out, _ = sjson.SetRawBytes(out, "tools", geminiTools)
		}
	}

	// Handle generation config from OpenAI format
	if maxOutputTokens := root.Get("max_output_tokens"); maxOutputTokens.Exists() {
		genConfig := []byte(`{"maxOutputTokens":0}`)
		genConfig, _ = sjson.SetBytes(genConfig, "maxOutputTokens", maxOutputTokens.Int())
		out, _ = sjson.SetRawBytes(out, "generationConfig", genConfig)
	}

	// Handle JSON schema structured output (OpenAI text.format with type=json_schema)
	if textFormat := root.Get("text.format"); textFormat.Exists() {
		formatType := textFormat.Get("type").String()
		if formatType == "json_schema" {
			// Set response_mime_type to application/json
			if !gjson.GetBytes(out, "generationConfig").Exists() {
				out, _ = sjson.SetRawBytes(out, "generationConfig", []byte(`{}`))
			}
			out, _ = sjson.SetBytes(out, "generationConfig.responseMimeType", "application/json")

			// Set responseJsonSchema from the schema field, normalizing it for Gemini's
			// supported JSON Schema subset (removes unsupported keywords like pattern,
			// minLength, multipleOf, etc. but preserves additionalProperties, $defs, $ref)
			if schema := textFormat.Get("schema"); schema.Exists() {
				schemaStr := normalizeJSONSchema(schema.Raw)
				out, _ = sjson.SetRawBytes(out, "generationConfig.responseJsonSchema", []byte(schemaStr))
			}
		}
	}

	// Handle temperature if present
	if temperature := root.Get("temperature"); temperature.Exists() {
		if !gjson.GetBytes(out, "generationConfig").Exists() {
			out, _ = sjson.SetRawBytes(out, "generationConfig", []byte(`{}`))
		}
		out, _ = sjson.SetBytes(out, "generationConfig.temperature", temperature.Float())
	}

	// Handle top_p if present
	if topP := root.Get("top_p"); topP.Exists() {
		if !gjson.GetBytes(out, "generationConfig").Exists() {
			out, _ = sjson.SetRawBytes(out, "generationConfig", []byte(`{}`))
		}
		out, _ = sjson.SetBytes(out, "generationConfig.topP", topP.Float())
	}

	// Handle stop sequences
	if stopSequences := root.Get("stop_sequences"); stopSequences.Exists() && stopSequences.IsArray() {
		if !gjson.GetBytes(out, "generationConfig").Exists() {
			out, _ = sjson.SetRawBytes(out, "generationConfig", []byte(`{}`))
		}
		var sequences []string
		stopSequences.ForEach(func(_, seq gjson.Result) bool {
			sequences = append(sequences, seq.String())
			return true
		})
		out, _ = sjson.SetBytes(out, "generationConfig.stopSequences", sequences)
	}

	// Apply thinking configuration: convert OpenAI Responses API reasoning.effort to Gemini thinkingConfig.
	// Inline translation-only mapping; capability checks happen later in ApplyThinking.
	re := root.Get("reasoning.effort")
	if re.Exists() {
		effort := strings.ToLower(strings.TrimSpace(re.String()))
		if effort != "" {
			thinkingPath := "generationConfig.thinkingConfig"
			if effort == "auto" {
				out, _ = sjson.SetBytes(out, thinkingPath+".thinkingBudget", -1)
				out, _ = sjson.SetBytes(out, thinkingPath+".includeThoughts", true)
			} else {
				out, _ = sjson.SetBytes(out, thinkingPath+".thinkingLevel", effort)
				out, _ = sjson.SetBytes(out, thinkingPath+".includeThoughts", effort != "none")
			}
		}
	}

	result := out
	result = common.AttachDefaultSafetySettings(result, "safetySettings")
	return result
}

func openAIResponsesGeminiThoughtSignature(rawSignature string) string {
	return sigcompat.GeminiReplaySignatureOrBypass(rawSignature, sigcompat.SignatureBlockKindGeminiModelPart)
}

// normalizeSchemaForGemini recursively normalizes an OpenAI JSON Schema to Gemini's supported subset.
//
// OpenAI and Gemini have different JSON Schema support:
//
// OpenAI supports:
// - Types: string, number, boolean, integer, object, array, enum, anyOf
// - String: pattern, format (date-time, time, date, duration, email, hostname, ipv4, ipv6, uuid)
// - Number: multipleOf, maximum, exclusiveMaximum, minimum, exclusiveMinimum
// - Array: minItems, maxItems
// - Requires: additionalProperties: false, all fields in required
// - Root must be object (not anyOf)
// - Does NOT support: allOf, not, dependentRequired, dependentSchemas, if/then/else
//
// Gemini supports:
// - Types: string, number, integer, boolean, object, array, null (via type array ["string", "null"])
// - Object: properties, required, additionalProperties (boolean or schema)
// - Array: items, prefixItems, minItems, maxItems
// - String: enum, format (ONLY date-time, date, time)
// - Number/Integer: enum, minimum, maximum
// - Composition: anyOf
// - Recursive: $ref
// - Does NOT support: pattern, minLength, maxLength, multipleOf, exclusiveMinimum, exclusiveMaximum,
//   uniqueItems, contains, const, default, allOf, oneOf, not, if/then/else
//
// This function:
// 1. Preserves additionalProperties (Gemini JSON Schema mode supports it)
// 2. Merges allOf before iteration to avoid non-deterministic map iteration overwrites
// 3. Converts oneOf to anyOf, preserves nullable type arrays as-is
// 4. Removes unsupported keywords: pattern, minLength, maxLength, multipleOf, exclusiveMinimum,
//    exclusiveMaximum, uniqueItems, contains, const, default
// 5. Filters format to only date-time, date, time
// 6. Recursively processes all nested schemas
func normalizeSchemaForGemini(schema interface{}) interface{} {
	switch v := schema.(type) {
	case map[string]interface{}:
		// If allOf exists, merge it first to avoid non-deterministic overwrites during map iteration
		if allOf, exists := v["allOf"]; exists {
			if allOfArray, ok := allOf.([]interface{}); ok {
				merged := mergeAllOf(allOfArray)
				for k, val := range v {
					if k == "allOf" {
						continue
					}
					if k == "properties" {
						mergedProps, _ := merged["properties"].(map[string]interface{})
						if mergedProps == nil {
							mergedProps = make(map[string]interface{})
						} else {
							// Copy to avoid mutating original
							newProps := make(map[string]interface{})
							for pk, pv := range mergedProps {
								newProps[pk] = pv
							}
							mergedProps = newProps
						}
						if vProps, ok := val.(map[string]interface{}); ok {
							for pk, pv := range vProps {
								mergedProps[pk] = pv
							}
						}
						merged["properties"] = mergedProps
					} else if k == "required" {
						var mergedReq []interface{}
						if mr, ok := merged["required"].([]interface{}); ok {
							mergedReq = append(mergedReq, mr...)
						}
						if vr, ok := val.([]interface{}); ok {
							mergedReq = append(mergedReq, vr...)
						}
						merged["required"] = mergedReq
					} else {
						merged[k] = val
					}
				}
				v = merged
			}
		}

		result := make(map[string]interface{})

		for key, val := range v {
			switch key {
			case "allOf":
				// Already merged pre-iteration
				continue

			case "additionalProperties":
				// Preserve additionalProperties - Gemini JSON Schema mode supports it
				// Normalize if it's a schema, pass through booleans as-is
				if boolVal, ok := val.(bool); ok {
					result[key] = boolVal
				} else {
					result[key] = normalizeSchemaForGemini(val)
				}

			case "type":
				// Preserve type arrays with null (e.g., ["string", "null"]) for downstream handling
				// - Gemini/Vertex support type arrays natively
				// - CLI/Antigravity paths use CleanJSONSchemaForGemini which converts to description hints
				// Only flatten non-null type arrays (e.g., ["string", "integer"]) to anyOf
				if typeArray, ok := val.([]interface{}); ok {
					var nonNullTypes []string
					hasNull := false
					for _, t := range typeArray {
						if typeStr, ok := t.(string); ok {
							if typeStr == "null" {
								hasNull = true
							} else {
								nonNullTypes = append(nonNullTypes, typeStr)
							}
						}
					}
					// If contains null, preserve the array as-is
					if hasNull {
						result[key] = val
					} else if len(nonNullTypes) == 1 {
						// Single non-null type - use scalar
						result["type"] = nonNullTypes[0]
					} else if len(nonNullTypes) > 1 {
						// Multiple non-null types - use anyOf
						var anyOfSchemas []map[string]interface{}
						for _, t := range nonNullTypes {
							schema := map[string]interface{}{"type": t}
							anyOfSchemas = append(anyOfSchemas, schema)
						}
						result["anyOf"] = anyOfSchemas
					}
				} else {
					// Keep single type as-is
					result[key] = val
				}

			case "properties":
				// Recursively normalize each property
				if propsMap, ok := val.(map[string]interface{}); ok {
					normalizedProps := make(map[string]interface{})
					for propName, propSchema := range propsMap {
						normalizedProps[propName] = normalizeSchemaForGemini(propSchema)
					}
					result[key] = normalizedProps
				}

			case "items":
				// Recursively normalize items schema
				result[key] = normalizeSchemaForGemini(val)

			case "anyOf":
				// Recursively normalize each anyOf option
				if anyOfArray, ok := val.([]interface{}); ok {
					normalized := make([]interface{}, len(anyOfArray))
					for i, subSchema := range anyOfArray {
						normalized[i] = normalizeSchemaForGemini(subSchema)
					}
					result[key] = normalized
				}

			case "oneOf":
				// Convert oneOf to anyOf
				if oneOfArray, ok := val.([]interface{}); ok {
					normalized := make([]interface{}, len(oneOfArray))
					for i, subSchema := range oneOfArray {
						normalized[i] = normalizeSchemaForGemini(subSchema)
					}
					result["anyOf"] = normalized
				}

			case "prefixItems":
				// Recursively normalize prefix items
				if itemsArray, ok := val.([]interface{}); ok {
					normalized := make([]interface{}, len(itemsArray))
					for i, item := range itemsArray {
						normalized[i] = normalizeSchemaForGemini(item)
					}
					result[key] = normalized
				}

			case "format":
				// Only preserve supported formats: date-time, date, time
				if formatStr, ok := val.(string); ok {
					if formatStr == "date-time" || formatStr == "date" || formatStr == "time" {
						result[key] = val
					}
					// Drop other formats (email, hostname, ipv4, ipv6, uuid, duration)
				}

			case "required", "enum", "title", "description", "minimum", "maximum",
				"minItems", "maxItems":
				// These are supported by Gemini, preserve as-is
				result[key] = val

			case "$ref":
				// Gemini supports $ref for recursive schemas
				result[key] = val

			case "$defs", "definitions":
				// Preserve definition for recursive schemas
				if defsMap, ok := val.(map[string]interface{}); ok {
					normalized := make(map[string]interface{})
					for k, def := range defsMap {
						normalized[k] = normalizeSchemaForGemini(def)
					}
					result[key] = normalized
				}

			// Skip unsupported keywords:
			// pattern, minLength, maxLength (string constraints)
			// multipleOf, exclusiveMinimum, exclusiveMaximum (number constraints)
			// uniqueItems, contains (array constraints)
			// const, default (value constraints)
			// not, if, then, else (composition)
			// dependentRequired, dependentSchemas (dependencies)
			}
		}

		return result

	case []interface{}:
		result := make([]interface{}, len(v))
		for i, item := range v {
			result[i] = normalizeSchemaForGemini(item)
		}
		return result

	default:
		return v
	}
}

// mergeAllOf merges all schemas in an allOf array into a single schema
func mergeAllOf(schemas []interface{}) map[string]interface{} {
	merged := make(map[string]interface{})
	properties := make(map[string]interface{})
	var required []interface{}

	for _, schema := range schemas {
		if schemaMap, ok := schema.(map[string]interface{}); ok {
			for k, v := range schemaMap {
				switch k {
				case "properties":
					if propsMap, ok := v.(map[string]interface{}); ok {
						for pk, pv := range propsMap {
							properties[pk] = pv
						}
					}
				case "required":
					if reqArray, ok := v.([]interface{}); ok {
						required = append(required, reqArray...)
					}
				default:
					merged[k] = v
				}
			}
		}
	}

	// Build final merged schema
	if len(properties) > 0 {
		merged["properties"] = properties
	}
	if len(required) > 0 {
		// Deduplicate required fields
		seen := make(map[string]bool)
		var uniqueRequired []interface{}
		for _, r := range required {
			if rStr, ok := r.(string); ok {
				if !seen[rStr] {
					seen[rStr] = true
					uniqueRequired = append(uniqueRequired, rStr)
				}
			}
		}
		if len(uniqueRequired) > 0 {
			merged["required"] = uniqueRequired
		}
	}

	return merged
}

// normalizeJSONSchema parses a JSON schema string, normalizes it for Gemini's supported subset,
// and returns the normalized JSON string. This is the main entry point for schema normalization.
//
// The function handles the complete conversion from OpenAI's JSON Schema format to Gemini's
// supported subset, including:
// - Removing unsupported keywords (pattern, minLength, multipleOf, etc.)
// - Preserving nullable type arrays ["string", "null"]
// - Preserving additionalProperties constraints
// - Filtering format values to only date-time, date, time
// - Recursively processing all nested schemas
//
// If parsing fails, the original schema is returned unchanged.
func normalizeJSONSchema(schemaJSON string) string {
	var schema interface{}
	decoder := json.NewDecoder(strings.NewReader(schemaJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&schema); err != nil {
		return schemaJSON
	}

	normalized := normalizeSchemaForGemini(schema)

	result, err := json.Marshal(normalized)
	if err != nil {
		return schemaJSON
	}

	return string(result)
}
