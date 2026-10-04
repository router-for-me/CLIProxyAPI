package responses

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	applypatch "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/apply-patch"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type interactionsToResponsesStreamState struct {
	translatorcommon.ApplyPatchErrorState
	ID                    string
	EnvironmentID         string
	FunctionCalls         map[int]*interactionsFunctionCallState
	ItemIDs               map[int]string
	ItemTypes             map[int]string
	ReasoningEncrypted    map[int]string
	ReasoningSummaries    map[int][]string
	TextOutputs           map[int]*strings.Builder
	Seq                   int
	Done                  bool
	Terminal              bool
	SourceFailed          bool
	ToolIdentityMap       map[string]util.ResponsesToolIdentity
	PendingEnvelopeError  error
	PendingIdentityErrors map[int]error
	ItemIdentityIndexes   map[string]int
	CallIdentityIndexes   map[string]int
	ForAntigravity        bool
}

type interactionsFunctionCallState struct {
	ID                   string
	CallID               string
	ItemIDSeen           bool
	CallIDSeen           bool
	InitialArguments     string
	RawName              string
	Added                bool
	PatchCall            *translatorcommon.ApplyPatchCallState
	PendingError         error
	SnapshotArguments    string
	SnapshotInput        string
	HasSnapshot          bool
	Name                 string
	Namespace            string
	IsCustom             bool
	Arguments            strings.Builder
	ArgumentFragments    []string
	SourceStopped        bool
	StopPending          bool
	IdentityFinalized    bool
	ArgumentsDoneEmitted bool
	ItemDoneEmitted      bool
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
	if param == nil {
		var local any
		param = &local
	}
	if *param == nil {
		*param = &interactionsToResponsesStreamState{}
	}
	st := (*param).(*interactionsToResponsesStreamState)
	st.ForAntigravity = isAntigravityModel(modelName)
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
	if st.ToolIdentityMap == nil {
		reqJSON := originalRequestRawJSON
		if len(reqJSON) == 0 {
			reqJSON = requestRawJSON
		}
		if len(reqJSON) > 0 {
			st.ToolIdentityMap = interactionsToolIdentityMap(reqJSON, st.ForAntigravity)
		}
	}
	return convertInteractionsEventToResponses(modelName, originalRequestRawJSON, requestRawJSON, rawJSON, st)
}

func ConvertInteractionsResponseToOpenAIResponsesNonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
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
	forAntigravity := isAntigravityModel(responseModel(modelName, root))
	reqJSON := originalRequestRawJSON
	if len(reqJSON) == 0 {
		reqJSON = requestRawJSON
	}
	var toolIdentityMap map[string]util.ResponsesToolIdentity
	if len(reqJSON) > 0 {
		toolIdentityMap = interactionsToolIdentityMap(reqJSON, forAntigravity)
	}
	patchEnabled := false
	for _, identity := range toolIdentityMap {
		if identity.ApplyPatch {
			patchEnabled = true
			break
		}
	}
	var toolInputError error
	status := firstNonEmpty(root.Get("status").String(), root.Get("interaction.status").String())
	sourceError := firstExisting(root.Get("error"), root.Get("interaction.error"))
	if patchEnabled && (status == "failed" || (sourceError.Exists() && sourceError.Type != gjson.Null)) {
		toolInputError = fmt.Errorf("upstream apply_patch interaction failed")
	}
	var outputs [][]byte
	steps.ForEach(func(_, step gjson.Result) bool {
		if toolInputError != nil {
			return false
		}
		if patchEnabled && step.Get("type").String() == "function_call" && step.Get("name").String() == "" {
			toolInputError = fmt.Errorf("unresolved Interactions apply_patch call identity")
			return false
		}
		if step.Get("type").String() == "function_call" && toolIdentityMap[step.Get("name").String()].ApplyPatch {
			if !gjson.ValidBytes(rawJSON) {
				toolInputError = fmt.Errorf("invalid Interactions apply_patch response JSON")
				return false
			}
			var patchCall translatorcommon.ApplyPatchCallState
			if _, _, errFinishArguments := patchCall.FinishArguments(jsonStringValue(step.Get("arguments"), "{}")); errFinishArguments != nil {
				toolInputError = errFinishArguments
				return false
			}
		}
		if item, ok := interactionsStepToResponsesOutput(step, forAntigravity, toolIdentityMap); ok {
			outputs = append(outputs, item)
		}
		return true
	})
	if toolInputError != nil {
		if param != nil {
			state := &translatorcommon.ApplyPatchErrorState{}
			state.SetToolInputError(toolInputError)
			*param = state
		}
		return nil
	}
	if len(outputs) > 0 {
		out, _ = sjson.SetRawBytes(out, "output", translatorcommon.JoinRawArray(outputs))
	}
	interactionStatus := firstNonEmpty(root.Get("status").String(), root.Get("interaction.status").String())
	interactionFinishReason := firstNonEmpty(root.Get("finish_reason").String(), root.Get("interaction.finish_reason").String())
	if interactionFinishReason == "content_filter" {
		out, _ = sjson.SetBytes(out, "status", "incomplete")
		out, _ = sjson.SetBytes(out, "incomplete_details.reason", "content_filter")
	} else if interactionStatus == "incomplete" || interactionFinishReason == "length" || interactionFinishReason == "max_tokens" {
		out, _ = sjson.SetBytes(out, "status", "incomplete")
		out, _ = sjson.SetBytes(out, "incomplete_details.reason", "max_output_tokens")
	}
	if envID := firstNonEmpty(root.Get("environment_id").String(), root.Get("interaction.environment_id").String(), root.Get("environment.id").String(), root.Get("interaction.environment.id").String()); envID != "" {
		out, _ = sjson.SetBytes(out, "environment_id", envID)
	}
	out = setResponsesUsageFromInteractions(out, "usage", translatorcommon.InteractionsUsage(root))
	return out
}

