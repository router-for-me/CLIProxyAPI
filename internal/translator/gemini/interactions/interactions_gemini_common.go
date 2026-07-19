package interactions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type StreamState struct {
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
}

func ConvertInteractionsRequestToGemini(modelName string, inputRawJSON []byte, stream bool) []byte {
	root := gjson.ParseBytes(inputRawJSON)
	out := []byte(`{"model":"","contents":[]}`)
	if modelName != "" && root.Get("model").Exists() {
		out, _ = sjson.SetBytes(out, "model", modelName)
	}
	out = copyInteractionsSystemInstruction(out, root)
	out = copyInteractionsGenerationConfig(out, root)
	out = copyInteractionsResponseModalities(out, root)
	out = copyInteractionsTools(out, root)
	out = copyInteractionsToolChoice(out, root)
	out = copyInteractionsServiceTier(out, root)
	contentItems := translatorcommon.NewRawArrayItems(root.Get("input.#").Int())
	appendInteractionsInput(&contentItems, root.Get("input"))
	out = translatorcommon.SetRawArrayItems(out, "contents", contentItems)
	return out
}

func ConvertGeminiRequestToInteractions(modelName string, inputRawJSON []byte, stream bool) []byte {
	root := gjson.ParseBytes(inputRawJSON)
	out := []byte(`{"model":"","input":[]}`)
	out, _ = sjson.SetBytes(out, "model", modelName)
	out = copyGeminiSystemInstructionToInteractions(out, root)
	if root.Get("generationConfig").Exists() {
		converted := convertCamelCaseKeysToSnakeCase([]byte(root.Get("generationConfig").Raw))
		out, _ = sjson.SetRawBytes(out, "generation_config", converted)
		out = normalizeGeminiThinkingConfigForInteractions(out)
	}
	out = copyGeminiToolsToInteractions(out, root)
	inputItems := translatorcommon.NewRawArrayItems(root.Get("contents.#").Int())
	root.Get("contents").ForEach(func(_, content gjson.Result) bool {
		role := content.Get("role").String()
		stepType := "user_input"
		if role == "model" {
			stepType = "model_output"
		}
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			if fc := part.Get("functionCall"); fc.Exists() {
				step := geminiPartToInteractionsStep(part)
				if len(step) > 0 {
					inputItems = append(inputItems, step)
				}
				return true
			}
			if fr := part.Get("functionResponse"); fr.Exists() {
				step := geminiPartToInteractionsStep(part)
				if len(step) > 0 {
					inputItems = append(inputItems, step)
				}
				return true
			}
			item := geminiPartToInteractionsContent(part)
			if len(item) == 0 {
				return true
			}
			currentStepType := stepType
			if part.Get("thought").Bool() && role == "model" {
				currentStepType = "thought"
			}
			step := []byte(`{"type":"","content":[]}`)
			step, _ = sjson.SetBytes(step, "type", currentStepType)
			step = translatorcommon.SetRawArrayItems(step, "content", [][]byte{item})
			inputItems = append(inputItems, step)
			return true
		})
		return true
	})
	out = translatorcommon.SetRawArrayItems(out, "input", inputItems)
	out, _ = sjson.SetBytes(out, "stream", stream)
	return out
}

func copyGeminiSystemInstructionToInteractions(out []byte, root gjson.Result) []byte {
	sys := root.Get("systemInstruction")
	if !sys.Exists() {
		sys = root.Get("system_instruction")
	}
	text := geminiSystemInstructionText(sys)
	if text == "" {
		return out
	}
	out, _ = sjson.SetBytes(out, "system_instruction", text)
	return out
}

