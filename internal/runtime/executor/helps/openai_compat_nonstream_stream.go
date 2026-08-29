package helps

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ShouldForceNonStreamToolCalls reports whether a streaming request must be sent
// to the upstream as a non-streaming call.
//
// Some OpenAI-compatible aggregators truncate tool call arguments when streaming:
// they emit the tool name with an empty "arguments" string and then close the
// stream, which leaves the client with an unusable tool call. The same request
// answered without streaming returns complete arguments, so the request is routed
// through the non-streaming endpoint and the SSE frames are synthesized locally.
//
// The rewrite only applies when the payload actually declares tools, so plain
// chat completions keep their native token-by-token streaming.
func ShouldForceNonStreamToolCalls(compat *config.OpenAICompatibility, payload []byte) bool {
	if compat == nil || !compat.NonStreamToolCalls {
		return false
	}
	tools := gjson.GetBytes(payload, "tools")
	return tools.Exists() && tools.IsArray() && len(tools.Array()) > 0
}

// SynthesizeOpenAIStreamBootstrapFrame builds the assistant-role opening chunk
// used to release downstream bootstrap while a non-streaming upstream body is
// still being generated.
func SynthesizeOpenAIStreamBootstrapFrame(model string) string {
	frame := []byte(`{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
	frame, _ = sjson.SetBytes(frame, "model", model)
	return string(frame)
}

// SynthesizeOpenAIStreamFrames converts a non-streaming OpenAI chat completion
// response into the sequence of SSE data frames an equivalent streaming call
// would have produced. The returned frames are raw JSON payloads without the
// "data: " prefix, terminated by a "[DONE]" sentinel, matching the frame format
// used by the provider response cache.
func SynthesizeOpenAIStreamFrames(body []byte) []string {
	if len(body) == 0 {
		return nil
	}
	root := gjson.ParseBytes(body)
	choices := root.Get("choices")
	if !choices.Exists() || !choices.IsArray() {
		return nil
	}

	id := root.Get("id").String()
	model := root.Get("model").String()
	created := root.Get("created").Int()
	usage := root.Get("usage")

	frames := make([]string, 0, 8)
	emit := func(index int64, delta string, finishReason gjson.Result, withUsage bool) {
		frame := []byte(`{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{}}]}`)
		frame, _ = sjson.SetBytes(frame, "id", id)
		frame, _ = sjson.SetBytes(frame, "model", model)
		frame, _ = sjson.SetBytes(frame, "created", created)
		frame, _ = sjson.SetBytes(frame, "choices.0.index", index)
		if delta != "" {
			frame, _ = sjson.SetRawBytes(frame, "choices.0.delta", []byte(delta))
		}
		if finishReason.Exists() && finishReason.String() != "" {
			frame, _ = sjson.SetBytes(frame, "choices.0.finish_reason", finishReason.String())
		} else {
			frame, _ = sjson.SetRawBytes(frame, "choices.0.finish_reason", []byte("null"))
		}
		if withUsage && usage.Exists() {
			frame, _ = sjson.SetRawBytes(frame, "usage", []byte(usage.Raw))
		}
		frames = append(frames, string(frame))
	}

	choices.ForEach(func(position, choice gjson.Result) bool {
		index := choice.Get("index").Int()
		message := choice.Get("message")
		finishReason := choice.Get("finish_reason")

		if reasoning := message.Get("reasoning_content"); reasoning.Exists() && reasoning.String() != "" {
			emit(index, fmt.Sprintf(`{"reasoning_content":%s}`, reasoning.Raw), gjson.Result{}, false)
		}
		if refusal := message.Get("refusal"); refusal.Exists() && refusal.Type != gjson.Null {
			emit(index, fmt.Sprintf(`{"refusal":%s}`, refusal.Raw), gjson.Result{}, false)
		}
		if content := message.Get("content"); content.Exists() && content.String() != "" {
			emit(index, fmt.Sprintf(`{"content":%s}`, content.Raw), gjson.Result{}, false)
		}
		if toolCalls := message.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
			if delta := buildToolCallsDelta(toolCalls); delta != "" {
				emit(index, delta, gjson.Result{}, false)
			}
		}

		// The terminal chunk carries the finish reason and, for the first choice,
		// the usage block that stream_options.include_usage would have appended.
		// Keying on the array position instead of the reported index keeps usage
		// single even when an upstream omits the index field on every choice.
		emit(index, "", finishReason, position.Int() == 0)
		return true
	})

	if len(frames) == 0 {
		return nil
	}
	return append(frames, "[DONE]")
}

// buildToolCallsDelta rewrites a non-streaming tool_calls array into a single
// streaming delta that carries the complete arguments for every call.
func buildToolCallsDelta(toolCalls gjson.Result) string {
	delta := []byte(`{"tool_calls":[]}`)
	position := 0
	toolCalls.ForEach(func(_, call gjson.Result) bool {
		entry := []byte(`{}`)
		entry, _ = sjson.SetBytes(entry, "index", position)
		if id := call.Get("id"); id.Exists() {
			entry, _ = sjson.SetBytes(entry, "id", id.String())
		}
		callType := call.Get("type").String()
		if callType == "" {
			callType = "function"
		}
		entry, _ = sjson.SetBytes(entry, "type", callType)
		entry, _ = sjson.SetBytes(entry, "function.name", call.Get("function.name").String())
		entry, _ = sjson.SetBytes(entry, "function.arguments", call.Get("function.arguments").String())

		updated, err := sjson.SetRawBytes(delta, "tool_calls.-1", entry)
		if err != nil {
			return true
		}
		delta = updated
		position++
		return true
	})
	if position == 0 {
		return ""
	}
	return string(delta)
}
