package responses

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type claudeToResponsesState struct {
	Seq          int
	ResponseID   string
	CreatedAt    int64
	CurrentMsgID string
	CurrentFCID  string
	InTextBlock  bool
	InFuncBlock  bool
	FuncArgsBuf  map[int]*strings.Builder // index -> args
	// function call bookkeeping for output aggregation
	FuncNames   map[int]string // index -> function name
	FuncCallIDs map[int]string // index -> call id
	// message text aggregation
	TextBuf strings.Builder
	// reasoning state
	ReasoningActive    bool
	ReasoningItemID    string
	ReasoningBuf       strings.Builder
	ReasoningPartAdded bool
	ReasoningIndex     int
	// usage aggregation
	InputTokens  int64
	OutputTokens int64
	UsageSeen    bool
}

var dataTag = []byte("data:")

func pickRequestJSON(originalRequestRawJSON, requestRawJSON []byte) []byte {
	if len(originalRequestRawJSON) > 0 && gjson.ValidBytes(originalRequestRawJSON) {
		return originalRequestRawJSON
	}
	if len(requestRawJSON) > 0 && gjson.ValidBytes(requestRawJSON) {
		return requestRawJSON
	}
	return nil
}

func emitEvent(event string, payload []byte) []byte {
	return translatorcommon.SSEEventData(event, payload)
}