func geminiSystemInstructionText(sys gjson.Result) string {
	if !sys.Exists() {
		return ""
	}
	if sys.Type == gjson.String {
		return sys.String()
	}
	if text := sys.Get("text"); text.Exists() && text.Type == gjson.String {
		return text.String()
	}
	parts := sys.Get("parts")
	if !parts.Exists() || !parts.IsArray() {
		return ""
	}
	var builder strings.Builder
	parts.ForEach(func(_, part gjson.Result) bool {
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

func normalizeGeminiThinkingConfigForInteractions(out []byte) []byte {
	if level := firstExistingPath(gjson.ParseBytes(out), []string{
		"generation_config.thinking_config.thinking_level",
		"generation_config.thinkingConfig.thinkingLevel",
		"generation_config.thinkingConfig.thinking_level",
	}); level.Exists() {
		out, _ = sjson.SetBytes(out, "generation_config.thinking_level", strings.ToLower(strings.TrimSpace(level.String())))
	}
	if budget := firstExistingPath(gjson.ParseBytes(out), []string{
		"generation_config.thinking_config.thinking_budget",
		"generation_config.thinkingConfig.thinkingBudget",
		"generation_config.thinkingConfig.thinking_budget",
	}); budget.Exists() {
		out, _ = sjson.SetRawBytes(out, "generation_config.thinking_budget", []byte(budget.Raw))
	}
	if !gjson.GetBytes(out, "generation_config.thinking_summaries").Exists() {
		if include := firstExistingPath(gjson.ParseBytes(out), []string{
			"generation_config.thinking_config.include_thoughts",
			"generation_config.thinking_config.includeThoughts",
			"generation_config.thinkingConfig.include_thoughts",
			"generation_config.thinkingConfig.includeThoughts",
		}); include.Exists() {
			summary := "none"
			if include.Bool() {
				summary = "auto"
			}
			out, _ = sjson.SetBytes(out, "generation_config.thinking_summaries", summary)
		}
	}
	return out
}

func firstExistingPath(root gjson.Result, paths []string) gjson.Result {
	for _, path := range paths {
		if value := root.Get(path); value.Exists() {
			return value
		}
	}
	return gjson.Result{}
}

func copyGeminiToolsToInteractions(out []byte, root gjson.Result) []byte {
	tools := root.Get("tools")
	if !tools.Exists() {
		return out
	}
	if !tools.IsArray() {
		out, _ = sjson.SetRawBytes(out, "tools", []byte(tools.Raw))
		return out
	}
	normalized := make([]map[string]any, 0)
	tools.ForEach(func(_, tool gjson.Result) bool {
		if name := tool.Get("name"); name.Exists() {
			entry := map[string]any{
				"type": "function",
				"name": name.String(),
			}
			if desc := tool.Get("description"); desc.Exists() {
				entry["description"] = desc.String()
			}
			if params := tool.Get("parameters"); params.Exists() {
				entry["parameters"] = json.RawMessage(params.Raw)
			} else if params := tool.Get("parametersJsonSchema"); params.Exists() {
				entry["parameters"] = json.RawMessage(params.Raw)
			}
			normalized = append(normalized, entry)
			return true
		}
		decls := tool.Get("functionDeclarations")
		if !decls.Exists() {
			decls = tool.Get("function_declarations")
		}
		decls.ForEach(func(_, decl gjson.Result) bool {
			if name := decl.Get("name"); name.Exists() {
				entry := map[string]any{
					"type": "function",
					"name": name.String(),
				}
				if desc := decl.Get("description"); desc.Exists() {
					entry["description"] = desc.String()
				}
				if params := decl.Get("parameters"); params.Exists() {
					entry["parameters"] = json.RawMessage(params.Raw)
				} else if params := decl.Get("parametersJsonSchema"); params.Exists() {
					entry["parameters"] = json.RawMessage(params.Raw)
				}
				normalized = append(normalized, entry)
			}
			return true
		})
		return true
	})
	if len(normalized) == 0 {
		out, _ = sjson.SetRawBytes(out, "tools", []byte(tools.Raw))
		return out
	}
	raw, errMarshal := json.Marshal(normalized)
	if errMarshal != nil {
		out, _ = sjson.SetRawBytes(out, "tools", []byte(tools.Raw))
		return out
	}
	out, _ = sjson.SetRawBytes(out, "tools", raw)
	return out
}

func geminiPartToInteractionsContent(part gjson.Result) []byte {
	if text := part.Get("text"); text.Exists() {
		item := []byte(`{"type":"text","text":""}`)
		item, _ = sjson.SetBytes(item, "text", text.String())
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
	return nil
}

func ConvertGeminiResponseToInteractionsStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	_ = ctx
	if *param == nil {
		*param = &StreamState{ID: fmt.Sprintf("interaction_%d", time.Now().UnixNano())}
	}
	st := (*param).(*StreamState)
	if bytes.Equal(bytes.TrimSpace(rawJSON), []byte("[DONE]")) {
		var out [][]byte
		if !st.Completed {
			out = appendInteractionsStepStop(out, st)
			out = appendInteractionsCompleted(out, st, modelName, gjson.Result{})
		}
		return appendInteractionsDone(out, st)
	}
	root := gjson.ParseBytes(rawJSON)
	var out [][]byte
	if !st.Started {
		out = appendInteractionsCreated(out, st, modelName)
		out = appendInteractionsStatusUpdate(out, st)
		st.Started = true
	}
	root.Get("candidates.0.content.parts").ForEach(func(_, part gjson.Result) bool {
		out = appendGeminiPartToInteractionsStream(out, st, part)
		return true
	})
	hasFinish := root.Get("candidates.0.finishReason").Exists()
	hasUsage := hasInteractionsGeminiStreamUsage(root)
	if hasFinish && !st.Finished {
		out = appendInteractionsStepStop(out, st)
		st.Finished = true
	}
	if hasUsage && st.Finished && !st.Completed {
		out = appendInteractionsCompleted(out, st, modelName, root)
	}
	return out
}

func hasInteractionsGeminiStreamUsage(root gjson.Result) bool {
	usage := root.Get("usageMetadata")
	if !usage.Exists() {
		usage = root.Get("usage_metadata")
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
	created := []byte(`{"interaction":{"id":"","status":"in_progress","object":"interaction","model":""},"event_type":"interaction.created"}`)
	created, _ = sjson.SetBytes(created, "interaction.id", st.ID)
	created, _ = sjson.SetBytes(created, "interaction.model", modelName)
	return append(out, translatorcommon.SSEEventData("interaction.created", created))
}

func appendInteractionsStatusUpdate(out [][]byte, st *StreamState) [][]byte {
	statusUpdate := []byte(`{"interaction_id":"","status":"in_progress","event_type":"interaction.status_update"}`)
	statusUpdate, _ = sjson.SetBytes(statusUpdate, "interaction_id", st.ID)
	return append(out, translatorcommon.SSEEventData("interaction.status_update", statusUpdate))
}

func appendInteractionsCompleted(out [][]byte, st *StreamState, modelName string, root gjson.Result) [][]byte {
	now := time.Now().UTC().Format(time.RFC3339)
	completed := []byte(`{"interaction":{"id":"","status":"completed","usage":{},"created":"","updated":"","service_tier":"standard","object":"interaction","model":""},"event_type":"interaction.completed"}`)
	completed, _ = sjson.SetBytes(completed, "interaction.id", st.ID)
	completed, _ = sjson.SetBytes(completed, "interaction.created", now)
	completed, _ = sjson.SetBytes(completed, "interaction.updated", now)
	completed, _ = sjson.SetBytes(completed, "interaction.model", modelName)
	if root.Exists() {
		completed = setInteractionsStreamUsageFromGemini(completed, "interaction.usage", root)
	}
	out = append(out, translatorcommon.SSEEventData("interaction.completed", completed))
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
	root := gjson.ParseBytes(rawJSON)
	out := []byte(`{"id":"","object":"interaction","status":"completed","model":"","steps":[]}`)
	id := root.Get("responseId").String()
	if id == "" {
		id = fmt.Sprintf("interaction_%d", time.Now().UnixNano())
	}
	out, _ = sjson.SetBytes(out, "id", id)
	out, _ = sjson.SetBytes(out, "model", modelName)
	root.Get("candidates.0.content.parts").ForEach(func(_, part gjson.Result) bool {
		if step := geminiPartToInteractionsStep(part); len(step) > 0 {
			out, _ = sjson.SetRawBytes(out, "steps.-1", step)
		}
		return true
	})
	out = setInteractionsUsageFromGemini(out, "usage", root)
	return out
}

func copyInteractionsSystemInstruction(out []byte, root gjson.Result) []byte {
	sys := root.Get("system_instruction")
	if !sys.Exists() {
		return out
	}
	if sys.Type == gjson.String {
		instr := []byte(`{"parts":[{"text":""}]}`)
		instr, _ = sjson.SetBytes(instr, "parts.0.text", sys.String())
		out, _ = sjson.SetRawBytes(out, "systemInstruction", instr)
		return out
	}
	if text := sys.Get("text"); text.Exists() && !sys.Get("parts").Exists() {
		instr := []byte(`{"parts":[{"text":""}]}`)
		instr, _ = sjson.SetBytes(instr, "parts.0.text", text.String())
		out, _ = sjson.SetRawBytes(out, "systemInstruction", instr)
		return out
	}
	out, _ = sjson.SetRawBytes(out, "systemInstruction", []byte(sys.Raw))
	return out
}

func copyInteractionsGenerationConfig(out []byte, root gjson.Result) []byte {
	cfg := root.Get("generation_config")
	if !cfg.Exists() {
		cfg = root.Get("generationConfig")
		if !cfg.Exists() {
			return out
		}
		out, _ = sjson.SetRawBytes(out, "generationConfig", []byte(cfg.Raw))
		return normalizeInteractionsGenerationConfig(out)
	}
	converted := convertSnakeCaseKeysToCamelCase([]byte(cfg.Raw))
	out, _ = sjson.SetRawBytes(out, "generationConfig", converted)
	out = normalizeInteractionsGenerationConfig(out)
	return out
}

func normalizeInteractionsGenerationConfig(out []byte) []byte {
	if toolChoice := gjson.GetBytes(out, "generationConfig.toolChoice"); toolChoice.Exists() {
		out, _ = sjson.DeleteBytes(out, "generationConfig.toolChoice")
	}
	if thinkingLevel := gjson.GetBytes(out, "generationConfig.thinkingLevel"); thinkingLevel.Exists() {
		out, _ = sjson.SetRawBytes(out, "generationConfig.thinkingConfig.thinkingLevel", []byte(thinkingLevel.Raw))
		out, _ = sjson.DeleteBytes(out, "generationConfig.thinkingLevel")
	}
	if thinkingBudget := gjson.GetBytes(out, "generationConfig.thinkingBudget"); thinkingBudget.Exists() {
		out, _ = sjson.SetRawBytes(out, "generationConfig.thinkingConfig.thinkingBudget", []byte(thinkingBudget.Raw))
		out, _ = sjson.DeleteBytes(out, "generationConfig.thinkingBudget")
	}
	if includeThoughts := gjson.GetBytes(out, "generationConfig.includeThoughts"); includeThoughts.Exists() {
		out, _ = sjson.SetRawBytes(out, "generationConfig.thinkingConfig.includeThoughts", []byte(includeThoughts.Raw))
		out, _ = sjson.DeleteBytes(out, "generationConfig.includeThoughts")
	}
	if summaries := gjson.GetBytes(out, "generationConfig.thinkingSummaries"); summaries.Exists() {
		if includeThoughts, ok := interactionsThinkingSummariesIncludeThoughts(summaries); ok {
			out, _ = sjson.SetBytes(out, "generationConfig.thinkingConfig.includeThoughts", includeThoughts)
		}
		out, _ = sjson.DeleteBytes(out, "generationConfig.thinkingSummaries")
	}
	return out
}

func interactionsThinkingSummariesIncludeThoughts(summary gjson.Result) (bool, bool) {
	switch summary.Type {
	case gjson.True:
		return true, true
	case gjson.False:
		return false, true
	case gjson.String:
		switch strings.ToLower(strings.TrimSpace(summary.String())) {
		case "", "none", "off", "false", "disabled":
			return false, true
		default:
			return true, true
		}
	}
	return false, false
}

func copyInteractionsResponseModalities(out []byte, root gjson.Result) []byte {
	mods := root.Get("response_modalities")
	if !mods.Exists() {
		mods = root.Get("responseModalities")
	}
	if !mods.Exists() || !mods.IsArray() {
		return out
	}
	var responseMods []string
	mods.ForEach(func(_, mod gjson.Result) bool {
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
		out, _ = sjson.SetBytes(out, "generationConfig.responseModalities", responseMods)
	}
	return out
}

func copyInteractionsToolChoice(out []byte, root gjson.Result) []byte {
	toolChoice := root.Get("tool_choice")
	if !toolChoice.Exists() {
		toolChoice = root.Get("generation_config.tool_choice")
	}
	if !toolChoice.Exists() {
		toolChoice = root.Get("generationConfig.toolChoice")
	}
	if !toolChoice.Exists() {
		return out
	}
	mode := ""
	var allowedNames []string
	if toolChoice.Type == gjson.String {
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
		return out
	}
	out, _ = sjson.SetBytes(out, "toolConfig.functionCallingConfig.mode", mode)
	if len(allowedNames) > 0 {
		out, _ = sjson.SetBytes(out, "toolConfig.functionCallingConfig.allowedFunctionNames", allowedNames)
	}
	return out
}

func copyInteractionsServiceTier(out []byte, root gjson.Result) []byte {
	serviceTier := root.Get("service_tier")
	if !serviceTier.Exists() || serviceTier.Type != gjson.String {
		return out
	}
	out, _ = sjson.SetBytes(out, "service_tier", serviceTier.String())
	return out
}

func convertSnakeCaseKeysToCamelCase(raw []byte) []byte {
	root := gjson.ParseBytes(raw)
	if !root.Exists() {
		return raw
	}
	out := []byte(`{}`)
	out = copySnakeCaseValueToCamelCase(out, "", root)
	return out
}

func copySnakeCaseValueToCamelCase(out []byte, path string, node gjson.Result) []byte {
	if node.IsObject() {
		node.ForEach(func(key, value gjson.Result) bool {
			childPath := joinJSONPath(path, toCamelCase(key.String()))
			out = copySnakeCaseValueToCamelCase(out, childPath, value)
			return true
		})
		return out
	}
	if node.IsArray() {
		node.ForEach(func(_, value gjson.Result) bool {
			childPath := path + ".-1"
			out = copySnakeCaseValueToCamelCase(out, childPath, value)
			return true
		})
		return out
	}
	out, _ = sjson.SetRawBytes(out, path, []byte(node.Raw))
	return out
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
	root := gjson.ParseBytes(raw)
	if !root.Exists() {
		return raw
	}
	out := []byte(`{}`)
	out = copyCamelCaseValueToSnakeCase(out, "", root)
	return out
}

func copyCamelCaseValueToSnakeCase(out []byte, path string, node gjson.Result) []byte {
	if node.IsObject() {
		node.ForEach(func(key, value gjson.Result) bool {
			childPath := joinJSONPath(path, toSnakeCase(key.String()))
			out = copyCamelCaseValueToSnakeCase(out, childPath, value)
			return true
		})
		return out
	}
	if node.IsArray() {
		node.ForEach(func(_, value gjson.Result) bool {
			childPath := path + ".-1"
			out = copyCamelCaseValueToSnakeCase(out, childPath, value)
			return true
		})
		return out
	}
	out, _ = sjson.SetRawBytes(out, path, []byte(node.Raw))
	return out
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

func copyInteractionsTools(out []byte, root gjson.Result) []byte {
	tools := root.Get("tools")
	if !tools.Exists() {
		return out
	}
	if !tools.IsArray() {
		out, _ = sjson.SetRawBytes(out, "tools", []byte(tools.Raw))
		return out
	}
	normalized := make([]map[string]any, 0)
	tools.ForEach(func(_, tool gjson.Result) bool {
		if tool.Get("functionDeclarations").Exists() {
			out, _ = sjson.SetRawBytes(out, "tools", []byte(tools.Raw))
			normalized = nil
			return false
		}
		entry := map[string]any{}
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
			entry = nil
		}
		if entry != nil {
			normalized = append(normalized, entry)
		}
		return true
	})
	if normalized == nil {
		return out
	}
	if len(normalized) == 0 {
		out, _ = sjson.SetRawBytes(out, "tools", []byte(tools.Raw))
		return out
	}
	raw, errMarshal := json.Marshal(normalized)
	if errMarshal != nil {
		out, _ = sjson.SetRawBytes(out, "tools", []byte(tools.Raw))
		return out
	}
	out, _ = sjson.SetRawBytes(out, "tools", raw)
	return out
}

func appendInteractionsInput(items *[][]byte, input gjson.Result) {
	if !input.Exists() {
		return
	}
	if input.Type == gjson.String {
		appendGeminiTextContent(items, "user", input.String())
		return
	}
	if input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			appendInteractionsInputItem(items, item, "user")
			return true
		})
		return
	}
	if steps := input.Get("steps"); steps.Exists() && steps.IsArray() {
		defaultRole := "user"
		if role := input.Get("role").String(); role == "model" || role == "assistant" {
			defaultRole = "model"
		}
		steps.ForEach(func(_, step gjson.Result) bool {
			appendInteractionsInputItem(items, step, defaultRole)
			return true
		})
		return
	}
	appendInteractionsInputItem(items, input, "user")
}

func appendInteractionsInputItem(items *[][]byte, item gjson.Result, defaultRole string) {
	if item.Type == gjson.String {
		appendGeminiTextContent(items, defaultRole, item.String())
		return
	}
	if steps := item.Get("steps"); steps.Exists() && steps.IsArray() {
		role := defaultRole
		if itemRole := item.Get("role").String(); itemRole == "model" || itemRole == "assistant" {
			role = "model"
		} else if itemRole == "user" {
			role = "user"
		}
		steps.ForEach(func(_, step gjson.Result) bool {
			appendInteractionsInputItem(items, step, role)
			return true
		})
		return
	}
	stepType := item.Get("type").String()
	switch stepType {
	case "model_output", "thought":
		appendInteractionsStepContent(items, "model", item, stepType == "thought")
	case "function_call":
		appendInteractionsFunctionCall(items, item)
	case "function_result":
		appendInteractionsFunctionResult(items, item)
	case "user_input", "":
		if item.Get("parts").Exists() {
			appendInteractionsNativeContent(items, item, defaultRole)
		} else {
			appendInteractionsContentList(items, defaultRole, item.Get("content"))
		}
	default:
		if item.Get("parts").Exists() {
			appendInteractionsNativeContent(items, item, defaultRole)
		} else if item.Get("content").Exists() {
			appendInteractionsContentList(items, defaultRole, item.Get("content"))
		} else if text := item.Get("text"); text.Exists() {
			appendGeminiTextContent(items, defaultRole, text.String())
		}
	}
}

func appendInteractionsNativeContent(items *[][]byte, item gjson.Result, defaultRole string) {
	parts := item.Get("parts")
	if !parts.Exists() || !parts.IsArray() {
		return
	}
	partItems := make([][]byte, 0, 4)
	parts.ForEach(func(_, part gjson.Result) bool {
		if partJSON := interactionsNativeGeminiPart(part); len(partJSON) > 0 {
			partItems = append(partItems, partJSON)
		}
		return true
	})
	if len(partItems) == 0 {
		return
	}
	role := interactionsGeminiContentRole(item.Get("role").String(), defaultRole)
	*items = append(*items, interactionsGeminiContent(role, partItems))
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

func interactionsNativeGeminiPart(part gjson.Result) []byte {
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

func appendInteractionsContentPart(items *[][]byte, role string, part gjson.Result) {
	partJSON := interactionsContentPartToGeminiPart(part, false)
	if len(partJSON) == 0 {
		return
	}
	*items = append(*items, interactionsGeminiContent(role, [][]byte{partJSON}))
}

func interactionsContentPartToGeminiPart(part gjson.Result, thought bool) []byte {
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
				return geminiInlineDataPartJSON(gjson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, mimeType, data)))
			}
		}
		if uri := part.Get("file_uri"); uri.Exists() || part.Get("fileUri").Exists() {
			fileURI := uri.String()
			if fileURI == "" {
				fileURI = part.Get("fileUri").String()
			}
			mimeType := part.Get("mime_type").String()
			if mimeType == "" {
				mimeType = part.Get("mimeType").String()
			}
			return geminiFileDataPartJSON(gjson.Parse(fmt.Sprintf(`{"mimeType":%q,"fileUri":%q}`, mimeType, fileURI)))
		}
		if url := part.Get("url"); url.Exists() {
			return geminiInlineDataPartFromDataURL(url.String())
		}
	case "image_url":
		return geminiInlineDataPartFromDataURL(part.Get("image_url.url").String())
	case "input_audio":
		mimeType := interactionsInputAudioMimeType(part.Get("input_audio.format").String())
		return geminiInlineDataPartJSON(gjson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, mimeType, part.Get("input_audio.data").String())))
	case "file":
		filename := part.Get("file.filename").String()
		fileData := part.Get("file.file_data").String()
		ext := ""
		if sp := strings.Split(filename, "."); len(sp) > 1 {
			ext = sp[len(sp)-1]
		}
		if mimeType, ok := misc.MimeTypes[ext]; ok && fileData != "" {
			return geminiInlineDataPartJSON(gjson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, mimeType, fileData)))
		}
	}
	return nil
}

