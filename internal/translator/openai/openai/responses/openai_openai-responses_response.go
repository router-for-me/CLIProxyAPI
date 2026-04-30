package responses

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type oaiToResponsesStateReasoning struct {
	ReasoningID   string
	ReasoningData string
	OutputIndex   int
}
type oaiToResponsesState struct {
	Seq               int
	ResponseID        string
	Created           int64
	Started           bool
	CompletionPending bool
	CompletedEmitted  bool
	ReasoningID       string
	ReasoningIndex    int
	// aggregation buffers for response.output
	// Per-output message text buffers by index
	MsgTextBuf   map[int]*strings.Builder
	ReasoningBuf strings.Builder
	Reasonings   []oaiToResponsesStateReasoning
	FuncArgsBuf  map[string]*strings.Builder
	FuncNames    map[string]string
	FuncCallIDs  map[string]string
	FuncOutputIx map[string]int
	MsgOutputIx  map[int]int
	NextOutputIx int
	// message item state per output index
	MsgItemAdded    map[int]bool // whether response.output_item.added emitted for message
	MsgContentAdded map[int]bool // whether response.content_part.added emitted for message
	MsgItemDone     map[int]bool // whether message done events were emitted
	// function item done state
	FuncArgsDone map[string]bool
	FuncItemDone map[string]bool
	// usage aggregation
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	ReasoningTokens  int64
	UsageSeen        bool
}

// responseIDCounter provides a process-wide unique counter for synthesized response identifiers.
var responseIDCounter uint64

func emitRespEvent(event string, payload []byte) []byte {
	return translatorcommon.SSEEventData(event, payload)
}

func buildResponsesCompletedEvent(st *oaiToResponsesState, requestRawJSON []byte, nextSeq func() int) []byte {
	completed := []byte(`{"type":"response.completed","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null}}`)
	completed, _ = sjson.SetBytes(completed, "sequence_number", nextSeq())
	completed, _ = sjson.SetBytes(completed, "response.id", st.ResponseID)
	completed, _ = sjson.SetBytes(completed, "response.created_at", st.Created)
	// Inject original request fields into response as per docs/response.completed.json
	if requestRawJSON != nil {
		req := gjson.ParseBytes(requestRawJSON)
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

	outputsWrapper := []byte(`{"arr":[]}`)
	type completedOutputItem struct {
		index int
		raw   []byte
	}
	outputItems := make([]completedOutputItem, 0, len(st.Reasonings)+len(st.MsgItemAdded)+len(st.FuncArgsBuf))
	if len(st.Reasonings) > 0 {
		for _, r := range st.Reasonings {
			item := []byte(`{"id":"","type":"reasoning","summary":[{"type":"summary_text","text":""}]}`)
			item, _ = sjson.SetBytes(item, "id", r.ReasoningID)
			item, _ = sjson.SetBytes(item, "summary.0.text", r.ReasoningData)
			outputItems = append(outputItems, completedOutputItem{index: r.OutputIndex, raw: item})
		}
	}
	if len(st.MsgItemAdded) > 0 {
		for i := range st.MsgItemAdded {
			txt := ""
			if b := st.MsgTextBuf[i]; b != nil {
				txt = b.String()
			}
			item := []byte(`{"id":"","type":"message","status":"completed","content":[],"role":"assistant"}`)
			item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("msg_%s_%d", st.ResponseID, i))
			// Insert reasoning_text content part at index 0 if reasoning data is available
			if len(st.Reasonings) > 0 {
				rp := []byte(`{"type":"reasoning_text","text":""}`)
				rp, _ = sjson.SetBytes(rp, "text", st.Reasonings[len(st.Reasonings)-1].ReasoningData)
				item, _ = sjson.SetRawBytes(item, "content.-1", rp)
			}
			op := []byte(`{"type":"output_text","annotations":[],"logprobs":[],"text":""}`)
			op, _ = sjson.SetBytes(op, "text", txt)
			item, _ = sjson.SetRawBytes(item, "content.-1", op)
			outputItems = append(outputItems, completedOutputItem{index: st.MsgOutputIx[i], raw: item})
		}
	}
	if len(st.FuncArgsBuf) > 0 {
		for key := range st.FuncArgsBuf {
			args := ""
			if b := st.FuncArgsBuf[key]; b != nil {
				args = b.String()
			}
			callID := st.FuncCallIDs[key]
			name := st.FuncNames[key]
			item := []byte(`{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`)
			item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("fc_%s", callID))
			item, _ = sjson.SetBytes(item, "arguments", args)
			item, _ = sjson.SetBytes(item, "call_id", callID)
			item, _ = sjson.SetBytes(item, "name", name)
			outputItems = append(outputItems, completedOutputItem{index: st.FuncOutputIx[key], raw: item})
		}
	}
	sort.Slice(outputItems, func(i, j int) bool { return outputItems[i].index < outputItems[j].index })
	for _, item := range outputItems {
		outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, "arr.-1", item.raw)
	}
	if gjson.GetBytes(outputsWrapper, "arr.#").Int() > 0 {
		completed, _ = sjson.SetRawBytes(completed, "response.output", []byte(gjson.GetBytes(outputsWrapper, "arr").Raw))
	}
	if st.UsageSeen {
		completed, _ = sjson.SetBytes(completed, "response.usage.input_tokens", st.PromptTokens)
		completed, _ = sjson.SetBytes(completed, "response.usage.input_tokens_details.cached_tokens", st.CachedTokens)
		completed, _ = sjson.SetBytes(completed, "response.usage.output_tokens", st.CompletionTokens)
		if st.ReasoningTokens > 0 {
			completed, _ = sjson.SetBytes(completed, "response.usage.output_tokens_details.reasoning_tokens", st.ReasoningTokens)
		}
		total := st.TotalTokens
		if total == 0 {
			total = st.PromptTokens + st.CompletionTokens
		}
		completed, _ = sjson.SetBytes(completed, "response.usage.total_tokens", total)
	}
	return emitRespEvent("response.completed", completed)
}

