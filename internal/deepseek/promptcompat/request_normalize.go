package promptcompat

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/modelinfo"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/deepseek/util"
)

type ConfigReader interface {
	ModelAliases() map[string]string
	AutoRouteVisionEnabled() bool
}

func NormalizeOpenAIChatRequest(store ConfigReader, req map[string]any, traceID string) (StandardRequest, error) {
	model, _ := req["model"].(string)
	messagesRaw, _ := req["messages"].([]any)
	if strings.TrimSpace(model) == "" || len(messagesRaw) == 0 {
		return StandardRequest{}, fmt.Errorf("request must include 'model' and 'messages'")
	}
	resolvedModel, ok := modelinfo.ResolveModel(store, model)
	if !ok {
		return StandardRequest{}, fmt.Errorf("model %q is not available", model)
	}
	defaultThinkingEnabled, searchEnabled, _ := modelinfo.GetModelConfig(resolvedModel)
	thinkingEnabled := util.ResolveThinkingEnabled(req, defaultThinkingEnabled)
	if modelinfo.IsNoThinkingModel(resolvedModel) {
		thinkingEnabled = false
	}
	responseModel := strings.TrimSpace(model)
	if responseModel == "" {
		responseModel = resolvedModel
	}
	toolPolicy := DefaultToolChoicePolicy()
	finalPrompt, toolNames := BuildOpenAIPrompt(messagesRaw, req["tools"], traceID, toolPolicy, thinkingEnabled)
	toolNames = ensureToolDetectionEnabled(toolNames, req["tools"])
	passThrough := collectOpenAIChatPassThrough(req)
	refFileIDs := CollectOpenAIRefFileIDs(req)

	return StandardRequest{
		Surface:         "openai_chat",
		RequestedModel:  strings.TrimSpace(model),
		ResolvedModel:   resolvedModel,
		ResponseModel:   responseModel,
		Messages:        messagesRaw,
		PromptTokenText: finalPrompt,
		ToolsRaw:        req["tools"],
		FinalPrompt:     finalPrompt,
		ToolNames:       toolNames,
		ToolChoice:      toolPolicy,
		Stream:          util.ToBool(req["stream"]),
		Thinking:        thinkingEnabled,
		Search:          searchEnabled,
		RefFileIDs:      refFileIDs,
		RefFileTokens:   estimateInlineFileTokens(req),
		PassThrough:     passThrough,
	}, nil
}

func NormalizeOpenAIResponsesRequest(store ConfigReader, req map[string]any, traceID string) (StandardRequest, error) {
	model, _ := req["model"].(string)
	model = strings.TrimSpace(model)
	if model == "" {
		return StandardRequest{}, fmt.Errorf("request must include 'model'")
	}
	resolvedModel, ok := modelinfo.ResolveModel(store, model)
	if !ok {
		return StandardRequest{}, fmt.Errorf("model %q is not available", model)
	}
	defaultThinkingEnabled, searchEnabled, _ := modelinfo.GetModelConfig(resolvedModel)
	thinkingEnabled := util.ResolveThinkingEnabled(req, defaultThinkingEnabled)
	if modelinfo.IsNoThinkingModel(resolvedModel) {
		thinkingEnabled = false
	}

	messagesRaw := ResponsesMessagesFromRequest(req)
	if len(messagesRaw) == 0 {
		return StandardRequest{}, fmt.Errorf("request must include 'input' or 'messages'")
	}
	toolPolicy, err := parseToolChoicePolicy(req["tool_choice"], req["tools"])
	if err != nil {
		return StandardRequest{}, err
	}
	finalPrompt, toolNames := BuildOpenAIPrompt(messagesRaw, req["tools"], traceID, toolPolicy, thinkingEnabled)
	toolNames = ensureToolDetectionEnabled(toolNames, req["tools"])
	if !toolPolicy.IsNone() {
		toolPolicy.Allowed = namesToSet(toolNames)
	}
	passThrough := collectOpenAIChatPassThrough(req)
	refFileIDs := CollectOpenAIRefFileIDs(req)

	return StandardRequest{
		Surface:         "openai_responses",
		RequestedModel:  model,
		ResolvedModel:   resolvedModel,
		ResponseModel:   model,
		Messages:        messagesRaw,
		PromptTokenText: finalPrompt,
		ToolsRaw:        req["tools"],
		FinalPrompt:     finalPrompt,
		ToolNames:       toolNames,
		ToolChoice:      toolPolicy,
		Stream:          util.ToBool(req["stream"]),
		Thinking:        thinkingEnabled,
		Search:          searchEnabled,
		RefFileIDs:      refFileIDs,
		RefFileTokens:   estimateInlineFileTokens(req),
		PassThrough:     passThrough,
	}, nil
}

