package interactions

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
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
	root := util.ParseGJSONBytesNoCopy(rawJSON)
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
	steps.ForEach(func(_, step ojson.Result) bool {
		parts = append(parts, interactionsStepToGeminiParts(step)...)
		return true
	})
	out := buildInteractionsGeminiChunk(st, modelName, parts, "STOP", translatorcommon.InteractionsUsage(root), true)
	return out
}

func ConvertInteractionsRequestToInteractions(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	_ = modelName
	_ = stream
	return inputRawJSON, nil
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
	root := util.ParseGJSONBytesNoCopy(payload)
	if !root.Exists() {
		return nil
	}
	switch root.Get("event_type").String() {
	case "interaction.created":
		interaction := root.Get("interaction")
		st.ID = strings.Clone(firstNonEmptyInteractionString(st.ID, interaction.Get("id").String()))
		st.Model = strings.Clone(firstNonEmptyInteractionString(st.Model, interaction.Get("model").String(), modelName))
	case "step.start":
		rememberInteractionsGeminiStep(root, st)
	case "step.delta":
		if chunk := interactionsStepDeltaToGeminiChunk(modelName, root, st); len(chunk) > 0 {
			return [][]byte{chunk}
		}
	case "interaction.completed", "finish":
		interaction := root.Get("interaction")
		st.ID = strings.Clone(firstNonEmptyInteractionString(st.ID, interaction.Get("id").String()))
		st.Model = strings.Clone(firstNonEmptyInteractionString(st.Model, interaction.Get("model").String(), modelName))
		st.ServiceTier = strings.Clone(firstNonEmptyInteractionString(st.ServiceTier, interaction.Get("service_tier").String()))
		chunk := buildInteractionsGeminiChunk(st, modelName, nil, "STOP", translatorcommon.InteractionsUsage(root), true)
		return [][]byte{chunk}
	case "response.failed", "interaction.failed":
		errNode := root.Get("error")
		if !errNode.Exists() {
			errNode = root.Get("interaction.error")
		}
		msg := errNode.Get("message").String()
		if msg == "" {
			msg = "upstream error occurred"
		}
		codeVal := firstNonEmptyInteractionString(errNode.Get("code").String(), root.Get("code").String(), errNode.Get("status").String())
		statusCode, statusText := mapInteractionsErrorToGemini(codeVal)
		errorResponseDoc := ojson.NewDocString(`{"error":{"code":500,"message":"","status":"INTERNAL"}}`)
		_ = errorResponseDoc.Set("error.code", statusCode)
		_ = errorResponseDoc.Set("error.message", msg)
		_ = errorResponseDoc.Set("error.status", statusText)
		return [][]byte{errorResponseDoc.Bytes()}
	}
	return nil
}

func mapInteractionsErrorToGemini(codeStr string) (int, string) {
	codeStr = strings.TrimSpace(codeStr)
	switch strings.ToLower(codeStr) {
	case "400", "invalid_argument":
		return 400, "INVALID_ARGUMENT"
	case "401", "unauthenticated":
		return 401, "UNAUTHENTICATED"
	case "403", "permission_denied":
		return 403, "PERMISSION_DENIED"
	case "404", "not_found":
		return 404, "NOT_FOUND"
	case "429", "resource_exhausted", "rate_limit_exceeded":
		return 429, "RESOURCE_EXHAUSTED"
	case "499", "canceled", "cancelled":
		return 499, "CANCELLED"
	case "503", "unavailable":
		return 503, "UNAVAILABLE"
	case "504", "deadline_exceeded":
		return 504, "DEADLINE_EXCEEDED"
	case "500", "internal":
		return 500, "INTERNAL"
	default:
		if n, err := strconv.Atoi(codeStr); err == nil && n >= 400 && n < 600 {
			if n >= 500 {
				return n, "INTERNAL"
			}
			return n, "INVALID_ARGUMENT"
		}
		return 500, "INTERNAL"
	}
}

func rememberInteractionsGeminiStep(root ojson.Result, st *interactionsToGeminiStreamState) {
	index := int(root.Get("index").Int())
	step := root.Get("step")
	st.StepNames[index] = strings.Clone(step.Get("name").String())
	st.StepIDs[index] = strings.Clone(firstNonEmptyInteractionString(step.Get("call_id").String(), step.Get("id").String()))
	st.StepSignatures[index] = strings.Clone(firstNonEmptyInteractionString(step.Get("signature").String(), step.Get("thoughtSignature").String(), step.Get("thought_signature").String()))
}

