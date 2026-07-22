package helps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/tiktoken-go/tokenizer"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

var (
	claudeInputTokenizerOnce  sync.Once
	claudeInputTokenizerCodec tokenizer.Codec
	claudeInputTokenizerErr   error
)

// ClaudeInputTokenState tracks the one-time input token update for a translated Claude stream.
type ClaudeInputTokenState struct {
	upstreamFormat  sdktranslator.Format
	responseFormat  sdktranslator.Format
	originalRequest []byte
	codec           tokenizer.Codec
	handled         bool
}

// NewClaudeInputTokenState creates request-scoped state for translated Claude input token usage.
func NewClaudeInputTokenState(sourceFormat, upstreamFormat, responseFormat sdktranslator.Format, originalRequest []byte) *ClaudeInputTokenState {
	enabled := sourceFormat == sdktranslator.FormatClaude &&
		upstreamFormat != sdktranslator.FormatClaude &&
		responseFormat == sdktranslator.FormatClaude
	return &ClaudeInputTokenState{
		upstreamFormat:  upstreamFormat,
		responseFormat:  responseFormat,
		originalRequest: originalRequest,
		handled:         !enabled,
	}
}

// TranslateStreamWithClaudeInputTokens translates a stream chunk and estimates Claude message_start input usage once.
func TranslateStreamWithClaudeInputTokens(
	ctx context.Context,
	upstreamFormat, responseFormat sdktranslator.Format,
	model string,
	originalRequestRawJSON, requestRawJSON, rawJSON []byte,
	param *any,
	state *ClaudeInputTokenState,
) [][]byte {
	chunks := sdktranslator.TranslateStream(
		ctx,
		upstreamFormat,
		responseFormat,
		model,
		originalRequestRawJSON,
		requestRawJSON,
		rawJSON,
		param,
	)
	if state == nil {
		return chunks
	}
	return state.apply(ctx, chunks)
}

func claudeInputTokenizer() (tokenizer.Codec, error) {
	claudeInputTokenizerOnce.Do(func() {
		claudeInputTokenizerCodec, claudeInputTokenizerErr = tokenizer.Get(tokenizer.O200kBase)
	})
	return claudeInputTokenizerCodec, claudeInputTokenizerErr
}

func countClaudeInputTokens(enc tokenizer.Codec, payload []byte) (int64, error) {
	if enc == nil {
		return 0, fmt.Errorf("encoder is nil")
	}
	segments, err := collectClaudeInputTokenSegments(payload)
	if err != nil {
		return 0, err
	}
	if len(segments) == 0 {
		return 0, nil
	}
	count, err := enc.Count(strings.Join(segments, "\n"))
	if err != nil {
		return 0, err
	}
	return int64(count), nil
}

func collectClaudeInputTokenSegments(payload []byte) ([]string, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil, nil
	}
	if !gjson.ValidBytes(payload) {
		return nil, fmt.Errorf("invalid Claude request JSON")
	}

	root := gjson.ParseBytes(payload)
	segments := make([]string, 0, 32)
	collectClaudeSystemTokenSegments(root.Get("system"), &segments)
	collectClaudeMessageTokenSegments(root.Get("messages"), &segments)
	collectClaudeToolTokenSegments(root.Get("tools"), &segments)
	collectClaudeToolChoiceTokenSegments(root.Get("tool_choice"), &segments)
	return segments, nil
}

func collectClaudeSystemTokenSegments(system gjson.Result, segments *[]string) {
	if system.Type == gjson.String {
		appendClaudeTokenString(segments, system.String())
		return
	}
	if !system.IsArray() {
		return
	}
	system.ForEach(func(_, part gjson.Result) bool {
		if part.Type == gjson.String {
			appendClaudeTokenString(segments, part.String())
		} else if part.Get("type").String() == "text" {
			appendClaudeTokenString(segments, part.Get("text").String())
		}
		return true
	})
}

func collectClaudeMessageTokenSegments(messages gjson.Result, segments *[]string) {
	if !messages.IsArray() {
		return
	}
	messages.ForEach(func(_, message gjson.Result) bool {
		appendClaudeTokenString(segments, message.Get("role").String())
		collectClaudeContentTokenSegments(message.Get("content"), segments)
		return true
	})
}

