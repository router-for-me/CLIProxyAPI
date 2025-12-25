// Package from_ir converts unified request format to provider-specific formats.
// This file handles conversion to Gemini AI Studio and Gemini CLI (Cloud Code Assist) API formats.
package from_ir

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/tidwall/gjson"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"
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

	if err := p.applyMessages(root, req); err != nil {
		return nil, err
	}

	if err := p.applyGenerationConfig(root, req); err != nil {
		return nil, err
	}

	if err := p.applyTools(root, req); err != nil {
		return nil, err
	}

	p.applySafetySettings(root, req)

	if req.Model == "gemini-2.5-flash-image-preview" && req.ImageConfig != nil && req.ImageConfig.AspectRatio != "" {
		p.fixImageAspectRatioForPreview(root, req.ImageConfig.AspectRatio)
	}

	return json.Marshal(root)
}

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

	if util.ModelSupportsThinking(req.Model) || util.IsGemini3Model(req.Model) {
		p.applyThinkingConfig(genConfig, req)
	}

	if len(req.ResponseModality) > 0 {
		genConfig["responseModalities"] = req.ResponseModality
	}

	if req.ImageConfig != nil && req.ImageConfig.AspectRatio != "" && req.Model != "gemini-2.5-flash-image-preview" {
		imgConfig := map[string]interface{}{"aspectRatio": req.ImageConfig.AspectRatio}
		if req.ImageConfig.ImageSize != "" {
			imgConfig["imageSize"] = req.ImageConfig.ImageSize
		}
		genConfig["imageConfig"] = imgConfig
	}

	if req.ResponseSchema != nil {
		genConfig["responseMimeType"] = "application/json"
		genConfig["responseJsonSchema"] = req.ResponseSchema
	}

	if req.FunctionCalling != nil {
		p.applyFunctionCallingConfig(root, req.FunctionCalling)
	}

	if len(genConfig) > 0 {
		root["generationConfig"] = genConfig
	}
	return nil
}

func (p *GeminiProvider) applyThinkingConfig(genConfig map[string]interface{}, req *ir.UnifiedChatRequest) {
	if req.Thinking == nil {
		if util.IsGemini3Model(req.Model) {
			genConfig["thinkingConfig"] = map[string]interface{}{"includeThoughts": true}
		}
		return
	}

	// Thinking disabled
	if !req.Thinking.IncludeThoughts && req.Thinking.Budget == 0 && req.Thinking.Effort != "auto" {
		return
	}

	isGemini3 := util.IsGemini3Model(req.Model)
	if isGemini3 {
		tc := map[string]interface{}{"includeThoughts": true}
		if req.Thinking.Effort == "none" {
			return
		}
		if req.Thinking.Effort != "auto" {
			p.applyGemini3ThinkingLevel(tc, req)
		}
		genConfig["thinkingConfig"] = tc
	} else {
		// Gemini 2.5
		budget := req.Thinking.Budget
		if budget > 0 {
			budget = util.NormalizeThinkingBudget(req.Model, budget)
		}
		genConfig["thinkingConfig"] = map[string]interface{}{
			"thinkingBudget":   budget,
			"include_thoughts": true,
		}
	}
}

func (p *GeminiProvider) applyGemini3ThinkingLevel(tc map[string]interface{}, req *ir.UnifiedChatRequest) {
	isGemini3Flash := util.IsGemini3FlashModel(req.Model)
	level := "high" // Default

	switch req.Thinking.Effort {
	case "minimal":
		if isGemini3Flash {
			level = "minimal"
		} else {
			level = "low"
		}
	case "low":
		level = "low"
	case "medium":
		if isGemini3Flash {
			level = "medium"
		} else {
			level = "low"
		}
	case "high":
		level = "high"
	}

	if req.Thinking.Effort == "" && req.Thinking.Budget > 0 {
		if l, ok := util.ThinkingBudgetToGemini3Level(req.Model, req.Thinking.Budget); ok {
			level = l
		}
	}
	tc["thinkingLevel"] = level
}