func convertInteractionsEventToResponses(modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, st *interactionsToResponsesStreamState) [][]byte {
	if st.Done || st.ToolInputError() != nil || st.SourceFailed {
		return nil
	}
	payload := interactionsSSEPayload(rawJSON)
	if len(payload) == 0 {
		return nil
	}
	// A source sentinel remains legal after response completion, but only once.
	isDone := bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) || (gjson.ValidBytes(payload) && gjson.GetBytes(payload, "event_type").String() == "done")
	if isDone {
		events := interactionsFinishPatchCalls(st)
		if st.ToolInputError() != nil {
			return events
		}
		st.Done = true
		st.Terminal = true
		return append(events, []byte("data: [DONE]"))
	}
	if st.Terminal {
		return nil
	}
	root := gjson.ParseBytes(payload)
	if !root.Exists() {
		return nil
	}
	if !gjson.ValidBytes(payload) {
		errJSON := fmt.Errorf("invalid Interactions apply_patch event JSON")
		index, errResolveStepIndex := interactionsResolveStepIndex(root.Get("index"), root.Get("step"), 0, st)
		if errResolveStepIndex != nil {
			return interactionsPatchFailure(st, errResolveStepIndex)
		}
		if strings.HasPrefix(root.Get("event_type").String(), "step.") {
			call := st.FunctionCalls[index]
			if call == nil {
				call = &interactionsFunctionCallState{ID: root.Get("step.id").String(), CallID: root.Get("step.call_id").String(), ItemIDSeen: root.Get("step.id").String() != "", CallIDSeen: root.Get("step.call_id").String() != ""}
				st.FunctionCalls[index] = call
			}
			if call.PendingError == nil {
				call.PendingError = errJSON
			}
			if st.ToolIdentityMap[call.RawName].ApplyPatch || st.ToolIdentityMap[root.Get("step.name").String()].ApplyPatch {
				return interactionsPatchFailure(st, errJSON)
			}
		} else {
			if st.PendingEnvelopeError == nil {
				st.PendingEnvelopeError = errJSON
			}
			for _, call := range st.FunctionCalls {
				if call.PatchCall != nil {
					return interactionsPatchFailure(st, st.PendingEnvelopeError)
				}
			}
		}
	}
	switch root.Get("event_type").String() {
	case "interaction.created":
		return [][]byte{responsesCreatedEvent(modelName, originalRequestRawJSON, requestRawJSON, root, st)}
	case "step.start":
		return interactionsStepStartToResponses(modelName, root, st)
	case "step.delta":
		return interactionsStepDeltaToResponses(root, st)
	case "step.stop":
		return interactionsStepStopToResponses(root, st)
	case "interaction.completed", "finish":
		var events [][]byte
		steps := firstExisting(root.Get("interaction.steps"), root.Get("steps"))
		patchEnabled := interactionsHasPatchBridge(st)
		steps.ForEach(func(key, step gjson.Result) bool {
			index, errResolveStepIndex := interactionsResolveStepIndex(step.Get("index"), step, int(key.Int()), st)
			if errResolveStepIndex != nil {
				events = append(events, interactionsPatchFailure(st, errResolveStepIndex)...)
				return false
			}
			call := st.FunctionCalls[index]
			// Patch-enabled unnamed functions retain evidence before final snapshot filtering.
			if call == nil || (call.PatchCall == nil && !st.ToolIdentityMap[call.RawName].ApplyPatch && (!patchEnabled || call.RawName != "")) {
				if step.Get("type").String() != "function_call" {
					return true
				}
				unresolved := patchEnabled && (step.Get("name").String() == "" || (call != nil && call.RawName == ""))
				if !st.ToolIdentityMap[step.Get("name").String()].ApplyPatch && !unresolved {
					return true
				}
			}
			events = append(events, interactionsUpdateFunctionCall(index, step, st, false)...)
			return !st.Terminal
		})
		if st.Terminal {
			return events
		}
		events = append(events, interactionsFinishPatchCalls(st)...)
		if st.Terminal {
			return events
		}
		st.Terminal = true
		return append(events, responsesCompletedEvent(modelName, root, st))
	case "response.failed", "interaction.failed":
		if interactionsHasPatchBridge(st) {
			return interactionsPatchFailure(st, fmt.Errorf("upstream apply_patch interaction failed"))
		}
		st.SourceFailed = true
		st.Terminal = true
		return [][]byte{responsesFailedEvent(modelName, root, st)}

	}
	return nil
}

func interactionsStepToResponsesOutput(step gjson.Result, forAntigravity bool, toolIdentityMap map[string]util.ResponsesToolIdentity) ([]byte, bool) {
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
			item = translatorcommon.SetRawArrayItems(item, "content", [][]byte{part})
		} else {
			var parts [][]byte
			content.ForEach(func(_, part gjson.Result) bool {
				if converted, ok := interactionsContentPartToResponses(part, "assistant"); ok {
					parts = append(parts, converted)
				}
				return true
			})
			if len(parts) > 0 {
				item = translatorcommon.SetRawArrayItems(item, "content", parts)
			}
		}
		return item, true
	case "thought":
		item := []byte(`{"type":"reasoning","summary":[]}`)
		if signature := interactionsThoughtSignature(step); signature != "" {
			item, _ = sjson.SetBytes(item, "encrypted_content", signature)
		}
		texts := interactionsContentTexts(step.Get("content"))
		if len(texts) > 0 {
			summaries := make([][]byte, 0, len(texts))
			for _, text := range texts {
				part := []byte(`{"type":"summary_text","text":""}`)
				part, _ = sjson.SetBytes(part, "text", text)
				summaries = append(summaries, part)
			}
			item = translatorcommon.SetRawArrayItems(item, "summary", summaries)
		}
		return item, true
	case "function_call":
		item := interactionsFunctionCallToResponsesWithIdentity(step, forAntigravity, toolIdentityMap)
		item, _ = sjson.SetBytes(item, "status", "completed")
		return item, true
	}
	return nil, false
}

func responsesCreatedEvent(modelName string, originalRequestRawJSON, requestRawJSON []byte, root gjson.Result, st *interactionsToResponsesStreamState) []byte {
	payload := []byte(`{"type":"response.created","response":{"id":"","object":"response","status":"in_progress","model":"","output":[]}}`)
	payload, _ = sjson.SetBytes(payload, "sequence_number", nextResponsesSeq(st))
	id := firstNonEmpty(root.Get("interaction.id").String(), root.Get("id").String())
	if st != nil && id != "" {
		st.ID = id
	}
	payload, _ = sjson.SetBytes(payload, "response.id", id)
	payload, _ = sjson.SetBytes(payload, "response.model", modelName)
	if envID := firstNonEmpty(root.Get("interaction.environment_id").String(), root.Get("environment_id").String(), root.Get("environment.id").String(), root.Get("interaction.environment.id").String()); envID != "" {
		if st != nil {
			st.EnvironmentID = envID
		}
		payload, _ = sjson.SetBytes(payload, "response.environment_id", envID)
	}
	requestModelName := translatorcommon.RequestModelName(originalRequestRawJSON, requestRawJSON)
	if requestModelName == "" {
		requestModelName = modelName
	}
	if requestModelName != "" {
		payload, _ = sjson.SetBytes(payload, "response.model", requestModelName)
	}
	return emitResponsesEvent("response.created", payload)
}