func geminiTextPartJSON(text string, thought bool) []byte {
	partJSON := []byte(`{"text":""}`)
	partJSON, _ = sjson.SetBytes(partJSON, "text", text)
	if thought {
		partJSON, _ = sjson.SetBytes(partJSON, "thought", true)
	}
	return partJSON
}

func geminiInlineDataPartJSON(inline gjson.Result) []byte {
	mimeType := inline.Get("mimeType").String()
	if mimeType == "" {
		mimeType = inline.Get("mime_type").String()
	}
	data := inline.Get("data").String()
	if mimeType == "" || data == "" {
		return nil
	}
	partJSON := []byte(`{"inlineData":{"mimeType":"","data":""}}`)
	partJSON, _ = sjson.SetBytes(partJSON, "inlineData.mimeType", mimeType)
	partJSON, _ = sjson.SetBytes(partJSON, "inlineData.data", data)
	return partJSON
}

func geminiFileDataPartJSON(fileData gjson.Result) []byte {
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
	partJSON := []byte(`{"fileData":{"mimeType":"","fileUri":""}}`)
	partJSON, _ = sjson.SetBytes(partJSON, "fileData.mimeType", mimeType)
	partJSON, _ = sjson.SetBytes(partJSON, "fileData.fileUri", fileURI)
	return partJSON
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
	return geminiInlineDataPartJSON(gjson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, mimeType, data)))
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
	return item
}