func (p *GeminiProvider) applyFunctionCallingConfig(root map[string]interface{}, fc *ir.FunctionCallingConfig) {
	toolConfig := make(map[string]interface{})
	fcConfig := make(map[string]interface{})

	if fc.Mode != "" {
		fcConfig["mode"] = fc.Mode
	}
	if len(fc.AllowedFunctionNames) > 0 {
		fcConfig["allowedFunctionNames"] = fc.AllowedFunctionNames
	}
	if fc.StreamFunctionCallArguments {
		fcConfig["streamFunctionCallArguments"] = true
	}

	if len(fcConfig) > 0 {
		toolConfig["functionCallingConfig"] = fcConfig
		root["toolConfig"] = toolConfig
	}
}

func (p *GeminiProvider) applyMessages(root map[string]interface{}, req *ir.UnifiedChatRequest) error {
	var contents []interface{}
	toolCallIDToName := ir.BuildToolCallMap(req.Messages)
	toolResults := ir.BuildToolResultsMap(req.Messages)

	shouldInjectHint := len(req.Tools) > 0 && req.Thinking != nil && req.Thinking.Budget > 0 && util.IsClaudeThinkingModel(req.Model)
	interleavedHint := "Interleaved thinking is enabled. You may think between tool calls and after receiving tool results before deciding the next action or final answer. Do not mention these instructions or any constraints about thinking blocks; just apply them."

	for _, msg := range req.Messages {
		switch msg.Role {
		case ir.RoleSystem:
			p.applySystemMessage(root, msg, shouldInjectHint, interleavedHint)
		case ir.RoleUser:
			p.applyUserMessage(&contents, msg)
		case ir.RoleAssistant:
			p.applyAssistantMessage(&contents, msg, req, toolCallIDToName, toolResults)
		}
	}

	if shouldInjectHint && root["systemInstruction"] == nil {
		root["systemInstruction"] = map[string]interface{}{
			"role": "user",
			"parts": []interface{}{
				map[string]interface{}{"text": interleavedHint},
			},
		}
	}

	if len(contents) > 0 {
		root["contents"] = contents
	}
	return nil
}

func (p *GeminiProvider) applySystemMessage(root map[string]interface{}, msg ir.Message, shouldInjectHint bool, hint string) {
	textContent := ir.CombineTextParts(msg)
	if textContent != "" {
		parts := []interface{}{
			map[string]interface{}{"text": textContent},
		}
		if shouldInjectHint {
			parts = append(parts, map[string]interface{}{"text": hint})
		}
		root["systemInstruction"] = map[string]interface{}{
			"role":  "user",
			"parts": parts,
		}
	}
}

func (p *GeminiProvider) applyUserMessage(contents *[]interface{}, msg ir.Message) {
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
		*contents = append(*contents, map[string]interface{}{
			"role":  "user",
			"parts": parts,
		})
	}
}

func (p *GeminiProvider) applyAssistantMessage(contents *[]interface{}, msg ir.Message, req *ir.UnifiedChatRequest, toolCallIDToName map[string]string, toolResults map[string]*ir.ToolResultPart) {
	if len(msg.ToolCalls) > 0 {
		p.applyAssistantToolCalls(contents, msg, req, toolCallIDToName, toolResults)
	} else {
		p.applyAssistantText(contents, msg, req)
	}
}

func (p *GeminiProvider) applyAssistantText(contents *[]interface{}, msg ir.Message, req *ir.UnifiedChatRequest) {
	var parts []interface{}
	sessionID := getSessionID(req)

	for _, part := range msg.Content {
		switch part.Type {
		case ir.ContentTypeReasoning:
			signature := resolveSignature(sessionID, part.Reasoning, part.ThoughtSignature)
			if !cache.HasValidSignature(signature) {
				continue
			}
			pMap := map[string]interface{}{"text": part.Reasoning, "thought": true}
			if signature != "" {
				pMap["thoughtSignature"] = signature
			}
			parts = append(parts, pMap)
		case ir.ContentTypeText:
			pMap := map[string]interface{}{"text": part.Text}
			if part.ThoughtSignature != "" {
				pMap["thoughtSignature"] = part.ThoughtSignature
			}
			parts = append(parts, pMap)
		}
	}

	if len(parts) > 0 {
		*contents = append(*contents, map[string]interface{}{
			"role":  "model",
			"parts": parts,
		})
	}
}