func interactionsStepStartToResponses(modelName string, root gjson.Result, st *interactionsToResponsesStreamState) [][]byte {
	step := root.Get("step")
	index, errResolveStepIndex := interactionsResolveStepIndex(root.Get("index"), step, 0, st)
	if errResolveStepIndex != nil {
		return interactionsPatchFailure(st, errResolveStepIndex)
	}
	stepType := step.Get("type").String()
	// A repeated start must not overwrite a possible patch call's type evidence.
	if call := st.FunctionCalls[index]; call != nil && (call.PatchCall != nil || st.ToolIdentityMap[call.RawName].ApplyPatch || (call.RawName == "" && interactionsHasPatchBridge(st))) {
		return interactionsUpdateFunctionCall(index, step, st, true)
	}
	itemID := firstNonEmpty(step.Get("id").String(), step.Get("call_id").String(), fmt.Sprintf("item_%d", index))
	if stepType == "function_call" {
		return interactionsUpdateFunctionCall(index, step, st, true)
	}
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
		if signature := interactionsReasoningEncryptedContent(st.ReasoningEncrypted[index]); signature != "" {
			added, _ = sjson.SetBytes(added, "item.encrypted_content", signature)
		}
		part := []byte(`{"type":"response.reasoning_summary_part.added","item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`)
		part, _ = sjson.SetBytes(part, "sequence_number", nextResponsesSeq(st))
		part, _ = sjson.SetBytes(part, "item_id", itemID)
		part, _ = sjson.SetBytes(part, "output_index", index)
		return [][]byte{emitResponsesEvent("response.output_item.added", added), emitResponsesEvent("response.reasoning_summary_part.added", part)}

	}
	return nil
}

