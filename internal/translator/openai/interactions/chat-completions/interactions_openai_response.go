package chat_completions

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

type openAIToInteractionsStreamState struct {
	Created         bool
	StatusUpdated   bool
	Completed       bool
	Done            bool
	CurrentStepType string
	CurrentStepID   string
	ToolCallIDs     map[int]string
	ToolCallNames   map[int]string
	ID              string
	StepIndex       int
	ActiveStepIndex int
	ActiveStepOpen  bool
	Usage           ojson.Result
}

func ConvertOpenAIResponseToInteractions(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	_ = ctx
	_ = originalRequestRawJSON
	_ = requestRawJSON
	if param == nil {
		var local any
		param = &local
	}
	if *param == nil {
		*param = &openAIToInteractionsStreamState{}
	}
	st := (*param).(*openAIToInteractionsStreamState)
	if st.ToolCallIDs == nil {
		st.ToolCallIDs = make(map[int]string)
	}
	if st.ToolCallNames == nil {
		st.ToolCallNames = make(map[int]string)
	}
	return convertOpenAIChatStreamToInteractions(modelName, rawJSON, st)
}

func ConvertOpenAIResponseToInteractionsNonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	_ = ctx
	_ = originalRequestRawJSON
	_ = requestRawJSON
	root := util.ParseGJSONBytesNoCopy(rawJSON)
	outDoc := ojson.NewDocString(`{"id":"","status":"completed","object":"interaction","model":"","steps":[]}`)
	_ = outDoc.Set("id", firstNonEmpty(root.Get("id").String(), fmt.Sprintf("interaction_%d", time.Now().UnixNano())))
	_ = outDoc.Set("model", firstNonEmpty(modelName, root.Get("model").String()))
	choices := root.Get("choices")
	var steps [][]byte
	choices.ForEach(func(_, choice ojson.Result) bool {
		message := choice.Get("message")
		if reasoning := message.Get("reasoning_content"); reasoning.Exists() {
			for _, text := range openAIReasoningTexts(reasoning) {
				steps = append(steps, interactionsTextStep("thought", text))
			}
		}
		if content := message.Get("content"); content.Exists() && content.String() != "" {
			steps = append(steps, interactionsTextStep("model_output", content.String()))
		}
		if toolCalls := message.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
			forAntigravity := isAntigravityModel(modelName)
			toolCalls.ForEach(func(_, toolCall ojson.Result) bool {
				if step, ok := openAIToolCallToInteractionsStep(toolCall, forAntigravity); ok {
					steps = append(steps, step)
				}
				return true
			})
		}
		if finishReason := choice.Get("finish_reason"); finishReason.Exists() {
			_ = outDoc.Set("finish_reason", finishReason.String())
		}
		return true
	})
	if len(steps) > 0 {
		translatorcommon.SetRawArrayItemsDoc(outDoc, "steps", steps)
	}
	setInteractionsUsageFromOpenAIChatDoc(outDoc, "usage", root.Get("usage"))
	return outDoc.Bytes()
}

func convertOpenAIChatStreamToInteractions(modelName string, rawJSON []byte, st *openAIToInteractionsStreamState) [][]byte {
	payload := openAIChatSSEPayload(rawJSON)
	if len(payload) == 0 {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		out := make([][]byte, 0, 3)
		out = appendInteractionsStepStop(out, st)
		if !st.Completed {
			out = appendInteractionsCompleted(out, st, modelName, ojson.Result{})
		}
		return appendInteractionsDone(out, st)
	}
	root := util.ParseGJSONBytesNoCopy(payload)
	if !root.Exists() {
		return nil
	}
	if usage := root.Get("usage"); usage.Exists() {
		st.Usage = ojson.Parse(strings.Clone(usage.Raw))
	}
	out := make([][]byte, 0)
	if choices := root.Get("choices"); choices.Exists() && choices.IsArray() {
		if len(choices.Array()) == 0 {
			if root.Get("usage").Exists() {
				out = appendInteractionsStepStop(out, st)
				out = appendInteractionsCompleted(out, st, modelName, root)
			}
			return out
		}
		choices.ForEach(func(_, choice ojson.Result) bool {
			delta := choice.Get("delta")
			if reasoning := delta.Get("reasoning_content"); reasoning.Exists() {
				for _, text := range openAIReasoningTexts(reasoning) {
					out = ensureInteractionsStep(out, st, modelName, "thought", root)
					out = appendInteractionsTextDelta(out, st, text, true)
				}
			}
			if content := delta.Get("content"); content.Exists() && content.String() != "" {
				out = ensureInteractionsStep(out, st, modelName, "model_output", root)
				out = appendInteractionsTextDelta(out, st, content.String(), false)
			}
			if toolCalls := delta.Get("tool_calls"); toolCalls.Exists() && toolCalls.IsArray() {
				toolCalls.ForEach(func(_, toolCall ojson.Result) bool {
					out = appendOpenAIToolCallDelta(out, st, modelName, root, toolCall)
					return true
				})
			}
			if finishReason := choice.Get("finish_reason"); finishReason.Exists() {
				out = appendInteractionsStepStop(out, st)
			}
			return true
		})
	}
	return out
}

