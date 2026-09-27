package websearch

import (
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// synthesisSupported reports whether a final non-streaming response can be
// re-emitted as downstream wire chunks for streaming fallback requests.
func synthesisSupported(format string) bool {
	switch normalizeFallbackFormat(format) {
	case FallbackFormatClaude, FallbackFormatOpenAI, FallbackFormatOpenAIResponse, FallbackFormatCodex, FallbackFormatGemini:
		return true
	default:
		return false
	}
}

// SynthesizeStream converts a final non-streaming response body into
// downstream wire chunks matching what executors emit for live streams:
// framed SSE for Claude and Responses, bare JSON for OpenAI chat and
// Gemini (their handlers add framing).
func SynthesizeStream(format, model string, body []byte) ([][]byte, error) {
	if !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("invalid response body")
	}
	switch normalizeFallbackFormat(format) {
	case FallbackFormatClaude:
		return synthesizeClaudeStream(body), nil
	case FallbackFormatOpenAI:
		return synthesizeChatStream(model, body), nil
	case FallbackFormatOpenAIResponse, FallbackFormatCodex:
		return synthesizeResponsesStream(model, body), nil
	case FallbackFormatGemini:
		return [][]byte{body}, nil
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}
}

func sseFramed(event string, payload []byte, trailingNewlines int) []byte {
	out := make([]byte, 0, len(event)+len(payload)+16)
	out = append(out, "event: "...)
	out = append(out, event...)
	out = append(out, '\n')
	out = append(out, "data: "...)
	out = append(out, payload...)
	for range trailingNewlines {
		out = append(out, '\n')
	}
	return out
}

func synthesizeClaudeStream(body []byte) [][]byte {
	root := gjson.ParseBytes(body)
	messageID := root.Get("id").String()
	if messageID == "" {
		messageID = fmt.Sprintf("msg_proxy_%d", time.Now().UnixNano())
	}
	model := root.Get("model").String()
	usage := root.Get("usage")
	startUsage := []byte(`{"input_tokens":0,"output_tokens":0}`)
	if usage.Exists() {
		startUsage, _ = sjson.SetBytes(startUsage, "input_tokens", usage.Get("input_tokens").Int())
		for _, field := range []string{"cache_creation_input_tokens", "cache_read_input_tokens"} {
			if value := usage.Get(field); value.Exists() {
				startUsage, _ = sjson.SetBytes(startUsage, field, value.Int())
			}
		}
	}
	start := []byte(`{"type":"message_start","message":{"id":"","type":"message","role":"assistant","content":[],"model":"","stop_reason":null,"usage":{}}}`)
	start, _ = sjson.SetBytes(start, "message.id", messageID)
	start, _ = sjson.SetBytes(start, "message.model", model)
	start, _ = sjson.SetRawBytes(start, "message.usage", startUsage)
	chunks := [][]byte{sseFramed("message_start", start, 2)}

	blocks := root.Get("content")
	index := 0
	if blocks.IsArray() {
		for _, block := range blocks.Array() {
			chunks = append(chunks, synthesizeClaudeBlock(block, index)...)
			index++
		}
	}
	stopReason := strings.TrimSpace(root.Get("stop_reason").String())
	if stopReason == "" {
		stopReason = "end_turn"
	}
	finalUsage := []byte(`{"input_tokens":0,"output_tokens":0}`)
	if usage.Exists() {
		finalUsage = []byte(usage.Raw)
	}
	delta := []byte(`{"type":"message_delta","delta":{"stop_reason":"","stop_sequence":null},"usage":{}}`)
	delta, _ = sjson.SetBytes(delta, "delta.stop_reason", stopReason)
	delta, _ = sjson.SetRawBytes(delta, "usage", finalUsage)
	chunks = append(chunks, sseFramed("message_delta", delta, 2))
	chunks = append(chunks, sseFramed("message_stop", []byte(`{"type":"message_stop"}`), 2))
	return chunks
}

