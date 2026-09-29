package toolconv

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/relaykit/dto"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// InspectResponse reports protocol information that the current response
// converters cannot faithfully express. It keeps loss handling centralized so
// direct and multi-step routes behave consistently.
func InspectResponse(from types.RelayFormat, to types.RelayFormat, response any) []types.ConversionDiagnostic {
	if from == to {
		return nil
	}
	var diagnostics []types.ConversionDiagnostic
	switch value := response.(type) {
	case *dto.ClaudeResponse:
		diagnostics = inspectClaudeResponse(value, to)
	case dto.ClaudeResponse:
		diagnostics = inspectClaudeResponse(&value, to)
	case *dto.OpenAIResponsesResponse:
		diagnostics = inspectOpenAIResponsesResponse(value, to)
	case dto.OpenAIResponsesResponse:
		diagnostics = inspectOpenAIResponsesResponse(&value, to)
	case *dto.ResponsesStreamResponse:
		diagnostics = inspectOpenAIResponsesStreamResponse(value)
	case dto.ResponsesStreamResponse:
		diagnostics = inspectOpenAIResponsesStreamResponse(&value)
	case *dto.GeminiChatResponse:
		diagnostics = inspectGeminiResponse(value, to)
	case dto.GeminiChatResponse:
		diagnostics = inspectGeminiResponse(&value, to)
	}
	for index := range diagnostics {
		diagnostics[index].From = from
		diagnostics[index].To = to
	}
	return diagnostics
}

// InspectStreamResponse avoids treating a single Gemini streaming chunk as a
// complete grounding document. Gemini grounding chunk indexes and segment
// offsets are cumulative across the stream; the stateful converter validates
// and resolves them after accumulating prior chunks.
func InspectStreamResponse(from types.RelayFormat, to types.RelayFormat, response any) []types.ConversionDiagnostic {
	if from != types.RelayFormatGemini {
		return InspectResponse(from, to, response)
	}
	var value *dto.GeminiChatResponse
	switch response := response.(type) {
	case *dto.GeminiChatResponse:
		value = response
	case dto.GeminiChatResponse:
		value = &response
	default:
		return InspectResponse(from, to, response)
	}
	if value == nil {
		return nil
	}
	var diagnostics []types.ConversionDiagnostic
	for index := range value.Candidates {
		metadata := value.Candidates[index].GroundingMetadata
		if metadata == nil {
			continue
		}
		if len(metadata.WebSearchQueries) > 0 && to != types.RelayFormatOpenAIResponses {
			diagnostics = append(diagnostics, responseSemanticLoss(
				fmt.Sprintf("candidates[%d].groundingMetadata.webSearchQueries", index),
				"web_search_call_unrepresentable",
				"Gemini grounding confirms a hosted web search, but the target stream converter cannot produce an OpenAI Responses web_search_call lifecycle",
			))
		}
		if len(metadata.WebSearchQueries) == 0 && len(metadata.RetrievalQueries) == 0 && len(metadata.SearchEntryPoint) == 0 && len(metadata.RetrievalMetadata) == 0 && len(metadata.SourceFlaggingUris) == 0 && metadata.GoogleMapsWidgetContextToken == "" {
			continue
		}
		diagnostics = append(diagnostics, responsePresentationLoss(
			fmt.Sprintf("candidates[%d].groundingMetadata", index),
			"hosted_tool_metadata_reduced",
			"Gemini grounding citations are preserved across stream chunks, but provider-specific search metadata has no target-protocol equivalent",
		))
	}
	for index := range diagnostics {
		diagnostics[index].From = from
		diagnostics[index].To = to
	}
	return diagnostics
}

func inspectClaudeResponse(response *dto.ClaudeResponse, to types.RelayFormat) []types.ConversionDiagnostic {
	if response == nil {
		return nil
	}
	var diagnostics []types.ConversionDiagnostic
	if to != types.RelayFormatOpenAIResponses && (response.StopReason == "pause_turn" || response.Delta != nil && response.Delta.StopReason != nil && *response.Delta.StopReason == "pause_turn") {
		diagnostics = append(diagnostics, responseSemanticLoss(
			"stop_reason",
			"continuation_state_lost",
			"Claude pause_turn requires protocol-native continuation state that the target response cannot preserve",
		))
	}
	for index := range response.Content {
		diagnostics = append(diagnostics, inspectClaudeContentBlock(&response.Content[index], fmt.Sprintf("content[%d]", index), to, false)...)
	}
	if response.ContentBlock != nil {
		diagnostics = append(diagnostics, inspectClaudeContentBlock(response.ContentBlock, "content_block", to, true)...)
	}
	return diagnostics
}