func appendInteractionsContentList(items *[][]byte, role string, content gjson.Result) {
	if !content.Exists() {
		return
	}
	if content.IsArray() {
		content.ForEach(func(_, part gjson.Result) bool {
			appendInteractionsContentPart(items, role, part)
			return true
		})
		return
	}
	if content.IsObject() {
		appendInteractionsContentPart(items, role, content)
	} else if content.Type == gjson.String {
		appendGeminiTextContent(items, role, content.String())
	}
}

func appendInteractionsStepContent(items *[][]byte, role string, item gjson.Result, thought bool) {
	content := item.Get("content")
	if !content.Exists() {
		return
	}
	partItems := make([][]byte, 0, 4)
	if content.IsArray() {
		content.ForEach(func(_, part gjson.Result) bool {
			if partJSON := interactionsContentPartToGeminiPart(part, thought); len(partJSON) > 0 {
				partItems = append(partItems, partJSON)
			}
			return true
		})
	} else if content.IsObject() {
		if partJSON := interactionsContentPartToGeminiPart(content, thought); len(partJSON) > 0 {
			partItems = append(partItems, partJSON)
		}
	} else if content.Type == gjson.String {
		partItems = append(partItems, geminiTextPartJSON(content.String(), thought))
	}
	if len(partItems) > 0 {
		*items = append(*items, interactionsGeminiContent(role, partItems))
	}
}