func synthesizeClaudeBlock(block gjson.Result, index int) [][]byte {
	blockType := block.Get("type").String()
	start := []byte(`{"type":"content_block_start","index":0,"content_block":{}}`)
	start, _ = sjson.SetBytes(start, "index", index)
	stop := []byte(`{"type":"content_block_stop","index":0}`)
	stop, _ = sjson.SetBytes(stop, "index", index)
	deltaFor := func(delta []byte) []byte {
		event, _ := sjson.SetBytes([]byte(`{"type":"content_block_delta","index":0}`), "index", index)
		event, _ = sjson.SetRawBytes(event, "delta", delta)
		return sseFramed("content_block_delta", event, 2)
	}
	switch blockType {
	case "text":
		start, _ = sjson.SetRawBytes(start, "content_block", []byte(`{"type":"text","text":""}`))
		delta, _ := sjson.SetBytes([]byte(`{"type":"text_delta","text":""}`), "text", block.Get("text").String())
		return [][]byte{sseFramed("content_block_start", start, 2), deltaFor(delta), sseFramed("content_block_stop", stop, 2)}
	case "tool_use":
		skeleton := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
		skeleton, _ = sjson.SetBytes(skeleton, "id", block.Get("id").String())
		skeleton, _ = sjson.SetBytes(skeleton, "name", block.Get("name").String())
		start, _ = sjson.SetRawBytes(start, "content_block", skeleton)
		partial := "{}"
		if input := block.Get("input"); input.Exists() {
			partial = input.Raw
			if input.Type == gjson.String {
				partial = input.String()
			}
		}
		delta, _ := sjson.SetBytes([]byte(`{"type":"input_json_delta","partial_json":""}`), "partial_json", partial)
		return [][]byte{sseFramed("content_block_start", start, 2), deltaFor(delta), sseFramed("content_block_stop", stop, 2)}
	case "thinking":
		start, _ = sjson.SetRawBytes(start, "content_block", []byte(`{"type":"thinking","thinking":""}`))
		chunks := [][]byte{sseFramed("content_block_start", start, 2)}
		thinking, _ := sjson.SetBytes([]byte(`{"type":"thinking_delta","thinking":""}`), "thinking", block.Get("thinking").String())
		chunks = append(chunks, deltaFor(thinking))
		if signature := block.Get("signature").String(); signature != "" {
			signatureDelta, _ := sjson.SetBytes([]byte(`{"type":"signature_delta","signature":""}`), "signature", signature)
			chunks = append(chunks, deltaFor(signatureDelta))
		}
		return append(chunks, sseFramed("content_block_stop", stop, 2))
	case "redacted_thinking":
		skeleton := []byte(`{"type":"redacted_thinking","data":""}`)
		skeleton, _ = sjson.SetBytes(skeleton, "data", block.Get("data").String())
		start, _ = sjson.SetRawBytes(start, "content_block", skeleton)
		return [][]byte{sseFramed("content_block_start", start, 2), sseFramed("content_block_stop", stop, 2)}
	default:
		return nil
	}
}