func inspectClaudeContentBlock(block *dto.ClaudeMediaMessage, path string, to types.RelayFormat, stream bool) []types.ConversionDiagnostic {
	if block == nil {
		return nil
	}
	blockType := strings.TrimSpace(block.Type)
	var diagnostics []types.ConversionDiagnostic
	if isClaudeHostedToolBlock(blockType) {
		kind := KindNative
		if blockType == "server_tool_use" || blockType == "mcp_tool_use" {
			kind = hostedKindFromClaudeCall(blockType, block.Name)
		} else {
			kind = hostedKindFromClaudeResult(blockType)
		}
		if to != types.RelayFormatOpenAIResponses || kind != KindWebSearch && kind != KindMCP {
			diagnostics = append(diagnostics, responseSemanticLoss(
				path,
				"hosted_tool_unrepresentable",
				fmt.Sprintf("%s cannot losslessly represent Claude hosted-tool response block %q", to, blockType),
			))
		} else if blockType == "server_tool_use" || blockType == "mcp_tool_use" {
			if block.Id == "" {
				diagnostics = append(diagnostics, responseSemanticLoss(
					path+".id",
					"hosted_tool_id_missing",
					"Claude hosted-tool call has no id for pairing it with its result",
				))
			}
			if kind == KindMCP && (block.Name == "" || block.ServerName == "") {
				diagnostics = append(diagnostics, responseSemanticLoss(
					path,
					"mcp_identity_missing",
					"Claude MCP tool use must include both name and server_name for Responses MCP mapping",
				))
			}
			if rawJSONPresent(block.Caller) {
				diagnostics = append(diagnostics, responseSemanticLoss(
					path+".caller",
					"hosted_tool_caller_unrepresentable",
					"OpenAI Responses web_search_call and mcp_call items cannot preserve Claude's hosted-tool caller provenance",
				))
			}
			if !stream {
				input, err := kitutil.Marshal(block.Input)
				if err != nil {
					diagnostics = append(diagnostics, responseSemanticLoss(
						path+".input",
						"hosted_tool_input_invalid",
						err.Error(),
					))
				} else if kind == KindMCP {
					if _, err := responsesMCPArgumentsFromClaude(input); err != nil {
						diagnostics = append(diagnostics, responseSemanticLoss(
							path+".input",
							"mcp_arguments_unrepresentable",
							err.Error(),
						))
					}
				} else if kind == KindWebSearch {
					if _, err := dto.NormalizeResponsesWebSearchAction(input); err != nil {
						diagnostics = append(diagnostics, responseSemanticLoss(
							path+".input",
							"web_search_action_unrepresentable",
							err.Error(),
						))
					}
				}
			}
		} else if block.ToolUseId == "" {
			diagnostics = append(diagnostics, responseSemanticLoss(
				path+".tool_use_id",
				"hosted_tool_id_missing",
				"Claude hosted-tool result has no tool_use_id for pairing it with its call",
			))
		}
		if kind == KindMCP && blockType == "mcp_tool_result" {
			content, err := kitutil.Marshal(block.Content)
			if err != nil {
				diagnostics = append(diagnostics, responseSemanticLoss(path+".content", "mcp_result_unrepresentable", err.Error()))
			} else {
				failed, errorCode := claudeHostedResultFailure(blockType, content, block.IsError, block.ErrorCode)
				var normalized bool
				if failed {
					_, normalized, err = responsesMCPErrorFromClaudeContent(content, errorCode)
				} else {
					_, normalized, err = responsesMCPStringFromClaudeContent(content)
				}
				if err != nil {
					diagnostics = append(diagnostics, responseSemanticLoss(path+".content", "mcp_result_unrepresentable", err.Error()))
				} else if normalized {
					diagnostics = append(diagnostics, responsePresentationLoss(
						path+".content",
						"mcp_text_result_normalized",
						"Claude's single MCP text block is normalized to a Responses output string",
					))
				}
			}
		}
	}
	if blockType == "redacted_thinking" && block.Data != "" {
		diagnostics = append(diagnostics, responseSemanticLoss(
			path+".data",
			"continuation_state_lost",
			"Claude encrypted thinking state cannot be represented by the target response",
		))
	}
	return diagnostics
}

