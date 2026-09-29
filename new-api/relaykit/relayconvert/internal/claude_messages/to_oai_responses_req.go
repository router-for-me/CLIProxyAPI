package claudemessages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/internal/convdiag"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
)

func ClaudeMessagesRequestToOpenAIResponses(ctx context.Context, claudeRequest dto.ClaudeRequest, info convmeta.Meta) (*dto.OpenAIResponsesRequest, error) {
	if strings.TrimSpace(claudeRequest.Model) == "" {
		return nil, errors.New("model is required")
	}

	input, err := claudeMessagesToResponsesInput(claudeRequest.Messages)
	if err != nil {
		return nil, err
	}
	instructions, err := claudeSystemToResponsesInstructions(&claudeRequest)
	if err != nil {
		return nil, err
	}
	tools, err := claudeToolsToResponsesTools(claudeRequest.Tools)
	if err != nil {
		return nil, err
	}
	toolChoice, parallelToolCalls, err := claudeToolChoiceToResponses(claudeRequest.ToolChoice)
	if err != nil {
		return nil, err
	}

	// Claude context_management is an object containing protocol-specific edit
	// strategies. Responses expects an array of compaction entries, so copying
	// the raw Claude value would produce an invalid upstream request.
	responsesRequest := &dto.OpenAIResponsesRequest{
		Model:             claudeRequest.Model,
		Input:             input,
		Instructions:      instructions,
		Metadata:          append(json.RawMessage(nil), claudeRequest.Metadata...),
		ServiceTier:       claudeRequest.ServiceTier,
		Stream:            claudeRequest.Stream,
		Temperature:       claudeRequest.Temperature,
		Tools:             tools,
		ToolChoice:        toolChoice,
		ParallelToolCalls: parallelToolCalls,
		TopP:              claudeRequest.TopP,
	}
	if info != nil && !convmeta.OptionsOf(info).OpenRouterDialect {
		// Keep the outgoing -thinking suffix so a cascaded downstream new-api
		// can recover reasoning intent from the model name. This is an
		// emission-side policy, not converter-side suffix parsing.
		thinkingSuffix := "-thinking"
		if strings.HasSuffix(info.GetOriginModelName(), thinkingSuffix) && !strings.HasSuffix(responsesRequest.Model, thinkingSuffix) {
			responsesRequest.Model += thinkingSuffix
		}
	}
	if claudeRequest.MaxTokens != nil {
		maxOutputTokens := *claudeRequest.MaxTokens
		responsesRequest.MaxOutputTokens = &maxOutputTokens
	} else if claudeRequest.MaxTokensToSample != nil {
		maxOutputTokens := *claudeRequest.MaxTokensToSample
		responsesRequest.MaxOutputTokens = &maxOutputTokens
	}

	reasoningIntent, effectiveEffort, err := claudeRequestReasoningIntent(ctx, &claudeRequest, info)
	if err != nil {
		return nil, reasoning.AsClientError(err)
	}
	if err := reasoning.ApplyToOpenAIResponses(responsesRequest, reasoningIntent); err != nil {
		return nil, reasoning.AsClientError(err)
	}
	if info != nil && effectiveEffort != "" {
		info.SetReasoningEffort(string(effectiveEffort))
	}

	return responsesRequest, nil
}

func claudeRequestReasoningIntent(ctx context.Context, claudeRequest *dto.ClaudeRequest, info convmeta.Meta) (reasoning.Intent, reasoning.Effort, error) {
	reasoningIntent, diagnostics, err := reasoning.FromClaude(claudeRequest)
	if err != nil {
		return reasoning.Intent{}, "", err
	}
	convdiag.Add(ctx, diagnostics...)
	sourceModel := claudeRequest.Model
	if info != nil && info.GetOriginModelName() != "" {
		sourceModel = info.GetOriginModelName()
	}
	if suffix := reasoning.IntentFromState(convmeta.ReasoningStateOf(info)); !suffix.IsEmpty() {
		reasoningIntent, diagnostics, err = reasoning.MergeExplicitAndSuffix(reasoningIntent, suffix, sourceModel)
		if err != nil {
			return reasoning.Intent{}, "", err
		}
		convdiag.Add(ctx, diagnostics...)
	}
	reasoningIntent = reasoning.ResolveClaudeDefault(sourceModel, reasoningIntent)
	return reasoningIntent, reasoning.EffectiveEffort(reasoningIntent), nil
}

func claudeSystemToResponsesInstructions(request *dto.ClaudeRequest) (json.RawMessage, error) {
	if request == nil || request.System == nil {
		return nil, nil
	}
	if request.IsStringSystem() {
		return kitutil.Marshal(request.GetStringSystem())
	}

	var instructions strings.Builder
	systemBlocks, err := kitutil.Any2Type[[]dto.ClaudeMediaMessage](request.System)
	if err != nil {
		return nil, fmt.Errorf("invalid Claude system content: %w", err)
	}
	for _, block := range systemBlocks {
		if block.Type == "text" || block.Type == "input_text" || block.Type == "" {
			instructions.WriteString(block.GetText())
		}
	}
	if instructions.Len() == 0 {
		return nil, nil
	}
	return kitutil.Marshal(instructions.String())
}