func appendOpenAIToolCallDelta(out [][]byte, st *openAIToInteractionsStreamState, modelName string, root, toolCall ojson.Result) [][]byte {
	index := int(toolCall.Get("index").Int())
	if id := toolCall.Get("id").String(); id != "" {
		st.ToolCallIDs[index] = strings.Clone(id)
	}
	function := toolCall.Get("function")
	if name := function.Get("name").String(); name != "" {
		if isAntigravityModel(modelName) {
			name = translatorcommon.AntigravityToolNameToUpstream(name)
		}
		st.ToolCallNames[index] = strings.Clone(name)
	}
	stepID := firstNonEmpty(st.ToolCallIDs[index], fmt.Sprintf("call_%d", index))
	stepName := st.ToolCallNames[index]
	if st.CurrentStepType != "function_call" || st.CurrentStepID != stepID {
		out = appendInteractionsStepStop(out, st)
		stepDoc := ojson.NewDocString(`{"type":"function_call","id":"","name":"","arguments":{}}`)
		_ = stepDoc.Set("id", stepID)
		_ = stepDoc.Set("name", stepName)
		out = appendInteractionsCreated(out, st, modelName, root)
		out = appendInteractionsStepStart(out, st, "function_call", util.ParseGJSONBytesNoCopy(stepDoc.Bytes()))
	}
	if args := function.Get("arguments"); args.Exists() && args.String() != "" {
		out = appendInteractionsArgumentsDelta(out, st, args.String())
	}
	return out
}

func appendInteractionsCreated(out [][]byte, st *openAIToInteractionsStreamState, modelName string, root ojson.Result) [][]byte {
	if st.Created {
		return out
	}
	st.ID = strings.Clone(firstNonEmpty(root.Get("id").String(), st.ID, fmt.Sprintf("interaction_%d", time.Now().UnixNano())))
	createdDoc := ojson.NewDocString(`{"interaction":{"id":"","status":"in_progress","object":"interaction","model":""},"event_type":"interaction.created"}`)
	_ = createdDoc.Set("interaction.id", st.ID)
	_ = createdDoc.Set("interaction.model", firstNonEmpty(modelName, root.Get("model").String()))
	out = append(out, translatorcommon.SSEEventData("interaction.created", createdDoc.Bytes()))
	st.Created = true
	return appendInteractionsStatusUpdate(out, st)
}

func appendInteractionsStatusUpdate(out [][]byte, st *openAIToInteractionsStreamState) [][]byte {
	if st.StatusUpdated {
		return out
	}
	statusUpdate := []byte(`{"interaction_id":"","status":"in_progress","event_type":"interaction.status_update"}`)
	statusUpdate, _ = ojson.SetBytes(statusUpdate, "interaction_id", st.ID)
	out = append(out, translatorcommon.SSEEventData("interaction.status_update", statusUpdate))
	st.StatusUpdated = true
	return out
}

func ensureInteractionsStep(out [][]byte, st *openAIToInteractionsStreamState, modelName, stepType string, step ojson.Result) [][]byte {
	out = appendInteractionsCreated(out, st, modelName, step)
	if st.ActiveStepOpen && st.CurrentStepType == stepType {
		return out
	}
	out = appendInteractionsStepStop(out, st)
	return appendInteractionsStepStart(out, st, stepType, step)
}

