package interactions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

type codexToInteractionsStreamState struct {
	Started              bool
	Completed            bool
	Done                 bool
	ActiveStepOpen       bool
	ActiveStepType       string
	ActiveStepIndex      int
	StepIndex            int
	ID                   string
	Model                string
	CreatedAt            int64
	HasOutputText        bool
	HasThoughtSummary    bool
	StreamedSummaryItems map[string]bool
	FunctionCallName     string
	FunctionCallID       string
	// SeenReasoningSignatures dedupes repeated reasoning output_item.done
	// events so one signature is emitted as a thought_signature delta once.
	SeenReasoningSignatures map[[32]byte]bool
}

func ConvertCodexResponseToInteractions(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	_ = ctx
	_ = originalRequestRawJSON
	_ = requestRawJSON
	if param == nil {
		var local any
		param = &local
	}
	if *param == nil {
		*param = &codexToInteractionsStreamState{
			ID:    fmt.Sprintf("interaction_%d", time.Now().UnixNano()),
			Model: modelName,
		}
	}
	st := (*param).(*codexToInteractionsStreamState)
	payload := codexStreamPayload(rawJSON)
	if bytes.Equal(payload, []byte("[DONE]")) {
		out := appendCodexInteractionsStepStop(nil, st)
		if !st.Completed {
			out = appendCodexInteractionsCompleted(out, st, ojson.Result{})
		}
		return appendCodexInteractionsDone(out, st)
	}
	if len(payload) == 0 {
		return nil
	}
	root := util.ParseGJSONBytesNoCopy(payload)
	switch root.Get("type").String() {
	case "response.created":
		return appendCodexInteractionsCreated(nil, st, root.Get("response"))
	case "response.output_item.added":
		return codexOutputItemAddedToInteractions(st, root)
	case "response.output_text.delta":
		return codexOutputTextDeltaToInteractions(st, root)
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return codexReasoningDeltaToInteractions(st, root)
	case "response.function_call_arguments.delta":
		return codexFunctionArgumentsDeltaToInteractions(st, root)
	case "response.output_item.done":
		return codexOutputItemDoneToInteractions(st, root.Get("item"))
	case "response.completed", "response.incomplete":
		out := appendCodexInteractionsCreated(nil, st, root.Get("response"))
		out = appendCodexInteractionsStepStop(out, st)
		out = appendCodexInteractionsCompleted(out, st, root.Get("response"))
		return appendCodexInteractionsDone(out, st)
	default:
		return nil
	}
}

func ConvertCodexResponseToInteractionsNonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	_ = ctx
	_ = originalRequestRawJSON
	_ = requestRawJSON
	root := util.ParseGJSONBytesNoCopy(rawJSON)
	response := root.Get("response")
	if !response.Exists() {
		response = root
	}
	outDoc := ojson.NewDocString(`{"id":"","object":"interaction","status":"completed","model":"","steps":[]}`)
	if status := response.Get("status").String(); status != "" {
		_ = outDoc.Set("status", status)
	}
	id := response.Get("id").String()
	if id == "" {
		id = fmt.Sprintf("interaction_%d", time.Now().UnixNano())
	}
	_ = outDoc.Set("id", id)
	if model := response.Get("model").String(); model != "" {
		_ = outDoc.Set("model", model)
	} else {
		_ = outDoc.Set("model", modelName)
	}
	var steps [][]byte
	response.Get("output").ForEach(func(_, item ojson.Result) bool {
		switch item.Get("type").String() {
		case "message":
			if step := buildCodexMessageItemToInteractions(item); len(step) > 0 {
				steps = append(steps, step)
			}
		case "reasoning":
			if step := buildCodexReasoningItemToInteractions(item); len(step) > 0 {
				steps = append(steps, step)
			}
		case "function_call", "tool_call":
			if step := buildCodexFunctionCallItemToInteractions(item); len(step) > 0 {
				steps = append(steps, step)
			}
		case "image_generation_call":
			if step := buildCodexImageItemToInteractions(item); len(step) > 0 {
				steps = append(steps, step)
			}
		}
		return true
	})
	if len(steps) > 0 {
		translatorcommon.SetRawArrayItemsDoc(outDoc, "steps", steps)
	}
	setCodexInteractionsUsageDoc(outDoc, "usage", response.Get("usage"), false)
	return outDoc.Bytes()
}

