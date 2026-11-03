package interactions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

type StreamState struct {
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
}

func ConvertInteractionsRequestToGemini(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertInteractionsRequestToGemini(modelName, inputRawJSON, stream)

}

// convertInteractionsRequestToGemini also reports a user turn that was left empty
// because its only attachment has no Gemini equivalent.
func convertInteractionsRequestToGemini(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	root := util.ParseGJSONBytesNoCopy(inputRawJSON)
	outDoc := ojson.NewDocString(`{"model":"","contents":[]}`)
	if modelName != "" && root.Get("model").Exists() {
		_ = outDoc.Set("model", modelName)
	}
	copyInteractionsSystemInstructionDoc(outDoc, root)
	copyInteractionsGenerationConfigDoc(outDoc, root)
	copyInteractionsResponseModalitiesDoc(outDoc, root)
	copyInteractionsToolsDoc(outDoc, root)
	copyInteractionsToolChoiceDoc(outDoc, root)
	copyInteractionsServiceTierDoc(outDoc, root)
	contentItems := translatorcommon.NewRawArrayItems(root.Get("input.#").Int())
	errInput := appendInteractionsInput(&contentItems, root.Get("input"))
	translatorcommon.SetRawArrayItemsDoc(outDoc, "contents", contentItems)
	return outDoc.Bytes(), errInput
}

func ConvertGeminiRequestToInteractions(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertGeminiRequestToInteractions(modelName, inputRawJSON, stream)

}

// convertGeminiRequestToInteractions also reports a user turn that was left empty
// because its only part could not be carried over to Interactions.
func convertGeminiRequestToInteractions(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	var drops translatorcommon.UserTurnDrops
	root := util.ParseGJSONBytesNoCopy(inputRawJSON)
	outDoc := ojson.NewDocString(`{"model":"","input":[]}`)
	_ = outDoc.Set("model", modelName)
	copyGeminiSystemInstructionToInteractionsDoc(outDoc, root)
	if root.Get("generationConfig").Exists() {
		converted := convertCamelCaseKeysToSnakeCase([]byte(root.Get("generationConfig").Raw))
		_ = outDoc.SetRawBytes("generation_config", converted)
		normalizeGeminiThinkingConfigForInteractionsDoc(outDoc, util.ParseGJSONBytesNoCopy(converted))
	}
	copyGeminiToolsToInteractionsDoc(outDoc, root)
	inputItems := translatorcommon.NewRawArrayItems(root.Get("contents.#").Int())
	root.Get("contents").ForEach(func(_, content ojson.Result) bool {
		role := content.Get("role").String()
		stepType := "user_input"
		if role == "model" {
			stepType = "model_output"
		}
		// Counts what this turn sends; empty text is not sendable.
		sendable := 0
		content.Get("parts").ForEach(func(_, part ojson.Result) bool {
			if fc := part.Get("functionCall"); fc.Exists() {
				for _, step := range geminiPartToInteractionsSteps(part) {
					if len(step) > 0 {
						inputItems = append(inputItems, step)
						sendable++
					}
				}
				return true
			}
			if fr := part.Get("functionResponse"); fr.Exists() {
				for _, step := range geminiPartToInteractionsSteps(part) {
					if len(step) > 0 {
						inputItems = append(inputItems, step)
						sendable++
					}
				}
				return true
			}
			if text := part.Get("text"); text.Exists() && text.String() == "" {
				for _, step := range geminiPartToInteractionsSteps(part) {
					if len(step) > 0 {
						inputItems = append(inputItems, step)
					}
				}
				return true
			}
			item := geminiPartToInteractionsContent(part)
			if len(item) == 0 {
				if role != "model" && geminiPartFileData(part).Exists() {
					drops.Drop("fileData")
				}
				return true
			}
			currentStepType := stepType
			if part.Get("thought").Bool() && role == "model" {
				currentStepType = "thought"
			}
			stepDoc := ojson.NewDocString(`{"type":"","content":[]}`)
			_ = stepDoc.Set("type", currentStepType)
			translatorcommon.SetRawArrayItemsDoc(stepDoc, "content", [][]byte{item})
			inputItems = append(inputItems, stepDoc.Bytes())
			sendable++
			return true
		})
		if role != "model" {
			drops.EndTurn(sendable)
		}
		return true
	})
	translatorcommon.SetRawArrayItemsDoc(outDoc, "input", inputItems)
	_ = outDoc.Set("stream", stream)
	return outDoc.Bytes(), drops.Err()
}

func copyGeminiSystemInstructionToInteractionsDoc(outDoc *ojson.Doc, root ojson.Result) {
	sys := root.Get("systemInstruction")
	if !sys.Exists() {
		sys = root.Get("system_instruction")
	}
	text := geminiSystemInstructionText(sys)
	if text == "" {
		return
	}
	_ = outDoc.Set("system_instruction", text)
}

