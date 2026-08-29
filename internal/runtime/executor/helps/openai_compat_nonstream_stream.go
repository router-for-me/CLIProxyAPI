package helps

import (
	"fmt"
	"strings"

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

// SynthesizeOpenAIStreamFrames converts a non-streaming OpenAI chat completion
// response into the sequence of SSE data frames an equivalent streaming call
// would have produced. The returned frames are raw JSON payloads without the
// "data: " prefix, terminated by a "[DONE]" sentinel, matching the frame format
// used by the provider response cache.
func SynthesizeOpenAIStreamFrames(body []byte) []string {
	if len(body) == 0 {
		return nil
	}
	if !gjson.ValidBytes(body) {
		return nil
	}
	root := gjson.ParseBytes(body)
	choices := root.Get("choices")
	if !choices.Exists() || !choices.IsArray() || len(choices.Array()) == 0 {
		return nil
	}
	// Every choice must carry a usable assistant message. Rejecting placeholder
	// entries such as {"choices":[{}]} keeps the caller's bootstrap error path
	// intact so credential and model fallback can still run.
	for _, choice := range choices.Array() {
		if !hasUsableAssistantChoice(choice) {
			return nil
		}
	}

	id := root.Get("id").String()
	model := root.Get("model").String()
	created := root.Get("created").Int()
	serviceTier := root.Get("service_tier")
	systemFingerprint := root.Get("system_fingerprint")
	usage := root.Get("usage")

	frames := make([]string, 0, 8)
	emit := func(index int64, delta string, finishReason, logprobs gjson.Result, withUsage bool) {
		frame := []byte(`{"object":"chat.completion.chunk","choices":[{"index":0,"delta":{}}]}`)
		frame, _ = sjson.SetBytes(frame, "id", id)
		frame, _ = sjson.SetBytes(frame, "model", model)
		frame, _ = sjson.SetBytes(frame, "created", created)
		if serviceTier.Exists() && serviceTier.Type != gjson.Null {
			frame, _ = sjson.SetRawBytes(frame, "service_tier", []byte(serviceTier.Raw))
		}
		if systemFingerprint.Exists() && systemFingerprint.Type != gjson.Null {
			frame, _ = sjson.SetRawBytes(frame, "system_fingerprint", []byte(systemFingerprint.Raw))
		}
		frame, _ = sjson.SetBytes(frame, "choices.0.index", index)
		if delta != "" {
			frame, _ = sjson.SetRawBytes(frame, "choices.0.delta", []byte(delta))
		}
		if finishReason.Exists() && finishReason.String() != "" {
			frame, _ = sjson.SetBytes(frame, "choices.0.finish_reason", finishReason.String())
		} else {
			frame, _ = sjson.SetRawBytes(frame, "choices.0.finish_reason", []byte("null"))
		}
		if logprobs.Exists() && logprobs.Type != gjson.Null {
			frame, _ = sjson.SetRawBytes(frame, "choices.0.logprobs", []byte(logprobs.Raw))
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
		logprobs := choice.Get("logprobs")

		emit(index, `{"role":"assistant"}`, gjson.Result{}, gjson.Result{}, false)

		if reasoning := message.Get("reasoning_content"); reasoning.Exists() && reasoning.String() != "" {
			emit(index, fmt.Sprintf(`{"reasoning_content":%s}`, reasoning.Raw), gjson.Result{}, gjson.Result{}, false)
		} else if reasoning = message.Get("reasoning"); reasoning.Exists() && reasoning.String() != "" {
			emit(index, fmt.Sprintf(`{"reasoning":%s}`, reasoning.Raw), gjson.Result{}, gjson.Result{}, false)
		}
		if refusal := message.Get("refusal"); refusal.Exists() && refusal.Type != gjson.Null {
			emit(index, fmt.Sprintf(`{"refusal":%s}`, refusal.Raw), gjson.Result{}, gjson.Result{}, false)
		}
		contentEmitted := false
		if content := message.Get("content"); content.Exists() && content.String() != "" {
			delta := fmt.Sprintf(`{"content":%s}`, content.Raw)
			if annotations := message.Get("annotations"); annotations.Exists() && annotations.Type != gjson.Null {
				if updated, errSet := sjson.SetRaw(delta, "annotations", annotations.Raw); errSet == nil {
					delta = updated
				}
			}
			emit(index, delta, gjson.Result{}, logprobs, false)
			contentEmitted = true
		}
		if audio := message.Get("audio"); audio.Exists() && audio.Type != gjson.Null {
			emit(index, fmt.Sprintf(`{"audio":%s}`, audio.Raw), gjson.Result{}, gjson.Result{}, false)
			contentEmitted = true
		}
		if !contentEmitted && logprobs.Exists() && logprobs.Type != gjson.Null {
			emit(index, "", gjson.Result{}, logprobs, false)
		}
		if toolCalls := message.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
			if delta := buildToolCallsDelta(toolCalls); delta != "" {
				emit(index, delta, gjson.Result{}, gjson.Result{}, false)
			}
		}

		// The terminal chunk carries the finish reason and, for the first choice,
		// the usage block that stream_options.include_usage would have appended.
		// Keying on the array position instead of the reported index keeps usage
		// single even when an upstream omits the index field on every choice.
		emit(index, "", finishReason, gjson.Result{}, position.Int() == 0)
		return true
	})

	if len(frames) == 0 {
		return nil
	}
	return append(frames, "[DONE]")
}

