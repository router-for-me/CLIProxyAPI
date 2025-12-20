// Package from_ir converts unified request format to provider-specific formats.
// This file handles conversion to Gemini AI Studio and Gemini CLI (Cloud Code Assist) API formats.
package from_ir

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/tidwall/gjson"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/ir"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/to_ir"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

// GeminiProvider handles conversion to Gemini AI Studio API format.
type GeminiProvider struct{}

// ConvertRequest maps UnifiedChatRequest to Gemini AI Studio API JSON format.
func (p *GeminiProvider) ConvertRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	root := map[string]interface{}{
		"contents": []interface{}{},
	}

	// 1. Messages (Contents) - Apply first so we can inspect contents later if needed
	if err := p.applyMessages(root, req); err != nil {
		return nil, err
	}

	// 2. Generation Config
	if err := p.applyGenerationConfig(root, req); err != nil {
		return nil, err
	}

	// 3. Tools
	if err := p.applyTools(root, req); err != nil {
		return nil, err
	}

	// 4. Safety Settings
	p.applySafetySettings(root, req)

	// 5. Special fix for gemini-2.5-flash-image-preview
	if req.Model == "gemini-2.5-flash-image-preview" && req.ImageConfig != nil && req.ImageConfig.AspectRatio != "" {
		p.fixImageAspectRatioForPreview(root, req.ImageConfig.AspectRatio)
	}

	return json.Marshal(root)
}

