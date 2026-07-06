package responses

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type interactionsToResponsesStreamState struct {
	FunctionCalls      map[int]*interactionsFunctionCallState
	ItemIDs            map[int]string
	ItemTypes          map[int]string
	ReasoningEncrypted map[int]string
	ReasoningSummaries map[int][]string
	TextOutputs        map[int]*strings.Builder
	Seq                int
	Done               bool
}

type interactionsFunctionCallState struct {
	ID        string
	Name      string
	Arguments strings.Builder
}

type responsesToInteractionsStreamState struct {
	ID                  string
	Created             bool
	StatusUpdated       bool
	Completed           bool
	Done                bool
	StepIndex           int
	ActiveStepIndex     int
	ActiveStepType      string
	ActiveStepOpen      bool
	SentText            map[string]bool
	UnkeyedTextDelta    bool
	FunctionCallIndexes map[string]int
	FunctionArgsSent    map[string]bool
}

func ConvertInteractionsResponseToOpenAIResponses(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	_ = ctx
	_ = originalRequestRawJSON
	_ = requestRawJSON
	if param == nil {
		var local any
		param = &local
	}
	if *param == nil {
		*param = &interactionsToResponsesStreamState{}
	}
	st := (*param).(*interactionsToResponsesStreamState)
	if st.FunctionCalls == nil {
		st.FunctionCalls = make(map[int]*interactionsFunctionCallState)
	}
	if st.ItemIDs == nil {
		st.ItemIDs = make(map[int]string)
	}
	if st.ItemTypes == nil {
		st.ItemTypes = make(map[int]string)
	}
	if st.ReasoningEncrypted == nil {
		st.ReasoningEncrypted = make(map[int]string)
	}
	if st.ReasoningSummaries == nil {
		st.ReasoningSummaries = make(map[int][]string)
	}
	if st.TextOutputs == nil {
		st.TextOutputs = make(map[int]*strings.Builder)
	}
	return convertInteractionsEventToResponses(modelName, rawJSON, st)
}

func ConvertInteractionsResponseToOpenAIResponsesNonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	_ = ctx
	_ = originalRequestRawJSON
	_ = requestRawJSON
	root := gjson.ParseBytes(rawJSON)
	out := []byte(`{"id":"","object":"response","status":"completed","model":"","output":[]}`)
	out, _ = sjson.SetBytes(out, "id", firstNonEmpty(root.Get("id").String(), root.Get("interaction.id").String()))
	out, _ = sjson.SetBytes(out, "model", responseModel(modelName, root))
	steps := root.Get("steps")
	if !steps.Exists() {
		steps = root.Get("interaction.steps")
	}
	steps.ForEach(func(_, step gjson.Result) bool {
		if item, ok := interactionsStepToResponsesOutput(step); ok {
			out, _ = sjson.SetRawBytes(out, "output.-1", item)
		}
		return true
	})
	out = setResponsesUsageFromInteractions(out, "usage", translatorcommon.InteractionsUsage(root))
	return out
}

func convertInteractionsEventToResponses(modelName string, rawJSON []byte, st *interactionsToResponsesStreamState) [][]byte {
	payload := interactionsSSEPayload(rawJSON)
	if len(payload) == 0 {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		if st.Done {
			return nil
		}
		st.Done = true
		return [][]byte{[]byte("data: [DONE]")}
	}
	root := gjson.ParseBytes(payload)
	if !root.Exists() {
		return nil
	}
	switch root.Get("event_type").String() {
	case "interaction.created":
		return [][]byte{responsesCreatedEvent(modelName, root, st)}
	case "step.start":
		return interactionsStepStartToResponses(root, st)
	case "step.delta":
		return interactionsStepDeltaToResponses(root, st)
	case "step.stop":
		return interactionsStepStopToResponses(root, st)
	case "interaction.completed", "finish":
		return [][]byte{responsesCompletedEvent(modelName, root, st)}
	case "done":
		if st.Done {
			return nil
		}
		st.Done = true
		return [][]byte{[]byte("data: [DONE]")}
	}
	return nil
}

func interactionsStepToResponsesOutput(step gjson.Result) ([]byte, bool) {
	switch step.Get("type").String() {
	case "model_output":
		item := []byte(`{"type":"message","role":"assistant","content":[]}`)
		if id := firstNonEmpty(step.Get("id").String(), step.Get("step_id").String()); id != "" {
			item, _ = sjson.SetBytes(item, "id", id)
		}
		content := step.Get("content")
		if content.Type == gjson.String {
			part := []byte(`{"type":"output_text","text":""}`)
			part, _ = sjson.SetBytes(part, "text", content.String())
			item, _ = sjson.SetRawBytes(item, "content.-1", part)
		} else {
			content.ForEach(func(_, part gjson.Result) bool {
				if converted, ok := interactionsContentPartToResponses(part, "assistant"); ok {
					item, _ = sjson.SetRawBytes(item, "content.-1", converted)
				}
				return true
			})
		}
		return item, true
	case "thought":
		item := []byte(`{"type":"reasoning","summary":[]}`)
		if signature := interactionsThoughtSignature(step); signature != "" {
			item, _ = sjson.SetBytes(item, "encrypted_content", signature)
		}
		for _, text := range interactionsContentTexts(step.Get("content")) {
			part := []byte(`{"type":"summary_text","text":""}`)
			part, _ = sjson.SetBytes(part, "text", text)
			item, _ = sjson.SetRawBytes(item, "summary.-1", part)
		}
		return item, true
	case "function_call":
		return interactionsFunctionCallToResponses(step), true
	}
	return nil, false
}

