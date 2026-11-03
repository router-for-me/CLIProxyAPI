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

type antigravityToInteractionsStreamState struct {
	Started         bool
	Finished        bool
	Completed       bool
	Done            bool
	ActiveStepOpen  bool
	PendingUsage    ojson.Result
	HasPendingUsage bool
	UsageAfterSeen  bool
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
				completedRoot := ojson.Result{}
				if st.HasPendingUsage {
					completedRoot = st.PendingUsage
				}
				out = appendAntigravityInteractionsCompleted(out, st, modelName, completedRoot)
			}
			out = appendAntigravityInteractionsDone(out, st)
			continue
		}
		root := unwrapAntigravityResponse(util.ParseGJSONBytesNoCopy(payload))
		root = restoreInteractionsFunctionNames(root, st.ToolNameMap)
		if !root.Exists() {
			continue
		}
		if !st.Started {
			out = appendAntigravityInteractionsCreated(out, st, modelName)
			out = appendAntigravityInteractionsStatusUpdate(out, st)
			st.Started = true
		}
		root.Get("candidates.0.content.parts").ForEach(func(_, part ojson.Result) bool {
			out = appendAntigravityPartToInteractionsStream(out, st, part)
			return true
		})
		finishReason := root.Get("candidates.0.finishReason")
		hasFinish := finishReason.Exists() && strings.TrimSpace(finishReason.String()) != ""
		frameHasUsage := hasAntigravityStreamUsage(root)
		if frameHasUsage {
			st.PendingUsage = ojson.Parse(strings.Clone(root.Raw))
			st.HasPendingUsage = true
			if st.Finished {
				st.UsageAfterSeen = true
			}
		}
		if hasFinish && !st.Finished {
			out = appendAntigravityInteractionsStepStop(out, st)
			st.Finished = true
			if frameHasUsage {
				st.UsageAfterSeen = true
			}
		}
		// Complete only on usage observed at or after the finish frame.
		// Usage cached from earlier content frames is a fallback for the
		// explicit [DONE] terminator, not a completion trigger: a later
		// frame may still carry the final usage.
		if st.UsageAfterSeen && st.Finished && !st.Completed {
			out = appendAntigravityInteractionsCompleted(out, st, modelName, st.PendingUsage)
			out = appendAntigravityInteractionsDone(out, st)
		}
	}
	return out
}

func ConvertAntigravityResponseToInteractionsNonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	_ = ctx
	_ = originalRequestRawJSON
	_ = requestRawJSON
	root := unwrapAntigravityResponse(util.ParseGJSONBytesNoCopy(rawJSON))
	root = restoreInteractionsFunctionNames(root, util.DisambiguatedToolNameMap(originalRequestRawJSON))
	outDoc := ojson.NewDoc([]byte(`{"id":"","object":"interaction","status":"completed","model":"","steps":[]}`))
	id := root.Get("responseId").String()
	if id == "" {
		id = fmt.Sprintf("interaction_%d", time.Now().UnixNano())
	}
	_ = outDoc.Set("id", id)
	_ = outDoc.Set("model", modelName)
	var steps [][]byte
	root.Get("candidates.0.content.parts").ForEach(func(_, part ojson.Result) bool {
		for _, step := range antigravityPartToInteractionsSteps(part) {
			if len(step) > 0 {
				steps = append(steps, step)
			}
		}
		return true
	})
	if len(steps) > 0 {
		translatorcommon.SetRawArrayItemsDoc(outDoc, "steps", steps)
	}
	setInteractionsUsageFromAntigravityDoc(outDoc, "usage", root)
	return outDoc.Bytes()
}

