package claude

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
	root := util.ParseGJSONBytesNoCopy(rawJSON)
	interaction := root
	if nested := root.Get("interaction"); nested.Exists() {
		interaction = nested
	}
	outDoc := ojson.NewDocString(`{"id":"","type":"message","role":"assistant","model":"","content":[],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}`)
	_ = outDoc.Set("id", firstNonEmpty(interaction.Get("id").String(), root.Get("id").String(), fmt.Sprintf("msg_%d", time.Now().UnixNano())))
	_ = outDoc.Set("model", firstNonEmpty(interaction.Get("model").String(), modelName))
	steps := interaction.Get("steps")
	if !steps.Exists() {
		steps = root.Get("steps")
	}
	sawToolCall := false
	var contentBlocks [][]byte
	steps.ForEach(func(_, step ojson.Result) bool {
		switch step.Get("type").String() {
		case "thought":
			for _, text := range interactionsContentTexts(step.Get("content")) {
				blockDoc := ojson.NewDocString(`{"type":"thinking","thinking":""}`)
				_ = blockDoc.Set("thinking", text)
				if signature := interactionsSignature(step); signature != "" {
					_ = blockDoc.Set("signature", signature)
				}
				contentBlocks = append(contentBlocks, blockDoc.Bytes())
			}
		case "function_call":
			sawToolCall = true
			blockDoc := ojson.NewDocString(`{"type":"tool_use","id":"","name":"","input":{}}`)
			_ = blockDoc.Set("id", interactionsToolID(step))
			_ = blockDoc.Set("name", step.Get("name").String())
			if signature := interactionsSignature(step); signature != "" {
				_ = blockDoc.Set("signature", signature)
			}
			args := firstExisting(step, "arguments", "args")
			if args.Exists() && args.IsObject() {
				_ = blockDoc.SetRaw("input", args.Raw)
			}
			contentBlocks = append(contentBlocks, blockDoc.Bytes())
		default:
			for _, text := range interactionsContentTexts(step.Get("content")) {
				block := []byte(`{"type":"text","text":""}`)
				block, _ = ojson.SetBytes(block, "text", text)
				contentBlocks = append(contentBlocks, block)
			}
		}
		return true
	})
	if len(contentBlocks) > 0 {
		translatorcommon.SetRawArrayItemsDoc(outDoc, "content", contentBlocks)
	}
	if sawToolCall {
		_ = outDoc.Set("stop_reason", "tool_use")
	}
	status := firstNonEmpty(interaction.Get("status").String(), root.Get("status").String())
	finishReason := firstNonEmpty(interaction.Get("finish_reason").String(), root.Get("finish_reason").String())
	if status == "incomplete" || finishReason == "length" || finishReason == "max_tokens" {
		_ = outDoc.Set("stop_reason", "max_tokens")
	}
	setClaudeUsageFromInteractionsDoc(outDoc, "usage", translatorcommon.InteractionsUsage(root))
	return outDoc.Bytes()
}

func convertInteractionsEventToClaude(modelName string, rawJSON []byte, st *interactionsToClaudeStreamState) [][]byte {
	payload := interactionsSSEPayload(rawJSON)
	if len(payload) == 0 {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
		return appendClaudeMessageStop(nil, st)
	}
	root := util.ParseGJSONBytesNoCopy(payload)
	if !root.Exists() {
		return nil
	}
	switch root.Get("event_type").String() {
	case "interaction.created":
		interaction := root.Get("interaction")
		st.ID = strings.Clone(firstNonEmpty(interaction.Get("id").String(), st.ID))
		st.Model = strings.Clone(firstNonEmpty(interaction.Get("model").String(), st.Model, modelName))
		return appendClaudeMessageStart(nil, st)
	case "step.start":
		return interactionsStepStartToClaude(modelName, root, st)
	case "step.delta":
		return interactionsStepDeltaToClaude(modelName, root, st)
	case "step.stop":
		return appendClaudeContentBlockStop(nil, st)
	case "interaction.completed", "finish":
		return appendClaudeMessageDelta(nil, root, st)
	case "response.failed", "interaction.failed":
		return appendClaudeError(nil, root, st)
	case "done":
		return appendClaudeMessageStop(nil, st)
	}
	return nil
}

