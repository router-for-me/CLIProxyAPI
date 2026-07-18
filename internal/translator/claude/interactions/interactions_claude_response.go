package interactions

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var claudeInteractionsDataTag = []byte("data:")

type claudeToInteractionsStreamState struct {
	ID                 string
	Model              string
	Created            bool
	StatusUpdated      bool
	Completed          bool
	Done               bool
	UsageRaw           []byte
	StepIndex          int
	ActiveStepIndex    int
	ActiveStepType     string
	ActiveStepOpen     bool
	CurrentStepByIndex map[int]string
	ToolNames          map[int]string
	ToolIDs            map[int]string
	ToolArgs           map[int]*strings.Builder
}

func ConvertClaudeResponseToInteractions(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	_ = ctx
	_ = originalRequestRawJSON
	_ = requestRawJSON
	if param == nil {
		var local any
		param = &local
	}
	if *param == nil {
		*param = &claudeToInteractionsStreamState{Model: modelName}
	}
	st := (*param).(*claudeToInteractionsStreamState)
	st.Model = firstNonEmptyString(st.Model, modelName)
	st.ensureMaps()
	return convertClaudeEventToInteractions(modelName, rawJSON, st)
}

func ConvertClaudeResponseToInteractionsNonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	_ = ctx
	_ = originalRequestRawJSON
	_ = requestRawJSON
	root := gjson.ParseBytes(rawJSON)
	if root.Exists() && root.Get("content").Exists() {
		return convertClaudeMessageToInteractions(modelName, root)
	}
	return convertClaudeSSEToInteractionsNonStream(modelName, rawJSON)
}

func convertClaudeMessageToInteractions(modelName string, root gjson.Result) []byte {
	out := []byte(`{"id":"","object":"interaction","status":"completed","model":"","steps":[]}`)
	out, _ = sjson.SetBytes(out, "id", firstNonEmptyString(root.Get("id").String(), fmt.Sprintf("interaction_%d", time.Now().UnixNano())))
	out, _ = sjson.SetBytes(out, "model", firstNonEmptyString(root.Get("model").String(), modelName))
	root.Get("content").ForEach(func(_, part gjson.Result) bool {
		if step := claudeContentBlockToInteractionsStep(part); len(step) > 0 {
			out, _ = sjson.SetRawBytes(out, "steps.-1", step)
		}
		return true
	})
	out = setInteractionsUsageFromClaude(out, "usage", root.Get("usage"))
	return out
}

func convertClaudeSSEToInteractionsNonStream(modelName string, rawJSON []byte) []byte {
	out := []byte(`{"id":"","object":"interaction","status":"completed","model":"","steps":[]}`)
	out, _ = sjson.SetBytes(out, "id", fmt.Sprintf("interaction_%d", time.Now().UnixNano()))
	out, _ = sjson.SetBytes(out, "model", modelName)
	st := &claudeToInteractionsStreamState{Model: modelName}
	st.ensureMaps()
	scanner := bufio.NewScanner(bytes.NewReader(rawJSON))
	buffer := make([]byte, 1024*1024)
	scanner.Buffer(buffer, 52_428_800)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if !bytes.HasPrefix(line, claudeInteractionsDataTag) {
			continue
		}
		payload := bytes.TrimSpace(line[len(claudeInteractionsDataTag):])
		if bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		root := gjson.ParseBytes(payload)
		switch root.Get("type").String() {
		case "message_start":
			msg := root.Get("message")
			if id := msg.Get("id").String(); id != "" {
				out, _ = sjson.SetBytes(out, "id", id)
			}
			if model := msg.Get("model").String(); model != "" {
				out, _ = sjson.SetBytes(out, "model", model)
			}
			mergeClaudeUsage(st, msg.Get("usage"))
		case "content_block_start":
			claudeNonStreamContentBlockStart(root, st)
		case "content_block_delta":
			claudeNonStreamContentBlockDelta(root, st)
		case "content_block_stop":
			if step := claudeNonStreamContentBlockStop(root, st); len(step) > 0 {
				out, _ = sjson.SetRawBytes(out, "steps.-1", step)
			}
		case "message_delta":
			mergeClaudeUsage(st, root.Get("usage"))
		}
	}
	out = setInteractionsUsageFromClaude(out, "usage", claudeMergedUsage(st))
	return out
}