func ensureToolDetectionEnabled(toolNames []string, toolsRaw any) []string {
	if len(toolNames) > 0 {
		return toolNames
	}
	tools, _ := toolsRaw.([]any)
	if len(tools) == 0 {
		return toolNames
	}
	// Keep stream sieve/tool buffering enabled even when client tool schemas
	// are malformed or lack explicit names; parsed tool payload names are no
	// longer filtered by this list.
	return []string{"__any_tool__"}
}

func collectOpenAIChatPassThrough(req map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{
		"temperature",
		"top_p",
		"max_tokens",
		"max_completion_tokens",
		"presence_penalty",
		"frequency_penalty",
		"stop",
	} {
		if v, ok := req[k]; ok {
			out[k] = v
		}
	}
	return out
}

func parseToolChoicePolicy(toolChoiceRaw any, toolsRaw any) (ToolChoicePolicy, error) {
	policy := DefaultToolChoicePolicy()
	declaredNames := extractDeclaredToolNames(toolsRaw)
	declaredSet := namesToSet(declaredNames)
	if len(declaredNames) > 0 {
		policy.Allowed = declaredSet
	}

	if toolChoiceRaw == nil {
		return policy, nil
	}

	switch v := toolChoiceRaw.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "", "auto":
			policy.Mode = ToolChoiceAuto
		case "none":
			policy.Mode = ToolChoiceNone
			policy.Allowed = nil
		case "required":
			policy.Mode = ToolChoiceRequired
		default:
			return ToolChoicePolicy{}, fmt.Errorf("unsupported tool_choice: %q", v)
		}
	case map[string]any:
		allowedOverride, hasAllowedOverride, err := parseAllowedToolNames(v["allowed_tools"])
		if err != nil {
			return ToolChoicePolicy{}, err
		}
		if hasAllowedOverride {
			filtered := make([]string, 0, len(allowedOverride))
			for _, name := range allowedOverride {
				if _, ok := declaredSet[name]; !ok {
					return ToolChoicePolicy{}, fmt.Errorf("tool_choice.allowed_tools contains undeclared tool %q", name)
				}
				filtered = append(filtered, name)
			}
			policy.Allowed = namesToSet(filtered)
		}

		typ := strings.ToLower(strings.TrimSpace(asString(v["type"])))
		switch typ {
		case "", "auto":
			if hasFunctionSelector(v) {
				name, err := parseForcedToolName(v)
				if err != nil {
					return ToolChoicePolicy{}, err
				}
				policy.Mode = ToolChoiceForced
				policy.ForcedName = name
				policy.Allowed = namesToSet([]string{name})
			} else {
				policy.Mode = ToolChoiceAuto
			}
		case "none":
			policy.Mode = ToolChoiceNone
			policy.Allowed = nil
		case "required":
			policy.Mode = ToolChoiceRequired
		case "function":
			name, err := parseForcedToolName(v)
			if err != nil {
				return ToolChoicePolicy{}, err
			}
			policy.Mode = ToolChoiceForced
			policy.ForcedName = name
			policy.Allowed = namesToSet([]string{name})
		default:
			return ToolChoicePolicy{}, fmt.Errorf("unsupported tool_choice.type: %q", typ)
		}
	default:
		return ToolChoicePolicy{}, fmt.Errorf("tool_choice must be a string or object")
	}

	if policy.Mode == ToolChoiceRequired || policy.Mode == ToolChoiceForced {
		if len(declaredNames) == 0 {
			return ToolChoicePolicy{}, fmt.Errorf("tool_choice=%s requires non-empty tools", policy.Mode)
		}
	}
	if policy.Mode == ToolChoiceForced {
		if _, ok := declaredSet[policy.ForcedName]; !ok {
			return ToolChoicePolicy{}, fmt.Errorf("tool_choice forced function %q is not declared in tools", policy.ForcedName)
		}
	}
	if len(policy.Allowed) == 0 && (policy.Mode == ToolChoiceRequired || policy.Mode == ToolChoiceForced) {
		return ToolChoicePolicy{}, fmt.Errorf("tool_choice policy resolved to empty allowed tool set")
	}
	return policy, nil
}