func collectClaudeContentTokenSegments(content gjson.Result, segments *[]string) {
	if !content.Exists() {
		return
	}
	if content.Type == gjson.String {
		appendClaudeTokenString(segments, content.String())
		return
	}
	if content.IsArray() {
		content.ForEach(func(_, part gjson.Result) bool {
			collectClaudeContentTokenSegments(part, segments)
			return true
		})
		return
	}
	if !content.IsObject() {
		return
	}

	switch content.Get("type").String() {
	case "text":
		appendClaudeTokenString(segments, content.Get("text").String())
	case "thinking":
		appendClaudeTokenString(segments, content.Get("thinking").String())
	case "document":
		collectClaudeDocumentTokenSegments(content, segments)
	case "tool_use", "server_tool_use", "mcp_tool_use":
		appendClaudeTokenString(segments, content.Get("id").String())
		appendClaudeTokenString(segments, content.Get("name").String())
		appendClaudeTokenJSON(segments, content.Get("input"))
	case "tool_result", "mcp_tool_result", "web_search_tool_result", "web_fetch_tool_result", "code_execution_tool_result", "bash_code_execution_tool_result", "text_editor_code_execution_tool_result":
		appendClaudeTokenString(segments, content.Get("tool_use_id").String())
		appendClaudeTokenString(segments, content.Get("tool_call_id").String())
		collectClaudeContentTokenSegments(content.Get("content"), segments)
	case "web_search_result", "search_result":
		if source := content.Get("source"); source.Type == gjson.String {
			appendClaudeTokenString(segments, source.String())
		}
		appendClaudeTokenString(segments, content.Get("title").String())
		appendClaudeTokenString(segments, content.Get("url").String())
		appendClaudeTokenString(segments, content.Get("page_age").String())
		collectClaudeContentTokenSegments(content.Get("content"), segments)
	case "web_fetch_result":
		appendClaudeTokenString(segments, content.Get("url").String())
		appendClaudeTokenString(segments, content.Get("retrieved_at").String())
		collectClaudeContentTokenSegments(content.Get("content"), segments)
	case "code_execution_result", "bash_code_execution_result", "text_editor_code_execution_result":
		appendClaudeTokenString(segments, content.Get("stdout").String())
		appendClaudeTokenString(segments, content.Get("stderr").String())
		appendClaudeTokenString(segments, content.Get("return_code").String())
		collectClaudeContentTokenSegments(content.Get("content"), segments)
		collectClaudeContentTokenSegments(content.Get("output"), segments)
	case "tool_reference":
		appendClaudeTokenString(segments, content.Get("tool_name").String())
	case "image", "input_audio", "audio", "video", "redacted_thinking":
		return
	case "":
		appendClaudeTokenJSON(segments, content)
	default:
		appendClaudeTokenString(segments, content.Get("text").String())
	}
}

func collectClaudeDocumentTokenSegments(document gjson.Result, segments *[]string) {
	source := document.Get("source")
	if source.Get("type").String() != "text" {
		return
	}
	appendClaudeTokenString(segments, document.Get("title").String())
	appendClaudeTokenString(segments, document.Get("context").String())
	appendClaudeTokenString(segments, source.Get("data").String())
	appendClaudeTokenString(segments, source.Get("content").String())
}

func collectClaudeToolTokenSegments(tools gjson.Result, segments *[]string) {
	if !tools.IsArray() {
		return
	}
	tools.ForEach(func(_, tool gjson.Result) bool {
		appendClaudeTokenString(segments, tool.Get("type").String())
		appendClaudeTokenString(segments, tool.Get("name").String())
		appendClaudeTokenString(segments, tool.Get("description").String())
		appendClaudeTokenJSON(segments, tool.Get("input_schema"))
		return true
	})
}

func collectClaudeToolChoiceTokenSegments(toolChoice gjson.Result, segments *[]string) {
	if !toolChoice.Exists() {
		return
	}
	if toolChoice.Type == gjson.String {
		appendClaudeTokenString(segments, toolChoice.String())
		return
	}
	appendClaudeTokenString(segments, toolChoice.Get("type").String())
	appendClaudeTokenString(segments, toolChoice.Get("name").String())
}