func codexStreamPayload(rawJSON []byte) []byte {
	rawJSON = bytes.TrimSpace(rawJSON)
	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[len("data:"):])
	}
	return rawJSON
}

func codexStreamEventType(rawJSON []byte) string {
	payload := codexStreamPayload(rawJSON)
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return ""
	}
	return ojson.GetBytesNoCopy(payload, "type").String()
}

func appendCodexInteractionsCreated(out [][]byte, st *codexToInteractionsStreamState, response ojson.Result) [][]byte {
	if st.Started {
		return out
	}
	if id := response.Get("id").String(); id != "" {
		st.ID = strings.Clone(id)
	}
	if model := response.Get("model").String(); model != "" {
		st.Model = strings.Clone(model)
	}
	if createdAt := response.Get("created_at"); createdAt.Exists() {
		st.CreatedAt = createdAt.Int()
	}
	createdDoc := ojson.NewDocString(`{"interaction":{"id":"","status":"in_progress","object":"interaction","model":""},"event_type":"interaction.created"}`)
	_ = createdDoc.Set("interaction.id", st.ID)
	_ = createdDoc.Set("interaction.model", st.Model)
	out = append(out, translatorcommon.SSEEventData("interaction.created", createdDoc.Bytes()))
	statusUpdate := []byte(`{"interaction_id":"","status":"in_progress","event_type":"interaction.status_update"}`)
	statusUpdate, _ = ojson.SetBytes(statusUpdate, "interaction_id", st.ID)
	out = append(out, translatorcommon.SSEEventData("interaction.status_update", statusUpdate))
	st.Started = true
	return out
}

func appendCodexInteractionsCompleted(out [][]byte, st *codexToInteractionsStreamState, response ojson.Result) [][]byte {
	if st.Completed {
		return out
	}
	created := time.Now().UTC()
	if st.CreatedAt > 0 {
		created = time.Unix(st.CreatedAt, 0).UTC()
	}
	completedDoc := ojson.NewDocString(`{"interaction":{"id":"","status":"completed","usage":{},"created":"","updated":"","service_tier":"standard","object":"interaction","model":""},"event_type":"interaction.completed"}`)
	_ = completedDoc.Set("interaction.id", st.ID)
	_ = completedDoc.Set("interaction.created", created.Format(time.RFC3339))
	_ = completedDoc.Set("interaction.updated", time.Now().UTC().Format(time.RFC3339))
	_ = completedDoc.Set("interaction.model", st.Model)
	if status := response.Get("status").String(); status != "" {
		_ = completedDoc.Set("interaction.status", status)
	}
	setCodexInteractionsUsageDoc(completedDoc, "interaction.usage", response.Get("usage"), true)
	out = append(out, translatorcommon.SSEEventData("interaction.completed", completedDoc.Bytes()))
	st.Completed = true
	return out
}

func appendCodexInteractionsDone(out [][]byte, st *codexToInteractionsStreamState) [][]byte {
	if st.Done {
		return out
	}
	out = append(out, translatorcommon.SSEEventData("done", []byte("[DONE]")))
	st.Done = true
	return out
}

func codexOutputItemAddedToInteractions(st *codexToInteractionsStreamState, root ojson.Result) [][]byte {
	out := appendCodexInteractionsCreated(nil, st, root.Get("response"))
	item := root.Get("item")
	switch item.Get("type").String() {
	case "message":
		return ensureCodexInteractionsStep(out, st, "model_output", item)
	case "reasoning":
		return ensureCodexInteractionsStep(out, st, "thought", item)
	case "function_call", "tool_call":
		st.FunctionCallName = strings.Clone(item.Get("name").String())
		st.FunctionCallID = strings.Clone(codexItemCallID(item))
		return ensureCodexInteractionsStep(out, st, "function_call", item)
	}
	return out
}

