package chat_completions

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

type interactionsToOpenAIChatStreamState struct {
	ID              string
	Model           string
	Created         int64
	Started         bool
	Completed       bool
	SawToolCall     bool
	StepTypes       map[int]string
	ToolIDs         map[int]string
	ToolNames       map[int]string
	ToolArguments   map[int]*strings.Builder
	TextByStepIndex map[int]*strings.Builder
}

func ConvertInteractionsResponseToOpenAI(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	_ = ctx
	_ = originalRequestRawJSON
	_ = requestRawJSON
	if param == nil {
		var local any
		param = &local
	}
	if *param == nil {
		*param = &interactionsToOpenAIChatStreamState{Model: modelName}
	}
	st := (*param).(*interactionsToOpenAIChatStreamState)
	st.Model = firstNonEmpty(st.Model, modelName)
	st.ensureMaps()
	return convertInteractionsEventToOpenAIChat(modelName, rawJSON, st)
}

func ConvertInteractionsResponseToOpenAINonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	_ = ctx
	_ = originalRequestRawJSON
	_ = requestRawJSON
	root := gjson.ParseBytes(rawJSON)
	interaction := root
	if nested := root.Get("interaction"); nested.Exists() {
		interaction = nested
	}
	out := []byte(`{"id":"","object":"chat.completion","created":0,"model":"","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`)
	out, _ = sjson.SetBytes(out, "id", firstNonEmpty(interaction.Get("id").String(), root.Get("id").String(), fmt.Sprintf("chatcmpl_%d", time.Now().UnixNano())))
	out, _ = sjson.SetBytes(out, "created", time.Now().Unix())
	out, _ = sjson.SetBytes(out, "model", firstNonEmpty(interaction.Get("model").String(), modelName))
	steps := interaction.Get("steps")
	if !steps.Exists() {
		steps = root.Get("steps")
	}
	var textBuilder strings.Builder
	var reasoningBuilder strings.Builder
	sawToolCall := false
	steps.ForEach(func(_, step gjson.Result) bool {
		switch step.Get("type").String() {
		case "model_output":
			for _, text := range interactionsContentTextsForOpenAIChat(step.Get("content")) {
				textBuilder.WriteString(text)
			}
		case "thought":
			for _, text := range interactionsContentTextsForOpenAIChat(step.Get("content")) {
				reasoningBuilder.WriteString(text)
			}
		case "function_call":
			sawToolCall = true
			out, _ = sjson.SetRawBytes(out, "choices.0.message.tool_calls.-1", openAIChatToolCallFromInteractions(step, gjson.Result{}))
		}
		return true
	})
	if textBuilder.Len() > 0 {
		out, _ = sjson.SetBytes(out, "choices.0.message.content", textBuilder.String())
	}
	if reasoningBuilder.Len() > 0 {
		out, _ = sjson.SetBytes(out, "choices.0.message.reasoning_content", reasoningBuilder.String())
	}
	if sawToolCall {
		out, _ = sjson.SetBytes(out, "choices.0.message.content", nil)
		out, _ = sjson.SetBytes(out, "choices.0.finish_reason", "tool_calls")
	}
	out = setOpenAIChatUsageFromInteractions(out, "usage", translatorcommon.InteractionsUsage(root))
	return out
}

func convertInteractionsEventToOpenAIChat(modelName string, rawJSON []byte, st *interactionsToOpenAIChatStreamState) [][]byte {
	payload := openAIChatInteractionsPayload(rawJSON)
	if len(payload) == 0 || bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		return nil
	}
	root := gjson.ParseBytes(payload)
	if !root.Exists() {
		return nil
	}
	switch root.Get("event_type").String() {
	case "interaction.created":
		interaction := root.Get("interaction")
		st.ID = firstNonEmpty(interaction.Get("id").String(), st.ID)
		st.Model = firstNonEmpty(interaction.Get("model").String(), st.Model, modelName)
		return ensureOpenAIChatStarted(nil, st)
	case "step.start":
		return interactionsStepStartToOpenAIChat(modelName, root, st)
	case "step.delta":
		return interactionsStepDeltaToOpenAIChat(modelName, root, st)
	case "interaction.completed", "finish":
		return appendOpenAIChatCompleted(nil, root, st)
	case "done":
		return nil
	}
	return nil
}