// applyGenerationConfig sets temperature, topP, topK, maxTokens, thinking, modalities, and image config.
func (p *GeminiProvider) applyGenerationConfig(root map[string]interface{}, req *ir.UnifiedChatRequest) error {
	genConfig := make(map[string]interface{})

	if req.Temperature != nil {
		genConfig["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		genConfig["topP"] = *req.TopP
	}
	if req.TopK != nil {
		genConfig["topK"] = *req.TopK
	}
	if req.MaxTokens != nil {
		genConfig["maxOutputTokens"] = *req.MaxTokens
	}

	// Thinking Config
	// Check if model is Gemini 3 family using util function for consistent detection
	isGemini3 := util.IsGemini3Model(req.Model)
	isGemini3Flash := util.IsGemini3FlashModel(req.Model)
	if util.ModelSupportsThinking(req.Model) || isGemini3 {
		if req.Thinking != nil {
			// Check if thinking is explicitly disabled (IncludeThoughts=false with Budget=0)
			// This happens when reasoning_effort="none" or thinking.type="disabled"
			if !req.Thinking.IncludeThoughts && req.Thinking.Budget == 0 && req.Thinking.Effort != "auto" {
				// Thinking disabled - don't add thinkingConfig at all
				// Skip to end of thinking config block
			} else if isGemini3 {
				// Gemini 3 uses thinkingLevel instead of thinkingBudget
				// Handle special effort values for Gemini 3
				switch req.Thinking.Effort {
				case "none":
					// "none" = disable thinking entirely, don't add thinkingConfig
					// This is different from other levels - we skip adding the config
				case "auto":
					// "auto" = enable thinking with dynamic default (no explicit level)
					genConfig["thinkingConfig"] = map[string]interface{}{
						"includeThoughts": true,
					}
				default:
					// Standard thinking levels
					tc := map[string]interface{}{
						// Always include_thoughts=true to get readable thinking text
						// Without this, Gemini only returns encrypted thoughtSignature
						"includeThoughts": true,
					}
					// Gemini 3 Pro: "low", "high" (default: "high")
					// Gemini 3 Flash: "minimal", "low", "medium", "high" (default: "high")
					switch req.Thinking.Effort {
					case "minimal":
						if isGemini3Flash {
							tc["thinkingLevel"] = "minimal"
						} else {
							tc["thinkingLevel"] = "low" // Pro doesn't have minimal, use low
						}
					case "low":
						tc["thinkingLevel"] = "low"
					case "medium":
						if isGemini3Flash {
							tc["thinkingLevel"] = "medium"
						} else {
							tc["thinkingLevel"] = "low" // Pro doesn't have medium, use low
						}
					case "high":
						tc["thinkingLevel"] = "high"
					default:
						// If effort not specified but thinking enabled, use high (API default)
						tc["thinkingLevel"] = "high"
					}
					// If budget is set but not effort, convert budget to level
					if req.Thinking.Effort == "" && req.Thinking.Budget > 0 {
						if level, ok := util.ThinkingBudgetToGemini3Level(req.Model, req.Thinking.Budget); ok {
							tc["thinkingLevel"] = level
						}
					}
					genConfig["thinkingConfig"] = tc
				}
			} else {
				// Gemini 2.5 and others use thinking_budget
				budget := req.Thinking.Budget
				if budget > 0 {
					budget = util.NormalizeThinkingBudget(req.Model, budget)
				}
				genConfig["thinkingConfig"] = map[string]interface{}{
					"thinkingBudget": budget,
					// Always include_thoughts=true to get readable thinking text
					"include_thoughts": true,
				}
			}
		} else if isGemini3 {
			// Default for Gemini 3 models - always include thoughts to get readable text
			// Don't set thinkingLevel - let API use its dynamic default ("high")
			genConfig["thinkingConfig"] = map[string]interface{}{
				"includeThoughts": true,
			}
		}
		// For Gemini 2.5 without explicit thinking config - don't send thinkingConfig at all
		// Gemini 2.5 doesn't support thinkingBudget: 0 and will error
	}

	// Response Modalities
	if len(req.ResponseModality) > 0 {
		genConfig["responseModalities"] = req.ResponseModality
	}

	// Image Config (standard)
	if req.ImageConfig != nil && req.ImageConfig.AspectRatio != "" && req.Model != "gemini-2.5-flash-image-preview" {
		imgConfig := map[string]interface{}{"aspectRatio": req.ImageConfig.AspectRatio}
		if req.ImageConfig.ImageSize != "" {
			imgConfig["imageSize"] = req.ImageConfig.ImageSize
		}
		genConfig["imageConfig"] = imgConfig
	}

	// Response Schema (Structured Output)
	// Note: Gemini API renamed responseSchema to responseJsonSchema
	if req.ResponseSchema != nil {
		genConfig["responseMimeType"] = "application/json"
		genConfig["responseJsonSchema"] = req.ResponseSchema
	}

	// Function Calling Config
	if req.FunctionCalling != nil {
		// If we have function calling config, we need to ensure toolConfig is set on root

		toolConfig := make(map[string]interface{})
		fcConfig := make(map[string]interface{})

		if req.FunctionCalling.Mode != "" {
			fcConfig["mode"] = req.FunctionCalling.Mode
		}
		if len(req.FunctionCalling.AllowedFunctionNames) > 0 {
			fcConfig["allowedFunctionNames"] = req.FunctionCalling.AllowedFunctionNames
		}
		if req.FunctionCalling.StreamFunctionCallArguments {
			fcConfig["streamFunctionCallArguments"] = true
		}

		if len(fcConfig) > 0 {
			toolConfig["functionCallingConfig"] = fcConfig
			root["toolConfig"] = toolConfig
		}
	}

	if len(genConfig) > 0 {
		root["generationConfig"] = genConfig
	}
	return nil
}

// applyMessages converts messages to Gemini contents format.
func (p *GeminiProvider) applyMessages(root map[string]interface{}, req *ir.UnifiedChatRequest) error {
	var contents []interface{}
	toolCallIDToName := ir.BuildToolCallMap(req.Messages)
	toolResults := ir.BuildToolResultsMap(req.Messages)

	for _, msg := range req.Messages {
		switch msg.Role {
		case ir.RoleSystem:
			// System message → systemInstruction
			var textContent string
			for _, part := range msg.Content {
				if part.Type == ir.ContentTypeText {
					textContent = part.Text
					break
				}
			}
			if textContent != "" {
				root["systemInstruction"] = map[string]interface{}{
					"role": "user",
					"parts": []interface{}{
						map[string]interface{}{"text": textContent},
					},
				}
			}

		case ir.RoleUser:
			var parts []interface{}
			for _, part := range msg.Content {
				switch part.Type {
				case ir.ContentTypeText:
					parts = append(parts, map[string]interface{}{"text": part.Text})
				case ir.ContentTypeImage:
					if part.Image != nil {
						parts = append(parts, map[string]interface{}{
							"inlineData": map[string]interface{}{
								"mimeType": part.Image.MimeType,
								"data":     part.Image.Data,
							},
						})
					}
				}
			}
			if len(parts) > 0 {
				contents = append(contents, map[string]interface{}{
					"role":  "user",
					"parts": parts,
				})
			}

		case ir.RoleAssistant:
			if len(msg.ToolCalls) > 0 {
				// Assistant message with tool calls
				var parts []interface{}
				var toolCallIDs []string

				for i, tc := range msg.ToolCalls {
					argsJSON := ir.ValidateAndNormalizeJSON(tc.Args)
					fcMap := map[string]interface{}{
						"name": tc.Name,
						"args": json.RawMessage(argsJSON),
					}
					// Generate tool call ID if missing
					// Claude API requires tool_use.id field, so we must always have one
					toolID := tc.ID
					if toolID == "" {
						// Generate a unique ID for this tool call
						toolID = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), i)
					}
					fcMap["id"] = toolID

					part := map[string]interface{}{
						"functionCall": fcMap,
					}
					if tc.ThoughtSignature != "" {
						part["thoughtSignature"] = tc.ThoughtSignature
					} else if i == 0 {
						// Fallback for missing signature (e.g. from non-thinking model or lost history)
						// Only apply to the first tool call in a parallel sequence, as subsequent ones
						// shouldn't have it if the first one does (or if we are faking it).
						part["thoughtSignature"] = "skip_thought_signature_validator"
					}
					parts = append(parts, part)
					toolCallIDs = append(toolCallIDs, toolID)
				}

				contents = append(contents, map[string]interface{}{
					"role":  "model",
					"parts": parts,
				})

				// Add corresponding tool responses
				var responseParts []interface{}
				for _, tcID := range toolCallIDs {
					name, ok := toolCallIDToName[tcID]
					if !ok {
						continue
					}
					resultPart, hasResult := toolResults[tcID]
					if !hasResult {
						continue
					}

					// Construct functionResponse
					funcResp := map[string]interface{}{
						"name": name,
						"id":   tcID, // Include ID for Claude models in Antigravity
					}

					// Check for multimodal content (Gemini 3+)
					if len(resultPart.Images) > 0 || len(resultPart.Files) > 0 {
						// Multimodal function response
						var responseObj interface{}
						if parsed := gjson.Parse(resultPart.Result); parsed.Type == gjson.JSON {
							var jsonObj interface{}
							if err := json.Unmarshal([]byte(resultPart.Result), &jsonObj); err == nil {
								responseObj = jsonObj
							} else {
								responseObj = map[string]interface{}{"content": resultPart.Result}
							}
						} else {
							responseObj = map[string]interface{}{"content": resultPart.Result}
						}
						funcResp["response"] = responseObj

						var nestedParts []interface{}
						for _, img := range resultPart.Images {
							nestedParts = append(nestedParts, map[string]interface{}{
								"inlineData": map[string]interface{}{
									"mimeType": img.MimeType,
									"data":     img.Data,
								},
							})
						}
						for _, f := range resultPart.Files {
							nestedParts = append(nestedParts, map[string]interface{}{
								"inlineData": map[string]interface{}{ // Use inlineData for small files or fileData for GCS?
									// The doc says "Each multimodal part must contain inlineData or fileData."
									// If we have base64 data, use inlineData.
									"mimeType": "application/pdf", // Default or detect? FilePart doesn't have MimeType?
									"data":     f.FileData,
								},
							})
						}

						if len(nestedParts) > 0 {
						}
					} else {
						// Standard response
						var responseObj interface{}
						if parsed := gjson.Parse(resultPart.Result); parsed.Type == gjson.JSON {
							var jsonObj interface{}
							if err := json.Unmarshal([]byte(resultPart.Result), &jsonObj); err == nil {
								responseObj = jsonObj
							} else {
								responseObj = map[string]interface{}{"content": resultPart.Result}
							}
						} else {
							responseObj = map[string]interface{}{"content": resultPart.Result}
						}
						funcResp["response"] = responseObj
					}

					responseParts = append(responseParts, map[string]interface{}{
						"functionResponse": funcResp,
					})
				}

				if len(responseParts) > 0 {
					contents = append(contents, map[string]interface{}{
						"role":  "user",
						"parts": responseParts,
					})
				}
			} else {
				// Assistant text message (with optional reasoning)
				var parts []interface{}
				for _, part := range msg.Content {
					switch part.Type {
					case ir.ContentTypeReasoning:
						p := map[string]interface{}{
							"text":    part.Reasoning,
							"thought": true,
						}
						if part.ThoughtSignature != "" {
							p["thoughtSignature"] = part.ThoughtSignature
						}
						parts = append(parts, p)
					case ir.ContentTypeText:
						p := map[string]interface{}{"text": part.Text}
						if part.ThoughtSignature != "" {
							p["thoughtSignature"] = part.ThoughtSignature
						}
						parts = append(parts, p)
					}
				}

				// Combine remaining text parts if any (legacy behavior, but we iterate parts above now)

				if len(parts) > 0 {
					contents = append(contents, map[string]interface{}{
						"role":  "model",
						"parts": parts,
					})
				}
			}
		}
	}

	if len(contents) > 0 {
		root["contents"] = contents
	}
	return nil
}