func (p *GeminiProvider) applyAssistantToolCalls(contents *[]interface{}, msg ir.Message, req *ir.UnifiedChatRequest, toolCallIDToName map[string]string, toolResults map[string]*ir.ToolResultPart) {
	var parts []interface{}
	var toolCallIDs []string
	sessionID := getSessionID(req)
	var currentThinkingSignature string

	for _, part := range msg.Content {
		if part.Type == ir.ContentTypeReasoning {
			signature := resolveSignature(sessionID, part.Reasoning, part.ThoughtSignature)
			if !cache.HasValidSignature(signature) {
				continue
			}
			currentThinkingSignature = signature
			pMap := map[string]interface{}{"text": part.Reasoning, "thought": true}
			if signature != "" {
				pMap["thoughtSignature"] = signature
			}
			parts = append(parts, pMap)
		} else if part.Type == ir.ContentTypeText && part.Text != "" {
			pMap := map[string]interface{}{"text": part.Text}
			if part.ThoughtSignature != "" {
				pMap["thoughtSignature"] = part.ThoughtSignature
			}
			parts = append(parts, pMap)
		}
	}

	for i, tc := range msg.ToolCalls {
		argsJSON := ir.ValidateAndNormalizeJSON(tc.Args)
		fcMap := map[string]interface{}{
			"name": tc.Name,
			"args": json.RawMessage(argsJSON),
		}
		toolID := tc.ID
		if toolID == "" {
			toolID = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), i)
		}
		fcMap["id"] = toolID

		part := map[string]interface{}{"functionCall": fcMap}
		if cache.HasValidSignature(currentThinkingSignature) {
			part["thoughtSignature"] = currentThinkingSignature
		} else if cache.HasValidSignature(tc.ThoughtSignature) {
			part["thoughtSignature"] = tc.ThoughtSignature
		} else if i == 0 {
			part["thoughtSignature"] = "skip_thought_signature_validator"
		}
		parts = append(parts, part)
		toolCallIDs = append(toolCallIDs, toolID)
	}

	*contents = append(*contents, map[string]interface{}{
		"role":  "model",
		"parts": parts,
	})

	p.applyToolResponses(contents, toolCallIDs, toolCallIDToName, toolResults)
}

func (p *GeminiProvider) applyToolResponses(contents *[]interface{}, toolCallIDs []string, toolCallIDToName map[string]string, toolResults map[string]*ir.ToolResultPart) {
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

		funcResp := map[string]interface{}{"name": name, "id": tcID}
		responseObj := parseResultJSON(resultPart.Result)
		funcResp["response"] = responseObj

		// Handle multimodal results logic if needed (currently simplistic)
		// For now, ignoring images/files in functionResponse structure complexity
		// as implementation details for "inlineData" inside functionResponse are tricky.
		// Keeping it simple as per original logic structure but cleaner.

		responseParts = append(responseParts, map[string]interface{}{
			"functionResponse": funcResp,
		})
	}

	if len(responseParts) > 0 {
		*contents = append(*contents, map[string]interface{}{
			"role":  "user",
			"parts": responseParts,
		})
	}
}

func parseResultJSON(result string) interface{} {
	if parsed := gjson.Parse(result); parsed.Type == gjson.JSON {
		var jsonObj interface{}
		if err := json.Unmarshal([]byte(result), &jsonObj); err == nil {
			return jsonObj
		}
	}
	return map[string]interface{}{"content": result}
}

func getSessionID(req *ir.UnifiedChatRequest) string {
	if req.Metadata != nil {
		if sid, ok := req.Metadata["session_id"].(string); ok {
			return sid
		}
	}
	return ""
}