func convertClaudeEventToInteractions(modelName string, rawJSON []byte, st *claudeToInteractionsStreamState) [][]byte {
	payload := claudeInteractionsSSEPayload(rawJSON)
	if len(payload) == 0 {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		return appendClaudeInteractionsDone(nil, st)
	}
	root := gjson.ParseBytes(payload)
	switch root.Get("type").String() {
	case "message_start":
		msg := root.Get("message")
		st.ID = firstNonEmptyString(msg.Get("id").String(), st.ID, fmt.Sprintf("interaction_%d", time.Now().UnixNano()))
		st.Model = firstNonEmptyString(msg.Get("model").String(), st.Model, modelName)
		mergeClaudeUsage(st, msg.Get("usage"))
		return appendClaudeInteractionsCreated(nil, st, st.Model)
	case "content_block_start":
		return claudeContentBlockStartToInteractions(modelName, root, st)
	case "content_block_delta":
		return claudeContentBlockDeltaToInteractions(modelName, root, st)
	case "content_block_stop":
		return claudeContentBlockStopToInteractions(root, st)
	case "message_delta":
		mergeClaudeUsage(st, root.Get("usage"))
		out := appendClaudeInteractionsStepStop(nil, st)
		out = appendClaudeInteractionsCompleted(out, st, modelName, root)
		return out
	case "message_stop":
		if st.Completed {
			return nil
		}
		return appendClaudeInteractionsCompleted(nil, st, modelName, root)
	case "error":
		out := appendClaudeInteractionsCreated(nil, st, modelName)
		return appendClaudeInteractionsCompleted(out, st, modelName, root)
	}
	return nil
}

func claudeContentBlockStartToInteractions(modelName string, root gjson.Result, st *claudeToInteractionsStreamState) [][]byte {
	out := appendClaudeInteractionsCreated(nil, st, modelName)
	out = appendClaudeInteractionsStepStop(out, st)
	index := int(root.Get("index").Int())
	block := root.Get("content_block")
	stepType := claudeBlockInteractionsStepType(block.Get("type").String())
	st.CurrentStepByIndex[index] = stepType
	if stepType == "function_call" {
		if name := block.Get("name").String(); name != "" {
			st.ToolNames[index] = name
		}
		if id := block.Get("id").String(); id != "" {
			st.ToolIDs[index] = id
		}
		if input := block.Get("input"); input.Exists() && input.IsObject() && input.Raw != "{}" {
			builder := &strings.Builder{}
			builder.WriteString(input.Raw)
			st.ToolArgs[index] = builder
		}
	}
	step := claudeBlockToInteractionsStep(block, stepType)
	return appendClaudeInteractionsStepStart(out, st, stepType, step)
}

func claudeContentBlockDeltaToInteractions(modelName string, root gjson.Result, st *claudeToInteractionsStreamState) [][]byte {
	index := int(root.Get("index").Int())
	stepType := st.CurrentStepByIndex[index]
	if stepType == "" {
		stepType = claudeDeltaInteractionsStepType(root.Get("delta.type").String())
		out := appendClaudeInteractionsCreated(nil, st, modelName)
		out = appendClaudeInteractionsStepStop(out, st)
		out = appendClaudeInteractionsStepStart(out, st, stepType, []byte(`{"type":"`+stepType+`"}`))
		st.CurrentStepByIndex[index] = stepType
		return appendClaudeDeltaToInteractions(out, st, root.Get("delta"), index)
	}
	if !st.ActiveStepOpen || st.ActiveStepIndex != index {
		out := appendClaudeInteractionsCreated(nil, st, modelName)
		out = appendClaudeInteractionsStepStop(out, st)
		step := claudeStepForKnownIndex(stepType, index, st)
		out = appendClaudeInteractionsStepStart(out, st, stepType, step)
		return appendClaudeDeltaToInteractions(out, st, root.Get("delta"), index)
	}
	return appendClaudeDeltaToInteractions(nil, st, root.Get("delta"), index)
}