// applyTools converts tool definitions to Gemini functionDeclarations format.
func (p *GeminiProvider) applyTools(root map[string]interface{}, req *ir.UnifiedChatRequest) error {
	var googleSearch interface{}
	if req.Metadata != nil {
		if gs, ok := req.Metadata["google_search"]; ok {
			googleSearch = gs
		}
	}

	if len(req.Tools) == 0 && googleSearch == nil {
		return nil
	}

	toolNode := make(map[string]interface{})

	if len(req.Tools) > 0 {
		funcs := make([]interface{}, len(req.Tools))
		for i, t := range req.Tools {
			funcDecl := map[string]interface{}{
				"name":        t.Name,
				"description": t.Description,
			}
			if len(t.Parameters) == 0 {
				funcDecl["parametersJsonSchema"] = map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				}
			} else {
				funcDecl["parametersJsonSchema"] = ir.CleanJsonSchema(copyMap(t.Parameters))
			}
			funcs[i] = funcDecl
		}
		toolNode["functionDeclarations"] = funcs
	}

	if googleSearch != nil {
		toolNode["googleSearch"] = googleSearch
	}

	root["tools"] = []interface{}{toolNode}

	// Set toolConfig.functionCallingConfig.mode based on ToolChoice from request.
	// - "none" -> NONE (don't call functions)
	// - "required" or "any" -> ANY (must call a function)
	// - "auto" or empty -> AUTO (model decides)
	// Note: We default to AUTO, not ANY, because ANY forces the model to always
	// call a function even when inappropriate (e.g., user says "hello").
	if len(req.Tools) > 0 {
		mode := "AUTO" // Default: let model decide
		switch req.ToolChoice {
		case "none":
			mode = "NONE"
		case "required", "any":
			mode = "ANY"
		case "auto", "":
			mode = "AUTO"
		}
		root["toolConfig"] = map[string]interface{}{
			"functionCallingConfig": map[string]interface{}{
				"mode": mode,
			},
		}
	}

	return nil
}

