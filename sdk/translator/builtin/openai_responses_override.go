package builtin

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	. "github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	openairesponses "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/openai/openai/responses"
	coretranslator "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type openAIResponsesOverrideReasoning struct {
	ID   string
	Data string
}

type openAIResponsesOverrideFuncCall struct {
	ChoiceIndex   int
	ToolCallIndex int
	OutputIndex   int
	Name          string
	CallID        string
	HasRealCallID bool
	ArgsBuf       strings.Builder
	ArgsStreamed  int
	ItemAdded     bool
	ArgsDone      bool
	ItemDone      bool
}

type openAIResponsesOverrideState struct {
	Seq              int
	ResponseID       string
	Created          int64
	Started          bool
	ReasoningID      string
	ReasoningIndex   int
	MsgTextBuf       map[int]*strings.Builder
	ReasoningBuf     strings.Builder
	Reasonings       []openAIResponsesOverrideReasoning
	FuncCalls        map[string]*openAIResponsesOverrideFuncCall
	NextFuncIndex    int
	MsgItemAdded     map[int]bool
	MsgContentAdded  map[int]bool
	MsgItemDone      map[int]bool
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	ReasoningTokens  int64
	UsageSeen        bool
}

func init() {
	coretranslator.Register(
		OpenaiResponse,
		OpenAI,
		openairesponses.ConvertOpenAIResponsesRequestToOpenAIChatCompletions,
		interfaces.TranslateResponse{
			Stream:    convertOpenAIChatCompletionsResponseToOpenAIResponsesOverride,
			NonStream: openairesponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream,
		},
	)
}

func openAIResponsesOverrideEvent(event string, payload string) string {
	return fmt.Sprintf("event: %s\ndata: %s", event, payload)
}

func openAIResponsesOverrideFuncKey(choiceIndex, toolCallIndex int) string {
	return fmt.Sprintf("%d:%d", choiceIndex, toolCallIndex)
}

