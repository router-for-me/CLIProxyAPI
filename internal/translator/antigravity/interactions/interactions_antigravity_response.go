package interactions

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type antigravityToInteractionsStreamState struct {
	Started         bool
	Finished        bool
	Completed       bool
	Done            bool
	ActiveStepOpen  bool
	ID              string
	StepID          string
	ActiveStepType  string
	ActiveStepIndex int
	StepIndex       int
	ToolNameMap     map[string]string
}

func ConvertAntigravityResponseToInteractions(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	_ = ctx
	_ = originalRequestRawJSON
	_ = requestRawJSON
	if param == nil {
		var local any
		param = &local
	}
	if *param == nil {
		*param = &antigravityToInteractionsStreamState{
			ID:          fmt.Sprintf("interaction_%d", time.Now().UnixNano()),
			ToolNameMap: util.DisambiguatedToolNameMap(originalRequestRawJSON),
		}
	}
	st := (*param).(*antigravityToInteractionsStreamState)
	payloads := antigravityStreamPayloads(rawJSON)
	out := make([][]byte, 0)
	for _, payload := range payloads {
		if bytes.Equal(bytes.TrimSpace(payload), []byte("[DONE]")) {
			if !st.Completed {
				out = appendAntigravityInteractionsStepStop(out, st)
				out = appendAntigravityInteractionsCompleted(out, st, modelName, gjson.Result{})
			}
			out = appendAntigravityInteractionsDone(out, st)
			continue
		}
		root := unwrapAntigravityResponse(gjson.ParseBytes(payload))
		root = restoreInteractionsFunctionNames(root, st.ToolNameMap)
		if !root.Exists() {
			continue
		}
		if !st.Started {
			out = appendAntigravityInteractionsCreated(out, st, modelName)
			out = appendAntigravityInteractionsStatusUpdate(out, st)
			st.Started = true
		}
		root.Get("candidates.0.content.parts").ForEach(func(_, part gjson.Result) bool {
			out = appendAntigravityPartToInteractionsStream(out, st, part)
			return true
		})
		hasFinish := root.Get("candidates.0.finishReason").Exists()
		hasUsage := hasAntigravityStreamUsage(root)
		if hasFinish && !st.Finished {
			out = appendAntigravityInteractionsStepStop(out, st)
			st.Finished = true
		}
		if hasUsage && st.Finished && !st.Completed {
			out = appendAntigravityInteractionsCompleted(out, st, modelName, root)
		}
	}
	return out
}

func ConvertAntigravityResponseToInteractionsNonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	_ = ctx
	_ = originalRequestRawJSON
	_ = requestRawJSON
	root := unwrapAntigravityResponse(gjson.ParseBytes(rawJSON))
	root = restoreInteractionsFunctionNames(root, util.DisambiguatedToolNameMap(originalRequestRawJSON))
	out := []byte(`{"id":"","object":"interaction","status":"completed","model":"","steps":[]}`)
	id := root.Get("responseId").String()
	if id == "" {
		id = fmt.Sprintf("interaction_%d", time.Now().UnixNano())
	}
	out, _ = sjson.SetBytes(out, "id", id)
	out, _ = sjson.SetBytes(out, "model", modelName)
	root.Get("candidates.0.content.parts").ForEach(func(_, part gjson.Result) bool {
		if step := antigravityPartToInteractionsStep(part); len(step) > 0 {
			out, _ = sjson.SetRawBytes(out, "steps.-1", step)
		}
		return true
	})
	out = setInteractionsUsageFromAntigravity(out, "usage", root)
	return out
}

func antigravityStreamPayloads(rawJSON []byte) [][]byte {
	trimmed := bytes.TrimSpace(rawJSON)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		return [][]byte{bytes.TrimSpace(trimmed[5:])}
	}
	root := gjson.ParseBytes(trimmed)
	if root.IsArray() {
		payloads := make([][]byte, 0)
		root.ForEach(func(_, item gjson.Result) bool {
			if response := item.Get("response"); response.Exists() {
				payloads = append(payloads, []byte(response.Raw))
			} else if item.Exists() {
				payloads = append(payloads, []byte(item.Raw))
			}
			return true
		})
		if len(payloads) > 0 {
			return payloads
		}
	}
	return [][]byte{trimmed}
}