// applySafetySettings sets safety settings or applies defaults.
func (p *GeminiProvider) applySafetySettings(root map[string]interface{}, req *ir.UnifiedChatRequest) {
	if len(req.SafetySettings) > 0 {
		settings := make([]interface{}, len(req.SafetySettings))
		for i, s := range req.SafetySettings {
			settings[i] = map[string]interface{}{
				"category":  s.Category,
				"threshold": s.Threshold,
			}
		}
		root["safetySettings"] = settings
	} else {
		// Default settings
		root["safetySettings"] = ir.DefaultGeminiSafetySettings()
	}
}

// fixImageAspectRatioForPreview handles gemini-2.5-flash-image-preview requirements.
func (p *GeminiProvider) fixImageAspectRatioForPreview(root map[string]interface{}, aspectRatio string) {
	contents, ok := root["contents"].([]interface{})
	if !ok || len(contents) == 0 {
		return
	}

	// Check if there's already an image
	hasInlineData := false
	for _, content := range contents {
		if cMap, ok := content.(map[string]interface{}); ok {
			if parts, ok := cMap["parts"].([]interface{}); ok {
				for _, part := range parts {
					if pMap, ok := part.(map[string]interface{}); ok {
						if _, exists := pMap["inlineData"]; exists {
							hasInlineData = true
							break
						}
					}
				}
			}
		}
		if hasInlineData {
			break
		}
	}

	if hasInlineData {
		return
	}

	// Inject white image placeholder
	emptyImageBase64, err := util.CreateWhiteImageBase64(aspectRatio)
	if err != nil {
		return
	}

	// Create new parts for the first content message
	firstContent := contents[0].(map[string]interface{})
	existingParts := firstContent["parts"].([]interface{})

	newParts := []interface{}{
		map[string]interface{}{
			"text": "Based on the following requirements, create an image within the uploaded picture. The new content *MUST* completely cover the entire area of the original picture, maintaining its exact proportions, and *NO* blank areas should appear.",
		},
		map[string]interface{}{
			"inlineData": map[string]interface{}{
				"mime_type": "image/png",
				"data":      emptyImageBase64,
			},
		},
	}
	newParts = append(newParts, existingParts...)
	firstContent["parts"] = newParts

	// Update generation config
	if genConfig, ok := root["generationConfig"].(map[string]interface{}); ok {
		genConfig["responseModalities"] = []string{"IMAGE", "TEXT"}
		delete(genConfig, "imageConfig")
	} else {
		root["generationConfig"] = map[string]interface{}{
			"responseModalities": []string{"IMAGE", "TEXT"},
		}
	}
}