func responsesCreatedEvent(modelName string, root gjson.Result, st *interactionsToResponsesStreamState) []byte {
	payload := []byte(`{"type":"response.created","response":{"id":"","object":"response","status":"in_progress","model":""}}`)
	payload, _ = sjson.SetBytes(payload, "sequence_number", nextResponsesSeq(st))
	payload, _ = sjson.SetBytes(payload, "response.id", firstNonEmpty(root.Get("interaction.id").String(), root.Get("id").String()))
	payload, _ = sjson.SetBytes(payload, "response.model", modelName)
	return emitResponsesEvent("response.created", payload)
}

func interactionsStepStartToResponses(root gjson.Result, st *interactionsToResponsesStreamState) [][]byte {
	index := int(root.Get("index").Int())
	step := root.Get("step")
	stepType := step.Get("type").String()
	itemID := firstNonEmpty(step.Get("id").String(), step.Get("call_id").String(), fmt.Sprintf("item_%d", index))
	st.ItemIDs[index] = itemID
	st.ItemTypes[index] = stepType
	switch stepType {
	case "model_output":
		added := []byte(`{"type":"response.output_item.added","output_index":0,"item":{"id":"","type":"message","status":"in_progress","role":"assistant","content":[]}}`)
		added, _ = sjson.SetBytes(added, "sequence_number", nextResponsesSeq(st))
		added, _ = sjson.SetBytes(added, "output_index", index)
		added, _ = sjson.SetBytes(added, "item.id", itemID)
		part := []byte(`{"type":"response.content_part.added","output_index":0,"content_index":0,"item_id":"","part":{"type":"output_text","text":""}}`)
		part, _ = sjson.SetBytes(part, "sequence_number", nextResponsesSeq(st))
		part, _ = sjson.SetBytes(part, "output_index", index)
		part, _ = sjson.SetBytes(part, "item_id", itemID)
		return [][]byte{emitResponsesEvent("response.output_item.added", added), emitResponsesEvent("response.content_part.added", part)}
	case "thought":
		added := []byte(`{"type":"response.output_item.added","output_index":0,"item":{"id":"","type":"reasoning","status":"in_progress","encrypted_content":"","summary":[]}}`)
		added, _ = sjson.SetBytes(added, "sequence_number", nextResponsesSeq(st))
		added, _ = sjson.SetBytes(added, "output_index", index)
		added, _ = sjson.SetBytes(added, "item.id", itemID)
		if signature := st.ReasoningEncrypted[index]; signature != "" {
			added, _ = sjson.SetBytes(added, "item.encrypted_content", signature)
		}
		return [][]byte{emitResponsesEvent("response.output_item.added", added)}
	case "function_call":
		call := &interactionsFunctionCallState{
			ID:   itemID,
			Name: step.Get("name").String(),
		}
		if args := step.Get("arguments"); args.Exists() && strings.TrimSpace(args.Raw) != "{}" {
			call.Arguments.WriteString(jsonStringValue(args, "{}"))
		}
		st.FunctionCalls[index] = call
		added := []byte(`{"type":"response.output_item.added","output_index":0,"item":{"id":"","type":"function_call","call_id":"","name":"","arguments":""}}`)
		added, _ = sjson.SetBytes(added, "sequence_number", nextResponsesSeq(st))
		added, _ = sjson.SetBytes(added, "output_index", index)
		added, _ = sjson.SetBytes(added, "item.id", itemID)
		added, _ = sjson.SetBytes(added, "item.call_id", itemID)
		added, _ = sjson.SetBytes(added, "item.name", call.Name)
		return [][]byte{emitResponsesEvent("response.output_item.added", added)}
	}
	return nil
}

func interactionsStepDeltaToResponses(root gjson.Result, st *interactionsToResponsesStreamState) [][]byte {
	index := int(root.Get("index").Int())
	delta := root.Get("delta")
	switch delta.Get("type").String() {
	case "thought_summary":
		text := firstNonEmpty(delta.Get("content.text").String(), delta.Get("text").String())
		recordResponsesReasoningSummary(st, index, text)
		payload := []byte(`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":""}`)
		payload, _ = sjson.SetBytes(payload, "sequence_number", nextResponsesSeq(st))
		payload, _ = sjson.SetBytes(payload, "output_index", index)
		payload, _ = sjson.SetBytes(payload, "delta", text)
		return [][]byte{emitResponsesEvent("response.reasoning_summary_text.delta", payload)}
	case "thought_signature":
		if signature := delta.Get("signature").String(); signature != "" {
			st.ReasoningEncrypted[index] = signature
		}
		return nil
	case "arguments_delta":
		if call := st.FunctionCalls[index]; call != nil {
			call.Arguments.WriteString(delta.Get("arguments").String())
		}
		payload := []byte(`{"type":"response.function_call_arguments.delta","output_index":0,"delta":""}`)
		payload, _ = sjson.SetBytes(payload, "sequence_number", nextResponsesSeq(st))
		payload, _ = sjson.SetBytes(payload, "output_index", index)
		payload, _ = sjson.SetBytes(payload, "item_id", st.ItemIDs[index])
		payload, _ = sjson.SetBytes(payload, "delta", delta.Get("arguments").String())
		return [][]byte{emitResponsesEvent("response.function_call_arguments.delta", payload)}
	default:
		payload := []byte(`{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"","delta":""}`)
		payload, _ = sjson.SetBytes(payload, "sequence_number", nextResponsesSeq(st))
		payload, _ = sjson.SetBytes(payload, "output_index", index)
		payload, _ = sjson.SetBytes(payload, "item_id", st.ItemIDs[index])
		text := delta.Get("text").String()
		recordResponsesTextOutput(st, index, text)
		payload, _ = sjson.SetBytes(payload, "delta", text)
		return [][]byte{emitResponsesEvent("response.output_text.delta", payload)}
	}
}

