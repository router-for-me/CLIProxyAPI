package oaichat

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/reasonmap"
	"github.com/QuantumNous/new-api/relaykit/relayconvert/convmeta"
	sharedclaude "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/shared/claude"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

func generateStopBlock(index int) *dto.ClaudeResponse {
	return &dto.ClaudeResponse{
		Type:  "content_block_stop",
		Index: kitutil.GetPointer[int](index),
	}
}

func stopOpenBlocks(state *convmeta.ClaudeConvertInfo) []*dto.ClaudeResponse {
	if state == nil {
		return nil
	}
	switch state.LastMessagesType {
	case convmeta.LastMessageTypeText, convmeta.LastMessageTypeThinking:
		return []*dto.ClaudeResponse{generateStopBlock(state.Index)}
	case convmeta.LastMessageTypeTools:
		if len(state.ToolCalls) == 0 {
			responses := make([]*dto.ClaudeResponse, 0, state.ToolCallMaxIndexOffset+1)
			for offset := 0; offset <= state.ToolCallMaxIndexOffset; offset++ {
				responses = append(responses, generateStopBlock(state.ToolCallBaseIndex+offset))
			}
			return responses
		}
		responses := make([]*dto.ClaudeResponse, 0, len(state.ToolCalls))
		for _, tool := range state.ToolCalls {
			if tool != nil && tool.Started {
				responses = append(responses, generateStopBlock(tool.BlockIndex))
			}
		}
		return responses
	default:
		return nil
	}
}

func startPendingToolBlocks(state *convmeta.ClaudeConvertInfo) []*dto.ClaudeResponse {
	if state == nil || state.LastMessagesType != convmeta.LastMessageTypeTools {
		return nil
	}
	responses := make([]*dto.ClaudeResponse, 0)
	for _, tool := range state.ToolCalls {
		if tool == nil || tool.Started || tool.Name == "" {
			continue
		}
		if tool.ID == "" {
			tool.ID = fmt.Sprintf("toolu_%s", kitutil.GetUUID())
		}
		idx := tool.BlockIndex
		responses = append(responses, &dto.ClaudeResponse{
			Index: &idx,
			Type:  "content_block_start",
			ContentBlock: &dto.ClaudeMediaMessage{
				Id:    tool.ID,
				Type:  "tool_use",
				Name:  tool.Name,
				Input: map[string]any{},
			},
		})
		tool.Started = true
		if tool.PendingArguments != "" {
			arguments := tool.PendingArguments
			responses = append(responses, &dto.ClaudeResponse{
				Index: &idx,
				Type:  "content_block_delta",
				Delta: &dto.ClaudeMediaMessage{
					Type:        "input_json_delta",
					PartialJson: &arguments,
				},
			})
			tool.PendingArguments = ""
		}
	}
	return responses
}

func buildClaudeUsageFromOpenAIUsage(oaiUsage *dto.Usage) *dto.ClaudeUsage {
	return sharedclaude.UsageFromOpenAI(oaiUsage)
}

func clientVisibleClaudeStreamUsage(state *convmeta.ClaudeConvertInfo, incoming *dto.Usage) *dto.ClaudeUsage {
	prior := buildClaudeUsageFromOpenAIUsage(state.Usage)
	converted := buildClaudeUsageFromOpenAIUsage(incoming)
	if incoming != nil {
		state.Usage = dto.MergeUsageNonZero(state.Usage, incoming)
	}
	if prior == nil {
		return converted
	}
	if converted == nil {
		return prior
	}
	return dto.MergeClaudeUsageNonZero(prior, converted)
}

func NormalizeCacheCreationSplit(totalTokens int, tokens5m int, tokens1h int) (int, int) {
	return sharedclaude.NormalizeCacheCreationSplit(totalTokens, tokens5m, tokens1h)
}

