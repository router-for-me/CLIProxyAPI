package oaichat

import (
	"fmt"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

type ChatToGeminiStreamState struct {
	toolsByChoice   map[int][]*chatToGeminiStreamTool
	toolByIndex     map[chatToGeminiStreamToolKey]*chatToGeminiStreamTool
	toolByID        map[chatToGeminiStreamToolIDKey]*chatToGeminiStreamTool
	finishedChoices map[int]bool
	seenChoices     map[int]bool
	usage           *dto.Usage
	usageEmitted    bool
	finalized       bool
}

type chatToGeminiStreamToolKey struct {
	ChoiceIndex int
	ToolIndex   int
}

type chatToGeminiStreamToolIDKey struct {
	ChoiceIndex int
	ID          string
}

type chatToGeminiStreamTool struct {
	ID        string
	Name      string
	Arguments strings.Builder
	Emitted   bool
}

func NewChatToGeminiStreamState() *ChatToGeminiStreamState {
	return &ChatToGeminiStreamState{
		toolsByChoice:   make(map[int][]*chatToGeminiStreamTool),
		toolByIndex:     make(map[chatToGeminiStreamToolKey]*chatToGeminiStreamTool),
		toolByID:        make(map[chatToGeminiStreamToolIDKey]*chatToGeminiStreamTool),
		finishedChoices: make(map[int]bool),
		seenChoices:     make(map[int]bool),
	}
}

// ResponseOpenAI2Gemini 将 OpenAI 响应转换为 Gemini 格式
func ResponseOpenAI2Gemini(openAIResponse *dto.OpenAITextResponse, info convmeta.Meta) *dto.GeminiChatResponse {
	totalTokens := openAIResponse.TotalTokens
	if totalTokens == 0 {
		totalTokens = openAIResponse.PromptTokens + openAIResponse.CompletionTokens
	}
	geminiResponse := &dto.GeminiChatResponse{
		Candidates:       make([]dto.GeminiChatCandidate, 0, len(openAIResponse.Choices)),
		HasUsageMetadata: true,
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:     openAIResponse.PromptTokens,
			CandidatesTokenCount: openAIResponse.CompletionTokens,
			TotalTokenCount:      totalTokens,
			BillingUsage:         openAIBillingUsageFromUsage(&openAIResponse.Usage),
		},
	}
	if metadata, ok := geminiBillingMetadataFromOpenAIUsage(&openAIResponse.Usage); ok {
		geminiResponse.UsageMetadata = metadata
	}

	for _, choice := range openAIResponse.Choices {
		candidate := dto.GeminiChatCandidate{
			Index:         int64(choice.Index),
			SafetyRatings: []dto.GeminiChatSafetyRating{},
		}

		// 设置结束原因
		var finishReason string
		switch choice.FinishReason {
		case "stop":
			finishReason = "STOP"
		case "length":
			finishReason = "MAX_TOKENS"
		case "content_filter":
			finishReason = "SAFETY"
		case "tool_calls":
			finishReason = "STOP"
		default:
			finishReason = "STOP"
		}
		candidate.FinishReason = &finishReason

		// 转换消息内容
		content := dto.GeminiChatContent{
			Role:  "model",
			Parts: make([]dto.GeminiPart, 0),
		}

		textContent := choice.Message.StringContent()
		if textContent != "" {
			part := dto.GeminiPart{
				Text: textContent,
			}
			content.Parts = append(content.Parts, part)
		}

		toolCalls := choice.Message.ParseToolCalls()
		for _, toolCall := range toolCalls {
			part := dto.GeminiPart{
				FunctionCall: &dto.FunctionCall{
					ID:           toolCall.ID,
					FunctionName: toolCall.Function.Name,
					Arguments:    geminiFunctionArguments(toolCall.Function.Arguments),
				},
			}
			content.Parts = append(content.Parts, part)
		}

		candidate.Content = content
		geminiResponse.Candidates = append(geminiResponse.Candidates, candidate)
	}

	return geminiResponse
}

