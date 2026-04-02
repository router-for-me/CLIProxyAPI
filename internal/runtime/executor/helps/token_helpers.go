package helps

import (
	"fmt"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tiktoken-go/tokenizer"
)

// tokenizerCache stores tokenizer instances to avoid repeated creation.
var tokenizerCache sync.Map

type adjustedTokenizer struct {
	tokenizer.Codec
	adjustmentFactor float64
}

func (tw *adjustedTokenizer) Count(text string) (int, error) {
	count, err := tw.Codec.Count(text)
	if err != nil {
		return 0, err
	}
	if tw.adjustmentFactor > 0 && tw.adjustmentFactor != 1.0 {
		return int(float64(count) * tw.adjustmentFactor), nil
	}
	return count, nil
}

// TokenizerForModel returns a tokenizer codec suitable for an OpenAI-style model id.
// For Claude-like models, it applies an adjustment factor since tiktoken may underestimate token counts.
func TokenizerForModel(model string) (tokenizer.Codec, error) {
	sanitized := strings.ToLower(strings.TrimSpace(model))
	if cached, ok := tokenizerCache.Load(sanitized); ok {
		return cached.(tokenizer.Codec), nil
	}

	enc, err := tokenizerForModel(sanitized)
	if err != nil {
		return nil, err
	}

	actual, _ := tokenizerCache.LoadOrStore(sanitized, enc)
	return actual.(tokenizer.Codec), nil
}

func tokenizerForModel(sanitized string) (tokenizer.Codec, error) {
	if sanitized == "" {
		return tokenizer.Get(tokenizer.Cl100kBase)
	}

	// Claude models use cl100k_base with an adjustment factor because tiktoken may underestimate.
	if strings.Contains(sanitized, "claude") || strings.HasPrefix(sanitized, "kiro-") || strings.HasPrefix(sanitized, "amazonq-") {
		enc, err := tokenizer.Get(tokenizer.Cl100kBase)
		if err != nil {
			return nil, err
		}
		return &adjustedTokenizer{Codec: enc, adjustmentFactor: 1.1}, nil
	}

	switch {
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
	case strings.HasPrefix(sanitized, "o1"):
		return tokenizer.ForModel(tokenizer.O1)
	case strings.HasPrefix(sanitized, "o3"):
		return tokenizer.ForModel(tokenizer.O3)
	case strings.HasPrefix(sanitized, "o4"):
		return tokenizer.ForModel(tokenizer.O4Mini)
	default:
		return tokenizer.Get(tokenizer.O200kBase)
	}
}

// CountOpenAIChatTokens approximates prompt tokens for OpenAI chat completions payloads.
func CountOpenAIChatTokens(enc tokenizer.Codec, payload []byte) (int64, error) {
	if enc == nil {
		return 0, fmt.Errorf("encoder is nil")
	}
	if len(payload) == 0 {
		return 0, nil
	}

	root := gjson.ParseBytes(payload)
	segments := make([]string, 0, 32)

	collectOpenAIMessages(root.Get("messages"), &segments)
	collectOpenAITools(root.Get("tools"), &segments)
	collectOpenAIFunctions(root.Get("functions"), &segments)
	collectOpenAIToolChoice(root.Get("tool_choice"), &segments)
	collectOpenAIResponseFormat(root.Get("response_format"), &segments)
	addIfNotEmpty(&segments, root.Get("input").String())
	addIfNotEmpty(&segments, root.Get("prompt").String())

	joined := strings.TrimSpace(strings.Join(segments, "\n"))
	if joined == "" {
		return 0, nil
	}

	count, err := enc.Count(joined)
	if err != nil {
		return 0, err
	}
	return int64(count), nil
}

// CountClaudeChatTokens approximates prompt tokens for Claude API chat payloads.
func CountClaudeChatTokens(enc tokenizer.Codec, payload []byte) (int64, error) {
	if enc == nil {
		return 0, fmt.Errorf("encoder is nil")
	}
	if len(payload) == 0 {
		return 0, nil
	}

	root := gjson.ParseBytes(payload)
	segments := make([]string, 0, 32)
	imageTokens := 0

	collectClaudeContent(root.Get("system"), &segments, &imageTokens)
	collectClaudeMessages(root.Get("messages"), &segments, &imageTokens)
	collectClaudeTools(root.Get("tools"), &segments)

	joined := strings.TrimSpace(strings.Join(segments, "\n"))
	if joined == "" {
		return int64(imageTokens), nil
	}
	count, err := enc.Count(joined)
	if err != nil {
		return 0, err
	}
	return int64(count + imageTokens), nil
}

