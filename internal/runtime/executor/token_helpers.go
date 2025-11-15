package executor

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tiktoken-go/tokenizer"
)

// tokenizerForModel returns a tokenizer codec suitable for an OpenAI-style model id.
func tokenizerForModel(model string) (tokenizer.Codec, error) {
	sanitized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case sanitized == "":
		return tokenizer.Get(tokenizer.Cl100kBase)
	case strings.HasPrefix(sanitized, "gpt-5"):
		return tokenizer.ForModel(tokenizer.GPT5)
	case strings.HasPrefix(sanitized, "gpt-5.1"):
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

// countOpenAIChatTokens approximates prompt tokens for OpenAI chat completions payloads.
func countOpenAIChatTokens(enc tokenizer.Codec, payload []byte) (int64, error) {
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

// buildOpenAIUsageJSON returns a minimal usage structure understood by downstream translators.
func buildOpenAIUsageJSON(count int64) []byte {
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

func addIfNotEmpty(segments *[]string, value string) {
	if segments == nil {
		return
	}
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		*segments = append(*segments, trimmed)
	}
}
