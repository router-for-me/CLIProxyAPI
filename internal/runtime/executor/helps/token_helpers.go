package helps

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
	"github.com/tiktoken-go/tokenizer"
)

// TokenizerForModel returns a tokenizer codec suitable for an OpenAI-style model id.
func TokenizerForModel(model string) (tokenizer.Codec, error) {
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

	collectOpenAIContent(root.Get("system"), &segments)
	collectOpenAIContent(root.Get("instructions"), &segments)
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

// EstimateUsage approximates request and response tokens when upstream usage is missing.
func EstimateUsage(model string, requestBody []byte, responseBody []byte) usage.Detail {
	enc, err := TokenizerForModel(model)
	if err != nil {
		return usage.Detail{}
	}

	inputTokens := estimateInputTokens(enc, requestBody)
	outputTokens := countTextTokens(enc, ExtractOutputText(responseBody))
	totalTokens := inputTokens + outputTokens
	if totalTokens == 0 {
		return usage.Detail{}
	}
	return usage.Detail{
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  totalTokens,
	}
}

func estimateInputTokens(enc tokenizer.Codec, requestBody []byte) int64 {
	if len(bytes.TrimSpace(requestBody)) == 0 {
		return 0
	}
	if count, err := CountOpenAIChatTokens(enc, requestBody); err == nil && count > 0 {
		return count
	}
	return countTextTokens(enc, ExtractInputText(requestBody))
}

func countTextTokens(enc tokenizer.Codec, text string) int64 {
	if enc == nil {
		return 0
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	count, err := enc.Count(trimmed)
	if err != nil {
		return 0
	}
	return int64(count)
}

// ExtractInputText collects text-bearing request fields across supported provider schemas.
func ExtractInputText(data []byte) string {
	payloads := jsonPayloads(data)
	if len(payloads) == 0 {
		return ""
	}
	segments := make([]string, 0, 32)
	for _, payload := range payloads {
		collectInputText(gjson.ParseBytes(payload), &segments)
	}
	return strings.TrimSpace(strings.Join(segments, "\n"))
}

// ExtractOutputText collects visible response text across non-streaming and streaming schemas.
func ExtractOutputText(data []byte) string {
	payloads := jsonPayloads(data)
	if len(payloads) == 0 {
		return ""
	}
	options := detectOutputExtractionOptions(payloads)
	segments := make([]string, 0, 32)
	for _, payload := range payloads {
		collectOutputText(gjson.ParseBytes(payload), options, &segments)
	}
	return strings.TrimSpace(strings.Join(segments, ""))
}

type outputExtractionOptions struct {
	hasResponsesTextDelta     bool
	hasResponsesFunctionDelta bool
	hasResponsesOutputDone    bool
}

func detectOutputExtractionOptions(payloads [][]byte) outputExtractionOptions {
	var options outputExtractionOptions
	for _, payload := range payloads {
		root := gjson.ParseBytes(payload)
		if root.IsArray() {
			root.ForEach(func(_, item gjson.Result) bool {
				updateOutputExtractionOptions(item, &options)
				return true
			})
			continue
		}
		updateOutputExtractionOptions(root, &options)
	}
	return options
}

func updateOutputExtractionOptions(root gjson.Result, options *outputExtractionOptions) {
	if options == nil {
		return
	}
	switch root.Get("type").String() {
	case "response.output_text.delta", "response.reasoning_summary_text.delta":
		options.hasResponsesTextDelta = true
	case "response.function_call_arguments.delta":
		options.hasResponsesFunctionDelta = true
	case "response.output_item.done":
		options.hasResponsesOutputDone = true
	case "content_block_delta":
		options.hasResponsesTextDelta = true
	}
}

func jsonPayloads(data []byte) [][]byte {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil
	}
	if gjson.ValidBytes(trimmed) {
		return [][]byte{append([]byte(nil), trimmed...)}
	}

	lines := bytes.Split(trimmed, []byte("\n"))
	payloads := make([][]byte, 0, len(lines))
	for _, line := range lines {
		payload := JSONPayload(line)
		if len(payload) == 0 {
			line = bytes.TrimSpace(line)
			if !gjson.ValidBytes(line) {
				continue
			}
			payload = line
		}
		payloads = append(payloads, append([]byte(nil), payload...))
	}
	return payloads
}

func collectInputText(root gjson.Result, segments *[]string) {
	if !root.Exists() {
		return
	}
	if root.IsArray() {
		root.ForEach(func(_, item gjson.Result) bool {
			collectInputText(item, segments)
			return true
		})
		return
	}

	collectOpenAIContent(root.Get("system"), segments)
	collectOpenAIContent(root.Get("instructions"), segments)
	collectOpenAIMessages(root.Get("messages"), segments)
	collectOpenAIContent(root.Get("input"), segments)
	addIfNotEmpty(segments, root.Get("prompt").String())

	collectGeminiContents(root.Get("contents"), segments)
	collectGeminiContents(root.Get("request.contents"), segments)
	collectGeminiContent(root.Get("systemInstruction"), segments)
	collectGeminiContent(root.Get("request.systemInstruction"), segments)
	collectGeminiTools(root.Get("tools"), segments)
	collectGeminiTools(root.Get("request.tools"), segments)
	collectOpenAITools(root.Get("request.tools"), segments)
}

func collectOutputText(root gjson.Result, options outputExtractionOptions, segments *[]string) {
	if !root.Exists() {
		return
	}
	if root.IsArray() {
		root.ForEach(func(_, item gjson.Result) bool {
			collectOutputText(item, options, segments)
			return true
		})
		return
	}

	eventType := root.Get("type").String()
	switch eventType {
	case "response.output_text.delta", "response.reasoning_summary_text.delta":
		addOutputIfNotEmpty(segments, root.Get("delta").String())
		return
	case "response.function_call_arguments.delta":
		addOutputIfNotEmpty(segments, root.Get("delta").String())
		return
	case "response.output_text.done", "response.reasoning_summary_text.done":
		if !options.hasResponsesTextDelta {
			addOutputIfNotEmpty(segments, root.Get("text").String())
		}
		return
	case "response.function_call_arguments.done":
		if !options.hasResponsesFunctionDelta {
			addOutputIfNotEmpty(segments, root.Get("arguments").String())
		}
		return
	case "response.output_item.done":
		collectResponseOutputItem(root.Get("item"), options, segments)
		return
	case "response.completed", "response.done":
		if !options.hasResponsesTextDelta && !options.hasResponsesFunctionDelta && !options.hasResponsesOutputDone {
			collectResponseOutput(root.Get("response.output"), options, segments)
			addIfNotEmpty(segments, root.Get("response.output_text").String())
		}
		return
	case "content_block_delta":
		collectClaudeDelta(root.Get("delta"), segments)
		return
	case "content_block_start":
		if !options.hasResponsesTextDelta {
			collectClaudeContentBlock(root.Get("content_block"), segments)
		}
		return
	}

	collectOpenAIChoices(root.Get("choices"), segments)
	collectResponseOutput(root.Get("output"), options, segments)
	collectResponseOutput(root.Get("response.output"), options, segments)
	addOutputIfNotEmpty(segments, root.Get("output_text").String())
	addOutputIfNotEmpty(segments, root.Get("response.output_text").String())
	collectClaudeContent(root.Get("content"), segments)
	collectGeminiCandidates(root.Get("candidates"), segments)
	collectGeminiCandidates(root.Get("response.candidates"), segments)
}

func collectOpenAIChoices(choices gjson.Result, segments *[]string) {
	if !choices.Exists() || !choices.IsArray() {
		return
	}
	choices.ForEach(func(_, choice gjson.Result) bool {
		message := choice.Get("message")
		if message.Exists() {
			collectOutputContent(message.Get("content"), segments)
			collectOpenAIToolCalls(message.Get("tool_calls"), segments)
			collectOpenAIFunctionCall(message.Get("function_call"), segments)
		}
		delta := choice.Get("delta")
		if delta.Exists() {
			collectOutputContent(delta.Get("content"), segments)
			collectOpenAIToolCalls(delta.Get("tool_calls"), segments)
			collectOpenAIFunctionCall(delta.Get("function_call"), segments)
		}
		return true
	})
}

func collectResponseOutput(output gjson.Result, options outputExtractionOptions, segments *[]string) {
	if !output.Exists() {
		return
	}
	if output.IsArray() {
		output.ForEach(func(_, item gjson.Result) bool {
			collectResponseOutputItem(item, options, segments)
			return true
		})
		return
	}
	collectResponseOutputItem(output, options, segments)
}

func collectResponseOutputItem(item gjson.Result, options outputExtractionOptions, segments *[]string) {
	if !item.Exists() {
		return
	}
	switch item.Get("type").String() {
	case "message", "":
		if options.hasResponsesTextDelta {
			return
		}
		collectOutputContent(item.Get("content"), segments)
	case "function_call":
		if options.hasResponsesFunctionDelta {
			return
		}
		addOutputIfNotEmpty(segments, item.Get("name").String())
		addOutputIfNotEmpty(segments, item.Get("arguments").String())
	default:
		collectOutputContent(item.Get("content"), segments)
		addOutputIfNotEmpty(segments, item.Get("name").String())
		addOutputIfNotEmpty(segments, item.Get("arguments").String())
		addOutputIfNotEmpty(segments, item.Get("text").String())
	}
}

func collectClaudeContent(content gjson.Result, segments *[]string) {
	collectOutputContent(content, segments)
}

func collectClaudeDelta(delta gjson.Result, segments *[]string) {
	if !delta.Exists() {
		return
	}
	addOutputIfNotEmpty(segments, delta.Get("text").String())
	addOutputIfNotEmpty(segments, delta.Get("partial_json").String())
}

func collectClaudeContentBlock(block gjson.Result, segments *[]string) {
	if !block.Exists() {
		return
	}
	switch block.Get("type").String() {
	case "text":
		addOutputIfNotEmpty(segments, block.Get("text").String())
	case "tool_use":
		addOutputIfNotEmpty(segments, block.Get("name").String())
		if input := block.Get("input"); input.Exists() {
			addOutputIfNotEmpty(segments, input.Raw)
		}
	}
}

func collectGeminiCandidates(candidates gjson.Result, segments *[]string) {
	if !candidates.Exists() || !candidates.IsArray() {
		return
	}
	candidates.ForEach(func(_, candidate gjson.Result) bool {
		collectGeminiContent(candidate.Get("content"), segments)
		return true
	})
}

func collectGeminiContents(contents gjson.Result, segments *[]string) {
	if !contents.Exists() {
		return
	}
	if contents.IsArray() {
		contents.ForEach(func(_, content gjson.Result) bool {
			collectGeminiContent(content, segments)
			return true
		})
		return
	}
	collectGeminiContent(contents, segments)
}

func collectGeminiContent(content gjson.Result, segments *[]string) {
	if !content.Exists() {
		return
	}
	parts := content.Get("parts")
	if parts.Exists() && parts.IsArray() {
		parts.ForEach(func(_, part gjson.Result) bool {
			addOutputIfNotEmpty(segments, part.Get("text").String())
			if functionCall := part.Get("functionCall"); functionCall.Exists() {
				addOutputIfNotEmpty(segments, functionCall.Get("name").String())
				if args := functionCall.Get("args"); args.Exists() {
					addOutputIfNotEmpty(segments, args.Raw)
				}
			}
			if functionResponse := part.Get("functionResponse"); functionResponse.Exists() {
				addOutputIfNotEmpty(segments, functionResponse.Get("name").String())
				if response := functionResponse.Get("response"); response.Exists() {
					addOutputIfNotEmpty(segments, response.Raw)
				}
			}
			return true
		})
		return
	}
	collectOutputContent(content, segments)
}

func collectGeminiTools(tools gjson.Result, segments *[]string) {
	if !tools.Exists() {
		return
	}
	if tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			collectGeminiTool(tool, segments)
			return true
		})
		return
	}
	collectGeminiTool(tools, segments)
}