func unwrapAntigravityResponse(root gjson.Result) gjson.Result {
	if response := root.Get("response"); response.Exists() {
		response = restoreAntigravityUsageMetadata(response)
		return response
	}
	return restoreAntigravityUsageMetadata(root)
}

func restoreInteractionsFunctionNames(root gjson.Result, nameMap map[string]string) gjson.Result {
	if !root.Exists() || len(nameMap) == 0 {
		return root
	}
	raw := []byte(root.Raw)
	candidates := root.Get("candidates")
	for candidateIndex, candidate := range candidates.Array() {
		for partIndex, part := range candidate.Get("content.parts").Array() {
			for _, field := range []string{"functionCall", "functionResponse"} {
				name := part.Get(field + ".name").String()
				if name == "" {
					continue
				}
				path := fmt.Sprintf("candidates.%d.content.parts.%d.%s.name", candidateIndex, partIndex, field)
				raw, _ = sjson.SetBytes(raw, path, util.RestoreSanitizedToolName(nameMap, name))
			}
		}
	}
	return gjson.ParseBytes(raw)
}

func restoreAntigravityUsageMetadata(root gjson.Result) gjson.Result {
	if !root.Get("usageMetadata").Exists() {
		if cpaUsage := root.Get("cpaUsageMetadata"); cpaUsage.Exists() {
			raw, _ := sjson.SetRawBytes([]byte(root.Raw), "usageMetadata", []byte(cpaUsage.Raw))
			raw, _ = sjson.DeleteBytes(raw, "cpaUsageMetadata")
			return gjson.ParseBytes(raw)
		}
	}
	return root
}

func appendAntigravityInteractionsCreated(out [][]byte, st *antigravityToInteractionsStreamState, modelName string) [][]byte {
	created := []byte(`{"interaction":{"id":"","status":"in_progress","object":"interaction","model":""},"event_type":"interaction.created"}`)
	created, _ = sjson.SetBytes(created, "interaction.id", st.ID)
	created, _ = sjson.SetBytes(created, "interaction.model", modelName)
	return append(out, translatorcommon.SSEEventData("interaction.created", created))
}

func appendAntigravityInteractionsStatusUpdate(out [][]byte, st *antigravityToInteractionsStreamState) [][]byte {
	statusUpdate := []byte(`{"interaction_id":"","status":"in_progress","event_type":"interaction.status_update"}`)
	statusUpdate, _ = sjson.SetBytes(statusUpdate, "interaction_id", st.ID)
	return append(out, translatorcommon.SSEEventData("interaction.status_update", statusUpdate))
}

func appendAntigravityInteractionsCompleted(out [][]byte, st *antigravityToInteractionsStreamState, modelName string, root gjson.Result) [][]byte {
	now := time.Now().UTC().Format(time.RFC3339)
	completed := []byte(`{"interaction":{"id":"","status":"completed","usage":{},"created":"","updated":"","service_tier":"standard","object":"interaction","model":""},"event_type":"interaction.completed"}`)
	completed, _ = sjson.SetBytes(completed, "interaction.id", st.ID)
	completed, _ = sjson.SetBytes(completed, "interaction.created", now)
	completed, _ = sjson.SetBytes(completed, "interaction.updated", now)
	completed, _ = sjson.SetBytes(completed, "interaction.model", modelName)
	if root.Exists() {
		completed = setInteractionsStreamUsageFromAntigravity(completed, "interaction.usage", root)
	}
	out = append(out, translatorcommon.SSEEventData("interaction.completed", completed))
	st.Completed = true
	return out
}

func appendAntigravityInteractionsDone(out [][]byte, st *antigravityToInteractionsStreamState) [][]byte {
	if st.Done {
		return out
	}
	out = append(out, translatorcommon.SSEEventData("done", []byte("[DONE]")))
	st.Done = true
	return out
}