func interactionsStepStopToResponses(root gjson.Result, st *interactionsToResponsesStreamState) [][]byte {
	index := int(root.Get("index").Int())
	itemID := st.ItemIDs[index]
	switch st.ItemTypes[index] {
	case "model_output":
		text := ""
		if builder := st.TextOutputs[index]; builder != nil {
			text = builder.String()
		}
		textDone := []byte(`{"type":"response.output_text.done","output_index":0,"content_index":0,"item_id":"","text":"","logprobs":[]}`)
		textDone, _ = sjson.SetBytes(textDone, "sequence_number", nextResponsesSeq(st))
		textDone, _ = sjson.SetBytes(textDone, "output_index", index)
		textDone, _ = sjson.SetBytes(textDone, "item_id", itemID)
		textDone, _ = sjson.SetBytes(textDone, "text", text)
		part := []byte(`{"type":"response.content_part.done","output_index":0,"content_index":0,"item_id":"","part":{"type":"output_text","text":""}}`)
		part, _ = sjson.SetBytes(part, "sequence_number", nextResponsesSeq(st))
		part, _ = sjson.SetBytes(part, "output_index", index)
		part, _ = sjson.SetBytes(part, "item_id", itemID)
		part, _ = sjson.SetBytes(part, "part.text", text)
		done := []byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"","type":"message","status":"completed","role":"assistant","content":[]}}`)
		done, _ = sjson.SetBytes(done, "sequence_number", nextResponsesSeq(st))
		done, _ = sjson.SetBytes(done, "output_index", index)
		done, _ = sjson.SetBytes(done, "item.id", itemID)
		outputText := []byte(`{"type":"output_text","text":""}`)
		outputText, _ = sjson.SetBytes(outputText, "text", text)
		done, _ = sjson.SetRawBytes(done, "item.content.-1", outputText)
		return [][]byte{emitResponsesEvent("response.output_text.done", textDone), emitResponsesEvent("response.content_part.done", part), emitResponsesEvent("response.output_item.done", done)}
	case "function_call":
		call := st.FunctionCalls[index]
		done := []byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"","type":"function_call","call_id":"","name":"","arguments":""}}`)
		done, _ = sjson.SetBytes(done, "sequence_number", nextResponsesSeq(st))
		done, _ = sjson.SetBytes(done, "output_index", index)
		done, _ = sjson.SetBytes(done, "item.id", itemID)
		done, _ = sjson.SetBytes(done, "item.call_id", itemID)
		if call != nil {
			done, _ = sjson.SetBytes(done, "item.name", call.Name)
			done, _ = sjson.SetBytes(done, "item.arguments", call.Arguments.String())
		}
		return [][]byte{emitResponsesEvent("response.output_item.done", done)}
	default:
		done := []byte(`{"type":"response.output_item.done","output_index":0,"item":{}}`)
		done, _ = sjson.SetBytes(done, "sequence_number", nextResponsesSeq(st))
		done, _ = sjson.SetBytes(done, "output_index", index)
		done, _ = sjson.SetRawBytes(done, "item", responsesReasoningItem(index, st))
		return [][]byte{emitResponsesEvent("response.output_item.done", done)}
	}
}

func responsesCompletedEvent(modelName string, root gjson.Result, st *interactionsToResponsesStreamState) []byte {
	payload := []byte(`{"type":"response.completed","response":{"id":"","object":"response","status":"completed","model":"","output":[],"usage":{}}}`)
	payload, _ = sjson.SetBytes(payload, "sequence_number", nextResponsesSeq(st))
	interaction := root.Get("interaction")
	payload, _ = sjson.SetBytes(payload, "response.id", firstNonEmpty(interaction.Get("id").String(), root.Get("id").String()))
	payload, _ = sjson.SetBytes(payload, "response.model", firstNonEmpty(interaction.Get("model").String(), modelName))
	payload = setResponsesCompletedOutput(payload, st)
	payload = setResponsesUsageFromInteractions(payload, "response.usage", translatorcommon.InteractionsUsage(root))
	return emitResponsesEvent("response.completed", payload)
}

func interactionsThoughtSignature(step gjson.Result) string {
	for _, path := range []string{
		"encrypted_content",
		"signature",
		"thought_signature",
		"thoughtSignature",
		"extra_content.google.thought_signature",
	} {
		if signature := step.Get(path).String(); signature != "" {
			return signature
		}
	}
	content := step.Get("content")
	if content.IsArray() {
		var signature string
		content.ForEach(func(_, part gjson.Result) bool {
			signature = firstNonEmpty(
				part.Get("signature").String(),
				part.Get("thought_signature").String(),
				part.Get("thoughtSignature").String(),
				part.Get("extra_content.google.thought_signature").String(),
			)
			return signature == ""
		})
		return signature
	}
	return ""
}