func claudeContentBlockStopToInteractions(root gjson.Result, st *claudeToInteractionsStreamState) [][]byte {
	index := int(root.Get("index").Int())
	out := appendClaudeInteractionsStepStop(nil, st)
	delete(st.CurrentStepByIndex, index)
	delete(st.ToolNames, index)
	delete(st.ToolIDs, index)
	delete(st.ToolArgs, index)
	return out
}

func appendClaudeDeltaToInteractions(out [][]byte, st *claudeToInteractionsStreamState, delta gjson.Result, index int) [][]byte {
	switch delta.Get("type").String() {
	case "text_delta":
		return appendClaudeInteractionsTextDelta(out, st, delta.Get("text").String(), false)
	case "thinking_delta":
		return appendClaudeInteractionsTextDelta(out, st, delta.Get("thinking").String(), true)
	case "input_json_delta":
		if st.ToolArgs[index] == nil {
			st.ToolArgs[index] = &strings.Builder{}
		}
		partial := delta.Get("partial_json").String()
		st.ToolArgs[index].WriteString(partial)
		return appendClaudeInteractionsArgumentsDelta(out, st, partial)
	}
	return out
}

func claudeContentBlockToInteractionsStep(part gjson.Result) []byte {
	switch part.Get("type").String() {
	case "text":
		step := []byte(`{"type":"model_output","content":[]}`)
		content := []byte(`{"type":"text","text":""}`)
		content, _ = sjson.SetBytes(content, "text", part.Get("text").String())
		step, _ = sjson.SetRawBytes(step, "content.-1", content)
		return step
	case "thinking":
		step := []byte(`{"type":"thought","content":[]}`)
		content := []byte(`{"type":"text","text":""}`)
		content, _ = sjson.SetBytes(content, "text", part.Get("thinking").String())
		step, _ = sjson.SetRawBytes(step, "content.-1", content)
		return step
	case "tool_use":
		return claudeToolUseToInteractionsStep(part, strings.TrimSpace(part.Get("input").Raw))
	}
	return nil
}

func claudeToolUseToInteractionsStep(part gjson.Result, argsRaw string) []byte {
	step := []byte(`{"type":"function_call","name":"","arguments":{}}`)
	step, _ = sjson.SetBytes(step, "name", part.Get("name").String())
	if id := part.Get("id").String(); id != "" {
		step, _ = sjson.SetBytes(step, "id", id)
		step, _ = sjson.SetBytes(step, "call_id", id)
	}
	if argsRaw != "" && gjson.Valid(argsRaw) {
		step, _ = sjson.SetRawBytes(step, "arguments", []byte(argsRaw))
	}
	return step
}

func claudeBlockToInteractionsStep(block gjson.Result, stepType string) []byte {
	step := []byte(`{"type":""}`)
	step, _ = sjson.SetBytes(step, "type", stepType)
	if stepType == "function_call" {
		step, _ = sjson.SetBytes(step, "name", block.Get("name").String())
		if id := block.Get("id").String(); id != "" {
			step, _ = sjson.SetBytes(step, "id", id)
			step, _ = sjson.SetBytes(step, "call_id", id)
		}
		step, _ = sjson.SetRawBytes(step, "arguments", []byte(`{}`))
	}
	return step
}

func claudeStepForKnownIndex(stepType string, index int, st *claudeToInteractionsStreamState) []byte {
	step := []byte(`{"type":""}`)
	step, _ = sjson.SetBytes(step, "type", stepType)
	if stepType == "function_call" {
		step, _ = sjson.SetBytes(step, "name", st.ToolNames[index])
		if id := st.ToolIDs[index]; id != "" {
			step, _ = sjson.SetBytes(step, "id", id)
			step, _ = sjson.SetBytes(step, "call_id", id)
		}
		step, _ = sjson.SetRawBytes(step, "arguments", []byte(`{}`))
	}
	return step
}

