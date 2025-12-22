package responses

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type geminiToResponsesState struct {
	Seq        int
	ResponseID string
	CreatedAt  int64
	Started    bool

	// message aggregation
	MsgOpened    bool
	MsgIndex     int
	CurrentMsgID string
	TextBuf      strings.Builder

	// reasoning aggregation
	ReasoningOpened bool
	ReasoningIndex  int
	ReasoningItemID string
	ReasoningBuf    strings.Builder
	ReasoningClosed bool

	// function call aggregation (keyed by output_index)
	NextIndex   int
	FuncArgsBuf map[int]*strings.Builder
	FuncNames   map[int]string
	FuncCallIDs map[int]string
}

// responseIDCounter provides a process-wide unique counter for synthesized response identifiers.
var responseIDCounter uint64

// funcCallIDCounter provides a process-wide unique counter for function call identifiers.
var funcCallIDCounter uint64

func emitEvent(event string, payload string) string {
	return fmt.Sprintf("event: %s\ndata: %s", event, payload)
}

// ConvertGeminiResponseToOpenAIResponses converts Gemini SSE chunks into OpenAI Responses SSE events.
func ConvertGeminiResponseToOpenAIResponses(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	if *param == nil {
		*param = &geminiToResponsesState{
			FuncArgsBuf: make(map[int]*strings.Builder),
			FuncNames:   make(map[int]string),
			FuncCallIDs: make(map[int]string),
		}
	}
	st := (*param).(*geminiToResponsesState)

	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}

	root := gjson.ParseBytes(rawJSON)
	if !root.Exists() {
		return []string{}
	}

	var out []string
	nextSeq := func() int { st.Seq++; return st.Seq }

	// Helper to finalize reasoning summary events in correct order.
	// It emits response.reasoning_summary_text.done followed by
	// response.reasoning_summary_part.done exactly once.
	finalizeReasoning := func() {
		if !st.ReasoningOpened || st.ReasoningClosed {
			return
		}
		full := st.ReasoningBuf.String()
		textDone := `{"type":"response.reasoning_summary_text.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"text":""}`
		textDone, _ = sjson.Set(textDone, "sequence_number", nextSeq())
		textDone, _ = sjson.Set(textDone, "item_id", st.ReasoningItemID)
		textDone, _ = sjson.Set(textDone, "output_index", st.ReasoningIndex)
		textDone, _ = sjson.Set(textDone, "text", full)
		out = append(out, emitEvent("response.reasoning_summary_text.done", textDone))

		partDone := `{"type":"response.reasoning_summary_part.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`
		partDone, _ = sjson.Set(partDone, "sequence_number", nextSeq())
		partDone, _ = sjson.Set(partDone, "item_id", st.ReasoningItemID)
		partDone, _ = sjson.Set(partDone, "output_index", st.ReasoningIndex)
		partDone, _ = sjson.Set(partDone, "part.text", full)
		out = append(out, emitEvent("response.reasoning_summary_part.done", partDone))

		itemDone := `{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","encrypted_content":"","summary":[{"type":"summary_text","text":""}]}}`
		itemDone, _ = sjson.Set(itemDone, "sequence_number", nextSeq())
		itemDone, _ = sjson.Set(itemDone, "item.id", st.ReasoningItemID)
		itemDone, _ = sjson.Set(itemDone, "output_index", st.ReasoningIndex)
		itemDone, _ = sjson.Set(itemDone, "item.summary.0.text", full)
		out = append(out, emitEvent("response.output_item.done", itemDone))

		st.ReasoningClosed = true
	}

	// Initialize per-response fields and emit created/in_progress once
	if !st.Started {
		if v := root.Get("responseId"); v.Exists() {
			st.ResponseID = v.String()
		}
		if v := root.Get("createTime"); v.Exists() {
			if t, err := time.Parse(time.RFC3339Nano, v.String()); err == nil {
				st.CreatedAt = t.Unix()
			}
		}
		if st.CreatedAt == 0 {
			st.CreatedAt = time.Now().Unix()
		}

		created := `{"type":"response.created","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null,"output":[]}}`
		created, _ = sjson.Set(created, "sequence_number", nextSeq())
		created, _ = sjson.Set(created, "response.id", st.ResponseID)
		created, _ = sjson.Set(created, "response.created_at", st.CreatedAt)
		out = append(out, emitEvent("response.created", created))

		inprog := `{"type":"response.in_progress","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress"}}`
		inprog, _ = sjson.Set(inprog, "sequence_number", nextSeq())
		inprog, _ = sjson.Set(inprog, "response.id", st.ResponseID)
		inprog, _ = sjson.Set(inprog, "response.created_at", st.CreatedAt)
		out = append(out, emitEvent("response.in_progress", inprog))

		st.Started = true
		st.NextIndex = 0
	}

	// Handle parts (text/thought/functionCall)
	if parts := root.Get("candidates.0.content.parts"); parts.Exists() && parts.IsArray() {
		parts.ForEach(func(_, part gjson.Result) bool {
			// Reasoning text
			if part.Get("thought").Bool() {
				if st.ReasoningClosed {
					// Ignore any late thought chunks after reasoning is finalized.
					return true
				}
				if !st.ReasoningOpened {
					st.ReasoningOpened = true
					st.ReasoningIndex = st.NextIndex
					st.NextIndex++
					st.ReasoningItemID = fmt.Sprintf("rs_%s_%d", st.ResponseID, st.ReasoningIndex)
					item := `{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"in_progress","summary":[]}}`
					item, _ = sjson.Set(item, "sequence_number", nextSeq())
					item, _ = sjson.Set(item, "output_index", st.ReasoningIndex)
					item, _ = sjson.Set(item, "item.id", st.ReasoningItemID)
					out = append(out, emitEvent("response.output_item.added", item))
					partAdded := `{"type":"response.reasoning_summary_part.added","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`
					partAdded, _ = sjson.Set(partAdded, "sequence_number", nextSeq())
					partAdded, _ = sjson.Set(partAdded, "item_id", st.ReasoningItemID)
					partAdded, _ = sjson.Set(partAdded, "output_index", st.ReasoningIndex)
					out = append(out, emitEvent("response.reasoning_summary_part.added", partAdded))
				}
				if t := part.Get("text"); t.Exists() && t.String() != "" {
					st.ReasoningBuf.WriteString(t.String())
					msg := `{"type":"response.reasoning_summary_text.delta","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"delta":""}`
					msg, _ = sjson.Set(msg, "sequence_number", nextSeq())
					msg, _ = sjson.Set(msg, "item_id", st.ReasoningItemID)
					msg, _ = sjson.Set(msg, "output_index", st.ReasoningIndex)
					msg, _ = sjson.Set(msg, "delta", t.String())
					out = append(out, emitEvent("response.reasoning_summary_text.delta", msg))
				}
				return true
			}

			// Assistant visible text
			if t := part.Get("text"); t.Exists() && t.String() != "" {
				// Before emitting non-reasoning outputs, finalize reasoning if open.
				finalizeReasoning()
				if !st.MsgOpened {
					st.MsgOpened = true
					st.MsgIndex = st.NextIndex
					st.NextIndex++
					st.CurrentMsgID = fmt.Sprintf("msg_%s_0", st.ResponseID)
					item := `{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"in_progress","content":[],"role":"assistant"}}`
					item, _ = sjson.Set(item, "sequence_number", nextSeq())
					item, _ = sjson.Set(item, "output_index", st.MsgIndex)
					item, _ = sjson.Set(item, "item.id", st.CurrentMsgID)
					out = append(out, emitEvent("response.output_item.added", item))
					partAdded := `{"type":"response.content_part.added","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`
					partAdded, _ = sjson.Set(partAdded, "sequence_number", nextSeq())
					partAdded, _ = sjson.Set(partAdded, "item_id", st.CurrentMsgID)
					partAdded, _ = sjson.Set(partAdded, "output_index", st.MsgIndex)
					out = append(out, emitEvent("response.content_part.added", partAdded))
				}
				st.TextBuf.WriteString(t.String())
				msg := `{"type":"response.output_text.delta","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"delta":"","logprobs":[]}`
				msg, _ = sjson.Set(msg, "sequence_number", nextSeq())
				msg, _ = sjson.Set(msg, "item_id", st.CurrentMsgID)
				msg, _ = sjson.Set(msg, "output_index", st.MsgIndex)
				msg, _ = sjson.Set(msg, "delta", t.String())
				out = append(out, emitEvent("response.output_text.delta", msg))
				return true
			}

			// Function call
			if fc := part.Get("functionCall"); fc.Exists() {
				// Before emitting function-call outputs, finalize reasoning if open.
				finalizeReasoning()
				name := fc.Get("name").String()
				idx := st.NextIndex
				st.NextIndex++
				// Ensure buffers
				if st.FuncArgsBuf[idx] == nil {
					st.FuncArgsBuf[idx] = &strings.Builder{}
				}
				if st.FuncCallIDs[idx] == "" {
					st.FuncCallIDs[idx] = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), atomic.AddUint64(&funcCallIDCounter, 1))
				}
				st.FuncNames[idx] = name

				// Emit item.added for function call
				item := `{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"in_progress","arguments":"","call_id":"","name":""}}`
				item, _ = sjson.Set(item, "sequence_number", nextSeq())
				item, _ = sjson.Set(item, "output_index", idx)
				item, _ = sjson.Set(item, "item.id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
				item, _ = sjson.Set(item, "item.call_id", st.FuncCallIDs[idx])
				item, _ = sjson.Set(item, "item.name", name)
				out = append(out, emitEvent("response.output_item.added", item))

				// Emit arguments delta (full args in one chunk)
				if args := fc.Get("args"); args.Exists() {
					argsJSON := args.Raw
					st.FuncArgsBuf[idx].WriteString(argsJSON)
					ad := `{"type":"response.function_call_arguments.delta","sequence_number":0,"item_id":"","output_index":0,"delta":""}`
					ad, _ = sjson.Set(ad, "sequence_number", nextSeq())
					ad, _ = sjson.Set(ad, "item_id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
					ad, _ = sjson.Set(ad, "output_index", idx)
					ad, _ = sjson.Set(ad, "delta", argsJSON)
					out = append(out, emitEvent("response.function_call_arguments.delta", ad))
				}

				return true
			}

			return true
		})
	}

	// Finalization on finishReason
	if fr := root.Get("candidates.0.finishReason"); fr.Exists() && fr.String() != "" {
		// Finalize reasoning first to keep ordering tight with last delta
		finalizeReasoning()
		// Close message output if opened
		if st.MsgOpened {
			done := `{"type":"response.output_text.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"text":"","logprobs":[]}`
			done, _ = sjson.Set(done, "sequence_number", nextSeq())
			done, _ = sjson.Set(done, "item_id", st.CurrentMsgID)
			done, _ = sjson.Set(done, "output_index", st.MsgIndex)
			out = append(out, emitEvent("response.output_text.done", done))
			partDone := `{"type":"response.content_part.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`
			partDone, _ = sjson.Set(partDone, "sequence_number", nextSeq())
			partDone, _ = sjson.Set(partDone, "item_id", st.CurrentMsgID)
			partDone, _ = sjson.Set(partDone, "output_index", st.MsgIndex)
			out = append(out, emitEvent("response.content_part.done", partDone))
			final := `{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"completed","content":[{"type":"output_text","text":""}],"role":"assistant"}}`
			final, _ = sjson.Set(final, "sequence_number", nextSeq())
			final, _ = sjson.Set(final, "output_index", st.MsgIndex)
			final, _ = sjson.Set(final, "item.id", st.CurrentMsgID)
			out = append(out, emitEvent("response.output_item.done", final))
		}

		// Close function calls
		if len(st.FuncArgsBuf) > 0 {
			// sort indices (small N); avoid extra imports
			idxs := make([]int, 0, len(st.FuncArgsBuf))
			for idx := range st.FuncArgsBuf {
				idxs = append(idxs, idx)
			}
			for i := 0; i < len(idxs); i++ {
				for j := i + 1; j < len(idxs); j++ {
					if idxs[j] < idxs[i] {
						idxs[i], idxs[j] = idxs[j], idxs[i]
					}
				}
			}
			for _, idx := range idxs {
				args := "{}"
				if b := st.FuncArgsBuf[idx]; b != nil && b.Len() > 0 {
					args = b.String()
				}
				fcDone := `{"type":"response.function_call_arguments.done","sequence_number":0,"item_id":"","output_index":0,"arguments":""}`
				fcDone, _ = sjson.Set(fcDone, "sequence_number", nextSeq())
				fcDone, _ = sjson.Set(fcDone, "item_id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
				fcDone, _ = sjson.Set(fcDone, "output_index", idx)
				fcDone, _ = sjson.Set(fcDone, "arguments", args)
				out = append(out, emitEvent("response.function_call_arguments.done", fcDone))

				itemDone := `{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}}`
				itemDone, _ = sjson.Set(itemDone, "sequence_number", nextSeq())
				itemDone, _ = sjson.Set(itemDone, "output_index", idx)
				itemDone, _ = sjson.Set(itemDone, "item.id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
				itemDone, _ = sjson.Set(itemDone, "item.arguments", args)
				itemDone, _ = sjson.Set(itemDone, "item.call_id", st.FuncCallIDs[idx])
				itemDone, _ = sjson.Set(itemDone, "item.name", st.FuncNames[idx])
				out = append(out, emitEvent("response.output_item.done", itemDone))
			}
		}

		// Reasoning already finalized above if present

		// Build response.completed with aggregated outputs and request echo fields
		completed := `{"type":"response.completed","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null}}`
		completed, _ = sjson.Set(completed, "sequence_number", nextSeq())
		completed, _ = sjson.Set(completed, "response.id", st.ResponseID)
		completed, _ = sjson.Set(completed, "response.created_at", st.CreatedAt)

		if requestRawJSON != nil {
			req := gjson.ParseBytes(requestRawJSON)
			if v := req.Get("instructions"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.instructions", v.String())
			}
			if v := req.Get("max_output_tokens"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.max_output_tokens", v.Int())
			}
			if v := req.Get("max_tool_calls"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.max_tool_calls", v.Int())
			}
			if v := req.Get("model"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.model", v.String())
			}
			if v := req.Get("parallel_tool_calls"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.parallel_tool_calls", v.Bool())
			}
			if v := req.Get("previous_response_id"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.previous_response_id", v.String())
			}
			if v := req.Get("prompt_cache_key"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.prompt_cache_key", v.String())
			}
			if v := req.Get("reasoning"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.reasoning", v.Value())
			}
			if v := req.Get("safety_identifier"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.safety_identifier", v.String())
			}
			if v := req.Get("service_tier"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.service_tier", v.String())
			}
			if v := req.Get("store"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.store", v.Bool())
			}
			if v := req.Get("temperature"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.temperature", v.Float())
			}
			if v := req.Get("text"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.text", v.Value())
			}
			if v := req.Get("tool_choice"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.tool_choice", v.Value())
			}
			if v := req.Get("tools"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.tools", v.Value())
			}
			if v := req.Get("top_logprobs"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.top_logprobs", v.Int())
			}
			if v := req.Get("top_p"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.top_p", v.Float())
			}
			if v := req.Get("truncation"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.truncation", v.String())
			}
			if v := req.Get("user"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.user", v.Value())
			}
			if v := req.Get("metadata"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.metadata", v.Value())
			}
		}

		// Compose outputs in encountered order: reasoning, message, function_calls
		outputsWrapper := `{"arr":[]}`
		if st.ReasoningOpened {
			item := `{"id":"","type":"reasoning","summary":[{"type":"summary_text","text":""}]}`
			item, _ = sjson.Set(item, "id", st.ReasoningItemID)
			item, _ = sjson.Set(item, "summary.0.text", st.ReasoningBuf.String())
			outputsWrapper, _ = sjson.SetRaw(outputsWrapper, "arr.-1", item)
		}
		if st.MsgOpened {
			item := `{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}`
			item, _ = sjson.Set(item, "id", st.CurrentMsgID)
			item, _ = sjson.Set(item, "content.0.text", st.TextBuf.String())
			outputsWrapper, _ = sjson.SetRaw(outputsWrapper, "arr.-1", item)
		}
		if len(st.FuncArgsBuf) > 0 {
			idxs := make([]int, 0, len(st.FuncArgsBuf))
			for idx := range st.FuncArgsBuf {
				idxs = append(idxs, idx)
			}
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
				item := `{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`
				item, _ = sjson.Set(item, "id", fmt.Sprintf("fc_%s", st.FuncCallIDs[idx]))
				item, _ = sjson.Set(item, "arguments", args)
				item, _ = sjson.Set(item, "call_id", st.FuncCallIDs[idx])
				item, _ = sjson.Set(item, "name", st.FuncNames[idx])
				outputsWrapper, _ = sjson.SetRaw(outputsWrapper, "arr.-1", item)
			}
		}
		if gjson.Get(outputsWrapper, "arr.#").Int() > 0 {
			completed, _ = sjson.SetRaw(completed, "response.output", gjson.Get(outputsWrapper, "arr").Raw)
		}

		// usage mapping
		if um := root.Get("usageMetadata"); um.Exists() {
			// input tokens = prompt + thoughts
			input := um.Get("promptTokenCount").Int() + um.Get("thoughtsTokenCount").Int()
			completed, _ = sjson.Set(completed, "response.usage.input_tokens", input)
			// cached_tokens not provided by Gemini; default to 0 for structure compatibility
			completed, _ = sjson.Set(completed, "response.usage.input_tokens_details.cached_tokens", 0)
			// output tokens
			if v := um.Get("candidatesTokenCount"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.usage.output_tokens", v.Int())
			} else {
				completed, _ = sjson.Set(completed, "response.usage.output_tokens", 0)
			}
			if v := um.Get("thoughtsTokenCount"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.usage.output_tokens_details.reasoning_tokens", v.Int())
			} else {
				completed, _ = sjson.Set(completed, "response.usage.output_tokens_details.reasoning_tokens", 0)
			}
			if v := um.Get("totalTokenCount"); v.Exists() {
				completed, _ = sjson.Set(completed, "response.usage.total_tokens", v.Int())
			} else {
				completed, _ = sjson.Set(completed, "response.usage.total_tokens", 0)
			}
		}

		out = append(out, emitEvent("response.completed", completed))
	}

	return out
}