// ConvertClaudeResponseToOpenAIResponses converts Claude SSE to OpenAI Responses SSE events.
func ConvertClaudeResponseToOpenAIResponses(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &claudeToResponsesState{FuncArgsBuf: make(map[int]*strings.Builder), FuncNames: make(map[int]string), FuncCallIDs: make(map[int]string)}
	}
	st := (*param).(*claudeToResponsesState)

	// Expect `data: {..}` from Claude clients
	if !bytes.HasPrefix(rawJSON, dataTag) {
		return [][]byte{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])
	root := gjson.ParseBytes(rawJSON)
	ev := root.Get("type").String()
	var out [][]byte

	nextSeq := func() int { st.Seq++; return st.Seq }

	switch ev {
	case "message_start":
		if msg := root.Get("message"); msg.Exists() {
			st.ResponseID = msg.Get("id").String()
			st.CreatedAt = time.Now().Unix()
			// Reset per-message aggregation state
			st.TextBuf.Reset()
			st.ReasoningBuf.Reset()
			st.ReasoningActive = false
			st.InTextBlock = false
			st.InFuncBlock = false
			st.CurrentMsgID = ""
			st.CurrentFCID = ""
			st.ReasoningItemID = ""
			st.ReasoningIndex = 0
			st.ReasoningPartAdded = false
			st.FuncArgsBuf = make(map[int]*strings.Builder)
			st.FuncNames = make(map[int]string)
			st.FuncCallIDs = make(map[int]string)
			st.InputTokens = 0
			st.OutputTokens = 0
			st.UsageSeen = false
			if usage := msg.Get("usage"); usage.Exists() {
				if v := usage.Get("input_tokens"); v.Exists() {
					st.InputTokens = v.Int()
					st.UsageSeen = true
				}
				if v := usage.Get("output_tokens"); v.Exists() {
					st.OutputTokens = v.Int()
					st.UsageSeen = true
				}
			}
			// response.created
			created := []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null,"output":[]}}`)
			created, _ = sjson.SetBytes(created, "sequence_number", nextSeq())
			created, _ = sjson.SetBytes(created, "response.id", st.ResponseID)
			created, _ = sjson.SetBytes(created, "response.created_at", st.CreatedAt)
			out = append(out, emitEvent("response.created", created))
			// response.in_progress
			inprog := []byte(`{"type":"response.in_progress","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress"}}`)
			inprog, _ = sjson.SetBytes(inprog, "sequence_number", nextSeq())
			inprog, _ = sjson.SetBytes(inprog, "response.id", st.ResponseID)
			inprog, _ = sjson.SetBytes(inprog, "response.created_at", st.CreatedAt)
			out = append(out, emitEvent("response.in_progress", inprog))
		}
	case "content_block_start":
		cb := root.Get("content_block")
		if !cb.Exists() {
			return out
		}
		idx := int(root.Get("index").Int())
		typ := cb.Get("type").String()
		if typ == "text" {
			// open message item + content part
			st.InTextBlock = true
			st.CurrentMsgID = fmt.Sprintf("msg_%s_0", st.ResponseID)
			item := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"in_progress","content":[],"role":"assistant"}}`)
			item, _ = sjson.SetBytes(item, "sequence_number", nextSeq())
			item, _ = sjson.SetBytes(item, "item.id", st.CurrentMsgID)
			out = append(out, emitEvent("response.output_item.added", item))

			part := []byte(`{"type":"response.content_part.added","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
			part, _ = sjson.SetBytes(part, "sequence_number", nextSeq())
			part, _ = sjson.SetBytes(part, "item_id", st.CurrentMsgID)
			out = append(out, emitEvent("response.content_part.added", part))
		} else if typ == "tool_use" {
			st.InFuncBlock = true
			st.CurrentFCID = cb.Get("id").String()
			name := cb.Get("name").String()
			item := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"in_progress","arguments":"","call_id":"","name":""}}`)
			item, _ = sjson.SetBytes(item, "sequence_number", nextSeq())
			item, _ = sjson.SetBytes(item, "output_index", idx)
			item, _ = sjson.SetBytes(item, "item.id", fmt.Sprintf("fc_%s", st.CurrentFCID))
			item, _ = sjson.SetBytes(item, "item.call_id", st.CurrentFCID)
			item, _ = sjson.SetBytes(item, "item.name", name)
			out = append(out, emitEvent("response.output_item.added", item))
			if st.FuncArgsBuf[idx] == nil {
				st.FuncArgsBuf[idx] = &strings.Builder{}
			}
			// record function metadata for aggregation
			st.FuncCallIDs[idx] = st.CurrentFCID
			st.FuncNames[idx] = name
		} else if typ == "thinking" {
			// start reasoning item
			st.ReasoningActive = true
			st.ReasoningIndex = idx
			st.ReasoningBuf.Reset()
			st.ReasoningItemID = fmt.Sprintf("rs_%s_%d", st.ResponseID, idx)
			item := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"in_progress","summary":[]}}`)
			item, _ = sjson.SetBytes(item, "sequence_number", nextSeq())
			item, _ = sjson.SetBytes(item, "output_index", idx)
			item, _ = sjson.SetBytes(item, "item.id", st.ReasoningItemID)
			out = append(out, emitEvent("response.output_item.added", item))
			// add a summary part placeholder
			part := []byte(`{"type":"response.reasoning_summary_part.added","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`)
			part, _ = sjson.SetBytes(part, "sequence_number", nextSeq())
			part, _ = sjson.SetBytes(part, "item_id", st.ReasoningItemID)
			part, _ = sjson.SetBytes(part, "output_index", idx)
			out = append(out, emitEvent("response.reasoning_summary_part.added", part))
			st.ReasoningPartAdded = true
		}
	case "content_block_delta":
		d := root.Get("delta")
		if !d.Exists() {
			return out
		}
		dt := d.Get("type").String()
		if dt == "text_delta" {
			if t := d.Get("text"); t.Exists() {
				msg := []byte(`{"type":"response.output_text.delta","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"delta":"","logprobs":[]}`)
				msg, _ = sjson.SetBytes(msg, "sequence_number", nextSeq())
				msg, _ = sjson.SetBytes(msg, "item_id", st.CurrentMsgID)
				msg, _ = sjson.SetBytes(msg, "delta", t.String())
				out = append(out, emitEvent("response.output_text.delta", msg))
				// aggregate text for response.output
				st.TextBuf.WriteString(t.String())
			}
		} else if dt == "input_json_delta" {
			idx := int(root.Get("index").Int())
			if pj := d.Get("partial_json"); pj.Exists() {
				if st.FuncArgsBuf[idx] == nil {
					st.FuncArgsBuf[idx] = &strings.Builder{}
				}
				st.FuncArgsBuf[idx].WriteString(pj.String())
				msg := []byte(`{"type":"response.function_call_arguments.delta","sequence_number":0,"item_id":"","output_index":0,"delta":""}`)
				msg, _ = sjson.SetBytes(msg, "sequence_number", nextSeq())
				msg, _ = sjson.SetBytes(msg, "item_id", fmt.Sprintf("fc_%s", st.CurrentFCID))
				msg, _ = sjson.SetBytes(msg, "output_index", idx)
				msg, _ = sjson.SetBytes(msg, "delta", pj.String())
				out = append(out, emitEvent("response.function_call_arguments.delta", msg))
			}
		} else if dt == "thinking_delta" {
			if st.ReasoningActive {
				if t := d.Get("thinking"); t.Exists() {
					st.ReasoningBuf.WriteString(t.String())
					msg := []byte(`{"type":"response.reasoning_summary_text.delta","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"delta":""}`)
					msg, _ = sjson.SetBytes(msg, "sequence_number", nextSeq())
					msg, _ = sjson.SetBytes(msg, "item_id", st.ReasoningItemID)
					msg, _ = sjson.SetBytes(msg, "output_index", st.ReasoningIndex)
					msg, _ = sjson.SetBytes(msg, "delta", t.String())
					out = append(out, emitEvent("response.reasoning_summary_text.delta", msg))
				}
			}
		}
	case "content_block_stop":
		idx := int(root.Get("index").Int())
		if st.InTextBlock {
			done := []byte(`{"type":"response.output_text.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"text":"","logprobs":[]}`)
			done, _ = sjson.SetBytes(done, "sequence_number", nextSeq())
			done, _ = sjson.SetBytes(done, "item_id", st.CurrentMsgID)
			out = append(out, emitEvent("response.output_text.done", done))
			partDone := []byte(`{"type":"response.content_part.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
			partDone, _ = sjson.SetBytes(partDone, "sequence_number", nextSeq())
			partDone, _ = sjson.SetBytes(partDone, "item_id", st.CurrentMsgID)
			out = append(out, emitEvent("response.content_part.done", partDone))
			final := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"completed","content":[{"type":"output_text","text":""}],"role":"assistant"}}`)
			final, _ = sjson.SetBytes(final, "sequence_number", nextSeq())
			final, _ = sjson.SetBytes(final, "item.id", st.CurrentMsgID)
			out = append(out, emitEvent("response.output_item.done", final))
			st.InTextBlock = false
		} else if st.InFuncBlock {
			args := "{}"
			if buf := st.FuncArgsBuf[idx]; buf != nil {
				if buf.Len() > 0 {
					args = buf.String()
				}
			}
			fcDone := []byte(`{"type":"response.function_call_arguments.done","sequence_number":0,"item_id":"","output_index":0,"arguments":""}`)
			fcDone, _ = sjson.SetBytes(fcDone, "sequence_number", nextSeq())
			fcDone, _ = sjson.SetBytes(fcDone, "item_id", fmt.Sprintf("fc_%s", st.CurrentFCID))
			fcDone, _ = sjson.SetBytes(fcDone, "output_index", idx)
			fcDone, _ = sjson.SetBytes(fcDone, "arguments", args)
			out = append(out, emitEvent("response.function_call_arguments.done", fcDone))
			itemDone := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}}`)
			itemDone, _ = sjson.SetBytes(itemDone, "sequence_number", nextSeq())
			itemDone, _ = sjson.SetBytes(itemDone, "output_index", idx)
			itemDone, _ = sjson.SetBytes(itemDone, "item.id", fmt.Sprintf("fc_%s", st.CurrentFCID))
			itemDone, _ = sjson.SetBytes(itemDone, "item.arguments", args)
			itemDone, _ = sjson.SetBytes(itemDone, "item.call_id", st.CurrentFCID)
			itemDone, _ = sjson.SetBytes(itemDone, "item.name", st.FuncNames[idx])
			out = append(out, emitEvent("response.output_item.done", itemDone))
			st.InFuncBlock = false
		} else if st.ReasoningActive {
			full := st.ReasoningBuf.String()
			textDone := []byte(`{"type":"response.reasoning_summary_text.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"text":""}`)
			textDone, _ = sjson.SetBytes(textDone, "sequence_number", nextSeq())
			textDone, _ = sjson.SetBytes(textDone, "item_id", st.ReasoningItemID)
			textDone, _ = sjson.SetBytes(textDone, "output_index", st.ReasoningIndex)
			textDone, _ = sjson.SetBytes(textDone, "text", full)
			out = append(out, emitEvent("response.reasoning_summary_text.done", textDone))
			partDone := []byte(`{"type":"response.reasoning_summary_part.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`)
			partDone, _ = sjson.SetBytes(partDone, "sequence_number", nextSeq())
			partDone, _ = sjson.SetBytes(partDone, "item_id", st.ReasoningItemID)
			partDone, _ = sjson.SetBytes(partDone, "output_index", st.ReasoningIndex)
			partDone, _ = sjson.SetBytes(partDone, "part.text", full)
			out = append(out, emitEvent("response.reasoning_summary_part.done", partDone))
			st.ReasoningActive = false
			st.ReasoningPartAdded = false
		}
	case "message_delta":
		if usage := root.Get("usage"); usage.Exists() {
			if v := usage.Get("output_tokens"); v.Exists() {
				st.OutputTokens = v.Int()
				st.UsageSeen = true
			}
			if v := usage.Get("input_tokens"); v.Exists() {
				st.InputTokens = v.Int()
				st.UsageSeen = true
			}
		}
	case "message_stop":

		completed := []byte(`{"type":"response.completed","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null}}`)
		completed, _ = sjson.SetBytes(completed, "sequence_number", nextSeq())
		completed, _ = sjson.SetBytes(completed, "response.id", st.ResponseID)
		completed, _ = sjson.SetBytes(completed, "response.created_at", st.CreatedAt)
		// Inject original request fields into response as per docs/response.completed.json

		reqBytes := pickRequestJSON(originalRequestRawJSON, requestRawJSON)
		if len(reqBytes) > 0 {
			req := gjson.ParseBytes(reqBytes)
			if v := req.Get("instructions"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.instructions", v.String())
			}
			if v := req.Get("max_output_tokens"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.max_output_tokens", v.Int())
			}
			if v := req.Get("max_tool_calls"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.max_tool_calls", v.Int())
			}
			if v := req.Get("model"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.model", v.String())
			}
			if v := req.Get("parallel_tool_calls"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.parallel_tool_calls", v.Bool())
			}
			if v := req.Get("previous_response_id"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.previous_response_id", v.String())
			}
			if v := req.Get("prompt_cache_key"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.prompt_cache_key", v.String())
			}
			if v := req.Get("reasoning"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.reasoning", v.Value())
			}
			if v := req.Get("safety_identifier"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.safety_identifier", v.String())
			}
			if v := req.Get("service_tier"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.service_tier", v.String())
			}
			if v := req.Get("store"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.store", v.Bool())
			}
			if v := req.Get("temperature"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.temperature", v.Float())
			}
			if v := req.Get("text"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.text", v.Value())
			}
			if v := req.Get("tool_choice"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.tool_choice", v.Value())
			}
			if v := req.Get("tools"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.tools", v.Value())
			}
			if v := req.Get("top_logprobs"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.top_logprobs", v.Int())
			}
			if v := req.Get("top_p"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.top_p", v.Float())
			}
			if v := req.Get("truncation"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.truncation", v.String())
			}
			if v := req.Get("user"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.user", v.Value())
			}
			if v := req.Get("metadata"); v.Exists() {
				completed, _ = sjson.SetBytes(completed, "response.metadata", v.Value())
			}
		}

		// Build response.output from aggregated state
		outputsWrapper := []byte(`{"arr":[]}`)
		// reasoning item (if any)
		if st.ReasoningBuf.Len() > 0 || st.ReasoningPartAdded {
			item := []byte(`{"id":"","type":"reasoning","summary":[{"type":"summary_text","text":""}]}`)
			item, _ = sjson.SetBytes(item, "id", st.ReasoningItemID)
			item, _ = sjson.SetBytes(item, "summary.0.text", st.ReasoningBuf.String())
			outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, "arr.-1", item)
		}
		// assistant message item (if any text)
		if st.TextBuf.Len() > 0 || st.InTextBlock || st.CurrentMsgID != "" {
			item := []byte(`{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}`)
			item, _ = sjson.SetBytes(item, "id", st.CurrentMsgID)
			item, _ = sjson.SetBytes(item, "content.0.text", st.TextBuf.String())
			outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, "arr.-1", item)
		}
		// function_call items (in ascending index order for determinism)
		if len(st.FuncArgsBuf) > 0 {
			// collect indices
			idxs := make([]int, 0, len(st.FuncArgsBuf))
			for idx := range st.FuncArgsBuf {
				idxs = append(idxs, idx)
			}
			// simple sort (small N), avoid adding new imports
			for i := 0; i < len(idxs); i++ {
				for j := i + 1; j < len(idxs); j++ {
					if idxs[j] < idxs[i] {
						idxs[i], idxs[j] = idxs[j], idxs[i]
					}
				}
			}
			for _, idx := range idxs {
				args := ""
				if b := st.FuncArgsBuf[idx]; b != nil {
					args = b.String()
				}
				callID := st.FuncCallIDs[idx]
				name := st.FuncNames[idx]
				if callID == "" && st.CurrentFCID != "" {
					callID = st.CurrentFCID
				}
				item := []byte(`{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`)
				item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("fc_%s", callID))
				item, _ = sjson.SetBytes(item, "arguments", args)
				item, _ = sjson.SetBytes(item, "call_id", callID)
				item, _ = sjson.SetBytes(item, "name", name)
				outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, "arr.-1", item)
			}
		}
		if gjson.GetBytes(outputsWrapper, "arr.#").Int() > 0 {
			completed, _ = sjson.SetRawBytes(completed, "response.output", []byte(gjson.GetBytes(outputsWrapper, "arr").Raw))
		}

		reasoningTokens := int64(0)
		if st.ReasoningBuf.Len() > 0 {
			reasoningTokens = int64(st.ReasoningBuf.Len() / 4)
		}
		usagePresent := st.UsageSeen || reasoningTokens > 0
		if usagePresent {
			completed, _ = sjson.SetBytes(completed, "response.usage.input_tokens", st.InputTokens)
			completed, _ = sjson.SetBytes(completed, "response.usage.input_tokens_details.cached_tokens", 0)
			completed, _ = sjson.SetBytes(completed, "response.usage.output_tokens", st.OutputTokens)
			if reasoningTokens > 0 {
				completed, _ = sjson.SetBytes(completed, "response.usage.output_tokens_details.reasoning_tokens", reasoningTokens)
			}
			total := st.InputTokens + st.OutputTokens
			if total > 0 || st.UsageSeen {
				completed, _ = sjson.SetBytes(completed, "response.usage.total_tokens", total)
			}
		}
		out = append(out, emitEvent("response.completed", completed))
	}

	return out
}

