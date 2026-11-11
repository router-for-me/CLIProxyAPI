package interactions

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
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
	root := util.ParseGJSONBytesNoCopy(rawJSON)
	if root.Exists() && root.Get("content").Exists() {
		return convertClaudeMessageToInteractions(modelName, root)
	}
	return convertClaudeSSEToInteractionsNonStream(modelName, rawJSON)
}

func convertClaudeMessageToInteractions(modelName string, root ojson.Result) []byte {
	outDoc := ojson.NewDoc([]byte(`{"id":"","object":"interaction","status":"completed","model":"","steps":[]}`))
	_ = outDoc.Set("id", firstNonEmptyString(root.Get("id").String(), fmt.Sprintf("interaction_%d", time.Now().UnixNano())))
	_ = outDoc.Set("model", firstNonEmptyString(root.Get("model").String(), modelName))
	steps := make([][]byte, 0, 4)
	root.Get("content").ForEach(func(_, part ojson.Result) bool {
		if step := claudeContentBlockToInteractionsStep(part); len(step) > 0 {
			steps = append(steps, step)
		}
		return true
	})
	if len(steps) > 0 {
		_ = outDoc.SetRawBytes("steps", translatorcommon.JoinRawArray(steps))
	}
	setInteractionsUsageFromClaudeDoc(outDoc, "usage", root.Get("usage"))
	return outDoc.Bytes()
}

func convertClaudeSSEToInteractionsNonStream(modelName string, rawJSON []byte) []byte {
	outDoc := ojson.NewDoc([]byte(`{"id":"","object":"interaction","status":"completed","model":"","steps":[]}`))
	_ = outDoc.Set("id", fmt.Sprintf("interaction_%d", time.Now().UnixNano()))
	_ = outDoc.Set("model", modelName)
	st := &claudeToInteractionsStreamState{Model: modelName}
	st.ensureMaps()
	steps := make([][]byte, 0, 8)
	remaining := rawJSON
	for len(remaining) > 0 {
		var line []byte
		idx := bytes.IndexByte(remaining, '\n')
		if idx >= 0 {
			line = remaining[:idx]
			remaining = remaining[idx+1:]
		} else {
			line = remaining
			remaining = nil
		}
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, claudeInteractionsDataTag) {
			continue
		}
		payload := bytes.TrimSpace(line[len(claudeInteractionsDataTag):])
		if bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		root := util.ParseGJSONBytesNoCopy(payload)
		switch root.Get("type").String() {
		case "message_start":
			msg := root.Get("message")
			if id := msg.Get("id").String(); id != "" {
				_ = outDoc.Set("id", id)
			}
			if model := msg.Get("model").String(); model != "" {
				_ = outDoc.Set("model", model)
			}
			mergeClaudeUsage(st, msg.Get("usage"))
		case "content_block_start":
			claudeNonStreamContentBlockStart(root, st)
		case "content_block_delta":
			claudeNonStreamContentBlockDelta(root, st)
		case "content_block_stop":
			if step := claudeNonStreamContentBlockStop(root, st); len(step) > 0 {
				steps = append(steps, step)
			}
		case "message_delta":
			mergeClaudeUsage(st, root.Get("usage"))
		}
	}
	if len(steps) > 0 {
		_ = outDoc.SetRawBytes("steps", translatorcommon.JoinRawArray(steps))
	}
	setInteractionsUsageFromClaudeDoc(outDoc, "usage", claudeMergedUsage(st))
	return outDoc.Bytes()
}