func appendInteractionsStepStart(out [][]byte, st *openAIToInteractionsStreamState, stepType string, step ojson.Result) [][]byte {
	index := st.StepIndex
	st.StepIndex++
	st.ActiveStepIndex = index
	st.CurrentStepType = stepType
	st.ActiveStepOpen = true
	payloadDoc := ojson.NewDocString(`{"index":0,"step":{"type":""},"event_type":"step.start"}`)
	_ = payloadDoc.Set("index", index)
	_ = payloadDoc.Set("step.type", stepType)
	if stepType == "function_call" {
		id := firstNonEmpty(step.Get("id").String(), step.Get("call_id").String(), st.CurrentStepID)
		st.CurrentStepID = strings.Clone(id)
		if id != "" {
			_ = payloadDoc.Set("step.id", id)
		}
		_ = payloadDoc.Set("step.name", step.Get("name").String())
		_ = payloadDoc.SetRaw("step.arguments", `{}`)
	} else {
		st.CurrentStepID = ""
	}
	return append(out, translatorcommon.SSEEventData("step.start", payloadDoc.Bytes()))
}

func appendInteractionsTextDelta(out [][]byte, st *openAIToInteractionsStreamState, text string, thought bool) [][]byte {
	if thought {
		payloadDoc := ojson.NewDocString(`{"index":0,"delta":{"content":{"text":"","type":"text"},"type":"thought_summary"},"event_type":"step.delta"}`)
		_ = payloadDoc.Set("index", st.ActiveStepIndex)
		_ = payloadDoc.Set("delta.content.text", text)
		return append(out, translatorcommon.SSEEventData("step.delta", payloadDoc.Bytes()))
	}
	payloadDoc := ojson.NewDocString(`{"index":0,"delta":{"text":"","type":"text"},"event_type":"step.delta"}`)
	_ = payloadDoc.Set("index", st.ActiveStepIndex)
	_ = payloadDoc.Set("delta.text", text)
	return append(out, translatorcommon.SSEEventData("step.delta", payloadDoc.Bytes()))
}

func appendInteractionsArgumentsDelta(out [][]byte, st *openAIToInteractionsStreamState, arguments string) [][]byte {
	payloadDoc := ojson.NewDocString(`{"index":0,"delta":{"arguments":"","type":"arguments_delta"},"event_type":"step.delta"}`)
	_ = payloadDoc.Set("index", st.ActiveStepIndex)
	_ = payloadDoc.Set("delta.arguments", arguments)
	return append(out, translatorcommon.SSEEventData("step.delta", payloadDoc.Bytes()))
}

func appendInteractionsStepStop(out [][]byte, st *openAIToInteractionsStreamState) [][]byte {
	if !st.ActiveStepOpen {
		return out
	}
	payload := []byte(`{"index":0,"event_type":"step.stop"}`)
	payload, _ = ojson.SetBytes(payload, "index", st.ActiveStepIndex)
	out = append(out, translatorcommon.SSEEventData("step.stop", payload))
	st.ActiveStepOpen = false
	st.CurrentStepType = ""
	st.CurrentStepID = ""
	return out
}