// --- Response Conversion ---

// ToGeminiResponse converts messages to a complete Gemini API response.
func ToGeminiResponse(messages []ir.Message, usage *ir.Usage, model string) ([]byte, error) {
	builder := ir.NewResponseBuilder(messages, usage, model)

	response := map[string]interface{}{
		"candidates":   []interface{}{},
		"modelVersion": model,
	}

	if builder.HasContent() {
		response["candidates"] = []interface{}{
			map[string]interface{}{
				"content": map[string]interface{}{
					"role":  "model",
					"parts": builder.BuildGeminiContentParts(),
				},
				"finishReason": "STOP",
			},
		}
	}

	if usage != nil {
		response["usageMetadata"] = map[string]interface{}{
			"promptTokenCount":     usage.PromptTokens,
			"candidatesTokenCount": usage.CompletionTokens,
			"totalTokenCount":      usage.TotalTokens,
		}
	}

	return json.Marshal(response)
}

// ToGeminiChunk converts a single event to Gemini streaming chunk.
func ToGeminiChunk(event ir.UnifiedEvent, model string) ([]byte, error) {
	chunk := map[string]interface{}{
		"candidates":   []interface{}{},
		"modelVersion": model,
	}

	candidate := map[string]interface{}{
		"content": map[string]interface{}{
			"role":  "model",
			"parts": []interface{}{},
		},
	}

	switch event.Type {
	case ir.EventTypeToken:
		candidate["content"].(map[string]interface{})["parts"] = []interface{}{
			map[string]interface{}{"text": event.Content},
		}

	case ir.EventTypeReasoning:
		candidate["content"].(map[string]interface{})["parts"] = []interface{}{
			map[string]interface{}{"text": event.Reasoning, "thought": true},
		}

	case ir.EventTypeToolCall:
		if event.ToolCall != nil {
			var argsObj interface{} = map[string]interface{}{}
			if event.ToolCall.Args != "" && event.ToolCall.Args != "{}" {
				if err := json.Unmarshal([]byte(event.ToolCall.Args), &argsObj); err != nil {
					argsObj = map[string]interface{}{}
				}
			}
			candidate["content"].(map[string]interface{})["parts"] = []interface{}{
				map[string]interface{}{
					"functionCall": map[string]interface{}{
						"name": event.ToolCall.Name,
						"args": argsObj,
					},
				},
			}
		}

	case ir.EventTypeImage:
		if event.Image != nil {
			candidate["content"].(map[string]interface{})["parts"] = []interface{}{
				map[string]interface{}{
					"inlineData": map[string]interface{}{
						"mimeType": event.Image.MimeType,
						"data":     event.Image.Data,
					},
				},
			}
		}

	case ir.EventTypeFinish:
		candidate["finishReason"] = "STOP"
		if event.Usage != nil {
			chunk["usageMetadata"] = map[string]interface{}{
				"promptTokenCount":     event.Usage.PromptTokens,
				"candidatesTokenCount": event.Usage.CompletionTokens,
				"totalTokenCount":      event.Usage.TotalTokens,
			}
		}

	case ir.EventTypeError:
		return nil, fmt.Errorf("stream error: %v", event.Error)

	default:
		return nil, nil
	}

	chunk["candidates"] = []interface{}{candidate}

	jsonBytes, err := json.Marshal(chunk)
	if err != nil {
		return nil, err
	}

	// Gemini uses newline-delimited JSON (not SSE format)
	return append(jsonBytes, '\n'), nil
}