func codexOutputTextDeltaToInteractions(st *codexToInteractionsStreamState, root ojson.Result) [][]byte {
	out := appendCodexInteractionsCreated(nil, st, root.Get("response"))
	out = ensureCodexInteractionsStep(out, st, "model_output", ojson.Result{})
	deltaDoc := ojson.NewDocString(`{"index":0,"delta":{"text":"","type":"text"},"event_type":"step.delta"}`)
	_ = deltaDoc.Set("index", st.ActiveStepIndex)
	_ = deltaDoc.Set("delta.text", root.Get("delta").String())
	st.HasOutputText = true
	return append(out, translatorcommon.SSEEventData("step.delta", deltaDoc.Bytes()))
}

func codexReasoningDeltaToInteractions(st *codexToInteractionsStreamState, root ojson.Result) [][]byte {
	out := appendCodexInteractionsCreated(nil, st, root.Get("response"))
	out = ensureCodexInteractionsStep(out, st, "thought", ojson.Result{})
	deltaDoc := ojson.NewDocString(`{"index":0,"delta":{"content":{"text":"","type":"text"},"type":"thought_summary"},"event_type":"step.delta"}`)
	_ = deltaDoc.Set("index", st.ActiveStepIndex)
	_ = deltaDoc.Set("delta.content.text", root.Get("delta").String())
	if itemID := strings.TrimSpace(root.Get("item_id").String()); itemID != "" {
		if st.StreamedSummaryItems == nil {
			st.StreamedSummaryItems = make(map[string]bool)
		}
		st.StreamedSummaryItems[strings.Clone(itemID)] = true
	}
	st.HasThoughtSummary = true
	return append(out, translatorcommon.SSEEventData("step.delta", deltaDoc.Bytes()))
}

func codexFunctionArgumentsDeltaToInteractions(st *codexToInteractionsStreamState, root ojson.Result) [][]byte {
	out := appendCodexInteractionsCreated(nil, st, root.Get("response"))
	out = ensureCodexInteractionsStep(out, st, "function_call", root.Get("item"))
	deltaDoc := ojson.NewDocString(`{"index":0,"delta":{"arguments":"","type":"arguments_delta"},"event_type":"step.delta"}`)
	_ = deltaDoc.Set("index", st.ActiveStepIndex)
	_ = deltaDoc.Set("delta.arguments", root.Get("delta").String())
	return append(out, translatorcommon.SSEEventData("step.delta", deltaDoc.Bytes()))
}

func codexOutputItemDoneToInteractions(st *codexToInteractionsStreamState, item ojson.Result) [][]byte {
	out := appendCodexInteractionsCreated(nil, st, ojson.Result{})
	switch item.Get("type").String() {
	case "message":
		if st.HasOutputText {
			return appendCodexInteractionsStepStop(out, st)
		}
		out = appendCodexMessageItemToInteractionsStream(out, st, item)
		return appendCodexInteractionsStepStop(out, st)
	case "reasoning":
		out = appendCodexReasoningItemToInteractionsStream(out, st, item)
		return appendCodexInteractionsStepStop(out, st)
	case "function_call", "tool_call":
		out = appendCodexFunctionCallItemToInteractionsStream(out, st, item)
		return appendCodexInteractionsStepStop(out, st)
	case "image_generation_call":
		out = appendCodexImageItemToInteractionsStream(out, st, item)
		return appendCodexInteractionsStepStop(out, st)
	}
	return out
}

func ensureCodexInteractionsStep(out [][]byte, st *codexToInteractionsStreamState, stepType string, item ojson.Result) [][]byte {
	if st.ActiveStepOpen && st.ActiveStepType == stepType {
		return out
	}
	out = appendCodexInteractionsStepStop(out, st)
	return appendCodexInteractionsStepStart(out, st, stepType, item)
}

