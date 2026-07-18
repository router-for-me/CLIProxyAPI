package interactions

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

type interactionsToGeminiStreamState struct {
	ID             string
	Model          string
	ServiceTier    string
	StepNames      map[int]string
	StepIDs        map[int]string
	StepSignatures map[int]string
}

func ConvertGeminiResponseToInteractions(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	return ConvertGeminiResponseToInteractionsStream(ctx, modelName, originalRequestRawJSON, requestRawJSON, rawJSON, param)
}

func ConvertGeminiResponseToInteractionsNonStream(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	return convertGeminiResponseToInteractionsNonStreamDirect(modelName, originalRequestRawJSON, requestRawJSON, rawJSON)
}

func ConvertInteractionsResponseToGemini(_ context.Context, modelName string, _, _, rawJSON []byte, param *any) [][]byte {
	if param == nil {
		var local any
		param = &local
	}
	if *param == nil {
		*param = &interactionsToGeminiStreamState{Model: modelName}
	}
	st := (*param).(*interactionsToGeminiStreamState)
	st.ensureMaps()
	return convertInteractionsEventToGemini(modelName, rawJSON, st)
}

func ConvertInteractionsResponseToGeminiNonStream(_ context.Context, modelName string, _, _, rawJSON []byte, _ *any) []byte {
	root := gjson.ParseBytes(rawJSON)
	interaction := root
	if nested := root.Get("interaction"); nested.Exists() {
		interaction = nested
	}
	st := &interactionsToGeminiStreamState{
		ID:          firstNonEmptyInteractionString(interaction.Get("id").String(), root.Get("id").String(), fmt.Sprintf("response_%d", time.Now().UnixNano())),
		Model:       firstNonEmptyInteractionString(interaction.Get("model").String(), root.Get("model").String(), modelName),
		ServiceTier: firstNonEmptyInteractionString(interaction.Get("service_tier").String(), root.Get("service_tier").String()),
	}
	var parts [][]byte
	steps := interaction.Get("steps")
	if !steps.Exists() {
		steps = root.Get("steps")
	}
	steps.ForEach(func(_, step gjson.Result) bool {
		parts = append(parts, interactionsStepToGeminiParts(step)...)
		return true
	})
	out := buildInteractionsGeminiChunk(st, modelName, parts, "STOP", translatorcommon.InteractionsUsage(root), true)
	return out
}

func ConvertInteractionsRequestToInteractions(modelName string, inputRawJSON []byte, stream bool) []byte {
	_ = modelName
	_ = stream
	return inputRawJSON
}

func ConvertInteractionsResponsePassthrough(_ context.Context, _ string, _, _, rawJSON []byte, _ *any) [][]byte {
	if len(rawJSON) == 0 {
		return nil
	}
	return [][]byte{rawJSON}
}

func ConvertInteractionsResponsePassthroughNonStream(_ context.Context, _ string, _, _, rawJSON []byte, _ *any) []byte {
	return rawJSON
}

func convertInteractionsEventToGemini(modelName string, rawJSON []byte, st *interactionsToGeminiStreamState) [][]byte {
	payload := interactionsGeminiSSEPayload(rawJSON)
	if len(payload) == 0 {
		return nil
	}
	root := gjson.ParseBytes(payload)
	if !root.Exists() {
		return nil
	}
	switch root.Get("event_type").String() {
	case "interaction.created":
		interaction := root.Get("interaction")
		st.ID = firstNonEmptyInteractionString(st.ID, interaction.Get("id").String())
		st.Model = firstNonEmptyInteractionString(st.Model, interaction.Get("model").String(), modelName)
	case "step.start":
		rememberInteractionsGeminiStep(root, st)
	case "step.delta":
		if chunk := interactionsStepDeltaToGeminiChunk(modelName, root, st); len(chunk) > 0 {
			return [][]byte{chunk}
		}
	case "interaction.completed", "finish":
		interaction := root.Get("interaction")
		st.ID = firstNonEmptyInteractionString(st.ID, interaction.Get("id").String())
		st.Model = firstNonEmptyInteractionString(st.Model, interaction.Get("model").String(), modelName)
		st.ServiceTier = firstNonEmptyInteractionString(st.ServiceTier, interaction.Get("service_tier").String())
		chunk := buildInteractionsGeminiChunk(st, modelName, nil, "STOP", translatorcommon.InteractionsUsage(root), true)
		return [][]byte{chunk}
	}
	return nil
}

func rememberInteractionsGeminiStep(root gjson.Result, st *interactionsToGeminiStreamState) {
	index := int(root.Get("index").Int())
	step := root.Get("step")
	st.StepNames[index] = step.Get("name").String()
	st.StepIDs[index] = firstNonEmptyInteractionString(step.Get("call_id").String(), step.Get("id").String())
	st.StepSignatures[index] = firstNonEmptyInteractionString(step.Get("signature").String(), step.Get("thoughtSignature").String(), step.Get("thought_signature").String())
}