// ConvertClaudeResponseToOpenAIResponsesNonStream aggregates Claude SSE into a single OpenAI Responses JSON.
func ConvertClaudeResponseToOpenAIResponsesNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	// Aggregate Claude SSE lines into a single OpenAI Responses JSON (non-stream)
	// We follow the same aggregation logic as the streaming variant but produce
	// one final object matching docs/out.json structure.

	// Collect SSE data: lines start with "data: "; ignore others
	var chunks [][]byte
	{
		// Use a simple scanner to iterate through raw bytes
		// Note: extremely large responses may require increasing the buffer
		scanner := bufio.NewScanner(bytes.NewReader(rawJSON))
		buf := make([]byte, 52_428_800) // 50MB
		scanner.Buffer(buf, 52_428_800)
		for scanner.Scan() {
			line := scanner.Bytes()
			if !bytes.HasPrefix(line, dataTag) {
				continue
			}
			chunks = append(chunks, line[len(dataTag):])
		}
	}

	// Base OpenAI Responses (non-stream) object
	out := []byte(`{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"incomplete_details":null,"output":[],"usage":{"input_tokens":0,"input_tokens_details":{"cached_tokens":0},"output_tokens":0,"output_tokens_details":{},"total_tokens":0}}`)

	// Aggregation state
	var (
		responseID      string
		createdAt       int64
		currentMsgID    string
		currentFCID     string
		textBuf         strings.Builder
		reasoningBuf    strings.Builder
		reasoningActive bool
		reasoningItemID string
		inputTokens     int64
		outputTokens    int64
	)

	// Per-index tool call aggregation
	type toolState struct {
		id   string
		name string
		args strings.Builder
	}
	toolCalls := make(map[int]*toolState)

	// Walk through SSE chunks to fill state
	for _, ch := range chunks {
		root := gjson.ParseBytes(ch)
		ev := root.Get("type").String()

		switch ev {
		case "message_start":
			if msg := root.Get("message"); msg.Exists() {
				responseID = msg.Get("id").String()
				createdAt = time.Now().Unix()
				if usage := msg.Get("usage"); usage.Exists() {
					inputTokens = usage.Get("input_tokens").Int()
				}
			}

		case "content_block_start":
			cb := root.Get("content_block")
			if !cb.Exists() {
				continue
			}
			idx := int(root.Get("index").Int())
			typ := cb.Get("type").String()
			switch typ {
			case "text":
				currentMsgID = "msg_" + responseID + "_0"
			case "tool_use":
				currentFCID = cb.Get("id").String()
				name := cb.Get("name").String()
				if toolCalls[idx] == nil {
					toolCalls[idx] = &toolState{id: currentFCID, name: name}
				} else {
					toolCalls[idx].id = currentFCID
					toolCalls[idx].name = name
				}
			case "thinking":
				reasoningActive = true
				reasoningItemID = fmt.Sprintf("rs_%s_%d", responseID, idx)
			}

		case "content_block_delta":
			d := root.Get("delta")
			if !d.Exists() {
				continue
			}
			dt := d.Get("type").String()
			switch dt {
			case "text_delta":
				if t := d.Get("text"); t.Exists() {
					textBuf.WriteString(t.String())
				}
			case "input_json_delta":
				if pj := d.Get("partial_json"); pj.Exists() {
					idx := int(root.Get("index").Int())
					if toolCalls[idx] == nil {
						toolCalls[idx] = &toolState{}
					}
					toolCalls[idx].args.WriteString(pj.String())
				}
			case "thinking_delta":
				if reasoningActive {
					if t := d.Get("thinking"); t.Exists() {
						reasoningBuf.WriteString(t.String())
					}
				}
			}

		case "content_block_stop":
			// Nothing special to finalize for non-stream aggregation
			_ = root

		case "message_delta":
			if usage := root.Get("usage"); usage.Exists() {
				outputTokens = usage.Get("output_tokens").Int()
			}
		}
	}

	// Populate base fields
	out, _ = sjson.SetBytes(out, "id", responseID)
	out, _ = sjson.SetBytes(out, "created_at", createdAt)

	// Inject request echo fields as top-level (similar to streaming variant)
	reqBytes := pickRequestJSON(originalRequestRawJSON, requestRawJSON)
	if len(reqBytes) > 0 {
		req := gjson.ParseBytes(reqBytes)
		if v := req.Get("instructions"); v.Exists() {
			out, _ = sjson.SetBytes(out, "instructions", v.String())
		}
		if v := req.Get("max_output_tokens"); v.Exists() {
			out, _ = sjson.SetBytes(out, "max_output_tokens", v.Int())
		}
		if v := req.Get("max_tool_calls"); v.Exists() {
			out, _ = sjson.SetBytes(out, "max_tool_calls", v.Int())
		}
		if v := req.Get("model"); v.Exists() {
			out, _ = sjson.SetBytes(out, "model", v.String())
		}
		if v := req.Get("parallel_tool_calls"); v.Exists() {
			out, _ = sjson.SetBytes(out, "parallel_tool_calls", v.Bool())
		}
		if v := req.Get("previous_response_id"); v.Exists() {
			out, _ = sjson.SetBytes(out, "previous_response_id", v.String())
		}
		if v := req.Get("prompt_cache_key"); v.Exists() {
			out, _ = sjson.SetBytes(out, "prompt_cache_key", v.String())
		}
		if v := req.Get("reasoning"); v.Exists() {
			out, _ = sjson.SetBytes(out, "reasoning", v.Value())
		}
		if v := req.Get("safety_identifier"); v.Exists() {
			out, _ = sjson.SetBytes(out, "safety_identifier", v.String())
		}
		if v := req.Get("service_tier"); v.Exists() {
			out, _ = sjson.SetBytes(out, "service_tier", v.String())
		}
		if v := req.Get("store"); v.Exists() {
			out, _ = sjson.SetBytes(out, "store", v.Bool())
		}
		if v := req.Get("temperature"); v.Exists() {
			out, _ = sjson.SetBytes(out, "temperature", v.Float())
		}
		if v := req.Get("text"); v.Exists() {
			out, _ = sjson.SetBytes(out, "text", v.Value())
		}
		if v := req.Get("tool_choice"); v.Exists() {
			out, _ = sjson.SetBytes(out, "tool_choice", v.Value())
		}
		if v := req.Get("tools"); v.Exists() {
			out, _ = sjson.SetBytes(out, "tools", v.Value())
		}
		if v := req.Get("top_logprobs"); v.Exists() {
			out, _ = sjson.SetBytes(out, "top_logprobs", v.Int())
		}
		if v := req.Get("top_p"); v.Exists() {
			out, _ = sjson.SetBytes(out, "top_p", v.Float())
		}
		if v := req.Get("truncation"); v.Exists() {
			out, _ = sjson.SetBytes(out, "truncation", v.String())
		}
		if v := req.Get("user"); v.Exists() {
			out, _ = sjson.SetBytes(out, "user", v.Value())
		}
		if v := req.Get("metadata"); v.Exists() {
			out, _ = sjson.SetBytes(out, "metadata", v.Value())
		}
	}

	// Build output array
	outputsWrapper := []byte(`{"arr":[]}`)
	if reasoningBuf.Len() > 0 {
		item := []byte(`{"id":"","type":"reasoning","summary":[{"type":"summary_text","text":""}]}`)
		item, _ = sjson.SetBytes(item, "id", reasoningItemID)
		item, _ = sjson.SetBytes(item, "summary.0.text", reasoningBuf.String())
		outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, "arr.-1", item)
	}
	if currentMsgID != "" || textBuf.Len() > 0 {
		item := []byte(`{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}`)
		item, _ = sjson.SetBytes(item, "id", currentMsgID)
		item, _ = sjson.SetBytes(item, "content.0.text", textBuf.String())
		outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, "arr.-1", item)
	}
	if len(toolCalls) > 0 {
		// Preserve index order
		idxs := make([]int, 0, len(toolCalls))
		for i := range toolCalls {
			idxs = append(idxs, i)
		}
		for i := 0; i < len(idxs); i++ {
			for j := i + 1; j < len(idxs); j++ {
				if idxs[j] < idxs[i] {
					idxs[i], idxs[j] = idxs[j], idxs[i]
				}
			}
		}
		for _, i := range idxs {
			st := toolCalls[i]
			args := st.args.String()
			if args == "" {
				args = "{}"
			}
			item := []byte(`{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`)
			item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("fc_%s", st.id))
			item, _ = sjson.SetBytes(item, "arguments", args)
			item, _ = sjson.SetBytes(item, "call_id", st.id)
			item, _ = sjson.SetBytes(item, "name", st.name)
			outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, "arr.-1", item)
		}
	}
	if gjson.GetBytes(outputsWrapper, "arr.#").Int() > 0 {
		out, _ = sjson.SetRawBytes(out, "output", []byte(gjson.GetBytes(outputsWrapper, "arr").Raw))
	}

	// Usage
	total := inputTokens + outputTokens
	out, _ = sjson.SetBytes(out, "usage.input_tokens", inputTokens)
	out, _ = sjson.SetBytes(out, "usage.output_tokens", outputTokens)
	out, _ = sjson.SetBytes(out, "usage.total_tokens", total)
	if reasoningBuf.Len() > 0 {
		// Rough estimate similar to chat completions
		reasoningTokens := int64(len(reasoningBuf.String()) / 4)
		if reasoningTokens > 0 {
			out, _ = sjson.SetBytes(out, "usage.output_tokens_details.reasoning_tokens", reasoningTokens)
		}
	}

	return out
}