// ConvertGeminiResponseToOpenAIResponsesNonStream aggregates Gemini response JSON into a single OpenAI Responses JSON object.
func ConvertGeminiResponseToOpenAIResponsesNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) string {
	root := gjson.ParseBytes(rawJSON)

	// Base response scaffold
	resp := `{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"incomplete_details":null}`

	// id: prefer provider responseId, otherwise synthesize
	id := root.Get("responseId").String()
	if id == "" {
		id = fmt.Sprintf("resp_%x_%d", time.Now().UnixNano(), atomic.AddUint64(&responseIDCounter, 1))
	}
	// Normalize to response-style id (prefix resp_ if missing)
	if !strings.HasPrefix(id, "resp_") {
		id = fmt.Sprintf("resp_%s", id)
	}
	resp, _ = sjson.Set(resp, "id", id)

	// created_at: map from createTime if available
	createdAt := time.Now().Unix()
	if v := root.Get("createTime"); v.Exists() {
		if t, err := time.Parse(time.RFC3339Nano, v.String()); err == nil {
			createdAt = t.Unix()
		}
	}
	resp, _ = sjson.Set(resp, "created_at", createdAt)

	// Echo request fields when present; fallback model from response modelVersion
	if len(requestRawJSON) > 0 {
		req := gjson.ParseBytes(requestRawJSON)
		if v := req.Get("instructions"); v.Exists() {
			resp, _ = sjson.Set(resp, "instructions", v.String())
		}
		if v := req.Get("max_output_tokens"); v.Exists() {
			resp, _ = sjson.Set(resp, "max_output_tokens", v.Int())
		}
		if v := req.Get("max_tool_calls"); v.Exists() {
			resp, _ = sjson.Set(resp, "max_tool_calls", v.Int())
		}
		if v := req.Get("model"); v.Exists() {
			resp, _ = sjson.Set(resp, "model", v.String())
		} else if v = root.Get("modelVersion"); v.Exists() {
			resp, _ = sjson.Set(resp, "model", v.String())
		}
		if v := req.Get("parallel_tool_calls"); v.Exists() {
			resp, _ = sjson.Set(resp, "parallel_tool_calls", v.Bool())
		}
		if v := req.Get("previous_response_id"); v.Exists() {
			resp, _ = sjson.Set(resp, "previous_response_id", v.String())
		}
		if v := req.Get("prompt_cache_key"); v.Exists() {
			resp, _ = sjson.Set(resp, "prompt_cache_key", v.String())
		}
		if v := req.Get("reasoning"); v.Exists() {
			resp, _ = sjson.Set(resp, "reasoning", v.Value())
		}
		if v := req.Get("safety_identifier"); v.Exists() {
			resp, _ = sjson.Set(resp, "safety_identifier", v.String())
		}
		if v := req.Get("service_tier"); v.Exists() {
			resp, _ = sjson.Set(resp, "service_tier", v.String())
		}
		if v := req.Get("store"); v.Exists() {
			resp, _ = sjson.Set(resp, "store", v.Bool())
		}
		if v := req.Get("temperature"); v.Exists() {
			resp, _ = sjson.Set(resp, "temperature", v.Float())
		}
		if v := req.Get("text"); v.Exists() {
			resp, _ = sjson.Set(resp, "text", v.Value())
		}
		if v := req.Get("tool_choice"); v.Exists() {
			resp, _ = sjson.Set(resp, "tool_choice", v.Value())
		}
		if v := req.Get("tools"); v.Exists() {
			resp, _ = sjson.Set(resp, "tools", v.Value())
		}
		if v := req.Get("top_logprobs"); v.Exists() {
			resp, _ = sjson.Set(resp, "top_logprobs", v.Int())
		}
		if v := req.Get("top_p"); v.Exists() {
			resp, _ = sjson.Set(resp, "top_p", v.Float())
		}
		if v := req.Get("truncation"); v.Exists() {
			resp, _ = sjson.Set(resp, "truncation", v.String())
		}
		if v := req.Get("user"); v.Exists() {
			resp, _ = sjson.Set(resp, "user", v.Value())
		}
		if v := req.Get("metadata"); v.Exists() {
			resp, _ = sjson.Set(resp, "metadata", v.Value())
		}
	} else if v := root.Get("modelVersion"); v.Exists() {
		resp, _ = sjson.Set(resp, "model", v.String())
	}

	// Build outputs from candidates[0].content.parts
	var reasoningText strings.Builder
	var reasoningEncrypted string
	var messageText strings.Builder
	var haveMessage bool

	haveOutput := false
	ensureOutput := func() {
		if haveOutput {
			return
		}
		resp, _ = sjson.SetRaw(resp, "output", "[]")
		haveOutput = true
	}
	appendOutput := func(itemJSON string) {
		ensureOutput()
		resp, _ = sjson.SetRaw(resp, "output.-1", itemJSON)
	}

	if parts := root.Get("candidates.0.content.parts"); parts.Exists() && parts.IsArray() {
		parts.ForEach(func(_, p gjson.Result) bool {
			if p.Get("thought").Bool() {
				if t := p.Get("text"); t.Exists() {
					reasoningText.WriteString(t.String())
				}
				if sig := p.Get("thoughtSignature"); sig.Exists() && sig.String() != "" {
					reasoningEncrypted = sig.String()
				}
				return true
			}
			if t := p.Get("text"); t.Exists() && t.String() != "" {
				messageText.WriteString(t.String())
				haveMessage = true
				return true
			}
			if fc := p.Get("functionCall"); fc.Exists() {
				name := fc.Get("name").String()
				args := fc.Get("args")
				callID := fmt.Sprintf("call_%x_%d", time.Now().UnixNano(), atomic.AddUint64(&funcCallIDCounter, 1))
				itemJSON := `{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`
				itemJSON, _ = sjson.Set(itemJSON, "id", fmt.Sprintf("fc_%s", callID))
				itemJSON, _ = sjson.Set(itemJSON, "call_id", callID)
				itemJSON, _ = sjson.Set(itemJSON, "name", name)
				argsStr := ""
				if args.Exists() {
					argsStr = args.Raw
				}
				itemJSON, _ = sjson.Set(itemJSON, "arguments", argsStr)
				appendOutput(itemJSON)
				return true
			}
			return true
		})
	}

	// Reasoning output item
	if reasoningText.Len() > 0 || reasoningEncrypted != "" {
		rid := strings.TrimPrefix(id, "resp_")
		itemJSON := `{"id":"","type":"reasoning","encrypted_content":""}`
		itemJSON, _ = sjson.Set(itemJSON, "id", fmt.Sprintf("rs_%s", rid))
		itemJSON, _ = sjson.Set(itemJSON, "encrypted_content", reasoningEncrypted)
		if reasoningText.Len() > 0 {
			summaryJSON := `{"type":"summary_text","text":""}`
			summaryJSON, _ = sjson.Set(summaryJSON, "text", reasoningText.String())
			itemJSON, _ = sjson.SetRaw(itemJSON, "summary", "[]")
			itemJSON, _ = sjson.SetRaw(itemJSON, "summary.-1", summaryJSON)
		}
		appendOutput(itemJSON)
	}

	// Assistant message output item
	if haveMessage {
		itemJSON := `{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}`
		itemJSON, _ = sjson.Set(itemJSON, "id", fmt.Sprintf("msg_%s_0", strings.TrimPrefix(id, "resp_")))
		itemJSON, _ = sjson.Set(itemJSON, "content.0.text", messageText.String())
		appendOutput(itemJSON)
	}

	// usage mapping
	if um := root.Get("usageMetadata"); um.Exists() {
		// input tokens = prompt + thoughts
		input := um.Get("promptTokenCount").Int() + um.Get("thoughtsTokenCount").Int()
		resp, _ = sjson.Set(resp, "usage.input_tokens", input)
		// cached_tokens not provided by Gemini; default to 0 for structure compatibility
		resp, _ = sjson.Set(resp, "usage.input_tokens_details.cached_tokens", 0)
		// output tokens
		if v := um.Get("candidatesTokenCount"); v.Exists() {
			resp, _ = sjson.Set(resp, "usage.output_tokens", v.Int())
		}
		if v := um.Get("thoughtsTokenCount"); v.Exists() {
			resp, _ = sjson.Set(resp, "usage.output_tokens_details.reasoning_tokens", v.Int())
		}
		if v := um.Get("totalTokenCount"); v.Exists() {
			resp, _ = sjson.Set(resp, "usage.total_tokens", v.Int())
		}
	}

	return resp
}