func recordResponsesReasoningSummary(st *interactionsToResponsesStreamState, index int, text string) {
	if text == "" {
		return
	}
	st.ReasoningSummaries[index] = append(st.ReasoningSummaries[index], text)
}

func recordResponsesTextOutput(st *interactionsToResponsesStreamState, index int, text string) {
	if text == "" {
		return
	}
	if st.TextOutputs[index] == nil {
		st.TextOutputs[index] = &strings.Builder{}
	}
	st.TextOutputs[index].WriteString(text)
}

func setResponsesCompletedOutput(payload []byte, st *interactionsToResponsesStreamState) []byte {
	maxIndex := -1
	for index := range st.ItemTypes {
		if index > maxIndex {
			maxIndex = index
		}
	}
	for index := 0; index <= maxIndex; index++ {
		itemType, ok := st.ItemTypes[index]
		if !ok {
			continue
		}
		item, ok := responsesCompletedOutputItem(index, itemType, st)
		if ok {
			payload, _ = sjson.SetRawBytes(payload, "response.output.-1", item)
		}
	}
	return payload
}

func responsesCompletedOutputItem(index int, itemType string, st *interactionsToResponsesStreamState) ([]byte, bool) {
	switch itemType {
	case "model_output":
		item := []byte(`{"id":"","type":"message","status":"completed","role":"assistant","content":[]}`)
		item, _ = sjson.SetBytes(item, "id", st.ItemIDs[index])
		if builder := st.TextOutputs[index]; builder != nil && builder.String() != "" {
			part := []byte(`{"type":"output_text","text":""}`)
			part, _ = sjson.SetBytes(part, "text", builder.String())
			item, _ = sjson.SetRawBytes(item, "content.-1", part)
		}
		return item, true
	case "thought":
		return responsesReasoningItem(index, st), true
	case "function_call":
		item := []byte(`{"id":"","type":"function_call","call_id":"","name":"","arguments":""}`)
		itemID := st.ItemIDs[index]
		item, _ = sjson.SetBytes(item, "id", itemID)
		item, _ = sjson.SetBytes(item, "call_id", itemID)
		if call := st.FunctionCalls[index]; call != nil {
			item, _ = sjson.SetBytes(item, "name", call.Name)
			item, _ = sjson.SetBytes(item, "arguments", call.Arguments.String())
		}
		return item, true
	}
	return nil, false
}

func responsesReasoningItem(index int, st *interactionsToResponsesStreamState) []byte {
	item := []byte(`{"id":"","type":"reasoning","encrypted_content":"","summary":[]}`)
	item, _ = sjson.SetBytes(item, "id", st.ItemIDs[index])
	if signature := st.ReasoningEncrypted[index]; signature != "" {
		item, _ = sjson.SetBytes(item, "encrypted_content", signature)
	}
	for _, text := range st.ReasoningSummaries[index] {
		part := []byte(`{"type":"summary_text","text":""}`)
		part, _ = sjson.SetBytes(part, "text", text)
		item, _ = sjson.SetRawBytes(item, "summary.-1", part)
	}
	return item
}

func setResponsesUsageFromInteractions(out []byte, path string, usage gjson.Result) []byte {
	if !usage.Exists() {
		return out
	}
	if v, ok := firstUsageInt(usage, "input_tokens", "total_input_tokens"); ok {
		out, _ = sjson.SetBytes(out, path+".input_tokens", v)
	}
	if v, ok := firstUsageInt(usage, "output_tokens", "total_output_tokens"); ok {
		out, _ = sjson.SetBytes(out, path+".output_tokens", v)
	}
	if v, ok := firstUsageInt(usage, "total_tokens"); ok {
		out, _ = sjson.SetBytes(out, path+".total_tokens", v)
	}
	if v, ok := firstUsageInt(usage, "cached_tokens", "total_cached_tokens"); ok {
		out, _ = sjson.SetBytes(out, path+".input_tokens_details.cached_tokens", v)
	}
	if v, ok := firstUsageInt(usage, "reasoning_tokens", "total_thought_tokens"); ok {
		out, _ = sjson.SetBytes(out, path+".output_tokens_details.reasoning_tokens", v)
	}
	return out
}

func ConvertOpenAIResponsesResponseToInteractions(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	_ = ctx
	_ = originalRequestRawJSON
	_ = requestRawJSON
	if param == nil {
		var local any
		param = &local
	}
	if *param == nil {
		*param = &responsesToInteractionsStreamState{}
	}
	st := (*param).(*responsesToInteractionsStreamState)
	if st.FunctionCallIndexes == nil {
		st.FunctionCallIndexes = make(map[string]int)
	}
	if st.FunctionArgsSent == nil {
		st.FunctionArgsSent = make(map[string]bool)
	}
	return convertOpenAIResponsesEventToInteractions(modelName, rawJSON, st)
}

func ConvertOpenAIResponsesResponseToInteractionsNonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	_ = ctx
	_ = originalRequestRawJSON
	_ = requestRawJSON
	root := gjson.ParseBytes(rawJSON)
	out := []byte(`{"id":"","object":"interaction","status":"completed","model":"","steps":[]}`)
	out, _ = sjson.SetBytes(out, "id", root.Get("id").String())
	out, _ = sjson.SetBytes(out, "model", responseModel(modelName, root))
	root.Get("output").ForEach(func(_, item gjson.Result) bool {
		if step, ok := openAIResponsesOutputItemToInteractionsStep(item); ok {
			out, _ = sjson.SetRawBytes(out, "steps.-1", step)
		}
		return true
	})
	out = setInteractionsUsageFromResponses(out, "usage", root.Get("usage"))
	return out
}