func isClaudeHostedToolBlock(blockType string) bool {
	if blockType == "server_tool_use" || blockType == "mcp_tool_use" || blockType == "mcp_tool_result" {
		return true
	}
	return strings.HasSuffix(blockType, "_tool_result")
}

func inspectOpenAIResponsesResponse(response *dto.OpenAIResponsesResponse, to types.RelayFormat) []types.ConversionDiagnostic {
	if response == nil {
		return nil
	}
	var diagnostics []types.ConversionDiagnostic
	for index := range response.Output {
		output := &response.Output[index]
		if !isResponsesHostedOutput(output.Type) {
			continue
		}
		kind := hostedKindFromResponsesType(output.Type)
		if to != types.RelayFormatClaude || kind != KindWebSearch && kind != KindMCP {
			diagnostics = append(diagnostics, responseSemanticLoss(
				fmt.Sprintf("output[%d]", index),
				"hosted_tool_unrepresentable",
				fmt.Sprintf("%s cannot losslessly represent OpenAI Responses hosted-tool output %q", to, output.Type),
			))
			continue
		}
		if output.ID == "" {
			diagnostics = append(diagnostics, responseSemanticLoss(
				fmt.Sprintf("output[%d].id", index),
				"hosted_tool_id_missing",
				"hosted-tool output has no id for pairing the call with its result",
			))
		}
		if kind == KindMCP {
			if output.Name == "" || output.ServerLabel == "" {
				diagnostics = append(diagnostics, responseSemanticLoss(
					fmt.Sprintf("output[%d]", index),
					"mcp_identity_missing",
					"Responses MCP output must include both name and server_label for Claude MCP mapping",
				))
			}
			if output.ApprovalRequestID != "" {
				diagnostics = append(diagnostics, responseSemanticLoss(
					fmt.Sprintf("output[%d].approval_request_id", index),
					"mcp_approval_state_unrepresentable",
					"Claude MCP response blocks cannot preserve a Responses approval_request_id",
				))
			}
			if _, err := claudeMCPInputFromResponses(output.Arguments); err != nil {
				diagnostics = append(diagnostics, responseSemanticLoss(
					fmt.Sprintf("output[%d].arguments", index),
					"mcp_arguments_unrepresentable",
					err.Error(),
				))
			}
			if rawJSONPresent(output.Output) && rawJSONPresent(output.ItemError) {
				diagnostics = append(diagnostics, responseSemanticLoss(
					fmt.Sprintf("output[%d]", index),
					"mcp_result_ambiguous",
					"Responses MCP output contains both output and error",
				))
			}
			for _, field := range []struct {
				name string
				raw  json.RawMessage
			}{{name: "output", raw: output.Output}, {name: "error", raw: output.ItemError}} {
				if !rawJSONPresent(field.raw) {
					continue
				}
				if _, err := claudeMCPContentFromResponsesString(field.raw); err != nil {
					diagnostics = append(diagnostics, responseSemanticLoss(
						fmt.Sprintf("output[%d].%s", index, field.name),
						"mcp_result_unrepresentable",
						err.Error(),
					))
				}
			}
		} else if kind == KindWebSearch {
			if _, err := claudeWebSearchInputFromResponses(output.Action); err != nil {
				diagnostics = append(diagnostics, responseSemanticLoss(
					fmt.Sprintf("output[%d].action", index),
					"web_search_action_unrepresentable",
					err.Error(),
				))
			}
		}
		if output.Status != "" && output.Status != "in_progress" && output.Status != "completed" && output.Status != "failed" {
			diagnostics = append(diagnostics, responseSemanticLoss(
				fmt.Sprintf("output[%d].status", index),
				"hosted_tool_status_unrepresentable",
				fmt.Sprintf("Claude cannot preserve hosted-tool status %q", output.Status),
			))
		}
		if output.Status == "failed" && !rawJSONPresent(output.ItemError) && !rawJSONPresent(output.Output) && !rawJSONPresent(output.Results) {
			diagnostics = append(diagnostics, responseSemanticLoss(
				fmt.Sprintf("output[%d].status", index),
				"hosted_tool_error_missing",
				"failed hosted-tool output has no error or output that Claude can preserve",
			))
		}
	}
	return diagnostics
}