// StreamResponseOpenAI2Gemini 将 OpenAI 流式响应转换为 Gemini 格式
func StreamResponseOpenAI2Gemini(openAIResponse *dto.ChatCompletionsStreamResponse, info convmeta.Meta) *dto.GeminiChatResponse {
	// 检查是否有实际内容或结束标志
	hasContent := false
	hasFinishReason := false
	for _, choice := range openAIResponse.Choices {
		if len(choice.Delta.GetContentString()) > 0 || (choice.Delta.ToolCalls != nil && len(choice.Delta.ToolCalls) > 0) {
			hasContent = true
		}
		if choice.FinishReason != nil {
			hasFinishReason = true
		}
	}

	// 如果没有实际内容且没有结束标志，跳过。主要针对 openai 流响应开头的空数据
	if !hasContent && !hasFinishReason {
		return nil
	}

	estimatePromptTokens := 0
	if info != nil {
		estimatePromptTokens = info.GetEstimatePromptTokens()
	}
	geminiResponse := &dto.GeminiChatResponse{
		Candidates:       make([]dto.GeminiChatCandidate, 0, len(openAIResponse.Choices)),
		HasUsageMetadata: true,
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount:     estimatePromptTokens,
			CandidatesTokenCount: 0, // 流式响应中可能没有完整的 usage 信息
			TotalTokenCount:      estimatePromptTokens,
		},
	}

	if openAIResponse.Usage != nil {
		geminiResponse.UsageMetadata.PromptTokenCount = openAIResponse.Usage.PromptTokens
		geminiResponse.UsageMetadata.CandidatesTokenCount = openAIResponse.Usage.CompletionTokens
		geminiResponse.UsageMetadata.TotalTokenCount = openAIResponse.Usage.TotalTokens
		geminiResponse.UsageMetadata.BillingUsage = openAIBillingUsageFromUsage(openAIResponse.Usage)
		if metadata, ok := geminiBillingMetadataFromOpenAIUsage(openAIResponse.Usage); ok {
			geminiResponse.UsageMetadata = metadata
		}
	}

	for _, choice := range openAIResponse.Choices {
		candidate := dto.GeminiChatCandidate{
			Index:         int64(choice.Index),
			SafetyRatings: []dto.GeminiChatSafetyRating{},
		}

		// 设置结束原因
		if choice.FinishReason != nil {
			var finishReason string
			switch *choice.FinishReason {
			case "stop":
				finishReason = "STOP"
			case "length":
				finishReason = "MAX_TOKENS"
			case "content_filter":
				finishReason = "SAFETY"
			case "tool_calls":
				finishReason = "STOP"
			default:
				finishReason = "STOP"
			}
			candidate.FinishReason = &finishReason
		}

		// 转换消息内容
		content := dto.GeminiChatContent{
			Role:  "model",
			Parts: make([]dto.GeminiPart, 0),
		}

		// 处理工具调用
		if choice.Delta.ToolCalls != nil {
			for _, toolCall := range choice.Delta.ToolCalls {
				part := dto.GeminiPart{
					FunctionCall: &dto.FunctionCall{
						ID:           toolCall.ID,
						FunctionName: toolCall.Function.Name,
						Arguments:    geminiFunctionArguments(toolCall.Function.Arguments),
					},
				}
				content.Parts = append(content.Parts, part)
			}
		} else {
			// 处理文本内容
			textContent := choice.Delta.GetContentString()
			if textContent != "" {
				part := dto.GeminiPart{
					Text: textContent,
				}
				content.Parts = append(content.Parts, part)
			}
		}

		candidate.Content = content
		geminiResponse.Candidates = append(geminiResponse.Candidates, candidate)
	}

	return geminiResponse
}