func claudeNonStreamContentBlockStart(root gjson.Result, st *claudeToInteractionsStreamState) {
	index := int(root.Get("index").Int())
	block := root.Get("content_block")
	st.CurrentStepByIndex[index] = claudeBlockInteractionsStepType(block.Get("type").String())
	if block.Get("type").String() != "tool_use" {
		return
	}
	st.ToolNames[index] = block.Get("name").String()
	st.ToolIDs[index] = block.Get("id").String()
	if input := block.Get("input"); input.Exists() && input.IsObject() && input.Raw != "{}" {
		builder := &strings.Builder{}
		builder.WriteString(input.Raw)
		st.ToolArgs[index] = builder
	}
}

func claudeNonStreamContentBlockDelta(root gjson.Result, st *claudeToInteractionsStreamState) {
	index := int(root.Get("index").Int())
	delta := root.Get("delta")
	switch delta.Get("type").String() {
	case "text_delta", "thinking_delta":
		if st.ToolArgs[index] == nil {
			st.ToolArgs[index] = &strings.Builder{}
		}
		if delta.Get("type").String() == "text_delta" {
			st.ToolArgs[index].WriteString(delta.Get("text").String())
		} else {
			st.ToolArgs[index].WriteString(delta.Get("thinking").String())
		}
	case "input_json_delta":
		if st.ToolArgs[index] == nil {
			st.ToolArgs[index] = &strings.Builder{}
		}
		st.ToolArgs[index].WriteString(delta.Get("partial_json").String())
	}
}

func claudeNonStreamContentBlockStop(root gjson.Result, st *claudeToInteractionsStreamState) []byte {
	index := int(root.Get("index").Int())
	stepType := st.CurrentStepByIndex[index]
	builder := st.ToolArgs[index]
	text := ""
	if builder != nil {
		text = builder.String()
	}
	var step []byte
	switch stepType {
	case "thought":
		step = []byte(`{"type":"thought","content":[]}`)
		content := []byte(`{"type":"text","text":""}`)
		content, _ = sjson.SetBytes(content, "text", text)
		step, _ = sjson.SetRawBytes(step, "content.-1", content)
	case "function_call":
		part := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
		part, _ = sjson.SetBytes(part, "id", st.ToolIDs[index])
		part, _ = sjson.SetBytes(part, "name", st.ToolNames[index])
		step = claudeToolUseToInteractionsStep(gjson.ParseBytes(part), strings.TrimSpace(text))
	default:
		step = []byte(`{"type":"model_output","content":[]}`)
		content := []byte(`{"type":"text","text":""}`)
		content, _ = sjson.SetBytes(content, "text", text)
		step, _ = sjson.SetRawBytes(step, "content.-1", content)
	}
	delete(st.CurrentStepByIndex, index)
	delete(st.ToolNames, index)
	delete(st.ToolIDs, index)
	delete(st.ToolArgs, index)
	return step
}

func mergeClaudeUsage(st *claudeToInteractionsStreamState, usage gjson.Result) {
	if !usage.Exists() {
		return
	}
	if len(st.UsageRaw) == 0 {
		st.UsageRaw = []byte(`{}`)
	}
	for _, key := range []string{
		"input_tokens",
		"output_tokens",
		"cache_read_input_tokens",
		"cache_creation_input_tokens",
		"thinking_tokens",
	} {
		value := usage.Get(key)
		if !value.Exists() {
			continue
		}
		st.UsageRaw, _ = sjson.SetRawBytes(st.UsageRaw, key, []byte(value.Raw))
	}
}

func claudeMergedUsage(st *claudeToInteractionsStreamState) gjson.Result {
	if len(st.UsageRaw) == 0 {
		return gjson.Result{}
	}
	return gjson.ParseBytes(st.UsageRaw)
}

