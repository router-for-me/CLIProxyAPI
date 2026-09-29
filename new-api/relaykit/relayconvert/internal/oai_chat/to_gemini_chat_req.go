package oaichat

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"context"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	relaymedia "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/media"
	sharedgemini "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/shared/gemini"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
)

func OpenAIChatRequestToGeminiGenerateContent(c context.Context, textRequest dto.GeneralOpenAIRequest, info convmeta.Meta) (*dto.GeminiChatRequest, error) {
	opts := convmeta.OptionsOf(info)
	geminiRequest := dto.GeminiChatRequest{
		Contents: make([]dto.GeminiChatContent, 0, len(textRequest.Messages)),
		GenerationConfig: dto.GeminiChatGenerationConfig{
			Temperature: textRequest.Temperature,
		},
	}

	if textRequest.TopP != nil {
		geminiRequest.GenerationConfig.TopP = kitutil.GetPointer(*textRequest.TopP)
	}
	if textRequest.MaxCompletionTokens != nil {
		geminiRequest.GenerationConfig.MaxOutputTokens = kitutil.GetPointer(*textRequest.MaxCompletionTokens)
	} else if textRequest.MaxTokens != nil {
		geminiRequest.GenerationConfig.MaxOutputTokens = kitutil.GetPointer(*textRequest.MaxTokens)
	}
	if textRequest.Seed != nil {
		geminiRequest.GenerationConfig.Seed = kitutil.GetPointer(int64(*textRequest.Seed))
	}

	upstreamModelName := textRequest.Model
	if modelName := convmeta.UpstreamModelName(info); modelName != "" {
		upstreamModelName = modelName
	}

	if opts.Gemini.SupportsImagineModel(upstreamModelName) {
		geminiRequest.GenerationConfig.ResponseModalities = []string{
			"TEXT",
			"IMAGE",
		}
	}
	if stopSequences := sharedgemini.ParseStopSequences(textRequest.Stop); len(stopSequences) > 0 {
		if len(stopSequences) > 5 {
			stopSequences = stopSequences[:5]
		}
		geminiRequest.GenerationConfig.StopSequences = stopSequences
	}

	if len(textRequest.ExtraBody) > 0 {
		var extraBody map[string]any
		if err := kitutil.Unmarshal(textRequest.ExtraBody, &extraBody); err != nil {
			return nil, fmt.Errorf("invalid extra body: %w", err)
		}

		if googleBody, ok := extraBody["google"].(map[string]any); ok {
			if _, hasErrorParam := googleBody["thinkingConfig"]; hasErrorParam {
				return nil, errors.New("extra_body.google.thinkingConfig is not supported, use extra_body.google.thinking_config instead")
			}

			if thinkingConfig, ok := googleBody["thinking_config"].(map[string]any); ok {
				if _, hasErrorParam := thinkingConfig["thinkingBudget"]; hasErrorParam {
					return nil, errors.New("extra_body.google.thinking_config.thinkingBudget is not supported, use extra_body.google.thinking_config.thinking_budget instead")
				}
				var hasThinkingConfig bool
				var tempThinkingConfig dto.GeminiThinkingConfig

				if thinkingBudget, exists := thinkingConfig["thinking_budget"]; exists {
					v, ok := thinkingBudget.(float64)
					maxInt := int(^uint(0) >> 1)
					if !ok || math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v || v > float64(maxInt) || v < float64(-maxInt-1) {
						return nil, errors.New("extra_body.google.thinking_config.thinking_budget must be an integer")
					}
					budgetInt := int(v)
					tempThinkingConfig.ThinkingBudget = kitutil.GetPointer(budgetInt)
					hasThinkingConfig = true
				}

				if includeThoughts, exists := thinkingConfig["include_thoughts"]; exists {
					if v, ok := includeThoughts.(bool); ok {
						tempThinkingConfig.IncludeThoughts = kitutil.GetPointer(v)
						hasThinkingConfig = true
					} else {
						return nil, errors.New("extra_body.google.thinking_config.include_thoughts must be a boolean")
					}
				}
				if thinkingLevel, exists := thinkingConfig["thinking_level"]; exists {
					if v, ok := thinkingLevel.(string); ok {
						tempThinkingConfig.ThinkingLevel = v
						hasThinkingConfig = true
					} else {
						return nil, errors.New("extra_body.google.thinking_config.thinking_level must be a string")
					}
				}

				if hasThinkingConfig {
					geminiRequest.GenerationConfig.ThinkingConfig = &tempThinkingConfig
				}
			}

			if _, hasErrorParam := googleBody["imageConfig"]; hasErrorParam {
				return nil, errors.New("extra_body.google.imageConfig is not supported, use extra_body.google.image_config instead")
			}

			if imageConfig, ok := googleBody["image_config"].(map[string]any); ok {
				if _, hasErrorParam := imageConfig["aspectRatio"]; hasErrorParam {
					return nil, errors.New("extra_body.google.image_config.aspectRatio is not supported, use extra_body.google.image_config.aspect_ratio instead")
				}
				if _, hasErrorParam := imageConfig["imageSize"]; hasErrorParam {
					return nil, errors.New("extra_body.google.image_config.imageSize is not supported, use extra_body.google.image_config.image_size instead")
				}

				geminiImageConfig := make(map[string]any)
				if aspectRatio, ok := imageConfig["aspect_ratio"]; ok {
					geminiImageConfig["aspectRatio"] = aspectRatio
				}
				if imageSize, ok := imageConfig["image_size"]; ok {
					geminiImageConfig["imageSize"] = imageSize
				}

				if len(geminiImageConfig) > 0 {
					imageConfigBytes, err := kitutil.Marshal(geminiImageConfig)
					if err != nil {
						return nil, fmt.Errorf("failed to marshal image_config: %w", err)
					}
					geminiRequest.GenerationConfig.ImageConfig = imageConfigBytes
				}
			}
		}
	}

	if err := sharedgemini.ApplyThinkingConfig(c, &geminiRequest, info, textRequest); err != nil {
		return nil, reasoning.AsClientError(err)
	}

	var safetySettings []dto.GeminiChatSafetySettings
	for _, category := range sharedgemini.SafetySettingCategories {
		threshold := opts.Gemini.SafetySettingFor(category)
		if threshold == "" {
			continue
		}
		safetySettings = append(safetySettings, dto.GeminiChatSafetySettings{
			Category:  category,
			Threshold: threshold,
		})
	}
	if len(safetySettings) > 0 {
		geminiRequest.SafetySettings = safetySettings
	}

	if textRequest.Tools != nil {
		functions := make([]dto.FunctionRequest, 0, len(textRequest.Tools))
		for _, tool := range textRequest.Tools {
			if tool.Function.Parameters != nil {
				if params, ok := tool.Function.Parameters.(map[string]any); ok {
					if props, hasProps := params["properties"].(map[string]any); hasProps && len(props) == 0 {
						tool.Function.Parameters = nil
					}
				}
			}
			tool.Function.Parameters = sharedgemini.CleanFunctionParameters(tool.Function.Parameters)
			functions = append(functions, tool.Function)
		}
		geminiTools := geminiRequest.GetTools()
		if len(functions) > 0 {
			geminiTools = append(geminiTools, dto.GeminiChatTool{
				FunctionDeclarations: functions,
			})
		}
		geminiRequest.SetTools(geminiTools)

		if textRequest.ToolChoice != nil {
			geminiRequest.ToolConfig = sharedgemini.OpenAIToolChoiceToConfig(textRequest.ToolChoice)
		}
	}

	if textRequest.ResponseFormat != nil && (textRequest.ResponseFormat.Type == "json_schema" || textRequest.ResponseFormat.Type == "json_object") {
		geminiRequest.GenerationConfig.ResponseMimeType = "application/json"

		if len(textRequest.ResponseFormat.JsonSchema) > 0 {
			var jsonSchema dto.FormatJsonSchema
			if err := kitutil.Unmarshal(textRequest.ResponseFormat.JsonSchema, &jsonSchema); err == nil {
				cleanedSchema := sharedgemini.RemoveAdditionalProperties(jsonSchema.Schema, 0)
				geminiRequest.GenerationConfig.ResponseSchema = cleanedSchema
			}
		}
	}

	toolCallIDs := make(map[string]string)
	var systemContent []string
	for _, message := range textRequest.Messages {
		if message.Role == "system" || message.Role == "developer" {
			systemContent = append(systemContent, message.StringContent())
			continue
		}
		if message.Role == "tool" || message.Role == "function" {
			if len(geminiRequest.Contents) == 0 || geminiRequest.Contents[len(geminiRequest.Contents)-1].Role == "model" {
				geminiRequest.Contents = append(geminiRequest.Contents, dto.GeminiChatContent{
					Role: "user",
				})
			}
			parts := &geminiRequest.Contents[len(geminiRequest.Contents)-1].Parts
			name := ""
			if message.Name != nil {
				name = *message.Name
			} else if val, exists := toolCallIDs[message.ToolCallId]; exists {
				name = val
			}
			var contentMap map[string]any
			contentStr := message.StringContent()

			if err := kitutil.Unmarshal([]byte(contentStr), &contentMap); err != nil {
				var contentSlice []any
				if err := kitutil.Unmarshal([]byte(contentStr), &contentSlice); err == nil {
					contentMap = map[string]any{"result": contentSlice}
				} else {
					contentMap = map[string]any{"content": contentStr}
				}
			}

			functionResp := &dto.GeminiFunctionResponse{
				Name:     name,
				Response: contentMap,
			}
			if message.ToolCallId != "" {
				id, err := kitutil.Marshal(message.ToolCallId)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal function response ID: %w", err)
				}
				functionResp.ID = id
			}

			*parts = append(*parts, dto.GeminiPart{
				FunctionResponse: functionResp,
			})
			continue
		}

		var parts []dto.GeminiPart
		content := dto.GeminiChatContent{
			Role: message.Role,
		}
		shouldAttachThoughtSignature := (message.Role == "assistant" || message.Role == "model") && sharedgemini.ShouldAttachThoughtSignature(opts)
		signatureAttached := false
		if message.ToolCalls != nil {
			for _, call := range message.ParseToolCalls() {
				args := map[string]any{}
				if call.Function.Arguments != "" {
					if kitutil.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
						return nil, fmt.Errorf("invalid arguments for function %s, args: %s", call.Function.Name, call.Function.Arguments)
					}
				}
				toolCall := dto.GeminiPart{
					FunctionCall: &dto.FunctionCall{
						ID:           call.ID,
						FunctionName: call.Function.Name,
						Arguments:    args,
					},
				}
				if shouldAttachThoughtSignature && !signatureAttached && sharedgemini.AttachFunctionCallThoughtSignature(opts, &toolCall) {
					signatureAttached = true
				}
				parts = append(parts, toolCall)
				toolCallIDs[call.ID] = call.Function.Name
			}
		}

		openaiContent := message.ParseContent()
		for _, part := range openaiContent {
			if part.Type == dto.ContentTypeText {
				if part.Text == "" {
					continue
				}
				text := part.Text
				hasMarkdownImage := false
				for {
					startIdx := strings.Index(text, "![")
					if startIdx == -1 {
						break
					}
					bracketIdx := strings.Index(text[startIdx:], "](data:")
					if bracketIdx == -1 {
						break
					}
					bracketIdx += startIdx
					closeIdx := strings.Index(text[bracketIdx+2:], ")")
					if closeIdx == -1 {
						break
					}
					closeIdx += bracketIdx + 2

					hasMarkdownImage = true
					if startIdx > 0 {
						textBefore := text[:startIdx]
						if textBefore != "" {
							parts = append(parts, dto.GeminiPart{
								Text: textBefore,
							})
						}
					}

					dataURL := text[bracketIdx+2 : closeIdx]
					format, base64String, err := relaymedia.DecodeBase64FileData(dataURL)
					if err != nil {
						return nil, fmt.Errorf("decode markdown base64 image data failed: %s", err.Error())
					}
					imgPart := dto.GeminiPart{
						InlineData: &dto.GeminiInlineData{
							MimeType: format,
							Data:     base64String,
						},
					}
					if shouldAttachThoughtSignature {
						sharedgemini.AttachThoughtSignatureBypass(opts, &imgPart)
					}
					parts = append(parts, imgPart)
					text = text[closeIdx+1:]
				}
				if !hasMarkdownImage {
					parts = append(parts, dto.GeminiPart{
						Text: part.Text,
					})
				}
			} else {
				source := part.ToFileSource()
				if source == nil {
					continue
				}
				base64Data, mimeType, err := relaymedia.ResolveBase64Data(c, source, "formatting image for Gemini")
				if err != nil {
					return nil, fmt.Errorf("get file data from '%s' failed: %w", source.GetIdentifier(), err)
				}

				if _, ok := sharedgemini.SupportedMimeTypes[strings.ToLower(mimeType)]; !ok {
					return nil, fmt.Errorf("mime type is not supported by Gemini: '%s', url: '%s', supported types are: %v", mimeType, source.GetIdentifier(), sharedgemini.SupportedMimeTypesList())
				}

				parts = append(parts, dto.GeminiPart{
					InlineData: &dto.GeminiInlineData{
						MimeType: mimeType,
						Data:     base64Data,
					},
				})
			}
		}

		if shouldAttachThoughtSignature && !signatureAttached && len(parts) > 0 {
			sharedgemini.AttachFirstTextThoughtSignature(opts, parts)
		}

		content.Parts = parts
		if content.Role == "assistant" {
			content.Role = "model"
		}
		if len(content.Parts) > 0 {
			geminiRequest.Contents = append(geminiRequest.Contents, content)
		}
	}

	if len(systemContent) > 0 {
		geminiRequest.SystemInstructions = &dto.GeminiChatContent{
			Parts: []dto.GeminiPart{
				{
					Text: strings.Join(systemContent, "\n"),
				},
			},
		}
	}

	return &geminiRequest, nil
}