func claudeMessagesToResponsesInput(messages []dto.ClaudeMessage) (json.RawMessage, error) {
	input := make([]map[string]any, 0, len(messages))
	for messageIndex := range messages {
		message := messages[messageIndex]
		role := strings.TrimSpace(message.Role)
		if role == "" {
			continue
		}
		if message.IsStringContent() {
			input = append(input, map[string]any{
				"role":    role,
				"content": message.GetStringContent(),
			})
			continue
		}

		blocks, err := message.ParseContent()
		if err != nil {
			return nil, fmt.Errorf("messages[%d].content: %w", messageIndex, err)
		}
		contentParts := make([]map[string]any, 0, len(blocks))
		flushContent := func() {
			if len(contentParts) == 0 {
				return
			}
			input = append(input, map[string]any{
				"role":    role,
				"content": contentParts,
			})
			contentParts = nil
		}

		for blockIndex := range blocks {
			block := blocks[blockIndex]
			switch block.Type {
			case "text", "input_text":
				partType := "input_text"
				if role == "assistant" {
					partType = "output_text"
				}
				contentParts = append(contentParts, map[string]any{
					"type": partType,
					"text": block.GetText(),
				})
			case "image":
				if source := claudeSourceURL(block.Source); source != "" {
					contentParts = append(contentParts, map[string]any{
						"type":      "input_image",
						"image_url": source,
					})
				}
			case "document":
				if source := claudeSourceURL(block.Source); source != "" {
					contentParts = append(contentParts, map[string]any{
						"type":      "input_file",
						"file_data": source,
					})
				}
			case "tool_use":
				flushContent()
				arguments, err := kitutil.Marshal(block.Input)
				if err != nil {
					return nil, fmt.Errorf("messages[%d].content[%d].input: %w", messageIndex, blockIndex, err)
				}
				if block.Input == nil {
					arguments = []byte("{}")
				}
				input = append(input, map[string]any{
					"type":      "function_call",
					"call_id":   block.Id,
					"name":      block.Name,
					"arguments": string(arguments),
				})
			case "tool_result":
				flushContent()
				output, err := claudeToolResultToResponsesOutput(block.Content)
				if err != nil {
					return nil, fmt.Errorf("messages[%d].content[%d].content: %w", messageIndex, blockIndex, err)
				}
				input = append(input, map[string]any{
					"type":    "function_call_output",
					"call_id": block.ToolUseId,
					"output":  output,
				})
			}
		}
		flushContent()
	}
	return kitutil.Marshal(input)
}

func claudeToolsToResponsesTools(value any) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	tools, err := kitutil.Any2Type[[]dto.Tool](value)
	if err != nil {
		return nil, fmt.Errorf("invalid Claude tools: %w", err)
	}
	converted := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		function := map[string]any{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  tool.InputSchema,
		}
		if tool.Strict != nil {
			function["strict"] = *tool.Strict
		}
		converted = append(converted, function)
	}
	return kitutil.Marshal(converted)
}

func claudeToolChoiceToResponses(value any) (json.RawMessage, json.RawMessage, error) {
	if value == nil {
		return nil, nil, nil
	}
	choice, err := kitutil.Any2Type[dto.ClaudeToolChoice](value)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid Claude tool_choice: %w", err)
	}

	var converted any
	switch choice.Type {
	case "", "auto":
		converted = "auto"
	case "any":
		converted = "required"
	case "none":
		converted = "none"
	case "tool":
		converted = map[string]any{"type": "function", "name": choice.Name}
	default:
		return nil, nil, fmt.Errorf("unsupported Claude tool_choice type %q", choice.Type)
	}
	toolChoice, err := kitutil.Marshal(converted)
	if err != nil {
		return nil, nil, err
	}

	var parallelToolCalls json.RawMessage
	if choice.DisableParallelToolUse && choice.Type != "none" {
		parallelToolCalls, err = kitutil.Marshal(false)
		if err != nil {
			return nil, nil, err
		}
	}
	return toolChoice, parallelToolCalls, nil
}

func claudeToolResultToResponsesOutput(content any) (any, error) {
	if content == nil {
		return "", nil
	}
	if text, ok := content.(string); ok {
		return text, nil
	}
	blocks, err := kitutil.Any2Type[[]dto.ClaudeMediaMessage](content)
	if err != nil {
		return content, nil
	}
	parts := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "text", "input_text":
			parts = append(parts, map[string]any{"type": "input_text", "text": block.GetText()})
		case "image":
			if source := claudeSourceURL(block.Source); source != "" {
				parts = append(parts, map[string]any{"type": "input_image", "image_url": source})
			}
		case "document":
			if source := claudeSourceURL(block.Source); source != "" {
				parts = append(parts, map[string]any{"type": "input_file", "file_data": source})
			}
		}
	}
	if len(parts) == 0 {
		return content, nil
	}
	return parts, nil
}

func claudeSourceURL(source *dto.ClaudeMessageSource) string {
	if source == nil {
		return ""
	}
	if strings.TrimSpace(source.Url) != "" {
		return source.Url
	}
	data := kitutil.Interface2String(source.Data)
	if data == "" {
		return ""
	}
	if strings.HasPrefix(data, "data:") {
		return data
	}
	return fmt.Sprintf("data:%s;base64,%s", source.MediaType, data)
}