func setInteractionsUsageFromClaude(out []byte, path string, usage gjson.Result) []byte {
	if !usage.Exists() {
		return out
	}
	inputTokens := usage.Get("input_tokens").Int()
	outputTokens := usage.Get("output_tokens").Int()
	cacheRead := usage.Get("cache_read_input_tokens").Int()
	cacheCreation := usage.Get("cache_creation_input_tokens").Int()
	thinkingTokens := usage.Get("thinking_tokens").Int()
	if usage.Get("input_tokens").Exists() {
		out, _ = sjson.SetBytes(out, path+".input_tokens", inputTokens)
		out, _ = sjson.SetBytes(out, path+".total_input_tokens", inputTokens)
	}
	if usage.Get("output_tokens").Exists() {
		out, _ = sjson.SetBytes(out, path+".output_tokens", outputTokens)
		out, _ = sjson.SetBytes(out, path+".total_output_tokens", outputTokens)
	}
	total := inputTokens + outputTokens
	if usage.Get("input_tokens").Exists() || usage.Get("output_tokens").Exists() {
		out, _ = sjson.SetBytes(out, path+".total_tokens", total)
	}
	if cacheRead != 0 || cacheCreation != 0 {
		out, _ = sjson.SetBytes(out, path+".cached_tokens", cacheRead+cacheCreation)
		out, _ = sjson.SetBytes(out, path+".total_cached_tokens", cacheRead+cacheCreation)
	}
	if thinkingTokens != 0 {
		out, _ = sjson.SetBytes(out, path+".reasoning_tokens", thinkingTokens)
		out, _ = sjson.SetBytes(out, path+".total_thought_tokens", thinkingTokens)
	}
	return out
}

func appendClaudeInteractionsCreated(out [][]byte, st *claudeToInteractionsStreamState, modelName string) [][]byte {
	if st.Created {
		return out
	}
	st.ID = firstNonEmptyString(st.ID, fmt.Sprintf("interaction_%d", time.Now().UnixNano()))
	created := []byte(`{"interaction":{"id":"","status":"in_progress","object":"interaction","model":""},"event_type":"interaction.created"}`)
	created, _ = sjson.SetBytes(created, "interaction.id", st.ID)
	created, _ = sjson.SetBytes(created, "interaction.model", firstNonEmptyString(st.Model, modelName))
	out = append(out, translatorcommon.SSEEventData("interaction.created", created))
	st.Created = true
	return appendClaudeInteractionsStatusUpdate(out, st)
}

func appendClaudeInteractionsStatusUpdate(out [][]byte, st *claudeToInteractionsStreamState) [][]byte {
	if st.StatusUpdated {
		return out
	}
	statusUpdate := []byte(`{"interaction_id":"","status":"in_progress","event_type":"interaction.status_update"}`)
	statusUpdate, _ = sjson.SetBytes(statusUpdate, "interaction_id", st.ID)
	out = append(out, translatorcommon.SSEEventData("interaction.status_update", statusUpdate))
	st.StatusUpdated = true
	return out
}

func appendClaudeInteractionsStepStart(out [][]byte, st *claudeToInteractionsStreamState, stepType string, step []byte) [][]byte {
	st.ActiveStepIndex = st.StepIndex
	st.ActiveStepType = stepType
	st.ActiveStepOpen = true
	payload := []byte(`{"index":0,"step":{"type":""},"event_type":"step.start"}`)
	payload, _ = sjson.SetBytes(payload, "index", st.ActiveStepIndex)
	if len(step) > 0 && gjson.ValidBytes(step) {
		payload, _ = sjson.SetRawBytes(payload, "step", step)
	} else {
		payload, _ = sjson.SetBytes(payload, "step.type", stepType)
	}
	return append(out, translatorcommon.SSEEventData("step.start", payload))
}

func appendClaudeInteractionsTextDelta(out [][]byte, st *claudeToInteractionsStreamState, text string, thought bool) [][]byte {
	payload := []byte(`{"index":0,"delta":{"text":"","type":"text"},"event_type":"step.delta"}`)
	payload, _ = sjson.SetBytes(payload, "index", st.ActiveStepIndex)
	if thought {
		payload, _ = sjson.SetBytes(payload, "delta.type", "thought_summary")
		payload, _ = sjson.SetBytes(payload, "delta.content.type", "text")
		payload, _ = sjson.SetBytes(payload, "delta.content.text", text)
		payload, _ = sjson.DeleteBytes(payload, "delta.text")
	} else {
		payload, _ = sjson.SetBytes(payload, "delta.text", text)
	}
	return append(out, translatorcommon.SSEEventData("step.delta", payload))
}