func geminiSystemInstructionText(sys ojson.Result) string {
	if !sys.Exists() {
		return ""
	}
	if sys.Type == ojson.String {
		return sys.String()
	}
	if text := sys.Get("text"); text.Exists() && text.Type == ojson.String {
		return text.String()
	}
	parts := sys.Get("parts")
	if !parts.Exists() || !parts.IsArray() {
		return ""
	}
	var builder strings.Builder
	parts.ForEach(func(_, part ojson.Result) bool {
		text := part.Get("text").String()
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

func normalizeGeminiThinkingConfigForInteractionsDoc(outDoc *ojson.Doc, genCfg ojson.Result) {
	if level := firstExistingPath(genCfg, []string{
		"thinking_config.thinking_level",
		"thinkingConfig.thinkingLevel",
		"thinkingConfig.thinking_level",
	}); level.Exists() {
		_ = outDoc.Set("generation_config.thinking_level", strings.ToLower(strings.TrimSpace(level.String())))
	}
	if budget := firstExistingPath(genCfg, []string{
		"thinking_config.thinking_budget",
		"thinkingConfig.thinkingBudget",
		"thinkingConfig.thinking_budget",
	}); budget.Exists() {
		_ = outDoc.SetRaw("generation_config.thinking_budget", budget.Raw)
	}
	if !genCfg.Get("thinking_summaries").Exists() {
		if include := firstExistingPath(genCfg, []string{
			"thinking_config.include_thoughts",
			"thinking_config.includeThoughts",
			"thinkingConfig.include_thoughts",
			"thinkingConfig.includeThoughts",
		}); include.Exists() {
			summary := "none"
			if include.Bool() {
				summary = "auto"
			}
			_ = outDoc.Set("generation_config.thinking_summaries", summary)
		}
	}
}

func firstExistingPath(root ojson.Result, paths []string) ojson.Result {
	for _, path := range paths {
		if value := root.Get(path); value.Exists() {
			return value
		}
	}
	return ojson.Result{}
}

func copyGeminiToolsToInteractionsDoc(outDoc *ojson.Doc, root ojson.Result) {
	tools := root.Get("tools")
	if !tools.Exists() {
		return
	}
	if !tools.IsArray() {
		_ = outDoc.SetRaw("tools", tools.Raw)
		return
	}
	normalized := make([]map[string]any, 0)
	tools.ForEach(func(_, tool ojson.Result) bool {
		if uc := tool.Get("urlContext"); uc.Exists() {
			entry := map[string]any{"type": "url_context"}
			if uc.IsObject() && len(uc.Map()) > 0 {
				entry["url_context"] = json.RawMessage(uc.Raw)
			}
			normalized = append(normalized, entry)
		} else if uc := tool.Get("url_context"); uc.Exists() {
			entry := map[string]any{"type": "url_context"}
			if uc.IsObject() && len(uc.Map()) > 0 {
				entry["url_context"] = json.RawMessage(uc.Raw)
			}
			normalized = append(normalized, entry)
		}
		if ce := tool.Get("codeExecution"); ce.Exists() {
			entry := map[string]any{"type": "code_execution"}
			if ce.IsObject() && len(ce.Map()) > 0 {
				entry["code_execution"] = json.RawMessage(ce.Raw)
			}
			normalized = append(normalized, entry)
		} else if ce := tool.Get("code_execution"); ce.Exists() {
			entry := map[string]any{"type": "code_execution"}
			if ce.IsObject() && len(ce.Map()) > 0 {
				entry["code_execution"] = json.RawMessage(ce.Raw)
			}
			normalized = append(normalized, entry)
		}
		if gs := tool.Get("googleSearch"); gs.Exists() {
			entry := map[string]any{"type": "google_search"}
			if gs.IsObject() && len(gs.Map()) > 0 {
				entry["google_search"] = json.RawMessage(gs.Raw)
			}
			normalized = append(normalized, entry)
		} else if gs := tool.Get("google_search"); gs.Exists() {
			entry := map[string]any{"type": "google_search"}
			if gs.IsObject() && len(gs.Map()) > 0 {
				entry["google_search"] = json.RawMessage(gs.Raw)
			}
			normalized = append(normalized, entry)
		}
		if name := tool.Get("name"); name.Exists() {
			entry := map[string]any{
				"type": "function",
				"name": name.String(),
			}
			if desc := tool.Get("description"); desc.Exists() {
				entry["description"] = desc.String()
			}
			if params := tool.Get("parameters"); params.Exists() {
				entry["parameters"] = json.RawMessage(util.NormalizeJSONSchemaNumericConstraints(params.Raw))
			} else if params := tool.Get("parametersJsonSchema"); params.Exists() {
				entry["parameters"] = json.RawMessage(util.NormalizeJSONSchemaNumericConstraints(params.Raw))
			}
			normalized = append(normalized, entry)
			return true
		}
		decls := tool.Get("functionDeclarations")
		if !decls.Exists() {
			decls = tool.Get("function_declarations")
		}
		decls.ForEach(func(_, decl ojson.Result) bool {
			if name := decl.Get("name"); name.Exists() {
				entry := map[string]any{
					"type": "function",
					"name": name.String(),
				}
				if desc := decl.Get("description"); desc.Exists() {
					entry["description"] = desc.String()
				}
				if params := decl.Get("parameters"); params.Exists() {
					entry["parameters"] = json.RawMessage(util.NormalizeJSONSchemaNumericConstraints(params.Raw))
				} else if params := decl.Get("parametersJsonSchema"); params.Exists() {
					entry["parameters"] = json.RawMessage(util.NormalizeJSONSchemaNumericConstraints(params.Raw))
				}
				normalized = append(normalized, entry)
			}
			return true
		})
		return true
	})
	if len(normalized) == 0 {
		_ = outDoc.SetRaw("tools", tools.Raw)
		return
	}
	raw, errMarshal := json.Marshal(normalized)
	if errMarshal != nil {
		_ = outDoc.SetRaw("tools", tools.Raw)
		return
	}
	_ = outDoc.SetRawBytes("tools", raw)
}

func geminiPartToInteractionsContent(part ojson.Result) []byte {
	if text := part.Get("text"); text.Exists() {
		item := []byte(`{"type":"text","text":""}`)
		item, _ = ojson.SetBytes(item, "text", text.String())
		return item
	}
	if inline := part.Get("inlineData"); inline.Exists() {
		mimeType := inline.Get("mimeType").String()
		if mimeType == "" {
			mimeType = inline.Get("mime_type").String()
		}
		return geminiInlineDataToInteractionsContent(mimeType, inline.Get("data").String())
	}
	if inline := part.Get("inline_data"); inline.Exists() {
		return geminiInlineDataToInteractionsContent(inline.Get("mime_type").String(), inline.Get("data").String())
	}
	if fileData := geminiPartFileData(part); fileData.Exists() {
		return geminiFileDataToInteractionsContent(fileData)
	}
	return nil
}

// geminiPartFileData returns the fileData object of a Gemini part in either spelling.
func geminiPartFileData(part ojson.Result) ojson.Result {
	if fileData := part.Get("fileData"); fileData.Exists() {
		return fileData
	}
	return part.Get("file_data")
}

func ConvertGeminiResponseToInteractionsStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	_ = ctx
	// Vertex stream paths pass SSE lines, while the Gemini executor passes bare payloads.
	rawJSON = bytes.TrimSpace(rawJSON)
	if after, ok := bytes.CutPrefix(rawJSON, []byte("data:")); ok {
		rawJSON = bytes.TrimSpace(after)
	}
	if len(rawJSON) == 0 || rawJSON[0] == ':' || bytes.HasPrefix(rawJSON, []byte("event:")) || bytes.HasPrefix(rawJSON, []byte("id:")) || bytes.HasPrefix(rawJSON, []byte("retry:")) {
		return nil
	}
	if *param == nil {
		*param = &StreamState{ID: fmt.Sprintf("interaction_%d", time.Now().UnixNano())}
	}
	st := (*param).(*StreamState)
	if bytes.Equal(rawJSON, []byte("[DONE]")) {
		var out [][]byte
		if !st.Completed {
			out = appendInteractionsStepStop(out, st)
			completedRoot := ojson.Result{}
			if st.HasPendingUsage {
				completedRoot = st.PendingUsage
			}
			out = appendInteractionsCompleted(out, st, modelName, completedRoot)
		}
		return appendInteractionsDone(out, st)
	}
	root := util.ParseGJSONBytesNoCopy(rawJSON)
	var out [][]byte
	if !st.Started {
		out = appendInteractionsCreated(out, st, modelName)
		out = appendInteractionsStatusUpdate(out, st)
		st.Started = true
	}
	root.Get("candidates.0.content.parts").ForEach(func(_, part ojson.Result) bool {
		out = appendGeminiPartToInteractionsStream(out, st, part)
		return true
	})
	finishReason := root.Get("candidates.0.finishReason")
	hasFinish := finishReason.Exists() && strings.TrimSpace(finishReason.String()) != ""
	root = restoreInteractionsGeminiUsageMetadata(root)
	frameHasUsage := hasInteractionsGeminiStreamUsage(root)
	if frameHasUsage {
		st.PendingUsage = ojson.Parse(strings.Clone(root.Raw))
		st.HasPendingUsage = true
		if st.Finished {
			st.UsageAfterSeen = true
		}
	}
	if hasFinish && !st.Finished {
		out = appendInteractionsStepStop(out, st)
		st.Finished = true
		if frameHasUsage {
			st.UsageAfterSeen = true
		}
	}
	// Complete only on usage observed at or after the finish frame. Usage
	// cached from earlier content frames is a fallback for the explicit
	// [DONE] terminator, not a completion trigger: a later frame may still
	// carry the final usage.
	if st.UsageAfterSeen && st.Finished && !st.Completed {
		out = appendInteractionsCompleted(out, st, modelName, st.PendingUsage)
		out = appendInteractionsDone(out, st)
	}
	return out
}

// restoreInteractionsGeminiUsageMetadata renames cpaUsageMetadata back to
// usageMetadata. Executors rename non-terminal usage before translation, so
// the filtered spelling must be restored for usage detection and output.
func restoreInteractionsGeminiUsageMetadata(root ojson.Result) ojson.Result {
	if !root.Get("usageMetadata").Exists() && !root.Get("usage_metadata").Exists() {
		if cpaUsage := root.Get("cpaUsageMetadata"); cpaUsage.Exists() {
			doc := ojson.NewDocString(root.Raw)
			_ = doc.SetRaw("usageMetadata", cpaUsage.Raw)
			_ = doc.Delete("cpaUsageMetadata")
			return util.ParseGJSONBytesNoCopy(doc.Bytes())
		}
	}
	return root
}

func hasInteractionsGeminiStreamUsage(root ojson.Result) bool {
	usage := root.Get("usageMetadata")
	if !usage.Exists() {
		usage = root.Get("usage_metadata")
	}
	if !usage.Exists() {
		usage = root.Get("cpaUsageMetadata")
	}
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

func appendInteractionsCreated(out [][]byte, st *StreamState, modelName string) [][]byte {
	createdDoc := ojson.NewDocString(`{"interaction":{"id":"","status":"in_progress","object":"interaction","model":""},"event_type":"interaction.created"}`)
	_ = createdDoc.Set("interaction.id", st.ID)
	_ = createdDoc.Set("interaction.model", modelName)
	return append(out, translatorcommon.SSEEventData("interaction.created", createdDoc.Bytes()))
}

func appendInteractionsStatusUpdate(out [][]byte, st *StreamState) [][]byte {
	statusUpdate := []byte(`{"interaction_id":"","status":"in_progress","event_type":"interaction.status_update"}`)
	statusUpdate, _ = ojson.SetBytes(statusUpdate, "interaction_id", st.ID)
	return append(out, translatorcommon.SSEEventData("interaction.status_update", statusUpdate))
}

func appendInteractionsCompleted(out [][]byte, st *StreamState, modelName string, root ojson.Result) [][]byte {
	now := time.Now().UTC().Format(time.RFC3339)
	completedDoc := ojson.NewDocString(`{"interaction":{"id":"","status":"completed","usage":{},"created":"","updated":"","service_tier":"standard","object":"interaction","model":""},"event_type":"interaction.completed"}`)
	_ = completedDoc.Set("interaction.id", st.ID)
	_ = completedDoc.Set("interaction.created", now)
	_ = completedDoc.Set("interaction.updated", now)
	_ = completedDoc.Set("interaction.model", modelName)
	if root.Exists() {
		setInteractionsStreamUsageFromGeminiDoc(completedDoc, "interaction.usage", root)
	}
	out = append(out, translatorcommon.SSEEventData("interaction.completed", completedDoc.Bytes()))
	st.Completed = true
	return out
}

func appendInteractionsDone(out [][]byte, st *StreamState) [][]byte {
	if st.Done {
		return out
	}
	out = append(out, translatorcommon.SSEEventData("done", []byte("[DONE]")))
	st.Done = true
	return out
}

func convertGeminiResponseToInteractionsNonStreamDirect(modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte) []byte {
	_ = originalRequestRawJSON
	_ = requestRawJSON
	root := util.ParseGJSONBytesNoCopy(rawJSON)
	outDoc := ojson.NewDocString(`{"id":"","object":"interaction","status":"completed","model":"","steps":[]}`)
	id := root.Get("responseId").String()
	if id == "" {
		id = fmt.Sprintf("interaction_%d", time.Now().UnixNano())
	}
	_ = outDoc.Set("id", id)
	_ = outDoc.Set("model", modelName)
	var steps [][]byte
	root.Get("candidates.0.content.parts").ForEach(func(_, part ojson.Result) bool {
		for _, step := range geminiPartToInteractionsSteps(part) {
			if len(step) > 0 {
				steps = append(steps, step)
			}
		}
		return true
	})
	if len(steps) > 0 {
		translatorcommon.SetRawArrayItemsDoc(outDoc, "steps", steps)
	}
	setInteractionsUsageFromGeminiDoc(outDoc, "usage", root)
	return outDoc.Bytes()
}

func copyInteractionsSystemInstructionDoc(outDoc *ojson.Doc, root ojson.Result) {
	sys := root.Get("system_instruction")
	if !sys.Exists() {
		return
	}
	if sys.Type == ojson.String {
		instr := []byte(`{"parts":[{"text":""}]}`)
		instr, _ = ojson.SetBytes(instr, "parts.0.text", sys.String())
		_ = outDoc.SetRawBytes("systemInstruction", instr)
		return
	}
	if text := sys.Get("text"); text.Exists() && !sys.Get("parts").Exists() {
		instr := []byte(`{"parts":[{"text":""}]}`)
		instr, _ = ojson.SetBytes(instr, "parts.0.text", text.String())
		_ = outDoc.SetRawBytes("systemInstruction", instr)
		return
	}
	_ = outDoc.SetRaw("systemInstruction", sys.Raw)
}

func copyInteractionsGenerationConfigDoc(outDoc *ojson.Doc, root ojson.Result) {
	cfg := root.Get("generation_config")
	if !cfg.Exists() {
		cfg = root.Get("generationConfig")
		if !cfg.Exists() {
			return
		}
		_ = outDoc.SetRaw("generationConfig", cfg.Raw)
		normalizeInteractionsGenerationConfigDoc(outDoc, cfg)
		return
	}
	converted := convertSnakeCaseKeysToCamelCase([]byte(cfg.Raw))
	_ = outDoc.SetRawBytes("generationConfig", converted)
	normalizeInteractionsGenerationConfigDoc(outDoc, util.ParseGJSONBytesNoCopy(converted))
}

func normalizeInteractionsGenerationConfigDoc(outDoc *ojson.Doc, genCfg ojson.Result) {
	if toolChoice := genCfg.Get("toolChoice"); toolChoice.Exists() {
		_ = outDoc.Delete("generationConfig.toolChoice")
	}
	if thinkingLevel := genCfg.Get("thinkingLevel"); thinkingLevel.Exists() {
		_ = outDoc.SetRaw("generationConfig.thinkingConfig.thinkingLevel", thinkingLevel.Raw)
		_ = outDoc.Delete("generationConfig.thinkingLevel")
	}
	if thinkingBudget := genCfg.Get("thinkingBudget"); thinkingBudget.Exists() {
		_ = outDoc.SetRaw("generationConfig.thinkingConfig.thinkingBudget", thinkingBudget.Raw)
		_ = outDoc.Delete("generationConfig.thinkingBudget")
	}
	if includeThoughts := genCfg.Get("includeThoughts"); includeThoughts.Exists() {
		_ = outDoc.SetRaw("generationConfig.thinkingConfig.includeThoughts", includeThoughts.Raw)
		_ = outDoc.Delete("generationConfig.includeThoughts")
	}
	if summaries := genCfg.Get("thinkingSummaries"); summaries.Exists() {
		if includeThoughts, ok := interactionsThinkingSummariesIncludeThoughts(summaries); ok {
			_ = outDoc.Set("generationConfig.thinkingConfig.includeThoughts", includeThoughts)
		}
		_ = outDoc.Delete("generationConfig.thinkingSummaries")
	}
}

func interactionsThinkingSummariesIncludeThoughts(summary ojson.Result) (bool, bool) {
	if summary.Type != ojson.String {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(summary.String())) {
	case "auto":
		return true, true
	case "none":
		return false, true
	default:
		return false, false
	}
}

func copyInteractionsResponseModalitiesDoc(outDoc *ojson.Doc, root ojson.Result) {
	mods := root.Get("response_modalities")
	if !mods.Exists() {
		mods = root.Get("responseModalities")
	}
	if !mods.Exists() || !mods.IsArray() {
		return
	}
	var responseMods []string
	mods.ForEach(func(_, mod ojson.Result) bool {
		switch strings.ToLower(strings.TrimSpace(mod.String())) {
		case "text":
			responseMods = append(responseMods, "TEXT")
		case "image":
			responseMods = append(responseMods, "IMAGE")
		case "audio":
			responseMods = append(responseMods, "AUDIO")
		}
		return true
	})
	if len(responseMods) > 0 {
		_ = outDoc.Set("generationConfig.responseModalities", responseMods)
	}
}

func copyInteractionsToolChoiceDoc(outDoc *ojson.Doc, root ojson.Result) {
	toolChoice := root.Get("tool_choice")
	if !toolChoice.Exists() {
		toolChoice = root.Get("generation_config.tool_choice")
	}
	if !toolChoice.Exists() {
		toolChoice = root.Get("generationConfig.toolChoice")
	}
	if !toolChoice.Exists() {
		return
	}
	mode := ""
	var allowedNames []string
	if toolChoice.Type == ojson.String {
		switch strings.ToLower(strings.TrimSpace(toolChoice.String())) {
		case "none":
			mode = "NONE"
		case "auto":
			mode = "AUTO"
		case "required", "any":
			mode = "ANY"
		}
	} else if toolChoice.IsObject() {
		toolType := strings.ToLower(strings.TrimSpace(toolChoice.Get("type").String()))
		switch toolType {
		case "none":
			mode = "NONE"
		case "auto":
			mode = "AUTO"
		case "required", "any":
			mode = "ANY"
		case "function":
			mode = "ANY"
			if name := strings.TrimSpace(toolChoice.Get("function.name").String()); name != "" {
				allowedNames = append(allowedNames, name)
			}
		case "tool":
			mode = "ANY"
			if name := strings.TrimSpace(toolChoice.Get("name").String()); name != "" {
				allowedNames = append(allowedNames, name)
			}
		}
	}
	if mode == "" {
		return
	}
	_ = outDoc.Set("toolConfig.functionCallingConfig.mode", mode)
	if len(allowedNames) > 0 {
		_ = outDoc.Set("toolConfig.functionCallingConfig.allowedFunctionNames", allowedNames)
	}
}

func copyInteractionsServiceTierDoc(outDoc *ojson.Doc, root ojson.Result) {
	serviceTier := root.Get("service_tier")
	if !serviceTier.Exists() || serviceTier.Type != ojson.String {
		return
	}
	_ = outDoc.Set("service_tier", serviceTier.String())
}

func convertSnakeCaseKeysToCamelCase(raw []byte) []byte {
	root := util.ParseGJSONBytesNoCopy(raw)
	if !root.Exists() {
		return raw
	}
	outDoc := ojson.NewDocString(`{}`)
	copySnakeCaseValueToCamelCaseDoc(outDoc, "", root)
	return outDoc.Bytes()
}

func copySnakeCaseValueToCamelCaseDoc(outDoc *ojson.Doc, path string, node ojson.Result) {
	if node.IsObject() {
		node.ForEach(func(key, value ojson.Result) bool {
			childPath := joinJSONPath(path, toCamelCase(key.String()))
			copySnakeCaseValueToCamelCaseDoc(outDoc, childPath, value)
			return true
		})
		return
	}
	if node.IsArray() {
		node.ForEach(func(_, value ojson.Result) bool {
			childPath := path + ".-1"
			copySnakeCaseValueToCamelCaseDoc(outDoc, childPath, value)
			return true
		})
		return
	}
	_ = outDoc.SetRaw(path, node.Raw)
}

func joinJSONPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func toCamelCase(s string) string {
	parts := strings.Split(s, "_")
	if len(parts) == 0 {
		return s
	}
	out := parts[0]
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		out += strings.ToUpper(p[:1]) + p[1:]
	}
	return out
}

func convertCamelCaseKeysToSnakeCase(raw []byte) []byte {
	root := util.ParseGJSONBytesNoCopy(raw)
	if !root.Exists() {
		return raw
	}
	outDoc := ojson.NewDocString(`{}`)
	copyCamelCaseValueToSnakeCaseDoc(outDoc, "", root)
	return outDoc.Bytes()
}

func copyCamelCaseValueToSnakeCaseDoc(outDoc *ojson.Doc, path string, node ojson.Result) {
	if node.IsObject() {
		node.ForEach(func(key, value ojson.Result) bool {
			childPath := joinJSONPath(path, toSnakeCase(key.String()))
			copyCamelCaseValueToSnakeCaseDoc(outDoc, childPath, value)
			return true
		})
		return
	}
	if node.IsArray() {
		node.ForEach(func(_, value ojson.Result) bool {
			childPath := path + ".-1"
			copyCamelCaseValueToSnakeCaseDoc(outDoc, childPath, value)
			return true
		})
		return
	}
	_ = outDoc.SetRaw(path, node.Raw)
}

func toSnakeCase(s string) string {
	var out strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			out.WriteByte('_')
		}
		out.WriteRune(r)
	}
	return strings.ToLower(out.String())
}

