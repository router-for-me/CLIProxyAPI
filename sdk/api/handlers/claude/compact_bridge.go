package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	claudeCompactionMaxSize      = 8 << 20
	claudeCompactionSSEChunkSize = 16 << 10
)

type claudeCompactionReplay struct {
	Output []json.RawMessage
}

type responsesCompactionResource struct {
	ID     string            `json:"id"`
	Object string            `json:"object"`
	Output []json.RawMessage `json:"output"`
	Usage  struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

func isClaudeCompactRequest(rawJSON []byte) bool {
	messages := gjson.GetBytes(rawJSON, "messages")
	if !messages.IsArray() {
		return false
	}
	items := messages.Array()
	if len(items) == 0 || !strings.EqualFold(strings.TrimSpace(items[len(items)-1].Get("role").String()), "user") {
		return false
	}
	text := normalizedClaudeCompactPrompt(claudeMessageText(items[len(items)-1].Get("content")))
	if !strings.Contains(text, "critical: respond with text only") || strings.Count(text, "do not call any tools") < 2 {
		return false
	}
	if !strings.Contains(text, "your task is to create a detailed summary") {
		return false
	}
	if strings.Contains(text, "conversation so far") || strings.Contains(text, "recent portion of the conversation") {
		return true
	}
	return strings.Contains(text, "conversation") && (strings.Contains(text, "up to this point") || strings.Contains(text, "up to and including"))
}

func claudeMessageText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}
	var parts []string
	content.ForEach(func(_, part gjson.Result) bool {
		if part.Get("type").String() == "text" {
			if text := part.Get("text").String(); text != "" {
				parts = append(parts, text)
			}
		}
		return true
	})
	return strings.Join(parts, "\n")
}

func normalizedClaudeCompactPrompt(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}

func prepareClaudeCompactionReplay(rawJSON []byte, _ string) ([]byte, *claudeCompactionReplay, error) {
	var root map[string]any
	if errUnmarshal := json.Unmarshal(rawJSON, &root); errUnmarshal != nil {
		return nil, nil, fmt.Errorf("decode Claude request for compaction replay: %w", errUnmarshal)
	}
	messages, okMessages := root["messages"].([]any)
	if !okMessages {
		return nil, nil, fmt.Errorf("compaction replay requires a messages array")
	}

	var replay *claudeCompactionReplay
	updatedMessages := make([]any, 0, len(messages))
	for _, rawMessage := range messages {
		message, okMessage := rawMessage.(map[string]any)
		if !okMessage {
			updatedMessages = append(updatedMessages, rawMessage)
			continue
		}
		keepMessage, found, errRewrite := rewriteClaudeMessageCompaction(message)
		if errRewrite != nil {
			return nil, nil, errRewrite
		}
		if found != nil {
			// The newest compact result replaces the previous context window.
			updatedMessages = updatedMessages[:0]
			replay = found
		}
		if keepMessage {
			updatedMessages = append(updatedMessages, message)
		}
	}
	if replay == nil {
		return rawJSON, nil, nil
	}
	root["messages"] = updatedMessages
	root[ClaudeResponsesCompactionField] = map[string]any{"output": replay.Output}
	updated, errMarshal := json.Marshal(root)
	if errMarshal != nil {
		return nil, nil, fmt.Errorf("encode Claude request with compaction replay: %w", errMarshal)
	}
	return updated, replay, nil
}

func rewriteClaudeMessageCompaction(message map[string]any) (bool, *claudeCompactionReplay, error) {
	content, exists := message["content"]
	if !exists {
		return true, nil, nil
	}
	switch value := content.(type) {
	case string:
		text, replay, found, errStrip := stripClaudeCompactionCiphertext(value)
		if errStrip != nil {
			return false, nil, errStrip
		}
		if !found {
			return true, nil, nil
		}
		if text == "" {
			return false, replay, nil
		}
		message["content"] = text
		return true, replay, nil
	case []any:
		var replay *claudeCompactionReplay
		parts := make([]any, 0, len(value))
		for _, rawPart := range value {
			part, okPart := rawPart.(map[string]any)
			if !okPart || part["type"] != "text" {
				parts = append(parts, rawPart)
				continue
			}
			text, _ := part["text"].(string)
			updatedText, foundReplay, found, errStrip := stripClaudeCompactionCiphertext(text)
			if errStrip != nil {
				return false, nil, errStrip
			}
			if found {
				if replay != nil {
					return false, nil, fmt.Errorf("multiple compaction blocks in one message")
				}
				replay = foundReplay
				if updatedText == "" {
					continue
				}
				part["text"] = updatedText
			}
			parts = append(parts, part)
		}
		if replay == nil {
			return true, nil, nil
		}
		if len(parts) == 0 {
			return false, replay, nil
		}
		message["content"] = parts
		return true, replay, nil
	default:
		return true, nil, nil
	}
}

