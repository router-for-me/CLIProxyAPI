package claude

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

type interactionsToClaudeStreamState struct {
	ID              string
	Model           string
	Started         bool
	ActiveBlock     bool
	ActiveBlockType string
	BlockIndex      int
	SawToolCall     bool
	Completed       bool
	Stopped         bool
	Done            bool
	StepTypes       map[int]string
	ToolNames       map[int]string
	ToolIDs         map[int]string
	ToolSignatures  map[int]string
}

func ConvertInteractionsResponseToClaude(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	_ = originalRequestRawJSON
	_ = requestRawJSON
	if param == nil {
		var local any
		param = &local
	}
	if *param == nil {
		*param = &interactionsToClaudeStreamState{Model: modelName}
	}
	st := (*param).(*interactionsToClaudeStreamState)
	st.Model = firstNonEmpty(st.Model, modelName)
	st.ensureMaps()
	return convertInteractionsEventToClaude(modelName, rawJSON, st)
}

func ConvertInteractionsResponseToClaudeNonStream(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	_ = originalRequestRawJSON
	_ = requestRawJSON
	root := gjson.ParseBytes(rawJSON)
	interaction := root
	if nested := root.Get("interaction"); nested.Exists() {
		interaction = nested
	}
	out := []byte(`{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}`)
	out, _ = sjson.SetBytes(out, "id", firstNonEmpty(interaction.Get("id").String(), root.Get("id").String(), fmt.Sprintf("msg_%d", time.Now().UnixNano())))
	out, _ = sjson.SetBytes(out, "model", firstNonEmpty(interaction.Get("model").String(), modelName))
	steps := interaction.Get("steps")
	if !steps.Exists() {
		steps = root.Get("steps")
	}
	sawToolCall := false
	steps.ForEach(func(_, step gjson.Result) bool {
		switch step.Get("type").String() {
		case "thought":
			for _, text := range interactionsContentTexts(step.Get("content")) {
				block := []byte(`{"type":"thinking","thinking":""}`)
				block, _ = sjson.SetBytes(block, "thinking", text)
				out, _ = sjson.SetRawBytes(out, "content.-1", block)
			}
		case "function_call":
			sawToolCall = true
			block := []byte(`{"type":"tool_use","id":"","name":"","input":{}}`)
			block, _ = sjson.SetBytes(block, "id", interactionsToolID(step))
			block, _ = sjson.SetBytes(block, "name", step.Get("name").String())
			if signature := interactionsSignature(step); signature != "" {
				block, _ = sjson.SetBytes(block, "signature", signature)
			}
			args := firstExisting(step, "arguments", "args")
			if args.Exists() && args.IsObject() {
				block, _ = sjson.SetRawBytes(block, "input", []byte(args.Raw))
			}
			out, _ = sjson.SetRawBytes(out, "content.-1", block)
		default:
			for _, text := range interactionsContentTexts(step.Get("content")) {
				block := []byte(`{"type":"text","text":""}`)
				block, _ = sjson.SetBytes(block, "text", text)
				out, _ = sjson.SetRawBytes(out, "content.-1", block)
			}
		}
		return true
	})
	if sawToolCall {
		out, _ = sjson.SetBytes(out, "stop_reason", "tool_use")
	}
	out = setClaudeUsageFromInteractions(out, "usage", translatorcommon.InteractionsUsage(root))
	return out
}

func convertInteractionsEventToClaude(modelName string, rawJSON []byte, st *interactionsToClaudeStreamState) [][]byte {
	payload := interactionsSSEPayload(rawJSON)
	if len(payload) == 0 {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		return appendClaudeMessageStop(nil, st)
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
		return appendClaudeMessageStart(nil, st)
	case "step.start":
		return interactionsStepStartToClaude(modelName, root, st)
	case "step.delta":
		return interactionsStepDeltaToClaude(modelName, root, st)
	case "step.stop":
		return appendClaudeContentBlockStop(nil, st)
	case "interaction.completed", "finish":
		return appendClaudeMessageDelta(nil, root, st)
	case "done":
		return appendClaudeMessageStop(nil, st)
	}
	return nil
}