func appendInteractionsFunctionCall(items *[][]byte, item gjson.Result) {
	part := []byte(`{"functionCall":{"name":"","args":{}}}`)
	part, _ = sjson.SetBytes(part, "functionCall.name", item.Get("name").String())
	if callID := item.Get("call_id"); callID.Exists() {
		part, _ = sjson.SetBytes(part, "functionCall.id", callID.String())
	} else if id := item.Get("id"); id.Exists() {
		part, _ = sjson.SetBytes(part, "functionCall.id", id.String())
	}
	if args := item.Get("arguments"); args.Exists() {
		part, _ = sjson.SetRawBytes(part, "functionCall.args", []byte(args.Raw))
	}
	*items = append(*items, interactionsGeminiContent("model", [][]byte{part}))
}

func appendInteractionsFunctionResult(items *[][]byte, item gjson.Result) {
	part := []byte(`{"functionResponse":{"name":"","response":{}}}`)
	part, _ = sjson.SetBytes(part, "functionResponse.name", item.Get("name").String())
	if callID := item.Get("call_id"); callID.Exists() {
		part, _ = sjson.SetBytes(part, "functionResponse.id", callID.String())
	} else if id := item.Get("id"); id.Exists() {
		part, _ = sjson.SetBytes(part, "functionResponse.id", id.String())
	}
	if result := item.Get("result"); result.Exists() {
		part, _ = sjson.SetRawBytes(part, "functionResponse.response", []byte(result.Raw))
	}
	*items = append(*items, interactionsGeminiContent("user", [][]byte{part}))
}

