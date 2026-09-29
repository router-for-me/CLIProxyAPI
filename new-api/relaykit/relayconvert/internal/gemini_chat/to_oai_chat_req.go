package geminichat

import (
	"context"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/internal/convdiag"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/internal/jsonutil"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
)

func GeminiGenerateContentRequestToOpenAIChat(ctx context.Context, geminiRequest *dto.GeminiChatRequest, info convmeta.Meta) (*dto.GeneralOpenAIRequest, error) {
	modelName := ""
	isStream := false
	if info != nil {
		isStream = info.GetIsStream()
	}
	modelName = convmeta.UpstreamModelName(info)
	openaiRequest := &dto.GeneralOpenAIRequest{
		Model:  modelName,
		Stream: kitutil.GetPointer(isStream),
	}
	sourceModelName := modelName
	if info != nil && info.GetOriginModelName() != "" {
		sourceModelName = info.GetOriginModelName()
	}
	opts := convmeta.OptionsOf(info)
	preserveSuffix := opts.ShouldPreserveThinkingSuffix(sourceModelName)
	// Capability lookups need the unsuffixed Gemini model the client addressed.
	// The host already folded any alias into ReasoningState; mirror its parser
	// so gemini-pro-latest-thinking still resolves to gemini-pro-latest.
	baseSourceModel := sourceModelName
	if !preserveSuffix {
		baseSourceModel = reasoning.ParseModelModifiers(sourceModelName).Base
		if base, _, found, err := reasoning.ParseGeminiModelSuffix(baseSourceModel, opts.Gemini.ThinkingAdapterEnabled); err == nil && found {
			baseSourceModel = base
		}
	}
	if baseSourceModel != "" && geminiRequest.GenerationConfig.ThinkingConfig != nil {
		_, diagnostics, err := reasoning.NormalizeGeminiThinkingConfig(baseSourceModel, &geminiRequest.GenerationConfig)
		if err != nil {
			return nil, reasoning.AsClientError(err)
		}
		convdiag.Add(ctx, diagnostics...)
	}
	reasoningIntent, diagnostics, err := reasoning.FromGemini(geminiRequest)
	if err != nil {
		return nil, reasoning.AsClientError(err)
	}
	convdiag.Add(ctx, diagnostics...)
	if !preserveSuffix {
		if suffix := reasoning.IntentFromState(convmeta.ReasoningStateOf(info)); !suffix.IsEmpty() {
			reasoningIntent, diagnostics, err = reasoning.MergeExplicitAndSuffix(reasoningIntent, suffix, sourceModelName)
			if err != nil {
				return nil, reasoning.AsClientError(err)
			}
			convdiag.Add(ctx, diagnostics...)
		}
	}
	reasoningIntent = reasoning.ResolveGeminiDefault(baseSourceModel, reasoningIntent)
	effectiveEffort := reasoning.EffectiveEffort(reasoningIntent)
	if err := reasoning.ApplyToOpenAIChat(openaiRequest, reasoningIntent); err != nil {
		return nil, reasoning.AsClientError(err)
	}
	if effectiveEffort != "" && info != nil {
		info.SetReasoningEffort(string(effectiveEffort))
	}

	callHistory := newGeminiFunctionCallHistory(geminiRequest.Contents)
	var messages []dto.Message
	for _, content := range geminiRequest.Contents {
		message := dto.Message{
			Role: convertGeminiRoleToOpenAI(content.Role),
		}

		var mediaContents []dto.MediaContent
		var toolCalls []dto.ToolCallRequest
		var reasoningTexts []string
		for _, part := range content.Parts {
			if part.Text != "" {
				if part.Thought {
					reasoningTexts = append(reasoningTexts, part.Text)
					continue
				}
				mediaContent := dto.MediaContent{
					Type: "text",
					Text: part.Text,
				}
				mediaContents = append(mediaContents, mediaContent)
			} else if part.InlineData != nil {
				mediaContent := dto.MediaContent{
					Type: "image_url",
					ImageUrl: &dto.MessageImageUrl{
						Url:      fmt.Sprintf("data:%s;base64,%s", part.InlineData.MimeType, part.InlineData.Data),
						Detail:   "auto",
						MimeType: part.InlineData.MimeType,
					},
				}
				mediaContents = append(mediaContents, mediaContent)
			} else if part.FileData != nil {
				mediaContent := dto.MediaContent{
					Type: "image_url",
					ImageUrl: &dto.MessageImageUrl{
						Url:      part.FileData.FileUri,
						Detail:   "auto",
						MimeType: part.FileData.MimeType,
					},
				}
				mediaContents = append(mediaContents, mediaContent)
			} else if part.FunctionCall != nil {
				toolCall := dto.ToolCallRequest{
					ID:   callHistory.add(part.FunctionCall),
					Type: "function",
					Function: dto.FunctionRequest{
						Name:      part.FunctionCall.FunctionName,
						Arguments: jsonutil.ToJSONString(part.FunctionCall.Arguments),
					},
				}
				toolCalls = append(toolCalls, toolCall)
			} else if part.FunctionResponse != nil {
				toolMessage := dto.Message{
					Role:       "tool",
					ToolCallId: callHistory.match(part.FunctionResponse),
				}
				toolMessage.SetStringContent(jsonutil.ToJSONString(part.FunctionResponse.Response))
				messages = append(messages, toolMessage)
			}
		}

		if len(toolCalls) > 0 {
			message.SetToolCalls(toolCalls)
		} else if len(mediaContents) == 1 && mediaContents[0].Type == "text" {
			message.Content = mediaContents[0].Text
		} else if len(mediaContents) > 0 {
			message.SetMediaContent(mediaContents)
		}
		if len(reasoningTexts) > 0 {
			reasoningContent := strings.Join(reasoningTexts, "\n")
			message.ReasoningContent = &reasoningContent
		}

		if len(message.ParseContent()) > 0 || len(message.ToolCalls) > 0 || message.ReasoningContent != nil {
			messages = append(messages, message)
		}
	}

	openaiRequest.Messages = messages

	if geminiRequest.GenerationConfig.Temperature != nil {
		openaiRequest.Temperature = geminiRequest.GenerationConfig.Temperature
	}
	if geminiRequest.GenerationConfig.TopP != nil {
		openaiRequest.TopP = kitutil.GetPointer(*geminiRequest.GenerationConfig.TopP)
	}
	if geminiRequest.GenerationConfig.TopK != nil {
		openaiRequest.TopK = kitutil.GetPointer(int(*geminiRequest.GenerationConfig.TopK))
	}
	if geminiRequest.GenerationConfig.MaxOutputTokens != nil {
		openaiRequest.MaxTokens = kitutil.GetPointer(*geminiRequest.GenerationConfig.MaxOutputTokens)
	}
	if len(geminiRequest.GenerationConfig.StopSequences) > 0 {
		openaiRequest.Stop = geminiRequest.GenerationConfig.StopSequences[:min(len(geminiRequest.GenerationConfig.StopSequences), 4)]
	}
	if geminiRequest.GenerationConfig.CandidateCount != nil {
		openaiRequest.N = kitutil.GetPointer(*geminiRequest.GenerationConfig.CandidateCount)
	}

	if len(geminiRequest.GetTools()) > 0 {
		var tools []dto.ToolCallRequest
		for _, tool := range geminiRequest.GetTools() {
			if tool.FunctionDeclarations == nil {
				continue
			}
			functionDeclarations, err := kitutil.Any2Type[[]dto.FunctionRequest](tool.FunctionDeclarations)
			if err != nil {
				kitutil.LogSystemError(fmt.Sprintf("failed to parse gemini function declarations: %v (type=%T)", err, tool.FunctionDeclarations))
				continue
			}
			for _, function := range functionDeclarations {
				openAITool := dto.ToolCallRequest{
					Type: "function",
					Function: dto.FunctionRequest{
						Name:        function.Name,
						Description: function.Description,
						Parameters:  function.Parameters,
					},
				}
				tools = append(tools, openAITool)
			}
		}
		if len(tools) > 0 {
			openaiRequest.Tools = tools
		}
	}

	if geminiRequest.SystemInstructions != nil {
		systemMessage := dto.Message{
			Role:    "system",
			Content: extractTextFromGeminiParts(geminiRequest.SystemInstructions.Parts),
		}
		openaiRequest.Messages = append([]dto.Message{systemMessage}, openaiRequest.Messages...)
	}

	return openaiRequest, nil
}