func copyInteractionsToolsDoc(outDoc *ojson.Doc, root ojson.Result) {
	tools := root.Get("tools")
	if !tools.Exists() {
		return
	}
	if !tools.IsArray() {
		_ = outDoc.SetRaw("tools", tools.Raw)
		return
	}
	normalized := make([]map[string]any, 0)
	tools.ForEach(func(_, tool ojson.Result) bool {
		if tool.Get("functionDeclarations").Exists() {
			_ = outDoc.SetRaw("tools", tools.Raw)
			normalized = nil
			return false
		}
		entry := map[string]any{}
		toolType := tool.Get("type").String()
		switch toolType {
		case "url_context":
			raw := json.RawMessage(`{}`)
			if uc := tool.Get("url_context"); uc.Exists() && uc.IsObject() {
				raw = json.RawMessage(uc.Raw)
			} else if uc := tool.Get("urlContext"); uc.Exists() && uc.IsObject() {
				raw = json.RawMessage(uc.Raw)
			}
			entry["urlContext"] = raw
		case "code_execution":
			raw := json.RawMessage(`{}`)
			if ce := tool.Get("code_execution"); ce.Exists() && ce.IsObject() {
				raw = json.RawMessage(ce.Raw)
			} else if ce := tool.Get("codeExecution"); ce.Exists() && ce.IsObject() {
				raw = json.RawMessage(ce.Raw)
			}
			entry["codeExecution"] = raw
		case "google_search", "web_search":
			raw := json.RawMessage(`{}`)
			if gs := tool.Get("google_search"); gs.Exists() && gs.IsObject() {
				raw = json.RawMessage(gs.Raw)
			} else if gs := tool.Get("googleSearch"); gs.Exists() && gs.IsObject() {
				raw = json.RawMessage(gs.Raw)
			}
			entry["googleSearch"] = raw
		default:
			if decls := tool.Get("function_declarations"); decls.Exists() && decls.IsArray() {
				entry["functionDeclarations"] = json.RawMessage(decls.Raw)
			} else if name := tool.Get("name"); name.Exists() {
				decl := map[string]any{"name": name.String()}
				if desc := tool.Get("description"); desc.Exists() {
					decl["description"] = desc.String()
				}
				if params := tool.Get("parameters"); params.Exists() {
					decl["parameters"] = json.RawMessage(params.Raw)
				}
				entry["functionDeclarations"] = []map[string]any{decl}
			} else {
				var rawMap map[string]any
				if errUnmarshal := json.Unmarshal([]byte(tool.Raw), &rawMap); errUnmarshal == nil {
					if toolType == "" {
						if uc, ok := rawMap["url_context"]; ok {
							rawMap["urlContext"] = uc
							delete(rawMap, "url_context")
						}
						if ce, ok := rawMap["code_execution"]; ok {
							rawMap["codeExecution"] = ce
							delete(rawMap, "code_execution")
						}
						if gs, ok := rawMap["google_search"]; ok {
							rawMap["googleSearch"] = gs
							delete(rawMap, "google_search")
						}
						if ws, ok := rawMap["web_search"]; ok {
							rawMap["googleSearch"] = ws
							delete(rawMap, "web_search")
						}
					}
					entry = rawMap
				} else {
					entry = nil
				}
			}
		}
		if entry != nil {
			normalized = append(normalized, entry)
		}
		return true
	})
	if normalized == nil {
		return
	}
	if len(normalized) == 0 {
		_ = outDoc.SetRaw("tools", tools.Raw)
		return
	}
	raw, errMarshal := json.Marshal(normalized)
	if errMarshal != nil {
		_ = outDoc.SetRaw("tools", tools.Raw)
		return
	}
	_ = outDoc.SetRawBytes("tools", raw)
}