func interactionsStepDeltaToResponses(root gjson.Result, st *interactionsToResponsesStreamState) [][]byte {
	index, errResolveStepIndex := interactionsResolveStepIndex(root.Get("index"), root.Get("step"), 0, st)
	if errResolveStepIndex != nil {
		return interactionsPatchFailure(st, errResolveStepIndex)
	}
	// Check the source barrier before a same-event snapshot or identity update can
	// replay fragments and publish completion. Unknown names retain the violation.
	if call := st.FunctionCalls[index]; call != nil && call.SourceStopped && root.Get("delta.type").String() == "arguments_delta" && root.Get("delta.arguments").String() != "" {
		errSourceStop := fmt.Errorf("apply_patch delta after source stop")
		if call.PatchCall != nil || st.ToolIdentityMap[call.RawName].ApplyPatch || st.ToolIdentityMap[root.Get("step.name").String()].ApplyPatch {
			return interactionsPatchFailure(st, errSourceStop)
		}
		if call.RawName == "" && interactionsHasPatchBridge(st) && call.PendingError == nil {
			call.PendingError = errSourceStop
		}
	}
	if root.Get("step").IsObject() {
		updates := interactionsUpdateFunctionCall(index, root.Get("step"), st, false)
		if st.Terminal {
			return updates
		}
		// Process the same real delta after its late identity update, without
		// replaying the update or turning its snapshot into parameter fragments.
		raw, _ := sjson.DeleteBytes([]byte(root.Raw), "step")
		raw, _ = sjson.SetBytes(raw, "index", index)
		return append(updates, interactionsStepDeltaToResponses(gjson.ParseBytes(raw), st)...)
	}
	delta := root.Get("delta")
	switch delta.Get("type").String() {
	case "thought_summary":
		text := firstNonEmpty(delta.Get("content.text").String(), delta.Get("text").String())
		recordResponsesReasoningSummary(st, index, text)
		payload := []byte(`{"type":"response.reasoning_summary_text.delta","item_id":"","output_index":0,"summary_index":0,"delta":""}`)
		payload, _ = sjson.SetBytes(payload, "sequence_number", nextResponsesSeq(st))
		payload, _ = sjson.SetBytes(payload, "item_id", st.ItemIDs[index])
		payload, _ = sjson.SetBytes(payload, "output_index", index)
		payload, _ = sjson.SetBytes(payload, "delta", text)
		return [][]byte{emitResponsesEvent("response.reasoning_summary_text.delta", payload)}
	case "thought_signature":
		if signature := interactionsReasoningEncryptedContent(delta.Get("signature").String()); signature != "" {
			st.ReasoningEncrypted[index] = signature
		}
		return nil
	case "arguments_delta":
		arguments := delta.Get("arguments").String()
		if st.FunctionCalls[index] == nil {
			st.FunctionCalls[index] = &interactionsFunctionCallState{}
		}
		if call := st.FunctionCalls[index]; call != nil {
			if delta.Get("invalid_json_str").Exists() {
				call.PendingError = fmt.Errorf("legacy freeform arguments are invalid for apply_patch")
				if call.PatchCall != nil || st.ToolIdentityMap[call.RawName].ApplyPatch {
					return interactionsPatchFailure(st, call.PendingError)
				}
			}
			if call.PatchCall != nil {
				if call.ItemDoneEmitted && arguments != "" {
					return interactionsPatchFailure(st, fmt.Errorf("apply_patch delta after item completion"))
				}
				call.Arguments.WriteString(arguments)
				delta, errPushArguments := call.PatchCall.PushArguments(arguments)
				if errPushArguments != nil {
					return interactionsPatchFailure(st, errPushArguments)
				}
				return interactionsPatchDelta(st, call, delta)
			}
			if call.ItemDoneEmitted {
				return nil
			}
			call.Arguments.WriteString(arguments)
			if !call.SourceStopped && (call.RawName == "" || st.ToolIdentityMap[call.RawName].ApplyPatch) {
				call.ArgumentFragments = append(call.ArgumentFragments, arguments)
			}
			if call.RawName == "" {
				return nil
			}
			if call.IsCustom {
				return nil
			}
		}
		return [][]byte{responsesFunctionCallArgumentsDeltaToResponses(index, st.ItemIDs[index], arguments, st)}
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

func responsesFunctionCallArgumentsDeltaToResponses(index int, itemID, arguments string, st *interactionsToResponsesStreamState) []byte {
	payload := []byte(`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"","delta":""}`)
	payload, _ = sjson.SetBytes(payload, "sequence_number", nextResponsesSeq(st))
	payload, _ = sjson.SetBytes(payload, "output_index", index)
	payload, _ = sjson.SetBytes(payload, "item_id", itemID)
	payload, _ = translatorcommon.SetStringWithoutHTMLEscape(payload, "delta", arguments)
	return emitResponsesEvent("response.function_call_arguments.delta", payload)
}

func responsesFunctionCallArgumentsDoneToResponses(index int, itemID, arguments string, st *interactionsToResponsesStreamState) []byte {
	payload := []byte(`{"type":"response.function_call_arguments.done","output_index":0,"item_id":"","arguments":""}`)
	payload, _ = sjson.SetBytes(payload, "sequence_number", nextResponsesSeq(st))
	payload, _ = sjson.SetBytes(payload, "output_index", index)
	payload, _ = sjson.SetBytes(payload, "item_id", itemID)
	payload, _ = translatorcommon.SetStringWithoutHTMLEscape(payload, "arguments", arguments)
	return emitResponsesEvent("response.function_call_arguments.done", payload)
}

func responsesCustomToolCallInputDoneToResponses(index int, itemID, input string, st *interactionsToResponsesStreamState) []byte {
	payload := []byte(`{"type":"response.custom_tool_call_input.done","output_index":0,"item_id":"","input":""}`)
	payload, _ = sjson.SetBytes(payload, "sequence_number", nextResponsesSeq(st))
	payload, _ = sjson.SetBytes(payload, "output_index", index)
	payload, _ = sjson.SetBytes(payload, "item_id", itemID)
	payload, _ = sjson.SetBytes(payload, "input", input)
	return emitResponsesEvent("response.custom_tool_call_input.done", payload)
}

func interactionsStepStopToResponses(root gjson.Result, st *interactionsToResponsesStreamState) [][]byte {
	index, errResolveStepIndex := interactionsResolveStepIndex(root.Get("index"), root.Get("step"), 0, st)
	if errResolveStepIndex != nil {
		return interactionsPatchFailure(st, errResolveStepIndex)
	}
	// Source completion is independent of downstream identity and publication,
	// including candidates established by arguments before their first start.
	if call := st.FunctionCalls[index]; call != nil {
		call.SourceStopped = true
		if call.RawName == "" {
			call.StopPending = true
		}
	}
	var updates [][]byte
	if st.ItemTypes[index] == "function_call" && root.Get("step").IsObject() {
		updates = interactionsUpdateFunctionCall(index, root.Get("step"), st, false)
		if st.Terminal {
			return updates
		}
	}
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
		if call == nil {
			call = &interactionsFunctionCallState{ID: itemID, SourceStopped: true}
			st.FunctionCalls[index] = call
		}
		if call.RawName == "" {
			call.StopPending = true
			return updates
		}
		if st.ToolIdentityMap[call.RawName].ApplyPatch && call.PatchCall == nil {
			call.StopPending = true
			return updates
		}
		if call.ItemDoneEmitted {
			return updates
		}
		events := updates
		if call.PatchCall != nil {
			args := call.SnapshotArguments
			if !call.HasSnapshot {
				args = call.Arguments.String()
			}
			if call.HasSnapshot && gjson.Valid(call.Arguments.String()) {
				var source translatorcommon.ApplyPatchCallState
				_, input, errFinishArguments := source.FinishArguments(call.Arguments.String())
				if errFinishArguments != nil {
					return interactionsPatchFailure(st, errFinishArguments)
				}
				if input != call.SnapshotInput {
					return interactionsPatchFailure(st, fmt.Errorf("apply_patch complete source conflicts with snapshot"))
				}
			}
			tail, input, errFinishArguments := call.PatchCall.FinishArguments(args)
			if errFinishArguments != nil {
				return interactionsPatchFailure(st, errFinishArguments)
			}
			events = append(events, interactionsPatchDelta(st, call, tail)...)
			events = append(events, emitResponsesEvent("response.custom_tool_call_input.done", translatorcommon.ApplyPatchInputDone(call.PatchCall, input, nextResponsesSeq(st))))
			item, _ := responsesCompletedOutputItem(index, "function_call", st)
			done := []byte(`{"type":"response.output_item.done"}`)
			done, _ = sjson.SetBytes(done, "sequence_number", nextResponsesSeq(st))
			done, _ = sjson.SetBytes(done, "output_index", index)
			done, _ = sjson.SetRawBytes(done, "item", item)
			call.ItemDoneEmitted = true
			call.ArgumentsDoneEmitted = true
			return append(events, emitResponsesEvent("response.output_item.done", done))
		}
		if call.IsCustom {
			input := util.UnwrapResponsesCustomToolInput(call.Arguments.String())
			if !call.ArgumentsDoneEmitted {
				events = append(events, responsesCustomToolCallInputDoneToResponses(index, itemID, input, st))
				call.ArgumentsDoneEmitted = true
			}
			done := []byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"","type":"custom_tool_call","call_id":"","name":"","input":"","status":"completed"}}`)
			done, _ = sjson.SetBytes(done, "sequence_number", nextResponsesSeq(st))
			done, _ = sjson.SetBytes(done, "output_index", index)
			done, _ = sjson.SetBytes(done, "item.id", itemID)
			done, _ = sjson.SetBytes(done, "item.call_id", call.CallID)
			if call.Namespace != "" {
				done, _ = sjson.SetBytes(done, "item.namespace", call.Namespace)
			}
			done, _ = sjson.SetBytes(done, "item.name", call.Name)
			done, _ = sjson.SetBytes(done, "item.input", input)
			call.ItemDoneEmitted = true
			return append(events, emitResponsesEvent("response.output_item.done", done))
		}

		arguments := responsesFunctionCallArguments(call)
		if !call.ArgumentsDoneEmitted {
			events = append(events, responsesFunctionCallArgumentsDoneToResponses(index, itemID, arguments, st))
			call.ArgumentsDoneEmitted = true
		}
		done := []byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"","type":"function_call","call_id":"","name":"","arguments":"","status":"completed"}}`)
		done, _ = sjson.SetBytes(done, "sequence_number", nextResponsesSeq(st))
		done, _ = sjson.SetBytes(done, "output_index", index)
		done, _ = sjson.SetBytes(done, "item.id", itemID)
		done, _ = sjson.SetBytes(done, "item.call_id", call.CallID)
		if call.Namespace != "" {
			done, _ = sjson.SetBytes(done, "item.namespace", call.Namespace)
		}
		done, _ = sjson.SetBytes(done, "item.name", call.Name)
		done, _ = translatorcommon.SetStringWithoutHTMLEscape(done, "item.arguments", arguments)
		call.ItemDoneEmitted = true
		return append(events, emitResponsesEvent("response.output_item.done", done))
	case "thought":
		text := strings.Join(st.ReasoningSummaries[index], "")
		textDone := []byte(`{"type":"response.reasoning_summary_text.done","item_id":"","output_index":0,"summary_index":0,"text":""}`)
		textDone, _ = sjson.SetBytes(textDone, "sequence_number", nextResponsesSeq(st))
		textDone, _ = sjson.SetBytes(textDone, "item_id", itemID)
		textDone, _ = sjson.SetBytes(textDone, "output_index", index)
		textDone, _ = sjson.SetBytes(textDone, "text", text)
		partDone := []byte(`{"type":"response.reasoning_summary_part.done","item_id":"","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`)
		partDone, _ = sjson.SetBytes(partDone, "sequence_number", nextResponsesSeq(st))
		partDone, _ = sjson.SetBytes(partDone, "item_id", itemID)
		partDone, _ = sjson.SetBytes(partDone, "output_index", index)
		partDone, _ = sjson.SetBytes(partDone, "part.text", text)
		done := []byte(`{"type":"response.output_item.done","output_index":0,"item":{}}`)
		done, _ = sjson.SetBytes(done, "sequence_number", nextResponsesSeq(st))
		done, _ = sjson.SetBytes(done, "output_index", index)
		done, _ = sjson.SetRawBytes(done, "item", responsesReasoningItem(index, st))
		return [][]byte{
			emitResponsesEvent("response.reasoning_summary_text.done", textDone),
			emitResponsesEvent("response.reasoning_summary_part.done", partDone),
			emitResponsesEvent("response.output_item.done", done),
		}
	default:
		done := []byte(`{"type":"response.output_item.done","output_index":0,"item":{}}`)
		done, _ = sjson.SetBytes(done, "sequence_number", nextResponsesSeq(st))
		done, _ = sjson.SetBytes(done, "output_index", index)
		done, _ = sjson.SetRawBytes(done, "item", responsesReasoningItem(index, st))
		return [][]byte{emitResponsesEvent("response.output_item.done", done)}
	}
}