func inspectOpenAIResponsesStreamResponse(response *dto.ResponsesStreamResponse) []types.ConversionDiagnostic {
	if response == nil {
		return nil
	}
	if response.Item != nil && isResponsesHostedOutput(response.Item.Type) {
		return []types.ConversionDiagnostic{responseSemanticLoss(
			"item",
			"hosted_tool_event_unrepresentable",
			fmt.Sprintf("OpenAI Responses hosted-tool output %q has no semantic target-protocol stream mapping", response.Item.Type),
		)}
	}
	eventType := strings.TrimSpace(response.Type)
	if strings.Contains(eventType, ".web_search_call.") ||
		strings.Contains(eventType, ".file_search_call.") ||
		strings.Contains(eventType, ".code_interpreter_call.") ||
		strings.Contains(eventType, ".computer_tool_call.") ||
		strings.Contains(eventType, ".image_generation_call.") ||
		strings.Contains(eventType, ".mcp_call.") {
		return []types.ConversionDiagnostic{responseSemanticLoss(
			"type",
			"hosted_tool_event_unrepresentable",
			fmt.Sprintf("OpenAI Responses hosted-tool stream event %q has no semantic target-protocol mapping", eventType),
		)}
	}
	return nil
}

func isResponsesHostedOutput(outputType string) bool {
	switch strings.TrimSpace(outputType) {
	case "", "message", "reasoning", "function_call", "custom_tool_call":
		return false
	default:
		return true
	}
}

func inspectGeminiResponse(response *dto.GeminiChatResponse, to types.RelayFormat) []types.ConversionDiagnostic {
	if response == nil {
		return nil
	}
	var diagnostics []types.ConversionDiagnostic
	for index := range response.Candidates {
		metadata := response.Candidates[index].GroundingMetadata
		if metadata == nil {
			continue
		}
		path := fmt.Sprintf("candidates[%d].groundingMetadata", index)
		if len(metadata.WebSearchQueries) > 0 && to != types.RelayFormatOpenAIResponses {
			diagnostics = append(diagnostics, responseSemanticLoss(
				path+".webSearchQueries",
				"web_search_call_unrepresentable",
				"Gemini grounding confirms a hosted web search, but the target converter cannot produce an OpenAI Responses web_search_call item",
			))
		}
		diagnostics = append(diagnostics, inspectGeminiGroundingCitations(response.Candidates[index].Content, metadata, path)...)
		if len(metadata.WebSearchQueries) == 0 && len(metadata.RetrievalQueries) == 0 && len(metadata.SearchEntryPoint) == 0 && len(metadata.RetrievalMetadata) == 0 && len(metadata.SourceFlaggingUris) == 0 && metadata.GoogleMapsWidgetContextToken == "" {
			continue
		}
		diagnostics = append(diagnostics, responsePresentationLoss(
			path,
			"hosted_tool_metadata_reduced",
			"Gemini grounding citations are preserved, but provider-specific search metadata has no target-protocol equivalent",
		))
	}
	return diagnostics
}

type groundingSupportForInspection struct {
	Segment struct {
		PartIndex  *int   `json:"partIndex,omitempty"`
		StartIndex int    `json:"startIndex,omitempty"`
		EndIndex   int    `json:"endIndex,omitempty"`
		Text       string `json:"text,omitempty"`
	} `json:"segment"`
	GroundingChunkIndices []int `json:"groundingChunkIndices"`
}

type groundingChunkForInspection struct {
	Web              *groundingSourceForInspection `json:"web,omitempty"`
	RetrievedContext *groundingSourceForInspection `json:"retrievedContext,omitempty"`
}

type groundingSourceForInspection struct {
	URI string `json:"uri,omitempty"`
}