type geminiInteractionsInputContext struct {
	items            [][]byte
	inModelTurn      bool
	lastStepType     string
	pendingSignature string
	// run follows the consecutive user content so that an attachment Gemini cannot
	// carry is refused only when its whole user turn is left with nothing to send.
	run translatorcommon.UserRun
	// instruction is set while the step being added is developer or system content.
	instruction bool
}

// userRun returns the tracker for content added with role. Model content and
// developer or system content close the open user turn and have no tracker, so
// they can never keep an emptied user turn alive.
func (ctx *geminiInteractionsInputContext) userRun(role string) *translatorcommon.UserRun {
	if role == "user" && !ctx.instruction {
		return &ctx.run
	}
	ctx.run.End()
	return nil
}

// appendText adds a text content; blank text does not keep a user turn alive.
func (ctx *geminiInteractionsInputContext) appendText(role, text string) {
	appendGeminiTextContent(&ctx.items, role, text)
	run := ctx.userRun(role)
	if strings.TrimSpace(text) != "" {
		run.Add()
	}
}

func appendInteractionsInput(items *[][]byte, input ojson.Result) error {
	if !input.Exists() {
		return nil
	}
	ctx := &geminiInteractionsInputContext{
		items: *items,
	}
	if input.Type == ojson.String {
		appendGeminiTextContent(&ctx.items, "user", input.String())
		ctx.lastStepType = "text"
		*items = ctx.items
		return nil
	}
	if input.IsArray() {
		input.ForEach(func(_, item ojson.Result) bool {
			appendInteractionsStepToGemini(ctx, item, "user")
			return true
		})
	} else if steps := input.Get("steps"); steps.Exists() && steps.IsArray() {
		defaultRole := "user"
		if role := input.Get("role").String(); role == "model" || role == "assistant" {
			defaultRole = "model"
		}
		ctx.instruction = translatorcommon.IsInteractionsInstructionStep(input, false)
		steps.ForEach(func(_, step ojson.Result) bool {
			appendInteractionsStepToGemini(ctx, step, defaultRole)
			return true
		})
		ctx.instruction = false
	} else {
		appendInteractionsStepToGemini(ctx, input, "user")
	}
	flushPendingGeminiSignature(ctx)
	*items = ctx.items
	ctx.run.End()
	return ctx.run.Err()
}

