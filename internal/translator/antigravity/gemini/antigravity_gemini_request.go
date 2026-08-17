// Package gemini provides request translation functionality for Antigravity to Gemini API compatibility.
// It handles parsing and transforming Antigravity API requests into Gemini API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Antigravity API format and Gemini API's expected format.
package gemini

import (
	"bytes"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertAntigravityRequestToGemini converts an Antigravity API request to Gemini API format.
// It performs various transformations including system instruction mapping, role normalization,
// tool declaration conversion, and safety settings handling.
//
// Parameters:
//   - modelName: The name of the model being used for the request
//   - inputRawJSON: The raw JSON request from the Antigravity API
//   - stream: A boolean indicating if the request is for a streaming response
//
// Returns:
//   - []byte: The transformed request data in Gemini API format
func ConvertAntigravityRequestToGemini(modelName string, inputRawJSON []byte, stream bool) []byte {
	rawJSON := inputRawJSON
	functionNameMap := util.SanitizedFunctionNameMap(rawJSON)
	// Base envelope
	out := []byte(`{}`)

	// If model exists, set it
	if modelName != "" {
		out, _ = sjson.SetBytes(out, "model", modelName)
	}

	if genConfig := util.GetGJSONBytesNoCopy(rawJSON, "request.generationConfig"); genConfig.Exists() {
		out, _ = sjson.SetRawBytes(out, "generationConfig", []byte(genConfig.Raw))
	} else if genConfig := util.GetGJSONBytesNoCopy(rawJSON, "request.generation_config"); genConfig.Exists() {
		out, _ = sjson.SetRawBytes(out, "generationConfig", []byte(genConfig.Raw))
	}

	out = applyAntigravityThinkingCompatibilityToGemini(out, rawJSON)

	// Clean JSON Schema in responseSchema if it exists
	if responseSchema := util.GetGJSONBytesNoCopy(out, "generationConfig.responseSchema"); responseSchema.Exists() {
		cleanedSchema := util.CleanJSONSchemaForAntigravityResponse(responseSchema.Raw)
		out, _ = sjson.SetRawBytes(out, "generationConfig.responseSchema", []byte(cleanedSchema))
	}

	fixedJSON, errFixCLIToolResponse := fixCLIToolResponse(rawJSON)
	if errFixCLIToolResponse != nil {
		return []byte{}
	}
	rawJSON = fixedJSON

	if systemInstructionResult := util.GetGJSONBytesNoCopy(rawJSON, "request.system_instruction"); systemInstructionResult.Exists() {
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "request.systemInstruction", []byte(systemInstructionResult.Raw))
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "request.system_instruction")
	}

	// Normalize roles in request.contents: default to valid values if missing/invalid.
	// The contents array is only materialized when a role actually changes; copying
	// every content up front duplicates the whole payload for large inline data.
	contents := util.GetGJSONBytesNoCopy(rawJSON, "request.contents")
	if contents.IsArray() && geminiContentRolesNeedNormalization(contents) {
		contentItems := translatorcommon.NewRawArrayItems(contents.Get("#").Int())
		previousRole := ""
		contents.ForEach(func(_, value gjson.Result) bool {
			role := value.Get("role").String()
			content := []byte(value.Raw)
			if role != "user" && role != "model" {
				if previousRole == "" || previousRole == "model" {
					role = "user"
				} else {
					role = "model"
				}
				content, _ = sjson.SetBytes(content, "role", role)
			}
			previousRole = role
			contentItems = append(contentItems, content)
			return true
		})
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "request.contents", translatorcommon.JoinRawArray(contentItems))
	}

	toolsResult := util.GetGJSONBytesNoCopy(rawJSON, "request.tools")
	if toolsResult.IsArray() {
		seenFunctionNames := make(map[string]struct{})
		toolsChanged := false
		var toolItems [][]byte
		toolsResult.ForEach(func(toolIndex, tool gjson.Result) bool {
			toolJSON := []byte(tool.Raw)
			toolChanged := false
			for _, key := range []string{"functionDeclarations", "function_declarations"} {
				declarations := tool.Get(key)
				if !declarations.IsArray() {
					continue
				}

				declarationsChanged := false
				var declarationItems [][]byte
				declarations.ForEach(func(_, declaration gjson.Result) bool {
					nameResult := declaration.Get("name")
					originalName := nameResult.String()
					mappedName := util.MapSanitizedFunctionName(functionNameMap, originalName)
					if strings.Contains(strings.ToLower(modelName), "claude") {
						mappedName = util.SanitizeClaudeFunctionName(originalName)
					}
					if mappedName != "" {
						if _, exists := seenFunctionNames[mappedName]; exists {
							declarationsChanged = true
							return true
						}
						seenFunctionNames[mappedName] = struct{}{}
					}

					declarationJSON := []byte(declaration.Raw)
					if nameResult.Type != gjson.String || mappedName != originalName {
						declarationJSON, _ = sjson.SetBytes(declarationJSON, "name", mappedName)
						declarationsChanged = true
					}
					if parameters := declaration.Get("parameters"); parameters.Exists() && parameters.Raw != "" && parameters.Raw != "null" && parameters.Raw != "{}" {
						declarationJSON, _ = sjson.SetRawBytes(declarationJSON, "parametersJsonSchema", []byte(parameters.Raw))
						declarationJSON, _ = sjson.DeleteBytes(declarationJSON, "parameters")
						declarationsChanged = true
					} else if pjs := declaration.Get("parametersJsonSchema"); !pjs.Exists() || pjs.Raw == "" || pjs.Raw == "{}" || pjs.Raw == "null" {
						declarationJSON, _ = sjson.SetRawBytes(declarationJSON, "parametersJsonSchema", []byte(`{"type":"object","properties":{}}`))
						declarationsChanged = true
					}
					declarationItems = append(declarationItems, declarationJSON)
					return true
				})
				if declarationsChanged {
					var errSet error
					toolJSON, errSet = sjson.SetRawBytes(toolJSON, key, translatorcommon.JoinRawArray(declarationItems))
					if errSet != nil {
						log.Warnf("failed to normalize function declarations in tool %d: %v", toolIndex.Int(), errSet)
					} else {
						toolChanged = true
					}
				}
			}
			toolsChanged = toolsChanged || toolChanged
			toolItems = append(toolItems, toolJSON)
			return true
		})
		if toolsChanged {
			rawJSON, _ = sjson.SetRawBytes(rawJSON, "request.tools", translatorcommon.JoinRawArray(toolItems))
		}
		rawJSON = removeEmptyGeminiFunctionTools(rawJSON)
	}
	rawJSON = rewriteGeminiFunctionNames(rawJSON, functionNameMap)

	if strings.Contains(strings.ToLower(modelName), "claude") {
		rawJSON = SanitizeAntigravityClaudeGeminiRequestSignatures(modelName, rawJSON)
	}

	// Copy all fields from request to root
	if request := util.GetGJSONBytesNoCopy(rawJSON, "request"); request.Exists() && request.IsObject() {
		request.ForEach(func(key, value gjson.Result) bool {
			// Skip generationConfig as we already handled it
			if key.String() == "generationConfig" || key.String() == "generation_config" {
				return true
			}
			out, _ = sjson.SetRawBytes(out, key.String(), []byte(value.Raw))
			return true
		})
	}

	// System instruction handling
	if systemInstructionResult := util.GetGJSONBytesNoCopy(rawJSON, "systemInstruction"); systemInstructionResult.Exists() {
		out, _ = sjson.SetRawBytes(out, "systemInstruction", []byte(systemInstructionResult.Raw))
	} else if systemInstructionResult := util.GetGJSONBytesNoCopy(rawJSON, "system_instruction"); systemInstructionResult.Exists() {
		out, _ = sjson.SetRawBytes(out, "systemInstruction", []byte(systemInstructionResult.Raw))
	}

	// If no system instruction is provided, use default
	if systemInstruction := util.GetGJSONBytesNoCopy(out, "systemInstruction"); !systemInstruction.Exists() {
		defaultSystemInstruction := []byte(`{"parts":[{"text":""}],"role":"user"}`)
		out, _ = sjson.SetRawBytes(out, "systemInstruction", defaultSystemInstruction)
	}

	// Safety settings handling
	safetySettingsResult := util.GetGJSONBytesNoCopy(out, "safetySettings")
	if !safetySettingsResult.Exists() {
		safetySettingsResult = util.GetGJSONBytesNoCopy(out, "safety_settings")
	}

	if safetySettingsResult.Exists() && safetySettingsResult.IsArray() {
		// Use provided safety settings, but replace HARM_BLOCK_THRESHOLD_UNSPECIFIED with BLOCK_NONE
		safetySettings := []byte(safetySettingsResult.Raw)
		safetySettings = bytes.ReplaceAll(safetySettings, []byte("HARM_BLOCK_THRESHOLD_UNSPECIFIED"), []byte("BLOCK_NONE"))
		out, _ = sjson.SetRawBytes(out, "safetySettings", safetySettings)
	} else {
		// Default safety settings to BLOCK_NONE
		defaultSafetySettings := []byte(`[
			{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "BLOCK_NONE"},
			{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "BLOCK_NONE"},
			{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_NONE"},
			{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "BLOCK_NONE"},
			{"category": "HARM_CATEGORY_CIVIC_INTEGRITY", "threshold": "BLOCK_NONE"}
		]`)
		out, _ = sjson.SetRawBytes(out, "safetySettings", defaultSafetySettings)
	}

	// Realtime search is not supported in streaming mode
	// If streaming and search tool exists, remove it
	if stream {
		if toolsResult := util.GetGJSONBytesNoCopy(out, "tools"); toolsResult.Exists() && toolsResult.IsArray() {
			var newTools [][]byte
			toolsResult.ForEach(func(_, tool gjson.Result) bool {
				toolJSON := []byte(tool.Raw)
				if googleSearch := tool.Get("googleSearch"); googleSearch.Exists() {
					toolJSON, _ = sjson.DeleteBytes(toolJSON, "googleSearch")
				}
				if googleSearch := tool.Get("google_search"); googleSearch.Exists() {
					toolJSON, _ = sjson.DeleteBytes(toolJSON, "google_search")
				}
				// Only keep the tool object if it still contains valid properties
				if len(gjson.ParseBytes(toolJSON).Map()) > 0 {
					newTools = append(newTools, toolJSON)
				}
				return true
			})
			if len(newTools) > 0 {
				out, _ = sjson.SetRawBytes(out, "tools", translatorcommon.JoinRawArray(newTools))
			} else {
				out, _ = sjson.DeleteBytes(out, "tools")
			}
		}
	}

	// Claude models on Antigravity use standard JSON Schema (additionalProperties=false, etc.)
	// Clean the tool schemas to ensure compatibility
	isClaude := strings.Contains(strings.ToLower(modelName), "claude")
	if isClaude {
		if toolsResult := util.GetGJSONBytesNoCopy(out, "tools"); toolsResult.Exists() && toolsResult.IsArray() {
			var cleanedTools [][]byte
			toolsResult.ForEach(func(_, tool gjson.Result) bool {
				toolJSON := []byte(tool.Raw)
				for _, key := range []string{"functionDeclarations", "function_declarations"} {
					if decls := tool.Get(key); decls.Exists() && decls.IsArray() {
						var cleanedDecls [][]byte
						decls.ForEach(func(_, decl gjson.Result) bool {
							declJSON := []byte(decl.Raw)
							if schema := decl.Get("parametersJsonSchema"); schema.Exists() {
								cleaned := util.CleanJSONSchemaForAntigravity(schema.Raw)
								declJSON, _ = sjson.SetRawBytes(declJSON, "parametersJsonSchema", []byte(cleaned))
							}
							cleanedDecls = append(cleanedDecls, declJSON)
							return true
						})
						toolJSON, _ = sjson.SetRawBytes(toolJSON, key, translatorcommon.JoinRawArray(cleanedDecls))
					}
				}
				cleanedTools = append(cleanedTools, toolJSON)
				return true
			})
			out, _ = sjson.SetRawBytes(out, "tools", translatorcommon.JoinRawArray(cleanedTools))
		}
	}

	// Tool config mapping from request.toolConfig / request.tool_config
	if toolConfig := util.GetGJSONBytesNoCopy(rawJSON, "request.toolConfig"); toolConfig.Exists() {
		out, _ = sjson.SetRawBytes(out, "toolConfig", []byte(toolConfig.Raw))
	} else if toolConfig := util.GetGJSONBytesNoCopy(rawJSON, "request.tool_config"); toolConfig.Exists() {
		out, _ = sjson.SetRawBytes(out, "toolConfig", []byte(toolConfig.Raw))
	}

	// If toolConfig.functionCallingConfig exists, ensure mode is uppercase
	if mode := util.GetGJSONBytesNoCopy(out, "toolConfig.functionCallingConfig.mode"); mode.Exists() {
		out, _ = sjson.SetBytes(out, "toolConfig.functionCallingConfig.mode", strings.ToUpper(mode.String()))
	} else if mode := util.GetGJSONBytesNoCopy(out, "toolConfig.function_calling_config.mode"); mode.Exists() {
		out, _ = sjson.SetBytes(out, "toolConfig.functionCallingConfig.mode", strings.ToUpper(mode.String()))
		out, _ = sjson.DeleteBytes(out, "toolConfig.function_calling_config")
	}

	// Stream setting configuration
	out, _ = sjson.SetBytes(out, "stream", stream)

	return out
}