// ConvertChunk accumulates OpenAI tool-call deltas until their choice ends.
// Gemini functionCall parts are atomic, so emitting each OpenAI arguments
// fragment as a separate part would create duplicate calls with invalid input.
func (s *ChatToGeminiStreamState) ConvertChunk(openAIResponse *dto.ChatCompletionsStreamResponse, info convmeta.Meta) ([]*dto.GeminiChatResponse, error) {
	if openAIResponse == nil {
		return nil, nil
	}
	if s == nil {
		return nil, fmt.Errorf("OpenAI chat to Gemini stream state is required")
	}
	if s.finalized {
		return nil, fmt.Errorf("OpenAI chat to Gemini stream received data after finalization")
	}
	if s.toolsByChoice == nil {
		s.toolsByChoice = make(map[int][]*chatToGeminiStreamTool)
	}
	if s.toolByIndex == nil {
		s.toolByIndex = make(map[chatToGeminiStreamToolKey]*chatToGeminiStreamTool)
	}
	if s.toolByID == nil {
		s.toolByID = make(map[chatToGeminiStreamToolIDKey]*chatToGeminiStreamTool)
	}
	if s.finishedChoices == nil {
		s.finishedChoices = make(map[int]bool)
	}
	if s.seenChoices == nil {
		s.seenChoices = make(map[int]bool)
	}
	if openAIResponse.Usage != nil {
		s.usage = UsageFromChatUsage(openAIResponse.Usage)
	}

	candidates := make([]dto.GeminiChatCandidate, 0, len(openAIResponse.Choices))
	for _, choice := range openAIResponse.Choices {
		s.seenChoices[choice.Index] = true
		hasText := choice.Delta.GetContentString() != ""
		hasToolDelta := len(choice.Delta.ToolCalls) > 0
		hasFinish := choice.FinishReason != nil && strings.TrimSpace(*choice.FinishReason) != ""
		if s.finishedChoices[choice.Index] {
			if hasText || hasToolDelta {
				return nil, fmt.Errorf("OpenAI chat choice %d received data after completion", choice.Index)
			}
			continue
		}

		for position, toolCall := range choice.Delta.ToolCalls {
			if toolCall.Index == nil {
				toolCall.SetIndex(position)
			}
			if err := s.appendToolCallDelta(choice.Index, toolCall); err != nil {
				return nil, err
			}
		}

		candidate := dto.GeminiChatCandidate{
			Index:         int64(choice.Index),
			SafetyRatings: []dto.GeminiChatSafetyRating{},
			Content: dto.GeminiChatContent{
				Role:  "model",
				Parts: make([]dto.GeminiPart, 0),
			},
		}
		if hasText {
			candidate.Content.Parts = append(candidate.Content.Parts, dto.GeminiPart{Text: choice.Delta.GetContentString()})
		}
		if hasFinish {
			parts, err := s.finishChoice(choice.Index)
			if err != nil {
				return nil, err
			}
			candidate.Content.Parts = append(candidate.Content.Parts, parts...)
			finishReason := geminiFinishReason(*choice.FinishReason)
			candidate.FinishReason = &finishReason
			s.finishedChoices[choice.Index] = true
		}
		if len(candidate.Content.Parts) > 0 || candidate.FinishReason != nil {
			candidates = append(candidates, candidate)
		}
	}

	if len(candidates) == 0 {
		if openAIResponse.Usage != nil && len(s.finishedChoices) > 0 {
			s.usageEmitted = true
			return []*dto.GeminiChatResponse{newGeminiStreamResponse(nil, s.usage, info)}, nil
		}
		return nil, nil
	}
	if openAIResponse.Usage != nil {
		s.usageEmitted = true
	}
	return []*dto.GeminiChatResponse{newGeminiStreamResponse(candidates, openAIResponse.Usage, info)}, nil
}

// Finalize emits any calls left pending when an upstream closes without a
// finish-reason chunk. Calling Finalize more than once is safe.
func (s *ChatToGeminiStreamState) Finalize(info convmeta.Meta) ([]*dto.GeminiChatResponse, error) {
	if s == nil || s.finalized {
		return nil, nil
	}

	choiceIndexes := make(map[int]struct{})
	for choiceIndex, tools := range s.toolsByChoice {
		for _, tool := range tools {
			if !tool.Emitted {
				choiceIndexes[choiceIndex] = struct{}{}
				break
			}
		}
	}
	for choiceIndex := range s.seenChoices {
		if !s.finishedChoices[choiceIndex] {
			choiceIndexes[choiceIndex] = struct{}{}
		}
	}
	orderedChoices := make([]int, 0, len(choiceIndexes))
	for choiceIndex := range choiceIndexes {
		orderedChoices = append(orderedChoices, choiceIndex)
	}
	sort.Ints(orderedChoices)

	candidates := make([]dto.GeminiChatCandidate, 0, len(orderedChoices))
	for _, choiceIndex := range orderedChoices {
		parts, err := s.finishChoice(choiceIndex)
		if err != nil {
			return nil, err
		}
		finishReason := "STOP"
		candidates = append(candidates, dto.GeminiChatCandidate{
			Index:         int64(choiceIndex),
			FinishReason:  &finishReason,
			SafetyRatings: []dto.GeminiChatSafetyRating{},
			Content: dto.GeminiChatContent{
				Role:  "model",
				Parts: parts,
			},
		})
	}
	if len(candidates) == 0 {
		s.finalized = true
		if s.usage == nil || s.usageEmitted {
			return nil, nil
		}
		s.usageEmitted = true
		return []*dto.GeminiChatResponse{newGeminiStreamResponse(nil, s.usage, info)}, nil
	}
	s.finalized = true
	s.usageEmitted = s.usage != nil
	return []*dto.GeminiChatResponse{newGeminiStreamResponse(candidates, s.usage, info)}, nil
}