func appendInteractionsStepToGemini(ctx *geminiInteractionsInputContext, item ojson.Result, defaultRole string) {
	inheritedInstruction := ctx.instruction
	ctx.instruction = translatorcommon.IsInteractionsInstructionStep(item, inheritedInstruction)
	defer func() { ctx.instruction = inheritedInstruction }()
	if item.Type == ojson.String {
		if ctx.inModelTurn {
			flushPendingGeminiSignature(ctx)
			ctx.inModelTurn = false
		}
		ctx.appendText(defaultRole, item.String())
		ctx.lastStepType = "text"
		return
	}
	if steps := item.Get("steps"); steps.Exists() && steps.IsArray() {
		role := defaultRole
		if itemRole := item.Get("role").String(); itemRole == "model" || itemRole == "assistant" {
			role = "model"
		} else if itemRole == "user" {
			role = "user"
		}
		steps.ForEach(func(_, child ojson.Result) bool {
			appendInteractionsStepToGemini(ctx, child, role)
			return true
		})
		return
	}
	stepType := item.Get("type").String()
	switch stepType {
	case "model_output":
		ctx.run.End()
		if ctx.pendingSignature != "" {
			carrier := geminiTextPartJSON("", false)
			carrier, _ = ojson.SetBytes(carrier, "thoughtSignature", ctx.pendingSignature)
			ctx.pendingSignature = ""
			if ctx.inModelTurn && len(ctx.items) > 0 && ojson.GetBytesNoCopy(ctx.items[len(ctx.items)-1], "role").String() == "model" {
				ctx.items[len(ctx.items)-1] = appendGeminiContentPart(ctx.items[len(ctx.items)-1], carrier)
			} else {
				ctx.items = append(ctx.items, interactionsGeminiContent("model", [][]byte{carrier}))
			}
		}
		partItems := extractInteractionsStepContentPartsToGemini(item, false)
		if len(partItems) > 0 {
			if ctx.inModelTurn && len(ctx.items) > 0 && ojson.GetBytesNoCopy(ctx.items[len(ctx.items)-1], "role").String() == "model" {
				ctx.items[len(ctx.items)-1] = appendGeminiContentParts(ctx.items[len(ctx.items)-1], partItems)
			} else {
				ctx.items = append(ctx.items, interactionsGeminiContent("model", partItems))
			}
		}
		ctx.inModelTurn = true
		ctx.lastStepType = "model_output"
	case "thought":
		ctx.run.End()
		if sig := firstNonEmptyString(item.Get("signature").String(), item.Get("thought_signature").String(), item.Get("thoughtSignature").String()); sig != "" {
			if ctx.pendingSignature != "" && ctx.pendingSignature != sig {
				carrier := geminiTextPartJSON("", false)
				carrier, _ = ojson.SetBytes(carrier, "thoughtSignature", ctx.pendingSignature)
				if ctx.inModelTurn && len(ctx.items) > 0 && ojson.GetBytesNoCopy(ctx.items[len(ctx.items)-1], "role").String() == "model" {
					ctx.items[len(ctx.items)-1] = appendGeminiContentPart(ctx.items[len(ctx.items)-1], carrier)
				} else {
					ctx.items = append(ctx.items, interactionsGeminiContent("model", [][]byte{carrier}))
				}
			}
			ctx.pendingSignature = sig
		}
		partItems := extractInteractionsThoughtPartsToGemini(item)
		if len(partItems) > 0 {
			if ctx.inModelTurn && len(ctx.items) > 0 && ojson.GetBytesNoCopy(ctx.items[len(ctx.items)-1], "role").String() == "model" {
				ctx.items[len(ctx.items)-1] = appendGeminiContentParts(ctx.items[len(ctx.items)-1], partItems)
			} else {
				ctx.items = append(ctx.items, interactionsGeminiContent("model", partItems))
			}
		}
		ctx.inModelTurn = true
		ctx.lastStepType = "thought"
	case "function_call":
		ctx.run.End()
		sig := firstNonEmptyString(item.Get("signature").String(), item.Get("thought_signature").String(), item.Get("thoughtSignature").String())
		if sig == "" && ctx.pendingSignature != "" {
			sig = ctx.pendingSignature
			ctx.pendingSignature = ""
		} else if sig != "" && ctx.pendingSignature != "" {
			if ctx.pendingSignature == sig {
				ctx.pendingSignature = ""
			} else {
				carrier := geminiTextPartJSON("", false)
				carrier, _ = ojson.SetBytes(carrier, "thoughtSignature", ctx.pendingSignature)
				ctx.pendingSignature = ""
				if ctx.inModelTurn && len(ctx.items) > 0 && ojson.GetBytesNoCopy(ctx.items[len(ctx.items)-1], "role").String() == "model" {
					ctx.items[len(ctx.items)-1] = appendGeminiContentPart(ctx.items[len(ctx.items)-1], carrier)
				} else {
					ctx.items = append(ctx.items, interactionsGeminiContent("model", [][]byte{carrier}))
				}
			}
		}
		part := buildGeminiFunctionCallPartWithSig(item, sig)
		if ctx.inModelTurn && len(ctx.items) > 0 && ojson.GetBytesNoCopy(ctx.items[len(ctx.items)-1], "role").String() == "model" {
			ctx.items[len(ctx.items)-1] = appendGeminiContentPart(ctx.items[len(ctx.items)-1], part)
		} else {
			ctx.items = append(ctx.items, interactionsGeminiContent("model", [][]byte{part}))
		}
		ctx.inModelTurn = true
		ctx.lastStepType = "function_call"
	case "function_result":
		if ctx.inModelTurn {
			flushPendingGeminiSignature(ctx)
			ctx.inModelTurn = false
		}
		part := buildGeminiFunctionResultPart(item)
		// A tool result is content the model reads, so it keeps the surrounding user turn.
		ctx.run.Add()
		if ctx.lastStepType == "function_result" && len(ctx.items) > 0 && ojson.GetBytesNoCopy(ctx.items[len(ctx.items)-1], "role").String() == "user" {
			ctx.items[len(ctx.items)-1] = appendGeminiUserContentPart(ctx.items[len(ctx.items)-1], part)
		} else {
			ctx.items = append(ctx.items, interactionsGeminiContent("user", [][]byte{part}))
		}
		ctx.lastStepType = "function_result"
	case "user_input", "":
		if ctx.inModelTurn {
			flushPendingGeminiSignature(ctx)
			ctx.inModelTurn = false
		}
		if item.Get("parts").Exists() {
			appendInteractionsNativeContent(ctx, item, defaultRole)
		} else {
			appendInteractionsContentList(ctx, defaultRole, item.Get("content"))
		}
		ctx.lastStepType = "user_input"
	default:
		if ctx.inModelTurn {
			flushPendingGeminiSignature(ctx)
			ctx.inModelTurn = false
		}
		if item.Get("parts").Exists() {
			appendInteractionsNativeContent(ctx, item, defaultRole)
		} else if item.Get("content").Exists() {
			appendInteractionsContentList(ctx, defaultRole, item.Get("content"))
		} else if text := item.Get("text"); text.Exists() {
			ctx.appendText(defaultRole, text.String())
		}
		ctx.lastStepType = "default"
	}
}

func flushPendingGeminiSignature(ctx *geminiInteractionsInputContext) {
	if ctx.pendingSignature == "" {
		return
	}
	carrier := geminiTextPartJSON("", false)
	carrier, _ = ojson.SetBytes(carrier, "thoughtSignature", ctx.pendingSignature)
	ctx.pendingSignature = ""
	if len(ctx.items) > 0 && ojson.GetBytesNoCopy(ctx.items[len(ctx.items)-1], "role").String() == "model" {
		ctx.items[len(ctx.items)-1] = appendGeminiContentPart(ctx.items[len(ctx.items)-1], carrier)
	} else {
		ctx.items = append(ctx.items, interactionsGeminiContent("model", [][]byte{carrier}))
	}
}

func appendGeminiContentPart(content []byte, part []byte) []byte {
	parts := ojson.GetBytesNoCopy(content, "parts").Array()
	rawParts := make([][]byte, 0, len(parts)+1)
	for _, p := range parts {
		rawParts = append(rawParts, []byte(p.Raw))
	}
	rawParts = append(rawParts, part)
	updated, err := ojson.SetRawBytes(content, "parts", translatorcommon.JoinRawArray(rawParts))
	if err != nil {
		return content
	}
	return updated
}

func appendGeminiContentParts(content []byte, newParts [][]byte) []byte {
	if len(newParts) == 0 {
		return content
	}
	parts := ojson.GetBytesNoCopy(content, "parts").Array()
	rawParts := make([][]byte, 0, len(parts)+len(newParts))
	for _, p := range parts {
		rawParts = append(rawParts, []byte(p.Raw))
	}
	rawParts = append(rawParts, newParts...)
	updated, err := ojson.SetRawBytes(content, "parts", translatorcommon.JoinRawArray(rawParts))
	if err != nil {
		return content
	}
	return updated
}

