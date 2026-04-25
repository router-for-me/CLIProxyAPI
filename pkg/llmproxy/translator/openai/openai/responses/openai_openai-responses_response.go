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

func pickRequestJSON(originalRequestRawJSON, requestRawJSON []byte) []byte {
	if len(originalRequestRawJSON) > 0 && gjson.ValidBytes(originalRequestRawJSON) {
		return originalRequestRawJSON
	}
	if len(requestRawJSON) > 0 && gjson.ValidBytes(requestRawJSON) {
		return requestRawJSON
	}
	return nil
}

func unwrapOpenAIChatCompletionResult(root gjson.Result) gjson.Result {
	if root.Get("choices").Exists() {
		return root
	}
	for _, path := range []string{"data", "result", "response", "data.response"} {
		candidate := root.Get(path)
		if candidate.Exists() && candidate.Get("choices").Exists() {
			return candidate
		}
	}
	return root
}

type oaiToResponsesStateReasoning struct {
	ReasoningID   string
	ReasoningData string
}
type oaiToResponsesState struct {
	Seq            int
	ResponseID     string
	Created        int64
	Started        bool
	ReasoningID    string
	ReasoningIndex int
	// aggregation buffers for response.output
	// Per-output message text buffers by index
	MsgTextBuf   map[int]*strings.Builder
	ReasoningBuf strings.Builder
	Reasonings   []oaiToResponsesStateReasoning
	FuncArgsBuf  map[int]*strings.Builder // index -> args
	FuncNames    map[int]string           // index -> name
	FuncCallIDs  map[int]string           // index -> call_id
	// message item state per output index
	MsgItemAdded    map[int]bool // whether response.output_item.added emitted for message
	MsgContentAdded map[int]bool // whether response.content_part.added emitted for message
	MsgItemDone     map[int]bool // whether message done events were emitted
	// function item done state
	FuncArgsDone map[int]bool
	FuncItemDone map[int]bool
	// Accumulated annotations per output index
	Annotations map[int][]interface{}
	// usage aggregation
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	ReasoningTokens  int64
	UsageSeen        bool
	CompletionSent   bool
	StopSeen         bool
}

// responseIDCounter provides a process-wide unique counter for synthesized response identifiers.
var responseIDCounter uint64

func emitRespEvent(event string, payload string) string {
	return fmt.Sprintf("event: %s\ndata: %s", event, payload)
}

func emitCompletionEvents(st *oaiToResponsesState) []string {
	if st == nil || st.CompletionSent {
		return []string{}
	}

	nextSeq := func() int {
		st.Seq++
		return st.Seq
	}

	completed := `{"type":"response.completed","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null}}`
	completed, _ = sjson.SetBytesM(completed, "sequence_number", nextSeq())
	completed, _ = sjson.SetBytesM(completed, "response.id", st.ResponseID)
	completed, _ = sjson.SetBytesM(completed, "response.created_at", st.Created)

	if st.UsageSeen {
		completed, _ = sjson.SetBytesM(completed, "response.usage.input_tokens", st.PromptTokens)
		completed, _ = sjson.SetBytesM(completed, "response.usage.input_tokens_details.cached_tokens", st.CachedTokens)
		completed, _ = sjson.SetBytesM(completed, "response.usage.output_tokens", st.CompletionTokens)
		if st.ReasoningTokens > 0 {
			completed, _ = sjson.SetBytesM(completed, "response.usage.output_tokens_details.reasoning_tokens", st.ReasoningTokens)
		}
		total := st.TotalTokens
		if total == 0 {
			total = st.PromptTokens + st.CompletionTokens
		}
		completed, _ = sjson.SetBytesM(completed, "response.usage.total_tokens", total)
	}

	st.CompletionSent = true
	return []string{emitRespEvent("response.completed", completed)}
}