func interactionsStepStartToClaude(modelName string, root gjson.Result, st *interactionsToClaudeStreamState) [][]byte {
	out := appendClaudeMessageStart(nil, st)
	out = appendClaudeContentBlockStop(out, st)
	index := int(root.Get("index").Int())
	step := root.Get("step")
	stepType := step.Get("type").String()
	st.StepTypes[index] = stepType
	switch stepType {
	case "function_call":
		st.SawToolCall = true
		st.ToolNames[index] = step.Get("name").String()
		st.ToolIDs[index] = interactionsToolID(step)
		st.ToolSignatures[index] = interactionsSignature(step)
		return appendClaudeToolBlockStart(out, index, st)
	case "thought":
		return appendClaudeContentBlockStart(out, "thinking", st)
	default:
		_ = modelName
		return appendClaudeContentBlockStart(out, "text", st)
	}
}

func interactionsStepDeltaToClaude(modelName string, root gjson.Result, st *interactionsToClaudeStreamState) [][]byte {
	index := int(root.Get("index").Int())
	delta := root.Get("delta")
	switch delta.Get("type").String() {
	case "thought_summary":
		out := appendClaudeMessageStart(nil, st)
		out = ensureClaudeContentBlock(out, "thinking", st)
		text := firstNonEmpty(delta.Get("content.text").String(), delta.Get("text").String())
		return appendClaudeContentDelta(out, "thinking_delta", "thinking", text, st)
	case "thought_signature":
		if st.ActiveBlock && st.ActiveBlockType == "thinking" {
			return appendClaudeContentDelta(nil, "signature_delta", "signature", delta.Get("signature").String(), st)
		}
	case "arguments_delta":
		out := appendClaudeMessageStart(nil, st)
		if !st.ActiveBlock || st.ActiveBlockType != "tool_use" {
			out = appendClaudeContentBlockStop(out, st)
			if st.ToolNames[index] == "" {
				st.ToolNames[index] = root.Get("step.name").String()
			}
			if st.ToolIDs[index] == "" {
				st.ToolIDs[index] = fmt.Sprintf("toolu_%d", index)
			}
			out = appendClaudeToolBlockStart(out, index, st)
		}
		return appendClaudeContentDelta(out, "input_json_delta", "partial_json", delta.Get("arguments").String(), st)
	default:
		_ = modelName
		out := appendClaudeMessageStart(nil, st)
		out = ensureClaudeContentBlock(out, "text", st)
		return appendClaudeContentDelta(out, "text_delta", "text", delta.Get("text").String(), st)
	}
	return nil
}

func appendClaudeMessageStart(out [][]byte, st *interactionsToClaudeStreamState) [][]byte {
	if st.Started {
		return out
	}
	msg := []byte(`{"type":"message_start","message":{"id":"","type":"message","role":"assistant","content":[],"model":"","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`)
	msg, _ = sjson.SetBytes(msg, "message.id", firstNonEmpty(st.ID, fmt.Sprintf("msg_%d", time.Now().UnixNano())))
	msg, _ = sjson.SetBytes(msg, "message.model", st.Model)
	st.Started = true
	return append(out, translatorcommon.AppendSSEEventBytes(nil, "message_start", msg, 3))
}

func appendClaudeContentBlockStart(out [][]byte, blockType string, st *interactionsToClaudeStreamState) [][]byte {
	if st.ActiveBlock && st.ActiveBlockType == blockType {
		return out
	}
	out = appendClaudeContentBlockStop(out, st)
	var block []byte
	if blockType == "thinking" {
		block = []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
	} else {
		block = []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	}
	block, _ = sjson.SetBytes(block, "index", st.BlockIndex)
	st.ActiveBlock = true
	st.ActiveBlockType = blockType
	return append(out, translatorcommon.AppendSSEEventBytes(nil, "content_block_start", block, 3))
}

func appendClaudeToolBlockStart(out [][]byte, stepIndex int, st *interactionsToClaudeStreamState) [][]byte {
	out = appendClaudeContentBlockStop(out, st)
	block := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"","name":"","input":{}}}`)
	block, _ = sjson.SetBytes(block, "index", st.BlockIndex)
	block, _ = sjson.SetBytes(block, "content_block.id", firstNonEmpty(st.ToolIDs[stepIndex], fmt.Sprintf("toolu_%d", stepIndex)))
	block, _ = sjson.SetBytes(block, "content_block.name", st.ToolNames[stepIndex])
	if signature := st.ToolSignatures[stepIndex]; signature != "" {
		block, _ = sjson.SetBytes(block, "content_block.signature", signature)
	}
	st.ActiveBlock = true
	st.ActiveBlockType = "tool_use"
	return append(out, translatorcommon.AppendSSEEventBytes(nil, "content_block_start", block, 3))
}

func ensureClaudeContentBlock(out [][]byte, blockType string, st *interactionsToClaudeStreamState) [][]byte {
	if st.ActiveBlock && st.ActiveBlockType == blockType {
		return out
	}
	return appendClaudeContentBlockStart(out, blockType, st)
}

func appendClaudeContentDelta(out [][]byte, deltaType, field, value string, st *interactionsToClaudeStreamState) [][]byte {
	if value == "" && deltaType != "input_json_delta" {
		return out
	}
	delta := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":""}}`)
	delta, _ = sjson.SetBytes(delta, "index", st.BlockIndex)
	delta, _ = sjson.SetBytes(delta, "delta.type", deltaType)
	delta, _ = sjson.SetBytes(delta, "delta."+field, value)
	return append(out, translatorcommon.AppendSSEEventBytes(nil, "content_block_delta", delta, 3))
}