func appendGeminiUserContentPart(content []byte, part []byte) []byte {
	parts := ojson.GetBytesNoCopy(content, "parts").Array()
	rawParts := make([][]byte, 0, len(parts)+1)
	for _, p := range parts {
		rawParts = append(rawParts, []byte(p.Raw))
	}
	rawParts = append(rawParts, part)
	rawParts = translatorcommon.ReorderGeminiUserParts(rawParts)
	updated, err := ojson.SetRawBytes(content, "parts", translatorcommon.JoinRawArray(rawParts))
	if err != nil {
		return content
	}
	return updated
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func extractInteractionsThoughtPartsToGemini(step ojson.Result) [][]byte {
	content := step.Get("content")
	if !content.Exists() {
		content = step.Get("summary")
	}
	if !content.Exists() {
		content = step.Get("text")
	}
	if !content.Exists() {
		return nil
	}
	var partItems [][]byte
	if content.IsArray() {
		content.ForEach(func(_, part ojson.Result) bool {
			if partJSON := interactionsContentPartToGeminiPart(part, true); len(partJSON) > 0 {
				partItems = append(partItems, partJSON)
			}
			return true
		})
	} else if content.IsObject() {
		if partJSON := interactionsContentPartToGeminiPart(content, true); len(partJSON) > 0 {
			partItems = append(partItems, partJSON)
		}
	} else if content.Type == ojson.String {
		partItems = append(partItems, geminiTextPartJSON(content.String(), true))
	}
	return partItems
}

func extractInteractionsStepContentPartsToGemini(step ojson.Result, thought bool) [][]byte {
	content := step.Get("content")
	if !content.Exists() {
		content = step.Get("text")
	}
	if !content.Exists() {
		return nil
	}
	var partItems [][]byte
	if content.IsArray() {
		content.ForEach(func(_, part ojson.Result) bool {
			if partJSON := interactionsContentPartToGeminiPart(part, thought); len(partJSON) > 0 {
				partItems = append(partItems, partJSON)
			}
			return true
		})
	} else if content.IsObject() {
		if partJSON := interactionsContentPartToGeminiPart(content, thought); len(partJSON) > 0 {
			partItems = append(partItems, partJSON)
		}
	} else if content.Type == ojson.String {
		partItems = append(partItems, geminiTextPartJSON(content.String(), thought))
	}
	return partItems
}

func buildGeminiFunctionCallPart(item ojson.Result) []byte {
	return buildGeminiFunctionCallPartWithSig(item, "")
}

func buildGeminiFunctionCallPartWithSig(item ojson.Result, sig string) []byte {
	partDoc := ojson.NewDocString(`{"functionCall":{"name":"","args":{}}}`)
	_ = partDoc.Set("functionCall.name", item.Get("name").String())
	if callID := item.Get("call_id"); callID.Exists() {
		_ = partDoc.Set("functionCall.id", callID.String())
	} else if id := item.Get("id"); id.Exists() {
		_ = partDoc.Set("functionCall.id", id.String())
	}
	if args := item.Get("arguments"); args.Exists() {
		_ = partDoc.SetRaw("functionCall.args", args.Raw)
	}
	if sig != "" {
		_ = partDoc.Set("thoughtSignature", sig)
	}
	return partDoc.Bytes()
}

func buildGeminiFunctionResultPart(item ojson.Result) []byte {
	partDoc := ojson.NewDocString(`{"functionResponse":{"name":"","response":{}}}`)
	_ = partDoc.Set("functionResponse.name", item.Get("name").String())
	if callID := item.Get("call_id"); callID.Exists() {
		_ = partDoc.Set("functionResponse.id", callID.String())
	} else if id := item.Get("id"); id.Exists() {
		_ = partDoc.Set("functionResponse.id", id.String())
	}
	if result := item.Get("result"); result.Exists() {
		translatorcommon.SetGeminiFunctionResponseResultDoc(partDoc, "functionResponse.response", result)
	}
	return partDoc.Bytes()
}

func appendInteractionsNativeContent(ctx *geminiInteractionsInputContext, item ojson.Result, defaultRole string) {
	parts := item.Get("parts")
	if !parts.Exists() || !parts.IsArray() {
		return
	}
	role := interactionsGeminiContentRole(item.Get("role").String(), defaultRole)
	run := ctx.userRun(role)
	partItems := make([][]byte, 0, 4)
	parts.ForEach(func(_, part ojson.Result) bool {
		partJSON := interactionsNativeGeminiPart(part)
		if len(partJSON) == 0 {
			if droppedType := translatorcommon.InteractionsAttachmentType(part); droppedType != "" {
				run.Drop(droppedType)
			}
			return true
		}
		partItems = append(partItems, partJSON)
		if translatorcommon.GeminiPartIsSendable(partJSON) {
			run.Add()
		}
		return true
	})
	if len(partItems) == 0 {
		return
	}
	ctx.items = append(ctx.items, interactionsGeminiContent(role, partItems))
}

func interactionsGeminiContentRole(role, defaultRole string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "model", "assistant":
		return "model"
	case "user":
		return "user"
	}
	if defaultRole == "model" {
		return "model"
	}
	return "user"
}

func interactionsNativeGeminiPart(part ojson.Result) []byte {
	switch {
	case part.Get("text").Exists(), part.Get("functionCall").Exists(), part.Get("functionResponse").Exists():
		return []byte(part.Raw)
	case part.Get("inlineData").Exists():
		return geminiInlineDataPartJSON(part.Get("inlineData"))
	case part.Get("fileData").Exists():
		return geminiFileDataPartJSON(part.Get("fileData"))
	case part.Get("inline_data").Exists():
		return geminiInlineDataPartJSON(part.Get("inline_data"))
	case part.Get("file_data").Exists():
		return geminiFileDataPartJSON(part.Get("file_data"))
	}
	return nil
}

// appendInteractionsContentPart adds one content part. run is nil for model
// content; for user content it records what is sent and what cannot be.
func appendInteractionsContentPart(items *[][]byte, role string, part ojson.Result, run *translatorcommon.UserRun) {
	partJSON := interactionsContentPartToGeminiPart(part, false)
	if len(partJSON) == 0 {
		if droppedType := translatorcommon.InteractionsAttachmentType(part); droppedType != "" {
			run.Drop(droppedType)
		}
		return
	}
	*items = append(*items, interactionsGeminiContent(role, [][]byte{partJSON}))
	if translatorcommon.GeminiPartIsSendable(partJSON) {
		run.Add()
	}
}

func interactionsContentPartToGeminiPart(part ojson.Result, thought bool) []byte {
	if text := part.Get("text"); text.Exists() {
		return geminiTextPartJSON(text.String(), thought)
	}
	if inline := part.Get("inline_data"); inline.Exists() {
		return geminiInlineDataPartJSON(inline)
	}
	if inline := part.Get("inlineData"); inline.Exists() {
		return geminiInlineDataPartJSON(inline)
	}
	partType := strings.ToLower(strings.TrimSpace(part.Get("type").String()))
	switch partType {
	case "text":
		if text := part.Get("text"); text.Exists() {
			return geminiTextPartJSON(text.String(), thought)
		}
	case "image", "audio", "video", "document":
		if mime := part.Get("mime_type"); mime.Exists() || part.Get("mimeType").Exists() {
			mimeType := mime.String()
			if mimeType == "" {
				mimeType = part.Get("mimeType").String()
			}
			data := part.Get("data").String()
			if data != "" {
				return geminiInlineDataPartJSON(ojson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, mimeType, data)))
			}
		}
		// uri is the Interactions spelling of file_uri.
		if fileURI := firstNonEmptyString(part.Get("file_uri").String(), part.Get("fileUri").String(), part.Get("uri").String()); fileURI != "" {
			mimeType := part.Get("mime_type").String()
			if mimeType == "" {
				mimeType = part.Get("mimeType").String()
			}
			return geminiFileDataPartJSON(ojson.Parse(fmt.Sprintf(`{"mimeType":%q,"fileUri":%q}`, mimeType, fileURI)))
		}
		if url := part.Get("url"); url.Exists() {
			return geminiInlineDataPartFromDataURL(url.String())
		}
	case "image_url":
		return geminiInlineDataPartFromDataURL(part.Get("image_url.url").String())
	case "input_audio":
		mimeType := interactionsInputAudioMimeType(part.Get("input_audio.format").String())
		return geminiInlineDataPartJSON(ojson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, mimeType, part.Get("input_audio.data").String())))
	case "file":
		filename := part.Get("file.filename").String()
		fileData := part.Get("file.file_data").String()
		if mimeType, data, ok := translatorcommon.NormalizeOpenAIFileData(filename, "", fileData); ok {
			return geminiInlineDataPartJSON(ojson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, mimeType, data)))
		}
	}
	return nil
}

func geminiTextPartJSON(text string, thought bool) []byte {
	if !thought {
		partJSON := []byte(`{"text":""}`)
		partJSON, _ = ojson.SetBytes(partJSON, "text", text)
		return partJSON
	}
	partDoc := ojson.NewDocString(`{"text":""}`)
	_ = partDoc.Set("text", text)
	_ = partDoc.Set("thought", true)
	return partDoc.Bytes()
}

func geminiInlineDataPartJSON(inline ojson.Result) []byte {
	mimeType := inline.Get("mimeType").String()
	if mimeType == "" {
		mimeType = inline.Get("mime_type").String()
	}
	data := inline.Get("data").String()
	if mimeType == "" || data == "" {
		return nil
	}
	partDoc := ojson.NewDocString(`{"inlineData":{"mimeType":"","data":""}}`)
	_ = partDoc.Set("inlineData.mimeType", mimeType)
	_ = partDoc.Set("inlineData.data", data)
	return partDoc.Bytes()
}