func interactionsStepStartToOpenAIChat(modelName string, root gjson.Result, st *interactionsToOpenAIChatStreamState) [][]byte {
	_ = modelName
	out := ensureOpenAIChatStarted(nil, st)
	index := int(root.Get("index").Int())
	step := root.Get("step")
	stepType := step.Get("type").String()
	st.StepTypes[index] = stepType
	switch stepType {
	case "function_call":
		st.SawToolCall = true
		st.ToolIDs[index] = firstNonEmpty(step.Get("call_id").String(), step.Get("id").String(), fmt.Sprintf("call_%d", index))
		st.ToolNames[index] = step.Get("name").String()
		if st.ToolArguments[index] == nil {
			st.ToolArguments[index] = &strings.Builder{}
		}
		if args := step.Get("arguments"); args.Exists() && strings.TrimSpace(args.Raw) != "{}" {
			st.ToolArguments[index].WriteString(jsonStringValue(args, "{}"))
		}
		return append(out, openAIChatToolCallStartChunk(st, index))
	default:
		return out
	}
}

func interactionsStepDeltaToOpenAIChat(modelName string, root gjson.Result, st *interactionsToOpenAIChatStreamState) [][]byte {
	_ = modelName
	index := int(root.Get("index").Int())
	delta := root.Get("delta")
	out := ensureOpenAIChatStarted(nil, st)
	switch delta.Get("type").String() {
	case "thought_summary":
		text := firstNonEmpty(delta.Get("content.text").String(), delta.Get("text").String())
		if text == "" {
			return out
		}
		return append(out, openAIChatDeltaChunk(st, "reasoning_content", text))
	case "arguments_delta":
		args := delta.Get("arguments").String()
		if st.ToolArguments[index] == nil {
			st.ToolArguments[index] = &strings.Builder{}
		}
		st.ToolArguments[index].WriteString(args)
		return append(out, openAIChatToolCallArgumentsChunk(st, index, args))
	default:
		text := delta.Get("text").String()
		if text == "" {
			return out
		}
		if st.TextByStepIndex[index] == nil {
			st.TextByStepIndex[index] = &strings.Builder{}
		}
		st.TextByStepIndex[index].WriteString(text)
		return append(out, openAIChatDeltaChunk(st, "content", text))
	}
}

func ensureOpenAIChatStarted(out [][]byte, st *interactionsToOpenAIChatStreamState) [][]byte {
	if st.Started {
		return out
	}
	chunk := openAIChatBaseChunk(st)
	chunk, _ = sjson.SetBytes(chunk, "choices.0.delta.role", "assistant")
	st.Started = true
	return append(out, chunk)
}

func appendOpenAIChatCompleted(out [][]byte, root gjson.Result, st *interactionsToOpenAIChatStreamState) [][]byte {
	if st.Completed {
		return out
	}
	out = ensureOpenAIChatStarted(out, st)
	chunk := openAIChatBaseChunk(st)
	finishReason := "stop"
	if st.SawToolCall {
		finishReason = "tool_calls"
	}
	chunk, _ = sjson.SetBytes(chunk, "choices.0.finish_reason", finishReason)
	chunk = setOpenAIChatUsageFromInteractions(chunk, "usage", translatorcommon.InteractionsUsage(root))
	st.Completed = true
	return append(out, chunk)
}