func synthesizeChatStream(model string, body []byte) [][]byte {
	root := gjson.ParseBytes(body)
	id := root.Get("id").String()
	if id == "" {
		id = fmt.Sprintf("chatcmpl-proxy-%d", time.Now().UnixNano())
	}
	created := root.Get("created").Int()
	if created == 0 {
		created = time.Now().Unix()
	}
	if modelName := strings.TrimSpace(root.Get("model").String()); modelName != "" {
		model = modelName
	}
	base := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{},"finish_reason":null}]}`)
	base, _ = sjson.SetBytes(base, "id", id)
	base, _ = sjson.SetBytes(base, "created", created)
	base, _ = sjson.SetBytes(base, "model", model)
	role, _ := sjson.SetBytes([]byte(`{}`), "role", "assistant")
	first, _ := sjson.SetRawBytes(append([]byte(nil), base...), "choices.0.delta", role)
	chunks := [][]byte{first}

	message := root.Get("choices.0.message")
	if text := message.Get("content").String(); strings.TrimSpace(text) != "" {
		content, _ := sjson.SetBytes([]byte(`{}`), "content", message.Get("content").String())
		chunk, _ := sjson.SetRawBytes(append([]byte(nil), base...), "choices.0.delta", content)
		chunks = append(chunks, chunk)
	}
	toolCalls := message.Get("tool_calls")
	if toolCalls.IsArray() && len(toolCalls.Array()) > 0 {
		calls := make([][]byte, 0, len(toolCalls.Array()))
		for callIndex, call := range toolCalls.Array() {
			entry := []byte(`{"index":0,"id":"","type":"function","function":{"name":"","arguments":""}}`)
			entry, _ = sjson.SetBytes(entry, "index", callIndex)
			entry, _ = sjson.SetBytes(entry, "id", call.Get("id").String())
			entry, _ = sjson.SetBytes(entry, "function.name", call.Get("function.name").String())
			entry, _ = sjson.SetBytes(entry, "function.arguments", call.Get("function.arguments").String())
			calls = append(calls, entry)
		}
		delta, _ := sjson.SetRawBytes([]byte(`{}`), "tool_calls", joinRawJSONArray(calls))
		chunk, _ := sjson.SetRawBytes(append([]byte(nil), base...), "choices.0.delta", delta)
		chunks = append(chunks, chunk)
	}
	finishReason := strings.TrimSpace(root.Get("choices.0.finish_reason").String())
	if finishReason == "" || finishReason == "null" {
		if toolCalls.IsArray() && len(toolCalls.Array()) > 0 {
			finishReason = "tool_calls"
		} else {
			finishReason = "stop"
		}
	}
	terminal, _ := sjson.SetRawBytes(append([]byte(nil), base...), "choices.0.delta", []byte(`{}`))
	terminal, _ = sjson.SetBytes(terminal, "choices.0.finish_reason", finishReason)
	return append(chunks, terminal)
}

func synthesizeResponsesStream(model string, body []byte) [][]byte {
	root := gjson.ParseBytes(body)
	responseID := root.Get("id").String()
	if responseID == "" {
		responseID = fmt.Sprintf("resp_proxy_%d", time.Now().UnixNano())
	}
	createdAt := root.Get("created_at").Int()
	if createdAt == 0 {
		createdAt = root.Get("created").Int()
	}
	if createdAt == 0 {
		createdAt = time.Now().Unix()
	}
	if modelName := strings.TrimSpace(root.Get("model").String()); modelName != "" {
		model = modelName
	}
	sequence := 0
	nextSeq := func() int {
		value := sequence
		sequence++
		return value
	}
	created := []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null,"output":[]}}`)
	created, _ = sjson.SetBytes(created, "sequence_number", nextSeq())
	created, _ = sjson.SetBytes(created, "response.id", responseID)
	created, _ = sjson.SetBytes(created, "response.created_at", createdAt)
	created, _ = sjson.SetBytes(created, "response.model", model)
	inProgress := []byte(`{"type":"response.in_progress","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","output":[]}}`)
	inProgress, _ = sjson.SetBytes(inProgress, "sequence_number", nextSeq())
	inProgress, _ = sjson.SetBytes(inProgress, "response.id", responseID)
	inProgress, _ = sjson.SetBytes(inProgress, "response.created_at", createdAt)
	inProgress, _ = sjson.SetBytes(inProgress, "response.model", model)
	chunks := [][]byte{
		sseFramed("response.created", created, 0),
		sseFramed("response.in_progress", inProgress, 0),
	}

	output := root.Get("output")
	outputIndex := 0
	if output.IsArray() {
		for _, item := range output.Array() {
			chunks = append(chunks, synthesizeResponsesItem(item, outputIndex, &sequence)...)
			outputIndex++
		}
	}
	completedResponse := []byte(`{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"model":"","output":[]}`)
	completedResponse, _ = sjson.SetBytes(completedResponse, "id", responseID)
	completedResponse, _ = sjson.SetBytes(completedResponse, "created_at", createdAt)
	completedResponse, _ = sjson.SetBytes(completedResponse, "model", model)
	if output.IsArray() {
		completedResponse, _ = sjson.SetRawBytes(completedResponse, "output", []byte(output.Raw))
	}
	if usage := root.Get("usage"); usage.Exists() {
		completedResponse, _ = sjson.SetRawBytes(completedResponse, "usage", []byte(usage.Raw))
	}
	completed := []byte(`{"type":"response.completed","sequence_number":0,"response":{}}`)
	completed, _ = sjson.SetBytes(completed, "sequence_number", nextSeq())
	completed, _ = sjson.SetRawBytes(completed, "response", completedResponse)
	return append(chunks, sseFramed("response.completed", completed, 0))
}

