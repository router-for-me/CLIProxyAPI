package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/tiktoken-go/tokenizer"
)

const (
	claudeLiveUsageTickInterval  = 2 * time.Second
	claudeThinkingTokenCountBeta = "thinking-token-count-2026-05-13"
	// Claude clients treat unknown gateway model IDs as 200k-context models and
	// enter their native compact-and-retry path after a context_too_large error.
	// This compatibility boundary does not change the routed upstream model.
	claudeBridgeContextWindow = int64(200_000)
)

func claudeThinkingTokenCountRequested(headers http.Header) bool {
	if strings.Contains(strings.ToLower(headers.Get("Anthropic-Beta")), claudeThinkingTokenCountBeta) {
		return true
	}
	// Claude App workflow workers currently omit the beta header while retaining
	// the Claude CLI session headers and support for estimated_tokens deltas.
	return strings.EqualFold(strings.TrimSpace(headers.Get("X-App")), "cli") && strings.TrimSpace(headers.Get("X-Claude-Code-Session-Id")) != ""
}

func validateClaudeBridgeContextWindow(model string, body []byte, opts cliproxyexecutor.Options) (int64, error) {
	if opts.Alt != constant.ClaudeResponsesBridgeAlt {
		return 0, nil
	}
	count, errCount := estimateCodexInputTokens(model, body)
	if errCount != nil {
		return 0, errCount
	}
	if count <= claudeBridgeContextWindow || hasClaudeCompactionReplay(body) {
		return count, nil
	}
	errorBody := []byte(`{"error":{"message":"","type":"invalid_request_error","code":"context_too_large"}}`)
	errorBody, _ = sjson.SetBytes(errorBody, "error.message", fmt.Sprintf("prompt is too long: %d tokens > %d maximum", count, claudeBridgeContextWindow))
	return count, newCodexStatusErr(http.StatusBadRequest, errorBody)
}

func hasClaudeCompactionReplay(body []byte) bool {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	for _, item := range input.Array() {
		switch item.Get("type").String() {
		case "compaction", "compaction_summary":
			return strings.TrimSpace(item.Get("encrypted_content").String()) != ""
		}
	}
	return false
}

func (e *CodexExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("codex")
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	body, err := helps.ApplyRequestThinking(body, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	body = helps.SetStringIfDifferent(body, "model", baseModel)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	body, _ = sjson.DeleteBytes(body, "generate")
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body, _ = sjson.DeleteBytes(body, "stream_options")
	body = helps.SetBoolIfDifferent(body, "stream", false)
	body = normalizeCodexInstructions(body)

	enc, err := tokenizerForCodexModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex executor: tokenizer init failed: %w", err)
	}
	count, err := countCodexInputTokens(enc, body)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex executor: token counting failed: %w", err)
	}

	usageJSON := fmt.Sprintf(`{"response":{"usage":{"input_tokens":%d,"output_tokens":0,"total_tokens":%d}}}`, count, count)
	translated := sdktranslator.TranslateTokenCount(ctx, to, responseFormat, count, []byte(usageJSON))
	return cliproxyexecutor.Response{Payload: translated}, nil
}

func estimateCodexInputTokens(model string, body []byte) (int64, error) {
	enc, err := tokenizerForCodexModel(model)
	if err != nil {
		return 0, fmt.Errorf("tokenizer init failed: %w", err)
	}
	count, err := countCodexInputTokens(enc, body)
	if err != nil {
		return 0, fmt.Errorf("token counting failed: %w", err)
	}
	// Account conservatively for the Codex request framing and provider prompt that
	// are included in terminal upstream usage but are not present in the JSON body.
	if count > 0 {
		count += 256
	}
	return count, nil
}

func tokenizerForCodexModel(model string) (tokenizer.Codec, error) {
	sanitized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case sanitized == "":
		return tokenizer.Get(tokenizer.Cl100kBase)
	case strings.HasPrefix(sanitized, "gpt-5"):
		return tokenizer.ForModel(tokenizer.GPT5)
	case strings.HasPrefix(sanitized, "gpt-4.1"):
		return tokenizer.ForModel(tokenizer.GPT41)
	case strings.HasPrefix(sanitized, "gpt-4o"):
		return tokenizer.ForModel(tokenizer.GPT4o)
	case strings.HasPrefix(sanitized, "gpt-4"):
		return tokenizer.ForModel(tokenizer.GPT4)
	case strings.HasPrefix(sanitized, "gpt-3.5"), strings.HasPrefix(sanitized, "gpt-3"):
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
	var segments []string

	if inst := strings.TrimSpace(root.Get("instructions").String()); inst != "" {
		segments = append(segments, inst)
	}

	inputItems := root.Get("input")
	if inputItems.IsArray() {
		arr := inputItems.Array()
		for i := range arr {
			item := arr[i]
			switch item.Get("type").String() {
			case "message":
				content := item.Get("content")
				if content.IsArray() {
					parts := content.Array()
					for j := range parts {
						part := parts[j]
						if text := strings.TrimSpace(part.Get("text").String()); text != "" {
							segments = append(segments, text)
						}
					}
				}
			case "function_call":
				if name := strings.TrimSpace(item.Get("name").String()); name != "" {
					segments = append(segments, name)
				}
				if args := strings.TrimSpace(item.Get("arguments").String()); args != "" {
					segments = append(segments, args)
				}
			case "function_call_output":
				if out := strings.TrimSpace(item.Get("output").String()); out != "" {
					segments = append(segments, out)
				}
			default:
				if text := strings.TrimSpace(item.Get("text").String()); text != "" {
					segments = append(segments, text)
				}
			}
		}
	}

	tools := root.Get("tools")
	if tools.IsArray() {
		tarr := tools.Array()
		for i := range tarr {
			tool := tarr[i]
			if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
				segments = append(segments, name)
			}
			if desc := strings.TrimSpace(tool.Get("description").String()); desc != "" {
				segments = append(segments, desc)
			}
			if params := tool.Get("parameters"); params.Exists() {
				val := params.Raw
				if params.Type == gjson.String {
					val = params.String()
				}
				if trimmed := strings.TrimSpace(val); trimmed != "" {
					segments = append(segments, trimmed)
				}
			}
		}
	}

	textFormat := root.Get("text.format")
	if textFormat.Exists() {
		if name := strings.TrimSpace(textFormat.Get("name").String()); name != "" {
			segments = append(segments, name)
		}
		if schema := textFormat.Get("schema"); schema.Exists() {
			val := schema.Raw
			if schema.Type == gjson.String {
				val = schema.String()
			}
			if trimmed := strings.TrimSpace(val); trimmed != "" {
				segments = append(segments, trimmed)
			}
		}
	}

	text := strings.Join(segments, "\n")
	if text == "" {
		return 0, nil
	}

	count, err := enc.Count(text)
	if err != nil {
		return 0, err
	}
	return int64(count), nil
}