func appendInteractionsCompleted(out [][]byte, st *openAIToInteractionsStreamState, modelName string, root ojson.Result) [][]byte {
	if st.Completed {
		return out
	}
	if !st.Created {
		out = appendInteractionsCreated(out, st, modelName, root)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	payloadDoc := ojson.NewDocString(`{"interaction":{"id":"","status":"completed","usage":{},"created":"","updated":"","service_tier":"standard","object":"interaction","model":""},"event_type":"interaction.completed"}`)
	_ = payloadDoc.Set("interaction.id", st.ID)
	_ = payloadDoc.Set("interaction.created", now)
	_ = payloadDoc.Set("interaction.updated", now)
	_ = payloadDoc.Set("interaction.model", firstNonEmpty(modelName, root.Get("model").String()))
	usage := root.Get("usage")
	if !usage.Exists() {
		usage = st.Usage
	}
	setInteractionsUsageFromOpenAIChatDoc(payloadDoc, "interaction.usage", usage)
	out = append(out, translatorcommon.SSEEventData("interaction.completed", payloadDoc.Bytes()))
	st.Completed = true
	return out
}

func appendInteractionsDone(out [][]byte, st *openAIToInteractionsStreamState) [][]byte {
	if st.Done {
		return out
	}
	out = append(out, translatorcommon.SSEEventData("done", []byte("[DONE]")))
	st.Done = true
	return out
}

func isOpenAIStreamDone(rawJSON []byte) bool {
	return bytes.Equal(bytes.TrimSpace(openAIChatSSEPayload(rawJSON)), []byte("[DONE]"))
}

func openAIChatSSEPayload(rawJSON []byte) []byte {
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

func interactionsTextStep(stepType, text string) []byte {
	stepDoc := ojson.NewDocString(`{"type":"","content":[{"type":"text","text":""}]}`)
	_ = stepDoc.Set("type", stepType)
	_ = stepDoc.Set("content.0.text", text)
	return stepDoc.Bytes()
}

func openAIToolCallToInteractionsStep(toolCall ojson.Result, forAntigravity bool) ([]byte, bool) {
	if toolType := toolCall.Get("type").String(); toolType != "" && toolType != "function" {
		return nil, false
	}
	function := toolCall.Get("function")
	if !function.Exists() {
		return nil, false
	}
	stepDoc := ojson.NewDocString(`{"type":"function_call","name":"","arguments":{}}`)
	if id := toolCall.Get("id").String(); id != "" {
		_ = stepDoc.Set("id", id)
	}
	name := function.Get("name").String()
	if forAntigravity {
		name = translatorcommon.AntigravityToolNameToUpstream(name)
	}
	_ = stepDoc.Set("name", name)
	setRawJSONValueDoc(stepDoc, "arguments", function.Get("arguments"), `{}`)
	return stepDoc.Bytes(), true
}

func setInteractionsUsageFromOpenAIChatDoc(outDoc *ojson.Doc, path string, usage ojson.Result) {
	if !usage.Exists() {
		return
	}
	if value := usage.Get("prompt_tokens"); value.Exists() {
		_ = outDoc.Set(path+".input_tokens", value.Int())
		_ = outDoc.Set(path+".total_input_tokens", value.Int())
	}
	if value := usage.Get("completion_tokens"); value.Exists() {
		_ = outDoc.Set(path+".output_tokens", value.Int())
		_ = outDoc.Set(path+".total_output_tokens", value.Int())
	}
	if value := usage.Get("total_tokens"); value.Exists() {
		_ = outDoc.Set(path+".total_tokens", value.Int())
	}
	if value := usage.Get("prompt_tokens_details.cached_tokens"); value.Exists() {
		_ = outDoc.Set(path+".cached_tokens", value.Int())
		_ = outDoc.Set(path+".total_cached_tokens", value.Int())
	}
	if value := usage.Get("completion_tokens_details.reasoning_tokens"); value.Exists() {
		_ = outDoc.Set(path+".reasoning_tokens", value.Int())
		_ = outDoc.Set(path+".total_thought_tokens", value.Int())
	}
}

func openAIReasoningTexts(reasoning ojson.Result) []string {
	if reasoning.Type == ojson.String {
		if reasoning.String() == "" {
			return nil
		}
		return []string{reasoning.String()}
	}
	texts := make([]string, 0)
	if reasoning.IsArray() {
		reasoning.ForEach(func(_, item ojson.Result) bool {
			if text := firstNonEmpty(item.Get("text").String(), item.Get("content").String()); text != "" {
				texts = append(texts, text)
			}
			return true
		})
	}
	return texts
}

func setRawJSONValueDoc(outDoc *ojson.Doc, path string, value ojson.Result, fallback string) {
	if !value.Exists() {
		_ = outDoc.SetRaw(path, fallback)
		return
	}
	raw := strings.TrimSpace(value.String())
	if value.Type == ojson.String && ojson.Valid(raw) {
		_ = outDoc.SetRaw(path, raw)
		return
	}
	if value.Type == ojson.String {
		_ = outDoc.Set(path, value.String())
		return
	}
	_ = outDoc.SetRaw(path, value.Raw)
}