func convertOpenAIResponsesEventToInteractions(modelName string, rawJSON []byte, st *responsesToInteractionsStreamState) [][]byte {
	payload := interactionsSSEPayload(rawJSON)
	if len(payload) == 0 {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		return appendInteractionsDoneDirect(nil, st)
	}
	root := gjson.ParseBytes(payload)
	if !root.Exists() {
		return nil
	}
	switch root.Get("type").String() {
	case "response.created":
		return appendInteractionsCreatedDirect(nil, st, modelName, root.Get("response"))
	case "response.output_text.delta":
		out := ensureInteractionsStepDirect(nil, st, modelName, "model_output", gjson.Result{})
		out = appendInteractionsTextDeltaDirect(out, st, root.Get("delta").String(), false)
		st.markTextSent(textKeysFromResponsesEvent(root))
		return out
	case "response.reasoning_summary_text.delta":
		out := ensureInteractionsStepDirect(nil, st, modelName, "thought", gjson.Result{})
		return appendInteractionsTextDeltaDirect(out, st, root.Get("delta").String(), true)
	case "response.output_item.added":
		return openAIResponsesOutputItemAddedToInteractions(modelName, root, st)
	case "response.function_call_arguments.delta":
		out := ensureInteractionsFunctionCallStep(nil, st, modelName, root)
		out = appendInteractionsArgumentsDeltaDirect(out, st, root.Get("delta").String())
		st.markFunctionArgsSent(functionArgsKeysFromResponsesEvent(root))
		return out
	case "response.output_item.done":
		return openAIResponsesOutputItemDoneToInteractions(modelName, root, st)
	case "response.completed":
		return openAIResponsesCompletedToInteractions(modelName, root.Get("response"), st)
	}
	return nil
}

func openAIResponsesOutputItemToInteractionsStep(item gjson.Result) ([]byte, bool) {
	switch item.Get("type").String() {
	case "message":
		step := []byte(`{"type":"model_output","content":[]}`)
		item.Get("content").ForEach(func(_, part gjson.Result) bool {
			if converted, ok := responsesContentPartToInteractions(part); ok {
				step, _ = sjson.SetRawBytes(step, "content.-1", converted)
			}
			return true
		})
		return step, true
	case "function_call":
		return responsesFunctionCallToInteractions(item), true
	case "reasoning":
		step := []byte(`{"type":"thought","content":[]}`)
		item.Get("summary").ForEach(func(_, summary gjson.Result) bool {
			if text := summary.Get("text").String(); text != "" {
				part := []byte(`{"type":"text","text":""}`)
				part, _ = sjson.SetBytes(part, "text", text)
				step, _ = sjson.SetRawBytes(step, "content.-1", part)
			}
			return true
		})
		return step, true
	}
	return nil, false
}

func openAIResponsesOutputItemAddedToInteractions(modelName string, root gjson.Result, st *responsesToInteractionsStreamState) [][]byte {
	item := root.Get("item")
	switch item.Get("type").String() {
	case "function_call":
		out := ensureInteractionsCreatedDirect(nil, st, modelName)
		out = appendInteractionsStepStopDirect(out, st)
		step := []byte(`{"type":"function_call","name":"","arguments":{}}`)
		step, _ = sjson.SetBytes(step, "name", item.Get("name").String())
		if callID := firstNonEmpty(item.Get("call_id").String(), item.Get("id").String()); callID != "" {
			step, _ = sjson.SetBytes(step, "id", callID)
			step, _ = sjson.SetBytes(step, "call_id", callID)
			st.FunctionCallIndexes[callID] = st.StepIndex
		}
		out = appendInteractionsStepStartDirect(out, st, "function_call", gjson.ParseBytes(step))
		return out
	case "message":
		return ensureInteractionsStepDirect(nil, st, modelName, "model_output", gjson.Result{})
	case "reasoning":
		return ensureInteractionsStepDirect(nil, st, modelName, "thought", gjson.Result{})
	}
	return nil
}

func openAIResponsesOutputItemDoneToInteractions(modelName string, root gjson.Result, st *responsesToInteractionsStreamState) [][]byte {
	item := root.Get("item")
	switch item.Get("type").String() {
	case "function_call":
		out := ensureInteractionsFunctionCallStep(nil, st, modelName, root)
		if args := item.Get("arguments"); args.Exists() && args.String() != "" && !st.hasSentFunctionArgs(functionArgsKeysFromResponsesEvent(root)) {
			out = appendInteractionsArgumentsDeltaDirect(out, st, jsonStringValue(args, "{}"))
		}
		return appendInteractionsStepStopDirect(out, st)
	case "reasoning":
		out := ensureInteractionsStepDirect(nil, st, modelName, "thought", gjson.Result{})
		item.Get("summary").ForEach(func(_, summary gjson.Result) bool {
			if text := summary.Get("text").String(); text != "" {
				out = appendInteractionsTextDeltaDirect(out, st, text, true)
			}
			return true
		})
		return appendInteractionsStepStopDirect(out, st)
	case "message":
		return appendResponsesMessageFallbackToInteractions(nil, modelName, item, root, st, true)
	}
	return nil
}

