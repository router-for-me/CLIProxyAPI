package claude

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/tidwall/gjson"
)

const (
	claudeCompactionCapsuleVersion = 1
	claudeCompactionCapsulePrefix  = "<cpa-responses-compaction>"
	claudeCompactionCapsuleSuffix  = "</cpa-responses-compaction>"
	claudeCompactionCapsuleMaxSize = 8 << 20
	claudeCompactionSSEChunkSize   = 16 << 10
)

type claudeCompactionCapsule struct {
	Version int               `json:"version"`
	Model   string            `json:"model"`
	AuthID  string            `json:"auth_id"`
	Output  []json.RawMessage `json:"output"`
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

func prepareClaudeCompactionReplay(rawJSON []byte, upstreamModel string) ([]byte, *claudeCompactionCapsule, error) {
	var root map[string]any
	if errUnmarshal := json.Unmarshal(rawJSON, &root); errUnmarshal != nil {
		return nil, nil, fmt.Errorf("decode Claude request for compaction replay: %w", errUnmarshal)
	}
	messages, okMessages := root["messages"].([]any)
	if !okMessages {
		return nil, nil, fmt.Errorf("compaction replay requires a messages array")
	}

	var capsule *claudeCompactionCapsule
	updatedMessages := make([]any, 0, len(messages))
	for _, rawMessage := range messages {
		message, okMessage := rawMessage.(map[string]any)
		if !okMessage {
			updatedMessages = append(updatedMessages, rawMessage)
			continue
		}
		keepMessage, found, errRewrite := rewriteClaudeMessageCapsule(message)
		if errRewrite != nil {
			return nil, nil, errRewrite
		}
		if found != nil {
			if capsule != nil {
				return nil, nil, fmt.Errorf("multiple compaction capsules are not supported")
			}
			capsule = found
		}
		if keepMessage {
			updatedMessages = append(updatedMessages, message)
		}
	}
	if capsule == nil {
		return rawJSON, nil, nil
	}
	if thinking.ParseSuffix(capsule.Model).ModelName != thinking.ParseSuffix(upstreamModel).ModelName {
		return nil, nil, fmt.Errorf("compaction capsule model %q does not match request model %q", capsule.Model, upstreamModel)
	}
	if errValidate := validateClaudeCompactionCapsule(capsule); errValidate != nil {
		return nil, nil, errValidate
	}

	root["messages"] = updatedMessages
	root[ClaudeResponsesCompactionField] = map[string]any{"output": capsule.Output}
	updated, errMarshal := json.Marshal(root)
	if errMarshal != nil {
		return nil, nil, fmt.Errorf("encode Claude request with compaction replay: %w", errMarshal)
	}
	return updated, capsule, nil
}

func rewriteClaudeMessageCapsule(message map[string]any) (bool, *claudeCompactionCapsule, error) {
	content, exists := message["content"]
	if !exists {
		return true, nil, nil
	}
	switch value := content.(type) {
	case string:
		text, capsule, found, errStrip := stripClaudeCompactionCapsule(value)
		if errStrip != nil {
			return false, nil, errStrip
		}
		if !found {
			return true, nil, nil
		}
		if text == "" {
			return false, capsule, nil
		}
		message["content"] = text
		return true, capsule, nil
	case []any:
		var capsule *claudeCompactionCapsule
		parts := make([]any, 0, len(value))
		for _, rawPart := range value {
			part, okPart := rawPart.(map[string]any)
			if !okPart || part["type"] != "text" {
				parts = append(parts, rawPart)
				continue
			}
			text, _ := part["text"].(string)
			updatedText, foundCapsule, found, errStrip := stripClaudeCompactionCapsule(text)
			if errStrip != nil {
				return false, nil, errStrip
			}
			if found {
				if capsule != nil {
					return false, nil, fmt.Errorf("multiple compaction capsules are not supported")
				}
				capsule = foundCapsule
				if updatedText == "" {
					continue
				}
				part["text"] = updatedText
			}
			parts = append(parts, part)
		}
		if capsule == nil {
			return true, nil, nil
		}
		if len(parts) == 0 {
			return false, capsule, nil
		}
		message["content"] = parts
		return true, capsule, nil
	default:
		return true, nil, nil
	}
}

func stripClaudeCompactionCapsule(text string) (string, *claudeCompactionCapsule, bool, error) {
	searchFrom := 0
	foundStart := -1
	foundEnd := -1
	var foundCapsule *claudeCompactionCapsule
	for searchFrom < len(text) {
		relativeStart := strings.Index(text[searchFrom:], claudeCompactionCapsulePrefix)
		if relativeStart < 0 {
			break
		}
		start := searchFrom + relativeStart
		encodedStart := start + len(claudeCompactionCapsulePrefix)
		relativeEnd := strings.Index(text[encodedStart:], claudeCompactionCapsuleSuffix)
		if relativeEnd < 0 {
			if isDelimitedClaudeCompactionMarker(text, start, len(text)) {
				return "", nil, false, fmt.Errorf("unterminated compaction capsule")
			}
			searchFrom = encodedStart
			continue
		}
		encodedEnd := encodedStart + relativeEnd
		markerEnd := encodedEnd + len(claudeCompactionCapsuleSuffix)
		if !isDelimitedClaudeCompactionMarker(text, start, markerEnd) {
			searchFrom = encodedStart
			continue
		}
		capsule, errDecode := decodeClaudeCompactionCapsule(text[encodedStart:encodedEnd])
		if errDecode != nil {
			return "", nil, false, errDecode
		}
		if foundCapsule != nil {
			return "", nil, false, fmt.Errorf("multiple compaction capsules are not supported")
		}
		foundStart = start
		foundEnd = markerEnd
		foundCapsule = capsule
		searchFrom = markerEnd
	}
	if foundCapsule == nil {
		return text, nil, false, nil
	}
	remaining := strings.TrimSpace(text[:foundStart] + text[foundEnd:])
	return remaining, foundCapsule, true, nil
}

func isDelimitedClaudeCompactionMarker(text string, start, end int) bool {
	return (start == 0 || isClaudeCompactionWhitespace(text[start-1])) &&
		(end == len(text) || isClaudeCompactionWhitespace(text[end]))
}

func isClaudeCompactionWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r'
}