func interactionsStepDeltaToGeminiChunk(modelName string, root gjson.Result, st *interactionsToGeminiStreamState) []byte {
	index := int(root.Get("index").Int())
	delta := root.Get("delta")
	switch delta.Get("type").String() {
	case "arguments_delta":
		part := []byte(`{"functionCall":{"name":"","args":{}}}`)
		part, _ = sjson.SetBytes(part, "functionCall.name", firstNonEmptyInteractionString(st.StepNames[index], root.Get("step.name").String()))
		if id := st.StepIDs[index]; id != "" {
			part, _ = sjson.SetBytes(part, "functionCall.id", id)
		}
		if signature := st.StepSignatures[index]; signature != "" {
			part, _ = sjson.SetBytes(part, "thoughtSignature", signature)
		}
		arguments := strings.TrimSpace(delta.Get("arguments").String())
		if arguments != "" && gjson.Valid(arguments) {
			part, _ = sjson.SetRawBytes(part, "functionCall.args", []byte(arguments))
		}
		return buildInteractionsGeminiChunk(st, modelName, [][]byte{part}, "", gjson.Result{}, false)
	case "text":
		text := firstNonEmptyInteractionString(delta.Get("text").String(), delta.Get("content.text").String())
		if text == "" {
			return nil
		}
		return buildInteractionsGeminiChunk(st, modelName, [][]byte{geminiTextPartJSON(text, false)}, "", gjson.Result{}, false)
	case "thought_summary":
		text := firstNonEmptyInteractionString(delta.Get("content.text").String(), delta.Get("text").String())
		if text == "" {
			return nil
		}
		return buildInteractionsGeminiChunk(st, modelName, [][]byte{geminiTextPartJSON(text, true)}, "", gjson.Result{}, false)
	case "thought_signature":
		signature := firstNonEmptyInteractionString(delta.Get("signature").String(), delta.Get("thought_signature").String(), delta.Get("thoughtSignature").String())
		if signature == "" {
			return nil
		}
		st.StepSignatures[index] = signature
		part := geminiTextPartJSON("", true)
		part, _ = sjson.SetBytes(part, "thoughtSignature", signature)
		return buildInteractionsGeminiChunk(st, modelName, [][]byte{part}, "", gjson.Result{}, false)
	}
	return nil
}

func interactionsStepToGeminiParts(step gjson.Result) [][]byte {
	switch step.Get("type").String() {
	case "function_call":
		return [][]byte{interactionsFunctionCallStepToGeminiPart(step)}
	case "function_result":
		return [][]byte{interactionsFunctionResponseStepToGeminiPart(step)}
	case "thought":
		return interactionsContentToGeminiParts(step.Get("content"), true)
	default:
		return interactionsContentToGeminiParts(step.Get("content"), false)
	}
}

func interactionsContentToGeminiParts(content gjson.Result, thought bool) [][]byte {
	var parts [][]byte
	if !content.Exists() {
		return parts
	}
	if content.Type == gjson.String {
		return [][]byte{geminiTextPartJSON(content.String(), thought)}
	}
	if content.IsObject() {
		if part := interactionsContentPartToGeminiPart(content, thought); len(part) > 0 {
			parts = append(parts, part)
		}
		return parts
	}
	if content.IsArray() {
		content.ForEach(func(_, item gjson.Result) bool {
			if part := interactionsContentPartToGeminiPart(item, thought); len(part) > 0 {
				parts = append(parts, part)
			}
			return true
		})
	}
	return parts
}

func interactionsFunctionCallStepToGeminiPart(step gjson.Result) []byte {
	part := []byte(`{"functionCall":{"name":"","args":{}}}`)
	part, _ = sjson.SetBytes(part, "functionCall.name", step.Get("name").String())
	if id := firstNonEmptyInteractionString(step.Get("call_id").String(), step.Get("id").String()); id != "" {
		part, _ = sjson.SetBytes(part, "functionCall.id", id)
	}
	if signature := firstNonEmptyInteractionString(step.Get("signature").String(), step.Get("thoughtSignature").String(), step.Get("thought_signature").String()); signature != "" {
		part, _ = sjson.SetBytes(part, "thoughtSignature", signature)
	}
	part = setInteractionsGeminiRawObject(part, "functionCall.args", firstExistingInteractionResult(step, "arguments", "args"))
	return part
}

func interactionsFunctionResponseStepToGeminiPart(step gjson.Result) []byte {
	part := []byte(`{"functionResponse":{"name":"","response":{}}}`)
	part, _ = sjson.SetBytes(part, "functionResponse.name", step.Get("name").String())
	if id := firstNonEmptyInteractionString(step.Get("call_id").String(), step.Get("id").String()); id != "" {
		part, _ = sjson.SetBytes(part, "functionResponse.id", id)
	}
	part = setInteractionsGeminiRawObject(part, "functionResponse.response", firstExistingInteractionResult(step, "result", "response"))
	return part
}

