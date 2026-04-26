package executor

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tiktoken-go/tokenizer"
)

var (
	codexTokenizerCache      sync.Map
	codexTokenizerCacheGroup helps.InFlightGroup[tokenizer.Codec]
)

func (e *CodexExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	body, err := thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	enc, err := tokenizerForCodexModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex executor: tokenizer init failed: %w", err)
	}

	count, err := countCodexInputTokens(enc, body)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex executor: token counting failed: %w", err)
	}

	usageJSON := fmt.Sprintf(`{"response":{"usage":{"input_tokens":%d,"output_tokens":0,"total_tokens":%d}}}`, count, count)
	translated := sdktranslator.TranslateTokenCount(ctx, to, from, count, []byte(usageJSON))
	return cliproxyexecutor.Response{Payload: translated}, nil
}

func tokenizerForCodexModel(model string) (tokenizer.Codec, error) {
	key := codexTokenizerKey(model)
	if cached, ok := codexTokenizerCache.Load(key); ok {
		if enc, okEnc := cached.(tokenizer.Codec); okEnc {
			return enc, nil
		}
		codexTokenizerCache.Delete(key)
	}

	enc, _, _, err := codexTokenizerCacheGroup.Do(context.Background(), key, func() (tokenizer.Codec, error) {
		return loadCodexTokenizer(key)
	})
	if err != nil {
		return nil, err
	}
	codexTokenizerCache.Store(key, enc)
	return enc, nil
}

func codexTokenizerKey(model string) string {
	sanitized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case sanitized == "":
		return "cl100k_base"
	case strings.HasPrefix(sanitized, "gpt-5"):
		return "gpt-5"
	case strings.HasPrefix(sanitized, "gpt-4.1"):
		return "gpt-4.1"
	case strings.HasPrefix(sanitized, "gpt-4o"):
		return "gpt-4o"
	case strings.HasPrefix(sanitized, "gpt-4"):
		return "gpt-4"
	case strings.HasPrefix(sanitized, "gpt-3.5"), strings.HasPrefix(sanitized, "gpt-3"):
		return "gpt-3.5"
	default:
		return "cl100k_base"
	}
}

func loadCodexTokenizer(key string) (tokenizer.Codec, error) {
	switch key {
	case "gpt-5":
		return tokenizer.ForModel(tokenizer.GPT5)
	case "gpt-4.1":
		return tokenizer.ForModel(tokenizer.GPT41)
	case "gpt-4o":
		return tokenizer.ForModel(tokenizer.GPT4o)
	case "gpt-4":
		return tokenizer.ForModel(tokenizer.GPT4)
	case "gpt-3.5":
		return tokenizer.ForModel(tokenizer.GPT35Turbo)
	default:
		return tokenizer.Get(tokenizer.Cl100kBase)
	}
}

func countCodexInputTokens(enc tokenizer.Codec, body []byte) (int64, error) {
	if enc == nil {
		return 0, fmt.Errorf("encoder is nil")
	}
	if len(body) == 0 {
		return 0, nil
	}

	root := gjson.ParseBytes(body)
	text := buildCodexTokenCountText(root, len(body))
	if text == "" {
		return 0, nil
	}

	count, err := enc.Count(text)
	if err != nil {
		return 0, err
	}
	return int64(count), nil
}

func buildCodexTokenCountText(root gjson.Result, estimatedSize int) string {
	var builder strings.Builder
	if estimatedSize > 0 {
		builder.Grow(estimatedSize)
	}

	appendCodexTokenCountSegment(&builder, root.Get("instructions").String())

	inputItems := root.Get("input")
	if inputItems.IsArray() {
		inputItems.ForEach(func(_, item gjson.Result) bool {
			appendCodexTokenCountInputItem(&builder, item)
			return true
		})
	}

	tools := root.Get("tools")
	if tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			appendCodexTokenCountTool(&builder, tool)
			return true
		})
	}

	textFormat := root.Get("text.format")
	if textFormat.Exists() {
		appendCodexTokenCountSegment(&builder, textFormat.Get("name").String())
		appendCodexTokenCountJSONResult(&builder, textFormat.Get("schema"))
	}

	return builder.String()
}

func appendCodexTokenCountInputItem(builder *strings.Builder, item gjson.Result) {
	switch item.Get("type").String() {
	case "message":
		content := item.Get("content")
		if content.IsArray() {
			content.ForEach(func(_, part gjson.Result) bool {
				appendCodexTokenCountSegment(builder, part.Get("text").String())
				return true
			})
		}
	case "function_call":
		appendCodexTokenCountSegment(builder, item.Get("name").String())
		appendCodexTokenCountSegment(builder, item.Get("arguments").String())
	case "function_call_output":
		appendCodexTokenCountSegment(builder, item.Get("output").String())
	default:
		appendCodexTokenCountSegment(builder, item.Get("text").String())
	}
}

func appendCodexTokenCountTool(builder *strings.Builder, tool gjson.Result) {
	appendCodexTokenCountSegment(builder, tool.Get("name").String())
	appendCodexTokenCountSegment(builder, tool.Get("description").String())
	appendCodexTokenCountJSONResult(builder, tool.Get("parameters"))
}

func appendCodexTokenCountJSONResult(builder *strings.Builder, result gjson.Result) {
	if !result.Exists() {
		return
	}
	value := result.Raw
	if result.Type == gjson.String {
		value = result.String()
	}
	appendCodexTokenCountSegment(builder, value)
}

func appendCodexTokenCountSegment(builder *strings.Builder, value string) {
	if builder == nil {
		return
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return
	}
	if builder.Len() > 0 {
		builder.WriteByte('\n')
	}
	builder.WriteString(trimmed)
}