func appendClaudeTokenString(segments *[]string, value string) {
	if segments == nil {
		return
	}
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		*segments = append(*segments, trimmed)
	}
}

func appendClaudeTokenJSON(segments *[]string, value gjson.Result) {
	if !value.Exists() {
		return
	}
	if value.Type == gjson.String {
		appendClaudeTokenString(segments, value.String())
		return
	}
	raw := strings.TrimSpace(value.Raw)
	if raw == "" {
		return
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(raw)); err == nil {
		appendClaudeTokenString(segments, compact.String())
		return
	}
	appendClaudeTokenString(segments, raw)
}

func (state *ClaudeInputTokenState) apply(ctx context.Context, chunks [][]byte) [][]byte {
	if state == nil || state.handled {
		return chunks
	}
	for i := range chunks {
		updated, found := state.applyChunk(ctx, chunks[i])
		if !found {
			continue
		}
		state.handled = true
		chunks[i] = updated
		break
	}
	return chunks
}

func (state *ClaudeInputTokenState) applyChunk(ctx context.Context, chunk []byte) ([]byte, bool) {
	for lineStart := 0; lineStart < len(chunk); {
		lineEnd := bytes.IndexByte(chunk[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(chunk)
		} else {
			lineEnd += lineStart
		}

		contentEnd := lineEnd
		if contentEnd > lineStart && chunk[contentEnd-1] == '\r' {
			contentEnd--
		}
		line := chunk[lineStart:contentEnd]
		trimmedLeft := bytes.TrimLeft(line, " \t")
		if bytes.HasPrefix(trimmedLeft, []byte("data:")) {
			payloadOffset := len(line) - len(trimmedLeft) + len("data:")
			for payloadOffset < len(line) && (line[payloadOffset] == ' ' || line[payloadOffset] == '\t') {
				payloadOffset++
			}
			payloadEnd := len(line)
			for payloadEnd > payloadOffset && (line[payloadEnd-1] == ' ' || line[payloadEnd-1] == '\t') {
				payloadEnd--
			}
			payload := line[payloadOffset:payloadEnd]
			if gjson.GetBytes(payload, "type").String() == "message_start" {
				inputTokens := gjson.GetBytes(payload, "message.usage.input_tokens")
				if inputTokens.Exists() && inputTokens.Int() != 0 {
					return chunk, true
				}
				count, err := state.estimate()
				if err != nil {
					state.logEstimateError(ctx, err)
					return chunk, true
				}
				if count == 0 {
					return chunk, true
				}
				updatedPayload, errSet := sjson.SetBytes(payload, "message.usage.input_tokens", count)
				if errSet != nil {
					state.logEstimateError(ctx, fmt.Errorf("set message_start usage: %w", errSet))
					return chunk, true
				}
				payloadStart := lineStart + payloadOffset
				payloadStop := lineStart + payloadEnd
				updated := make([]byte, 0, len(chunk)+len(updatedPayload)-len(payload))
				updated = append(updated, chunk[:payloadStart]...)
				updated = append(updated, updatedPayload...)
				updated = append(updated, chunk[payloadStop:]...)
				return updated, true
			}
		}

		if lineEnd == len(chunk) {
			break
		}
		lineStart = lineEnd + 1
	}
	return chunk, false
}

func (state *ClaudeInputTokenState) estimate() (int64, error) {
	enc := state.codec
	if enc == nil {
		var err error
		enc, err = claudeInputTokenizer()
		if err != nil {
			return 0, fmt.Errorf("initialize O200kBase tokenizer: %w", err)
		}
	}
	count, err := countClaudeInputTokens(enc, state.originalRequest)
	if err != nil {
		return 0, fmt.Errorf("count Claude input tokens: %w", err)
	}
	return count, nil
}

func (state *ClaudeInputTokenState) logEstimateError(ctx context.Context, err error) {
	LogWithRequestID(ctx).WithFields(log.Fields{
		"upstream_format": state.upstreamFormat.String(),
		"response_format": state.responseFormat.String(),
	}).WithError(err).Warn("failed to estimate Claude input tokens")
}