type geminiPendingFunctionCall struct {
	id   string
	name string
}

// geminiFunctionCallHistory keeps legacy Gemini histories without call IDs
// correlated across content boundaries. Named matching permits results for
// different parallel functions to arrive out of order; same-name calls use
// their original call order because old payloads contain no stronger identity.
type geminiFunctionCallHistory struct {
	reservedIDs map[string]struct{}
	pending     []geminiPendingFunctionCall
	nextID      int
}

func newGeminiFunctionCallHistory(contents []dto.GeminiChatContent) *geminiFunctionCallHistory {
	history := &geminiFunctionCallHistory{
		reservedIDs: make(map[string]struct{}),
		nextID:      1,
	}
	for _, content := range contents {
		for _, part := range content.Parts {
			if part.FunctionCall != nil && part.FunctionCall.ID != "" {
				history.reservedIDs[part.FunctionCall.ID] = struct{}{}
			}
			if part.FunctionResponse != nil {
				if id := kitutil.JsonRawMessageToString(part.FunctionResponse.ID); id != "" {
					history.reservedIDs[id] = struct{}{}
				}
			}
		}
	}
	return history
}

func (h *geminiFunctionCallHistory) add(call *dto.FunctionCall) string {
	id := call.ID
	if id == "" {
		id = h.newFallbackID()
	}
	h.pending = append(h.pending, geminiPendingFunctionCall{id: id, name: call.FunctionName})
	return id
}

func (h *geminiFunctionCallHistory) match(response *dto.GeminiFunctionResponse) string {
	if id := kitutil.JsonRawMessageToString(response.ID); id != "" {
		h.removePendingByID(id)
		return id
	}

	for i, call := range h.pending {
		if response.Name != "" && call.name != response.Name {
			continue
		}
		h.pending = append(h.pending[:i], h.pending[i+1:]...)
		return call.id
	}
	return h.newFallbackID()
}

func (h *geminiFunctionCallHistory) removePendingByID(id string) {
	for i, call := range h.pending {
		if call.id != id {
			continue
		}
		h.pending = append(h.pending[:i], h.pending[i+1:]...)
		return
	}
}

func (h *geminiFunctionCallHistory) newFallbackID() string {
	for {
		id := fmt.Sprintf("call_%d", h.nextID)
		h.nextID++
		if _, exists := h.reservedIDs[id]; exists {
			continue
		}
		h.reservedIDs[id] = struct{}{}
		return id
	}
}

func convertGeminiRoleToOpenAI(geminiRole string) string {
	switch geminiRole {
	case "user":
		return "user"
	case "model":
		return "assistant"
	case "function":
		return "function"
	default:
		return "user"
	}
}

func extractTextFromGeminiParts(parts []dto.GeminiPart) string {
	texts := make([]string, 0)
	for _, part := range parts {
		if part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}