func appendGeminiTextContent(items *[][]byte, role, text string) {
	*items = append(*items, interactionsGeminiContent(role, [][]byte{geminiTextPartJSON(text, false)}))
}

func interactionsGeminiContent(role string, parts [][]byte) []byte {
	content := []byte(`{"role":"","parts":[]}`)
	content, _ = sjson.SetBytes(content, "role", role)
	content, _ = sjson.SetRawBytes(content, "parts", translatorcommon.JoinRawArray(parts))
	return content
}

func setInteractionsUsageFromGemini(out []byte, path string, root gjson.Result) []byte {
	usage := root.Get("usageMetadata")
	if !usage.Exists() {
		usage = root.Get("usage_metadata")
	}
	if !usage.Exists() {
		return out
	}
	out, _ = sjson.SetBytes(out, path+".input_tokens", usage.Get("promptTokenCount").Int())
	out, _ = sjson.SetBytes(out, path+".output_tokens", usage.Get("candidatesTokenCount").Int())
	if reasoning := usage.Get("thoughtsTokenCount"); reasoning.Exists() {
		out, _ = sjson.SetBytes(out, path+".reasoning_tokens", reasoning.Int())
	}
	out, _ = sjson.SetBytes(out, path+".total_tokens", usage.Get("totalTokenCount").Int())
	if cached := usage.Get("cachedContentTokenCount"); cached.Exists() {
		out, _ = sjson.SetBytes(out, path+".cached_tokens", cached.Int())
	} else if cached := usage.Get("cached_content_token_count"); cached.Exists() {
		out, _ = sjson.SetBytes(out, path+".cached_tokens", cached.Int())
	}
	return out
}