func appendCodexInteractionsStepStart(out [][]byte, st *codexToInteractionsStreamState, stepType string, item ojson.Result) [][]byte {
	st.ActiveStepIndex = st.StepIndex
	st.StepIndex++
	st.ActiveStepOpen = true
	st.ActiveStepType = stepType
	if stepType == "thought" {
		st.HasThoughtSummary = false
	}
	stepStartDoc := ojson.NewDocString(`{"index":0,"step":{"type":""},"event_type":"step.start"}`)
	_ = stepStartDoc.Set("index", st.ActiveStepIndex)
	_ = stepStartDoc.Set("step.type", stepType)
	if stepType == "function_call" {
		name := item.Get("name").String()
		if name == "" {
			name = st.FunctionCallName
		}
		callID := codexItemCallID(item)
		if callID == "" {
			callID = st.FunctionCallID
		}
		if callID == "" {
			callID = fmt.Sprintf("step_%d", time.Now().UnixNano())
		}
		_ = stepStartDoc.Set("step.id", callID)
		_ = stepStartDoc.Set("step.call_id", callID)
		_ = stepStartDoc.Set("step.name", name)
		_ = stepStartDoc.SetRawBytes("step.arguments", []byte(`{}`))
	}
	return append(out, translatorcommon.SSEEventData("step.start", stepStartDoc.Bytes()))
}

func appendCodexInteractionsStepStop(out [][]byte, st *codexToInteractionsStreamState) [][]byte {
	if !st.ActiveStepOpen {
		return out
	}
	stepStop := []byte(`{"index":0,"event_type":"step.stop"}`)
	stepStop, _ = ojson.SetBytes(stepStop, "index", st.ActiveStepIndex)
	out = append(out, translatorcommon.SSEEventData("step.stop", stepStop))
	if st.ActiveStepType == "thought" {
		st.HasThoughtSummary = false
	}
	st.ActiveStepOpen = false
	st.ActiveStepType = ""
	return out
}

func buildCodexMessageItemToInteractions(item ojson.Result) []byte {
	var contents [][]byte
	item.Get("content").ForEach(func(_, content ojson.Result) bool {
		if contentItem := codexContentToInteractionsContent(content); len(contentItem) > 0 {
			contents = append(contents, contentItem)
		}
		return true
	})
	if len(contents) == 0 {
		return nil
	}
	step := []byte(`{"type":"model_output","content":[]}`)
	return translatorcommon.SetRawArrayItems(step, "content", contents)
}

func buildCodexReasoningItemToInteractions(item ojson.Result) []byte {
	text := codexReasoningText(item)
	sig := strings.TrimSpace(item.Get("encrypted_content").String())
	if text == "" && sig == "" {
		return nil
	}
	stepDoc := ojson.NewDocString(`{"type":"thought"}`)
	if sig != "" {
		_ = stepDoc.Set("signature", sig)
	}
	if text != "" {
		_ = stepDoc.SetRawBytes("content", []byte(`[{"type":"text","text":""}]`))
		_ = stepDoc.Set("content.0.text", text)
	}
	return stepDoc.Bytes()
}

func buildCodexFunctionCallItemToInteractions(item ojson.Result) []byte {
	stepDoc := ojson.NewDocString(`{"type":"function_call","name":"","arguments":{}}`)
	_ = stepDoc.Set("name", item.Get("name").String())
	if callID := codexItemCallID(item); callID != "" {
		_ = stepDoc.Set("call_id", callID)
	}
	if args := codexArgumentsJSON(item.Get("arguments")); len(args) > 0 {
		_ = stepDoc.SetRawBytes("arguments", args)
	}
	return stepDoc.Bytes()
}

func buildCodexImageItemToInteractions(item ojson.Result) []byte {
	result := item.Get("result").String()
	if result == "" {
		return nil
	}
	stepDoc := ojson.NewDocString(`{"type":"model_output","content":[{"type":"image","mime_type":"","data":""}]}`)
	_ = stepDoc.Set("content.0.mime_type", mimeTypeFromCodexOutputFormat(item.Get("output_format").String()))
	_ = stepDoc.Set("content.0.data", result)
	return stepDoc.Bytes()
}

func appendCodexMessageItemToInteractionsStream(out [][]byte, st *codexToInteractionsStreamState, item ojson.Result) [][]byte {
	item.Get("content").ForEach(func(_, content ojson.Result) bool {
		if text := codexContentText(content); text != "" {
			out = ensureCodexInteractionsStep(out, st, "model_output", item)
			deltaDoc := ojson.NewDocString(`{"index":0,"delta":{"text":"","type":"text"},"event_type":"step.delta"}`)
			_ = deltaDoc.Set("index", st.ActiveStepIndex)
			_ = deltaDoc.Set("delta.text", text)
			out = append(out, translatorcommon.SSEEventData("step.delta", deltaDoc.Bytes()))
		}
		return true
	})
	return out
}