func appendAntigravityInteractionsStepStart(out [][]byte, st *antigravityToInteractionsStreamState, stepType string, part gjson.Result) [][]byte {
	st.StepID = fmt.Sprintf("step_%d", time.Now().UnixNano())
	st.ActiveStepIndex = st.StepIndex
	st.StepIndex++
	st.ActiveStepType = stepType
	st.ActiveStepOpen = true
	stepStart := []byte(`{"index":0,"step":{"type":""},"event_type":"step.start"}`)
	stepStart, _ = sjson.SetBytes(stepStart, "index", st.ActiveStepIndex)
	stepStart, _ = sjson.SetBytes(stepStart, "step.type", stepType)
	if stepType == "function_call" {
		id := antigravityFunctionPartID(part)
		if id == "" {
			id = st.StepID
		}
		stepStart, _ = sjson.SetBytes(stepStart, "step.id", id)
		stepStart, _ = sjson.SetBytes(stepStart, "step.call_id", id)
		stepStart, _ = sjson.SetBytes(stepStart, "step.name", part.Get("name").String())
		stepStart, _ = sjson.SetRawBytes(stepStart, "step.arguments", []byte(`{}`))
	}
	return append(out, translatorcommon.SSEEventData("step.start", stepStart))
}

func appendAntigravityInteractionsStepStop(out [][]byte, st *antigravityToInteractionsStreamState) [][]byte {
	if !st.ActiveStepOpen {
		return out
	}
	stepStop := []byte(`{"index":0,"event_type":"step.stop"}`)
	stepStop, _ = sjson.SetBytes(stepStop, "index", st.ActiveStepIndex)
	out = append(out, translatorcommon.SSEEventData("step.stop", stepStop))
	st.ActiveStepOpen = false
	st.ActiveStepType = ""
	return out
}

func ensureAntigravityInteractionsStep(out [][]byte, st *antigravityToInteractionsStreamState, stepType string, part gjson.Result) [][]byte {
	if st.ActiveStepOpen && st.ActiveStepType == stepType {
		return out
	}
	out = appendAntigravityInteractionsStepStop(out, st)
	return appendAntigravityInteractionsStepStart(out, st, stepType, part)
}

func appendAntigravityPartToInteractionsStream(out [][]byte, st *antigravityToInteractionsStreamState, part gjson.Result) [][]byte {
	if text := part.Get("text"); text.Exists() && text.String() != "" {
		if part.Get("thought").Bool() {
			out = ensureAntigravityInteractionsStep(out, st, "thought", gjson.Result{})
			delta := []byte(`{"index":0,"delta":{"content":{"text":"","type":"text"},"type":"thought_summary"},"event_type":"step.delta"}`)
			delta, _ = sjson.SetBytes(delta, "index", st.ActiveStepIndex)
			delta, _ = sjson.SetBytes(delta, "delta.content.text", text.String())
			out = append(out, translatorcommon.SSEEventData("step.delta", delta))
			return appendAntigravityThoughtSignature(out, st, part)
		}
		out = ensureAntigravityInteractionsStep(out, st, "model_output", gjson.Result{})
		delta := []byte(`{"index":0,"delta":{"text":"","type":"text"},"event_type":"step.delta"}`)
		delta, _ = sjson.SetBytes(delta, "index", st.ActiveStepIndex)
		delta, _ = sjson.SetBytes(delta, "delta.text", text.String())
		return append(out, translatorcommon.SSEEventData("step.delta", delta))
	}
	if fc := part.Get("functionCall"); fc.Exists() {
		out = appendAntigravityThoughtSignature(out, st, part)
		out = ensureAntigravityInteractionsStep(out, st, "function_call", fc)
		delta := []byte(`{"index":0,"delta":{"arguments":"","type":"arguments_delta"},"event_type":"step.delta"}`)
		delta, _ = sjson.SetBytes(delta, "index", st.ActiveStepIndex)
		arguments := `{}`
		if args := fc.Get("args"); args.Exists() {
			arguments = args.Raw
		}
		delta, _ = sjson.SetBytes(delta, "delta.arguments", arguments)
		out = append(out, translatorcommon.SSEEventData("step.delta", delta))
		return appendAntigravityInteractionsStepStop(out, st)
	}
	if fr := part.Get("functionResponse"); fr.Exists() {
		out = ensureAntigravityInteractionsStep(out, st, "function_result", fr)
		delta := []byte(`{"index":0,"delta":{"type":"function_result","name":"","result":{}},"event_type":"step.delta"}`)
		delta, _ = sjson.SetBytes(delta, "index", st.ActiveStepIndex)
		delta, _ = sjson.SetBytes(delta, "delta.name", fr.Get("name").String())
		if response := fr.Get("response"); response.Exists() {
			delta, _ = sjson.SetRawBytes(delta, "delta.result", []byte(response.Raw))
		}
		out = append(out, translatorcommon.SSEEventData("step.delta", delta))
		return appendAntigravityInteractionsStepStop(out, st)
	}
	return out
}