func setInteractionsStreamUsageFromGemini(out []byte, path string, root gjson.Result) []byte {
	usage := root.Get("usageMetadata")
	if !usage.Exists() {
		usage = root.Get("usage_metadata")
	}
	if !usage.Exists() {
		return out
	}
	inputTokens := usage.Get("promptTokenCount").Int()
	outputTokens := usage.Get("candidatesTokenCount").Int()
	totalTokens := usage.Get("totalTokenCount").Int()
	thoughtTokens := usage.Get("thoughtsTokenCount").Int()
	cachedTokens := usage.Get("cachedContentTokenCount").Int()
	if cachedTokens == 0 {
		cachedTokens = usage.Get("cached_content_token_count").Int()
	}
	out, _ = sjson.SetBytes(out, path+".total_tokens", totalTokens)
	out, _ = sjson.SetBytes(out, path+".total_input_tokens", inputTokens)
	out, _ = sjson.SetRawBytes(out, path+".input_tokens_by_modality", []byte(fmt.Sprintf(`[{"modality":"text","tokens":%d}]`, inputTokens)))
	out, _ = sjson.SetBytes(out, path+".total_cached_tokens", cachedTokens)
	out, _ = sjson.SetBytes(out, path+".total_output_tokens", outputTokens)
	out, _ = sjson.SetBytes(out, path+".total_tool_use_tokens", 0)
	out, _ = sjson.SetBytes(out, path+".total_thought_tokens", thoughtTokens)
	return out
}

func appendInteractionsStepStart(out [][]byte, st *StreamState, stepType string, part gjson.Result) [][]byte {
	st.StepID = fmt.Sprintf("step_%d", time.Now().UnixNano())
	st.ActiveStepIndex = st.StepIndex
	st.StepIndex++
	st.ActiveStepType = stepType
	st.ActiveStepOpen = true
	stepStart := []byte(`{"index":0,"step":{"type":""},"event_type":"step.start"}`)
	stepStart, _ = sjson.SetBytes(stepStart, "index", st.ActiveStepIndex)
	stepStart, _ = sjson.SetBytes(stepStart, "step.type", stepType)
	if stepType == "function_call" {
		id := interactionsFunctionPartID(part)
		if id == "" {
			id = st.StepID
		}
		stepStart, _ = sjson.SetBytes(stepStart, "step.id", id)
		stepStart, _ = sjson.SetBytes(stepStart, "step.name", part.Get("name").String())
		stepStart, _ = sjson.SetRawBytes(stepStart, "step.arguments", []byte(`{}`))
	}
	return append(out, translatorcommon.SSEEventData("step.start", stepStart))
}