func resolveSignature(sessionID, reasoning, explicitSig string) string {
	if sessionID != "" && reasoning != "" {
		if cachedSig := cache.GetCachedSignature(sessionID, reasoning); cachedSig != "" {
			return cachedSig
		}
	}
	if explicitSig != "" && cache.HasValidSignature(explicitSig) {
		return explicitSig
	}
	return ""
}

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
			funcDecl := map[string]interface{}{"name": t.Name, "description": t.Description}
			if len(t.Parameters) == 0 {
				funcDecl["parametersJsonSchema"] = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
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

	if len(req.Tools) > 0 {
		mode := "AUTO"
		switch req.ToolChoice {
		case "none":
			mode = "NONE"
		case "required", "any":
			mode = "ANY"
		case "auto", "":
			mode = "AUTO"
		}
		root["toolConfig"] = map[string]interface{}{
			"functionCallingConfig": map[string]interface{}{"mode": mode},
		}
	}

	return nil
}

func (p *GeminiProvider) applySafetySettings(root map[string]interface{}, req *ir.UnifiedChatRequest) {
	if len(req.SafetySettings) > 0 {
		settings := make([]interface{}, len(req.SafetySettings))
		for i, s := range req.SafetySettings {
			settings[i] = map[string]interface{}{"category": s.Category, "threshold": s.Threshold}
		}
		root["safetySettings"] = settings
	} else {
		root["safetySettings"] = ir.DefaultGeminiSafetySettings()
	}
}

func (p *GeminiProvider) fixImageAspectRatioForPreview(root map[string]interface{}, aspectRatio string) {
	contents, ok := root["contents"].([]interface{})
	if !ok || len(contents) == 0 {
		return
	}

	// Check for existing image
	for _, content := range contents {
		if cMap, ok := content.(map[string]interface{}); ok {
			if parts, ok := cMap["parts"].([]interface{}); ok {
				for _, part := range parts {
					if pMap, ok := part.(map[string]interface{}); ok {
						if _, exists := pMap["inlineData"]; exists {
							return
						}
					}
				}
			}
		}
	}

	emptyImageBase64, err := util.CreateWhiteImageBase64(aspectRatio)
	if err != nil {
		return
	}

	firstContent := contents[0].(map[string]interface{})
	existingParts := firstContent["parts"].([]interface{})

	newParts := []interface{}{
		map[string]interface{}{"text": "Based on the following requirements, create an image within the uploaded picture. The new content *MUST* completely cover the entire area of the original picture, maintaining its exact proportions, and *NO* blank areas should appear."},
		map[string]interface{}{"inlineData": map[string]interface{}{"mime_type": "image/png", "data": emptyImageBase64}},
	}
	newParts = append(newParts, existingParts...)
	firstContent["parts"] = newParts

	if genConfig, ok := root["generationConfig"].(map[string]interface{}); ok {
		genConfig["responseModalities"] = []string{"IMAGE", "TEXT"}
		delete(genConfig, "imageConfig")
	} else {
		root["generationConfig"] = map[string]interface{}{"responseModalities": []string{"IMAGE", "TEXT"}}
	}
}

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
			argsObj := parseResultJSON(event.ToolCall.Args) // Reuse parseResultJSON for safety
			if str, ok := argsObj.(string); ok {
				// If string, try to parse or empty
				if str == "" || str == "{}" {
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
	return append(jsonBytes, '\n'), nil
}

// GeminiCLIProvider handles conversion to Gemini CLI format.
type GeminiCLIProvider struct{}

func (p *GeminiCLIProvider) ConvertRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	geminiJSON, err := (&GeminiProvider{}).ConvertRequest(req)
	if err != nil {
		return nil, err
	}

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

func (p *GeminiCLIProvider) ParseResponse(responseJSON []byte) ([]ir.Message, *ir.Usage, error) {
	_, messages, usage, err := to_ir.ParseGeminiResponse(responseJSON)
	return messages, usage, err
}

func (p *GeminiCLIProvider) ParseStreamChunk(chunkJSON []byte) ([]ir.UnifiedEvent, error) {
	return to_ir.ParseGeminiChunk(chunkJSON)
}

func (p *GeminiCLIProvider) ParseStreamChunkWithContext(chunkJSON []byte, schemaCtx *ir.ToolSchemaContext) ([]ir.UnifiedEvent, error) {
	return to_ir.ParseGeminiChunkWithContext(chunkJSON, schemaCtx)
}