func parseForcedToolName(v map[string]any) (string, error) {
	if name := strings.TrimSpace(asString(v["name"])); name != "" {
		return name, nil
	}
	if fn, ok := v["function"].(map[string]any); ok {
		if name := strings.TrimSpace(asString(fn["name"])); name != "" {
			return name, nil
		}
	}
	return "", fmt.Errorf("tool_choice function requires name")
}

func parseAllowedToolNames(raw any) ([]string, bool, error) {
	if raw == nil {
		return nil, false, nil
	}
	collectName := func(v any) string {
		if name := strings.TrimSpace(asString(v)); name != "" {
			return name
		}
		if m, ok := v.(map[string]any); ok {
			if name := strings.TrimSpace(asString(m["name"])); name != "" {
				return name
			}
			if fn, ok := m["function"].(map[string]any); ok {
				if name := strings.TrimSpace(asString(fn["name"])); name != "" {
					return name
				}
			}
		}
		return ""
	}

	names := []string{}
	switch x := raw.(type) {
	case []any:
		for _, item := range x {
			name := collectName(item)
			if name == "" {
				return nil, true, fmt.Errorf("tool_choice.allowed_tools contains invalid item")
			}
			names = append(names, name)
		}
	case []string:
		for _, item := range x {
			name := strings.TrimSpace(item)
			if name == "" {
				return nil, true, fmt.Errorf("tool_choice.allowed_tools contains empty name")
			}
			names = append(names, name)
		}
	default:
		return nil, true, fmt.Errorf("tool_choice.allowed_tools must be an array")
	}

	if len(names) == 0 {
		return nil, true, fmt.Errorf("tool_choice.allowed_tools must not be empty")
	}
	return names, true, nil
}

func hasFunctionSelector(v map[string]any) bool {
	if strings.TrimSpace(asString(v["name"])) != "" {
		return true
	}
	if fn, ok := v["function"].(map[string]any); ok {
		return strings.TrimSpace(asString(fn["name"])) != ""
	}
	return false
}

func extractDeclaredToolNames(toolsRaw any) []string {
	tools, ok := toolsRaw.([]any)
	if !ok || len(tools) == 0 {
		return nil
	}
	out := make([]string, 0, len(tools))
	seen := map[string]struct{}{}
	for _, t := range tools {
		tool, ok := t.(map[string]any)
		if !ok {
			continue
		}
		fn, _ := tool["function"].(map[string]any)
		if len(fn) == 0 {
			fn = tool
		}
		name := strings.TrimSpace(asString(fn["name"]))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func namesToSet(names []string) map[string]struct{} {
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		out[trimmed] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// estimateInlineFileTokens extracts the byte count stashed by PreprocessInlineFileInputs
// and converts it to a conservative token estimate. Inline files are typically images or
// documents that the upstream model will process; we use bytes/3 (rather than bytes/4)
// as a slightly pessimistic approximation so the returned context token count stays
// safely above the real value.
func estimateInlineFileTokens(req map[string]any) int {
	raw, ok := req["_inline_file_bytes"]
	if !ok {
		return 0
	}
	var bytes int
	switch v := raw.(type) {
	case int:
		bytes = v
	case int64:
		bytes = int(v)
	case float64:
		bytes = int(v)
	default:
		return 0
	}
	if bytes <= 0 {
		return 0
	}
	return bytes / 3
}