func openAIResponsesCompletedToInteractions(modelName string, response gjson.Result, st *responsesToInteractionsStreamState) [][]byte {
	var out [][]byte
	response.Get("output").ForEach(func(outputIndex, item gjson.Result) bool {
		if item.Get("type").String() == "message" {
			out = appendResponsesMessageFallbackToInteractions(out, modelName, item, responseOutputIndexRoot(item, outputIndex), st, false)
		}
		return true
	})
	out = appendInteractionsStepStopDirect(out, st)
	out = appendInteractionsCompletedDirect(out, st, modelName, response)
	return appendInteractionsDoneDirect(out, st)
}

func appendResponsesMessageFallbackToInteractions(out [][]byte, modelName string, item, root gjson.Result, st *responsesToInteractionsStreamState, stop bool) [][]byte {
	itemID := item.Get("id").String()
	outputIndex := int(root.Get("output_index").Int())
	hasOutputIndex := root.Get("output_index").Exists()
	item.Get("content").ForEach(func(contentIndex, part gjson.Result) bool {
		if part.Get("type").String() != "output_text" && part.Get("type").String() != "text" {
			return true
		}
		hasContentIndex := contentIndex.Exists()
		keys := openAIResponsesTextKeys(itemID, outputIndex, hasOutputIndex, int(contentIndex.Int()), hasContentIndex)
		unkeyedKeys := openAIResponsesUnkeyedTextKeys(itemID, outputIndex, hasOutputIndex)
		if st.hasSentText(keys, hasContentIndex) || st.hasSentUnkeyedText(unkeyedKeys) {
			return true
		}
		text := part.Get("text").String()
		if text == "" {
			return true
		}
		out = ensureInteractionsStepDirect(out, st, modelName, "model_output", gjson.Result{})
		out = appendInteractionsTextDeltaDirect(out, st, text, false)
		st.markTextSent(keys)
		return true
	})
	if stop {
		return appendInteractionsStepStopDirect(out, st)
	}
	return out
}

func responseOutputIndexRoot(item, outputIndex gjson.Result) gjson.Result {
	raw := []byte(`{"output_index":0}`)
	raw, _ = sjson.SetBytes(raw, "output_index", outputIndex.Int())
	if id := item.Get("id").String(); id != "" {
		raw, _ = sjson.SetBytes(raw, "item_id", id)
	}
	return gjson.ParseBytes(raw)
}

func appendInteractionsCreatedDirect(out [][]byte, st *responsesToInteractionsStreamState, modelName string, response gjson.Result, markStatus ...bool) [][]byte {
	if st.Created {
		return out
	}
	st.ID = firstNonEmpty(response.Get("id").String(), st.ID, fmt.Sprintf("interaction_%d", time.Now().UnixNano()))
	created := []byte(`{"interaction":{"id":"","status":"in_progress","object":"interaction","model":""},"event_type":"interaction.created"}`)
	created, _ = sjson.SetBytes(created, "interaction.id", st.ID)
	created, _ = sjson.SetBytes(created, "interaction.model", responseModel(modelName, response))
	out = append(out, emitInteractionsEvent("interaction.created", created))
	st.Created = true
	if len(markStatus) == 0 || markStatus[0] {
		out = appendInteractionsStatusUpdateDirect(out, st)
	}
	return out
}

func appendInteractionsStatusUpdateDirect(out [][]byte, st *responsesToInteractionsStreamState) [][]byte {
	if st.StatusUpdated {
		return out
	}
	statusUpdate := []byte(`{"interaction_id":"","status":"in_progress","event_type":"interaction.status_update"}`)
	statusUpdate, _ = sjson.SetBytes(statusUpdate, "interaction_id", st.ID)
	out = append(out, emitInteractionsEvent("interaction.status_update", statusUpdate))
	st.StatusUpdated = true
	return out
}

func ensureInteractionsStepDirect(out [][]byte, st *responsesToInteractionsStreamState, modelName, stepType string, step gjson.Result) [][]byte {
	out = ensureInteractionsCreatedDirect(out, st, modelName)
	if st.ActiveStepOpen && st.ActiveStepType == stepType {
		return out
	}
	out = appendInteractionsStepStopDirect(out, st)
	return appendInteractionsStepStartDirect(out, st, stepType, step)
}

func ensureInteractionsCreatedDirect(out [][]byte, st *responsesToInteractionsStreamState, modelName string) [][]byte {
	return appendInteractionsCreatedDirect(out, st, modelName, gjson.Result{})
}

func appendInteractionsStepStartDirect(out [][]byte, st *responsesToInteractionsStreamState, stepType string, step gjson.Result) [][]byte {
	index := st.StepIndex
	st.StepIndex++
	st.ActiveStepIndex = index
	st.ActiveStepType = stepType
	st.ActiveStepOpen = true
	payload := []byte(`{"index":0,"step":{"type":""},"event_type":"step.start"}`)
	payload, _ = sjson.SetBytes(payload, "index", index)
	payload, _ = sjson.SetBytes(payload, "step.type", stepType)
	if stepType == "function_call" {
		if id := firstNonEmpty(step.Get("call_id").String(), step.Get("id").String()); id != "" {
			payload, _ = sjson.SetBytes(payload, "step.id", id)
			payload, _ = sjson.SetBytes(payload, "step.call_id", id)
		}
		payload, _ = sjson.SetBytes(payload, "step.name", step.Get("name").String())
		payload, _ = sjson.SetRawBytes(payload, "step.arguments", []byte(`{}`))
	}
	return append(out, emitInteractionsEvent("step.start", payload))
}