// ConvertOpenAIChatCompletionsResponseToOpenAIResponses converts OpenAI Chat Completions streaming chunks
// to OpenAI Responses SSE events (response.*).
func ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &oaiToResponsesState{
			FuncArgsBuf:     make(map[string]*strings.Builder),
			FuncNames:       make(map[string]string),
			FuncCallIDs:     make(map[string]string),
			FuncOutputIx:    make(map[string]int),
			MsgOutputIx:     make(map[int]int),
			MsgTextBuf:      make(map[int]*strings.Builder),
			MsgItemAdded:    make(map[int]bool),
			MsgContentAdded: make(map[int]bool),
			MsgItemDone:     make(map[int]bool),
			FuncArgsDone:    make(map[string]bool),
			FuncItemDone:    make(map[string]bool),
			Reasonings:      make([]oaiToResponsesStateReasoning, 0),
		}
	}
	st := (*param).(*oaiToResponsesState)

	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}

	rawJSON = bytes.TrimSpace(rawJSON)
	if len(rawJSON) == 0 {
		return [][]byte{}
	}
	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		if st.CompletionPending && !st.CompletedEmitted {
			st.CompletedEmitted = true
			return [][]byte{buildResponsesCompletedEvent(st, requestRawJSON, func() int { st.Seq++; return st.Seq })}
		}
		return [][]byte{}
	}

	root := gjson.ParseBytes(rawJSON)
	obj := root.Get("object")
	if obj.Exists() && obj.String() != "" && obj.String() != "chat.completion.chunk" {
		return [][]byte{}
	}
	if !root.Get("choices").Exists() || !root.Get("choices").IsArray() {
		return [][]byte{}
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
	allocOutputIndex := func() int {
		ix := st.NextOutputIx
		st.NextOutputIx++
		return ix
	}
	toolStateKey := func(outputIndex, toolIndex int) string { return fmt.Sprintf("%d:%d", outputIndex, toolIndex) }
	var out [][]byte

	if !st.Started {
		st.ResponseID = root.Get("id").String()
		st.Created = root.Get("created").Int()
		// reset aggregation state for a new streaming response
		st.MsgTextBuf = make(map[int]*strings.Builder)
		st.ReasoningBuf.Reset()
		st.ReasoningID = ""
		st.ReasoningIndex = 0
		st.FuncArgsBuf = make(map[string]*strings.Builder)
		st.FuncNames = make(map[string]string)
		st.FuncCallIDs = make(map[string]string)
		st.FuncOutputIx = make(map[string]int)
		st.MsgOutputIx = make(map[int]int)
		st.NextOutputIx = 0
		st.MsgItemAdded = make(map[int]bool)
		st.MsgContentAdded = make(map[int]bool)
		st.MsgItemDone = make(map[int]bool)
		st.FuncArgsDone = make(map[string]bool)
		st.FuncItemDone = make(map[string]bool)
		st.PromptTokens = 0
		st.CachedTokens = 0
		st.CompletionTokens = 0
		st.TotalTokens = 0
		st.ReasoningTokens = 0
		st.UsageSeen = false
		st.CompletionPending = false
		st.CompletedEmitted = false
		// response.created
		created := []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null,"output":[]}}`)
		created, _ = sjson.SetBytes(created, "sequence_number", nextSeq())
		created, _ = sjson.SetBytes(created, "response.id", st.ResponseID)
		created, _ = sjson.SetBytes(created, "response.created_at", st.Created)
		out = append(out, emitRespEvent("response.created", created))

		inprog := []byte(`{"type":"response.in_progress","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress"}}`)
		inprog, _ = sjson.SetBytes(inprog, "sequence_number", nextSeq())
		inprog, _ = sjson.SetBytes(inprog, "response.id", st.ResponseID)
		inprog, _ = sjson.SetBytes(inprog, "response.created_at", st.Created)
		out = append(out, emitRespEvent("response.in_progress", inprog))
		st.Started = true
	}

	stopReasoning := func(text string) {
		// Emit reasoning done events
		textDone := []byte(`{"type":"response.reasoning_summary_text.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"text":""}`)
		textDone, _ = sjson.SetBytes(textDone, "sequence_number", nextSeq())
		textDone, _ = sjson.SetBytes(textDone, "item_id", st.ReasoningID)
		textDone, _ = sjson.SetBytes(textDone, "output_index", st.ReasoningIndex)
		textDone, _ = sjson.SetBytes(textDone, "text", text)
		out = append(out, emitRespEvent("response.reasoning_summary_text.done", textDone))
		partDone := []byte(`{"type":"response.reasoning_summary_part.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`)
		partDone, _ = sjson.SetBytes(partDone, "sequence_number", nextSeq())
		partDone, _ = sjson.SetBytes(partDone, "item_id", st.ReasoningID)
		partDone, _ = sjson.SetBytes(partDone, "output_index", st.ReasoningIndex)
		partDone, _ = sjson.SetBytes(partDone, "part.text", text)
		out = append(out, emitRespEvent("response.reasoning_summary_part.done", partDone))
		outputItemDone := []byte(`{"type":"response.output_item.done","item":{"id":"","type":"reasoning","encrypted_content":"","summary":[{"type":"summary_text","text":""}]},"output_index":0,"sequence_number":0}`)
		outputItemDone, _ = sjson.SetBytes(outputItemDone, "sequence_number", nextSeq())
		outputItemDone, _ = sjson.SetBytes(outputItemDone, "item.id", st.ReasoningID)
		outputItemDone, _ = sjson.SetBytes(outputItemDone, "output_index", st.ReasoningIndex)
		outputItemDone, _ = sjson.SetBytes(outputItemDone, "item.summary.text", text)
		out = append(out, emitRespEvent("response.output_item.done", outputItemDone))

		st.Reasonings = append(st.Reasonings, oaiToResponsesStateReasoning{ReasoningID: st.ReasoningID, ReasoningData: text, OutputIndex: st.ReasoningIndex})
		st.ReasoningID = ""
	}

	// choices[].delta content / tool_calls / reasoning_content
	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() {
		choices.ForEach(func(_, choice gjson.Result) bool {
			idx := int(choice.Get("index").Int())
			delta := choice.Get("delta")
			if delta.Exists() {
				if c := delta.Get("content"); c.Exists() && c.String() != "" {
					var reasoningText string
					if st.ReasoningID != "" {
						reasoningText = st.ReasoningBuf.String()
						stopReasoning(st.ReasoningBuf.String())
						st.ReasoningBuf.Reset()
					}
					if _, exists := st.MsgOutputIx[idx]; !exists {
						st.MsgOutputIx[idx] = allocOutputIndex()
					}
					msgOutputIndex := st.MsgOutputIx[idx]
					if !st.MsgItemAdded[idx] {
						item := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"in_progress","content":[],"role":"assistant"}}`)
						item, _ = sjson.SetBytes(item, "sequence_number", nextSeq())
						item, _ = sjson.SetBytes(item, "output_index", msgOutputIndex)
						item, _ = sjson.SetBytes(item, "item.id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
						out = append(out, emitRespEvent("response.output_item.added", item))
						st.MsgItemAdded[idx] = true
					}
					if !st.MsgContentAdded[idx] {
						nextContentIdx := 0
						if reasoningText != "" {
							rp := []byte(`{"type":"response.content_part.added","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"reasoning_text","text":""}}`)
							rp, _ = sjson.SetBytes(rp, "sequence_number", nextSeq())
							rp, _ = sjson.SetBytes(rp, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
							rp, _ = sjson.SetBytes(rp, "output_index", msgOutputIndex)
							rp, _ = sjson.SetBytes(rp, "content_index", 0)
							rp, _ = sjson.SetBytes(rp, "part.text", reasoningText)
							out = append(out, emitRespEvent("response.content_part.added", rp))
							nextContentIdx = 1
						}
						part := []byte(`{"type":"response.content_part.added","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
						part, _ = sjson.SetBytes(part, "sequence_number", nextSeq())
						part, _ = sjson.SetBytes(part, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
						part, _ = sjson.SetBytes(part, "output_index", msgOutputIndex)
						part, _ = sjson.SetBytes(part, "content_index", nextContentIdx)
						out = append(out, emitRespEvent("response.content_part.added", part))
						st.MsgContentAdded[idx] = true
					}

					msg := []byte(`{"type":"response.output_text.delta","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"delta":"","logprobs":[]}`)
					msg, _ = sjson.SetBytes(msg, "sequence_number", nextSeq())
					msg, _ = sjson.SetBytes(msg, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
					msg, _ = sjson.SetBytes(msg, "output_index", msgOutputIndex)
					msg, _ = sjson.SetBytes(msg, "content_index", 0)
					msg, _ = sjson.SetBytes(msg, "delta", c.String())
					out = append(out, emitRespEvent("response.output_text.delta", msg))
					// aggregate for response.output
					if st.MsgTextBuf[idx] == nil {
						st.MsgTextBuf[idx] = &strings.Builder{}
					}
					st.MsgTextBuf[idx].WriteString(c.String())
				}

				// reasoning_content — always pass through from upstream Chat Completions.
				// All models that return reasoning_content get it converted to a
				// reasoning output item. The text is also embedded as a reasoning_text
				// content part in the assistant message for round-trip echo-back.
				if rc := delta.Get("reasoning_content"); rc.Exists() && rc.String() != "" {
					// On first appearance, add reasoning item and part
					if st.ReasoningID == "" {
						st.ReasoningID = fmt.Sprintf("rs_%s_%d", st.ResponseID, idx)
						st.ReasoningIndex = allocOutputIndex()
						item := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"in_progress","summary":[]}}`)
						item, _ = sjson.SetBytes(item, "sequence_number", nextSeq())
						item, _ = sjson.SetBytes(item, "output_index", st.ReasoningIndex)
						item, _ = sjson.SetBytes(item, "item.id", st.ReasoningID)
						out = append(out, emitRespEvent("response.output_item.added", item))
						part := []byte(`{"type":"response.reasoning_summary_part.added","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`)
						part, _ = sjson.SetBytes(part, "sequence_number", nextSeq())
						part, _ = sjson.SetBytes(part, "item_id", st.ReasoningID)
						part, _ = sjson.SetBytes(part, "output_index", st.ReasoningIndex)
						out = append(out, emitRespEvent("response.reasoning_summary_part.added", part))
					}
					// Append incremental text to reasoning buffer
					st.ReasoningBuf.WriteString(rc.String())
					msg := []byte(`{"type":"response.reasoning_summary_text.delta","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"delta":""}`)
					msg, _ = sjson.SetBytes(msg, "sequence_number", nextSeq())
					msg, _ = sjson.SetBytes(msg, "item_id", st.ReasoningID)
					msg, _ = sjson.SetBytes(msg, "output_index", st.ReasoningIndex)
					msg, _ = sjson.SetBytes(msg, "delta", rc.String())
					out = append(out, emitRespEvent("response.reasoning_summary_text.delta", msg))
				}

				// tool calls
				if tcs := delta.Get("tool_calls"); tcs.Exists() && tcs.IsArray() {
					var reasoningText string
					if st.ReasoningID != "" {
						reasoningText = st.ReasoningBuf.String()
						stopReasoning(st.ReasoningBuf.String())
						st.ReasoningBuf.Reset()
					}
					// Before emitting any function events, if a message is open for this index,
					// close its text/content to match Codex expected ordering.
					if st.MsgItemAdded[idx] && !st.MsgItemDone[idx] {
						msgOutputIndex := st.MsgOutputIx[idx]
						fullText := ""
						if b := st.MsgTextBuf[idx]; b != nil {
							fullText = b.String()
						}
						done := []byte(`{"type":"response.output_text.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"text":"","logprobs":[]}`)
						done, _ = sjson.SetBytes(done, "sequence_number", nextSeq())
						done, _ = sjson.SetBytes(done, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
						done, _ = sjson.SetBytes(done, "output_index", msgOutputIndex)
						done, _ = sjson.SetBytes(done, "content_index", 0)
						done, _ = sjson.SetBytes(done, "text", fullText)
						out = append(out, emitRespEvent("response.output_text.done", done))

						partDone := []byte(`{"type":"response.content_part.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
						partDone, _ = sjson.SetBytes(partDone, "sequence_number", nextSeq())
						partDone, _ = sjson.SetBytes(partDone, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
						partDone, _ = sjson.SetBytes(partDone, "output_index", msgOutputIndex)
						partDone, _ = sjson.SetBytes(partDone, "content_index", 0)
						partDone, _ = sjson.SetBytes(partDone, "part.text", fullText)
						out = append(out, emitRespEvent("response.content_part.done", partDone))

						contentArr := []byte(`[]`)
						if reasoningText != "" {
							rp := []byte(`{"type":"reasoning_text","text":""}`)
							rp, _ = sjson.SetBytes(rp, "text", reasoningText)
							contentArr, _ = sjson.SetRawBytes(contentArr, "-1", rp)
						}
						op := []byte(`{"type":"output_text","annotations":[],"logprobs":[],"text":""}`)
						op, _ = sjson.SetBytes(op, "text", fullText)
						contentArr, _ = sjson.SetRawBytes(contentArr, "-1", op)
						itemDone := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"completed","role":"assistant"}}`)
						itemDone, _ = sjson.SetBytes(itemDone, "sequence_number", nextSeq())
						itemDone, _ = sjson.SetBytes(itemDone, "output_index", msgOutputIndex)
						itemDone, _ = sjson.SetBytes(itemDone, "item.id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
						itemDone, _ = sjson.SetRawBytes(itemDone, "item.content", contentArr)
						out = append(out, emitRespEvent("response.output_item.done", itemDone))
						st.MsgItemDone[idx] = true
					}

					tcs.ForEach(func(_, tc gjson.Result) bool {
						toolIndex := int(tc.Get("index").Int())
						key := toolStateKey(idx, toolIndex)
						newCallID := tc.Get("id").String()
						nameChunk := tc.Get("function.name").String()
						if nameChunk != "" {
							st.FuncNames[key] = nameChunk
						}

						existingCallID := st.FuncCallIDs[key]
						effectiveCallID := existingCallID
						shouldEmitItem := false
						if existingCallID == "" && newCallID != "" {
							effectiveCallID = newCallID
							st.FuncCallIDs[key] = newCallID
							st.FuncOutputIx[key] = allocOutputIndex()
							shouldEmitItem = true
						}

						if shouldEmitItem && effectiveCallID != "" {
							outputIndex := st.FuncOutputIx[key]
							o := []byte(`{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"in_progress","arguments":"","call_id":"","name":""}}`)
							o, _ = sjson.SetBytes(o, "sequence_number", nextSeq())
							o, _ = sjson.SetBytes(o, "output_index", outputIndex)
							o, _ = sjson.SetBytes(o, "item.id", fmt.Sprintf("fc_%s", effectiveCallID))
							o, _ = sjson.SetBytes(o, "item.call_id", effectiveCallID)
							o, _ = sjson.SetBytes(o, "item.name", st.FuncNames[key])
							out = append(out, emitRespEvent("response.output_item.added", o))
						}

						if st.FuncArgsBuf[key] == nil {
							st.FuncArgsBuf[key] = &strings.Builder{}
						}

						if args := tc.Get("function.arguments"); args.Exists() && args.String() != "" {
							refCallID := st.FuncCallIDs[key]
							if refCallID == "" {
								refCallID = newCallID
							}
							if refCallID != "" {
								outputIndex := st.FuncOutputIx[key]
								ad := []byte(`{"type":"response.function_call_arguments.delta","sequence_number":0,"item_id":"","output_index":0,"delta":""}`)
								ad, _ = sjson.SetBytes(ad, "sequence_number", nextSeq())
								ad, _ = sjson.SetBytes(ad, "item_id", fmt.Sprintf("fc_%s", refCallID))
								ad, _ = sjson.SetBytes(ad, "output_index", outputIndex)
								ad, _ = sjson.SetBytes(ad, "delta", args.String())
								out = append(out, emitRespEvent("response.function_call_arguments.delta", ad))
							}
							st.FuncArgsBuf[key].WriteString(args.String())
						}
						return true
					})
				}
			}

			// finish_reason triggers item-level finalization. response.completed is
			// deferred until the terminal [DONE] marker so late usage-only chunks can
			// still populate response.usage.
			if fr := choice.Get("finish_reason"); fr.Exists() && fr.String() != "" {
				// Emit message done events for all indices that started a message
				if len(st.MsgItemAdded) > 0 {
					// sort indices for deterministic order
					idxs := make([]int, 0, len(st.MsgItemAdded))
					for i := range st.MsgItemAdded {
						idxs = append(idxs, i)
					}
					sort.Slice(idxs, func(i, j int) bool { return st.MsgOutputIx[idxs[i]] < st.MsgOutputIx[idxs[j]] })
					for _, i := range idxs {
						if st.MsgItemAdded[i] && !st.MsgItemDone[i] {
							msgOutputIndex := st.MsgOutputIx[i]
							fullText := ""
							if b := st.MsgTextBuf[i]; b != nil {
								fullText = b.String()
							}
							done := []byte(`{"type":"response.output_text.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"text":"","logprobs":[]}`)
							done, _ = sjson.SetBytes(done, "sequence_number", nextSeq())
							done, _ = sjson.SetBytes(done, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, i))
							done, _ = sjson.SetBytes(done, "output_index", msgOutputIndex)
							done, _ = sjson.SetBytes(done, "content_index", 0)
							done, _ = sjson.SetBytes(done, "text", fullText)
							out = append(out, emitRespEvent("response.output_text.done", done))

							partDone := []byte(`{"type":"response.content_part.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`)
							partDone, _ = sjson.SetBytes(partDone, "sequence_number", nextSeq())
							partDone, _ = sjson.SetBytes(partDone, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, i))
							partDone, _ = sjson.SetBytes(partDone, "output_index", msgOutputIndex)
							partDone, _ = sjson.SetBytes(partDone, "content_index", 0)
							partDone, _ = sjson.SetBytes(partDone, "part.text", fullText)
							out = append(out, emitRespEvent("response.content_part.done", partDone))

							// Build content array with reasoning_text if available
							contentArr := []byte(`[]`)
							if len(st.Reasonings) > 0 {
								rp := []byte(`{"type":"reasoning_text","text":""}`)
								rp, _ = sjson.SetBytes(rp, "text", st.Reasonings[len(st.Reasonings)-1].ReasoningData)
								contentArr, _ = sjson.SetRawBytes(contentArr, "-1", rp)
							}
							op := []byte(`{"type":"output_text","annotations":[],"logprobs":[],"text":""}`)
							op, _ = sjson.SetBytes(op, "text", fullText)
							contentArr, _ = sjson.SetRawBytes(contentArr, "-1", op)
							itemDone := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"completed","role":"assistant"}}`)
							itemDone, _ = sjson.SetBytes(itemDone, "sequence_number", nextSeq())
							itemDone, _ = sjson.SetBytes(itemDone, "output_index", msgOutputIndex)
							itemDone, _ = sjson.SetBytes(itemDone, "item.id", fmt.Sprintf("msg_%s_%d", st.ResponseID, i))
							itemDone, _ = sjson.SetRawBytes(itemDone, "item.content", contentArr)
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
					keys := make([]string, 0, len(st.FuncCallIDs))
					for key := range st.FuncCallIDs {
						keys = append(keys, key)
					}
					sort.Slice(keys, func(i, j int) bool {
						left := st.FuncOutputIx[keys[i]]
						right := st.FuncOutputIx[keys[j]]
						return left < right || (left == right && keys[i] < keys[j])
					})
					for _, key := range keys {
						callID := st.FuncCallIDs[key]
						if callID == "" || st.FuncItemDone[key] {
							continue
						}
						outputIndex := st.FuncOutputIx[key]
						args := "{}"
						if b := st.FuncArgsBuf[key]; b != nil && b.Len() > 0 {
							args = b.String()
						}
						fcDone := []byte(`{"type":"response.function_call_arguments.done","sequence_number":0,"item_id":"","output_index":0,"arguments":""}`)
						fcDone, _ = sjson.SetBytes(fcDone, "sequence_number", nextSeq())
						fcDone, _ = sjson.SetBytes(fcDone, "item_id", fmt.Sprintf("fc_%s", callID))
						fcDone, _ = sjson.SetBytes(fcDone, "output_index", outputIndex)
						fcDone, _ = sjson.SetBytes(fcDone, "arguments", args)
						out = append(out, emitRespEvent("response.function_call_arguments.done", fcDone))

						itemDone := []byte(`{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}}`)
						itemDone, _ = sjson.SetBytes(itemDone, "sequence_number", nextSeq())
						itemDone, _ = sjson.SetBytes(itemDone, "output_index", outputIndex)
						itemDone, _ = sjson.SetBytes(itemDone, "item.id", fmt.Sprintf("fc_%s", callID))
						itemDone, _ = sjson.SetBytes(itemDone, "item.arguments", args)
						itemDone, _ = sjson.SetBytes(itemDone, "item.call_id", callID)
						itemDone, _ = sjson.SetBytes(itemDone, "item.name", st.FuncNames[key])
						out = append(out, emitRespEvent("response.output_item.done", itemDone))
						st.FuncItemDone[key] = true
						st.FuncArgsDone[key] = true
					}
				}
				st.CompletionPending = true
			}

			return true
		})
	}

	return out
}

// ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream builds a single Responses JSON
// from a non-streaming OpenAI Chat Completions response.
func ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	root := gjson.ParseBytes(rawJSON)

	// Basic response scaffold
	resp := []byte(`{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"incomplete_details":null}`)

	// id: use provider id if present, otherwise synthesize
	id := root.Get("id").String()
	if id == "" {
		id = fmt.Sprintf("resp_%x_%d", time.Now().UnixNano(), atomic.AddUint64(&responseIDCounter, 1))
	}
	resp, _ = sjson.SetBytes(resp, "id", id)

	// created_at: map from chat.completion created
	created := root.Get("created").Int()
	if created == 0 {
		created = time.Now().Unix()
	}
	resp, _ = sjson.SetBytes(resp, "created_at", created)

	// Echo request fields when available (aligns with streaming path behavior)
	if len(requestRawJSON) > 0 {
		req := gjson.ParseBytes(requestRawJSON)
		if v := req.Get("instructions"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "instructions", v.String())
		}
		if v := req.Get("max_output_tokens"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "max_output_tokens", v.Int())
		} else {
			// Also support max_tokens from chat completion style
			if v = req.Get("max_tokens"); v.Exists() {
				resp, _ = sjson.SetBytes(resp, "max_output_tokens", v.Int())
			}
		}
		if v := req.Get("max_tool_calls"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "max_tool_calls", v.Int())
		}
		if v := req.Get("model"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "model", v.String())
		} else if v = root.Get("model"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "model", v.String())
		}
		if v := req.Get("parallel_tool_calls"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "parallel_tool_calls", v.Bool())
		}
		if v := req.Get("previous_response_id"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "previous_response_id", v.String())
		}
		if v := req.Get("prompt_cache_key"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "prompt_cache_key", v.String())
		}
		if v := req.Get("reasoning"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "reasoning", v.Value())
		}
		if v := req.Get("safety_identifier"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "safety_identifier", v.String())
		}
		if v := req.Get("service_tier"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "service_tier", v.String())
		}
		if v := req.Get("store"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "store", v.Bool())
		}
		if v := req.Get("temperature"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "temperature", v.Float())
		}
		if v := req.Get("text"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "text", v.Value())
		}
		if v := req.Get("tool_choice"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "tool_choice", v.Value())
		}
		if v := req.Get("tools"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "tools", v.Value())
		}
		if v := req.Get("top_logprobs"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "top_logprobs", v.Int())
		}
		if v := req.Get("top_p"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "top_p", v.Float())
		}
		if v := req.Get("truncation"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "truncation", v.String())
		}
		if v := req.Get("user"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "user", v.Value())
		}
		if v := req.Get("metadata"); v.Exists() {
			resp, _ = sjson.SetBytes(resp, "metadata", v.Value())
		}
	} else if v := root.Get("model"); v.Exists() {
		// Fallback model from response
		resp, _ = sjson.SetBytes(resp, "model", v.String())
	}

	// Build output list from choices[...]
	outputsWrapper := []byte(`{"arr":[]}`)

	// Detect reasoning_content from upstream Chat Completions response
	rcText := gjson.GetBytes(rawJSON, "choices.0.message.reasoning_content").String()
	includeReasoning := rcText != ""
	if !includeReasoning && len(requestRawJSON) > 0 {
		includeReasoning = gjson.GetBytes(requestRawJSON, "reasoning").Exists()
	}
	if includeReasoning {
		rid := id
		if strings.HasPrefix(rid, "resp_") {
			rid = strings.TrimPrefix(rid, "resp_")
		}
		reasoningItem := []byte(`{"id":"","type":"reasoning","encrypted_content":"","summary":[]}`)
		reasoningItem, _ = sjson.SetBytes(reasoningItem, "id", fmt.Sprintf("rs_%s", rid))
		if rcText != "" {
			reasoningItem, _ = sjson.SetBytes(reasoningItem, "summary.0.type", "summary_text")
			reasoningItem, _ = sjson.SetBytes(reasoningItem, "summary.0.text", rcText)
		}
		outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, "arr.-1", reasoningItem)
	}

	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() {
		choices.ForEach(func(_, choice gjson.Result) bool {
			msg := choice.Get("message")
			if msg.Exists() {
				// Text message part
				if c := msg.Get("content"); c.Exists() && c.String() != "" {
					item := []byte(`{"id":"","type":"message","status":"completed","content":[],"role":"assistant"}`)
					item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("msg_%s_%d", id, int(choice.Get("index").Int())))
					// Insert reasoning_text content part at index 0 if present
					if rcText != "" {
						rp := []byte(`{"type":"reasoning_text","text":""}`)
						rp, _ = sjson.SetBytes(rp, "text", rcText)
						item, _ = sjson.SetRawBytes(item, "content.-1", rp)
					}
					op := []byte(`{"type":"output_text","annotations":[],"logprobs":[],"text":""}`)
					op, _ = sjson.SetBytes(op, "text", c.String())
					item, _ = sjson.SetRawBytes(item, "content.-1", op)
					outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, "arr.-1", item)
				}

				// Function/tool calls
				if tcs := msg.Get("tool_calls"); tcs.Exists() && tcs.IsArray() {
					tcs.ForEach(func(_, tc gjson.Result) bool {
						callID := tc.Get("id").String()
						name := tc.Get("function.name").String()
						args := tc.Get("function.arguments").String()
						item := []byte(`{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`)
						item, _ = sjson.SetBytes(item, "id", fmt.Sprintf("fc_%s", callID))
						item, _ = sjson.SetBytes(item, "arguments", args)
						item, _ = sjson.SetBytes(item, "call_id", callID)
						item, _ = sjson.SetBytes(item, "name", name)
						outputsWrapper, _ = sjson.SetRawBytes(outputsWrapper, "arr.-1", item)
						return true
					})
				}
			}
			return true
		})
	}
	if gjson.GetBytes(outputsWrapper, "arr.#").Int() > 0 {
		resp, _ = sjson.SetRawBytes(resp, "output", []byte(gjson.GetBytes(outputsWrapper, "arr").Raw))
	}

	// usage mapping
	if usage := root.Get("usage"); usage.Exists() {
		// Map common tokens
		if usage.Get("prompt_tokens").Exists() || usage.Get("completion_tokens").Exists() || usage.Get("total_tokens").Exists() {
			resp, _ = sjson.SetBytes(resp, "usage.input_tokens", usage.Get("prompt_tokens").Int())
			if d := usage.Get("prompt_tokens_details.cached_tokens"); d.Exists() {
				resp, _ = sjson.SetBytes(resp, "usage.input_tokens_details.cached_tokens", d.Int())
			}
			resp, _ = sjson.SetBytes(resp, "usage.output_tokens", usage.Get("completion_tokens").Int())
			// Reasoning tokens not available in Chat Completions; set only if present under output_tokens_details
			if d := usage.Get("output_tokens_details.reasoning_tokens"); d.Exists() {
				resp, _ = sjson.SetBytes(resp, "usage.output_tokens_details.reasoning_tokens", d.Int())
			}
			resp, _ = sjson.SetBytes(resp, "usage.total_tokens", usage.Get("total_tokens").Int())
		} else {
			// Fallback to raw usage object if structure differs
			resp, _ = sjson.SetBytes(resp, "usage", usage.Value())
		}
	}

	return resp
}