// ConvertOpenAIChatCompletionsResponseToOpenAIResponses converts OpenAI Chat Completions streaming chunks
// to OpenAI Responses SSE events (response.*).
func ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	if *param == nil {
		*param = &oaiToResponsesState{
			FuncArgsBuf:     make(map[int]*strings.Builder),
			FuncNames:       make(map[int]string),
			FuncCallIDs:     make(map[int]string),
			MsgTextBuf:      make(map[int]*strings.Builder),
			MsgItemAdded:    make(map[int]bool),
			MsgContentAdded: make(map[int]bool),
			MsgItemDone:     make(map[int]bool),
			FuncArgsDone:    make(map[int]bool),
			FuncItemDone:    make(map[int]bool),
			Reasonings:      make([]oaiToResponsesStateReasoning, 0),
			Annotations:     make(map[int][]interface{}),
		}
	}
	st := (*param).(*oaiToResponsesState)

	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}

	rawJSON = bytes.TrimSpace(rawJSON)
	if len(rawJSON) == 0 {
		return []string{}
	}
	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		// GitHub #1085: Emit completion events on [DONE] marker instead of returning empty
		return emitCompletionEvents(st)
	}

	root := gjson.ParseBytes(rawJSON)
	obj := root.Get("object")
	if obj.Exists() && obj.String() != "" && obj.String() != "chat.completion.chunk" {
		return []string{}
	}
	if !root.Get("choices").Exists() || !root.Get("choices").IsArray() {
		return []string{}
	}

	if usage := root.Get("usage"); usage.Exists() {
		if v := usage.Get("prompt_tokens"); v.Exists() {
			st.PromptTokens = v.Int()
			st.UsageSeen = true
		}
		if v := usage.Get("prompt_tokens_details.cached_tokens"); v.Exists() {
			st.CachedTokens = v.Int()
			st.UsageSeen = true
		}
		if v := usage.Get("completion_tokens"); v.Exists() {
			st.CompletionTokens = v.Int()
			st.UsageSeen = true
		} else if v := usage.Get("output_tokens"); v.Exists() {
			st.CompletionTokens = v.Int()
			st.UsageSeen = true
		}
		if v := usage.Get("output_tokens_details.reasoning_tokens"); v.Exists() {
			st.ReasoningTokens = v.Int()
			st.UsageSeen = true
		} else if v := usage.Get("completion_tokens_details.reasoning_tokens"); v.Exists() {
			st.ReasoningTokens = v.Int()
			st.UsageSeen = true
		}
		if v := usage.Get("total_tokens"); v.Exists() {
			st.TotalTokens = v.Int()
			st.UsageSeen = true
		}
	}

	nextSeq := func() int { st.Seq++; return st.Seq }
	var out []string

	if !st.Started {
		st.ResponseID = root.Get("id").String()
		st.Created = root.Get("created").Int()
		// reset aggregation state for a new streaming response
		st.MsgTextBuf = make(map[int]*strings.Builder)
		st.ReasoningBuf.Reset()
		st.ReasoningID = ""
		st.ReasoningIndex = 0
		st.FuncArgsBuf = make(map[int]*strings.Builder)
		st.FuncNames = make(map[int]string)
		st.FuncCallIDs = make(map[int]string)
		st.MsgItemAdded = make(map[int]bool)
		st.MsgContentAdded = make(map[int]bool)
		st.MsgItemDone = make(map[int]bool)
		st.FuncArgsDone = make(map[int]bool)
		st.FuncItemDone = make(map[int]bool)
		st.PromptTokens = 0
		st.CachedTokens = 0
		st.CompletionTokens = 0
		st.TotalTokens = 0
		st.ReasoningTokens = 0
		st.UsageSeen = false
		st.CompletionSent = false
		st.StopSeen = false
		st.Annotations = make(map[int][]interface{})
		// response.created
		created := `{"type":"response.created","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null,"output":[]}}`
		created, _ = sjson.SetBytesM(created, "sequence_number", nextSeq())
		created, _ = sjson.SetBytesM(created, "response.id", st.ResponseID)
		created, _ = sjson.SetBytesM(created, "response.created_at", st.Created)
		out = append(out, emitRespEvent("response.created", created))

		inprog := `{"type":"response.in_progress","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress"}}`
		inprog, _ = sjson.SetBytesM(inprog, "sequence_number", nextSeq())
		inprog, _ = sjson.SetBytesM(inprog, "response.id", st.ResponseID)
		inprog, _ = sjson.SetBytesM(inprog, "response.created_at", st.Created)
		out = append(out, emitRespEvent("response.in_progress", inprog))
		st.Started = true
	}

	stopReasoning := func(text string) {
		// Emit reasoning done events
		textDone := `{"type":"response.reasoning_summary_text.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"text":""}`
		textDone, _ = sjson.SetBytesM(textDone, "sequence_number", nextSeq())
		textDone, _ = sjson.SetBytesM(textDone, "item_id", st.ReasoningID)
		textDone, _ = sjson.SetBytesM(textDone, "output_index", st.ReasoningIndex)
		textDone, _ = sjson.SetBytesM(textDone, "text", text)
		out = append(out, emitRespEvent("response.reasoning_summary_text.done", textDone))
		partDone := `{"type":"response.reasoning_summary_part.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`
		partDone, _ = sjson.SetBytesM(partDone, "sequence_number", nextSeq())
		partDone, _ = sjson.SetBytesM(partDone, "item_id", st.ReasoningID)
		partDone, _ = sjson.SetBytesM(partDone, "output_index", st.ReasoningIndex)
		partDone, _ = sjson.SetBytesM(partDone, "part.text", text)
		out = append(out, emitRespEvent("response.reasoning_summary_part.done", partDone))
		outputItemDone := `{"type":"response.output_item.done","item":{"id":"","type":"reasoning","encrypted_content":"","summary":[{"type":"summary_text","text":""}]},"output_index":0,"sequence_number":0}`
		outputItemDone, _ = sjson.SetBytesM(outputItemDone, "sequence_number", nextSeq())
		outputItemDone, _ = sjson.SetBytesM(outputItemDone, "item.id", st.ReasoningID)
		outputItemDone, _ = sjson.SetBytesM(outputItemDone, "output_index", st.ReasoningIndex)
		outputItemDone, _ = sjson.SetBytesM(outputItemDone, "item.summary.text", text)
		out = append(out, emitRespEvent("response.output_item.done", outputItemDone))

		st.Reasonings = append(st.Reasonings, oaiToResponsesStateReasoning{ReasoningID: st.ReasoningID, ReasoningData: text})
		st.ReasoningID = ""
	}

	// choices[].delta content / tool_calls / reasoning_content
	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() {
		choices.ForEach(func(_, choice gjson.Result) bool {
			idx := int(choice.Get("index").Int())
			delta := choice.Get("delta")
			if delta.Exists() {
				if c := delta.Get("content"); c.Exists() && c.String() != "" {
					// Ensure the message item and its first content part are announced before any text deltas
					if st.ReasoningID != "" {
						stopReasoning(st.ReasoningBuf.String())
						st.ReasoningBuf.Reset()
					}
					if !st.MsgItemAdded[idx] {
						item := `{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"in_progress","content":[],"role":"assistant"}}`
						item, _ = sjson.SetBytesM(item, "sequence_number", nextSeq())
						item, _ = sjson.SetBytesM(item, "output_index", idx)
						item, _ = sjson.SetBytesM(item, "item.id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
						out = append(out, emitRespEvent("response.output_item.added", item))
						st.MsgItemAdded[idx] = true
					}
					if !st.MsgContentAdded[idx] {
						part := `{"type":"response.content_part.added","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`
						part, _ = sjson.SetBytesM(part, "sequence_number", nextSeq())
						part, _ = sjson.SetBytesM(part, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
						part, _ = sjson.SetBytesM(part, "output_index", idx)
						part, _ = sjson.SetBytesM(part, "content_index", 0)
						out = append(out, emitRespEvent("response.content_part.added", part))
						st.MsgContentAdded[idx] = true
					}

					msg := `{"type":"response.output_text.delta","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"delta":"","logprobs":[]}`
					msg, _ = sjson.SetBytesM(msg, "sequence_number", nextSeq())
					msg, _ = sjson.SetBytesM(msg, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
					msg, _ = sjson.SetBytesM(msg, "output_index", idx)
					msg, _ = sjson.SetBytesM(msg, "content_index", 0)
					msg, _ = sjson.SetBytesM(msg, "delta", c.String())
					out = append(out, emitRespEvent("response.output_text.delta", msg))
					// aggregate for response.output
					if st.MsgTextBuf[idx] == nil {
						st.MsgTextBuf[idx] = &strings.Builder{}
					}
					st.MsgTextBuf[idx].WriteString(c.String())
				}

				// Handle annotations (web search citations)
				if anns := delta.Get("annotations"); anns.Exists() && anns.IsArray() {
					anns.ForEach(func(_, ann gjson.Result) bool {
						entry := map[string]interface{}{
							"type":        ann.Get("type").String(),
							"url":         ann.Get("url").String(),
							"title":       ann.Get("title").String(),
							"start_index": ann.Get("start_index").Int(),
							"end_index":   ann.Get("end_index").Int(),
						}
						st.Annotations[idx] = append(st.Annotations[idx], entry)
						return true
					})
				}

				// reasoning_content (OpenAI reasoning incremental text)
				if rc := delta.Get("reasoning_content"); rc.Exists() && rc.String() != "" {
					// On first appearance, add reasoning item and part
					if st.ReasoningID == "" {
						st.ReasoningID = fmt.Sprintf("rs_%s_%d", st.ResponseID, idx)
						st.ReasoningIndex = idx
						item := `{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"in_progress","summary":[]}}`
						item, _ = sjson.SetBytesM(item, "sequence_number", nextSeq())
						item, _ = sjson.SetBytesM(item, "output_index", idx)
						item, _ = sjson.SetBytesM(item, "item.id", st.ReasoningID)
						out = append(out, emitRespEvent("response.output_item.added", item))
						part := `{"type":"response.reasoning_summary_part.added","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`
						part, _ = sjson.SetBytesM(part, "sequence_number", nextSeq())
						part, _ = sjson.SetBytesM(part, "item_id", st.ReasoningID)
						part, _ = sjson.SetBytesM(part, "output_index", st.ReasoningIndex)
						out = append(out, emitRespEvent("response.reasoning_summary_part.added", part))
					}
					// Append incremental text to reasoning buffer
					st.ReasoningBuf.WriteString(rc.String())
					msg := `{"type":"response.reasoning_summary_text.delta","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"delta":""}`
					msg, _ = sjson.SetBytesM(msg, "sequence_number", nextSeq())
					msg, _ = sjson.SetBytesM(msg, "item_id", st.ReasoningID)
					msg, _ = sjson.SetBytesM(msg, "output_index", st.ReasoningIndex)
					msg, _ = sjson.SetBytesM(msg, "delta", rc.String())
					out = append(out, emitRespEvent("response.reasoning_summary_text.delta", msg))
				}

				// tool calls
				if tcs := delta.Get("tool_calls"); tcs.Exists() && tcs.IsArray() {
					if st.ReasoningID != "" {
						stopReasoning(st.ReasoningBuf.String())
						st.ReasoningBuf.Reset()
					}
					// Before emitting any function events, if a message is open for this index,
					// close its text/content to match Codex expected ordering.
					if st.MsgItemAdded[idx] && !st.MsgItemDone[idx] {
						fullText := ""
						if b := st.MsgTextBuf[idx]; b != nil {
							fullText = b.String()
						}
						done := `{"type":"response.output_text.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"text":"","logprobs":[]}`
						done, _ = sjson.SetBytesM(done, "sequence_number", nextSeq())
						done, _ = sjson.SetBytesM(done, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
						done, _ = sjson.SetBytesM(done, "output_index", idx)
						done, _ = sjson.SetBytesM(done, "content_index", 0)
						done, _ = sjson.SetBytesM(done, "text", fullText)
						out = append(out, emitRespEvent("response.output_text.done", done))

						partDone := `{"type":"response.content_part.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`
						partDone, _ = sjson.SetBytesM(partDone, "sequence_number", nextSeq())
						partDone, _ = sjson.SetBytesM(partDone, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
						partDone, _ = sjson.SetBytesM(partDone, "output_index", idx)
						partDone, _ = sjson.SetBytesM(partDone, "content_index", 0)
						partDone, _ = sjson.SetBytesM(partDone, "part.text", fullText)
						out = append(out, emitRespEvent("response.content_part.done", partDone))

						itemDone := `{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}}`
						itemDone, _ = sjson.SetBytesM(itemDone, "sequence_number", nextSeq())
						itemDone, _ = sjson.SetBytesM(itemDone, "output_index", idx)
						itemDone, _ = sjson.SetBytesM(itemDone, "item.id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
						itemDone, _ = sjson.SetBytesM(itemDone, "item.content.0.text", fullText)
						out = append(out, emitRespEvent("response.output_item.done", itemDone))
						st.MsgItemDone[idx] = true
					}

					// Only emit item.added once per tool call and preserve call_id across chunks.
					newCallID := tcs.Get("0.id").String()
					nameChunk := tcs.Get("0.function.name").String()
					if nameChunk != "" {
						st.FuncNames[idx] = nameChunk
					}
					existingCallID := st.FuncCallIDs[idx]
					effectiveCallID := existingCallID
					shouldEmitItem := false
					if existingCallID == "" && newCallID != "" {
						// First time seeing a valid call_id for this index
						effectiveCallID = newCallID
						st.FuncCallIDs[idx] = newCallID
						shouldEmitItem = true
					}

					if shouldEmitItem && effectiveCallID != "" {
						o := `{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"in_progress","arguments":"","call_id":"","name":""}}`
						o, _ = sjson.SetBytesM(o, "sequence_number", nextSeq())
						o, _ = sjson.SetBytesM(o, "output_index", idx)
						o, _ = sjson.SetBytesM(o, "item.id", fmt.Sprintf("fc_%s", effectiveCallID))
						o, _ = sjson.SetBytesM(o, "item.call_id", effectiveCallID)
						name := st.FuncNames[idx]
						o, _ = sjson.SetBytesM(o, "item.name", name)
						out = append(out, emitRespEvent("response.output_item.added", o))
					}

					// Ensure args buffer exists for this index
					if st.FuncArgsBuf[idx] == nil {
						st.FuncArgsBuf[idx] = &strings.Builder{}
					}

					// Append arguments delta if available and we have a valid call_id to reference
					if args := tcs.Get("0.function.arguments"); args.Exists() && args.String() != "" {
						// Prefer an already known call_id; fall back to newCallID if first time
						refCallID := st.FuncCallIDs[idx]
						if refCallID == "" {
							refCallID = newCallID
						}
						if refCallID != "" {
							ad := `{"type":"response.function_call_arguments.delta","sequence_number":0,"item_id":"","output_index":0,"delta":""}`
							ad, _ = sjson.SetBytesM(ad, "sequence_number", nextSeq())
							ad, _ = sjson.SetBytesM(ad, "item_id", fmt.Sprintf("fc_%s", refCallID))
							ad, _ = sjson.SetBytesM(ad, "output_index", idx)
							ad, _ = sjson.SetBytesM(ad, "delta", args.String())
							out = append(out, emitRespEvent("response.function_call_arguments.delta", ad))
						}
						st.FuncArgsBuf[idx].WriteString(args.String())
					}
				}
			}

			// finish_reason triggers finalization, including text done/content done/item done,
			// reasoning done/part.done, function args done/item done, and completed
			if fr := choice.Get("finish_reason"); fr.Exists() && fr.String() != "" {
				st.StopSeen = true
				// Emit message done events for all indices that started a message
				if len(st.MsgItemAdded) > 0 {
					// sort indices for deterministic order
					idxs := make([]int, 0, len(st.MsgItemAdded))
					for i := range st.MsgItemAdded {
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
						if st.MsgItemAdded[i] && !st.MsgItemDone[i] {
							fullText := ""
							if b := st.MsgTextBuf[i]; b != nil {
								fullText = b.String()
							}
							done := `{"type":"response.output_text.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"text":"","logprobs":[]}`
							done, _ = sjson.SetBytesM(done, "sequence_number", nextSeq())
							done, _ = sjson.SetBytesM(done, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, i))
							done, _ = sjson.SetBytesM(done, "output_index", i)
							done, _ = sjson.SetBytesM(done, "content_index", 0)
							done, _ = sjson.SetBytesM(done, "text", fullText)
							out = append(out, emitRespEvent("response.output_text.done", done))

							partDone := `{"type":"response.content_part.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`
							partDone, _ = sjson.SetBytesM(partDone, "sequence_number", nextSeq())
							partDone, _ = sjson.SetBytesM(partDone, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, i))
							partDone, _ = sjson.SetBytesM(partDone, "output_index", i)
							partDone, _ = sjson.SetBytesM(partDone, "content_index", 0)
							partDone, _ = sjson.SetBytesM(partDone, "part.text", fullText)
							if anns := st.Annotations[i]; len(anns) > 0 {
								partDone, _ = sjson.SetBytesM(partDone, "part.annotations", anns)
							}
							out = append(out, emitRespEvent("response.content_part.done", partDone))

							itemDone := `{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}}`
							itemDone, _ = sjson.SetBytesM(itemDone, "sequence_number", nextSeq())
							itemDone, _ = sjson.SetBytesM(itemDone, "output_index", i)
							itemDone, _ = sjson.SetBytesM(itemDone, "item.id", fmt.Sprintf("msg_%s_%d", st.ResponseID, i))
							itemDone, _ = sjson.SetBytesM(itemDone, "item.content.0.text", fullText)
							if anns := st.Annotations[i]; len(anns) > 0 {
								itemDone, _ = sjson.SetBytesM(itemDone, "item.content.0.annotations", anns)
							}
							out = append(out, emitRespEvent("response.output_item.done", itemDone))
							st.MsgItemDone[i] = true
						}
					}
				}

				if st.ReasoningID != "" {
					stopReasoning(st.ReasoningBuf.String())
					st.ReasoningBuf.Reset()
				}

				// Emit function call done events for any active function calls
				if len(st.FuncCallIDs) > 0 {
					idxs := make([]int, 0, len(st.FuncCallIDs))
					for i := range st.FuncCallIDs {
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
						callID := st.FuncCallIDs[i]
						if callID == "" || st.FuncItemDone[i] {
							continue
						}
						args := "{}"
						if b := st.FuncArgsBuf[i]; b != nil && b.Len() > 0 {
							args = b.String()
						}
						fcDone := `{"type":"response.function_call_arguments.done","sequence_number":0,"item_id":"","output_index":0,"arguments":""}`
						fcDone, _ = sjson.SetBytesM(fcDone, "sequence_number", nextSeq())
						fcDone, _ = sjson.SetBytesM(fcDone, "item_id", fmt.Sprintf("fc_%s", callID))
						fcDone, _ = sjson.SetBytesM(fcDone, "output_index", i)
						fcDone, _ = sjson.SetBytesM(fcDone, "arguments", args)
						out = append(out, emitRespEvent("response.function_call_arguments.done", fcDone))

						itemDone := `{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}}`
						itemDone, _ = sjson.SetBytesM(itemDone, "sequence_number", nextSeq())
						itemDone, _ = sjson.SetBytesM(itemDone, "output_index", i)
						itemDone, _ = sjson.SetBytesM(itemDone, "item.id", fmt.Sprintf("fc_%s", callID))
						itemDone, _ = sjson.SetBytesM(itemDone, "item.arguments", args)
						itemDone, _ = sjson.SetBytesM(itemDone, "item.call_id", callID)
						itemDone, _ = sjson.SetBytesM(itemDone, "item.name", st.FuncNames[i])
						out = append(out, emitRespEvent("response.output_item.done", itemDone))
						st.FuncItemDone[i] = true
						st.FuncArgsDone[i] = true
					}
				}
				completed := `{"type":"response.completed","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null}}`
				completed, _ = sjson.SetBytesM(completed, "sequence_number", nextSeq())
				completed, _ = sjson.SetBytesM(completed, "response.id", st.ResponseID)
				completed, _ = sjson.SetBytesM(completed, "response.created_at", st.Created)
				// Inject original request fields into response as per docs/response.completed.json.
				reqRawJSON := pickRequestJSON(originalRequestRawJSON, requestRawJSON)
				if reqRawJSON != nil {
					req := gjson.ParseBytes(reqRawJSON)
					if v := req.Get("instructions"); v.Exists() {
						completed, _ = sjson.SetBytesM(completed, "response.instructions", v.String())
					}
					if v := req.Get("max_output_tokens"); v.Exists() {
						completed, _ = sjson.SetBytesM(completed, "response.max_output_tokens", v.Int())
					}
					if v := req.Get("max_tool_calls"); v.Exists() {
						completed, _ = sjson.SetBytesM(completed, "response.max_tool_calls", v.Int())
					}
					if v := req.Get("model"); v.Exists() {
						completed, _ = sjson.SetBytesM(completed, "response.model", v.String())
					}
					if v := req.Get("parallel_tool_calls"); v.Exists() {
						completed, _ = sjson.SetBytesM(completed, "response.parallel_tool_calls", v.Bool())
					}
					if v := req.Get("previous_response_id"); v.Exists() {
						completed, _ = sjson.SetBytesM(completed, "response.previous_response_id", v.String())
					}
					if v := req.Get("prompt_cache_key"); v.Exists() {
						completed, _ = sjson.SetBytesM(completed, "response.prompt_cache_key", v.String())
					}
					if v := req.Get("reasoning"); v.Exists() {
						completed, _ = sjson.SetBytesM(completed, "response.reasoning", v.Value())
					}
					if v := req.Get("safety_identifier"); v.Exists() {
						completed, _ = sjson.SetBytesM(completed, "response.safety_identifier", v.String())
					}
					if v := req.Get("service_tier"); v.Exists() {
						completed, _ = sjson.SetBytesM(completed, "response.service_tier", v.String())
					}
					if v := req.Get("store"); v.Exists() {
						completed, _ = sjson.SetBytesM(completed, "response.store", v.Bool())
					}
					if v := req.Get("temperature"); v.Exists() {
						completed, _ = sjson.SetBytesM(completed, "response.temperature", v.Float())
					}
					if v := req.Get("text"); v.Exists() {
						completed, _ = sjson.SetBytesM(completed, "response.text", v.Value())
					}
					if v := req.Get("tool_choice"); v.Exists() {
						completed, _ = sjson.SetBytesM(completed, "response.tool_choice", v.Value())
					}
					if v := req.Get("tools"); v.Exists() {
						completed, _ = sjson.SetBytesM(completed, "response.tools", v.Value())
					}
					if v := req.Get("top_logprobs"); v.Exists() {
						completed, _ = sjson.SetBytesM(completed, "response.top_logprobs", v.Int())
					}
					if v := req.Get("top_p"); v.Exists() {
						completed, _ = sjson.SetBytesM(completed, "response.top_p", v.Float())
					}
					if v := req.Get("truncation"); v.Exists() {
						completed, _ = sjson.SetBytesM(completed, "response.truncation", v.String())
					}
					if v := req.Get("user"); v.Exists() {
						completed, _ = sjson.SetBytesM(completed, "response.user", v.Value())
					}
					if v := req.Get("metadata"); v.Exists() {
						completed, _ = sjson.SetBytesM(completed, "response.metadata", v.Value())
					}
				}
				// Build response.output using aggregated buffers
				outputsWrapper := `{"arr":[]}`
				if len(st.Reasonings) > 0 {
					for _, r := range st.Reasonings {
						item := `{"id":"","type":"reasoning","summary":[{"type":"summary_text","text":""}]}`
						item, _ = sjson.SetBytesM(item, "id", r.ReasoningID)
						item, _ = sjson.SetBytesM(item, "summary.0.text", r.ReasoningData)
						outputsWrapper, _ = sjson.SetRaw(outputsWrapper, "arr.-1", item)
					}
				}
				// Append message items in ascending index order
				if len(st.MsgItemAdded) > 0 {
					midxs := make([]int, 0, len(st.MsgItemAdded))
					for i := range st.MsgItemAdded {
						midxs = append(midxs, i)
					}
					for i := 0; i < len(midxs); i++ {
						for j := i + 1; j < len(midxs); j++ {
							if midxs[j] < midxs[i] {
								midxs[i], midxs[j] = midxs[j], midxs[i]
							}
						}
					}
					for _, i := range midxs {
						txt := ""
						if b := st.MsgTextBuf[i]; b != nil {
							txt = b.String()
						}
						item := `{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}`
						item, _ = sjson.SetBytesM(item, "id", fmt.Sprintf("msg_%s_%d", st.ResponseID, i))
						item, _ = sjson.SetBytesM(item, "content.0.text", txt)
						outputsWrapper, _ = sjson.SetRaw(outputsWrapper, "arr.-1", item)
					}
				}
				if len(st.FuncArgsBuf) > 0 {
					idxs := make([]int, 0, len(st.FuncArgsBuf))
					for i := range st.FuncArgsBuf {
						idxs = append(idxs, i)
					}
					// small-N sort without extra imports
					for i := 0; i < len(idxs); i++ {
						for j := i + 1; j < len(idxs); j++ {
							if idxs[j] < idxs[i] {
								idxs[i], idxs[j] = idxs[j], idxs[i]
							}
						}
					}
					for _, i := range idxs {
						args := ""
						if b := st.FuncArgsBuf[i]; b != nil {
							args = b.String()
						}
						callID := st.FuncCallIDs[i]
						name := st.FuncNames[i]
						item := `{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`
						item, _ = sjson.SetBytesM(item, "id", fmt.Sprintf("fc_%s", callID))
						item, _ = sjson.SetBytesM(item, "arguments", args)
						item, _ = sjson.SetBytesM(item, "call_id", callID)
						item, _ = sjson.SetBytesM(item, "name", name)
						outputsWrapper, _ = sjson.SetRaw(outputsWrapper, "arr.-1", item)
					}
				}
				if gjson.Get(outputsWrapper, "arr.#").Int() > 0 {
					completed, _ = sjson.SetRaw(completed, "response.output", gjson.Get(outputsWrapper, "arr").Raw)
				}
				if st.UsageSeen {
					completed, _ = sjson.SetBytesM(completed, "response.usage.input_tokens", st.PromptTokens)
					completed, _ = sjson.SetBytesM(completed, "response.usage.input_tokens_details.cached_tokens", st.CachedTokens)
					completed, _ = sjson.SetBytesM(completed, "response.usage.output_tokens", st.CompletionTokens)
					if st.ReasoningTokens > 0 {
						completed, _ = sjson.SetBytesM(completed, "response.usage.output_tokens_details.reasoning_tokens", st.ReasoningTokens)
					}
					total := st.TotalTokens
					if total == 0 {
						total = st.PromptTokens + st.CompletionTokens
					}
					completed, _ = sjson.SetBytesM(completed, "response.usage.total_tokens", total)
				}
				out = append(out, emitRespEvent("response.completed", completed))
				st.CompletionSent = true
			}

			return true
		})
	}

	return out
}

// ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream builds a single Responses JSON
// from a non-streaming OpenAI Chat Completions response.
func ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) string {
	root := unwrapOpenAIChatCompletionResult(gjson.ParseBytes(rawJSON))

	// Basic response scaffold
	resp := `{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"incomplete_details":null}`

	// id: use provider id if present, otherwise synthesize
	id := root.Get("id").String()
	if id == "" {
		id = fmt.Sprintf("resp_%x_%d", time.Now().UnixNano(), atomic.AddUint64(&responseIDCounter, 1))
	}
	resp, _ = sjson.SetBytesM(resp, "id", id)

	// created_at: map from chat.completion created
	created := root.Get("created").Int()
	if created == 0 {
		created = time.Now().Unix()
	}
	resp, _ = sjson.SetBytesM(resp, "created_at", created)

	// Echo request fields when available (aligns with streaming path behavior)
	reqRawJSON := pickRequestJSON(originalRequestRawJSON, requestRawJSON)
	if reqRawJSON != nil {
		req := gjson.ParseBytes(reqRawJSON)
		if v := req.Get("instructions"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "instructions", v.String())
		}
		if v := req.Get("max_output_tokens"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "max_output_tokens", v.Int())
		} else {
			// Also support max_tokens from chat completion style
			if v = req.Get("max_tokens"); v.Exists() {
				resp, _ = sjson.SetBytesM(resp, "max_output_tokens", v.Int())
			}
		}
		if v := req.Get("max_tool_calls"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "max_tool_calls", v.Int())
		}
		if v := req.Get("model"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "model", v.String())
		} else if v = root.Get("model"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "model", v.String())
		}
		if v := req.Get("parallel_tool_calls"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "parallel_tool_calls", v.Bool())
		}
		if v := req.Get("previous_response_id"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "previous_response_id", v.String())
		}
		if v := req.Get("prompt_cache_key"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "prompt_cache_key", v.String())
		}
		if v := req.Get("reasoning"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "reasoning", v.Value())
		}
		if v := req.Get("safety_identifier"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "safety_identifier", v.String())
		}
		if v := req.Get("service_tier"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "service_tier", v.String())
		}
		if v := req.Get("store"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "store", v.Bool())
		}
		if v := req.Get("temperature"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "temperature", v.Float())
		}
		if v := req.Get("text"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "text", v.Value())
		}
		if v := req.Get("tool_choice"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "tool_choice", v.Value())
		}
		if v := req.Get("tools"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "tools", v.Value())
		}
		if v := req.Get("top_logprobs"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "top_logprobs", v.Int())
		}
		if v := req.Get("top_p"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "top_p", v.Float())
		}
		if v := req.Get("truncation"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "truncation", v.String())
		}
		if v := req.Get("user"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "user", v.Value())
		}
		if v := req.Get("metadata"); v.Exists() {
			resp, _ = sjson.SetBytesM(resp, "metadata", v.Value())
		}
	} else if v := root.Get("model"); v.Exists() {
		// Fallback model from response
		resp, _ = sjson.SetBytesM(resp, "model", v.String())
	}

	// Build output list from choices[...]
	outputsWrapper := `{"arr":[]}`
	// Detect and capture reasoning content if present
	rcText := gjson.GetBytes(rawJSON, "choices.0.message.reasoning_content").String()
	includeReasoning := rcText != ""
	if !includeReasoning && reqRawJSON != nil {
		includeReasoning = gjson.GetBytes(reqRawJSON, "reasoning").Exists()
	}
	if includeReasoning {
		rid := strings.TrimPrefix(id, "resp_")
		// Prefer summary_text from reasoning_content; encrypted_content is optional
		reasoningItem := `{"id":"","type":"reasoning","encrypted_content":"","summary":[]}`
		reasoningItem, _ = sjson.SetBytesM(reasoningItem, "id", fmt.Sprintf("rs_%s", rid))
		if rcText != "" {
			reasoningItem, _ = sjson.SetBytesM(reasoningItem, "summary.0.type", "summary_text")
			reasoningItem, _ = sjson.SetBytesM(reasoningItem, "summary.0.text", rcText)
		}
		outputsWrapper, _ = sjson.SetRaw(outputsWrapper, "arr.-1", reasoningItem)
	}

	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() {
		choices.ForEach(func(_, choice gjson.Result) bool {
			msg := choice.Get("message")
			if msg.Exists() {
				// Text message part
				if c := msg.Get("content"); c.Exists() && c.String() != "" {
					item := `{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}`
					item, _ = sjson.SetBytesM(item, "id", fmt.Sprintf("msg_%s_%d", id, int(choice.Get("index").Int())))
					item, _ = sjson.SetBytesM(item, "content.0.text", c.String())
					// Include annotations from message if present
					if anns := msg.Get("annotations"); anns.Exists() && anns.IsArray() {
						var annotations []interface{}
						anns.ForEach(func(_, ann gjson.Result) bool {
							annotations = append(annotations, map[string]interface{}{
								"type":        ann.Get("type").String(),
								"url":         ann.Get("url").String(),
								"title":       ann.Get("title").String(),
								"start_index": ann.Get("start_index").Int(),
								"end_index":   ann.Get("end_index").Int(),
							})
							return true
						})
						if len(annotations) > 0 {
							item, _ = sjson.SetBytesM(item, "content.0.annotations", annotations)
						}
					}
					outputsWrapper, _ = sjson.SetRaw(outputsWrapper, "arr.-1", item)
				}

				// Function/tool calls
				if tcs := msg.Get("tool_calls"); tcs.Exists() && tcs.IsArray() {
					tcs.ForEach(func(_, tc gjson.Result) bool {
						callID := tc.Get("id").String()
						name := tc.Get("function.name").String()
						args := tc.Get("function.arguments").String()
						item := `{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`
						item, _ = sjson.SetBytesM(item, "id", fmt.Sprintf("fc_%s", callID))
						item, _ = sjson.SetBytesM(item, "arguments", args)
						item, _ = sjson.SetBytesM(item, "call_id", callID)
						item, _ = sjson.SetBytesM(item, "name", name)
						outputsWrapper, _ = sjson.SetRaw(outputsWrapper, "arr.-1", item)
						return true
					})
				}
			}
			return true
		})
	}
	if gjson.Get(outputsWrapper, "arr.#").Int() > 0 {
		resp, _ = sjson.SetRaw(resp, "output", gjson.Get(outputsWrapper, "arr").Raw)
	}

	// usage mapping
	if usage := root.Get("usage"); usage.Exists() {
		// Map common tokens
		if usage.Get("prompt_tokens").Exists() || usage.Get("completion_tokens").Exists() || usage.Get("total_tokens").Exists() {
			resp, _ = sjson.SetBytesM(resp, "usage.input_tokens", usage.Get("prompt_tokens").Int())
			if d := usage.Get("prompt_tokens_details.cached_tokens"); d.Exists() {
				resp, _ = sjson.SetBytesM(resp, "usage.input_tokens_details.cached_tokens", d.Int())
			}
			resp, _ = sjson.SetBytesM(resp, "usage.output_tokens", usage.Get("completion_tokens").Int())
			// Reasoning tokens not available in Chat Completions; set only if present under output_tokens_details
			if d := usage.Get("output_tokens_details.reasoning_tokens"); d.Exists() {
				resp, _ = sjson.SetBytesM(resp, "usage.output_tokens_details.reasoning_tokens", d.Int())
			}
			resp, _ = sjson.SetBytesM(resp, "usage.total_tokens", usage.Get("total_tokens").Int())
		} else {
			// Fallback to raw usage object if structure differs
			resp, _ = sjson.SetBytesM(resp, "usage", usage.Value())
		}
	}

	return resp
}