func responsesCompletedEvent(modelName string, root gjson.Result, st *interactionsToResponsesStreamState) []byte {
	eventType := "response.completed"
	status := "completed"
	interaction := root.Get("interaction")
	interactionStatus := firstNonEmpty(interaction.Get("status").String(), root.Get("status").String())
	interactionFinishReason := firstNonEmpty(interaction.Get("finish_reason").String(), root.Get("finish_reason").String())
	var incompleteReason string
	if interactionFinishReason == "content_filter" {
		eventType = "response.incomplete"
		status = "incomplete"
		incompleteReason = "content_filter"
	} else if interactionStatus == "incomplete" || interactionFinishReason == "length" || interactionFinishReason == "max_tokens" {
		eventType = "response.incomplete"
		status = "incomplete"
		incompleteReason = "max_output_tokens"
	}
	payload := []byte(`{"type":"response.completed","response":{"id":"","object":"response","status":"completed","model":"","output":[],"usage":{}}}`)
	payload, _ = sjson.SetBytes(payload, "type", eventType)
	payload, _ = sjson.SetBytes(payload, "response.status", status)
	if incompleteReason != "" {
		payload, _ = sjson.SetBytes(payload, "response.incomplete_details.reason", incompleteReason)
	}
	payload, _ = sjson.SetBytes(payload, "sequence_number", nextResponsesSeq(st))
	payload, _ = sjson.SetBytes(payload, "response.id", firstNonEmpty(interaction.Get("id").String(), root.Get("id").String()))
	payload, _ = sjson.SetBytes(payload, "response.model", firstNonEmpty(interaction.Get("model").String(), modelName))
	envID := firstNonEmpty(interaction.Get("environment_id").String(), root.Get("environment_id").String(), interaction.Get("environment.id").String(), root.Get("environment.id").String())
	if envID == "" && st != nil {
		envID = st.EnvironmentID
	}
	if envID != "" {
		payload, _ = sjson.SetBytes(payload, "response.environment_id", envID)
	}
	payload = setResponsesCompletedOutput(payload, st)
	payload = setResponsesUsageFromInteractions(payload, "response.usage", translatorcommon.InteractionsUsage(root))
	return emitResponsesEvent(eventType, payload)
}

func responsesFailedEvent(modelName string, root gjson.Result, st *interactionsToResponsesStreamState) []byte {
	eventType := "response.failed"
	payload := []byte(`{"type":"response.failed","response":{"id":"","object":"response","status":"failed","model":"","output":[],"error":{"message":"","code":"","type":"server_error"}}}`)
	payload, _ = sjson.SetBytes(payload, "sequence_number", nextResponsesSeq(st))
	interaction := root.Get("interaction")
	id := firstNonEmpty(interaction.Get("id").String(), root.Get("id").String())
	if id == "" && st != nil {
		id = st.ID
	}
	payload, _ = sjson.SetBytes(payload, "response.id", id)
	payload, _ = sjson.SetBytes(payload, "response.model", firstNonEmpty(interaction.Get("model").String(), modelName))
	errNode := root.Get("error")
	if !errNode.Exists() && interaction.Exists() {
		errNode = interaction.Get("error")
	}
	msg := errNode.Get("message").String()
	if msg == "" {
		msg = "upstream execution failed"
	}
	code := errNode.Get("code").String()
	payload, _ = sjson.SetBytes(payload, "response.error.message", msg)
	if code != "" {
		payload, _ = sjson.SetBytes(payload, "response.error.code", code)
	} else {
		payload, _ = sjson.DeleteBytes(payload, "response.error.code")
	}
	errType := errNode.Get("type").String()
	if errType == "" {
		errType = "server_error"
	}
	payload, _ = sjson.SetBytes(payload, "response.error.type", errType)
	return emitResponsesEvent(eventType, payload)
}

func interactionsThoughtSignature(step gjson.Result) string {
	for _, path := range []string{
		"encrypted_content",
		"signature",
		"thought_signature",
		"thoughtSignature",
		"extra_content.google.thought_signature",
	} {
		if signature := interactionsReasoningEncryptedContent(step.Get(path).String()); signature != "" {
			return signature
		}
	}
	content := step.Get("content")
	if content.IsArray() {
		var signature string
		content.ForEach(func(_, part gjson.Result) bool {
			candidate := firstNonEmpty(
				part.Get("signature").String(),
				part.Get("thought_signature").String(),
				part.Get("thoughtSignature").String(),
				part.Get("extra_content.google.thought_signature").String(),
			)
			if valid := interactionsReasoningEncryptedContent(candidate); valid != "" {
				signature = valid
				return false
			}
			return true
		})
		return signature
	}
	return ""
}