func geminiFileDataPartJSON(fileData ojson.Result) []byte {
	mimeType := fileData.Get("mimeType").String()
	if mimeType == "" {
		mimeType = fileData.Get("mime_type").String()
	}
	fileURI := fileData.Get("fileUri").String()
	if fileURI == "" {
		fileURI = fileData.Get("file_uri").String()
	}
	if mimeType == "" || fileURI == "" {
		return nil
	}
	partDoc := ojson.NewDocString(`{"fileData":{"mimeType":"","fileUri":""}}`)
	_ = partDoc.Set("fileData.mimeType", mimeType)
	_ = partDoc.Set("fileData.fileUri", fileURI)
	return partDoc.Bytes()
}

func geminiInlineDataPartFromDataURL(dataURL string) []byte {
	if !strings.HasPrefix(dataURL, "data:") {
		return nil
	}
	payload := dataURL[5:]
	pieces := strings.SplitN(payload, ";", 2)
	if len(pieces) != 2 || !strings.HasPrefix(pieces[1], "base64,") {
		return nil
	}
	mimeType := pieces[0]
	data := pieces[1][7:]
	return geminiInlineDataPartJSON(ojson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, mimeType, data)))
}

func interactionsInputAudioMimeType(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "wav":
		return "audio/wav"
	case "mp3":
		return "audio/mpeg"
	case "flac":
		return "audio/flac"
	case "opus":
		return "audio/opus"
	case "pcm16":
		return "audio/pcm"
	default:
		return "audio/mpeg"
	}
}

func geminiInlineDataToInteractionsContent(mimeType, data string) []byte {
	itemDoc := ojson.NewDocString(`{"type":"","mime_type":"","data":""}`)
	_ = itemDoc.Set("type", geminiInteractionsMediaType(mimeType))
	_ = itemDoc.Set("mime_type", mimeType)
	_ = itemDoc.Set("data", data)
	return itemDoc.Bytes()
}

// geminiFileDataToInteractionsContent maps a Gemini fileData part onto an
// Interactions media part that references the file by uri. It returns nil when
// the part names no uri, which leaves nothing to send.
func geminiFileDataToInteractionsContent(fileData ojson.Result) []byte {
	fileURI := strings.TrimSpace(fileData.Get("fileUri").String())
	if fileURI == "" {
		fileURI = strings.TrimSpace(fileData.Get("file_uri").String())
	}
	if fileURI == "" {
		return nil
	}
	mimeType := fileData.Get("mimeType").String()
	if mimeType == "" {
		mimeType = fileData.Get("mime_type").String()
	}
	itemDoc := ojson.NewDocString(`{"type":"","uri":""}`)
	_ = itemDoc.Set("type", geminiInteractionsMediaType(mimeType))
	_ = itemDoc.Set("uri", fileURI)
	if mimeType != "" {
		_ = itemDoc.Set("mime_type", mimeType)
	}
	return itemDoc.Bytes()
}

// geminiInteractionsMediaType picks the Interactions content type for a MIME type.
func geminiInteractionsMediaType(mimeType string) string {
	lower := strings.ToLower(mimeType)
	switch {
	case strings.HasPrefix(lower, "image/"):
		return "image"
	case strings.HasPrefix(lower, "audio/"):
		return "audio"
	case strings.HasPrefix(lower, "video/"):
		return "video"
	}
	return "document"
}

func appendInteractionsContentList(ctx *geminiInteractionsInputContext, role string, content ojson.Result) {
	if !content.Exists() {
		return
	}
	run := ctx.userRun(role)
	if content.IsArray() {
		content.ForEach(func(_, part ojson.Result) bool {
			appendInteractionsContentPart(&ctx.items, role, part, run)
			return true
		})
		return
	}
	if content.IsObject() {
		appendInteractionsContentPart(&ctx.items, role, content, run)
	} else if content.Type == ojson.String {
		ctx.appendText(role, content.String())
	}
}

func appendInteractionsStepContent(items *[][]byte, role string, item ojson.Result, thought bool) {
	content := item.Get("content")
	if !content.Exists() {
		return
	}
	partItems := make([][]byte, 0, 4)
	if content.IsArray() {
		content.ForEach(func(_, part ojson.Result) bool {
			if partJSON := interactionsContentPartToGeminiPart(part, thought); len(partJSON) > 0 {
				partItems = append(partItems, partJSON)
			}
			return true
		})
	} else if content.IsObject() {
		if partJSON := interactionsContentPartToGeminiPart(content, thought); len(partJSON) > 0 {
			partItems = append(partItems, partJSON)
		}
	} else if content.Type == ojson.String {
		partItems = append(partItems, geminiTextPartJSON(content.String(), thought))
	}
	if len(partItems) > 0 {
		*items = append(*items, interactionsGeminiContent(role, partItems))
	}
}

func appendInteractionsFunctionCall(items *[][]byte, item ojson.Result) {
	part := buildGeminiFunctionCallPart(item)
	*items = append(*items, interactionsGeminiContent("model", [][]byte{part}))
}

func appendInteractionsFunctionResult(items *[][]byte, item ojson.Result) {
	part := buildGeminiFunctionResultPart(item)
	*items = append(*items, interactionsGeminiContent("user", [][]byte{part}))
}

func appendGeminiTextContent(items *[][]byte, role, text string) {
	*items = append(*items, interactionsGeminiContent(role, [][]byte{geminiTextPartJSON(text, false)}))
}

func interactionsGeminiContent(role string, parts [][]byte) []byte {
	contentDoc := ojson.NewDocString(`{"role":"","parts":[]}`)
	_ = contentDoc.Set("role", role)
	_ = contentDoc.SetRawBytes("parts", translatorcommon.JoinRawArray(parts))
	return contentDoc.Bytes()
}

func firstInteractionsGeminiUsage(usage ojson.Result, paths ...string) ojson.Result {
	for _, path := range paths {
		if value := usage.Get(path); value.Exists() {
			return value
		}
	}
	return ojson.Result{}
}

func setInteractionsUsageFromGeminiDoc(outDoc *ojson.Doc, path string, root ojson.Result) {
	usage := root.Get("usageMetadata")
	if !usage.Exists() {
		usage = root.Get("usage_metadata")
	}
	if !usage.Exists() {
		usage = root.Get("cpaUsageMetadata")
	}
	if !usage.Exists() {
		return
	}
	_ = outDoc.Set(path+".input_tokens", firstInteractionsGeminiUsage(usage, "promptTokenCount", "prompt_token_count").Int())
	_ = outDoc.Set(path+".output_tokens", firstInteractionsGeminiUsage(usage, "candidatesTokenCount", "candidates_token_count").Int())
	if reasoning := firstInteractionsGeminiUsage(usage, "thoughtsTokenCount", "thoughts_token_count"); reasoning.Exists() {
		_ = outDoc.Set(path+".reasoning_tokens", reasoning.Int())
	}
	_ = outDoc.Set(path+".total_tokens", firstInteractionsGeminiUsage(usage, "totalTokenCount", "total_token_count").Int())
	if cached := usage.Get("cachedContentTokenCount"); cached.Exists() {
		_ = outDoc.Set(path+".cached_tokens", cached.Int())
	} else if cached := usage.Get("cached_content_token_count"); cached.Exists() {
		_ = outDoc.Set(path+".cached_tokens", cached.Int())
	}
}

func setInteractionsStreamUsageFromGeminiDoc(outDoc *ojson.Doc, path string, root ojson.Result) {
	usage := root.Get("usageMetadata")
	if !usage.Exists() {
		usage = root.Get("usage_metadata")
	}
	if !usage.Exists() {
		usage = root.Get("cpaUsageMetadata")
	}
	if !usage.Exists() {
		return
	}
	inputTokens := firstInteractionsGeminiUsage(usage, "promptTokenCount", "prompt_token_count").Int()
	outputTokens := firstInteractionsGeminiUsage(usage, "candidatesTokenCount", "candidates_token_count").Int()
	totalTokens := firstInteractionsGeminiUsage(usage, "totalTokenCount", "total_token_count").Int()
	thoughtTokens := firstInteractionsGeminiUsage(usage, "thoughtsTokenCount", "thoughts_token_count").Int()
	cachedTokens := usage.Get("cachedContentTokenCount").Int()
	if cachedTokens == 0 {
		cachedTokens = usage.Get("cached_content_token_count").Int()
	}
	_ = outDoc.Set(path+".total_tokens", totalTokens)
	_ = outDoc.Set(path+".total_input_tokens", inputTokens)
	_ = outDoc.SetRaw(path+".input_tokens_by_modality", fmt.Sprintf(`[{"modality":"text","tokens":%d}]`, inputTokens))
	_ = outDoc.Set(path+".total_cached_tokens", cachedTokens)
	_ = outDoc.Set(path+".total_output_tokens", outputTokens)
	_ = outDoc.Set(path+".total_tool_use_tokens", 0)
	_ = outDoc.Set(path+".total_thought_tokens", thoughtTokens)
}