func appendCodexReasoningItemToInteractionsStream(out [][]byte, st *codexToInteractionsStreamState, item ojson.Result) [][]byte {
	text := codexReasoningText(item)
	sig := strings.TrimSpace(item.Get("encrypted_content").String())
	if sig != "" {
		key := sha256.Sum256([]byte(item.Get("id").String() + "\x00" + sig))
		if st.SeenReasoningSignatures[key] {
			return out
		}
		if st.SeenReasoningSignatures == nil {
			st.SeenReasoningSignatures = make(map[[32]byte]bool)
		}
		st.SeenReasoningSignatures[key] = true
	}
	itemID := strings.TrimSpace(item.Get("id").String())
	hadStreamedSummary := st.HasThoughtSummary
	if itemID != "" && len(st.StreamedSummaryItems) > 0 {
		hadStreamedSummary = st.StreamedSummaryItems[itemID]
	}
	if text != "" && !hadStreamedSummary {
		out = ensureCodexInteractionsStep(out, st, "thought", item)
		deltaDoc := ojson.NewDocString(`{"index":0,"delta":{"content":{"text":"","type":"text"},"type":"thought_summary"},"event_type":"step.delta"}`)
		_ = deltaDoc.Set("index", st.ActiveStepIndex)
		_ = deltaDoc.Set("delta.content.text", text)
		out = append(out, translatorcommon.SSEEventData("step.delta", deltaDoc.Bytes()))
	}
	if sig != "" {
		out = ensureCodexInteractionsStep(out, st, "thought", item)
		signatureDeltaDoc := ojson.NewDocString(`{"index":0,"delta":{"signature":"","type":"thought_signature"},"event_type":"step.delta"}`)
		_ = signatureDeltaDoc.Set("index", st.ActiveStepIndex)
		_ = signatureDeltaDoc.Set("delta.signature", sig)
		out = append(out, translatorcommon.SSEEventData("step.delta", signatureDeltaDoc.Bytes()))
	}
	// Completing a reasoning item resets the current item's summary flag so a
	// subsequent done-only reasoning item is not suppressed by an earlier
	// item's streamed deltas.
	st.HasThoughtSummary = false
	return out
}

func appendCodexFunctionCallItemToInteractionsStream(out [][]byte, st *codexToInteractionsStreamState, item ojson.Result) [][]byte {
	out = ensureCodexInteractionsStep(out, st, "function_call", item)
	deltaDoc := ojson.NewDocString(`{"index":0,"delta":{"arguments":"","type":"arguments_delta"},"event_type":"step.delta"}`)
	_ = deltaDoc.Set("index", st.ActiveStepIndex)
	_ = deltaDoc.Set("delta.arguments", item.Get("arguments").String())
	return append(out, translatorcommon.SSEEventData("step.delta", deltaDoc.Bytes()))
}

func appendCodexImageItemToInteractionsStream(out [][]byte, st *codexToInteractionsStreamState, item ojson.Result) [][]byte {
	result := item.Get("result").String()
	if result == "" {
		return out
	}
	out = ensureCodexInteractionsStep(out, st, "model_output", item)
	deltaDoc := ojson.NewDocString(`{"index":0,"delta":{"content":{"type":"image","mime_type":"","data":""},"type":"content"},"event_type":"step.delta"}`)
	_ = deltaDoc.Set("index", st.ActiveStepIndex)
	_ = deltaDoc.Set("delta.content.mime_type", mimeTypeFromCodexOutputFormat(item.Get("output_format").String()))
	_ = deltaDoc.Set("delta.content.data", result)
	return append(out, translatorcommon.SSEEventData("step.delta", deltaDoc.Bytes()))
}

func codexContentToInteractionsContent(content ojson.Result) []byte {
	if text := codexContentText(content); text != "" {
		item := []byte(`{"type":"text","text":""}`)
		item, _ = ojson.SetBytes(item, "text", text)
		return item
	}
	return nil
}

func codexContentText(content ojson.Result) string {
	for _, path := range []string{"text", "content"} {
		if value := content.Get(path); value.Exists() && value.Type == ojson.String {
			return value.String()
		}
	}
	return ""
}