func interactionsReasoningEncryptedContent(rawSignature string) string {
	candidate := strings.TrimSpace(rawSignature)
	if candidate == "" {
		return ""
	}
	if signature.IsRecognizedReasoningSignature(candidate) {
		return candidate
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
	var outputItems [][]byte
	for index := 0; index <= maxIndex; index++ {
		itemType, ok := st.ItemTypes[index]
		if !ok {
			continue
		}
		item, ok := responsesCompletedOutputItem(index, itemType, st)
		if ok {
			outputItems = append(outputItems, item)
		}
	}
	if len(outputItems) > 0 {
		payload = translatorcommon.SetRawArrayItems(payload, "response.output", outputItems)
	}
	return payload
}

func responsesFunctionCallArguments(call *interactionsFunctionCallState) string {
	if call == nil || call.Arguments.Len() == 0 {
		return "{}"
	}
	return call.Arguments.String()
}

func responsesCompletedOutputItem(index int, itemType string, st *interactionsToResponsesStreamState) ([]byte, bool) {
	switch itemType {
	case "model_output":
		item := []byte(`{"id":"","type":"message","status":"completed","role":"assistant","content":[]}`)
		item, _ = sjson.SetBytes(item, "id", st.ItemIDs[index])
		if builder := st.TextOutputs[index]; builder != nil && builder.String() != "" {
			part := []byte(`{"type":"output_text","text":""}`)
			part, _ = sjson.SetBytes(part, "text", builder.String())
			item = translatorcommon.SetRawArrayItems(item, "content", [][]byte{part})
		}
		return item, true
	case "thought":
		return responsesReasoningItem(index, st), true
	case "function_call":
		if call := st.FunctionCalls[index]; call != nil && call.IsCustom {
			item := []byte(`{"id":"","type":"custom_tool_call","call_id":"","name":"","input":"","status":"completed"}`)
			itemID := st.ItemIDs[index]
			item, _ = sjson.SetBytes(item, "id", itemID)
			item, _ = sjson.SetBytes(item, "call_id", call.CallID)
			if call.Namespace != "" {
				item, _ = sjson.SetBytes(item, "namespace", call.Namespace)
			}
			item, _ = sjson.SetBytes(item, "name", call.Name)
			input := util.UnwrapResponsesCustomToolInput(responsesFunctionCallArguments(call))
			if call.PatchCall != nil {
				input = call.PatchCall.Decoder.Input()
			}
			item, _ = sjson.SetBytes(item, "input", input)
			return item, true
		}
		item := []byte(`{"id":"","type":"function_call","call_id":"","name":"","arguments":"{}","status":"completed"}`)
		itemID := st.ItemIDs[index]
		item, _ = sjson.SetBytes(item, "id", itemID)
		item, _ = sjson.SetBytes(item, "call_id", itemID)
		if call := st.FunctionCalls[index]; call != nil {
			item, _ = sjson.SetBytes(item, "call_id", call.CallID)
			if call.Namespace != "" {
				item, _ = sjson.SetBytes(item, "namespace", call.Namespace)
			}
			item, _ = sjson.SetBytes(item, "name", call.Name)
			item, _ = translatorcommon.SetStringWithoutHTMLEscape(item, "arguments", responsesFunctionCallArguments(call))
		}
		return item, true
	}
	return nil, false
}

func responsesReasoningItem(index int, st *interactionsToResponsesStreamState) []byte {
	item := []byte(`{"id":"","type":"reasoning","status":"completed","encrypted_content":"","summary":[]}`)
	item, _ = sjson.SetBytes(item, "id", st.ItemIDs[index])
	if signature := interactionsReasoningEncryptedContent(st.ReasoningEncrypted[index]); signature != "" {
		item, _ = sjson.SetBytes(item, "encrypted_content", signature)
	}
	part := []byte(`{"type":"summary_text","text":""}`)
	part, _ = sjson.SetBytes(part, "text", strings.Join(st.ReasoningSummaries[index], ""))
	item = translatorcommon.SetRawArrayItems(item, "summary", [][]byte{part})
	return item
}

func setResponsesUsageFromInteractions(out []byte, path string, usage gjson.Result) []byte {
	var inputTokens int64
	var outputTokens int64
	var totalTokens int64
	if usage.Exists() {
		if v, ok := firstUsageInt(usage, "input_tokens", "total_input_tokens"); ok {
			inputTokens = v
		}
		if v, ok := firstUsageInt(usage, "output_tokens", "total_output_tokens"); ok {
			outputTokens = v
		}
		if v, ok := firstUsageInt(usage, "total_tokens"); ok {
			totalTokens = v
		} else {
			totalTokens = inputTokens + outputTokens
		}
	}
	out, _ = sjson.SetBytes(out, path+".input_tokens", inputTokens)
	out, _ = sjson.SetBytes(out, path+".output_tokens", outputTokens)
	out, _ = sjson.SetBytes(out, path+".total_tokens", totalTokens)
	if usage.Exists() {
		if v, ok := firstUsageInt(usage, "cached_tokens", "total_cached_tokens"); ok {
			out, _ = sjson.SetBytes(out, path+".input_tokens_details.cached_tokens", v)
		}
		if v, ok := firstUsageInt(usage, "reasoning_tokens", "total_thought_tokens"); ok {
			out, _ = sjson.SetBytes(out, path+".output_tokens_details.reasoning_tokens", v)
		}
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
	if status := root.Get("status").String(); status != "" {
		out, _ = sjson.SetBytes(out, "status", status)
	}
	out, _ = sjson.SetBytes(out, "id", root.Get("id").String())
	out, _ = sjson.SetBytes(out, "model", responseModel(modelName, root))
	forAntigravity := isAntigravityModel(modelName)
	var stepItems [][]byte
	root.Get("output").ForEach(func(_, item gjson.Result) bool {
		if step, ok := openAIResponsesOutputItemToInteractionsStep(item, forAntigravity); ok {
			stepItems = append(stepItems, step)
		}
		return true
	})
	if len(stepItems) > 0 {
		out, _ = sjson.SetRawBytes(out, "steps", translatorcommon.JoinRawArray(stepItems))
	}
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
	case "response.completed", "response.incomplete":
		return openAIResponsesCompletedToInteractions(modelName, root.Get("response"), st)
	}
	return nil
}

func openAIResponsesOutputItemToInteractionsStep(item gjson.Result, forAntigravity bool) ([]byte, bool) {
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
		return responsesFunctionCallToInteractions(item, forAntigravity), true
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
		name := item.Get("name").String()
		if isAntigravityModel(modelName) {
			name = translatorcommon.AntigravityToolNameToUpstream(name)
		}
		step, _ = sjson.SetBytes(step, "name", name)
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
	payload, _ = translatorcommon.SetStringWithoutHTMLEscape(payload, "delta.arguments", arguments)
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
	if status := response.Get("status").String(); status != "" {
		payload, _ = sjson.SetBytes(payload, "interaction.status", status)
	}
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
	name := item.Get("name").String()
	if isAntigravityModel(modelName) {
		name = translatorcommon.AntigravityToolNameToUpstream(name)
	}
	step, _ = sjson.SetBytes(step, "name", name)
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

// Interactions uses qualified names directly, rather than Gemini's sanitized names.
func interactionsToolIdentityMap(rawJSON []byte, forAntigravity bool) map[string]util.ResponsesToolIdentity {
	root := gjson.ParseBytes(rawJSON)
	if request := root.Get("request"); request.Exists() {
		root = request
	}
	identities := make(map[string]util.ResponsesToolIdentity)
	for name, descriptor := range util.CollectResponsesToolWinners(root) {
		identity := util.ResponsesToolIdentity{Name: descriptor.LocalName, Namespace: descriptor.Namespace, Custom: descriptor.ToolType == "custom", ApplyPatch: applypatch.IsCustomTool(descriptor.Tool)}
		if forAntigravity {
			name = translatorcommon.AntigravityToolNameToUpstream(name)
		}
		identities[name] = identity
	}
	return identities
}

func interactionsPatchFailure(st *interactionsToResponsesStreamState, err error) [][]byte {
	if st.Terminal {
		return nil
	}
	st.SetToolInputError(err)
	st.Terminal = true
	return [][]byte{emitResponsesEvent("response.failed", translatorcommon.ApplyPatchFailure(st.ID, nextResponsesSeq(st)))}
}

func interactionsPatchDelta(st *interactionsToResponsesStreamState, call *interactionsFunctionCallState, delta string) [][]byte {
	if delta == "" {
		return nil
	}
	return [][]byte{emitResponsesEvent("response.custom_tool_call_input.delta", translatorcommon.ApplyPatchInputDelta(call.PatchCall, delta, nextResponsesSeq(st)))}
}

// Reconcile every supplied index and ID before snapshot types are inspected.
func interactionsResolveStepIndex(explicitIndex, step gjson.Result, fallback int, st *interactionsToResponsesStreamState) (int, error) {
	stepIndex := step.Get("index")
	index, indexed := fallback, explicitIndex.Exists() || stepIndex.Exists()
	if explicitIndex.Exists() {
		index = int(explicitIndex.Int())
	} else if stepIndex.Exists() {
		index = int(stepIndex.Int())
	}
	itemID, callID := step.Get("id").String(), step.Get("call_id").String()
	matched := make(map[int]bool)
	if itemID != "" {
		if callIndex, exists := st.ItemIdentityIndexes[itemID]; exists {
			matched[callIndex] = true
		}
	}
	if callID != "" {
		if callIndex, exists := st.CallIdentityIndexes[callID]; exists {
			matched[callIndex] = true
		}
	}
	if !indexed && len(matched) > 0 {
		// The fallback array position is not evidence when a supplied alias matches.
		first := true
		for callIndex := range matched {
			if first || callIndex < index {
				index = callIndex
				first = false
			}
		}
	}
	for callIndex, call := range st.FunctionCalls {
		if (itemID != "" && (call.ItemIDSeen || call.Added) && itemID == call.ID) || (callID != "" && (call.CallIDSeen || call.Added) && callID == call.CallID) {
			if !indexed && (len(matched) == 0 || callIndex < index) {
				index = callIndex
			}
			matched[callIndex] = true
		}
	}
	if !indexed && len(matched) == 0 {
		found := false
		for itemIndex, id := range st.ItemIDs {
			if itemID != "" && itemID == id && (!found || itemIndex < index) {
				index, found = itemIndex, true
			}
		}
		// A final array position must not rebind an unrelated item with a different supplied ID.
		if !found && (itemID != "" || callID != "") {
			for st.FunctionCalls[index] != nil || st.ItemTypes[index] != "" {
				index++
			}
		}
	}
	related := map[int]bool{index: true}
	if explicitIndex.Exists() {
		related[int(explicitIndex.Int())] = true
	}
	if stepIndex.Exists() {
		related[int(stepIndex.Int())] = true
	}
	conflict := len(matched) > 1 || (explicitIndex.Exists() && stepIndex.Exists() && explicitIndex.Int() != stepIndex.Int())
	for callIndex := range matched {
		related[callIndex] = true
		if (explicitIndex.Exists() && int(explicitIndex.Int()) != callIndex) || (stepIndex.Exists() && int(stepIndex.Int()) != callIndex) {
			conflict = true
		}
	}
	patchRelated := st.ToolIdentityMap[step.Get("name").String()].ApplyPatch
	for callIndex := range related {
		if call := st.FunctionCalls[callIndex]; call != nil {
			patchRelated = patchRelated || call.PatchCall != nil || st.ToolIdentityMap[call.RawName].ApplyPatch
			if (itemID != "" && call.ItemIDSeen && itemID != call.ID) || (callID != "" && call.CallIDSeen && callID != call.CallID) {
				conflict = true
			}
		}
	}
	if conflict {
		errIdentity := fmt.Errorf("conflicting Interactions apply_patch step identity")
		if st.PendingIdentityErrors == nil {
			st.PendingIdentityErrors = make(map[int]error)
		}
		// Retain both sides even when an unnamed non-function snapshot is skipped.
		for callIndex := range related {
			if st.PendingIdentityErrors[callIndex] == nil {
				st.PendingIdentityErrors[callIndex] = errIdentity
			}
			if call := st.FunctionCalls[callIndex]; call != nil && call.PendingError == nil {
				call.PendingError = errIdentity
			}
		}
	}
	// Keep unmatched aliases even on conflicts or partial invalid snapshots, so
	// later provenance through any supplied key cannot erase the contradiction.
	if st.ItemIdentityIndexes == nil {
		st.ItemIdentityIndexes = make(map[string]int)
	}
	if st.CallIdentityIndexes == nil {
		st.CallIdentityIndexes = make(map[string]int)
	}
	if itemID != "" {
		if _, exists := st.ItemIdentityIndexes[itemID]; !exists {
			st.ItemIdentityIndexes[itemID] = index
		}
	}
	if callID != "" {
		if _, exists := st.CallIdentityIndexes[callID]; !exists {
			st.CallIdentityIndexes[callID] = index
		}
	}
	if patchRelated {
		for callIndex := range related {
			if errIdentity := st.PendingIdentityErrors[callIndex]; errIdentity != nil {
				return index, errIdentity
			}
		}
	}
	// Ordinary functions retain their explicit-index behavior; only patch identities fail closed.
	return index, nil
}

func interactionsHasPatchBridge(st *interactionsToResponsesStreamState) bool {
	for _, identity := range st.ToolIdentityMap {
		if identity.ApplyPatch {
			return true
		}
	}
	return false
}

// Keep evidence even before the upstream name identifies the winning declaration.
func interactionsUpdateFunctionCall(index int, step gjson.Result, st *interactionsToResponsesStreamState, initial bool) [][]byte {
	call := st.FunctionCalls[index]
	if call == nil {
		call = &interactionsFunctionCallState{}
		st.FunctionCalls[index] = call
	}
	recordError := func(err error) {
		if call.PendingError == nil {
			call.PendingError = err
		}
	}
	if step.Get("type").Exists() && step.Get("type").String() != "function_call" {
		recordError(fmt.Errorf("conflicting apply_patch item type"))
	}
	mergeID := func(target *string, seen *bool, value string) {
		if value == "" {
			return
		}
		if (*seen || (call.Added && !st.ToolIdentityMap[call.RawName].ApplyPatch)) && *target != value {
			recordError(fmt.Errorf("conflicting apply_patch call identity"))
			return
		}
		*target = value
		*seen = true
	}
	mergeID(&call.ID, &call.ItemIDSeen, step.Get("id").String())
	mergeID(&call.CallID, &call.CallIDSeen, step.Get("call_id").String())
	if name := step.Get("name").String(); name != "" {
		if call.RawName != "" && call.RawName != name {
			recordError(fmt.Errorf("conflicting apply_patch call name"))
		} else {
			call.RawName = name
		}
	}
	if call.PendingError != nil && st.ToolIdentityMap[step.Get("name").String()].ApplyPatch {
		return interactionsPatchFailure(st, call.PendingError)
	}
	if args := step.Get("arguments"); args.Exists() && !(initial && !call.HasSnapshot && call.Arguments.Len() == 0 && !call.ItemDoneEmitted && strings.TrimSpace(jsonStringValue(args, "")) == "{}") {
		arguments := jsonStringValue(args, "{}")
		// A later complete snapshot is not a new prefix for already buffered fragments.
		if !call.Added && call.InitialArguments == "" && call.Arguments.Len() == 0 {
			call.InitialArguments = arguments
		}
		var snapshot translatorcommon.ApplyPatchCallState
		_, input, errFinishArguments := snapshot.FinishArguments(arguments)
		if errFinishArguments != nil {
			recordError(errFinishArguments)
		} else {
			if call.HasSnapshot && input != call.SnapshotInput {
				recordError(fmt.Errorf("conflicting apply_patch full snapshots"))
			}
			if call.PatchCall != nil && call.ItemDoneEmitted && input != call.PatchCall.Decoder.Input() {
				recordError(fmt.Errorf("apply_patch snapshot conflicts with completed input"))
			}
			call.HasSnapshot = true
			call.SnapshotInput = input
			call.SnapshotArguments = arguments
		}
	}
	st.ItemIDs[index] = call.ID
	st.ItemTypes[index] = "function_call"
	if call.RawName == "" {
		return nil
	}
	identity, known := st.ToolIdentityMap[call.RawName]
	if known {
		call.Name = identity.Name
		call.Namespace = identity.Namespace
		call.IsCustom = identity.Custom
	} else {
		call.Name = call.RawName
		if st.ForAntigravity {
			call.Name = translatorcommon.AntigravityUpstreamToolNameToClient(call.RawName)
		}
	}
	if identity.ApplyPatch {
		if st.PendingEnvelopeError != nil {
			return interactionsPatchFailure(st, st.PendingEnvelopeError)
		}
		if call.PendingError != nil {
			return interactionsPatchFailure(st, call.PendingError)
		}
		// Upstream evidence and downstream readiness are independent. A first late
		// ID may still be adopted; no provisional patch identity has escaped.
		if !(call.ItemIDSeen && call.CallIDSeen) && !call.IdentityFinalized {
			return nil
		}
	} else {
		call.ArgumentFragments = nil
		if !call.ItemIDSeen && !call.Added {
			call.ID = firstNonEmpty(call.CallID, fmt.Sprintf("item_%d", index))
		}
		if !call.CallIDSeen && !call.Added {
			call.CallID = call.ID
		}
	}
	st.ItemIDs[index] = call.ID
	var events [][]byte
	// Replay buffered ordinary arguments only when the item is first announced.
	announced := !call.Added
	if announced {
		if !identity.ApplyPatch && call.InitialArguments != "" {
			fragments := call.Arguments.String()
			call.Arguments.Reset()
			call.Arguments.WriteString(call.InitialArguments)
			call.Arguments.WriteString(fragments)
		}
		itemType, inputKey := "function_call", "arguments"
		if call.IsCustom {
			itemType, inputKey = "custom_tool_call", "input"
		}
		added := []byte(`{"type":"response.output_item.added","item":{"status":"in_progress"}}`)
		added, _ = sjson.SetBytes(added, "sequence_number", nextResponsesSeq(st))
		added, _ = sjson.SetBytes(added, "output_index", index)
		added, _ = sjson.SetBytes(added, "item.type", itemType)
		added, _ = sjson.SetBytes(added, "item."+inputKey, "")
		added, _ = sjson.SetBytes(added, "item.id", call.ID)
		added, _ = sjson.SetBytes(added, "item.call_id", call.CallID)
		added = translatorcommon.SetResponsesToolCallIdentity(added, call.Name, call.Namespace, "item")
		events = append(events, emitResponsesEvent("response.output_item.added", added))
		call.Added = true
	}
	if identity.ApplyPatch && call.PatchCall == nil {
		call.PatchCall = &translatorcommon.ApplyPatchCallState{ItemID: call.ID, CallID: call.CallID, Name: call.Name, Namespace: call.Namespace, OutputIndex: index}
		for _, fragment := range call.ArgumentFragments {
			delta, errPushArguments := call.PatchCall.PushArguments(fragment)
			if errPushArguments != nil {
				return append(events, interactionsPatchFailure(st, errPushArguments)...)
			}
			events = append(events, interactionsPatchDelta(st, call, delta)...)
		}
		call.ArgumentFragments = nil
	} else if !call.IsCustom && announced && call.Arguments.Len() > 0 {
		events = append(events, responsesFunctionCallArgumentsDeltaToResponses(index, call.ID, call.Arguments.String(), st))
	}
	if call.StopPending && call.PatchCall != nil {
		call.StopPending = false
		events = append(events, interactionsStepStopToResponses(gjson.Parse(fmt.Sprintf(`{"index":%d}`, index)), st)...)
	}
	return events
}

func interactionsFinishPatchCalls(st *interactionsToResponsesStreamState) [][]byte {
	if interactionsHasPatchBridge(st) {
		for _, call := range st.FunctionCalls {
			if call.RawName == "" {
				return interactionsPatchFailure(st, fmt.Errorf("unresolved Interactions apply_patch call identity"))
			}
		}
	}
	var indexes []int
	for index, call := range st.FunctionCalls {
		if st.ToolIdentityMap[call.RawName].ApplyPatch && !call.ItemDoneEmitted {
			indexes = append(indexes, index)
		}
	}
	sort.Ints(indexes)
	var events [][]byte
	for _, index := range indexes {
		call := st.FunctionCalls[index]
		if call.PatchCall == nil {
			// Interactions already maps an absent call_id to id (and an absent id
			// to call_id/item_<index>). Freeze that compatibility mapping only at
			// the response terminal, after all supplied snapshot IDs were reconciled.
			if !call.ItemIDSeen {
				call.ID = firstNonEmpty(call.CallID, fmt.Sprintf("item_%d", index))
			}
			if !call.CallIDSeen {
				call.CallID = call.ID
			}
			call.IdentityFinalized = true
			events = append(events, interactionsUpdateFunctionCall(index, gjson.Result{}, st, false)...)
			if st.Terminal {
				break
			}
		}
		root := gjson.Parse(fmt.Sprintf(`{"index":%d}`, index))
		events = append(events, interactionsStepStopToResponses(root, st)...)
		if st.Terminal {
			break
		}
	}
	return events
}

// FinalizeToolInput rejects a patch-enabled stream lacking its source terminator.
func (st *interactionsToResponsesStreamState) FinalizeToolInput() [][]byte {
	if st.ToolInputError() != nil || st.Terminal {
		return nil
	}
	enabled := false
	enabled = interactionsHasPatchBridge(st)
	if !enabled {
		return nil
	}
	st.SetToolInputError(fmt.Errorf("upstream apply_patch stream ended before protocol completion"))
	st.Terminal = true
	st.Seq++
	return [][]byte{emitResponsesEvent("response.failed", translatorcommon.ApplyPatchFailure(st.ID, st.Seq))}
}