func stripClaudeCompactionCiphertext(text string) (string, *claudeCompactionReplay, bool, error) {
	lines := strings.Split(text, "\n")
	var found *claudeCompactionReplay
	for i, line := range lines {
		token := strings.TrimSpace(line)
		if len(token) > claudeCompactionMaxSize || !signature.IsValidGPTReasoningSignature(token) {
			continue
		}
		if found != nil {
			return "", nil, false, fmt.Errorf("multiple raw compaction blocks in one message")
		}
		item, _ := json.Marshal(map[string]string{"type": "compaction", "encrypted_content": token})
		found = &claudeCompactionReplay{Output: []json.RawMessage{item}}
		lines[i] = ""
	}
	if found == nil {
		return text, nil, false, nil
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), found, true, nil
}

func validateResponsesCompactionOutput(output []json.RawMessage) error {
	if len(output) == 0 {
		return fmt.Errorf("compaction output is empty")
	}
	hasCompaction := false
	for i, item := range output {
		itemType := gjson.GetBytes(item, "type").String()
		switch itemType {
		case "compaction", "compaction_summary":
			if strings.TrimSpace(gjson.GetBytes(item, "encrypted_content").String()) == "" {
				return fmt.Errorf("compaction output item %d has no encrypted_content", i)
			}
			hasCompaction = true
		case "message":
			role := gjson.GetBytes(item, "role").String()
			if role != "user" && role != "assistant" && role != "developer" {
				return fmt.Errorf("compaction message item %d has unsupported role %q", i, role)
			}
			content := gjson.GetBytes(item, "content")
			if !content.IsArray() {
				return fmt.Errorf("compaction message item %d has invalid content", i)
			}
			for j, part := range content.Array() {
				partType := part.Get("type").String()
				if partType != "input_text" && partType != "output_text" {
					return fmt.Errorf("compaction message item %d content %d has unsupported type %q", i, j, partType)
				}
				if part.Get("text").Type != gjson.String {
					return fmt.Errorf("compaction message item %d content %d has invalid text", i, j)
				}
			}
		default:
			return fmt.Errorf("compaction output item %d has unsupported type %q", i, itemType)
		}
	}
	if !hasCompaction {
		return fmt.Errorf("compaction output has no opaque compaction item")
	}
	return nil
}

func (h *ClaudeCodeAPIHandler) handleCompactResponsesBridge(c *gin.Context, rawJSON []byte, clientModel string) {
	clientWantsStream := gjson.GetBytes(rawJSON, "stream").Bool()
	if clientWantsStream {
		if _, okFlusher := c.Writer.(http.Flusher); !okFlusher {
			c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
				Error: handlers.ErrorDetail{Message: "Streaming not supported", Type: "server_error"},
			})
			return
		}
	}

	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	modelName := gjson.GetBytes(rawJSON, "model").String()
	// Compaction is a normal Codex Responses request with a final trigger item.
	// Leave authentication, transport, and response assembly to CPA's executor.
	translated := sdktranslator.TranslateRequest(sdktranslator.FromString(Claude), sdktranslator.FromString(Codex), modelName, rawJSON, false)
	var input []json.RawMessage
	for _, item := range gjson.GetBytes(rawJSON, ClaudeResponsesCompactionField+".output").Array() {
		input = append(input, json.RawMessage(item.Raw))
	}
	for _, item := range gjson.GetBytes(translated, "input").Array() {
		input = append(input, json.RawMessage(item.Raw))
	}
	input = append(input, json.RawMessage(`{"type":"compaction_trigger"}`))
	inputJSON, _ := json.Marshal(input)
	translated, _ = sjson.SetRawBytes(translated, "input", inputJSON)
	stopKeepAlive := func() {}
	if !clientWantsStream {
		stopKeepAlive = h.StartNonStreamingKeepAlive(c, cliCtx)
	}
	response, errMsg := h.ExecuteProtocolWithAuthManager(cliCtx, handlers.ProtocolExecutionRequest{
		EntryProtocol:  Codex,
		ExitProtocol:   OpenaiResponse,
		ForcedProvider: Codex,
		Model:          modelName,
		Body:           translated,
		Headers:        c.Request.Header.Clone(),
		Query:          c.Request.URL.Query(),
	})
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	clientResponse, marker, errBuild := buildClaudeCompactResponse(response.Body, clientModel, modelName)
	if errBuild != nil {
		c.JSON(http.StatusBadGateway, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{Message: errBuild.Error(), Type: "api_error"},
		})
		cliCancel(errBuild)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), response.Headers)
	if !clientWantsStream {
		c.Header("Content-Type", "application/json")
		_, _ = c.Writer.Write(clientResponse)
		cliCancel()
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")
	_, _ = c.Writer.Write(buildClaudeCompactSSE(clientResponse, marker))
	c.Writer.(http.Flusher).Flush()
	cliCancel()
}