func collectGeminiTool(tool gjson.Result, segments *[]string) {
	if !tool.Exists() {
		return
	}
	declarations := tool.Get("functionDeclarations")
	if declarations.Exists() && declarations.IsArray() {
		declarations.ForEach(func(_, declaration gjson.Result) bool {
			addIfNotEmpty(segments, declaration.Get("name").String())
			addIfNotEmpty(segments, declaration.Get("description").String())
			if params := declaration.Get("parameters"); params.Exists() {
				addIfNotEmpty(segments, params.Raw)
			}
			return true
		})
	}
}

func collectOutputContent(content gjson.Result, segments *[]string) {
	if !content.Exists() {
		return
	}
	if content.Type == gjson.String {
		addOutputIfNotEmpty(segments, content.String())
		return
	}
	if content.IsArray() {
		content.ForEach(func(_, part gjson.Result) bool {
			collectOutputContent(part, segments)
			return true
		})
		return
	}
	addOutputIfNotEmpty(segments, content.Get("text").String())
	addOutputIfNotEmpty(segments, content.Get("delta").String())
	addOutputIfNotEmpty(segments, content.Get("transcript").String())
	if content.Get("type").String() == "tool_use" {
		addOutputIfNotEmpty(segments, content.Get("name").String())
		if input := content.Get("input"); input.Exists() {
			addOutputIfNotEmpty(segments, input.Raw)
		}
	}
	if content.Get("type").String() == "function_call" {
		addOutputIfNotEmpty(segments, content.Get("name").String())
		addOutputIfNotEmpty(segments, content.Get("arguments").String())
	}
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

func addOutputIfNotEmpty(segments *[]string, value string) {
	if segments == nil {
		return
	}
	if strings.TrimSpace(value) != "" {
		*segments = append(*segments, value)
	}
}