func fixCLIToolResponse(rawJSON []byte) ([]byte, error) {
	contents := util.GetGJSONBytesNoCopy(rawJSON, "request.contents")
	if !contents.Exists() || !contents.IsArray() {
		return rawJSON, nil
	}

	var newContents [][]byte
	var contentsModified bool

	contents.ForEach(func(_, content gjson.Result) bool {
		role := content.Get("role").String()
		parts := content.Get("parts")

		if role == "user" && parts.Exists() && parts.IsArray() {
			var newParts [][]byte
			var partsModified bool

			parts.ForEach(func(_, part gjson.Result) bool {
				functionResponse := part.Get("functionResponse")
				if functionResponse.Exists() && functionResponse.IsObject() {
					response := functionResponse.Get("response")
					output := response.Get("output")

					// Check if response contains only output and it is a string
					if output.Exists() && output.Type == gjson.String && len(response.Map()) == 1 {
						outputStr := output.String()
						// Try to parse output as JSON
						if gjson.Valid(outputStr) {
							parsedOutput := gjson.Parse(outputStr)
							if parsedOutput.IsObject() {
								// Replace output with parsed JSON object
								newPart, err := sjson.SetRawBytes([]byte(part.Raw), "functionResponse.response.output", []byte(parsedOutput.Raw))
								if err == nil {
									newParts = append(newParts, newPart)
									partsModified = true
									return true
								}
							}
						}
					}
				}
				newParts = append(newParts, []byte(part.Raw))
				return true
			})

			if partsModified {
				newContent, err := sjson.SetRawBytes([]byte(content.Raw), "parts", translatorcommon.JoinRawArray(newParts))
				if err == nil {
					newContents = append(newContents, newContent)
					contentsModified = true
					return true
				}
			}
		}

		newContents = append(newContents, []byte(content.Raw))
		return true
	})

	if contentsModified {
		return sjson.SetRawBytes(rawJSON, "request.contents", translatorcommon.JoinRawArray(newContents))
	}

	return rawJSON, nil
}

func geminiContentRolesNeedNormalization(contents gjson.Result) bool {
	var previousRole string
	var needsNormalization bool

	contents.ForEach(func(_, value gjson.Result) bool {
		role := value.Get("role").String()
		if role != "user" && role != "model" {
			needsNormalization = true
			return false
		}
		if role == previousRole {
			needsNormalization = true
			return false
		}
		previousRole = role
		return true
	})

	return needsNormalization
}

func applyAntigravityThinkingCompatibilityToGemini(out, rawJSON []byte) []byte {
	config := thinking.ExtractSummaryConfig(rawJSON, "antigravity")
	if config.SummaryMode != "" {
		return thinking.ApplySummaryConfig(out, "gemini", config)
	}

	modelName := gjson.GetBytes(rawJSON, "model").String()
	mi := registry.LookupModelInfo(modelName, "gemini")
	if mi != nil && mi.Thinking != nil && mi.Thinking.DefaultLevel != "" {
		thinkingLevel := string(mi.Thinking.DefaultLevel)
		thinkingBudget := thinking.ConvertLevelToBudgetOnly(thinkingLevel)
		if thinkingBudget != 0 {
			out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.thinkingBudget", thinkingBudget)
		}
	}

	return out
}