func appendInteractionsTextDeltaDirect(out [][]byte, st *responsesToInteractionsStreamState, text string, thought bool) [][]byte {
	if thought {
		payload := []byte(`{"index":0,"delta":{"content":{"text":"","type":"text"},"type":"thought_summary"},"event_type":"step.delta"}`)
		payload, _ = sjson.SetBytes(payload, "index", st.ActiveStepIndex)
		payload, _ = sjson.SetBytes(payload, "delta.content.text", text)
		return append(out, emitInteractionsEvent("step.delta", payload))
	}
	payload := []byte(`{"index":0,"delta":{"text":"","type":"text"},"event_type":"step.delta"}`)
	payload, _ = sjson.SetBytes(payload, "index", st.ActiveStepIndex)
	payload, _ = sjson.SetBytes(payload, "delta.text", text)
	return append(out, emitInteractionsEvent("step.delta", payload))
}

func appendInteractionsArgumentsDeltaDirect(out [][]byte, st *responsesToInteractionsStreamState, arguments string) [][]byte {
	payload := []byte(`{"index":0,"delta":{"arguments":"","type":"arguments_delta"},"event_type":"step.delta"}`)
	payload, _ = sjson.SetBytes(payload, "index", st.ActiveStepIndex)
	payload, _ = sjson.SetBytes(payload, "delta.arguments", arguments)
	return append(out, emitInteractionsEvent("step.delta", payload))
}

func appendInteractionsStepStopDirect(out [][]byte, st *responsesToInteractionsStreamState) [][]byte {
	if !st.ActiveStepOpen {
		return out
	}
	payload := []byte(`{"index":0,"event_type":"step.stop"}`)
	payload, _ = sjson.SetBytes(payload, "index", st.ActiveStepIndex)
	out = append(out, emitInteractionsEvent("step.stop", payload))
	st.ActiveStepOpen = false
	st.ActiveStepType = ""
	return out
}

func appendInteractionsCompletedDirect(out [][]byte, st *responsesToInteractionsStreamState, modelName string, response gjson.Result) [][]byte {
	if st.Completed {
		return out
	}
	now := time.Now().UTC().Format(time.RFC3339)
	payload := []byte(`{"interaction":{"id":"","status":"completed","usage":{},"created":"","updated":"","service_tier":"standard","object":"interaction","model":""},"event_type":"interaction.completed"}`)
	payload, _ = sjson.SetBytes(payload, "interaction.id", st.ID)
	payload, _ = sjson.SetBytes(payload, "interaction.created", now)
	payload, _ = sjson.SetBytes(payload, "interaction.updated", now)
	payload, _ = sjson.SetBytes(payload, "interaction.model", responseModel(modelName, response))
	payload = setInteractionsUsageFromResponses(payload, "interaction.usage", response.Get("usage"))
	out = append(out, emitInteractionsEvent("interaction.completed", payload))
	st.Completed = true
	return out
}

func appendInteractionsDoneDirect(out [][]byte, st *responsesToInteractionsStreamState) [][]byte {
	if st.Done {
		return out
	}
	out = append(out, emitInteractionsEvent("done", []byte("[DONE]")))
	st.Done = true
	return out
}

func ensureInteractionsFunctionCallStep(out [][]byte, st *responsesToInteractionsStreamState, modelName string, root gjson.Result) [][]byte {
	if st.ActiveStepOpen && st.ActiveStepType == "function_call" {
		return out
	}
	item := root.Get("item")
	if !item.Exists() {
		item = root
	}
	step := []byte(`{"type":"function_call","name":"","arguments":{}}`)
	step, _ = sjson.SetBytes(step, "name", item.Get("name").String())
	if callID := firstNonEmpty(item.Get("call_id").String(), item.Get("id").String(), root.Get("call_id").String(), root.Get("item_id").String()); callID != "" {
		step, _ = sjson.SetBytes(step, "id", callID)
		step, _ = sjson.SetBytes(step, "call_id", callID)
	}
	out = ensureInteractionsCreatedDirect(out, st, modelName)
	out = appendInteractionsStepStopDirect(out, st)
	return appendInteractionsStepStartDirect(out, st, "function_call", gjson.ParseBytes(step))
}

func setInteractionsUsageFromResponses(out []byte, path string, usage gjson.Result) []byte {
	if !usage.Exists() {
		return out
	}
	if v := usage.Get("input_tokens"); v.Exists() {
		out, _ = sjson.SetBytes(out, path+".input_tokens", v.Int())
		out, _ = sjson.SetBytes(out, path+".total_input_tokens", v.Int())
	}
	if v := usage.Get("output_tokens"); v.Exists() {
		out, _ = sjson.SetBytes(out, path+".output_tokens", v.Int())
		out, _ = sjson.SetBytes(out, path+".total_output_tokens", v.Int())
	}
	if v := usage.Get("total_tokens"); v.Exists() {
		out, _ = sjson.SetBytes(out, path+".total_tokens", v.Int())
	}
	if v := usage.Get("input_tokens_details.cached_tokens"); v.Exists() {
		out, _ = sjson.SetBytes(out, path+".cached_tokens", v.Int())
		out, _ = sjson.SetBytes(out, path+".total_cached_tokens", v.Int())
	}
	if v := usage.Get("output_tokens_details.reasoning_tokens"); v.Exists() {
		out, _ = sjson.SetBytes(out, path+".reasoning_tokens", v.Int())
		out, _ = sjson.SetBytes(out, path+".total_thought_tokens", v.Int())
	}
	return out
}