func appendInteractionsStepStart(out [][]byte, st *StreamState, stepType string, part ojson.Result) [][]byte {
	st.StepID = fmt.Sprintf("step_%d", time.Now().UnixNano())
	st.ActiveStepIndex = st.StepIndex
	st.StepIndex++
	st.ActiveStepType = stepType
	st.ActiveStepOpen = true
	stepStartDoc := ojson.NewDocString(`{"index":0,"step":{"type":""},"event_type":"step.start"}`)
	_ = stepStartDoc.Set("index", st.ActiveStepIndex)
	_ = stepStartDoc.Set("step.type", stepType)
	if stepType == "function_call" {
		id := interactionsFunctionPartID(part)
		if id == "" {
			id = st.StepID
		}
		_ = stepStartDoc.Set("step.id", id)
		_ = stepStartDoc.Set("step.name", part.Get("name").String())
		_ = stepStartDoc.SetRaw("step.arguments", `{}`)
	}
	return append(out, translatorcommon.SSEEventData("step.start", stepStartDoc.Bytes()))
}

func appendInteractionsStepStop(out [][]byte, st *StreamState) [][]byte {
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

func ensureInteractionsStep(out [][]byte, st *StreamState, stepType string, part ojson.Result) [][]byte {
	if st.ActiveStepOpen && st.ActiveStepType == stepType {
		return out
	}
	out = appendInteractionsStepStop(out, st)
	return appendInteractionsStepStart(out, st, stepType, part)
}

func appendGeminiPartToInteractionsStream(out [][]byte, st *StreamState, part ojson.Result) [][]byte {
	if text := part.Get("text"); text.Exists() && text.String() != "" {
		if part.Get("thought").Bool() {
			out = ensureInteractionsStep(out, st, "thought", ojson.Result{})
			deltaDoc := ojson.NewDocString(`{"index":0,"delta":{"content":{"text":"","type":"text"},"type":"thought_summary"},"event_type":"step.delta"}`)
			_ = deltaDoc.Set("index", st.ActiveStepIndex)
			_ = deltaDoc.Set("delta.content.text", text.String())
			out = append(out, translatorcommon.SSEEventData("step.delta", deltaDoc.Bytes()))
			return appendInteractionsThoughtSignature(out, st, part)
		}
		out = ensureInteractionsStep(out, st, "model_output", ojson.Result{})
		deltaDoc := ojson.NewDocString(`{"index":0,"delta":{"text":"","type":"text"},"event_type":"step.delta"}`)
		_ = deltaDoc.Set("index", st.ActiveStepIndex)
		_ = deltaDoc.Set("delta.text", text.String())
		out = append(out, translatorcommon.SSEEventData("step.delta", deltaDoc.Bytes()))
		return appendInteractionsThoughtSignature(out, st, part)
	}
	if fc := part.Get("functionCall"); fc.Exists() {
		out = appendInteractionsThoughtSignature(out, st, part)
		out = ensureInteractionsStep(out, st, "function_call", fc)
		deltaDoc := ojson.NewDocString(`{"index":0,"delta":{"arguments":"","type":"arguments_delta"},"event_type":"step.delta"}`)
		_ = deltaDoc.Set("index", st.ActiveStepIndex)
		arguments := `{}`
		if args := fc.Get("args"); args.Exists() {
			arguments = args.Raw
		}
		_ = deltaDoc.Set("delta.arguments", arguments)
		out = append(out, translatorcommon.SSEEventData("step.delta", deltaDoc.Bytes()))
		return appendInteractionsStepStop(out, st)
	}
	if fr := part.Get("functionResponse"); fr.Exists() {
		out = ensureInteractionsStep(out, st, "function_result", fr)
		deltaDoc := ojson.NewDocString(`{"index":0,"delta":{"type":"function_result","name":"","result":{}},"event_type":"step.delta"}`)
		_ = deltaDoc.Set("index", st.ActiveStepIndex)
		_ = deltaDoc.Set("delta.name", fr.Get("name").String())
		if response := fr.Get("response"); response.Exists() {
			_ = deltaDoc.SetRaw("delta.result", response.Raw)
		}
		out = append(out, translatorcommon.SSEEventData("step.delta", deltaDoc.Bytes()))
		return appendInteractionsStepStop(out, st)
	}
	if sig := interactionsThoughtSignature(part); sig != "" {
		return appendInteractionsThoughtSignature(out, st, part)
	}
	return out
}

func appendInteractionsThoughtSignature(out [][]byte, st *StreamState, part ojson.Result) [][]byte {
	if signature := interactionsThoughtSignature(part); signature != "" {
		out = ensureInteractionsStep(out, st, "thought", ojson.Result{})
		signatureDeltaDoc := ojson.NewDocString(`{"index":0,"delta":{"signature":"","type":"thought_signature"},"event_type":"step.delta"}`)
		_ = signatureDeltaDoc.Set("index", st.ActiveStepIndex)
		_ = signatureDeltaDoc.Set("delta.signature", signature)
		return append(out, translatorcommon.SSEEventData("step.delta", signatureDeltaDoc.Bytes()))
	}
	return out
}

func interactionsFunctionPartID(part ojson.Result) string {
	if id := part.Get("id"); id.Exists() {
		return id.String()
	}
	if callID := part.Get("call_id"); callID.Exists() {
		return callID.String()
	}
	return ""
}

func interactionsThoughtSignature(part ojson.Result) string {
	for _, path := range []string{"thoughtSignature", "thought_signature", "extra_content.google.thought_signature"} {
		if signature := strings.TrimSpace(part.Get(path).String()); signature != "" {
			return signature
		}
	}
	return ""
}

func geminiPartToInteractionsSteps(part ojson.Result) [][]byte {
	sig := interactionsThoughtSignature(part)
	if fc := part.Get("functionCall"); fc.Exists() {
		var steps [][]byte
		if sig != "" {
			steps = append(steps, geminiThoughtStepJSON(sig, ""))
		}
		stepDoc := ojson.NewDocString(`{"type":"function_call","name":"","arguments":{}}`)
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
		stepDoc := ojson.NewDocString(`{"type":"function_result","name":"","result":{}}`)
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
			return [][]byte{geminiThoughtStepJSON(sig, text.String())}
		}
		if text.String() == "" {
			if sig != "" {
				return [][]byte{geminiThoughtStepJSON(sig, "")}
			}
			return nil
		}
		step := []byte(`{"type":"model_output","content":[]}`)
		item := []byte(`{"text":""}`)
		item, _ = ojson.SetBytes(item, "text", text.String())
		step = translatorcommon.SetRawArrayItems(step, "content", [][]byte{item})
		var steps [][]byte
		steps = append(steps, step)
		if sig != "" {
			steps = append(steps, geminiThoughtStepJSON(sig, ""))
		}
		return steps
	}
	if inline := part.Get("inlineData"); inline.Exists() {
		mimeType := inline.Get("mimeType").String()
		if mimeType == "" {
			mimeType = inline.Get("mime_type").String()
		}
		item := geminiInlineDataToInteractionsContent(mimeType, inline.Get("data").String())
		step := []byte(`{"type":"model_output","content":[]}`)
		step = translatorcommon.SetRawArrayItems(step, "content", [][]byte{item})
		var steps [][]byte
		steps = append(steps, step)
		if sig != "" {
			steps = append(steps, geminiThoughtStepJSON(sig, ""))
		}
		return steps
	}
	if inline := part.Get("inline_data"); inline.Exists() {
		item := geminiInlineDataToInteractionsContent(inline.Get("mime_type").String(), inline.Get("data").String())
		step := []byte(`{"type":"model_output","content":[]}`)
		step = translatorcommon.SetRawArrayItems(step, "content", [][]byte{item})
		var steps [][]byte
		steps = append(steps, step)
		if sig != "" {
			steps = append(steps, geminiThoughtStepJSON(sig, ""))
		}
		return steps
	}
	if sig != "" {
		return [][]byte{geminiThoughtStepJSON(sig, "")}
	}
	return nil
}

func geminiThoughtStepJSON(sig, text string) []byte {
	stepDoc := ojson.NewDocString(`{"type":"thought"}`)
	if sig != "" {
		_ = stepDoc.Set("signature", sig)
	}
	if text != "" {
		item := []byte(`{"text":""}`)
		item, _ = ojson.SetBytes(item, "text", text)
		translatorcommon.SetRawArrayItemsDoc(stepDoc, "content", [][]byte{item})
	}
	return stepDoc.Bytes()
}

func geminiPartToInteractionsStep(part ojson.Result) []byte {
	steps := geminiPartToInteractionsSteps(part)
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