// hasUsableAssistantChoice reports whether a non-streaming choice carries
// something the synthesizer can replay as streaming deltas. A choice with an
// empty message is still accepted when the upstream reported a terminal reason,
// because an empty assistant reply is a legitimate completion.
func hasUsableAssistantChoice(choice gjson.Result) bool {
	message := choice.Get("message")
	if !message.Exists() || !message.IsObject() {
		return false
	}
	if content := message.Get("content"); content.Exists() && content.String() != "" {
		return true
	}
	for _, field := range []string{"reasoning_content", "reasoning"} {
		if value := message.Get(field); value.Exists() && value.String() != "" {
			return true
		}
	}
	if audio := message.Get("audio"); audio.Exists() && audio.Type != gjson.Null {
		return true
	}
	if refusal := message.Get("refusal"); refusal.Exists() && refusal.Type != gjson.Null {
		return true
	}
	if toolCalls := message.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() && len(toolCalls.Array()) > 0 {
		return hasUsableToolCalls(toolCalls)
	}
	return choice.Get("finish_reason").String() != ""
}

// hasUsableToolCalls reports whether every reported call carries function data
// the client can act on. Placeholder entries such as tool_calls:[{}] are treated
// as an unusable reply so the caller can fall back to another credential.
func hasUsableToolCalls(toolCalls gjson.Result) bool {
	usable := true
	toolCalls.ForEach(func(_, call gjson.Result) bool {
		if !call.IsObject() || call.Get("function.name").String() == "" {
			usable = false
			return false
		}
		// The truncation this option recovers from shows up as empty or partial
		// arguments, so a call is only usable when it carries valid JSON.
		arguments := strings.TrimSpace(call.Get("function.arguments").String())
		if arguments == "" || !gjson.Valid(arguments) {
			usable = false
			return false
		}
		return true
	})
	return usable
}

// buildToolCallsDelta rewrites a non-streaming tool_calls array into a single
// streaming delta that carries the complete arguments for every call.
func buildToolCallsDelta(toolCalls gjson.Result) string {
	delta := []byte(`{"tool_calls":[]}`)
	position := 0
	toolCalls.ForEach(func(_, call gjson.Result) bool {
		// Start from the upstream call so provider metadata such as Gemini's
		// extra_content.google.thought_signature survives the rewrite.
		entry := []byte(call.Raw)
		if !call.IsObject() {
			entry = []byte(`{}`)
		}
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