func interactionsSSEPayload(rawJSON []byte) []byte {
	trimmed := bytes.TrimSpace(rawJSON)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) {
		return trimmed
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		return bytes.TrimSpace(trimmed[len("data:"):])
	}
	var dataLines [][]byte
	for _, line := range bytes.Split(trimmed, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("data:")) {
			dataLines = append(dataLines, bytes.TrimSpace(line[len("data:"):]))
		}
	}
	if len(dataLines) > 0 {
		return bytes.Join(dataLines, []byte("\n"))
	}
	return trimmed
}

func responseModel(modelName string, root gjson.Result) string {
	return firstNonEmpty(modelName, root.Get("model").String(), root.Get("response.model").String(), root.Get("interaction.model").String())
}

func firstUsageInt(root gjson.Result, paths ...string) (int64, bool) {
	for _, path := range paths {
		if v := root.Get(path); v.Exists() {
			return v.Int(), true
		}
	}
	return 0, false
}

func nextResponsesSeq(st *interactionsToResponsesStreamState) int {
	st.Seq++
	return st.Seq
}

func emitResponsesEvent(event string, payload []byte) []byte {
	return translatorcommon.SSEEventData(event, payload)
}

func emitInteractionsEvent(event string, payload []byte) []byte {
	return translatorcommon.SSEEventData(event, payload)
}

func textKeysFromResponsesEvent(root gjson.Result) []string {
	itemID := root.Get("item_id").String()
	outputIndex := int(root.Get("output_index").Int())
	hasOutputIndex := root.Get("output_index").Exists()
	contentIndex := int(root.Get("content_index").Int())
	hasContentIndex := root.Get("content_index").Exists()
	if !hasContentIndex {
		return openAIResponsesUnkeyedTextKeys(itemID, outputIndex, hasOutputIndex)
	}
	return openAIResponsesTextKeys(itemID, outputIndex, hasOutputIndex, contentIndex, hasContentIndex)
}

func functionArgsKeysFromResponsesEvent(root gjson.Result) []string {
	item := root.Get("item")
	outputIndex := int(root.Get("output_index").Int())
	hasOutputIndex := root.Get("output_index").Exists()
	keys := make([]string, 0, 5)
	for _, id := range []string{
		root.Get("item_id").String(),
		root.Get("call_id").String(),
		item.Get("call_id").String(),
		item.Get("id").String(),
	} {
		if id == "" {
			continue
		}
		key := fmt.Sprintf("item:%s", id)
		if !stringSliceContains(keys, key) {
			keys = append(keys, key)
		}
	}
	if hasOutputIndex {
		keys = append(keys, fmt.Sprintf("output:%d", outputIndex))
	}
	return keys
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func openAIResponsesTextKeys(itemID string, outputIndex int, hasOutputIndex bool, contentIndex int, hasContentIndex bool) []string {
	if !hasContentIndex {
		return nil
	}
	keys := make([]string, 0, 3)
	if itemID != "" {
		keys = append(keys, fmt.Sprintf("item:%s:content:%d", itemID, contentIndex))
	}
	if hasOutputIndex {
		keys = append(keys, fmt.Sprintf("output:%d:content:%d", outputIndex, contentIndex))
	}
	keys = append(keys, fmt.Sprintf("content:%d", contentIndex))
	return keys
}

func openAIResponsesUnkeyedTextKeys(itemID string, outputIndex int, hasOutputIndex bool) []string {
	keys := make([]string, 0, 2)
	if itemID != "" {
		keys = append(keys, fmt.Sprintf("item:%s", itemID))
	}
	if hasOutputIndex {
		keys = append(keys, fmt.Sprintf("output:%d", outputIndex))
	}
	return keys
}

func (st *responsesToInteractionsStreamState) markTextSent(keys []string) {
	if len(keys) == 0 {
		st.UnkeyedTextDelta = true
		return
	}
	if st.SentText == nil {
		st.SentText = map[string]bool{}
	}
	for _, key := range keys {
		st.SentText[key] = true
	}
}

func (st *responsesToInteractionsStreamState) hasSentText(keys []string, hasContentIndex bool) bool {
	if !hasContentIndex && st.UnkeyedTextDelta {
		return true
	}
	for _, key := range keys {
		if st.SentText[key] {
			return true
		}
	}
	return false
}

func (st *responsesToInteractionsStreamState) hasSentUnkeyedText(keys []string) bool {
	if len(keys) == 0 {
		return st.UnkeyedTextDelta
	}
	for _, key := range keys {
		if st.SentText[key] {
			return true
		}
	}
	return false
}

func (st *responsesToInteractionsStreamState) markFunctionArgsSent(keys []string) {
	for _, key := range keys {
		st.FunctionArgsSent[key] = true
	}
}

func (st *responsesToInteractionsStreamState) hasSentFunctionArgs(keys []string) bool {
	for _, key := range keys {
		if st.FunctionArgsSent[key] {
			return true
		}
	}
	return false
}