func codexReasoningText(item ojson.Result) string {
	if content := item.Get("content"); content.Exists() {
		if content.Type == ojson.String {
			return content.String()
		}
		if content.IsArray() {
			var builder strings.Builder
			content.ForEach(func(_, part ojson.Result) bool {
				text := codexContentText(part)
				if text == "" {
					text = part.Get("summary_text").String()
				}
				if text == "" {
					return true
				}
				if builder.Len() > 0 {
					builder.WriteByte('\n')
				}
				builder.WriteString(text)
				return true
			})
			return builder.String()
		}
	}
	if summary := item.Get("summary"); summary.Exists() {
		if summary.Type == ojson.String {
			return summary.String()
		}
		if summary.IsArray() {
			var builder strings.Builder
			summary.ForEach(func(_, part ojson.Result) bool {
				text := codexContentText(part)
				if text == "" {
					return true
				}
				if builder.Len() > 0 {
					builder.WriteByte('\n')
				}
				builder.WriteString(text)
				return true
			})
			return builder.String()
		}
	}
	return ""
}

func codexItemCallID(item ojson.Result) string {
	if callID := strings.TrimSpace(item.Get("call_id").String()); callID != "" {
		return callID
	}
	return strings.TrimSpace(item.Get("id").String())
}

func codexArgumentsJSON(arguments ojson.Result) []byte {
	if !arguments.Exists() {
		return nil
	}
	if arguments.Type == ojson.String {
		parsed := ojson.Parse(arguments.String())
		if parsed.Exists() && parsed.IsObject() {
			return []byte(arguments.String())
		}
		return []byte(`{}`)
	}
	if arguments.IsObject() {
		return []byte(arguments.Raw)
	}
	return nil
}

func setCodexInteractionsUsageDoc(doc *ojson.Doc, path string, usage ojson.Result, stream bool) {
	if !usage.Exists() {
		return
	}
	inputTokens := usage.Get("input_tokens").Int()
	outputTokens := usage.Get("output_tokens").Int()
	if inputTokens == 0 {
		inputTokens = usage.Get("prompt_tokens").Int()
	}
	if outputTokens == 0 {
		outputTokens = usage.Get("completion_tokens").Int()
	}
	totalTokens := usage.Get("total_tokens").Int()
	if totalTokens == 0 {
		totalTokens = inputTokens + outputTokens
	}
	reasoningTokens := usage.Get("output_tokens_details.reasoning_tokens").Int()
	if reasoningTokens == 0 {
		reasoningTokens = usage.Get("reasoning_tokens").Int()
	}
	cachedTokens := usage.Get("input_tokens_details.cached_tokens").Int()
	if cachedTokens == 0 {
		cachedTokens = usage.Get("cached_tokens").Int()
	}
	if stream {
		_ = doc.Set(path+".total_tokens", totalTokens)
		_ = doc.Set(path+".total_input_tokens", inputTokens)
		_ = doc.SetRawBytes(path+".input_tokens_by_modality", []byte(fmt.Sprintf(`[{"modality":"text","tokens":%d}]`, inputTokens)))
		_ = doc.Set(path+".total_cached_tokens", cachedTokens)
		_ = doc.Set(path+".total_output_tokens", outputTokens)
		_ = doc.Set(path+".total_tool_use_tokens", 0)
		_ = doc.Set(path+".total_thought_tokens", reasoningTokens)
		return
	}
	_ = doc.Set(path+".input_tokens", inputTokens)
	_ = doc.Set(path+".output_tokens", outputTokens)
	_ = doc.Set(path+".total_tokens", totalTokens)
	if reasoningTokens > 0 {
		_ = doc.Set(path+".reasoning_tokens", reasoningTokens)
	}
	if cachedTokens > 0 {
		_ = doc.Set(path+".cached_tokens", cachedTokens)
	}
}

func mimeTypeFromCodexOutputFormat(outputFormat string) string {
	if outputFormat == "" {
		return "image/png"
	}
	if strings.Contains(outputFormat, "/") {
		return outputFormat
	}
	switch strings.ToLower(outputFormat) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	default:
		return "image/png"
	}
}