func encodeClaudeCompactionCapsule(capsule *claudeCompactionCapsule) (string, error) {
	if errValidate := validateClaudeCompactionCapsule(capsule); errValidate != nil {
		return "", errValidate
	}
	payload, errMarshal := json.Marshal(capsule)
	if errMarshal != nil {
		return "", fmt.Errorf("encode compaction capsule: %w", errMarshal)
	}
	if len(payload) > claudeCompactionCapsuleMaxSize {
		return "", fmt.Errorf("compaction capsule exceeds %d bytes", claudeCompactionCapsuleMaxSize)
	}
	return claudeCompactionCapsulePrefix + base64.RawURLEncoding.EncodeToString(payload) + claudeCompactionCapsuleSuffix, nil
}

func decodeClaudeCompactionCapsule(encoded string) (*claudeCompactionCapsule, error) {
	if len(encoded) > base64.RawURLEncoding.EncodedLen(claudeCompactionCapsuleMaxSize) {
		return nil, fmt.Errorf("compaction capsule is too large")
	}
	payload, errDecode := base64.RawURLEncoding.DecodeString(strings.TrimSpace(encoded))
	if errDecode != nil {
		return nil, fmt.Errorf("decode compaction capsule: %w", errDecode)
	}
	var capsule claudeCompactionCapsule
	if errUnmarshal := json.Unmarshal(payload, &capsule); errUnmarshal != nil {
		return nil, fmt.Errorf("decode compaction capsule JSON: %w", errUnmarshal)
	}
	if errValidate := validateClaudeCompactionCapsule(&capsule); errValidate != nil {
		return nil, errValidate
	}
	return &capsule, nil
}

func validateClaudeCompactionCapsule(capsule *claudeCompactionCapsule) error {
	if capsule == nil {
		return fmt.Errorf("compaction capsule is missing")
	}
	if capsule.Version != claudeCompactionCapsuleVersion {
		return fmt.Errorf("unsupported compaction capsule version %d", capsule.Version)
	}
	if strings.TrimSpace(capsule.Model) == "" {
		return fmt.Errorf("compaction capsule model is missing")
	}
	if strings.TrimSpace(capsule.AuthID) == "" {
		return fmt.Errorf("compaction capsule auth affinity is missing")
	}
	return validateResponsesCompactionOutput(capsule.Output)
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

func (h *ClaudeCodeAPIHandler) handleCompactResponsesBridge(c *gin.Context, rawJSON []byte, clientModel string, replay *claudeCompactionCapsule) {
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
	if replay != nil {
		cliCtx = handlers.WithPinnedAuthID(cliCtx, replay.AuthID)
	}
	selectedAuthID := ""
	cliCtx = handlers.WithSelectedAuthIDCallback(cliCtx, func(authID string) {
		selectedAuthID = authID
	})
	modelName := gjson.GetBytes(rawJSON, "model").String()
	stopKeepAlive := func() {}
	if !clientWantsStream {
		stopKeepAlive = h.StartNonStreamingKeepAlive(c, cliCtx)
	}
	response, errMsg := h.ExecuteProtocolWithAuthManager(cliCtx, handlers.ProtocolExecutionRequest{
		EntryProtocol:  Claude,
		ExitProtocol:   OpenaiResponse,
		ForcedProvider: Codex,
		Model:          modelName,
		Body:           rawJSON,
		Headers:        c.Request.Header.Clone(),
		Query:          c.Request.URL.Query(),
		Alt:            ClaudeResponsesCompactBridgeAlt,
	})
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	if selectedAuthID == "" && replay != nil {
		selectedAuthID = replay.AuthID
	}
	clientResponse, marker, errBuild := buildClaudeCompactResponse(response.Body, clientModel, modelName, selectedAuthID)
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

func buildClaudeCompactResponse(rawCompact []byte, clientModel, upstreamModel, authID string) ([]byte, string, error) {
	var compact responsesCompactionResource
	if errUnmarshal := json.Unmarshal(rawCompact, &compact); errUnmarshal != nil {
		return nil, "", fmt.Errorf("decode upstream compaction response: %w", errUnmarshal)
	}
	if compact.Object != "response.compaction" {
		return nil, "", fmt.Errorf("unexpected upstream compaction object %q", compact.Object)
	}
	if errValidate := validateResponsesCompactionOutput(compact.Output); errValidate != nil {
		return nil, "", errValidate
	}
	capsule := &claudeCompactionCapsule{
		Version: claudeCompactionCapsuleVersion,
		Model:   upstreamModel,
		AuthID:  authID,
		Output:  compact.Output,
	}
	marker, errEncode := encodeClaudeCompactionCapsule(capsule)
	if errEncode != nil {
		return nil, "", errEncode
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