func (s *ChatToGeminiStreamState) Usage() *dto.Usage {
	if s == nil || s.usage == nil {
		return nil
	}
	return UsageFromChatUsage(s.usage)
}

func (s *ChatToGeminiStreamState) SetUsage(usage *dto.Usage) {
	if s == nil || usage == nil {
		return
	}
	s.usage = UsageFromChatUsage(usage)
}

func (s *ChatToGeminiStreamState) StreamUsage() *dto.Usage {
	return s.Usage()
}

func (s *ChatToGeminiStreamState) SetStreamUsage(usage *dto.Usage) {
	s.SetUsage(usage)
}

func (s *ChatToGeminiStreamState) appendToolCallDelta(choiceIndex int, toolCall dto.ToolCallResponse) error {
	toolIndex := 0
	if toolCall.Index != nil {
		toolIndex = *toolCall.Index
	}
	if toolIndex < 0 {
		return fmt.Errorf("OpenAI chat choice %d has negative tool-call index %d", choiceIndex, toolIndex)
	}
	key := chatToGeminiStreamToolKey{ChoiceIndex: choiceIndex, ToolIndex: toolIndex}
	incomingID := strings.TrimSpace(toolCall.ID)
	var tool *chatToGeminiStreamTool
	if incomingID != "" {
		tool = s.toolByID[chatToGeminiStreamToolIDKey{ChoiceIndex: choiceIndex, ID: incomingID}]
	}
	if tool == nil {
		tool = s.toolByIndex[key]
	}
	if tool != nil && incomingID != "" && tool.ID != "" && tool.ID != incomingID {
		tool = nil
	}
	if tool == nil {
		tool = &chatToGeminiStreamTool{}
		s.toolsByChoice[choiceIndex] = append(s.toolsByChoice[choiceIndex], tool)
	}
	s.toolByIndex[key] = tool
	// Compatibility gateways may reset a source index for the next occurrence.
	// Once identity changes, keep the new occurrence active for later metadata-free deltas.
	if tool.Emitted {
		return fmt.Errorf("OpenAI chat choice %d tool-call index %d received data after completion", choiceIndex, toolIndex)
	}

	if incomingID != "" {
		if tool.ID != "" && tool.ID != incomingID {
			return fmt.Errorf("OpenAI chat choice %d tool-call index %d changed id from %q to %q", choiceIndex, toolIndex, tool.ID, incomingID)
		}
		tool.ID = incomingID
		s.toolByID[chatToGeminiStreamToolIDKey{ChoiceIndex: choiceIndex, ID: incomingID}] = tool
	}
	incomingName := strings.TrimSpace(toolCall.Function.Name)
	if incomingName != "" {
		if tool.Name != "" && tool.Name != incomingName {
			return fmt.Errorf("OpenAI chat choice %d tool-call index %d changed name from %q to %q", choiceIndex, toolIndex, tool.Name, incomingName)
		}
		tool.Name = incomingName
	}
	tool.Arguments.WriteString(toolCall.Function.Arguments)
	return nil
}

