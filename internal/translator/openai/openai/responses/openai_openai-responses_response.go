package responses

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

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
	// usage aggregation
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	ReasoningTokens  int64
	UsageSeen        bool
}

func emitRespEvent(event string, payload string) string {
	return fmt.Sprintf("event: %s\ndata: %s", event, payload)
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
	// scratch buffer (no-op)
	var outputs []interface{}

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
		// response.created
		created := `{"type":"response.created","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null}}`
		created, _ = sjson.Set(created, "sequence_number", nextSeq())
		created, _ = sjson.Set(created, "response.id", st.ResponseID)
		created, _ = sjson.Set(created, "response.created_at", st.Created)
		out = append(out, emitRespEvent("response.created", created))

		inprog := `{"type":"response.in_progress","sequence_number":0,"response":{"id":"","object":"response","created_at":0,"status":"in_progress"}}`
		inprog, _ = sjson.Set(inprog, "sequence_number", nextSeq())
		inprog, _ = sjson.Set(inprog, "response.id", st.ResponseID)
		inprog, _ = sjson.Set(inprog, "response.created_at", st.Created)
		out = append(out, emitRespEvent("response.in_progress", inprog))
		st.Started = true
	}

	// choices[].delta content / tool_calls / reasoning_content

	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() {
		choices.ForEach(func(_, choice gjson.Result) bool {
			msg := choice.Get("message")
			if msg.Exists() {
				// Function/tool calls FIRST to satisfy ordering: function_call must precede assistant message
				if tcs := msg.Get("tool_calls"); tcs.Exists() && tcs.IsArray() {
					tcs.ForEach(func(_, tc gjson.Result) bool {
						callID := tc.Get("id").String()
						name := tc.Get("function.name").String()
						args := tc.Get("function.arguments").String()
						outputs = append(outputs, map[string]interface{}{
							"id":        fmt.Sprintf("fc_%s", callID),
							"type":      "function_call",
							"status":    "completed",
							"arguments": args,
							"call_id":   callID,
							"name":      name,
						})
						return true
					})
				}

				// Then assistant message content (if any)
				if c := msg.Get("content"); c.Exists() && c.String() != "" {
					outputs = append(outputs, map[string]interface{}{
						"id":     fmt.Sprintf("msg_%s_%d", st.ResponseID, int(choice.Get("index").Int())),
						"type":   "message",
						"status": "completed",
						"content": []interface{}{map[string]interface{}{
							"type":        "output_text",
							"annotations": []interface{}{},
							"logprobs":    []interface{}{},
							"text":        c.String(),
						}},
						"role": "assistant",
					})
				}
			}
			return true
		})
	}

	return out
}


// ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream builds a single Responses JSON
// from a non-streaming OpenAI Chat Completions response.
func ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(_ context.Context, _ string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) string {
    root := gjson.ParseBytes(rawJSON)

    // Basic response scaffold
    resp := `{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"incomplete_details":null}`

    // id: use provider id if present, otherwise synthesize
    id := root.Get("id").String()
    if id == "" {
        id = fmt.Sprintf("resp_%x", time.Now().UnixNano())
    }
    resp, _ = sjson.Set(resp, "id", id)

    // created_at: map from chat.completion created
    created := root.Get("created").Int()
    if created == 0 {
        created = time.Now().Unix()
    }
    resp, _ = sjson.Set(resp, "created_at", created)

    // Echo selected request fields when available
    if len(requestRawJSON) > 0 {
        req := gjson.ParseBytes(requestRawJSON)
        if v := req.Get("model"); v.Exists() {
            resp, _ = sjson.Set(resp, "model", v.String())
        } else if v := root.Get("model"); v.Exists() {
            resp, _ = sjson.Set(resp, "model", v.String())
        }
        if v := req.Get("max_output_tokens"); v.Exists() {
            resp, _ = sjson.Set(resp, "max_output_tokens", v.Int())
        } else if v := req.Get("max_tokens"); v.Exists() {
            resp, _ = sjson.Set(resp, "max_output_tokens", v.Int())
        }
        if v := req.Get("previous_response_id"); v.Exists() {
            resp, _ = sjson.Set(resp, "previous_response_id", v.String())
        }
        if v := req.Get("tools"); v.Exists() { resp, _ = sjson.Set(resp, "tools", v.Value()) }
        if v := req.Get("tool_choice"); v.Exists() { resp, _ = sjson.Set(resp, "tool_choice", v.Value()) }
        if v := req.Get("parallel_tool_calls"); v.Exists() { resp, _ = sjson.Set(resp, "parallel_tool_calls", v.Bool()) }
    } else if v := root.Get("model"); v.Exists() {
        resp, _ = sjson.Set(resp, "model", v.String())
    }

    // Build outputs ensuring function_call precede message within a turn
    var outputs []interface{}
    // Optional reasoning item
    rcText := gjson.GetBytes(rawJSON, "choices.0.message.reasoning_content").String()
    if rcText != "" {
        rid := strings.TrimPrefix(id, "resp_")
        reasoningItem := map[string]interface{}{
            "id":                fmt.Sprintf("rs_%s", rid),
            "type":              "reasoning",
            "encrypted_content": "",
            "summary":           []interface{}{map[string]interface{}{"type": "summary_text", "text": rcText}},
        }
        outputs = append(outputs, reasoningItem)
    }

    if choices := root.Get("choices"); choices.Exists() && choices.IsArray() {
        choices.ForEach(func(_, choice gjson.Result) bool {
            msg := choice.Get("message")
            if !msg.Exists() { return true }
            // tool_calls first
            if tcs := msg.Get("tool_calls"); tcs.Exists() && tcs.IsArray() {
                tcs.ForEach(func(_, tc gjson.Result) bool {
                    callID := tc.Get("id").String()
                    name := tc.Get("function.name").String()
                    args := tc.Get("function.arguments").String()
                    outputs = append(outputs, map[string]interface{}{
                        "id":        fmt.Sprintf("fc_%s", callID),
                        "type":      "function_call",
                        "status":    "completed",
                        "arguments": args,
                        "call_id":   callID,
                        "name":      name,
                    })
                    return true
                })
            }
            // then message
            if c := msg.Get("content"); c.Exists() && c.String() != "" {
                outputs = append(outputs, map[string]interface{}{
                    "id":     fmt.Sprintf("msg_%s_%d", id, int(choice.Get("index").Int())),
                    "type":   "message",
                    "status": "completed",
                    "content": []interface{}{map[string]interface{}{
                        "type":        "output_text",
                        "annotations": []interface{}{},
                        "logprobs":    []interface{}{},
                        "text":        c.String(),
                    }},
                    "role": "assistant",
                })
            }
            return true
        })
    }
    if len(outputs) > 0 {
        resp, _ = sjson.Set(resp, "output", outputs)
    }

    // usage mapping (basic)
    if usage := root.Get("usage"); usage.Exists() {
        resp, _ = sjson.Set(resp, "usage.input_tokens", usage.Get("prompt_tokens").Int())
        if d := usage.Get("prompt_tokens_details.cached_tokens"); d.Exists() {
            resp, _ = sjson.Set(resp, "usage.input_tokens_details.cached_tokens", d.Int())
        }
        resp, _ = sjson.Set(resp, "usage.output_tokens", usage.Get("completion_tokens").Int())
        total := usage.Get("total_tokens").Int()
        if total == 0 {
            total = usage.Get("prompt_tokens").Int() + usage.Get("completion_tokens").Int()
        }
        resp, _ = sjson.Set(resp, "usage.total_tokens", total)
    }

    return resp
}