func openAIChatBaseChunk(st *interactionsToOpenAIChatStreamState) []byte {
	chunk := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{},"finish_reason":null}]}`)
	chunk, _ = sjson.SetBytes(chunk, "id", firstNonEmpty(st.ID, fmt.Sprintf("chatcmpl_%d", time.Now().UnixNano())))
	chunk, _ = sjson.SetBytes(chunk, "created", openAIChatCreated(st))
	chunk, _ = sjson.SetBytes(chunk, "model", st.Model)
	return chunk
}

func openAIChatDeltaChunk(st *interactionsToOpenAIChatStreamState, field, value string) []byte {
	chunk := openAIChatBaseChunk(st)
	chunk, _ = sjson.SetBytes(chunk, "choices.0.delta."+field, value)
	return chunk
}

func openAIChatToolCallStartChunk(st *interactionsToOpenAIChatStreamState, index int) []byte {
	chunk := openAIChatBaseChunk(st)
	toolCall := []byte(`{"index":0,"id":"","type":"function","function":{"name":"","arguments":""}}`)
	toolCall, _ = sjson.SetBytes(toolCall, "index", index)
	toolCall, _ = sjson.SetBytes(toolCall, "id", firstNonEmpty(st.ToolIDs[index], fmt.Sprintf("call_%d", index)))
	toolCall, _ = sjson.SetBytes(toolCall, "function.name", st.ToolNames[index])
	chunk, _ = sjson.SetRawBytes(chunk, "choices.0.delta.tool_calls.-1", toolCall)
	return chunk
}

func openAIChatToolCallArgumentsChunk(st *interactionsToOpenAIChatStreamState, index int, arguments string) []byte {
	chunk := openAIChatBaseChunk(st)
	toolCall := []byte(`{"index":0,"function":{"arguments":""}}`)
	toolCall, _ = sjson.SetBytes(toolCall, "index", index)
	toolCall, _ = sjson.SetBytes(toolCall, "function.arguments", arguments)
	chunk, _ = sjson.SetRawBytes(chunk, "choices.0.delta.tool_calls.-1", toolCall)
	return chunk
}

func openAIChatToolCallFromInteractions(step, fallbackArgs gjson.Result) []byte {
	toolCall := []byte(`{"id":"","type":"function","function":{"name":"","arguments":"{}"}}`)
	callID := firstNonEmpty(step.Get("call_id").String(), step.Get("id").String(), "call_0")
	toolCall, _ = sjson.SetBytes(toolCall, "id", callID)
	toolCall, _ = sjson.SetBytes(toolCall, "function.name", step.Get("name").String())
	args := step.Get("arguments")
	if !args.Exists() {
		args = fallbackArgs
	}
	toolCall, _ = sjson.SetBytes(toolCall, "function.arguments", jsonStringValue(args, "{}"))
	return toolCall
}

func setOpenAIChatUsageFromInteractions(out []byte, path string, usage gjson.Result) []byte {
	if !usage.Exists() {
		return out
	}
	if value, ok := interactionsUsageInt(usage, "input_tokens", "total_input_tokens"); ok {
		out, _ = sjson.SetBytes(out, path+".prompt_tokens", value)
	}
	if value, ok := interactionsUsageInt(usage, "output_tokens", "total_output_tokens"); ok {
		out, _ = sjson.SetBytes(out, path+".completion_tokens", value)
	}
	if value, ok := interactionsUsageInt(usage, "total_tokens"); ok {
		out, _ = sjson.SetBytes(out, path+".total_tokens", value)
	}
	if value, ok := interactionsUsageInt(usage, "cached_tokens", "total_cached_tokens"); ok {
		out, _ = sjson.SetBytes(out, path+".prompt_tokens_details.cached_tokens", value)
	}
	if value, ok := interactionsUsageInt(usage, "reasoning_tokens", "total_thought_tokens"); ok {
		out, _ = sjson.SetBytes(out, path+".completion_tokens_details.reasoning_tokens", value)
	}
	return out
}

func interactionsUsageInt(root gjson.Result, paths ...string) (int64, bool) {
	for _, path := range paths {
		if value := root.Get(path); value.Exists() {
			return value.Int(), true
		}
	}
	return 0, false
}

func interactionsContentTextsForOpenAIChat(content gjson.Result) []string {
	if !content.Exists() {
		return nil
	}
	if content.Type == gjson.String {
		return []string{content.String()}
	}
	var out []string
	content.ForEach(func(_, part gjson.Result) bool {
		if text := firstNonEmpty(part.Get("text").String(), part.Get("content.text").String()); text != "" {
			out = append(out, text)
		}
		return true
	})
	return out
}

func openAIChatInteractionsPayload(rawJSON []byte) []byte {
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

func openAIChatCreated(st *interactionsToOpenAIChatStreamState) int64 {
	if st.Created == 0 {
		st.Created = time.Now().Unix()
	}
	return st.Created
}

func (st *interactionsToOpenAIChatStreamState) ensureMaps() {
	if st.StepTypes == nil {
		st.StepTypes = make(map[int]string)
	}
	if st.ToolIDs == nil {
		st.ToolIDs = make(map[int]string)
	}
	if st.ToolNames == nil {
		st.ToolNames = make(map[int]string)
	}
	if st.ToolArguments == nil {
		st.ToolArguments = make(map[int]*strings.Builder)
	}
	if st.TextByStepIndex == nil {
		st.TextByStepIndex = make(map[int]*strings.Builder)
	}
}