func antigravityStreamPayloads(rawJSON []byte) [][]byte {
	trimmed := bytes.TrimSpace(rawJSON)
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		// The bare sentinel is not a JSON array; without this guard the
		// array branch below misparses it into a bogus payload.
		return [][]byte{trimmed}
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		return [][]byte{bytes.TrimSpace(trimmed[5:])}
	}
	root := util.ParseGJSONBytesNoCopy(trimmed)
	if root.IsArray() {
		payloads := make([][]byte, 0)
		root.ForEach(func(_, item ojson.Result) bool {
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

func unwrapAntigravityResponse(root ojson.Result) ojson.Result {
	if response := root.Get("response"); response.Exists() {
		response = restoreAntigravityUsageMetadata(response)
		return response
	}
	return restoreAntigravityUsageMetadata(root)
}

func restoreInteractionsFunctionNames(root ojson.Result, nameMap map[string]string) ojson.Result {
	if !root.Exists() || len(nameMap) == 0 {
		return root
	}
	var rawDoc *ojson.Doc
	candidates := root.Get("candidates")
	for candidateIndex, candidate := range candidates.Array() {
		for partIndex, part := range candidate.Get("content.parts").Array() {
			for _, field := range []string{"functionCall", "functionResponse"} {
				nameResult := part.Get(field + ".name")
				name := nameResult.String()
				if name == "" {
					continue
				}
				restoredName := util.RestoreSanitizedToolName(nameMap, name)
				if nameResult.Type == ojson.String && restoredName == name {
					continue
				}
				if rawDoc == nil {
					rawDoc = ojson.NewDocString(root.Raw)
				}
				path := fmt.Sprintf("candidates.%d.content.parts.%d.%s.name", candidateIndex, partIndex, field)
				_ = rawDoc.Set(path, restoredName)
			}
		}
	}
	if rawDoc != nil {
		return util.ParseGJSONBytesNoCopy(rawDoc.Bytes())
	}
	return root
}

func restoreAntigravityUsageMetadata(root ojson.Result) ojson.Result {
	if !root.Get("usageMetadata").Exists() {
		if cpaUsage := root.Get("cpaUsageMetadata"); cpaUsage.Exists() {
			rawDoc := ojson.NewDocString(root.Raw)
			_ = rawDoc.SetRaw("usageMetadata", cpaUsage.Raw)
			_ = rawDoc.Delete("cpaUsageMetadata")
			return util.ParseGJSONBytesNoCopy(rawDoc.Bytes())
		}
	}
	return root
}

func appendAntigravityInteractionsCreated(out [][]byte, st *antigravityToInteractionsStreamState, modelName string) [][]byte {
	createdDoc := ojson.NewDoc([]byte(`{"interaction":{"id":"","status":"in_progress","object":"interaction","model":""},"event_type":"interaction.created"}`))
	_ = createdDoc.Set("interaction.id", st.ID)
	_ = createdDoc.Set("interaction.model", modelName)
	return append(out, translatorcommon.SSEEventData("interaction.created", createdDoc.Bytes()))
}

func appendAntigravityInteractionsStatusUpdate(out [][]byte, st *antigravityToInteractionsStreamState) [][]byte {
	statusUpdate := []byte(`{"interaction_id":"","status":"in_progress","event_type":"interaction.status_update"}`)
	statusUpdate, _ = ojson.SetBytes(statusUpdate, "interaction_id", st.ID)
	return append(out, translatorcommon.SSEEventData("interaction.status_update", statusUpdate))
}

func appendAntigravityInteractionsCompleted(out [][]byte, st *antigravityToInteractionsStreamState, modelName string, root ojson.Result) [][]byte {
	now := time.Now().UTC().Format(time.RFC3339)
	completedDoc := ojson.NewDoc([]byte(`{"interaction":{"id":"","status":"completed","usage":{},"created":"","updated":"","service_tier":"standard","object":"interaction","model":""},"event_type":"interaction.completed"}`))
	_ = completedDoc.Set("interaction.id", st.ID)
	_ = completedDoc.Set("interaction.created", now)
	_ = completedDoc.Set("interaction.updated", now)
	_ = completedDoc.Set("interaction.model", modelName)
	if root.Exists() {
		setInteractionsStreamUsageFromAntigravityDoc(completedDoc, "interaction.usage", root)
	}
	out = append(out, translatorcommon.SSEEventData("interaction.completed", completedDoc.Bytes()))
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

func appendAntigravityInteractionsStepStart(out [][]byte, st *antigravityToInteractionsStreamState, stepType string, part ojson.Result) [][]byte {
	st.StepID = fmt.Sprintf("step_%d", time.Now().UnixNano())
	st.ActiveStepIndex = st.StepIndex
	st.StepIndex++
	st.ActiveStepType = strings.Clone(stepType)
	st.ActiveStepOpen = true
	stepStartDoc := ojson.NewDoc([]byte(`{"index":0,"step":{"type":""},"event_type":"step.start"}`))
	_ = stepStartDoc.Set("index", st.ActiveStepIndex)
	_ = stepStartDoc.Set("step.type", stepType)
	if stepType == "function_call" {
		id := antigravityFunctionPartID(part)
		if id == "" {
			id = st.StepID
		}
		_ = stepStartDoc.Set("step.id", id)
		_ = stepStartDoc.Set("step.call_id", id)
		_ = stepStartDoc.Set("step.name", part.Get("name").String())
		_ = stepStartDoc.SetRaw("step.arguments", `{}`)
	}
	return append(out, translatorcommon.SSEEventData("step.start", stepStartDoc.Bytes()))
}

func appendAntigravityInteractionsStepStop(out [][]byte, st *antigravityToInteractionsStreamState) [][]byte {
	if !st.ActiveStepOpen {
		return out
	}
	stepStop := []byte(`{"index":0,"event_type":"step.stop"}`)
	stepStop, _ = ojson.SetBytes(stepStop, "index", st.ActiveStepIndex)
	out = append(out, translatorcommon.SSEEventData("step.stop", stepStop))
	st.ActiveStepOpen = false
	st.ActiveStepType = ""
	return out
}

func ensureAntigravityInteractionsStep(out [][]byte, st *antigravityToInteractionsStreamState, stepType string, part ojson.Result) [][]byte {
	if st.ActiveStepOpen && st.ActiveStepType == stepType {
		return out
	}
	out = appendAntigravityInteractionsStepStop(out, st)
	return appendAntigravityInteractionsStepStart(out, st, stepType, part)
}

func appendAntigravityPartToInteractionsStream(out [][]byte, st *antigravityToInteractionsStreamState, part ojson.Result) [][]byte {
	if text := part.Get("text"); text.Exists() && text.String() != "" {
		if part.Get("thought").Bool() {
			out = ensureAntigravityInteractionsStep(out, st, "thought", ojson.Result{})
			deltaDoc := ojson.NewDoc([]byte(`{"index":0,"delta":{"content":{"text":"","type":"text"},"type":"thought_summary"},"event_type":"step.delta"}`))
			_ = deltaDoc.Set("index", st.ActiveStepIndex)
			_ = deltaDoc.Set("delta.content.text", text.String())
			out = append(out, translatorcommon.SSEEventData("step.delta", deltaDoc.Bytes()))
			return appendAntigravityThoughtSignature(out, st, part)
		}
		out = ensureAntigravityInteractionsStep(out, st, "model_output", ojson.Result{})
		deltaDoc := ojson.NewDoc([]byte(`{"index":0,"delta":{"text":"","type":"text"},"event_type":"step.delta"}`))
		_ = deltaDoc.Set("index", st.ActiveStepIndex)
		_ = deltaDoc.Set("delta.text", text.String())
		out = append(out, translatorcommon.SSEEventData("step.delta", deltaDoc.Bytes()))
		return appendAntigravityThoughtSignature(out, st, part)
	}
	if fc := part.Get("functionCall"); fc.Exists() {
		out = appendAntigravityThoughtSignature(out, st, part)
		out = ensureAntigravityInteractionsStep(out, st, "function_call", fc)
		deltaDoc := ojson.NewDoc([]byte(`{"index":0,"delta":{"arguments":"","type":"arguments_delta"},"event_type":"step.delta"}`))
		_ = deltaDoc.Set("index", st.ActiveStepIndex)
		arguments := `{}`
		if args := fc.Get("args"); args.Exists() {
			arguments = args.Raw
		}
		_ = deltaDoc.Set("delta.arguments", arguments)
		out = append(out, translatorcommon.SSEEventData("step.delta", deltaDoc.Bytes()))
		return appendAntigravityInteractionsStepStop(out, st)
	}
	if fr := part.Get("functionResponse"); fr.Exists() {
		out = ensureAntigravityInteractionsStep(out, st, "function_result", fr)
		deltaDoc := ojson.NewDoc([]byte(`{"index":0,"delta":{"type":"function_result","name":"","result":{}},"event_type":"step.delta"}`))
		_ = deltaDoc.Set("index", st.ActiveStepIndex)
		_ = deltaDoc.Set("delta.name", fr.Get("name").String())
		if response := fr.Get("response"); response.Exists() {
			_ = deltaDoc.SetRaw("delta.result", response.Raw)
		}
		out = append(out, translatorcommon.SSEEventData("step.delta", deltaDoc.Bytes()))
		return appendAntigravityInteractionsStepStop(out, st)
	}
	if sig := antigravityThoughtSignature(part); sig != "" {
		return appendAntigravityThoughtSignature(out, st, part)
	}
	return out
}

func appendAntigravityThoughtSignature(out [][]byte, st *antigravityToInteractionsStreamState, part ojson.Result) [][]byte {
	if signature := antigravityThoughtSignature(part); signature != "" {
		out = ensureAntigravityInteractionsStep(out, st, "thought", ojson.Result{})
		signatureDeltaDoc := ojson.NewDoc([]byte(`{"index":0,"delta":{"signature":"","type":"thought_signature"},"event_type":"step.delta"}`))
		_ = signatureDeltaDoc.Set("index", st.ActiveStepIndex)
		_ = signatureDeltaDoc.Set("delta.signature", signature)
		return append(out, translatorcommon.SSEEventData("step.delta", signatureDeltaDoc.Bytes()))
	}
	return out
}

func antigravityPartToInteractionsSteps(part ojson.Result) [][]byte {
	sig := antigravityThoughtSignature(part)
	if fc := part.Get("functionCall"); fc.Exists() {
		var steps [][]byte
		if sig != "" {
			steps = append(steps, antigravityThoughtStepJSON(sig, ""))
		}
		stepDoc := ojson.NewDoc([]byte(`{"type":"function_call","name":"","arguments":{}}`))
		_ = stepDoc.Set("name", fc.Get("name").String())
		if id := fc.Get("id"); id.Exists() {
			_ = stepDoc.Set("call_id", id.String())
		} else if callID := fc.Get("call_id"); callID.Exists() {
			_ = stepDoc.Set("call_id", callID.String())
		}
		if args := fc.Get("args"); args.Exists() {
			_ = stepDoc.SetRaw("arguments", args.Raw)
		}
		steps = append(steps, stepDoc.Bytes())
		return steps
	}
	if fr := part.Get("functionResponse"); fr.Exists() {
		stepDoc := ojson.NewDoc([]byte(`{"type":"function_result","name":"","result":{}}`))
		_ = stepDoc.Set("name", fr.Get("name").String())
		if id := fr.Get("id"); id.Exists() {
			_ = stepDoc.Set("call_id", id.String())
		} else if callID := fr.Get("call_id"); callID.Exists() {
			_ = stepDoc.Set("call_id", callID.String())
		}
		if response := fr.Get("response"); response.Exists() {
			_ = stepDoc.SetRaw("result", response.Raw)
		}
		return [][]byte{stepDoc.Bytes()}
	}
	if text := part.Get("text"); text.Exists() {
		if part.Get("thought").Bool() {
			return [][]byte{antigravityThoughtStepJSON(sig, text.String())}
		}
		if text.String() == "" {
			if sig != "" {
				return [][]byte{antigravityThoughtStepJSON(sig, "")}
			}
			return nil
		}
		step := []byte(`{"type":"model_output","content":[]}`)
		item := []byte(`{"type":"text","text":""}`)
		item, _ = ojson.SetBytes(item, "text", text.String())
		step = translatorcommon.SetRawArrayItems(step, "content", [][]byte{item})
		var steps [][]byte
		steps = append(steps, step)
		if sig != "" {
			steps = append(steps, antigravityThoughtStepJSON(sig, ""))
		}
		return steps
	}
	if inline := part.Get("inlineData"); inline.Exists() {
		if step := antigravityInlineDataToInteractionsStep(inline); len(step) > 0 {
			var steps [][]byte
			steps = append(steps, step)
			if sig != "" {
				steps = append(steps, antigravityThoughtStepJSON(sig, ""))
			}
			return steps
		}
	}
	if inline := part.Get("inline_data"); inline.Exists() {
		if step := antigravityInlineDataToInteractionsStep(inline); len(step) > 0 {
			var steps [][]byte
			steps = append(steps, step)
			if sig != "" {
				steps = append(steps, antigravityThoughtStepJSON(sig, ""))
			}
			return steps
		}
	}
	if sig != "" {
		return [][]byte{antigravityThoughtStepJSON(sig, "")}
	}
	return nil
}

func antigravityThoughtStepJSON(sig, text string) []byte {
	stepDoc := ojson.NewDoc([]byte(`{"type":"thought"}`))
	if sig != "" {
		_ = stepDoc.Set("signature", sig)
	}
	if text != "" {
		item := []byte(`{"type":"text","text":""}`)
		item, _ = ojson.SetBytes(item, "text", text)
		translatorcommon.SetRawArrayItemsDoc(stepDoc, "content", [][]byte{item})
	}
	return stepDoc.Bytes()
}

func antigravityPartToInteractionsStep(part ojson.Result) []byte {
	steps := antigravityPartToInteractionsSteps(part)
	if len(steps) == 0 {
		return nil
	}
	for _, s := range steps {
		if ojson.GetBytesNoCopy(s, "type").String() == "function_call" {
			return s
		}
	}
	return steps[0]
}

func antigravityInlineDataToInteractionsStep(inline ojson.Result) []byte {
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
	itemDoc := ojson.NewDoc([]byte(`{"type":"","mime_type":"","data":""}`))
	_ = itemDoc.Set("type", contentType)
	_ = itemDoc.Set("mime_type", mimeType)
	_ = itemDoc.Set("data", data)
	step, _ := ojson.SetRawBytes([]byte(`{"type":"model_output","content":[]}`), "content.-1", itemDoc.Bytes())
	return step
}

func hasAntigravityStreamUsage(root ojson.Result) bool {
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

func setInteractionsUsageFromAntigravityDoc(outDoc *ojson.Doc, path string, root ojson.Result) {
	usage := antigravityUsageNode(root)
	if !usage.Exists() {
		return
	}
	_ = outDoc.Set(path+".input_tokens", firstAntigravityUsageInt(usage, "promptTokenCount", "prompt_token_count"))
	_ = outDoc.Set(path+".output_tokens", firstAntigravityUsageInt(usage, "candidatesTokenCount", "candidates_token_count"))
	if antigravityUsagePathExists(usage, "thoughtsTokenCount", "thoughts_token_count") {
		_ = outDoc.Set(path+".reasoning_tokens", firstAntigravityUsageInt(usage, "thoughtsTokenCount", "thoughts_token_count"))
	}
	_ = outDoc.Set(path+".total_tokens", firstAntigravityUsageInt(usage, "totalTokenCount", "total_token_count"))
	if antigravityUsagePathExists(usage, "cachedContentTokenCount", "cached_content_token_count") {
		_ = outDoc.Set(path+".cached_tokens", firstAntigravityUsageInt(usage, "cachedContentTokenCount", "cached_content_token_count"))
	}
}

func setInteractionsStreamUsageFromAntigravityDoc(outDoc *ojson.Doc, path string, root ojson.Result) {
	usage := antigravityUsageNode(root)
	if !usage.Exists() {
		return
	}
	inputTokens := firstAntigravityUsageInt(usage, "promptTokenCount", "prompt_token_count")
	outputTokens := firstAntigravityUsageInt(usage, "candidatesTokenCount", "candidates_token_count")
	totalTokens := firstAntigravityUsageInt(usage, "totalTokenCount", "total_token_count")
	thoughtTokens := firstAntigravityUsageInt(usage, "thoughtsTokenCount", "thoughts_token_count")
	cachedTokens := firstAntigravityUsageInt(usage, "cachedContentTokenCount", "cached_content_token_count")
	_ = outDoc.Set(path+".total_tokens", totalTokens)
	_ = outDoc.Set(path+".total_input_tokens", inputTokens)
	_ = outDoc.SetRaw(path+".input_tokens_by_modality", fmt.Sprintf(`[{"modality":"text","tokens":%d}]`, inputTokens))
	_ = outDoc.Set(path+".total_cached_tokens", cachedTokens)
	_ = outDoc.Set(path+".total_output_tokens", outputTokens)
	_ = outDoc.Set(path+".total_tool_use_tokens", 0)
	_ = outDoc.Set(path+".total_thought_tokens", thoughtTokens)
}

func antigravityUsageNode(root ojson.Result) ojson.Result {
	if usage := root.Get("usageMetadata"); usage.Exists() {
		return usage
	}
	if usage := root.Get("usage_metadata"); usage.Exists() {
		return usage
	}
	if usage := root.Get("cpaUsageMetadata"); usage.Exists() {
		return usage
	}
	return ojson.Result{}
}

func firstAntigravityUsageInt(usage ojson.Result, paths ...string) int64 {
	for _, path := range paths {
		if value := usage.Get(path); value.Exists() {
			return value.Int()
		}
	}
	return 0
}

func antigravityUsagePathExists(usage ojson.Result, paths ...string) bool {
	for _, path := range paths {
		if usage.Get(path).Exists() {
			return true
		}
	}
	return false
}

func antigravityFunctionPartID(part ojson.Result) string {
	if id := part.Get("id"); id.Exists() {
		return id.String()
	}
	if callID := part.Get("call_id"); callID.Exists() {
		return callID.String()
	}
	return ""
}

func antigravityThoughtSignature(part ojson.Result) string {
	for _, path := range []string{"thoughtSignature", "thought_signature", "extra_content.google.thought_signature"} {
		if signature := strings.TrimSpace(part.Get(path).String()); signature != "" {
			return signature
		}
	}
	return ""
}