func appendAntigravityThoughtSignature(out [][]byte, st *antigravityToInteractionsStreamState, part gjson.Result) [][]byte {
	if signature := antigravityThoughtSignature(part); signature != "" {
		out = ensureAntigravityInteractionsStep(out, st, "thought", gjson.Result{})
		signatureDelta := []byte(`{"index":0,"delta":{"signature":"","type":"thought_signature"},"event_type":"step.delta"}`)
		signatureDelta, _ = sjson.SetBytes(signatureDelta, "index", st.ActiveStepIndex)
		signatureDelta, _ = sjson.SetBytes(signatureDelta, "delta.signature", signature)
		return append(out, translatorcommon.SSEEventData("step.delta", signatureDelta))
	}
	return out
}

func antigravityPartToInteractionsStep(part gjson.Result) []byte {
	if fc := part.Get("functionCall"); fc.Exists() {
		step := []byte(`{"type":"function_call","name":"","arguments":{}}`)
		step, _ = sjson.SetBytes(step, "name", fc.Get("name").String())
		if id := fc.Get("id"); id.Exists() {
			step, _ = sjson.SetBytes(step, "call_id", id.String())
		} else if callID := fc.Get("call_id"); callID.Exists() {
			step, _ = sjson.SetBytes(step, "call_id", callID.String())
		}
		if args := fc.Get("args"); args.Exists() {
			step, _ = sjson.SetRawBytes(step, "arguments", []byte(args.Raw))
		}
		return step
	}
	if fr := part.Get("functionResponse"); fr.Exists() {
		step := []byte(`{"type":"function_result","name":"","result":{}}`)
		step, _ = sjson.SetBytes(step, "name", fr.Get("name").String())
		if id := fr.Get("id"); id.Exists() {
			step, _ = sjson.SetBytes(step, "call_id", id.String())
		} else if callID := fr.Get("call_id"); callID.Exists() {
			step, _ = sjson.SetBytes(step, "call_id", callID.String())
		}
		if response := fr.Get("response"); response.Exists() {
			step, _ = sjson.SetRawBytes(step, "result", []byte(response.Raw))
		}
		return step
	}
	if text := part.Get("text"); text.Exists() {
		step := []byte(`{"type":"model_output","content":[]}`)
		if part.Get("thought").Bool() {
			step, _ = sjson.SetBytes(step, "type", "thought")
		}
		item := []byte(`{"type":"text","text":""}`)
		item, _ = sjson.SetBytes(item, "text", text.String())
		step, _ = sjson.SetRawBytes(step, "content.-1", item)
		return step
	}
	if inline := part.Get("inlineData"); inline.Exists() {
		return antigravityInlineDataToInteractionsStep(inline)
	}
	if inline := part.Get("inline_data"); inline.Exists() {
		return antigravityInlineDataToInteractionsStep(inline)
	}
	return nil
}

func antigravityInlineDataToInteractionsStep(inline gjson.Result) []byte {
	mimeType := inline.Get("mimeType").String()
	if mimeType == "" {
		mimeType = inline.Get("mime_type").String()
	}
	data := inline.Get("data").String()
	if mimeType == "" || data == "" {
		return nil
	}
	contentType := "document"
	lower := strings.ToLower(mimeType)
	switch {
	case strings.HasPrefix(lower, "image/"):
		contentType = "image"
	case strings.HasPrefix(lower, "audio/"):
		contentType = "audio"
	case strings.HasPrefix(lower, "video/"):
		contentType = "video"
	}
	item := []byte(`{"type":"","mime_type":"","data":""}`)
	item, _ = sjson.SetBytes(item, "type", contentType)
	item, _ = sjson.SetBytes(item, "mime_type", mimeType)
	item, _ = sjson.SetBytes(item, "data", data)
	step := []byte(`{"type":"model_output","content":[]}`)
	step, _ = sjson.SetRawBytes(step, "content.-1", item)
	return step
}

func hasAntigravityStreamUsage(root gjson.Result) bool {
	usage := antigravityUsageNode(root)
	if !usage.Exists() {
		return false
	}
	for _, path := range []string{
		"promptTokenCount",
		"candidatesTokenCount",
		"totalTokenCount",
		"thoughtsTokenCount",
		"cachedContentTokenCount",
		"prompt_token_count",
		"candidates_token_count",
		"total_token_count",
		"thoughts_token_count",
		"cached_content_token_count",
	} {
		if usage.Get(path).Exists() {
			return true
		}
	}
	return false
}