func StreamResponseOpenAI2Claude(openAIResponse *dto.ChatCompletionsStreamResponse, info convmeta.Meta) []*dto.ClaudeResponse {
	if info == nil {
		info = &convmeta.Values{}
	}
	state := info.EnsureClaudeConvertInfo()
	if state.Done {
		return nil
	}

	var claudeResponses []*dto.ClaudeResponse
	// stopOpenBlocks emits the required content_block_stop event(s) for the currently open block(s)
	// according to Anthropic's SSE streaming state machine:
	// content_block_start -> content_block_delta* -> content_block_stop (per index).
	//
	// For text/thinking, there is at most one open block at state.Index.
	// For tools, OpenAI tool_calls can stream multiple parallel tool_use blocks (indexed from 0),
	// so we may have multiple open blocks and must stop each one explicitly.
	appendStopOpenBlocks := func() {
		claudeResponses = append(claudeResponses, startPendingToolBlocks(state)...)
		claudeResponses = append(claudeResponses, stopOpenBlocks(state)...)
	}
	// stopOpenBlocksAndAdvance closes the currently open block(s) and advances the content block index
	// to the next available slot for subsequent content_block_start events.
	//
	// This prevents invalid streams where a content_block_delta (e.g. thinking_delta) is emitted for an
	// index whose active content_block type is different (the typical cause of "Mismatched content block type").
	stopOpenBlocksAndAdvance := func() {
		if state.LastMessagesType == convmeta.LastMessageTypeNone {
			return
		}
		appendStopOpenBlocks()
		switch state.LastMessagesType {
		case convmeta.LastMessageTypeTools:
			state.Index = state.ToolCallBaseIndex + len(state.ToolCalls)
			state.ToolCallBaseIndex = 0
			state.ToolCallMaxIndexOffset = 0
			state.ToolCalls = nil
			state.ToolCallByIndex = nil
			state.ToolCallByID = nil
		default:
			state.Index++
		}
		state.LastMessagesType = convmeta.LastMessageTypeNone
	}
	appendCitationDeltas := func(raw []byte) {
		citations := chatAnnotationsToClaude(raw, "")
		if len(citations) == 0 {
			return
		}
		if state.LastMessagesType != convmeta.LastMessageTypeText {
			stopOpenBlocksAndAdvance()
			idx := state.Index
			claudeResponses = append(claudeResponses, &dto.ClaudeResponse{
				Index: &idx,
				Type:  "content_block_start",
				ContentBlock: &dto.ClaudeMediaMessage{
					Type: "text",
					Text: kitutil.GetPointer[string](""),
				},
			})
			state.LastMessagesType = convmeta.LastMessageTypeText
		}
		for _, citation := range citations {
			idx := state.Index
			claudeResponses = append(claudeResponses, &dto.ClaudeResponse{
				Index: &idx,
				Type:  "content_block_delta",
				Delta: &dto.ClaudeMediaMessage{
					Type:     "citations_delta",
					Citation: citation,
				},
			})
		}
	}
	if info.GetSendResponseCount() == 1 {
		// Client-visible Claude stream usage matches billing merge: first
		// frame records first, a later non-zero field overrides, and a later
		// zero/missing field never erases a first-frame positive. Anthropic
		// clients treat message_delta as authoritative for the fields it
		// carries, including a corrected input_tokens.
		startUsage := &dto.ClaudeUsage{
			InputTokens:  info.GetEstimatePromptTokens(),
			OutputTokens: 0,
		}
		if openAIResponse.Usage != nil && dto.HasOpenAIUsageTokens(openAIResponse.Usage) {
			if real := buildClaudeUsageFromOpenAIUsage(openAIResponse.Usage); real != nil {
				startUsage = real
			}
			state.Usage = dto.MergeUsageNonZero(state.Usage, openAIResponse.Usage)
		}
		msg := &dto.ClaudeMediaMessage{
			Id:    openAIResponse.Id,
			Model: openAIResponse.Model,
			Type:  "message",
			Role:  "assistant",
			Usage: startUsage,
		}
		msg.SetContent(make([]any, 0))
		claudeResponses = append(claudeResponses, &dto.ClaudeResponse{
			Type:    "message_start",
			Message: msg,
		})
	}

	if len(openAIResponse.Choices) == 0 {
		// Some OpenAI-compatible upstreams end with a usage-only SSE chunk.
		oaiUsage := clientVisibleClaudeStreamUsage(state, openAIResponse.Usage)
		if oaiUsage != nil {
			appendStopOpenBlocks()
			stopReason := stopReasonOpenAI2Claude(state.FinishReason)
			if stopReason == "" {
				stopReason = "end_turn"
			}
			claudeResponses = append(claudeResponses, &dto.ClaudeResponse{
				Type:  "message_delta",
				Usage: oaiUsage,
				Delta: &dto.ClaudeMediaMessage{
					StopReason: kitutil.GetPointer[string](stopReason),
				},
			})
			claudeResponses = append(claudeResponses, &dto.ClaudeResponse{
				Type: "message_stop",
			})
			state.Done = true
		}
		return claudeResponses
	} else {
		chosenChoice := openAIResponse.Choices[0]
		doneChunk := chosenChoice.FinishReason != nil && *chosenChoice.FinishReason != ""
		if doneChunk {
			state.FinishReason = *chosenChoice.FinishReason
		}

		var claudeResponse dto.ClaudeResponse
		var isEmpty bool
		claudeResponse.Type = "content_block_delta"
		if len(chosenChoice.Delta.ToolCalls) > 0 {
			toolCalls := chosenChoice.Delta.ToolCalls
			if state.LastMessagesType != convmeta.LastMessageTypeTools {
				stopOpenBlocksAndAdvance()
				state.ToolCallBaseIndex = state.Index
				state.ToolCallMaxIndexOffset = 0
				state.ToolCalls = nil
				state.ToolCallByIndex = make(map[int]*convmeta.ClaudeStreamToolCall)
				state.ToolCallByID = make(map[string]*convmeta.ClaudeStreamToolCall)
			}
			state.LastMessagesType = convmeta.LastMessageTypeTools
			if state.ToolCallByIndex == nil {
				state.ToolCallByIndex = make(map[int]*convmeta.ClaudeStreamToolCall)
			}
			if state.ToolCallByID == nil {
				state.ToolCallByID = make(map[string]*convmeta.ClaudeStreamToolCall)
			}
			for i, toolCall := range toolCalls {
				toolIndex := i
				if toolCall.Index != nil {
					toolIndex = *toolCall.Index
				}
				incomingID := strings.TrimSpace(toolCall.ID)
				var tool *convmeta.ClaudeStreamToolCall
				if incomingID != "" {
					tool = state.ToolCallByID[incomingID]
				}
				if tool == nil {
					tool = state.ToolCallByIndex[toolIndex]
				}
				if tool != nil && incomingID != "" && tool.ID != "" && tool.ID != incomingID {
					tool = nil
				}
				if tool == nil {
					tool = &convmeta.ClaudeStreamToolCall{
						BlockIndex: state.ToolCallBaseIndex + len(state.ToolCalls),
					}
					state.ToolCalls = append(state.ToolCalls, tool)
				}
				state.ToolCallByIndex[toolIndex] = tool
				if tool.ID == "" && incomingID != "" {
					tool.ID = incomingID
					state.ToolCallByID[incomingID] = tool
				}
				if tool.Name == "" && strings.TrimSpace(toolCall.Function.Name) != "" {
					tool.Name = strings.TrimSpace(toolCall.Function.Name)
				}
				if !tool.Started {
					tool.PendingArguments += toolCall.Function.Arguments
				}

				idx := tool.BlockIndex
				if !tool.Started && tool.ID != "" && tool.Name != "" {
					claudeResponses = append(claudeResponses, &dto.ClaudeResponse{
						Index: &idx,
						Type:  "content_block_start",
						ContentBlock: &dto.ClaudeMediaMessage{
							Id:    tool.ID,
							Type:  "tool_use",
							Name:  tool.Name,
							Input: map[string]any{},
						},
					})
					tool.Started = true
					if tool.PendingArguments != "" {
						arguments := tool.PendingArguments
						claudeResponses = append(claudeResponses, &dto.ClaudeResponse{
							Index: &idx,
							Type:  "content_block_delta",
							Delta: &dto.ClaudeMediaMessage{
								Type:        "input_json_delta",
								PartialJson: &arguments,
							},
						})
						tool.PendingArguments = ""
					}
					continue
				}

				if tool.Started && toolCall.Function.Arguments != "" {
					claudeResponses = append(claudeResponses, &dto.ClaudeResponse{
						Index: &idx,
						Type:  "content_block_delta",
						Delta: &dto.ClaudeMediaMessage{
							Type:        "input_json_delta",
							PartialJson: &toolCall.Function.Arguments,
						},
					})
				}
			}
			state.ToolCallMaxIndexOffset = len(state.ToolCalls) - 1
			if len(state.ToolCalls) > 0 {
				state.Index = state.ToolCallBaseIndex + len(state.ToolCalls) - 1
			}
		} else {
			reasoning := chosenChoice.Delta.GetReasoningContent()
			textContent := chosenChoice.Delta.GetContentString()
			if reasoning != "" || textContent != "" {
				if reasoning != "" {
					if state.LastMessagesType != convmeta.LastMessageTypeThinking {
						stopOpenBlocksAndAdvance()
						idx := state.Index
						claudeResponses = append(claudeResponses, &dto.ClaudeResponse{
							Index: &idx,
							Type:  "content_block_start",
							ContentBlock: &dto.ClaudeMediaMessage{
								Type:     "thinking",
								Thinking: kitutil.GetPointer[string](""),
							},
						})
					}
					state.LastMessagesType = convmeta.LastMessageTypeThinking
					claudeResponse.Delta = &dto.ClaudeMediaMessage{
						Type:     "thinking_delta",
						Thinking: &reasoning,
					}
				} else {
					if state.LastMessagesType != convmeta.LastMessageTypeText {
						stopOpenBlocksAndAdvance()
						idx := state.Index
						claudeResponses = append(claudeResponses, &dto.ClaudeResponse{
							Index: &idx,
							Type:  "content_block_start",
							ContentBlock: &dto.ClaudeMediaMessage{
								Type: "text",
								Text: kitutil.GetPointer[string](""),
							},
						})
					}
					state.LastMessagesType = convmeta.LastMessageTypeText
					claudeResponse.Delta = &dto.ClaudeMediaMessage{
						Type: "text_delta",
						Text: kitutil.GetPointer[string](textContent),
					}
				}
			} else {
				isEmpty = true
			}
		}

		claudeResponse.Index = kitutil.GetPointer[int](state.Index)
		if !isEmpty && claudeResponse.Delta != nil {
			claudeResponses = append(claudeResponses, &claudeResponse)
		}
		appendCitationDeltas(chosenChoice.Delta.Annotations)

		if doneChunk || state.Done {
			oaiUsage := clientVisibleClaudeStreamUsage(state, openAIResponse.Usage)
			if oaiUsage == nil {
				// Some upstreams emit finish_reason first, then send a final usage-only chunk.
				// Keep content blocks open until usage is available so the terminal message_delta
				// can carry both usage and the final stop reason.
				return claudeResponses
			}
			appendStopOpenBlocks()
			claudeResponses = append(claudeResponses, &dto.ClaudeResponse{
				Type:  "message_delta",
				Usage: oaiUsage,
				Delta: &dto.ClaudeMediaMessage{
					StopReason: kitutil.GetPointer[string](stopReasonOpenAI2Claude(state.FinishReason)),
				},
			})
			claudeResponses = append(claudeResponses, &dto.ClaudeResponse{
				Type: "message_stop",
			})
			state.Done = true
			return claudeResponses
		}
	}

	return claudeResponses
}