// BuildOpenAIUsageJSON returns a minimal usage structure understood by downstream translators.
func BuildOpenAIUsageJSON(count int64) []byte {
	return []byte(fmt.Sprintf(`{"usage":{"prompt_tokens":%d,"completion_tokens":0,"total_tokens":%d}}`, count, count))
}

func collectOpenAIMessages(messages gjson.Result, segments *[]string) {
	if !messages.Exists() || !messages.IsArray() {
		return
	}
	messages.ForEach(func(_, message gjson.Result) bool {
		addIfNotEmpty(segments, message.Get("role").String())
		addIfNotEmpty(segments, message.Get("name").String())
		collectOpenAIContent(message.Get("content"), segments)
		collectOpenAIToolCalls(message.Get("tool_calls"), segments)
		collectOpenAIFunctionCall(message.Get("function_call"), segments)
		return true
	})
}

func collectOpenAIContent(content gjson.Result, segments *[]string) {
	if !content.Exists() {
		return
	}
	if content.Type == gjson.String {
		addIfNotEmpty(segments, content.String())
		return
	}
	if content.IsArray() {
		content.ForEach(func(_, part gjson.Result) bool {
			partType := part.Get("type").String()
			switch partType {
			case "text", "input_text", "output_text":
				addIfNotEmpty(segments, part.Get("text").String())
			case "image_url":
				addIfNotEmpty(segments, part.Get("image_url.url").String())
			case "input_audio", "output_audio", "audio":
				addIfNotEmpty(segments, part.Get("id").String())
			case "tool_result":
				addIfNotEmpty(segments, part.Get("name").String())
				collectOpenAIContent(part.Get("content"), segments)
			default:
				if part.IsArray() {
					collectOpenAIContent(part, segments)
					return true
				}
				if part.Type == gjson.JSON {
					addIfNotEmpty(segments, part.Raw)
					return true
				}
				addIfNotEmpty(segments, part.String())
			}
			return true
		})
		return
	}
	if content.Type == gjson.JSON {
		addIfNotEmpty(segments, content.Raw)
	}
}

func CollectOpenAIContent(content gjson.Result, segments *[]string) {
	collectOpenAIContent(content, segments)
}

func collectOpenAIToolCalls(calls gjson.Result, segments *[]string) {
	if !calls.Exists() || !calls.IsArray() {
		return
	}
	calls.ForEach(func(_, call gjson.Result) bool {
		addIfNotEmpty(segments, call.Get("id").String())
		addIfNotEmpty(segments, call.Get("type").String())
		function := call.Get("function")
		if function.Exists() {
			addIfNotEmpty(segments, function.Get("name").String())
			addIfNotEmpty(segments, function.Get("description").String())
			addIfNotEmpty(segments, function.Get("arguments").String())
			if params := function.Get("parameters"); params.Exists() {
				addIfNotEmpty(segments, params.Raw)
			}
		}
		return true
	})
}

func collectOpenAIFunctionCall(call gjson.Result, segments *[]string) {
	if !call.Exists() {
		return
	}
	addIfNotEmpty(segments, call.Get("name").String())
	addIfNotEmpty(segments, call.Get("arguments").String())
}

func collectOpenAITools(tools gjson.Result, segments *[]string) {
	if !tools.Exists() {
		return
	}
	if tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			appendToolPayload(tool, segments)
			return true
		})
		return
	}
	appendToolPayload(tools, segments)
}

func collectOpenAIFunctions(functions gjson.Result, segments *[]string) {
	if !functions.Exists() || !functions.IsArray() {
		return
	}
	functions.ForEach(func(_, function gjson.Result) bool {
		addIfNotEmpty(segments, function.Get("name").String())
		addIfNotEmpty(segments, function.Get("description").String())
		if params := function.Get("parameters"); params.Exists() {
			addIfNotEmpty(segments, params.Raw)
		}
		return true
	})
}

func collectOpenAIToolChoice(choice gjson.Result, segments *[]string) {
	if !choice.Exists() {
		return
	}
	if choice.Type == gjson.String {
		addIfNotEmpty(segments, choice.String())
		return
	}
	addIfNotEmpty(segments, choice.Raw)
}