func synthesizeResponsesItem(item gjson.Result, outputIndex int, sequence *int) [][]byte {
	nextSeq := func() int {
		value := *sequence
		*sequence++
		return value
	}
	itemType := item.Get("type").String()
	itemID := item.Get("id").String()
	added := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{}}`)
	added, _ = sjson.SetBytes(added, "sequence_number", nextSeq())
	added, _ = sjson.SetBytes(added, "output_index", outputIndex)
	skeleton := []byte(`{"id":"","type":"","status":"in_progress"}`)
	skeleton, _ = sjson.SetBytes(skeleton, "id", itemID)
	skeleton, _ = sjson.SetBytes(skeleton, "type", itemType)
	switch itemType {
	case "message":
		skeleton, _ = sjson.SetBytes(skeleton, "role", "assistant")
		skeleton, _ = sjson.SetRawBytes(skeleton, "content", []byte(`[]`))
	case "function_call", "custom_tool_call":
		skeleton, _ = sjson.SetBytes(skeleton, "call_id", item.Get("call_id").String())
		skeleton, _ = sjson.SetBytes(skeleton, "name", item.Get("name").String())
		skeleton, _ = sjson.SetBytes(skeleton, "arguments", "")
	}
	added, _ = sjson.SetRawBytes(added, "item", skeleton)
	chunks := [][]byte{sseFramed("response.output_item.added", added, 0)}

	switch itemType {
	case "message":
		content := item.Get("content")
		if content.IsArray() {
			for contentIndex, part := range content.Array() {
				if part.Get("type").String() != "output_text" {
					continue
				}
				partAdded := []byte(`{"type":"response.content_part.added","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
				partAdded, _ = sjson.SetBytes(partAdded, "sequence_number", nextSeq())
				partAdded, _ = sjson.SetBytes(partAdded, "item_id", itemID)
				partAdded, _ = sjson.SetBytes(partAdded, "output_index", outputIndex)
				partAdded, _ = sjson.SetBytes(partAdded, "content_index", contentIndex)
				chunks = append(chunks, sseFramed("response.content_part.added", partAdded, 0))
				text := part.Get("text").String()
				delta := []byte(`{"type":"response.output_text.delta","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"delta":"","logprobs":[]}`)
				delta, _ = sjson.SetBytes(delta, "sequence_number", nextSeq())
				delta, _ = sjson.SetBytes(delta, "item_id", itemID)
				delta, _ = sjson.SetBytes(delta, "output_index", outputIndex)
				delta, _ = sjson.SetBytes(delta, "content_index", contentIndex)
				delta, _ = sjson.SetBytes(delta, "delta", text)
				chunks = append(chunks, sseFramed("response.output_text.delta", delta, 0))
				textDone := []byte(`{"type":"response.output_text.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"text":"","logprobs":[]}`)
				textDone, _ = sjson.SetBytes(textDone, "sequence_number", nextSeq())
				textDone, _ = sjson.SetBytes(textDone, "item_id", itemID)
				textDone, _ = sjson.SetBytes(textDone, "output_index", outputIndex)
				textDone, _ = sjson.SetBytes(textDone, "content_index", contentIndex)
				textDone, _ = sjson.SetBytes(textDone, "text", text)
				chunks = append(chunks, sseFramed("response.output_text.done", textDone, 0))
				partDone := []byte(`{"type":"response.content_part.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
				partDone, _ = sjson.SetBytes(partDone, "sequence_number", nextSeq())
				partDone, _ = sjson.SetBytes(partDone, "item_id", itemID)
				partDone, _ = sjson.SetBytes(partDone, "output_index", outputIndex)
				partDone, _ = sjson.SetBytes(partDone, "content_index", contentIndex)
				partDone, _ = sjson.SetBytes(partDone, "part.text", text)
				if annotations := part.Get("annotations"); annotations.IsArray() {
					partDone, _ = sjson.SetRawBytes(partDone, "part.annotations", []byte(annotations.Raw))
				}
				chunks = append(chunks, sseFramed("response.content_part.done", partDone, 0))
			}
		}
	case "function_call", "custom_tool_call":
		arguments := item.Get("arguments").String()
		delta := []byte(`{"type":"response.function_call_arguments.delta","sequence_number":0,"item_id":"","output_index":0,"delta":""}`)
		delta, _ = sjson.SetBytes(delta, "sequence_number", nextSeq())
		delta, _ = sjson.SetBytes(delta, "item_id", itemID)
		delta, _ = sjson.SetBytes(delta, "output_index", outputIndex)
		delta, _ = sjson.SetBytes(delta, "delta", arguments)
		chunks = append(chunks, sseFramed("response.function_call_arguments.delta", delta, 0))
	}

	done := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{}}`)
	done, _ = sjson.SetBytes(done, "sequence_number", nextSeq())
	done, _ = sjson.SetBytes(done, "output_index", outputIndex)
	completed, _ := sjson.SetBytes([]byte(item.Raw), "status", "completed")
	done, _ = sjson.SetRawBytes(done, "item", completed)
	return append(chunks, sseFramed("response.output_item.done", done, 0))
}