func buildInteractionsGeminiChunk(st *interactionsToGeminiStreamState, modelName string, parts [][]byte, finishReason string, usage gjson.Result, includeEmptyPart bool) []byte {
	out := []byte(`{"candidates":[{"content":{"parts":[],"role":"model"},"index":0}]}`)
	if len(parts) == 0 && includeEmptyPart {
		parts = append(parts, geminiTextPartJSON("", false))
	}
	for _, part := range parts {
		if len(part) > 0 {
			out, _ = sjson.SetRawBytes(out, "candidates.0.content.parts.-1", part)
		}
	}
	if finishReason != "" {
		out, _ = sjson.SetBytes(out, "candidates.0.finishReason", finishReason)
	}
	if model := firstNonEmptyInteractionString(st.Model, modelName); model != "" {
		out, _ = sjson.SetBytes(out, "modelVersion", model)
	}
	if id := st.ID; id != "" {
		out, _ = sjson.SetBytes(out, "responseId", id)
	}
	if st.ServiceTier != "" {
		out, _ = sjson.SetBytes(out, "usageMetadata.serviceTier", st.ServiceTier)
	}
	return setGeminiUsageMetadataFromInteractionsUsage(out, usage)
}

func setGeminiUsageMetadataFromInteractionsUsage(out []byte, usage gjson.Result) []byte {
	if !usage.Exists() {
		return out
	}
	inputTokens, hasInputTokens := interactionsUsageInt(usage, "input_tokens", "total_input_tokens")
	outputTokens, hasOutputTokens := interactionsUsageInt(usage, "output_tokens", "total_output_tokens")
	totalTokens, hasTotalTokens := interactionsUsageInt(usage, "total_tokens")
	if hasInputTokens {
		out, _ = sjson.SetBytes(out, "usageMetadata.promptTokenCount", inputTokens)
		out, _ = sjson.SetRawBytes(out, "usageMetadata.promptTokensDetails", []byte(fmt.Sprintf(`[{"modality":"TEXT","tokenCount":%d}]`, inputTokens)))
	}
	if hasOutputTokens {
		out, _ = sjson.SetBytes(out, "usageMetadata.candidatesTokenCount", outputTokens)
	}
	if hasTotalTokens {
		out, _ = sjson.SetBytes(out, "usageMetadata.totalTokenCount", totalTokens)
	} else if hasInputTokens || hasOutputTokens {
		out, _ = sjson.SetBytes(out, "usageMetadata.totalTokenCount", inputTokens+outputTokens)
	}
	if thoughtTokens, ok := interactionsUsageInt(usage, "reasoning_tokens", "total_thought_tokens"); ok {
		out, _ = sjson.SetBytes(out, "usageMetadata.thoughtsTokenCount", thoughtTokens)
	}
	if cachedTokens, ok := interactionsUsageInt(usage, "cached_tokens", "total_cached_tokens"); ok {
		out, _ = sjson.SetBytes(out, "usageMetadata.cachedContentTokenCount", cachedTokens)
	}
	return out
}

func interactionsGeminiSSEPayload(rawJSON []byte) []byte {
	trimmed := bytes.TrimSpace(rawJSON)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("{")) {
		return trimmed
	}
	var payload []byte
	for _, line := range bytes.Split(trimmed, []byte{'\n'}) {
		line = bytes.TrimSpace(bytes.TrimRight(line, "\r"))
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		if len(payload) > 0 {
			payload = append(payload, '\n')
		}
		payload = append(payload, data...)
	}
	return payload
}

func interactionsUsageInt(usage gjson.Result, paths ...string) (int64, bool) {
	for _, path := range paths {
		if value := usage.Get(path); value.Exists() {
			return value.Int(), true
		}
	}
	return 0, false
}

func firstExistingInteractionResult(root gjson.Result, paths ...string) gjson.Result {
	for _, path := range paths {
		if value := root.Get(path); value.Exists() {
			return value
		}
	}
	return gjson.Result{}
}

func setInteractionsGeminiRawObject(out []byte, path string, value gjson.Result) []byte {
	if !value.Exists() {
		out, _ = sjson.SetRawBytes(out, path, []byte(`{}`))
		return out
	}
	if value.Type == gjson.String {
		raw := strings.TrimSpace(value.String())
		if raw != "" && gjson.Valid(raw) {
			out, _ = sjson.SetRawBytes(out, path, []byte(raw))
			return out
		}
	}
	if value.Raw != "" {
		out, _ = sjson.SetRawBytes(out, path, []byte(value.Raw))
	}
	return out
}

func firstNonEmptyInteractionString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (st *interactionsToGeminiStreamState) ensureMaps() {
	if st.StepNames == nil {
		st.StepNames = make(map[int]string)
	}
	if st.StepIDs == nil {
		st.StepIDs = make(map[int]string)
	}
	if st.StepSignatures == nil {
		st.StepSignatures = make(map[int]string)
	}
}