// --- Gemini CLI Provider ---

// GeminiCLIProvider handles conversion to Gemini CLI format.
// CLI format wraps AI Studio format: {"project":"", "model":"", "request":{...}}
type GeminiCLIProvider struct{}

// ConvertRequest converts UnifiedChatRequest to Gemini CLI JSON format.
func (p *GeminiCLIProvider) ConvertRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	// Build core Gemini AI Studio request
	geminiJSON, err := (&GeminiProvider{}).ConvertRequest(req)
	if err != nil {
		return nil, err
	}

	// Wrap in CLI envelope: {"project":"", "model":"...", "request":{...}}
	envelope := map[string]interface{}{
		"project": "",
		"model":   "",
		"request": json.RawMessage(geminiJSON),
	}
	if req.Model != "" {
		envelope["model"] = req.Model
	}

	return json.Marshal(envelope)
}

// ParseResponse parses a non-streaming Gemini CLI response into unified format.
// Delegates to to_ir package as the logic is identical to Gemini AI Studio response parsing.
func (p *GeminiCLIProvider) ParseResponse(responseJSON []byte) ([]ir.Message, *ir.Usage, error) {
	_, messages, usage, err := to_ir.ParseGeminiResponse(responseJSON)
	return messages, usage, err
}

// ParseStreamChunk parses a streaming Gemini CLI chunk into events.
// Delegates to to_ir package as the logic is identical to Gemini AI Studio chunk parsing.
func (p *GeminiCLIProvider) ParseStreamChunk(chunkJSON []byte) ([]ir.UnifiedEvent, error) {
	return to_ir.ParseGeminiChunk(chunkJSON)
}

// ParseStreamChunkWithContext parses a streaming Gemini CLI chunk with schema context.
// The schemaCtx parameter allows normalizing tool call parameters based on the original request schema.
func (p *GeminiCLIProvider) ParseStreamChunkWithContext(chunkJSON []byte, schemaCtx *ir.ToolSchemaContext) ([]ir.UnifiedEvent, error) {
	return to_ir.ParseGeminiChunkWithContext(chunkJSON, schemaCtx)
}