func appendClaudeContentBlockStop(out [][]byte, st *interactionsToClaudeStreamState) [][]byte {
	if !st.ActiveBlock {
		return out
	}
	stop := []byte(`{"type":"content_block_stop","index":0}`)
	stop, _ = sjson.SetBytes(stop, "index", st.BlockIndex)
	out = append(out, translatorcommon.AppendSSEEventBytes(nil, "content_block_stop", stop, 3))
	st.ActiveBlock = false
	st.ActiveBlockType = ""
	st.BlockIndex++
	return out
}

func appendClaudeMessageDelta(out [][]byte, root gjson.Result, st *interactionsToClaudeStreamState) [][]byte {
	if st.Completed {
		return out
	}
	out = appendClaudeMessageStart(out, st)
	out = appendClaudeContentBlockStop(out, st)
	payload := []byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`)
	if st.SawToolCall {
		payload, _ = sjson.SetBytes(payload, "delta.stop_reason", "tool_use")
	}
	payload = setClaudeUsageFromInteractions(payload, "usage", translatorcommon.InteractionsUsage(root))
	out = append(out, translatorcommon.AppendSSEEventBytes(nil, "message_delta", payload, 3))
	st.Completed = true
	return out
}

func appendClaudeMessageStop(out [][]byte, st *interactionsToClaudeStreamState) [][]byte {
	if st.Done {
		return out
	}
	out = appendClaudeContentBlockStop(out, st)
	if !st.Completed {
		out = appendClaudeMessageDelta(out, gjson.Result{}, st)
	}
	if !st.Stopped {
		out = append(out, translatorcommon.AppendSSEEventString(nil, "message_stop", `{"type":"message_stop"}`, 3))
		st.Stopped = true
	}
	st.Done = true
	return out
}

func setClaudeUsageFromInteractions(out []byte, path string, usage gjson.Result) []byte {
	if !usage.Exists() {
		return out
	}
	if v, ok := firstUsageInt(usage, "input_tokens", "total_input_tokens"); ok {
		out, _ = sjson.SetBytes(out, path+".input_tokens", v)
	}
	if v, ok := firstUsageInt(usage, "output_tokens", "total_output_tokens"); ok {
		out, _ = sjson.SetBytes(out, path+".output_tokens", v)
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

func interactionsContentTexts(content gjson.Result) []string {
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

func interactionsToolID(root gjson.Result) string {
	return firstNonEmpty(root.Get("call_id").String(), root.Get("id").String(), root.Get("tool_use_id").String(), "toolu_interactions")
}

func interactionsSignature(root gjson.Result) string {
	return firstNonEmpty(
		root.Get("signature").String(),
		root.Get("thought_signature").String(),
		root.Get("thoughtSignature").String(),
		root.Get("extra_content.google.thought_signature").String(),
	)
}

func firstExisting(root gjson.Result, paths ...string) gjson.Result {
	for _, path := range paths {
		if value := root.Get(path); value.Exists() {
			return value
		}
	}
	return gjson.Result{}
}

func firstUsageInt(root gjson.Result, paths ...string) (int64, bool) {
	for _, path := range paths {
		if value := root.Get(path); value.Exists() {
			return value.Int(), true
		}
	}
	return 0, false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (st *interactionsToClaudeStreamState) ensureMaps() {
	if st.StepTypes == nil {
		st.StepTypes = make(map[int]string)
	}
	if st.ToolNames == nil {
		st.ToolNames = make(map[int]string)
	}
	if st.ToolIDs == nil {
		st.ToolIDs = make(map[int]string)
	}
	if st.ToolSignatures == nil {
		st.ToolSignatures = make(map[int]string)
	}
}