func interactionsStepStartToClaude(modelName string, root ojson.Result, st *interactionsToClaudeStreamState) [][]byte {
	out := appendClaudeMessageStart(nil, st)
	out = appendClaudeContentBlockStop(out, st)
	index := int(root.Get("index").Int())
	step := root.Get("step")
	stepType := step.Get("type").String()
	st.StepTypes[index] = strings.Clone(stepType)
	switch stepType {
	case "function_call":
		st.SawToolCall = true
		st.ToolNames[index] = strings.Clone(step.Get("name").String())
		st.ToolIDs[index] = strings.Clone(interactionsToolID(step))
		st.ToolSignatures[index] = strings.Clone(interactionsSignature(step))
		return appendClaudeToolBlockStart(out, index, st)
	case "thought":
		return appendClaudeContentBlockStart(out, "thinking", st)
	default:
		_ = modelName
		return appendClaudeContentBlockStart(out, "text", st)
	}
}

func interactionsStepDeltaToClaude(modelName string, root ojson.Result, st *interactionsToClaudeStreamState) [][]byte {
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
				st.ToolNames[index] = strings.Clone(root.Get("step.name").String())
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
	msgDoc := ojson.NewDocString(`{"type":"message_start","message":{"id":"","type":"message","role":"assistant","content":[],"model":"","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`)
	_ = msgDoc.Set("message.id", firstNonEmpty(st.ID, fmt.Sprintf("msg_%d", time.Now().UnixNano())))
	_ = msgDoc.Set("message.model", st.Model)
	st.Started = true
	return append(out, translatorcommon.AppendSSEEventBytes(nil, "message_start", msgDoc.Bytes(), 3))
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
	block, _ = ojson.SetBytes(block, "index", st.BlockIndex)
	st.ActiveBlock = true
	st.ActiveBlockType = blockType
	return append(out, translatorcommon.AppendSSEEventBytes(nil, "content_block_start", block, 3))
}

func appendClaudeToolBlockStart(out [][]byte, stepIndex int, st *interactionsToClaudeStreamState) [][]byte {
	out = appendClaudeContentBlockStop(out, st)
	blockDoc := ojson.NewDocString(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"","name":"","input":{}}}`)
	_ = blockDoc.Set("index", st.BlockIndex)
	_ = blockDoc.Set("content_block.id", firstNonEmpty(st.ToolIDs[stepIndex], fmt.Sprintf("toolu_%d", stepIndex)))
	_ = blockDoc.Set("content_block.name", st.ToolNames[stepIndex])
	if signature := st.ToolSignatures[stepIndex]; signature != "" {
		_ = blockDoc.Set("content_block.signature", signature)
	}
	st.ActiveBlock = true
	st.ActiveBlockType = "tool_use"
	return append(out, translatorcommon.AppendSSEEventBytes(nil, "content_block_start", blockDoc.Bytes(), 3))
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
	deltaDoc := ojson.NewDocString(`{"type":"content_block_delta","index":0,"delta":{"type":""}}`)
	_ = deltaDoc.Set("index", st.BlockIndex)
	_ = deltaDoc.Set("delta.type", deltaType)
	_ = deltaDoc.Set("delta."+field, value)
	return append(out, translatorcommon.AppendSSEEventBytes(nil, "content_block_delta", deltaDoc.Bytes(), 3))
}

func appendClaudeContentBlockStop(out [][]byte, st *interactionsToClaudeStreamState) [][]byte {
	if !st.ActiveBlock {
		return out
	}
	stop := []byte(`{"type":"content_block_stop","index":0}`)
	stop, _ = ojson.SetBytes(stop, "index", st.BlockIndex)
	out = append(out, translatorcommon.AppendSSEEventBytes(nil, "content_block_stop", stop, 3))
	st.ActiveBlock = false
	st.ActiveBlockType = ""
	st.BlockIndex++
	return out
}

func appendClaudeMessageDelta(out [][]byte, root ojson.Result, st *interactionsToClaudeStreamState) [][]byte {
	if st.Completed {
		return out
	}
	out = appendClaudeMessageStart(out, st)
	out = appendClaudeContentBlockStop(out, st)
	payloadDoc := ojson.NewDocString(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":0,"output_tokens":0}}`)
	if st.SawToolCall {
		_ = payloadDoc.Set("delta.stop_reason", "tool_use")
	}
	interaction := root.Get("interaction")
	status := firstNonEmpty(interaction.Get("status").String(), root.Get("status").String())
	finishReason := firstNonEmpty(interaction.Get("finish_reason").String(), root.Get("finish_reason").String())
	if status == "incomplete" || finishReason == "length" || finishReason == "max_tokens" {
		_ = payloadDoc.Set("delta.stop_reason", "max_tokens")
	}
	setClaudeUsageFromInteractionsDoc(payloadDoc, "usage", translatorcommon.InteractionsUsage(root))
	out = append(out, translatorcommon.AppendSSEEventBytes(nil, "message_delta", payloadDoc.Bytes(), 3))
	st.Completed = true
	return out
}