func appendClaudeInteractionsArgumentsDelta(out [][]byte, st *claudeToInteractionsStreamState, arguments string) [][]byte {
	payload := []byte(`{"index":0,"delta":{"arguments":"","type":"arguments_delta"},"event_type":"step.delta"}`)
	payload, _ = sjson.SetBytes(payload, "index", st.ActiveStepIndex)
	payload, _ = sjson.SetBytes(payload, "delta.arguments", arguments)
	return append(out, translatorcommon.SSEEventData("step.delta", payload))
}

func appendClaudeInteractionsStepStop(out [][]byte, st *claudeToInteractionsStreamState) [][]byte {
	if !st.ActiveStepOpen {
		return out
	}
	payload := []byte(`{"index":0,"event_type":"step.stop"}`)
	payload, _ = sjson.SetBytes(payload, "index", st.ActiveStepIndex)
	out = append(out, translatorcommon.SSEEventData("step.stop", payload))
	st.ActiveStepOpen = false
	st.ActiveStepType = ""
	st.StepIndex++
	return out
}

func appendClaudeInteractionsCompleted(out [][]byte, st *claudeToInteractionsStreamState, modelName string, root gjson.Result) [][]byte {
	if st.Completed {
		return out
	}
	out = appendClaudeInteractionsCreated(out, st, modelName)
	now := time.Now().UTC().Format(time.RFC3339)
	completed := []byte(`{"interaction":{"id":"","status":"completed","usage":{},"created":"","updated":"","service_tier":"standard","object":"interaction","model":""},"event_type":"interaction.completed"}`)
	completed, _ = sjson.SetBytes(completed, "interaction.id", st.ID)
	completed, _ = sjson.SetBytes(completed, "interaction.created", now)
	completed, _ = sjson.SetBytes(completed, "interaction.updated", now)
	completed, _ = sjson.SetBytes(completed, "interaction.model", firstNonEmptyString(st.Model, modelName))
	usage := claudeMergedUsage(st)
	if !usage.Exists() {
		usage = root.Get("usage")
	}
	completed = setInteractionsUsageFromClaude(completed, "interaction.usage", usage)
	out = append(out, translatorcommon.SSEEventData("interaction.completed", completed))
	st.Completed = true
	return out
}

func appendClaudeInteractionsDone(out [][]byte, st *claudeToInteractionsStreamState) [][]byte {
	if st.Done {
		return out
	}
	out = append(out, translatorcommon.SSEEventData("done", []byte("[DONE]")))
	st.Done = true
	return out
}

func claudeInteractionsSSEPayload(rawJSON []byte) []byte {
	rawJSON = bytes.TrimSpace(rawJSON)
	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		return rawJSON
	}
	if !bytes.HasPrefix(rawJSON, claudeInteractionsDataTag) {
		return nil
	}
	return bytes.TrimSpace(rawJSON[len(claudeInteractionsDataTag):])
}

func claudeBlockInteractionsStepType(blockType string) string {
	switch blockType {
	case "thinking":
		return "thought"
	case "tool_use":
		return "function_call"
	default:
		return "model_output"
	}
}

func claudeDeltaInteractionsStepType(deltaType string) string {
	switch deltaType {
	case "thinking_delta":
		return "thought"
	case "input_json_delta":
		return "function_call"
	default:
		return "model_output"
	}
}

func (st *claudeToInteractionsStreamState) ensureMaps() {
	if st.CurrentStepByIndex == nil {
		st.CurrentStepByIndex = make(map[int]string)
	}
	if st.ToolNames == nil {
		st.ToolNames = make(map[int]string)
	}
	if st.ToolIDs == nil {
		st.ToolIDs = make(map[int]string)
	}
	if st.ToolArgs == nil {
		st.ToolArgs = make(map[int]*strings.Builder)
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