func setInteractionsUsageFromAntigravity(out []byte, path string, root gjson.Result) []byte {
	usage := antigravityUsageNode(root)
	if !usage.Exists() {
		return out
	}
	out, _ = sjson.SetBytes(out, path+".input_tokens", firstAntigravityUsageInt(usage, "promptTokenCount", "prompt_token_count"))
	out, _ = sjson.SetBytes(out, path+".output_tokens", firstAntigravityUsageInt(usage, "candidatesTokenCount", "candidates_token_count"))
	if antigravityUsagePathExists(usage, "thoughtsTokenCount", "thoughts_token_count") {
		out, _ = sjson.SetBytes(out, path+".reasoning_tokens", firstAntigravityUsageInt(usage, "thoughtsTokenCount", "thoughts_token_count"))
	}
	out, _ = sjson.SetBytes(out, path+".total_tokens", firstAntigravityUsageInt(usage, "totalTokenCount", "total_token_count"))
	if antigravityUsagePathExists(usage, "cachedContentTokenCount", "cached_content_token_count") {
		out, _ = sjson.SetBytes(out, path+".cached_tokens", firstAntigravityUsageInt(usage, "cachedContentTokenCount", "cached_content_token_count"))
	}
	return out
}

func setInteractionsStreamUsageFromAntigravity(out []byte, path string, root gjson.Result) []byte {
	usage := antigravityUsageNode(root)
	if !usage.Exists() {
		return out
	}
	inputTokens := firstAntigravityUsageInt(usage, "promptTokenCount", "prompt_token_count")
	outputTokens := firstAntigravityUsageInt(usage, "candidatesTokenCount", "candidates_token_count")
	totalTokens := firstAntigravityUsageInt(usage, "totalTokenCount", "total_token_count")
	thoughtTokens := firstAntigravityUsageInt(usage, "thoughtsTokenCount", "thoughts_token_count")
	cachedTokens := firstAntigravityUsageInt(usage, "cachedContentTokenCount", "cached_content_token_count")
	out, _ = sjson.SetBytes(out, path+".total_tokens", totalTokens)
	out, _ = sjson.SetBytes(out, path+".total_input_tokens", inputTokens)
	out, _ = sjson.SetRawBytes(out, path+".input_tokens_by_modality", []byte(fmt.Sprintf(`[{"modality":"text","tokens":%d}]`, inputTokens)))
	out, _ = sjson.SetBytes(out, path+".total_cached_tokens", cachedTokens)
	out, _ = sjson.SetBytes(out, path+".total_output_tokens", outputTokens)
	out, _ = sjson.SetBytes(out, path+".total_tool_use_tokens", 0)
	out, _ = sjson.SetBytes(out, path+".total_thought_tokens", thoughtTokens)
	return out
}

func antigravityUsageNode(root gjson.Result) gjson.Result {
	if usage := root.Get("usageMetadata"); usage.Exists() {
		return usage
	}
	if usage := root.Get("usage_metadata"); usage.Exists() {
		return usage
	}
	if usage := root.Get("cpaUsageMetadata"); usage.Exists() {
		return usage
	}
	return gjson.Result{}
}

func firstAntigravityUsageInt(usage gjson.Result, paths ...string) int64 {
	for _, path := range paths {
		if value := usage.Get(path); value.Exists() {
			return value.Int()
		}
	}
	return 0
}

func antigravityUsagePathExists(usage gjson.Result, paths ...string) bool {
	for _, path := range paths {
		if usage.Get(path).Exists() {
			return true
		}
	}
	return false
}

func antigravityFunctionPartID(part gjson.Result) string {
	if id := part.Get("id"); id.Exists() {
		return id.String()
	}
	if callID := part.Get("call_id"); callID.Exists() {
		return callID.String()
	}
	return ""
}

func antigravityThoughtSignature(part gjson.Result) string {
	for _, path := range []string{"thoughtSignature", "thought_signature", "extra_content.google.thought_signature"} {
		if signature := strings.TrimSpace(part.Get(path).String()); signature != "" {
			return signature
		}
	}
	return ""
}