func inspectGeminiGroundingCitations(content dto.GeminiChatContent, metadata *dto.GeminiGroundingMetadata, path string) []types.ConversionDiagnostic {
	if len(metadata.GroundingSupports) == 0 {
		return nil
	}
	var chunks []groundingChunkForInspection
	if len(metadata.GroundingChunks) == 0 || kitutil.Unmarshal(metadata.GroundingChunks, &chunks) != nil {
		return []types.ConversionDiagnostic{responseSemanticLoss(
			path+".groundingChunks",
			"grounding_source_invalid",
			"Gemini grounding chunks are missing or cannot be decoded",
		)}
	}
	var supports []groundingSupportForInspection
	if err := kitutil.Unmarshal(metadata.GroundingSupports, &supports); err != nil {
		return []types.ConversionDiagnostic{responseSemanticLoss(
			path+".groundingSupports",
			"grounding_citation_invalid",
			fmt.Sprintf("Gemini grounding supports cannot be decoded: %v", err),
		)}
	}
	textPartCount := 0
	soleTextPart := -1
	for index := range content.Parts {
		if content.Parts[index].Text == "" || content.Parts[index].Thought {
			continue
		}
		textPartCount++
		soleTextPart = index
	}
	var diagnostics []types.ConversionDiagnostic
	for index, support := range supports {
		segmentPath := fmt.Sprintf("%s.groundingSupports[%d].segment", path, index)
		hasSource := false
		for _, chunkIndex := range support.GroundingChunkIndices {
			if chunkIndex < 0 || chunkIndex >= len(chunks) {
				continue
			}
			source := chunks[chunkIndex].Web
			if source == nil {
				source = chunks[chunkIndex].RetrievedContext
			}
			if source != nil && source.URI != "" {
				hasSource = true
				break
			}
		}
		if !hasSource {
			diagnostics = append(diagnostics, responseSemanticLoss(
				fmt.Sprintf("%s.groundingSupports[%d].groundingChunkIndices", path, index),
				"grounding_source_invalid",
				"Gemini grounding support does not reference a valid source URI",
			))
			continue
		}
		partIndex := soleTextPart
		if support.Segment.PartIndex != nil {
			partIndex = *support.Segment.PartIndex
		} else if textPartCount != 1 {
			diagnostics = append(diagnostics, responseSemanticLoss(
				segmentPath+".partIndex",
				"grounding_part_ambiguous",
				"Gemini grounding omitted partIndex while multiple text parts are present, so citation placement is ambiguous",
			))
			continue
		}
		if partIndex < 0 || partIndex >= len(content.Parts) || content.Parts[partIndex].Text == "" || content.Parts[partIndex].Thought {
			diagnostics = append(diagnostics, responseSemanticLoss(
				segmentPath+".partIndex",
				"grounding_part_invalid",
				fmt.Sprintf("Gemini grounding references non-text part %d", partIndex),
			))
			continue
		}
		partText := content.Parts[partIndex].Text
		start, end := support.Segment.StartIndex, support.Segment.EndIndex
		if start < 0 || end <= start || end > len(partText) || !utf8.ValidString(partText[:start]) || !utf8.ValidString(partText[:end]) {
			diagnostics = append(diagnostics, responseSemanticLoss(
				segmentPath,
				"grounding_offset_invalid",
				"Gemini grounding byte offsets do not identify valid UTF-8 boundaries in the referenced part",
			))
			continue
		}
		if support.Segment.Text != "" && partText[start:end] != support.Segment.Text {
			diagnostics = append(diagnostics, responseSemanticLoss(
				segmentPath+".text",
				"grounding_text_mismatch",
				"Gemini grounding segment text does not match the referenced part range",
			))
		}
	}
	return diagnostics
}

func responseSemanticLoss(path string, code string, message string) types.ConversionDiagnostic {
	return types.ConversionDiagnostic{Code: code, Path: path, Message: message, Severity: types.ConversionDiagnosticError}
}

func responsePresentationLoss(path string, code string, message string) types.ConversionDiagnostic {
	return types.ConversionDiagnostic{Code: code, Path: path, Message: message, Severity: types.ConversionDiagnosticWarning}
}