func collectOpenAIResponseFormat(format gjson.Result, segments *[]string) {
	if !format.Exists() {
		return
	}
	addIfNotEmpty(segments, format.Get("type").String())
	addIfNotEmpty(segments, format.Get("name").String())
	if schema := format.Get("json_schema"); schema.Exists() {
		addIfNotEmpty(segments, schema.Raw)
	}
	if schema := format.Get("schema"); schema.Exists() {
		addIfNotEmpty(segments, schema.Raw)
	}
}

func appendToolPayload(tool gjson.Result, segments *[]string) {
	if !tool.Exists() {
		return
	}
	addIfNotEmpty(segments, tool.Get("type").String())
	addIfNotEmpty(segments, tool.Get("name").String())
	addIfNotEmpty(segments, tool.Get("description").String())
	if function := tool.Get("function"); function.Exists() {
		addIfNotEmpty(segments, function.Get("name").String())
		addIfNotEmpty(segments, function.Get("description").String())
		if params := function.Get("parameters"); params.Exists() {
			addIfNotEmpty(segments, params.Raw)
		}
	}
}

func collectClaudeMessages(messages gjson.Result, segments *[]string, imageTokens *int) {
	if !messages.Exists() || !messages.IsArray() {
		return
	}
	messages.ForEach(func(_, message gjson.Result) bool {
		addIfNotEmpty(segments, message.Get("role").String())
		collectClaudeContent(message.Get("content"), segments, imageTokens)
		return true
	})
}

func collectClaudeContent(content gjson.Result, segments *[]string, imageTokens *int) {
	if !content.Exists() {
		return
	}
	if content.Type == gjson.String {
		addIfNotEmpty(segments, content.String())
		return
	}
	if content.IsArray() {
		content.ForEach(func(_, part gjson.Result) bool {
			partType := part.Get("type").String()
			switch partType {
			case "text":
				addIfNotEmpty(segments, part.Get("text").String())
			case "image":
				source := part.Get("source")
				width := source.Get("width").Float()
				height := source.Get("height").Float()
				if imageTokens != nil {
					*imageTokens += estimateImageTokens(width, height)
				}
			case "tool_use":
				addIfNotEmpty(segments, part.Get("id").String())
				addIfNotEmpty(segments, part.Get("name").String())
				if input := part.Get("input"); input.Exists() {
					addIfNotEmpty(segments, input.Raw)
				}
			case "tool_result":
				addIfNotEmpty(segments, part.Get("tool_use_id").String())
				collectClaudeContent(part.Get("content"), segments, imageTokens)
			case "thinking":
				addIfNotEmpty(segments, part.Get("thinking").String())
			default:
				if part.Type == gjson.String {
					addIfNotEmpty(segments, part.String())
				} else if part.Type == gjson.JSON {
					addIfNotEmpty(segments, part.Raw)
				}
			}
			return true
		})
		return
	}
	if content.Type == gjson.JSON {
		addIfNotEmpty(segments, content.Raw)
	}
}

func collectClaudeTools(tools gjson.Result, segments *[]string) {
	if !tools.Exists() || !tools.IsArray() {
		return
	}
	tools.ForEach(func(_, tool gjson.Result) bool {
		addIfNotEmpty(segments, tool.Get("name").String())
		addIfNotEmpty(segments, tool.Get("description").String())
		if inputSchema := tool.Get("input_schema"); inputSchema.Exists() {
			addIfNotEmpty(segments, inputSchema.Raw)
		}
		return true
	})
}

// estimateImageTokens calculates estimated tokens for an image based on dimensions.
// Based on Claude's image token calculation: tokens ≈ (width * height) / 750
// Minimum 85 tokens, maximum 1590 tokens (for 1568x1568 images).
func estimateImageTokens(width, height float64) int {
	if width <= 0 || height <= 0 {
		// No valid dimensions, use default estimate (medium-sized image).
		return 1000
	}

	tokens := int(width * height / 750)
	if tokens < 85 {
		return 85
	}
	if tokens > 1590 {
		return 1590
	}
	return tokens
}

func addIfNotEmpty(segments *[]string, value string) {
	if segments == nil {
		return
	}
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		*segments = append(*segments, trimmed)
	}
}