func appendInteractionsStepStop(out [][]byte, st *StreamState) [][]byte {
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

func ensureInteractionsStep(out [][]byte, st *StreamState, stepType string, part gjson.Result) [][]byte {
	if st.ActiveStepOpen && st.ActiveStepType == stepType {
		return out
	}
	out = appendInteractionsStepStop(out, st)
	return appendInteractionsStepStart(out, st, stepType, part)
}

func appendGeminiPartToInteractionsStream(out [][]byte, st *StreamState, part gjson.Result) [][]byte {
	if text := part.Get("text"); text.Exists() && text.String() != "" {
		if part.Get("thought").Bool() {
			out = ensureInteractionsStep(out, st, "thought", gjson.Result{})
			delta := []byte(`{"index":0,"delta":{"content":{"text":"","type":"text"},"type":"thought_summary"},"event_type":"step.delta"}`)
			delta, _ = sjson.SetBytes(delta, "index", st.ActiveStepIndex)
			delta, _ = sjson.SetBytes(delta, "delta.content.text", text.String())
			out = append(out, translatorcommon.SSEEventData("step.delta", delta))
			return appendInteractionsThoughtSignature(out, st, part)
		}
		out = ensureInteractionsStep(out, st, "model_output", gjson.Result{})
		delta := []byte(`{"index":0,"delta":{"text":"","type":"text"},"event_type":"step.delta"}`)
		delta, _ = sjson.SetBytes(delta, "index", st.ActiveStepIndex)
		delta, _ = sjson.SetBytes(delta, "delta.text", text.String())
		return append(out, translatorcommon.SSEEventData("step.delta", delta))
	}
	if fc := part.Get("functionCall"); fc.Exists() {
		out = appendInteractionsThoughtSignature(out, st, part)
		out = ensureInteractionsStep(out, st, "function_call", fc)
		delta := []byte(`{"index":0,"delta":{"arguments":"","type":"arguments_delta"},"event_type":"step.delta"}`)
		delta, _ = sjson.SetBytes(delta, "index", st.ActiveStepIndex)
		arguments := `{}`
		if args := fc.Get("args"); args.Exists() {
			arguments = args.Raw
		}
		delta, _ = sjson.SetBytes(delta, "delta.arguments", arguments)
		out = append(out, translatorcommon.SSEEventData("step.delta", delta))
		return appendInteractionsStepStop(out, st)
	}
	if fr := part.Get("functionResponse"); fr.Exists() {
		out = ensureInteractionsStep(out, st, "function_result", fr)
		delta := []byte(`{"index":0,"delta":{"type":"function_result","name":"","result":{}},"event_type":"step.delta"}`)
		delta, _ = sjson.SetBytes(delta, "index", st.ActiveStepIndex)
		delta, _ = sjson.SetBytes(delta, "delta.name", fr.Get("name").String())
		if response := fr.Get("response"); response.Exists() {
			delta, _ = sjson.SetRawBytes(delta, "delta.result", []byte(response.Raw))
		}
		out = append(out, translatorcommon.SSEEventData("step.delta", delta))
		return appendInteractionsStepStop(out, st)
	}
	return out
}

func appendInteractionsThoughtSignature(out [][]byte, st *StreamState, part gjson.Result) [][]byte {
	if signature := interactionsThoughtSignature(part); signature != "" {
		out = ensureInteractionsStep(out, st, "thought", gjson.Result{})
		signatureDelta := []byte(`{"index":0,"delta":{"signature":"","type":"thought_signature"},"event_type":"step.delta"}`)
		signatureDelta, _ = sjson.SetBytes(signatureDelta, "index", st.ActiveStepIndex)
		signatureDelta, _ = sjson.SetBytes(signatureDelta, "delta.signature", signature)
		return append(out, translatorcommon.SSEEventData("step.delta", signatureDelta))
	}
	return out
}

func interactionsFunctionPartID(part gjson.Result) string {
	if id := part.Get("id"); id.Exists() {
		return id.String()
	}
	if callID := part.Get("call_id"); callID.Exists() {
		return callID.String()
	}
	return ""
}

func interactionsThoughtSignature(part gjson.Result) string {
	for _, path := range []string{"thoughtSignature", "thought_signature", "extra_content.google.thought_signature"} {
		if signature := strings.TrimSpace(part.Get(path).String()); signature != "" {
			return signature
		}
	}
	return ""
}

func geminiPartToInteractionsStep(part gjson.Result) []byte {
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
		item := []byte(`{"text":""}`)
		item, _ = sjson.SetBytes(item, "text", text.String())
		step, _ = sjson.SetRawBytes(step, "content.-1", item)
		return step
	}
	if inline := part.Get("inlineData"); inline.Exists() {
		mimeType := inline.Get("mimeType").String()
		if mimeType == "" {
			mimeType = inline.Get("mime_type").String()
		}
		item := geminiInlineDataToInteractionsContent(mimeType, inline.Get("data").String())
		step := []byte(`{"type":"model_output","content":[]}`)
		step, _ = sjson.SetRawBytes(step, "content.-1", item)
		return step
	}
	if inline := part.Get("inline_data"); inline.Exists() {
		item := geminiInlineDataToInteractionsContent(inline.Get("mime_type").String(), inline.Get("data").String())
		step := []byte(`{"type":"model_output","content":[]}`)
		step, _ = sjson.SetRawBytes(step, "content.-1", item)
		return step
	}
	return nil
}