func appendClaudeMessageStop(out [][]byte, st *interactionsToClaudeStreamState) [][]byte {
	if st.Done {
		return out
	}
	out = appendClaudeContentBlockStop(out, st)
	if !st.Completed {
		out = appendClaudeMessageDelta(out, ojson.Result{}, st)
	}
	if !st.Stopped {
		out = append(out, translatorcommon.AppendSSEEventString(nil, "message_stop", `{"type":"message_stop"}`, 3))
		st.Stopped = true
	}
	st.Done = true
	return out
}

func appendClaudeError(out [][]byte, root ojson.Result, st *interactionsToClaudeStreamState) [][]byte {
	out = appendClaudeContentBlockStop(out, st)
	errNode := root.Get("error")
	if !errNode.Exists() {
		errNode = root.Get("interaction.error")
	}
	msg := errNode.Get("message").String()
	if msg == "" {
		msg = "upstream error occurred"
	}
	errType := errNode.Get("type").String()
	if errType == "" {
		errType = "api_error"
	}
	payloadDoc := ojson.NewDocString(`{"type":"error","error":{"type":"","message":""}}`)
	_ = payloadDoc.Set("error.type", errType)
	_ = payloadDoc.Set("error.message", msg)
	return append(out, translatorcommon.AppendSSEEventBytes(nil, "error", payloadDoc.Bytes(), 3))
}

func setClaudeUsageFromInteractionsDoc(doc *ojson.Doc, path string, usage ojson.Result) {
	if !usage.Exists() {
		return
	}
	outputTokens, hasOutput := firstUsageInt(usage, "output_tokens", "total_output_tokens")
	cachedTokens, hasCached := firstUsageInt(usage, "cache_read_input_tokens", "cache_read_tokens", "cached_tokens", "total_cached_tokens")
	cacheWriteTokens, hasCacheWrite := firstUsageInt(usage, "cache_creation_input_tokens", "cache_creation_tokens", "cache_write_tokens")

	totalCache := int64(0)
	if hasCached && cachedTokens > 0 {
		totalCache += cachedTokens
	}
	if hasCacheWrite && cacheWriteTokens > 0 {
		totalCache += cacheWriteTokens
	}

	hasInput := false
	var inputTokens int64

	if inNode := usage.Get("input_tokens"); inNode.Exists() {
		hasInput = true
		inputTokens = inNode.Int()
	} else if totalVal, ok := firstUsageInt(usage, "total_input_tokens", "prompt_tokens"); ok {
		hasInput = true
		if totalVal >= totalCache {
			inputTokens = totalVal - totalCache
		} else {
			inputTokens = 0
		}
	}

	if hasInput {
		_ = doc.Set(path+".input_tokens", inputTokens)
	}
	if hasOutput {
		_ = doc.Set(path+".output_tokens", outputTokens)
	}
	if hasCached && cachedTokens > 0 {
		_ = doc.Set(path+".cache_read_input_tokens", cachedTokens)
	}
	if hasCacheWrite && cacheWriteTokens > 0 {
		_ = doc.Set(path+".cache_creation_input_tokens", cacheWriteTokens)
	}
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

func interactionsContentTexts(content ojson.Result) []string {
	if !content.Exists() {
		return nil
	}
	if content.Type == ojson.String {
		return []string{content.String()}
	}
	var out []string
	content.ForEach(func(_, part ojson.Result) bool {
		if text := firstNonEmpty(part.Get("text").String(), part.Get("content.text").String()); text != "" {
			out = append(out, text)
		}
		return true
	})
	return out
}

func interactionsToolID(root ojson.Result) string {
	return firstNonEmpty(root.Get("call_id").String(), root.Get("id").String(), root.Get("tool_use_id").String(), "toolu_interactions")
}

func interactionsSignature(root ojson.Result) string {
	return firstNonEmpty(
		root.Get("signature").String(),
		root.Get("thought_signature").String(),
		root.Get("thoughtSignature").String(),
		root.Get("extra_content.google.thought_signature").String(),
	)
}

func firstExisting(root ojson.Result, paths ...string) ojson.Result {
	for _, path := range paths {
		if value := root.Get(path); value.Exists() {
			return value
		}
	}
	return ojson.Result{}
}

func firstUsageInt(root ojson.Result, paths ...string) (int64, bool) {
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