func interactionsStepDeltaToGeminiChunk(modelName string, root ojson.Result, st *interactionsToGeminiStreamState) []byte {
	index := int(root.Get("index").Int())
	delta := root.Get("delta")
	switch delta.Get("type").String() {
	case "arguments_delta":
		partDoc := ojson.NewDocString(`{"functionCall":{"name":"","args":{}}}`)
		_ = partDoc.Set("functionCall.name", firstNonEmptyInteractionString(st.StepNames[index], root.Get("step.name").String()))
		if id := st.StepIDs[index]; id != "" {
			_ = partDoc.Set("functionCall.id", id)
		}
		if signature := st.StepSignatures[index]; signature != "" {
			_ = partDoc.Set("thoughtSignature", signature)
		}
		arguments := strings.TrimSpace(delta.Get("arguments").String())
		if arguments != "" && ojson.Valid(arguments) {
			_ = partDoc.SetRaw("functionCall.args", arguments)
		}
		return buildInteractionsGeminiChunk(st, modelName, [][]byte{partDoc.Bytes()}, "", ojson.Result{}, false)
	case "text":
		text := firstNonEmptyInteractionString(delta.Get("text").String(), delta.Get("content.text").String())
		if text == "" {
			return nil
		}
		return buildInteractionsGeminiChunk(st, modelName, [][]byte{geminiTextPartJSON(text, false)}, "", ojson.Result{}, false)
	case "thought_summary":
		text := firstNonEmptyInteractionString(delta.Get("content.text").String(), delta.Get("text").String())
		if text == "" {
			return nil
		}
		return buildInteractionsGeminiChunk(st, modelName, [][]byte{geminiTextPartJSON(text, true)}, "", ojson.Result{}, false)
	case "thought_signature":
		signature := strings.Clone(firstNonEmptyInteractionString(delta.Get("signature").String(), delta.Get("thought_signature").String(), delta.Get("thoughtSignature").String()))
		if signature == "" {
			return nil
		}
		st.StepSignatures[index] = signature
		partDoc := ojson.NewDocString(`{"text":"","thought":true}`)
		_ = partDoc.Set("thoughtSignature", signature)
		return buildInteractionsGeminiChunk(st, modelName, [][]byte{partDoc.Bytes()}, "", ojson.Result{}, false)
	}
	return nil
}