func (s *ChatToGeminiStreamState) finishChoice(choiceIndex int) ([]dto.GeminiPart, error) {
	tools := s.toolsByChoice[choiceIndex]
	pending := make([]*chatToGeminiStreamTool, 0, len(tools))
	for _, tool := range tools {
		if !tool.Emitted {
			pending = append(pending, tool)
		}
	}

	parts := make([]dto.GeminiPart, 0, len(pending))
	for _, tool := range pending {
		if tool.Name == "" {
			return nil, fmt.Errorf("OpenAI chat choice %d has a tool call without a function name", choiceIndex)
		}
		parts = append(parts, dto.GeminiPart{FunctionCall: &dto.FunctionCall{
			ID:           tool.ID,
			FunctionName: tool.Name,
			Arguments:    geminiFunctionArguments(tool.Arguments.String()),
		}})
	}
	for _, tool := range pending {
		tool.Emitted = true
	}
	return parts, nil
}

func newGeminiStreamResponse(candidates []dto.GeminiChatCandidate, usage *dto.Usage, info convmeta.Meta) *dto.GeminiChatResponse {
	if candidates == nil {
		candidates = make([]dto.GeminiChatCandidate, 0)
	}
	estimatePromptTokens := 0
	if info != nil {
		estimatePromptTokens = info.GetEstimatePromptTokens()
	}
	response := &dto.GeminiChatResponse{
		Candidates:       candidates,
		HasUsageMetadata: true,
		UsageMetadata: dto.GeminiUsageMetadata{
			PromptTokenCount: estimatePromptTokens,
			TotalTokenCount:  estimatePromptTokens,
		},
	}
	if usage == nil {
		return response
	}
	response.UsageMetadata.PromptTokenCount = usage.PromptTokens
	response.UsageMetadata.CandidatesTokenCount = usage.CompletionTokens
	response.UsageMetadata.TotalTokenCount = usage.TotalTokens
	response.UsageMetadata.BillingUsage = openAIBillingUsageFromUsage(usage)
	if metadata, ok := geminiBillingMetadataFromOpenAIUsage(usage); ok {
		response.UsageMetadata = metadata
	}
	return response
}

func geminiFunctionArguments(raw string) map[string]any {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "null" {
		return map[string]any{}
	}
	var args map[string]any
	if err := kitutil.Unmarshal([]byte(raw), &args); err == nil && args != nil {
		return args
	}
	// Preserve historically accepted malformed/non-object input without
	// emitting a non-object Gemini args value.
	return map[string]any{"arguments": raw}
}

func geminiFinishReason(finishReason string) string {
	switch strings.TrimSpace(finishReason) {
	case "length":
		return "MAX_TOKENS"
	case "content_filter":
		return "SAFETY"
	default:
		return "STOP"
	}
}

func geminiBillingMetadataFromOpenAIUsage(usage *dto.Usage) (dto.GeminiUsageMetadata, bool) {
	if usage == nil || usage.BillingUsage == nil || usage.BillingUsage.GeminiUsageMetadata == nil {
		return dto.GeminiUsageMetadata{}, false
	}
	if usage.BillingUsage.Source != dto.BillingUsageSourceGeminiChat && usage.BillingUsage.Semantic != dto.BillingUsageSemanticGemini {
		return dto.GeminiUsageMetadata{}, false
	}
	billingUsage := dto.CloneBillingUsage(usage.BillingUsage)
	if billingUsage == nil || billingUsage.GeminiUsageMetadata == nil {
		return dto.GeminiUsageMetadata{}, false
	}
	metadata := *billingUsage.GeminiUsageMetadata
	// Restore the sidecar marker on the restored native payload so the next
	// hop keeps settling on the original dialect (including Estimated).
	metadata.BillingUsage = dto.CloneBillingUsage(usage.BillingUsage)
	return metadata, true
}

func openAIBillingUsageFromUsage(usage *dto.Usage) *dto.BillingUsage {
	if usage == nil {
		return nil
	}
	// An existing sidecar snapshots the original provider usage; carry it
	// across this bridge unchanged regardless of its dialect. Only synthesize
	// an OpenAI snapshot when no sidecar exists yet.
	if existingBillingUsage := dto.CloneBillingUsage(usage.BillingUsage); existingBillingUsage != nil {
		return existingBillingUsage
	}
	return dto.NewOpenAIChatBillingUsage(usage)
}