func convertOpenAIChatCompletionsResponseToOpenAIResponsesOverride(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	_ = ctx
	_ = modelName

	if *param == nil {
		*param = &openAIResponsesOverrideState{
			MsgTextBuf:      make(map[int]*strings.Builder),
			FuncCalls:       make(map[string]*openAIResponsesOverrideFuncCall),
			MsgItemAdded:    make(map[int]bool),
			MsgContentAdded: make(map[int]bool),
			MsgItemDone:     make(map[int]bool),
			Reasonings:      make([]openAIResponsesOverrideReasoning, 0),
		}
	}
	st := (*param).(*openAIResponsesOverrideState)

	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
	}

	rawJSON = bytes.TrimSpace(rawJSON)
	if len(rawJSON) == 0 || bytes.Equal(rawJSON, []byte("[DONE]")) {
		return []string{}
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
		st.MsgTextBuf = make(map[int]*strings.Builder)
		st.ReasoningBuf.Reset()
		st.ReasoningID = ""
		st.ReasoningIndex = 0
		st.FuncCalls = make(map[string]*openAIResponsesOverrideFuncCall)
		st.NextFuncIndex = 0
		st.MsgItemAdded = make(map[int]bool)
		st.MsgContentAdded = make(map[int]bool)
		st.MsgItemDone = make(map[int]bool)
		st.Reasonings = st.Reasonings[:0]
		st.PromptTokens = 0
		st.CachedTokens = 0
		st.CompletionTokens = 0
		st.TotalTokens = 0
		st.ReasoningTokens = 0
		st.UsageSeen = false

		created := `{"type":"response.created","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null,"output":[]}}`
		created, _ = sjson.Set(created, "sequence_number", nextSeq())
		created, _ = sjson.Set(created, "response.id", st.ResponseID)
		created, _ = sjson.Set(created, "response.created_at", st.Created)
		out = append(out, openAIResponsesOverrideEvent("response.created", created))

		inprog := `{"type":"response.in_progress","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress"}}`
		inprog, _ = sjson.Set(inprog, "sequence_number", nextSeq())
		inprog, _ = sjson.Set(inprog, "response.id", st.ResponseID)
		inprog, _ = sjson.Set(inprog, "response.created_at", st.Created)
		out = append(out, openAIResponsesOverrideEvent("response.in_progress", inprog))
		st.Started = true
	}

	stopReasoning := func(text string) {
		textDone := `{"type":"response.reasoning_summary_text.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"text":""}`
		textDone, _ = sjson.Set(textDone, "sequence_number", nextSeq())
		textDone, _ = sjson.Set(textDone, "item_id", st.ReasoningID)
		textDone, _ = sjson.Set(textDone, "output_index", st.ReasoningIndex)
		textDone, _ = sjson.Set(textDone, "text", text)
		out = append(out, openAIResponsesOverrideEvent("response.reasoning_summary_text.done", textDone))

		partDone := `{"type":"response.reasoning_summary_part.done","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`
		partDone, _ = sjson.Set(partDone, "sequence_number", nextSeq())
		partDone, _ = sjson.Set(partDone, "item_id", st.ReasoningID)
		partDone, _ = sjson.Set(partDone, "output_index", st.ReasoningIndex)
		partDone, _ = sjson.Set(partDone, "part.text", text)
		out = append(out, openAIResponsesOverrideEvent("response.reasoning_summary_part.done", partDone))

		itemDone := `{"type":"response.output_item.done","item":{"id":"","type":"reasoning","encrypted_content":"","summary":[{"type":"summary_text","text":""}]},"output_index":0,"sequence_number":0}`
		itemDone, _ = sjson.Set(itemDone, "sequence_number", nextSeq())
		itemDone, _ = sjson.Set(itemDone, "item.id", st.ReasoningID)
		itemDone, _ = sjson.Set(itemDone, "output_index", st.ReasoningIndex)
		itemDone, _ = sjson.Set(itemDone, "item.summary.text", text)
		out = append(out, openAIResponsesOverrideEvent("response.output_item.done", itemDone))

		st.Reasonings = append(st.Reasonings, openAIResponsesOverrideReasoning{ID: st.ReasoningID, Data: text})
		st.ReasoningID = ""
	}

	getFuncCall := func(choiceIndex, toolCallIndex int) *openAIResponsesOverrideFuncCall {
		key := openAIResponsesOverrideFuncKey(choiceIndex, toolCallIndex)
		if fc, ok := st.FuncCalls[key]; ok {
			return fc
		}
		fc := &openAIResponsesOverrideFuncCall{
			ChoiceIndex:   choiceIndex,
			ToolCallIndex: toolCallIndex,
			OutputIndex:   st.NextFuncIndex,
		}
		st.NextFuncIndex++
		st.FuncCalls[key] = fc
		return fc
	}

	ensureFuncItemAdded := func(fc *openAIResponsesOverrideFuncCall, allowSynthetic bool) {
		if fc.ItemAdded {
			return
		}
		if fc.CallID == "" && allowSynthetic {
			fc.CallID = fmt.Sprintf("call_%s_%d_%d", st.ResponseID, fc.ChoiceIndex, fc.ToolCallIndex)
		}
		if fc.CallID == "" {
			return
		}
		o := `{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"in_progress","arguments":"","call_id":"","name":""}}`
		o, _ = sjson.Set(o, "sequence_number", nextSeq())
		o, _ = sjson.Set(o, "output_index", fc.OutputIndex)
		o, _ = sjson.Set(o, "item.id", fmt.Sprintf("fc_%s", fc.CallID))
		o, _ = sjson.Set(o, "item.call_id", fc.CallID)
		o, _ = sjson.Set(o, "item.name", fc.Name)
		out = append(out, openAIResponsesOverrideEvent("response.output_item.added", o))
		fc.ItemAdded = true
	}

	emitPendingFuncArgs := func(fc *openAIResponsesOverrideFuncCall) {
		if !fc.ItemAdded {
			return
		}
		args := fc.ArgsBuf.String()
		if fc.ArgsStreamed >= len(args) {
			return
		}
		delta := args[fc.ArgsStreamed:]
		ad := `{"type":"response.function_call_arguments.delta","sequence_number":0,"item_id":"","output_index":0,"delta":""}`
		ad, _ = sjson.Set(ad, "sequence_number", nextSeq())
		ad, _ = sjson.Set(ad, "item_id", fmt.Sprintf("fc_%s", fc.CallID))
		ad, _ = sjson.Set(ad, "output_index", fc.OutputIndex)
		ad, _ = sjson.Set(ad, "delta", delta)
		out = append(out, openAIResponsesOverrideEvent("response.function_call_arguments.delta", ad))
		fc.ArgsStreamed = len(args)
	}

	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() {
		choices.ForEach(func(_, choice gjson.Result) bool {
			idx := int(choice.Get("index").Int())
			delta := choice.Get("delta")
			if delta.Exists() {
				if c := delta.Get("content"); c.Exists() && c.String() != "" {
					if st.ReasoningID != "" {
						stopReasoning(st.ReasoningBuf.String())
						st.ReasoningBuf.Reset()
					}
					if !st.MsgItemAdded[idx] {
						item := `{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"in_progress","content":[],"role":"assistant"}}`
						item, _ = sjson.Set(item, "sequence_number", nextSeq())
						item, _ = sjson.Set(item, "output_index", idx)
						item, _ = sjson.Set(item, "item.id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
						out = append(out, openAIResponsesOverrideEvent("response.output_item.added", item))
						st.MsgItemAdded[idx] = true
					}
					if !st.MsgContentAdded[idx] {
						part := `{"type":"response.content_part.added","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`
						part, _ = sjson.Set(part, "sequence_number", nextSeq())
						part, _ = sjson.Set(part, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
						part, _ = sjson.Set(part, "output_index", idx)
						part, _ = sjson.Set(part, "content_index", 0)
						out = append(out, openAIResponsesOverrideEvent("response.content_part.added", part))
						st.MsgContentAdded[idx] = true
					}

					msg := `{"type":"response.output_text.delta","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"delta":"","logprobs":[]}`
					msg, _ = sjson.Set(msg, "sequence_number", nextSeq())
					msg, _ = sjson.Set(msg, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
					msg, _ = sjson.Set(msg, "output_index", idx)
					msg, _ = sjson.Set(msg, "content_index", 0)
					msg, _ = sjson.Set(msg, "delta", c.String())
					out = append(out, openAIResponsesOverrideEvent("response.output_text.delta", msg))
					if st.MsgTextBuf[idx] == nil {
						st.MsgTextBuf[idx] = &strings.Builder{}
					}
					st.MsgTextBuf[idx].WriteString(c.String())
				}

				if rc := delta.Get("reasoning_content"); rc.Exists() && rc.String() != "" {
					if st.ReasoningID == "" {
						st.ReasoningID = fmt.Sprintf("rs_%s_%d", st.ResponseID, idx)
						st.ReasoningIndex = idx
						item := `{"type":"response.output_item.added","sequence_number":0,"output_index":0,"item":{"id":"","type":"reasoning","status":"in_progress","summary":[]}}`
						item, _ = sjson.Set(item, "sequence_number", nextSeq())
						item, _ = sjson.Set(item, "output_index", idx)
						item, _ = sjson.Set(item, "item.id", st.ReasoningID)
						out = append(out, openAIResponsesOverrideEvent("response.output_item.added", item))

						part := `{"type":"response.reasoning_summary_part.added","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`
						part, _ = sjson.Set(part, "sequence_number", nextSeq())
						part, _ = sjson.Set(part, "item_id", st.ReasoningID)
						part, _ = sjson.Set(part, "output_index", st.ReasoningIndex)
						out = append(out, openAIResponsesOverrideEvent("response.reasoning_summary_part.added", part))
					}
					st.ReasoningBuf.WriteString(rc.String())
					msg := `{"type":"response.reasoning_summary_text.delta","sequence_number":0,"item_id":"","output_index":0,"summary_index":0,"delta":""}`
					msg, _ = sjson.Set(msg, "sequence_number", nextSeq())
					msg, _ = sjson.Set(msg, "item_id", st.ReasoningID)
					msg, _ = sjson.Set(msg, "output_index", st.ReasoningIndex)
					msg, _ = sjson.Set(msg, "delta", rc.String())
					out = append(out, openAIResponsesOverrideEvent("response.reasoning_summary_text.delta", msg))
				}

				if tcs := delta.Get("tool_calls"); tcs.Exists() && tcs.IsArray() {
					if st.ReasoningID != "" {
						stopReasoning(st.ReasoningBuf.String())
						st.ReasoningBuf.Reset()
					}
					if st.MsgItemAdded[idx] && !st.MsgItemDone[idx] {
						fullText := ""
						if b := st.MsgTextBuf[idx]; b != nil {
							fullText = b.String()
						}
						done := `{"type":"response.output_text.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"text":"","logprobs":[]}`
						done, _ = sjson.Set(done, "sequence_number", nextSeq())
						done, _ = sjson.Set(done, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
						done, _ = sjson.Set(done, "output_index", idx)
						done, _ = sjson.Set(done, "content_index", 0)
						done, _ = sjson.Set(done, "text", fullText)
						out = append(out, openAIResponsesOverrideEvent("response.output_text.done", done))

						partDone := `{"type":"response.content_part.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`
						partDone, _ = sjson.Set(partDone, "sequence_number", nextSeq())
						partDone, _ = sjson.Set(partDone, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
						partDone, _ = sjson.Set(partDone, "output_index", idx)
						partDone, _ = sjson.Set(partDone, "content_index", 0)
						partDone, _ = sjson.Set(partDone, "part.text", fullText)
						out = append(out, openAIResponsesOverrideEvent("response.content_part.done", partDone))

						itemDone := `{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}}`
						itemDone, _ = sjson.Set(itemDone, "sequence_number", nextSeq())
						itemDone, _ = sjson.Set(itemDone, "output_index", idx)
						itemDone, _ = sjson.Set(itemDone, "item.id", fmt.Sprintf("msg_%s_%d", st.ResponseID, idx))
						itemDone, _ = sjson.Set(itemDone, "item.content.0.text", fullText)
						out = append(out, openAIResponsesOverrideEvent("response.output_item.done", itemDone))
						st.MsgItemDone[idx] = true
					}

					tcs.ForEach(func(_, tc gjson.Result) bool {
						toolCallIndex := int(tc.Get("index").Int())
						fc := getFuncCall(idx, toolCallIndex)
						if nameChunk := tc.Get("function.name").String(); nameChunk != "" {
							fc.Name = nameChunk
						}
						if callID := tc.Get("id").String(); callID != "" {
							fc.CallID = callID
							fc.HasRealCallID = true
						}
						if args := tc.Get("function.arguments"); args.Exists() && args.String() != "" {
							fc.ArgsBuf.WriteString(args.String())
						}
						ensureFuncItemAdded(fc, false)
						emitPendingFuncArgs(fc)
						return true
					})
				}
			}

			if fr := choice.Get("finish_reason"); fr.Exists() && fr.String() != "" {
				if len(st.MsgItemAdded) > 0 {
					idxs := make([]int, 0, len(st.MsgItemAdded))
					for i := range st.MsgItemAdded {
						idxs = append(idxs, i)
					}
					sort.Ints(idxs)
					for _, i := range idxs {
						if st.MsgItemAdded[i] && !st.MsgItemDone[i] {
							fullText := ""
							if b := st.MsgTextBuf[i]; b != nil {
								fullText = b.String()
							}
							done := `{"type":"response.output_text.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"text":"","logprobs":[]}`
							done, _ = sjson.Set(done, "sequence_number", nextSeq())
							done, _ = sjson.Set(done, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, i))
							done, _ = sjson.Set(done, "output_index", i)
							done, _ = sjson.Set(done, "content_index", 0)
							done, _ = sjson.Set(done, "text", fullText)
							out = append(out, openAIResponsesOverrideEvent("response.output_text.done", done))

							partDone := `{"type":"response.content_part.done","sequence_number":0,"item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""}}`
							partDone, _ = sjson.Set(partDone, "sequence_number", nextSeq())
							partDone, _ = sjson.Set(partDone, "item_id", fmt.Sprintf("msg_%s_%d", st.ResponseID, i))
							partDone, _ = sjson.Set(partDone, "output_index", i)
							partDone, _ = sjson.Set(partDone, "content_index", 0)
							partDone, _ = sjson.Set(partDone, "part.text", fullText)
							out = append(out, openAIResponsesOverrideEvent("response.content_part.done", partDone))

							itemDone := `{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}}`
							itemDone, _ = sjson.Set(itemDone, "sequence_number", nextSeq())
							itemDone, _ = sjson.Set(itemDone, "output_index", i)
							itemDone, _ = sjson.Set(itemDone, "item.id", fmt.Sprintf("msg_%s_%d", st.ResponseID, i))
							itemDone, _ = sjson.Set(itemDone, "item.content.0.text", fullText)
							out = append(out, openAIResponsesOverrideEvent("response.output_item.done", itemDone))
							st.MsgItemDone[i] = true
						}
					}
				}

				if st.ReasoningID != "" {
					stopReasoning(st.ReasoningBuf.String())
					st.ReasoningBuf.Reset()
				}

				if len(st.FuncCalls) > 0 {
					funcCalls := make([]*openAIResponsesOverrideFuncCall, 0, len(st.FuncCalls))
					for _, fc := range st.FuncCalls {
						funcCalls = append(funcCalls, fc)
					}
					sort.Slice(funcCalls, func(i, j int) bool {
						return funcCalls[i].OutputIndex < funcCalls[j].OutputIndex
					})
					for _, fc := range funcCalls {
						if fc.ItemDone {
							continue
						}
						ensureFuncItemAdded(fc, true)
						emitPendingFuncArgs(fc)
						args := fc.ArgsBuf.String()
						if args == "" {
							args = "{}"
						}
						fcDone := `{"type":"response.function_call_arguments.done","sequence_number":0,"item_id":"","output_index":0,"arguments":""}`
						fcDone, _ = sjson.Set(fcDone, "sequence_number", nextSeq())
						fcDone, _ = sjson.Set(fcDone, "item_id", fmt.Sprintf("fc_%s", fc.CallID))
						fcDone, _ = sjson.Set(fcDone, "output_index", fc.OutputIndex)
						fcDone, _ = sjson.Set(fcDone, "arguments", args)
						out = append(out, openAIResponsesOverrideEvent("response.function_call_arguments.done", fcDone))

						itemDone := `{"type":"response.output_item.done","sequence_number":0,"output_index":0,"item":{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}}`
						itemDone, _ = sjson.Set(itemDone, "sequence_number", nextSeq())
						itemDone, _ = sjson.Set(itemDone, "output_index", fc.OutputIndex)
						itemDone, _ = sjson.Set(itemDone, "item.id", fmt.Sprintf("fc_%s", fc.CallID))
						itemDone, _ = sjson.Set(itemDone, "item.arguments", args)
						itemDone, _ = sjson.Set(itemDone, "item.call_id", fc.CallID)
						itemDone, _ = sjson.Set(itemDone, "item.name", fc.Name)
						out = append(out, openAIResponsesOverrideEvent("response.output_item.done", itemDone))
						fc.ArgsDone = true
						fc.ItemDone = true
					}
				}

				completed := `{"type":"response.completed","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null}}`
				completed, _ = sjson.Set(completed, "sequence_number", nextSeq())
				completed, _ = sjson.Set(completed, "response.id", st.ResponseID)
				completed, _ = sjson.Set(completed, "response.created_at", st.Created)
				if requestRawJSON != nil {
					req := gjson.ParseBytes(requestRawJSON)
					for _, path := range []string{"instructions", "model", "previous_response_id", "service_tier", "truncation"} {
						if v := req.Get(path); v.Exists() {
							completed, _ = sjson.Set(completed, "response."+path, v.Value())
						}
					}
					for _, path := range []string{"max_output_tokens", "max_tool_calls", "top_logprobs"} {
						if v := req.Get(path); v.Exists() {
							completed, _ = sjson.Set(completed, "response."+path, v.Int())
						}
					}
					for _, path := range []string{"parallel_tool_calls", "store"} {
						if v := req.Get(path); v.Exists() {
							completed, _ = sjson.Set(completed, "response."+path, v.Bool())
						}
					}
					for _, path := range []string{"temperature", "top_p"} {
						if v := req.Get(path); v.Exists() {
							completed, _ = sjson.Set(completed, "response."+path, v.Float())
						}
					}
					for _, path := range []string{"reasoning", "text", "tool_choice", "tools", "user", "metadata", "prompt_cache_key", "safety_identifier"} {
						if v := req.Get(path); v.Exists() {
							completed, _ = sjson.Set(completed, "response."+path, v.Value())
						}
					}
				}

				outputsWrapper := `{"arr":[]}`
				for _, r := range st.Reasonings {
					item := `{"id":"","type":"reasoning","summary":[{"type":"summary_text","text":""}]}`
					item, _ = sjson.Set(item, "id", r.ID)
					item, _ = sjson.Set(item, "summary.0.text", r.Data)
					outputsWrapper, _ = sjson.SetRaw(outputsWrapper, "arr.-1", item)
				}
				if len(st.MsgItemAdded) > 0 {
					midxs := make([]int, 0, len(st.MsgItemAdded))
					for i := range st.MsgItemAdded {
						midxs = append(midxs, i)
					}
					sort.Ints(midxs)
					for _, i := range midxs {
						txt := ""
						if b := st.MsgTextBuf[i]; b != nil {
							txt = b.String()
						}
						item := `{"id":"","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":""}],"role":"assistant"}`
						item, _ = sjson.Set(item, "id", fmt.Sprintf("msg_%s_%d", st.ResponseID, i))
						item, _ = sjson.Set(item, "content.0.text", txt)
						outputsWrapper, _ = sjson.SetRaw(outputsWrapper, "arr.-1", item)
					}
				}
				if len(st.FuncCalls) > 0 {
					funcCalls := make([]*openAIResponsesOverrideFuncCall, 0, len(st.FuncCalls))
					for _, fc := range st.FuncCalls {
						funcCalls = append(funcCalls, fc)
					}
					sort.Slice(funcCalls, func(i, j int) bool {
						return funcCalls[i].OutputIndex < funcCalls[j].OutputIndex
					})
					for _, fc := range funcCalls {
						args := fc.ArgsBuf.String()
						if args == "" {
							args = "{}"
						}
						item := `{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`
						item, _ = sjson.Set(item, "id", fmt.Sprintf("fc_%s", fc.CallID))
						item, _ = sjson.Set(item, "arguments", args)
						item, _ = sjson.Set(item, "call_id", fc.CallID)
						item, _ = sjson.Set(item, "name", fc.Name)
						outputsWrapper, _ = sjson.SetRaw(outputsWrapper, "arr.-1", item)
					}
				}
				if gjson.Get(outputsWrapper, "arr.#").Int() > 0 {
					completed, _ = sjson.SetRaw(completed, "response.output", gjson.Get(outputsWrapper, "arr").Raw)
				}
				if st.UsageSeen {
					completed, _ = sjson.Set(completed, "response.usage.input_tokens", st.PromptTokens)
					completed, _ = sjson.Set(completed, "response.usage.input_tokens_details.cached_tokens", st.CachedTokens)
					completed, _ = sjson.Set(completed, "response.usage.output_tokens", st.CompletionTokens)
					if st.ReasoningTokens > 0 {
						completed, _ = sjson.Set(completed, "response.usage.output_tokens_details.reasoning_tokens", st.ReasoningTokens)
					}
					total := st.TotalTokens
					if total == 0 {
						total = st.PromptTokens + st.CompletionTokens
					}
					completed, _ = sjson.Set(completed, "response.usage.total_tokens", total)
				}
				out = append(out, openAIResponsesOverrideEvent("response.completed", completed))
			}

			return true
		})
	}

	return out
}