func FinalizeStreamResponseOpenAI2Claude(info convmeta.Meta) []*dto.ClaudeResponse {
	if info == nil {
		info = &convmeta.Values{}
	}
	state := info.EnsureClaudeConvertInfo()
	if state.Done {
		return nil
	}

	stopReason := stopReasonOpenAI2Claude(state.FinishReason)
	if stopReason == "" {
		stopReason = "end_turn"
	}
	responses := startPendingToolBlocks(state)
	responses = append(responses, stopOpenBlocks(state)...)
	responses = append(responses,
		&dto.ClaudeResponse{
			Type:  "message_delta",
			Usage: clientVisibleClaudeStreamUsage(state, nil),
			Delta: &dto.ClaudeMediaMessage{
				StopReason: kitutil.GetPointer[string](stopReason),
			},
		},
		&dto.ClaudeResponse{Type: "message_stop"},
	)
	state.Done = true
	return responses
}

func ResponseOpenAI2Claude(openAIResponse *dto.OpenAITextResponse, info convmeta.Meta) *dto.ClaudeResponse {
	var stopReason string
	contents := make([]dto.ClaudeMediaMessage, 0)
	claudeResponse := &dto.ClaudeResponse{
		Id:    openAIResponse.Id,
		Type:  "message",
		Role:  "assistant",
		Model: openAIResponse.Model,
	}
	for _, choice := range openAIResponse.Choices {
		stopReason = stopReasonOpenAI2Claude(choice.FinishReason)
		reasoningContent := choice.Message.GetReasoningContent()
		textContent := choice.Message.StringContent()
		toolCalls := choice.Message.ParseToolCalls()
		if reasoningContent != "" {
			claudeContent := dto.ClaudeMediaMessage{Type: "thinking"}
			claudeContent.Thinking = kitutil.GetPointer(reasoningContent)
			contents = append(contents, claudeContent)
		}
		if textContent != "" || (reasoningContent == "" && len(toolCalls) == 0) {
			claudeContent := dto.ClaudeMediaMessage{}
			claudeContent.Type = "text"
			claudeContent.SetText(textContent)
			citations := chatAnnotationsToClaude(choice.Message.Annotations, textContent)
			if len(citations) > 0 {
				claudeContent.Citations, _ = kitutil.Marshal(citations)
			}
			contents = append(contents, claudeContent)
		}
		for _, toolUse := range toolCalls {
			claudeContent := dto.ClaudeMediaMessage{}
			claudeContent.Type = "tool_use"
			claudeContent.Id = toolUse.ID
			claudeContent.Name = toolUse.Function.Name
			mapParams := map[string]any{}
			if strings.TrimSpace(toolUse.Function.Arguments) != "" {
				var parsed map[string]any
				if err := kitutil.Unmarshal([]byte(toolUse.Function.Arguments), &parsed); err == nil && parsed != nil {
					mapParams = parsed
				}
			}
			claudeContent.Input = mapParams
			contents = append(contents, claudeContent)
		}
	}
	claudeResponse.Content = contents
	claudeResponse.StopReason = stopReason
	claudeResponse.Usage = buildClaudeUsageFromOpenAIUsage(&openAIResponse.Usage)

	return claudeResponse
}

func stopReasonOpenAI2Claude(reason string) string {
	return reasonmap.OpenAIFinishReasonToClaudeStopReason(reason)
}