func interactionsStepToGeminiParts(step ojson.Result) [][]byte {
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

func interactionsContentToGeminiParts(content ojson.Result, thought bool) [][]byte {
	var parts [][]byte
	if !content.Exists() {
		return parts
	}
	if content.Type == ojson.String {
		return [][]byte{geminiTextPartJSON(content.String(), thought)}
	}
	if content.IsObject() {
		if part := interactionsContentPartToGeminiPart(content, thought); len(part) > 0 {
			parts = append(parts, part)
		}
		return parts
	}
	if content.IsArray() {
		content.ForEach(func(_, item ojson.Result) bool {
			if part := interactionsContentPartToGeminiPart(item, thought); len(part) > 0 {
				parts = append(parts, part)
			}
			return true
		})
	}
	return parts
}

func interactionsFunctionCallStepToGeminiPart(step ojson.Result) []byte {
	partDoc := ojson.NewDocString(`{"functionCall":{"name":"","args":{}}}`)
	_ = partDoc.Set("functionCall.name", step.Get("name").String())
	if id := firstNonEmptyInteractionString(step.Get("call_id").String(), step.Get("id").String()); id != "" {
		_ = partDoc.Set("functionCall.id", id)
	}
	if signature := firstNonEmptyInteractionString(step.Get("signature").String(), step.Get("thoughtSignature").String(), step.Get("thought_signature").String()); signature != "" {
		_ = partDoc.Set("thoughtSignature", signature)
	}
	setInteractionsGeminiRawObjectDoc(partDoc, "functionCall.args", firstExistingInteractionResult(step, "arguments", "args"))
	return partDoc.Bytes()
}

func interactionsFunctionResponseStepToGeminiPart(step ojson.Result) []byte {
	partDoc := ojson.NewDocString(`{"functionResponse":{"name":"","response":{}}}`)
	_ = partDoc.Set("functionResponse.name", step.Get("name").String())
	if id := firstNonEmptyInteractionString(step.Get("call_id").String(), step.Get("id").String()); id != "" {
		_ = partDoc.Set("functionResponse.id", id)
	}
	setInteractionsGeminiFunctionResponseDoc(partDoc, "functionResponse.response", firstExistingInteractionResult(step, "result", "response"))
	return partDoc.Bytes()
}

func buildInteractionsGeminiChunk(st *interactionsToGeminiStreamState, modelName string, parts [][]byte, finishReason string, usage ojson.Result, includeEmptyPart bool) []byte {
	outDoc := ojson.NewDocString(`{"candidates":[{"content":{"parts":[],"role":"model"},"index":0}]}`)
	if len(parts) == 0 && includeEmptyPart {
		parts = append(parts, geminiTextPartJSON("", false))
	}
	validParts := make([][]byte, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 {
			validParts = append(validParts, part)
		}
	}
	if len(validParts) > 0 {
		translatorcommon.SetRawArrayItemsDoc(outDoc, "candidates.0.content.parts", validParts)
	}
	if finishReason != "" {
		_ = outDoc.Set("candidates.0.finishReason", finishReason)
	}
	if model := firstNonEmptyInteractionString(st.Model, modelName); model != "" {
		_ = outDoc.Set("modelVersion", model)
	}
	if id := st.ID; id != "" {
		_ = outDoc.Set("responseId", id)
	}
	if st.ServiceTier != "" {
		_ = outDoc.Set("usageMetadata.serviceTier", st.ServiceTier)
	}
	setGeminiUsageMetadataFromInteractionsUsageDoc(outDoc, usage)
	return outDoc.Bytes()
}

func setGeminiUsageMetadataFromInteractionsUsageDoc(outDoc *ojson.Doc, usage ojson.Result) {
	if !usage.Exists() {
		return
	}
	inputTokens, hasInputTokens := interactionsUsageInt(usage, "input_tokens", "total_input_tokens")
	outputTokens, hasOutputTokens := interactionsUsageInt(usage, "output_tokens", "total_output_tokens")
	totalTokens, hasTotalTokens := interactionsUsageInt(usage, "total_tokens")
	if hasInputTokens {
		_ = outDoc.Set("usageMetadata.promptTokenCount", inputTokens)
		_ = outDoc.SetRaw("usageMetadata.promptTokensDetails", fmt.Sprintf(`[{"modality":"TEXT","tokenCount":%d}]`, inputTokens))
	}
	if hasOutputTokens {
		_ = outDoc.Set("usageMetadata.candidatesTokenCount", outputTokens)
	}
	if hasTotalTokens {
		_ = outDoc.Set("usageMetadata.totalTokenCount", totalTokens)
	} else if hasInputTokens || hasOutputTokens {
		_ = outDoc.Set("usageMetadata.totalTokenCount", inputTokens+outputTokens)
	}
	if thoughtTokens, ok := interactionsUsageInt(usage, "reasoning_tokens", "total_thought_tokens"); ok {
		_ = outDoc.Set("usageMetadata.thoughtsTokenCount", thoughtTokens)
	}
	if cachedTokens, ok := interactionsUsageInt(usage, "cached_tokens", "total_cached_tokens"); ok {
		_ = outDoc.Set("usageMetadata.cachedContentTokenCount", cachedTokens)
	}
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

func interactionsUsageInt(usage ojson.Result, paths ...string) (int64, bool) {
	for _, path := range paths {
		if value := usage.Get(path); value.Exists() {
			return value.Int(), true
		}
	}
	return 0, false
}

func firstExistingInteractionResult(root ojson.Result, paths ...string) ojson.Result {
	for _, path := range paths {
		if value := root.Get(path); value.Exists() {
			return value
		}
	}
	return ojson.Result{}
}

func setInteractionsGeminiRawObjectDoc(doc *ojson.Doc, path string, value ojson.Result) {
	if !value.Exists() {
		_ = doc.SetRaw(path, `{}`)
		return
	}
	if value.Type == ojson.String {
		raw := strings.TrimSpace(value.String())
		if raw != "" && ojson.Valid(raw) {
			_ = doc.SetRaw(path, raw)
			return
		}
	}
	if value.Raw != "" {
		_ = doc.SetRaw(path, value.Raw)
	}
}

func setInteractionsGeminiFunctionResponseDoc(doc *ojson.Doc, path string, value ojson.Result) {
	if !value.Exists() {
		_ = doc.SetRaw(path, `{}`)
		return
	}
	if value.Type == ojson.String {
		raw := strings.TrimSpace(value.String())
		if raw != "" && ojson.Valid(raw) {
			translatorcommon.SetGeminiFunctionResponseRawDoc(doc, path, raw)
			return
		}
	}
	if value.Raw != "" {
		translatorcommon.SetGeminiFunctionResponseResultDoc(doc, path, value)
	}
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
