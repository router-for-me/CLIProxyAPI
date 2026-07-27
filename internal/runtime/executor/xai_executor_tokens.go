package executor

import (
	"context"
	"fmt"
	"strings"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tiktoken-go/tokenizer"
)

// CountTokens estimates token count for xAI Responses requests.
func (e *XAIExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	prepared, err := e.prepareResponsesRequest(ctx, req, opts, false)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	enc, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("xai executor: tokenizer init failed: %w", err)
	}
	count, err := countXAIInputTokens(enc, prepared.body)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("xai executor: token counting failed: %w", err)
	}
	usageJSON := fmt.Sprintf(`{"response":{"usage":{"input_tokens":%d,"output_tokens":0,"total_tokens":%d}}}`, count, count)
	translated := sdktranslator.TranslateTokenCount(ctx, prepared.to, prepared.responseFormat, count, []byte(usageJSON))
	return cliproxyexecutor.Response{Payload: translated}, nil
}

func countXAIInputTokens(enc tokenizer.Codec, body []byte) (int64, error) {
	if enc == nil {
		return 0, fmt.Errorf("encoder is nil")
	}
	if len(body) == 0 {
		return 0, nil
	}

	root := gjson.ParseBytes(body)
	segments := make([]string, 0, 32)
	xaiAppendTokenString(&segments, root.Get("instructions"))
	xaiCollectInputTokenSegments(root.Get("input"), &segments)
	xaiCollectToolTokenSegments(root.Get("tools"), &segments)

	textFormat := root.Get("text.format")
	if textFormat.Exists() {
		xaiAppendTokenString(&segments, textFormat.Get("name"))
		xaiAppendTokenJSON(&segments, textFormat.Get("schema"))
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

func xaiCollectInputTokenSegments(input gjson.Result, segments *[]string) {
	if input.Type == gjson.String {
		xaiAppendTokenString(segments, input)
		return
	}
	if !input.IsArray() {
		return
	}
	for _, item := range input.Array() {
		switch item.Get("type").String() {
		case "message":
			xaiCollectContentTokenSegments(item.Get("content"), segments)
		case "function_call":
			xaiAppendTokenString(segments, item.Get("name"))
			xaiAppendTokenJSON(segments, item.Get("arguments"))
		case "function_call_output":
			xaiAppendTokenJSON(segments, item.Get("output"))
		case "reasoning":
			for _, part := range item.Get("summary").Array() {
				xaiAppendTokenString(segments, part.Get("text"))
			}
		}
	}
}

func xaiCollectContentTokenSegments(content gjson.Result, segments *[]string) {
	if content.Type == gjson.String {
		xaiAppendTokenString(segments, content)
		return
	}
	if !content.IsArray() {
		return
	}
	for _, part := range content.Array() {
		switch part.Get("type").String() {
		case "text", "input_text", "output_text":
			xaiAppendTokenString(segments, part.Get("text"))
		case "refusal":
			xaiAppendTokenString(segments, part.Get("refusal"))
		case "input_image":
			xaiAppendTokenString(segments, part.Get("image_url"))
			xaiAppendTokenString(segments, part.Get("file_id"))
		case "input_file":
			xaiAppendTokenString(segments, part.Get("file_data"))
			xaiAppendTokenString(segments, part.Get("file_url"))
			xaiAppendTokenString(segments, part.Get("file_id"))
			xaiAppendTokenString(segments, part.Get("filename"))
		case "input_audio":
			xaiAppendTokenString(segments, part.Get("data"))
			xaiAppendTokenString(segments, part.Get("input_audio.data"))
		}
	}
}

func xaiCollectToolTokenSegments(tools gjson.Result, segments *[]string) {
	if !tools.IsArray() {
		return
	}
	for _, tool := range tools.Array() {
		if tool.Get("type").String() != xaiFunctionToolType {
			continue
		}
		xaiAppendTokenString(segments, tool.Get("name"))
		xaiAppendTokenString(segments, tool.Get("description"))
		xaiAppendTokenJSON(segments, tool.Get("parameters"))
	}
}

func xaiAppendTokenString(segments *[]string, value gjson.Result) {
	if text := strings.TrimSpace(value.String()); text != "" {
		*segments = append(*segments, text)
	}
}

func xaiAppendTokenJSON(segments *[]string, value gjson.Result) {
	if !value.Exists() {
		return
	}
	if value.Type == gjson.String {
		xaiAppendTokenString(segments, value)
		return
	}
	if text := strings.TrimSpace(value.Raw); text != "" {
		*segments = append(*segments, text)
	}
}
