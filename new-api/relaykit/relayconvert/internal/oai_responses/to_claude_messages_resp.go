package oairesponses

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/reasonmap"
	sharedclaude "github.com/QuantumNous/new-api/relaykit/relayconvert/internal/shared/claude"
	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
)

func ResponsesResponseToClaudeMessagesResponse(resp *dto.OpenAIResponsesResponse) (*dto.ClaudeResponse, *dto.Usage, error) {
	if resp == nil {
		return nil, nil, errors.New("response is nil")
	}

	usage := UsageFromResponsesUsage(resp.Usage)
	claudeResponse := &dto.ClaudeResponse{
		Id:    resp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: resp.Model,
		Usage: sharedclaude.UsageFromOpenAI(usage),
	}
	sawToolCall := false
	for index := range resp.Output {
		output := resp.Output[index]
		if output.Type == responsesOutputTypeMessage && output.Role != "" && output.Role != "assistant" {
			continue
		}
		switch output.Type {
		case responsesOutputTypeReasoning:
			if thinking := reasoningOutputText(&output); thinking != "" {
				claudeResponse.Content = append(claudeResponse.Content, dto.ClaudeMediaMessage{
					Type:     "thinking",
					Thinking: kitutil.GetPointer(thinking),
				})
			}
		case responsesOutputTypeMessage:
			for _, content := range output.Content {
				if content.Type != "output_text" {
					continue
				}
				block := dto.ClaudeMediaMessage{Type: "text", Text: kitutil.GetPointer(content.Text)}
				if citations := responsesAnnotationsToClaude(content.Annotations, content.Text); len(citations) > 0 {
					block.Citations, _ = kitutil.Marshal(citations)
				}
				claudeResponse.Content = append(claudeResponse.Content, block)
			}
		case responsesOutputTypeFunctionCall, responsesOutputTypeCustomToolCall:
			sawToolCall = true
			callID := strings.TrimSpace(output.CallId)
			if callID == "" {
				callID = strings.TrimSpace(output.ID)
			}
			claudeResponse.Content = append(claudeResponse.Content, dto.ClaudeMediaMessage{
				Type:  "tool_use",
				Id:    callID,
				Name:  output.Name,
				Input: responsesArgumentsToClaudeInput(output.ArgumentsString()),
			})
		}
	}
	if len(claudeResponse.Content) == 0 {
		claudeResponse.Content = []dto.ClaudeMediaMessage{{Type: "text", Text: kitutil.GetPointer("")}}
	}
	claudeResponse.StopReason = responsesClaudeStopReason(resp, sawToolCall)
	return claudeResponse, usage, nil
}

func responsesArgumentsToClaudeInput(arguments string) map[string]any {
	input := make(map[string]any)
	if strings.TrimSpace(arguments) == "" {
		return input
	}
	if err := kitutil.Unmarshal([]byte(arguments), &input); err == nil && input != nil {
		return input
	}
	return map[string]any{"input": arguments}
}

func responsesClaudeStopReason(resp *dto.OpenAIResponsesResponse, sawToolCall bool) string {
	if finishReason, ok := ResponsesFinishReasonFromStatus(resp); ok {
		return reasonmap.OpenAIFinishReasonToClaudeStopReason(finishReason)
	}
	if sawToolCall {
		return "tool_use"
	}
	return "end_turn"
}

func responsesAnnotationsToClaude(annotations []any, text string) []json.RawMessage {
	citations := make([]json.RawMessage, 0, len(annotations))
	for _, rawAnnotation := range annotations {
		annotation, err := kitutil.Any2Type[map[string]any](rawAnnotation)
		if err != nil || strings.TrimSpace(kitutil.Interface2String(annotation["type"])) != "url_citation" {
			continue
		}
		citation := annotation
		if nested, ok := annotation["url_citation"].(map[string]any); ok {
			citation = nested
		}
		url := strings.TrimSpace(kitutil.Interface2String(citation["url"]))
		if url == "" {
			continue
		}
		converted := map[string]any{
			"type":  "web_search_result_location",
			"url":   url,
			"title": strings.TrimSpace(kitutil.Interface2String(citation["title"])),
		}
		if citedText := kitutil.Interface2String(citation["cited_text"]); citedText != "" {
			converted["cited_text"] = citedText
		} else if citedText := responsesCitedText(text, citation); citedText != "" {
			converted["cited_text"] = citedText
		}
		if encryptedIndex := kitutil.Interface2String(citation["encrypted_index"]); encryptedIndex != "" {
			converted["encrypted_index"] = encryptedIndex
		}
		if converted["title"] == "" {
			delete(converted, "title")
		}
		encoded, err := kitutil.Marshal(converted)
		if err == nil {
			citations = append(citations, encoded)
		}
	}
	return citations
}

func responsesCitedText(text string, citation map[string]any) string {
	start, startOK := responsesAnnotationIndex(citation["start_index"])
	end, endOK := responsesAnnotationIndex(citation["end_index"])
	if !startOK || !endOK || start < 0 || end <= start || end > utf8.RuneCountInString(text) {
		return ""
	}
	runes := []rune(text)
	return string(runes[start:end])
}

func responsesAnnotationIndex(value any) (int, bool) {
	switch number := value.(type) {
	case float64:
		return int(number), number >= 0 && number == float64(int(number))
	case int:
		return number, number >= 0
	case json.Number:
		parsed, err := number.Int64()
		return int(parsed), err == nil && parsed >= 0
	default:
		return 0, false
	}
}