func convertClaudeEventToInteractions(modelName string, rawJSON []byte, st *claudeToInteractionsStreamState) [][]byte {
	payload := claudeInteractionsSSEPayload(rawJSON)
	if len(payload) == 0 {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		return appendClaudeInteractionsDone(nil, st)
	}
	root := util.ParseGJSONBytesNoCopy(payload)
	switch root.Get("type").String() {
	case "message_start":
		msg := root.Get("message")
		st.ID = strings.Clone(firstNonEmptyString(msg.Get("id").String(), st.ID, fmt.Sprintf("interaction_%d", time.Now().UnixNano())))
		st.Model = strings.Clone(firstNonEmptyString(msg.Get("model").String(), st.Model, modelName))
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

func claudeContentBlockStartToInteractions(modelName string, root ojson.Result, st *claudeToInteractionsStreamState) [][]byte {
	out := appendClaudeInteractionsCreated(nil, st, modelName)
	out = appendClaudeInteractionsStepStop(out, st)
	index := int(root.Get("index").Int())
	block := root.Get("content_block")
	stepType := claudeBlockInteractionsStepType(block.Get("type").String())
	st.CurrentStepByIndex[index] = stepType
	if stepType == "function_call" {
		if name := block.Get("name").String(); name != "" {
			st.ToolNames[index] = strings.Clone(name)
		}
		if id := block.Get("id").String(); id != "" {
			st.ToolIDs[index] = strings.Clone(id)
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

func claudeContentBlockDeltaToInteractions(modelName string, root ojson.Result, st *claudeToInteractionsStreamState) [][]byte {
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

func claudeContentBlockStopToInteractions(root ojson.Result, st *claudeToInteractionsStreamState) [][]byte {
	index := int(root.Get("index").Int())
	out := appendClaudeInteractionsStepStop(nil, st)
	delete(st.CurrentStepByIndex, index)
	delete(st.ToolNames, index)
	delete(st.ToolIDs, index)
	delete(st.ToolArgs, index)
	return out
}

func appendClaudeDeltaToInteractions(out [][]byte, st *claudeToInteractionsStreamState, delta ojson.Result, index int) [][]byte {
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

func claudeContentBlockToInteractionsStep(part ojson.Result) []byte {
	switch part.Get("type").String() {
	case "text":
		step := []byte(`{"type":"model_output","content":[]}`)
		content := []byte(`{"type":"text","text":""}`)
		content, _ = ojson.SetBytes(content, "text", part.Get("text").String())
		return translatorcommon.SetRawArrayItems(step, "content", [][]byte{content})
	case "thinking":
		step := []byte(`{"type":"thought","content":[]}`)
		content := []byte(`{"type":"text","text":""}`)
		content, _ = ojson.SetBytes(content, "text", part.Get("thinking").String())
		return translatorcommon.SetRawArrayItems(step, "content", [][]byte{content})
	case "tool_use":
		return claudeToolUseToInteractionsStep(part, strings.TrimSpace(part.Get("input").Raw))
	}
	return nil
}

func claudeToolUseToInteractionsStep(part ojson.Result, argsRaw string) []byte {
	stepDoc := ojson.NewDoc([]byte(`{"type":"function_call","name":"","arguments":{}}`))
	_ = stepDoc.Set("name", part.Get("name").String())
	if id := part.Get("id").String(); id != "" {
		_ = stepDoc.Set("id", id)
		_ = stepDoc.Set("call_id", id)
	}
	if argsRaw != "" && ojson.Valid(argsRaw) {
		_ = stepDoc.SetRaw("arguments", argsRaw)
	}
	return stepDoc.Bytes()
}

func claudeBlockToInteractionsStep(block ojson.Result, stepType string) []byte {
	stepDoc := ojson.NewDoc([]byte(`{"type":""}`))
	_ = stepDoc.Set("type", stepType)
	if stepType == "function_call" {
		_ = stepDoc.Set("name", block.Get("name").String())
		if id := block.Get("id").String(); id != "" {
			_ = stepDoc.Set("id", id)
			_ = stepDoc.Set("call_id", id)
		}
		_ = stepDoc.SetRaw("arguments", `{}`)
	}
	return stepDoc.Bytes()
}

func claudeStepForKnownIndex(stepType string, index int, st *claudeToInteractionsStreamState) []byte {
	stepDoc := ojson.NewDoc([]byte(`{"type":""}`))
	_ = stepDoc.Set("type", stepType)
	if stepType == "function_call" {
		_ = stepDoc.Set("name", st.ToolNames[index])
		if id := st.ToolIDs[index]; id != "" {
			_ = stepDoc.Set("id", id)
			_ = stepDoc.Set("call_id", id)
		}
		_ = stepDoc.SetRaw("arguments", `{}`)
	}
	return stepDoc.Bytes()
}

func claudeNonStreamContentBlockStart(root ojson.Result, st *claudeToInteractionsStreamState) {
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

func claudeNonStreamContentBlockDelta(root ojson.Result, st *claudeToInteractionsStreamState) {
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

func claudeNonStreamContentBlockStop(root ojson.Result, st *claudeToInteractionsStreamState) []byte {
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
		content, _ = ojson.SetBytes(content, "text", text)
		step = translatorcommon.SetRawArrayItems(step, "content", [][]byte{content})
	case "function_call":
		partDoc := ojson.NewDoc([]byte(`{"type":"tool_use","id":"","name":"","input":{}}`))
		_ = partDoc.Set("id", st.ToolIDs[index])
		_ = partDoc.Set("name", st.ToolNames[index])
		step = claudeToolUseToInteractionsStep(util.ParseGJSONBytesNoCopy(partDoc.Bytes()), strings.TrimSpace(text))
	default:
		step = []byte(`{"type":"model_output","content":[]}`)
		content := []byte(`{"type":"text","text":""}`)
		content, _ = ojson.SetBytes(content, "text", text)
		step = translatorcommon.SetRawArrayItems(step, "content", [][]byte{content})
	}
	delete(st.CurrentStepByIndex, index)
	delete(st.ToolNames, index)
	delete(st.ToolIDs, index)
	delete(st.ToolArgs, index)
	return step
}

func mergeClaudeUsage(st *claudeToInteractionsStreamState, usage ojson.Result) {
	if !usage.Exists() {
		return
	}
	if len(st.UsageRaw) == 0 {
		st.UsageRaw = []byte(`{}`)
	}
	usageDoc := ojson.NewDoc(st.UsageRaw)
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
		_ = usageDoc.SetRaw(key, value.Raw)
	}
	st.UsageRaw = usageDoc.Bytes()
}

func claudeMergedUsage(st *claudeToInteractionsStreamState) ojson.Result {
	if len(st.UsageRaw) == 0 {
		return ojson.Result{}
	}
	return util.ParseGJSONBytesNoCopy(st.UsageRaw)
}

func setInteractionsUsageFromClaudeDoc(outDoc *ojson.Doc, path string, usage ojson.Result) {
	if !usage.Exists() {
		return
	}
	inputTokens := usage.Get("input_tokens").Int()
	outputTokens := usage.Get("output_tokens").Int()
	cacheRead := usage.Get("cache_read_input_tokens").Int()
	cacheCreation := usage.Get("cache_creation_input_tokens").Int()
	thinkingTokens := usage.Get("thinking_tokens").Int()
	if usage.Get("input_tokens").Exists() {
		_ = outDoc.Set(path+".input_tokens", inputTokens)
		_ = outDoc.Set(path+".total_input_tokens", inputTokens)
	}
	if usage.Get("output_tokens").Exists() {
		_ = outDoc.Set(path+".output_tokens", outputTokens)
		_ = outDoc.Set(path+".total_output_tokens", outputTokens)
	}
	total := inputTokens + outputTokens
	if usage.Get("input_tokens").Exists() || usage.Get("output_tokens").Exists() {
		_ = outDoc.Set(path+".total_tokens", total)
	}
	if cacheRead != 0 || cacheCreation != 0 {
		_ = outDoc.Set(path+".cached_tokens", cacheRead+cacheCreation)
		_ = outDoc.Set(path+".total_cached_tokens", cacheRead+cacheCreation)
	}
	if thinkingTokens != 0 {
		_ = outDoc.Set(path+".reasoning_tokens", thinkingTokens)
		_ = outDoc.Set(path+".total_thought_tokens", thinkingTokens)
	}
}

func appendClaudeInteractionsCreated(out [][]byte, st *claudeToInteractionsStreamState, modelName string) [][]byte {
	if st.Created {
		return out
	}
	st.ID = firstNonEmptyString(st.ID, fmt.Sprintf("interaction_%d", time.Now().UnixNano()))
	createdDoc := ojson.NewDoc([]byte(`{"interaction":{"id":"","status":"in_progress","object":"interaction","model":""},"event_type":"interaction.created"}`))
	_ = createdDoc.Set("interaction.id", st.ID)
	_ = createdDoc.Set("interaction.model", firstNonEmptyString(st.Model, modelName))
	out = append(out, translatorcommon.SSEEventData("interaction.created", createdDoc.Bytes()))
	st.Created = true
	return appendClaudeInteractionsStatusUpdate(out, st)
}

func appendClaudeInteractionsStatusUpdate(out [][]byte, st *claudeToInteractionsStreamState) [][]byte {
	if st.StatusUpdated {
		return out
	}
	statusUpdate := []byte(`{"interaction_id":"","status":"in_progress","event_type":"interaction.status_update"}`)
	statusUpdate, _ = ojson.SetBytes(statusUpdate, "interaction_id", st.ID)
	out = append(out, translatorcommon.SSEEventData("interaction.status_update", statusUpdate))
	st.StatusUpdated = true
	return out
}

func appendClaudeInteractionsStepStart(out [][]byte, st *claudeToInteractionsStreamState, stepType string, step []byte) [][]byte {
	st.ActiveStepIndex = st.StepIndex
	st.ActiveStepType = stepType
	st.ActiveStepOpen = true
	payloadDoc := ojson.NewDoc([]byte(`{"index":0,"step":{"type":""},"event_type":"step.start"}`))
	_ = payloadDoc.Set("index", st.ActiveStepIndex)
	if len(step) > 0 && ojson.ValidBytes(step) {
		_ = payloadDoc.SetRawBytes("step", step)
	} else {
		_ = payloadDoc.Set("step.type", stepType)
	}
	return append(out, translatorcommon.SSEEventData("step.start", payloadDoc.Bytes()))
}

func appendClaudeInteractionsTextDelta(out [][]byte, st *claudeToInteractionsStreamState, text string, thought bool) [][]byte {
	payloadDoc := ojson.NewDoc([]byte(`{"index":0,"delta":{"text":"","type":"text"},"event_type":"step.delta"}`))
	_ = payloadDoc.Set("index", st.ActiveStepIndex)
	if thought {
		_ = payloadDoc.Set("delta.type", "thought_summary")
		_ = payloadDoc.Set("delta.content.type", "text")
		_ = payloadDoc.Set("delta.content.text", text)
		_ = payloadDoc.Delete("delta.text")
	} else {
		_ = payloadDoc.Set("delta.text", text)
	}
	return append(out, translatorcommon.SSEEventData("step.delta", payloadDoc.Bytes()))
}

func appendClaudeInteractionsArgumentsDelta(out [][]byte, st *claudeToInteractionsStreamState, arguments string) [][]byte {
	payloadDoc := ojson.NewDoc([]byte(`{"index":0,"delta":{"arguments":"","type":"arguments_delta"},"event_type":"step.delta"}`))
	_ = payloadDoc.Set("index", st.ActiveStepIndex)
	_ = payloadDoc.Set("delta.arguments", arguments)
	return append(out, translatorcommon.SSEEventData("step.delta", payloadDoc.Bytes()))
}

func appendClaudeInteractionsStepStop(out [][]byte, st *claudeToInteractionsStreamState) [][]byte {
	if !st.ActiveStepOpen {
		return out
	}
	payload := []byte(`{"index":0,"event_type":"step.stop"}`)
	payload, _ = ojson.SetBytes(payload, "index", st.ActiveStepIndex)
	out = append(out, translatorcommon.SSEEventData("step.stop", payload))
	st.ActiveStepOpen = false
	st.ActiveStepType = ""
	st.StepIndex++
	return out
}

func appendClaudeInteractionsCompleted(out [][]byte, st *claudeToInteractionsStreamState, modelName string, root ojson.Result) [][]byte {
	if st.Completed {
		return out
	}
	out = appendClaudeInteractionsCreated(out, st, modelName)
	now := time.Now().UTC().Format(time.RFC3339)
	completedDoc := ojson.NewDoc([]byte(`{"interaction":{"id":"","status":"completed","usage":{},"created":"","updated":"","service_tier":"standard","object":"interaction","model":""},"event_type":"interaction.completed"}`))
	_ = completedDoc.Set("interaction.id", st.ID)
	_ = completedDoc.Set("interaction.created", now)
	_ = completedDoc.Set("interaction.updated", now)
	_ = completedDoc.Set("interaction.model", firstNonEmptyString(st.Model, modelName))
	usage := claudeMergedUsage(st)
	if !usage.Exists() {
		usage = root.Get("usage")
	}
	setInteractionsUsageFromClaudeDoc(completedDoc, "interaction.usage", usage)
	out = append(out, translatorcommon.SSEEventData("interaction.completed", completedDoc.Bytes()))
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
