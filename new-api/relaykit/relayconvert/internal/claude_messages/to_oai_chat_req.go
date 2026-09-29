package claudemessages

import (
	"context"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
)

const (
	webSearchMaxUsesLow    = 1
	webSearchMaxUsesMedium = 5
	webSearchMaxUsesHigh   = 10
)

type openRouterRequestReasoning struct {
	Enabled   *bool  `json:"enabled,omitempty"`
	Effort    string `json:"effort,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
	Exclude   bool   `json:"exclude,omitempty"`
}

func ClaudeMessagesRequestToOpenAIChat(ctx context.Context, claudeRequest dto.ClaudeRequest, info convmeta.Meta) (*dto.GeneralOpenAIRequest, error) {
	openAIRequest := dto.GeneralOpenAIRequest{
		Model:       claudeRequest.Model,
		Temperature: claudeRequest.Temperature,
	}
	if claudeRequest.MaxTokens != nil {
		openAIRequest.MaxTokens = kitutil.GetPointer(*claudeRequest.MaxTokens)
	}
	if claudeRequest.TopP != nil {
		openAIRequest.TopP = kitutil.GetPointer(*claudeRequest.TopP)
	}
	if claudeRequest.TopK != nil {
		openAIRequest.TopK = kitutil.GetPointer(*claudeRequest.TopK)
	}
	if claudeRequest.Stream != nil {
		openAIRequest.Stream = kitutil.GetPointer(*claudeRequest.Stream)
	}
	reasoningIntent, effectiveEffort, err := claudeRequestReasoningIntent(ctx, &claudeRequest, info)
	if err != nil {
		return nil, reasoning.AsClientError(err)
	}

	isOpenRouter := convmeta.OptionsOf(info).OpenRouterDialect
	if isOpenRouter {
		if effort := claudeRequest.GetEfforts(); effort != "" {
			effortBytes, _ := kitutil.Marshal(effort)
			openAIRequest.Verbosity = effortBytes
		}
		if !reasoningIntent.IsEmpty() {
			var reasoningConfig openRouterRequestReasoning
			disabled := reasoningIntent.Mode == reasoning.ModeDisabled || reasoningIntent.Effort == reasoning.EffortNone
			enabled := !disabled
			reasoningConfig.Enabled = &enabled
			if enabled && reasoningIntent.BudgetTokens != nil && reasoningIntent.Mode != reasoning.ModeAdaptive {
				reasoningConfig = openRouterRequestReasoning{
					Enabled:   &enabled,
					MaxTokens: *reasoningIntent.BudgetTokens,
				}
			} else if enabled {
				reasoningConfig.Effort = string(reasoning.EffectiveEffort(reasoningIntent))
			}
			if reasoningIntent.IncludeThoughts != nil {
				reasoningConfig.Exclude = !*reasoningIntent.IncludeThoughts
			}
			reasoningJSON, err := kitutil.Marshal(reasoningConfig)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal reasoning: %w", err)
			}
			openAIRequest.Reasoning = reasoningJSON
		}
	} else {
		if err := reasoning.ApplyToOpenAIChat(&openAIRequest, reasoningIntent); err != nil {
			return nil, reasoning.AsClientError(err)
		}
		if info != nil {
			// Keep the outgoing -thinking suffix so a cascaded downstream
			// new-api can recover reasoning intent from the model name. This
			// is an emission-side policy, not converter-side suffix parsing.
			thinkingSuffix := "-thinking"
			if strings.HasSuffix(info.GetOriginModelName(), thinkingSuffix) &&
				!strings.HasSuffix(openAIRequest.Model, thinkingSuffix) {
				openAIRequest.Model = openAIRequest.Model + thinkingSuffix
			}
		}
	}
	if info != nil && effectiveEffort != "" {
		info.SetReasoningEffort(string(effectiveEffort))
	}

	if len(claudeRequest.StopSequences) == 1 {
		openAIRequest.Stop = claudeRequest.StopSequences[0]
	} else if len(claudeRequest.StopSequences) > 1 {
		openAIRequest.Stop = claudeRequest.StopSequences
	}

	tools, _ := kitutil.Any2Type[[]dto.Tool](claudeRequest.Tools)
	openAITools := make([]dto.ToolCallRequest, 0)
	for _, claudeTool := range tools {
		openAITool := dto.ToolCallRequest{
			Type: "function",
			Function: dto.FunctionRequest{
				Name:        claudeTool.Name,
				Description: claudeTool.Description,
				Parameters:  claudeTool.InputSchema,
			},
		}
		openAITools = append(openAITools, openAITool)
	}
	openAIRequest.Tools = openAITools

	openAIMessages := make([]dto.Message, 0)
	if claudeRequest.System != nil {
		if claudeRequest.IsStringSystem() && claudeRequest.GetStringSystem() != "" {
			openAIMessage := dto.Message{
				Role: "system",
			}
			openAIMessage.SetStringContent(claudeRequest.GetStringSystem())
			openAIMessages = append(openAIMessages, openAIMessage)
		} else {
			systems := claudeRequest.ParseSystem()
			if len(systems) > 0 {
				openAIMessage := dto.Message{
					Role: "system",
				}
				isOpenRouterClaude := isOpenRouter && strings.HasPrefix(convmeta.UpstreamModelName(info), "anthropic/claude")
				if isOpenRouterClaude {
					systemMediaMessages := make([]dto.MediaContent, 0, len(systems))
					for _, system := range systems {
						message := dto.MediaContent{
							Type:         "text",
							Text:         system.GetText(),
							CacheControl: system.CacheControl,
						}
						systemMediaMessages = append(systemMediaMessages, message)
					}
					openAIMessage.SetMediaContent(systemMediaMessages)
				} else {
					var systemStr strings.Builder
					for _, system := range systems {
						if system.Text != nil {
							systemStr.WriteString(*system.Text)
						}
					}
					openAIMessage.SetStringContent(systemStr.String())
				}
				openAIMessages = append(openAIMessages, openAIMessage)
			}
		}
	}

	type unnamedToolResult struct {
		index int
		id    string
	}
	toolNames := make(map[string]string)
	var unnamedToolResults []unnamedToolResult
	for _, claudeMessage := range claudeRequest.Messages {
		openAIMessage := dto.Message{
			Role: claudeMessage.Role,
		}
		if claudeMessage.IsStringContent() {
			openAIMessage.SetStringContent(claudeMessage.GetStringContent())
		} else {
			content, err := claudeMessage.ParseContent()
			if err != nil {
				return nil, err
			}
			var toolCalls []dto.ToolCallRequest
			mediaMessages := make([]dto.MediaContent, 0, len(content))

			for _, mediaMsg := range content {
				if _, exists := toolNames[mediaMsg.Id]; !exists {
					toolNames[mediaMsg.Id] = mediaMsg.Name
				}
				switch mediaMsg.Type {
				case "text", "input_text":
					message := dto.MediaContent{
						Type:         "text",
						Text:         mediaMsg.GetText(),
						CacheControl: mediaMsg.CacheControl,
					}
					mediaMessages = append(mediaMessages, message)
				case "image":
					imageData := fmt.Sprintf("data:%s;base64,%s", mediaMsg.Source.MediaType, mediaMsg.Source.Data)
					mediaMessage := dto.MediaContent{
						Type:     "image_url",
						ImageUrl: &dto.MessageImageUrl{Url: imageData},
					}
					mediaMessages = append(mediaMessages, mediaMessage)
				case "tool_use":
					toolCall := dto.ToolCallRequest{
						ID:   mediaMsg.Id,
						Type: "function",
						Function: dto.FunctionRequest{
							Name:      mediaMsg.Name,
							Arguments: requestToJSONString(mediaMsg.Input),
						},
					}
					toolCalls = append(toolCalls, toolCall)
				case "tool_result":
					toolName := mediaMsg.Name
					if toolName == "" {
						unnamedToolResults = append(unnamedToolResults, unnamedToolResult{index: len(openAIMessages), id: mediaMsg.ToolUseId})
					}
					oaiToolMessage := dto.Message{
						Role:       "tool",
						Name:       &toolName,
						ToolCallId: mediaMsg.ToolUseId,
					}
					if mediaMsg.IsStringContent() {
						oaiToolMessage.SetStringContent(mediaMsg.GetStringContent())
					} else {
						content, media := claudeToolResultToChat(mediaMsg.ParseMediaContent())
						oaiToolMessage.SetStringContent(content)
						// A Chat tool message only carries text. Images from the tool result
						// join the user message this Claude message becomes, which lands right
						// after the tool batch and keeps the tool messages contiguous.
						mediaMessages = append(mediaMessages, media...)
					}
					openAIMessages = append(openAIMessages, oaiToolMessage)
				}
			}

			if len(toolCalls) > 0 {
				openAIMessage.SetToolCalls(toolCalls)
			}
			if len(mediaMessages) > 0 && len(toolCalls) == 0 {
				openAIMessage.SetMediaContent(mediaMessages)
			}
		}
		if len(openAIMessage.ParseContent()) > 0 || len(openAIMessage.ToolCalls) > 0 {
			openAIMessages = append(openAIMessages, openAIMessage)
		}
	}
	for _, result := range unnamedToolResults {
		*openAIMessages[result.index].Name = toolNames[result.id]
	}

	openAIRequest.Messages = openAIMessages
	return &openAIRequest, nil
}

// claudeToolResultToChat maps a structured tool_result content array onto Chat Completions,
// where a tool message may only carry text. Text blocks stay on the tool message and image
// blocks come back in Chat shape for the caller to place on the following user message;
// stringifying them instead would hand base64 image data to the upstream text tokenizer.
// Arrays holding any other block type keep the historical stringified form so nothing is lost.
func claudeToolResultToChat(blocks []dto.ClaudeMediaMessage) (string, []dto.MediaContent) {
	texts := make([]string, 0, len(blocks))
	media := make([]dto.MediaContent, 0, len(blocks))
	for _, block := range blocks {
		switch {
		case block.Type == "text" || block.Type == "input_text":
			if text := block.GetText(); text != "" {
				texts = append(texts, text)
			}
		case block.Type == "image" && block.Source != nil:
			url := block.Source.Url
			if url == "" {
				url = fmt.Sprintf("data:%s;base64,%s", block.Source.MediaType, kitutil.Interface2String(block.Source.Data))
			}
			media = append(media, dto.MediaContent{Type: "image_url", ImageUrl: &dto.MessageImageUrl{Url: url}})
		default:
			return requestToJSONString(blocks), nil
		}
	}
	switch {
	case len(texts) == 0 && len(media) == 0:
		return requestToJSONString(blocks), nil
	case len(texts) == 0:
		// Upstreams reject empty tool content; the images ride on the following user message.
		return "[image]", media
	default:
		return strings.Join(texts, "\n"), media
	}
}

func requestToJSONString(v any) string {
	b, err := kitutil.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