func buildClaudeCompactResponse(rawCompact []byte, clientModel string, _ string) ([]byte, string, error) {
	var compact responsesCompactionResource
	if errUnmarshal := json.Unmarshal(rawCompact, &compact); errUnmarshal != nil {
		return nil, "", fmt.Errorf("decode upstream compaction response: %w", errUnmarshal)
	}
	if compact.Object != "response.compaction" && compact.Object != "response" {
		return nil, "", fmt.Errorf("unexpected upstream compaction object %q", compact.Object)
	}
	if errValidate := validateResponsesCompactionOutput(compact.Output); errValidate != nil {
		return nil, "", errValidate
	}
	var marker string
	for _, item := range compact.Output {
		kind := gjson.GetBytes(item, "type").String()
		if kind != "compaction" && kind != "compaction_summary" {
			continue
		}
		if marker != "" {
			return nil, "", fmt.Errorf("multiple upstream compaction blocks")
		}
		marker = gjson.GetBytes(item, "encrypted_content").String()
	}
	if len(marker) > claudeCompactionMaxSize || !signature.IsValidGPTReasoningSignature(marker) {
		return nil, "", fmt.Errorf("invalid upstream compaction ciphertext")
	}
	response := map[string]any{
		"id":            compact.ID,
		"type":          "message",
		"role":          "assistant",
		"model":         clientModel,
		"content":       []map[string]any{{"type": "text", "text": marker}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage": map[string]any{
			"cache_creation_input_tokens": int64(0),
			"cache_read_input_tokens":     int64(0),
			"input_tokens":                compact.Usage.InputTokens,
			"output_tokens":               compact.Usage.OutputTokens,
			"output_tokens_details":       map[string]any{"thinking_tokens": int64(0)},
			"server_tool_use":             map[string]any{"web_fetch_requests": int64(0), "web_search_requests": int64(0)},
		},
	}
	body, errMarshal := json.Marshal(response)
	if errMarshal != nil {
		return nil, "", fmt.Errorf("encode Claude compact response: %w", errMarshal)
	}
	return body, marker, nil
}

func buildClaudeCompactSSE(clientResponse []byte, marker string) []byte {
	response := gjson.ParseBytes(clientResponse)
	messageStart := map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            response.Get("id").String(),
			"type":          "message",
			"role":          "assistant",
			"model":         response.Get("model").String(),
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"cache_creation_input_tokens": int64(0),
				"cache_read_input_tokens":     int64(0),
				"input_tokens":                response.Get("usage.input_tokens").Int(),
				"output_tokens":               int64(0),
				"output_tokens_details":       map[string]any{"thinking_tokens": int64(0)},
				"server_tool_use":             map[string]any{"web_fetch_requests": int64(0), "web_search_requests": int64(0)},
			},
		},
	}
	var out bytes.Buffer
	appendClaudeSSEEvent(&out, "message_start", messageStart)
	appendClaudeSSEEvent(&out, "content_block_start", map[string]any{
		"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""},
	})
	for len(marker) > 0 {
		chunkSize := claudeCompactionSSEChunkSize
		if len(marker) < chunkSize {
			chunkSize = len(marker)
		}
		appendClaudeSSEEvent(&out, "content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": marker[:chunkSize]},
		})
		marker = marker[chunkSize:]
	}
	appendClaudeSSEEvent(&out, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	appendClaudeSSEEvent(&out, "message_delta", map[string]any{
		"type":               "message_delta",
		"context_management": nil,
		"delta": map[string]any{
			"container":     nil,
			"stop_details":  nil,
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
		},
		"usage": map[string]any{
			"cache_creation_input_tokens": int64(0),
			"cache_read_input_tokens":     int64(0),
			"input_tokens":                response.Get("usage.input_tokens").Int(),
			"iterations":                  nil,
			"output_tokens":               response.Get("usage.output_tokens").Int(),
			"output_tokens_details":       map[string]any{"thinking_tokens": int64(0)},
			"server_tool_use":             map[string]any{"web_fetch_requests": int64(0), "web_search_requests": int64(0)},
		},
	})
	appendClaudeSSEEvent(&out, "message_stop", map[string]any{"type": "message_stop"})
	return out.Bytes()
}

func appendClaudeSSEEvent(out *bytes.Buffer, event string, payload any) {
	data, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return
	}
	out.WriteString("event: ")
	out.WriteString(event)
	out.WriteByte('\n')
	out.WriteString("data: ")
	out.Write(data)
	out.WriteString("\n\n")
}
