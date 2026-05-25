package executor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAICompatAccountQuotaRetryWait = 24 * time.Hour
const deepSeekThinkingBudgetMin = 100
const deepSeekThinkingBudgetMax = 32768

type openAICompatProfile struct {
	Kind                     string
	SupportsResponses        bool
	SupportsStreamUsage      bool
	SupportsParallelToolCall bool
	SupportsReasoning        bool
	SupportsMetadata         bool
	SupportsStore            bool
	DefaultHeaders           map[string]string
}

func genericOpenAICompatProfile() openAICompatProfile {
	return openAICompatProfile{
		SupportsResponses:        true,
		SupportsStreamUsage:      true,
		SupportsParallelToolCall: true,
		SupportsReasoning:        true,
		SupportsMetadata:         true,
		SupportsStore:            true,
	}
}

var openAICompatProfiles = map[string]openAICompatProfile{
	"kimi": {
		Kind:                     "kimi",
		SupportsResponses:        false,
		SupportsStreamUsage:      false,
		SupportsParallelToolCall: false,
		SupportsReasoning:        false,
		SupportsMetadata:         false,
		SupportsStore:            false,
	},
	"minimax": {
		Kind:                     "minimax",
		SupportsResponses:        false,
		SupportsStreamUsage:      false,
		SupportsParallelToolCall: false,
		SupportsReasoning:        false,
		SupportsMetadata:         false,
		SupportsStore:            false,
	},
	"xiaomi": {
		Kind:                     "xiaomi",
		SupportsResponses:        false,
		SupportsStreamUsage:      false,
		SupportsParallelToolCall: false,
		SupportsReasoning:        false,
		SupportsMetadata:         false,
		SupportsStore:            false,
	},
	"zhipu": {
		Kind:                     "zhipu",
		SupportsResponses:        false,
		SupportsStreamUsage:      false,
		SupportsParallelToolCall: false,
		SupportsReasoning:        false,
		SupportsMetadata:         false,
		SupportsStore:            false,
	},
	"xfyun": {
		Kind:                     "xfyun",
		SupportsResponses:        false,
		SupportsStreamUsage:      false,
		SupportsParallelToolCall: false,
		SupportsReasoning:        false,
		SupportsMetadata:         false,
		SupportsStore:            false,
	},
	"maas": {
		Kind:                     "maas",
		SupportsResponses:        false,
		SupportsStreamUsage:      false,
		SupportsParallelToolCall: false,
		SupportsReasoning:        false,
		SupportsMetadata:         false,
		SupportsStore:            false,
	},
	"langengyun": {
		Kind:                     "langengyun",
		SupportsResponses:        false,
		SupportsStreamUsage:      false,
		SupportsParallelToolCall: false,
		SupportsReasoning:        false,
		SupportsMetadata:         false,
		SupportsStore:            false,
	},
	"newapi": {
		Kind:                     "newapi",
		SupportsResponses:        false,
		SupportsStreamUsage:      false,
		SupportsParallelToolCall: false,
		SupportsReasoning:        false,
		SupportsMetadata:         false,
		SupportsStore:            false,
	},
}

func openAICompatProfileForKind(kind string) openAICompatProfile {
	normalized := config.NormalizeOpenAICompatibilityKind(kind)
	if profile, ok := openAICompatProfiles[normalized]; ok {
		return profile
	}
	profile := genericOpenAICompatProfile()
	profile.Kind = normalized
	return profile
}

func (e *OpenAICompatExecutor) resolveProfile(auth *cliproxyauth.Auth) openAICompatProfile {
	profile := genericOpenAICompatProfile()
	profile.Kind = ""
	compat := e.resolveCompatConfig(auth)
	if compat == nil {
		if auth != nil && auth.Attributes != nil {
			if kind := config.NormalizeOpenAICompatibilityKind(auth.Attributes["compat_kind"]); kind != "" {
				return openAICompatProfileForKind(kind)
			}
			if kind := inferOpenAICompatKindFromBaseURL(auth.Attributes["base_url"]); kind != "" {
				return openAICompatProfileForKind(kind)
			}
		}
		return profile
	}
	resolved := openAICompatProfileForKind(compat.Kind)
	if resolved.Kind == "" && auth != nil && auth.Attributes != nil {
		if kind := inferOpenAICompatKindFromBaseURL(auth.Attributes["base_url"]); kind != "" {
			resolved = openAICompatProfileForKind(kind)
		}
	}
	if len(compat.Headers) > 0 {
		resolved.DefaultHeaders = config.NormalizeHeaders(compat.Headers)
	}
	return resolved
}

func inferOpenAICompatKindFromBaseURL(rawBaseURL string) string {
	baseURL := strings.TrimSpace(rawBaseURL)
	if baseURL == "" {
		return ""
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	switch host {
	case "api.moonshot.ai", "api.moonshot.cn", "api.kimi.com":
		return "kimi"
	case "api.minimax.io", "api.minimaxi.io", "api.minimaxi.com":
		return "minimax"
	case "api.z.ai", "open.bigmodel.cn", "maas-api.lanyun.net":
		return "zhipu"
	case "api.deepseek.com":
		return "deepseek"
	case "api.xiaomimimo.com":
		return "xiaomi"
	default:
		if config.IsXiaomiTokenPlanBaseURLHost(host) {
			return "xiaomi"
		}
		return ""
	}
}

func applyOpenAICompatDefaultHeaders(req *http.Request, profile openAICompatProfile) {
	if req == nil || len(profile.DefaultHeaders) == 0 {
		return
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	for key, value := range profile.DefaultHeaders {
		if req.Header.Get(key) != "" {
			continue
		}
		req.Header.Set(key, value)
	}
}

func scrubOpenAICompatPayload(payload []byte, profile openAICompatProfile) []byte {
	if len(payload) == 0 {
		return payload
	}
	if !profile.SupportsStore {
		if updated, err := sjson.DeleteBytes(payload, "store"); err == nil {
			payload = updated
		}
	}
	if !profile.SupportsMetadata {
		if updated, err := sjson.DeleteBytes(payload, "metadata"); err == nil {
			payload = updated
		}
	}
	if !profile.SupportsParallelToolCall {
		if updated, err := sjson.DeleteBytes(payload, "parallel_tool_calls"); err == nil {
			payload = updated
		}
	}
	if !profile.SupportsStreamUsage {
		if updated, err := sjson.DeleteBytes(payload, "stream_options"); err == nil {
			payload = updated
		}
	}
	if !profile.SupportsReasoning {
		for _, path := range []string{"reasoning", "reasoning_effort"} {
			if updated, err := sjson.DeleteBytes(payload, path); err == nil {
				payload = updated
			}
		}
		if config.NormalizeOpenAICompatibilityKind(profile.Kind) != "kimi" {
			payload = deleteMessageReasoningContent(payload)
		}
	}
	return payload
}

func scrubOpenAICompatPayloadForModel(payload []byte, profile openAICompatProfile, model string, baseURL string) []byte {
	payload = scrubOpenAICompatPayload(payload, profile)
	payload = repairOpenAICompatToolCallHistory(payload)
	payload = sanitizeOpenAICompatToolSchemas(payload)
	payload = scrubDeepSeekThinkingBudgetForCompat(payload, model, baseURL, profile.Kind)
	if config.NormalizeOpenAICompatibilityKind(profile.Kind) == "kimi" {
		if normalized, err := normalizeKimiToolMessageLinks(payload); err == nil {
			payload = normalized
		} else {
			log.WithError(err).Warn("openai compat executor: failed to normalize kimi tool message history")
		}
	}
	payload = scrubOpenAICompatProviderToolPayload(payload, profile)
	payload = scrubOpenAICompatToolChoice(payload, profile)
	if config.NormalizeOpenAICompatibilityKind(profile.Kind) == "zhipu" {
		payload = scrubZhipuImageURLDataURLs(payload)
	}
	if requiresDeepSeekToolSchemaCompatibility(model) {
		payload = scrubDeepSeekToolPayload(payload, baseURL)
	}
	return payload
}

func scrubDeepSeekThinkingBudgetForCompat(payload []byte, model string, baseURL string, compatKind string) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) || !requiresDeepSeekThinkingBudgetCompatibility(model, baseURL, compatKind) {
		return payload
	}

	if strings.EqualFold(strings.TrimSpace(gjson.GetBytes(payload, "thinking.type").String()), "disabled") {
		payload = deleteDeepSeekThinkingBudgetPaths(payload)
		return payload
	}

	for _, path := range []string{"thinking_budget", "thinking.budget_tokens"} {
		payload = normalizeDeepSeekThinkingBudgetPath(payload, path)
	}

	effort := strings.ToLower(strings.TrimSpace(gjson.GetBytes(payload, "reasoning_effort").String()))
	switch effort {
	case "none", "disabled", "off":
		payload, _ = sjson.SetBytes(payload, "thinking.type", "disabled")
		payload, _ = sjson.DeleteBytes(payload, "reasoning_effort")
		payload = deleteDeepSeekThinkingBudgetPaths(payload)
	case "minimal", "low", "medium":
		payload, _ = sjson.SetBytes(payload, "reasoning_effort", "high")
	case "xhigh":
		payload, _ = sjson.SetBytes(payload, "reasoning_effort", "max")
	}

	return payload
}

func requiresDeepSeekThinkingBudgetCompatibility(model string, baseURL string, compatKind string) bool {
	switch config.NormalizeOpenAICompatibilityKind(compatKind) {
	case "deepseek":
		return true
	case "kimi", "minimax", "xiaomi", "zhipu", "xfyun", "maas", "langengyun", "newapi":
		return false
	}
	switch config.InferCompatKindFromBaseURL(baseURL) {
	case "deepseek":
		return true
	case "kimi", "minimax", "xiaomi", "zhipu", "xfyun", "maas", "langengyun", "newapi":
		return false
	}
	modelName := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(modelName, "deepseek-v4") || strings.Contains(modelName, "deepseek-reasoner")
}

func deleteDeepSeekThinkingBudgetPaths(payload []byte) []byte {
	for _, path := range []string{"thinking_budget", "thinking.budget_tokens"} {
		if updated, err := sjson.DeleteBytes(payload, path); err == nil {
			payload = updated
		}
	}
	return payload
}

func normalizeDeepSeekThinkingBudgetPath(payload []byte, path string) []byte {
	value := gjson.GetBytes(payload, path)
	if !value.Exists() {
		return payload
	}

	budget, ok := deepSeekThinkingBudgetValue(value)
	if !ok || budget <= 0 {
		updated, err := sjson.DeleteBytes(payload, path)
		if err != nil {
			return payload
		}
		return updated
	}
	if budget < deepSeekThinkingBudgetMin {
		budget = deepSeekThinkingBudgetMin
	} else if budget > deepSeekThinkingBudgetMax {
		budget = deepSeekThinkingBudgetMax
	}
	updated, err := sjson.SetBytes(payload, path, budget)
	if err != nil {
		return payload
	}
	return updated
}

func deepSeekThinkingBudgetValue(value gjson.Result) (int, bool) {
	switch value.Type {
	case gjson.Number:
		return int(value.Int()), true
	case gjson.String:
		raw := strings.TrimSpace(value.String())
		if raw == "" {
			return 0, false
		}
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func scrubZhipuImageURLDataURLs(payload []byte) []byte {
	if len(payload) == 0 || !gjson.GetBytes(payload, "messages").IsArray() {
		return payload
	}

	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return payload
	}
	messages, ok := root["messages"].([]any)
	if !ok {
		return payload
	}

	changed := false
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := message["content"].([]any)
		if !ok {
			continue
		}
		for _, rawPart := range parts {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(compatStringValue(part["type"])), "image_url") {
				continue
			}
			imageURL, ok := part["image_url"].(map[string]any)
			if !ok {
				if urlValue := strings.TrimSpace(compatStringValue(part["image_url"])); urlValue != "" {
					if _, data, okData := util.ParseDataURL(urlValue); okData {
						part["image_url"] = map[string]any{"url": data}
						changed = true
					}
				}
				continue
			}
			urlValue := strings.TrimSpace(compatStringValue(imageURL["url"]))
			if urlValue == "" {
				continue
			}
			if _, data, okData := util.ParseDataURL(urlValue); okData {
				imageURL["url"] = data
				changed = true
			}
		}
	}
	if !changed {
		return payload
	}
	out, err := json.Marshal(root)
	if err != nil || !gjson.ValidBytes(out) {
		return payload
	}
	return out
}

func scrubOpenAICompatProviderToolPayload(payload []byte, profile openAICompatProfile) []byte {
	switch config.NormalizeOpenAICompatibilityKind(profile.Kind) {
	case "kimi", "minimax", "xiaomi", "zhipu", "xfyun", "maas", "langengyun", "newapi":
		return scrubOpenAICompatFunctionToolPayload(payload, profile)
	default:
		return payload
	}
}

func sanitizeOpenAICompatToolSchemas(payload []byte) []byte {
	if len(payload) == 0 || !gjson.GetBytes(payload, "tools").IsArray() {
		return payload
	}

	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return payload
	}
	tools, ok := root["tools"].([]any)
	if !ok || len(tools) == 0 {
		return payload
	}

	changed := false
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"input_schema", "parameters", "parametersJsonSchema"} {
			if normalized, okNormalize := normalizeOpenAICompatParameterNode(tool[key]); okNormalize {
				if !jsonValuesEqual(tool[key], normalized) {
					tool[key] = normalized
					changed = true
				}
			}
		}
		function, okFunction := tool["function"].(map[string]any)
		if !okFunction {
			continue
		}
		for _, key := range []string{"parameters", "parametersJsonSchema"} {
			if normalized, okNormalize := normalizeOpenAICompatParameterNode(function[key]); okNormalize {
				if !jsonValuesEqual(function[key], normalized) {
					function[key] = normalized
					changed = true
				}
			}
		}
	}
	if !changed {
		return payload
	}

	root["tools"] = tools
	out, err := json.Marshal(root)
	if err != nil || !gjson.ValidBytes(out) {
		return payload
	}
	return out
}

func scrubOpenAICompatFunctionToolPayload(payload []byte, profile openAICompatProfile) []byte {
	if len(payload) == 0 || !gjson.GetBytes(payload, "tools").IsArray() {
		return payload
	}
	profileKind := config.NormalizeOpenAICompatibilityKind(profile.Kind)

	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return payload
	}
	tools, ok := root["tools"].([]any)
	if !ok || len(tools) == 0 {
		return payload
	}

	cleanedTools := make([]any, 0, len(tools))
	nameMapping := make(map[string]string)
	changed := false
	for _, rawTool := range tools {
		cleaned, ok := normalizeDeepSeekTool(rawTool, false)
		if !ok {
			cleanedTools = append(cleanedTools, rawTool)
			continue
		}
		if profileKind == "kimi" {
			if function, okFunction := cleaned["function"].(map[string]any); okFunction {
				function["strict"] = false
			}
		}
		if originalName := openAICompatOriginalFunctionName(rawTool); originalName != "" {
			if normalizedName := openAICompatNormalizedFunctionName(cleaned); normalizedName != "" && normalizedName != originalName {
				nameMapping[originalName] = normalizedName
			}
		}
		cleanedTools = append(cleanedTools, cleaned)
		if !jsonValuesEqual(rawTool, cleaned) {
			changed = true
		}
	}
	if rewriteOpenAICompatFunctionNameReferences(root, nameMapping) {
		changed = true
	}
	if !changed {
		return payload
	}

	root["tools"] = cleanedTools
	out, err := json.Marshal(root)
	if err != nil || !gjson.ValidBytes(out) {
		return payload
	}
	return out
}

func scrubOpenAICompatToolChoice(payload []byte, profile openAICompatProfile) []byte {
	if config.NormalizeOpenAICompatibilityKind(profile.Kind) != "zhipu" {
		return payload
	}
	toolChoice := gjson.GetBytes(payload, "tool_choice")
	if !toolChoice.Exists() {
		return payload
	}
	if !gjson.GetBytes(payload, "tools").IsArray() {
		if out, err := sjson.DeleteBytes(payload, "tool_choice"); err == nil {
			return out
		}
		return payload
	}
	if toolChoice.Type == gjson.String && strings.EqualFold(strings.TrimSpace(toolChoice.String()), "auto") {
		return payload
	}
	out, err := sjson.SetBytes(payload, "tool_choice", "auto")
	if err != nil {
		return payload
	}
	return out
}

func repairOpenAICompatToolCallHistory(payload []byte) []byte {
	if len(payload) == 0 || !gjson.GetBytes(payload, "messages").IsArray() {
		return payload
	}

	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return payload
	}
	messages, ok := root["messages"].([]any)
	if !ok || len(messages) == 0 {
		return payload
	}

	repaired := make([]any, 0, len(messages))
	changed := false
	var pending map[string]bool
	seenToolCallIDs := make(map[string]bool)
	for idx, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			repaired = append(repaired, rawMessage)
			pending = nil
			continue
		}

		role := strings.TrimSpace(compatStringValue(message["role"]))
		if role == "tool" {
			toolCallID := strings.TrimSpace(compatStringValue(message["tool_call_id"]))
			if pending != nil && pending[toolCallID] {
				repaired = append(repaired, message)
				delete(pending, toolCallID)
			} else {
				changed = true
			}
			continue
		}

		pending = nil
		if role != "assistant" {
			if !openAICompatMessageHasContent(message) {
				changed = true
				continue
			}
			repaired = append(repaired, message)
			continue
		}

		toolCalls, ok := message["tool_calls"].([]any)
		if !ok || len(toolCalls) == 0 {
			if !openAICompatMessageHasContent(message) {
				changed = true
				continue
			}
			repaired = append(repaired, message)
			continue
		}

		nextToolResults := openAICompatToolResultIDsInNextMessages(messages, idx)
		keptToolCalls := make([]any, 0, len(toolCalls))
		keptIDs := make(map[string]bool)
		for _, rawToolCall := range toolCalls {
			normalizedToolCall, changedToolCall, okToolCall := normalizeOpenAICompatHistoryToolCall(rawToolCall)
			if !okToolCall {
				changed = true
				continue
			}
			if changedToolCall {
				changed = true
			}
			toolCallID := strings.TrimSpace(openAICompatToolCallID(rawToolCall))
			if toolCallID == "" || !nextToolResults[toolCallID] || keptIDs[toolCallID] || seenToolCallIDs[toolCallID] {
				changed = true
				continue
			}
			keptToolCalls = append(keptToolCalls, normalizedToolCall)
			keptIDs[toolCallID] = true
			seenToolCallIDs[toolCallID] = true
		}

		if len(keptToolCalls) == 0 {
			delete(message, "tool_calls")
			if !openAICompatMessageHasContent(message) {
				changed = true
				continue
			}
			repaired = append(repaired, message)
			continue
		}
		if len(keptToolCalls) != len(toolCalls) {
			message["tool_calls"] = keptToolCalls
		}
		repaired = append(repaired, message)
		pending = keptIDs
	}

	if !changed {
		return payload
	}
	root["messages"] = repaired
	out, err := json.Marshal(root)
	if err != nil || !gjson.ValidBytes(out) {
		return payload
	}
	return out
}

func openAICompatToolResultIDsInNextMessages(messages []any, assistantIdx int) map[string]bool {
	result := make(map[string]bool)
	for idx := assistantIdx + 1; idx < len(messages); idx++ {
		message, ok := messages[idx].(map[string]any)
		if !ok {
			break
		}
		if strings.TrimSpace(compatStringValue(message["role"])) != "tool" {
			break
		}
		if toolCallID := strings.TrimSpace(compatStringValue(message["tool_call_id"])); toolCallID != "" {
			result[toolCallID] = true
		}
	}
	return result
}

func openAICompatToolCallID(rawToolCall any) string {
	toolCall, ok := rawToolCall.(map[string]any)
	if !ok {
		return ""
	}
	return compatStringValue(toolCall["id"])
}

func normalizeOpenAICompatHistoryToolCall(rawToolCall any) (any, bool, bool) {
	toolCall, ok := rawToolCall.(map[string]any)
	if !ok {
		return nil, false, false
	}
	changed := false

	function, ok := toolCall["function"].(map[string]any)
	if !ok {
		function = map[string]any{}
		toolCall["function"] = function
		changed = true
	}

	name, okName := normalizeOpenAICompatFunctionName(compatStringValue(function["name"]))
	if !okName {
		name, okName = normalizeOpenAICompatFunctionName(compatStringValue(toolCall["name"]))
	}
	if !okName {
		return nil, changed, false
	}
	if compatStringValue(function["name"]) != name {
		function["name"] = name
		changed = true
	}
	if strings.TrimSpace(compatStringValue(toolCall["type"])) == "" {
		toolCall["type"] = "function"
		changed = true
	}
	if _, okArguments := function["arguments"].(string); !okArguments {
		if function["arguments"] == nil {
			function["arguments"] = ""
		} else if rawArguments, err := json.Marshal(function["arguments"]); err == nil {
			function["arguments"] = string(rawArguments)
		} else {
			function["arguments"] = ""
		}
		changed = true
	}

	return toolCall, changed, true
}

func openAICompatMessageHasContent(message map[string]any) bool {
	content, ok := message["content"]
	if !ok || content == nil {
		return false
	}
	switch v := content.(type) {
	case string:
		return strings.TrimSpace(v) != ""
	case []any:
		for _, rawPart := range v {
			switch part := rawPart.(type) {
			case string:
				if strings.TrimSpace(part) != "" {
					return true
				}
			case map[string]any:
				if strings.TrimSpace(compatStringValue(part["text"])) != "" {
					return true
				}
				if imageURL, okImageURL := part["image_url"].(map[string]any); okImageURL {
					if strings.TrimSpace(compatStringValue(imageURL["url"])) != "" {
						return true
					}
				} else if strings.TrimSpace(compatStringValue(part["image_url"])) != "" {
					return true
				}
			default:
				if rawPart != nil {
					return true
				}
			}
		}
		return false
	default:
		return true
	}
}

func requiresDeepSeekToolSchemaCompatibility(model string) bool {
	modelName := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(modelName, "deepseek-v4")
}

func scrubDeepSeekToolPayload(payload []byte, baseURL string) []byte {
	if len(payload) == 0 || !gjson.GetBytes(payload, "tools").IsArray() {
		return payload
	}

	var root map[string]any
	if err := json.Unmarshal(payload, &root); err != nil {
		return payload
	}
	tools, ok := root["tools"].([]any)
	if !ok || len(tools) == 0 {
		return payload
	}

	keepStrict := deepSeekBaseURLUsesBeta(baseURL) && allDeepSeekFunctionToolsStrict(tools)
	cleanedTools := make([]any, 0, len(tools))
	nameMapping := make(map[string]string)
	changed := false
	for _, rawTool := range tools {
		cleaned, ok := normalizeDeepSeekTool(rawTool, keepStrict)
		if !ok {
			cleanedTools = append(cleanedTools, rawTool)
			continue
		}
		if originalName := openAICompatOriginalFunctionName(rawTool); originalName != "" {
			if normalizedName := openAICompatNormalizedFunctionName(cleaned); normalizedName != "" && normalizedName != originalName {
				nameMapping[originalName] = normalizedName
			}
		}
		cleanedTools = append(cleanedTools, cleaned)
		if !jsonValuesEqual(rawTool, cleaned) {
			changed = true
		}
	}
	if rewriteOpenAICompatFunctionNameReferences(root, nameMapping) {
		changed = true
	}
	if !changed {
		return payload
	}

	root["tools"] = cleanedTools
	out, err := json.Marshal(root)
	if err != nil || !gjson.ValidBytes(out) {
		return payload
	}
	return out
}

func deepSeekBaseURLUsesBeta(baseURL string) bool {
	baseURL = strings.ToLower(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	return strings.HasSuffix(baseURL, "/beta")
}

func allDeepSeekFunctionToolsStrict(tools []any) bool {
	foundFunction := false
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		function := deepSeekFunctionToolNode(tool)
		if function == nil {
			continue
		}
		foundFunction = true
		if strict, _ := function["strict"].(bool); strict {
			continue
		}
		if strict, _ := tool["strict"].(bool); strict {
			continue
		}
		return false
	}
	return foundFunction
}

func normalizeDeepSeekTool(rawTool any, keepStrict bool) (map[string]any, bool) {
	tool, ok := rawTool.(map[string]any)
	if !ok {
		return nil, false
	}

	function := deepSeekFunctionToolNode(tool)
	if function == nil {
		return nil, false
	}

	name, ok := normalizeOpenAICompatFunctionName(compatStringValue(function["name"]))
	if !ok {
		name, ok = normalizeOpenAICompatFunctionName(compatStringValue(tool["name"]))
	}
	if !ok {
		return nil, false
	}

	normalizedFunction := map[string]any{"name": name}
	if description := compatStringValue(function["description"]); strings.TrimSpace(description) != "" {
		normalizedFunction["description"] = description
	} else if fallback := compatStringValue(tool["description"]); strings.TrimSpace(fallback) != "" {
		normalizedFunction["description"] = fallback
	}

	parameters, parametersRaw := deepSeekToolParameters(function, tool)
	if keepStrict {
		normalizedFunction["parameters"] = schemaValueFromString(util.CleanJSONSchemaForOpenAIStructuredOutput(parametersRaw))
		normalizedFunction["strict"] = true
	} else {
		normalizedFunction["parameters"] = parameters
	}

	return map[string]any{
		"type":     "function",
		"function": normalizedFunction,
	}, true
}

func openAICompatOriginalFunctionName(rawTool any) string {
	tool, ok := rawTool.(map[string]any)
	if !ok {
		return ""
	}
	if function, okFunction := tool["function"].(map[string]any); okFunction {
		if name := strings.TrimSpace(compatStringValue(function["name"])); name != "" {
			return name
		}
	}
	return strings.TrimSpace(compatStringValue(tool["name"]))
}

func openAICompatNormalizedFunctionName(tool map[string]any) string {
	if tool == nil {
		return ""
	}
	function, ok := tool["function"].(map[string]any)
	if !ok {
		return ""
	}
	return strings.TrimSpace(compatStringValue(function["name"]))
}

func rewriteOpenAICompatFunctionNameReferences(root map[string]any, mapping map[string]string) bool {
	if len(mapping) == 0 {
		return false
	}
	changed := false
	rename := func(value any) (string, bool) {
		name := strings.TrimSpace(compatStringValue(value))
		if name == "" {
			return "", false
		}
		mapped := mapping[name]
		if mapped == "" || mapped == name {
			return "", false
		}
		return mapped, true
	}

	if toolChoice, ok := root["tool_choice"].(map[string]any); ok {
		if mapped, okMap := rename(toolChoice["name"]); okMap {
			toolChoice["name"] = mapped
			changed = true
		}
		if function, okFunction := toolChoice["function"].(map[string]any); okFunction {
			if mapped, okMap := rename(function["name"]); okMap {
				function["name"] = mapped
				changed = true
			}
		}
	}

	messages, ok := root["messages"].([]any)
	if !ok {
		return changed
	}
	for _, rawMessage := range messages {
		message, okMessage := rawMessage.(map[string]any)
		if !okMessage {
			continue
		}
		toolCalls, okToolCalls := message["tool_calls"].([]any)
		if !okToolCalls {
			continue
		}
		for _, rawToolCall := range toolCalls {
			toolCall, okToolCall := rawToolCall.(map[string]any)
			if !okToolCall {
				continue
			}
			function, okFunction := toolCall["function"].(map[string]any)
			if !okFunction {
				continue
			}
			if mapped, okMap := rename(function["name"]); okMap {
				function["name"] = mapped
				changed = true
			}
		}
	}
	return changed
}

func deepSeekFunctionToolNode(tool map[string]any) map[string]any {
	if function, ok := tool["function"].(map[string]any); ok {
		return function
	}
	if _, hasName := tool["name"]; !hasName {
		return nil
	}
	if _, hasInputSchema := tool["input_schema"]; hasInputSchema {
		return tool
	}
	if _, hasParameters := tool["parameters"]; hasParameters {
		return tool
	}
	if toolType := strings.TrimSpace(compatStringValue(tool["type"])); toolType == "" || toolType == "function" {
		return tool
	}
	return nil
}

func deepSeekToolParameters(function map[string]any, tool map[string]any) (any, string) {
	for _, candidate := range []any{
		function["parameters"],
		function["parametersJsonSchema"],
		tool["parameters"],
		tool["input_schema"],
		tool["parametersJsonSchema"],
	} {
		if candidate == nil {
			continue
		}
		normalized := normalizeOpenAICompatParameters(candidate)
		raw, err := json.Marshal(normalized)
		if err != nil || !gjson.ValidBytes(raw) {
			continue
		}
		return normalized, string(raw)
	}
	defaultSchema := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
	raw, _ := json.Marshal(defaultSchema)
	return defaultSchema, string(raw)
}

func normalizeOpenAICompatParameters(parameters any) any {
	normalized, ok := normalizeOpenAICompatParameterNode(parameters)
	if !ok {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	return normalized
}

func normalizeOpenAICompatParameterNode(parameters any) (map[string]any, bool) {
	if raw, ok := parameters.(string); ok {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil, false
		}
		if schemaType, okType := normalizeOpenAICompatSchemaType(raw); okType {
			return openAICompatSchemaForType(schemaType), true
		}
		if !gjson.Valid(raw) {
			return nil, false
		}
		var parsed any
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			return nil, false
		}
		return normalizeOpenAICompatSchemaNode(parsed)
	}
	return normalizeOpenAICompatSchemaNode(parameters)
}

func normalizeOpenAICompatSchemaNode(node any) (map[string]any, bool) {
	schema, ok := node.(map[string]any)
	if !ok {
		if schemaType, okType := normalizeOpenAICompatScalarSchemaType(node); okType {
			return openAICompatSchemaForType(schemaType), true
		}
		return nil, false
	}

	out := make(map[string]any, len(schema)+2)
	for key, value := range schema {
		if value == nil {
			continue
		}
		switch key {
		case "properties":
			if properties, okProperties := value.(map[string]any); okProperties {
				cleanedProperties := make(map[string]any, len(properties))
				for propertyName, rawProperty := range properties {
					if propertyName = strings.TrimSpace(propertyName); propertyName == "" {
						continue
					}
					if normalizedProperty, okNormalize := normalizeOpenAICompatSchemaNode(rawProperty); okNormalize {
						cleanedProperties[propertyName] = normalizedProperty
					} else {
						cleanedProperties[propertyName] = map[string]any{"type": "string"}
					}
				}
				out[key] = cleanedProperties
			} else {
				out[key] = map[string]any{}
			}
		case "items":
			switch items := value.(type) {
			case []any:
				cleanedItems := make([]any, 0, len(items))
				for _, rawItem := range items {
					if normalizedItem, okNormalize := normalizeOpenAICompatSchemaNode(rawItem); okNormalize {
						cleanedItems = append(cleanedItems, normalizedItem)
					}
				}
				if len(cleanedItems) > 0 {
					out[key] = cleanedItems
				}
			default:
				if normalizedItem, okNormalize := normalizeOpenAICompatSchemaNode(items); okNormalize {
					out[key] = normalizedItem
				}
			}
		case "additionalProperties":
			switch additionalProperties := value.(type) {
			case bool:
				out[key] = additionalProperties
			default:
				if normalizedAdditional, okNormalize := normalizeOpenAICompatSchemaNode(additionalProperties); okNormalize {
					out[key] = normalizedAdditional
				}
			}
		case "required":
			if required := normalizeOpenAICompatStringArray(value); len(required) > 0 {
				out[key] = required
			}
		case "anyOf", "oneOf", "allOf":
			if branches, okBranches := value.([]any); okBranches {
				cleanedBranches := make([]any, 0, len(branches))
				for _, rawBranch := range branches {
					if normalizedBranch, okNormalize := normalizeOpenAICompatSchemaNode(rawBranch); okNormalize {
						cleanedBranches = append(cleanedBranches, normalizedBranch)
					}
				}
				if len(cleanedBranches) > 0 {
					out[key] = cleanedBranches
				}
			}
		case "type":
			if schemaType, okType := normalizeOpenAICompatScalarSchemaType(value); okType {
				out[key] = schemaType
			}
		default:
			out[key] = value
		}
	}
	schemaType := strings.TrimSpace(compatStringValue(out["type"]))
	if schemaType == "" {
		schemaType = "object"
		out["type"] = schemaType
	}
	if schemaType == "object" {
		if _, okProperties := out["properties"]; !okProperties {
			out["properties"] = map[string]any{}
		}
	}
	if schemaType == "array" {
		if _, okItems := out["items"]; !okItems {
			out["items"] = map[string]any{"type": "string"}
		}
	}
	if _, hasAnyOf := out["anyOf"]; hasAnyOf {
		delete(out, "type")
	}
	if _, hasOneOf := out["oneOf"]; hasOneOf {
		delete(out, "type")
	}
	return out, true
}

func normalizeOpenAICompatScalarSchemaType(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return normalizeOpenAICompatSchemaType(typed)
	case []any:
		for _, item := range typed {
			if str, ok := item.(string); ok {
				if schemaType, okType := normalizeOpenAICompatSchemaType(str); okType && schemaType != "null" {
					return schemaType, true
				}
			}
		}
		return "", false
	default:
		return "", false
	}
}

func normalizeOpenAICompatSchemaType(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "object", "array", "string", "number", "integer", "boolean", "null":
		return strings.ToLower(strings.TrimSpace(raw)), true
	default:
		return "", false
	}
}

func openAICompatSchemaForType(schemaType string) map[string]any {
	schema := map[string]any{"type": schemaType}
	switch schemaType {
	case "object":
		schema["properties"] = map[string]any{}
	case "array":
		schema["items"] = map[string]any{"type": "string"}
	}
	return schema
}

func normalizeOpenAICompatStringArray(value any) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if str, ok := item.(string); ok {
				if str = strings.TrimSpace(str); str != "" {
					out = append(out, str)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func schemaValueFromString(raw string) any {
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	return parsed
}

func compatStringValue(value any) string {
	str, _ := value.(string)
	return str
}

func normalizeOpenAICompatFunctionName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}

	var builder strings.Builder
	builder.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '_' || r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}

	normalized := builder.String()
	if normalized == "" {
		return "", false
	}
	first := normalized[0]
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || first == '_') {
		normalized = "_" + normalized
	}
	if len(normalized) > 64 {
		normalized = normalized[:64]
	}
	for len(normalized) < 3 {
		normalized += "_"
	}
	return normalized, true
}

func jsonValuesEqual(left any, right any) bool {
	leftJSON, errLeft := json.Marshal(left)
	rightJSON, errRight := json.Marshal(right)
	return errLeft == nil && errRight == nil && string(leftJSON) == string(rightJSON)
}

func deleteMessageReasoningContent(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload
	}
	messages.ForEach(func(key, value gjson.Result) bool {
		if !value.Get("reasoning_content").Exists() {
			return true
		}
		updated := value.Raw
		if next, err := sjson.Delete(updated, "reasoning_content"); err == nil {
			updated = next
		}
		if nextPayload, err := sjson.SetRawBytes(payload, fmt.Sprintf("messages.%s", key.String()), []byte(updated)); err == nil {
			payload = nextPayload
		}
		return true
	})
	return payload
}

func summarizeOpenAICompatError(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" || !gjson.ValidBytes(body) {
		return trimmed
	}
	message := firstNonEmptyJSONValue(body,
		"error.message",
		"message",
		"msg",
		"error.msg",
		"detail",
		"error.detail",
		"reason",
		"error.reason",
		"error.metadata.message",
		"error.metadata.reason",
		"error.details.0.message",
		"error.details.0.reason",
		"error.details.0.description",
	)
	if message == "" {
		return trimmed
	}
	label := firstNonEmptyJSONValue(body, "error.type", "type", "error.code", "code", "error.err_code")
	if label == "" {
		return message
	}
	lowerMessage := strings.ToLower(message)
	lowerLabel := strings.ToLower(label)
	if strings.Contains(lowerMessage, lowerLabel) {
		return message
	}
	return label + ": " + message
}

func firstNonEmptyJSONValue(body []byte, paths ...string) string {
	for _, path := range paths {
		value := gjson.GetBytes(body, path)
		if !value.Exists() {
			continue
		}
		switch value.Type {
		case gjson.String:
			if trimmed := strings.TrimSpace(value.String()); trimmed != "" {
				return trimmed
			}
		case gjson.Number:
			if raw := strings.TrimSpace(value.Raw); raw != "" {
				return raw
			}
		}
	}
	return ""
}

func openAICompatRetryAfter(headers http.Header, body []byte) *time.Duration {
	now := time.Now()
	if headers != nil {
		if retry := parseOpenAICompatRetryAfterString(headers.Get("Retry-After"), false, now); retry != nil {
			return retry
		}
	}

	candidates := []struct {
		path      string
		timestamp bool
	}{
		{path: "retry_after"},
		{path: "retryAfter"},
		{path: "retry_after_seconds"},
		{path: "retryAfterSeconds"},
		{path: "retry_delay"},
		{path: "retryDelay"},
		{path: "reset_after"},
		{path: "resetAfter"},
		{path: "reset_in"},
		{path: "resetIn"},
		{path: "reset_in_seconds"},
		{path: "resetInSeconds"},
		{path: "cooldown"},
		{path: "cooldown_seconds"},
		{path: "cooldownSeconds"},
		{path: "error.retry_after"},
		{path: "error.retryAfter"},
		{path: "error.retry_after_seconds"},
		{path: "error.retryAfterSeconds"},
		{path: "error.retry_delay"},
		{path: "error.retryDelay"},
		{path: "error.reset_after"},
		{path: "error.resetAfter"},
		{path: "error.reset_in"},
		{path: "error.resetIn"},
		{path: "error.reset_in_seconds"},
		{path: "error.resetInSeconds"},
		{path: "error.cooldown"},
		{path: "error.cooldown_seconds"},
		{path: "error.cooldownSeconds"},
		{path: "error.metadata.retry_after"},
		{path: "error.metadata.retry_after_seconds"},
		{path: "error.metadata.retryDelay"},
		{path: "error.metadata.reset_after"},
		{path: "error.metadata.reset_in_seconds"},
		{path: "retry_at", timestamp: true},
		{path: "retryAt", timestamp: true},
		{path: "reset_at", timestamp: true},
		{path: "resetAt", timestamp: true},
		{path: "error.retry_at", timestamp: true},
		{path: "error.retryAt", timestamp: true},
		{path: "error.reset_at", timestamp: true},
		{path: "error.resetAt", timestamp: true},
		{path: "error.metadata.retry_at", timestamp: true},
		{path: "error.metadata.retryAt", timestamp: true},
		{path: "error.metadata.reset_at", timestamp: true},
		{path: "error.metadata.resetAt", timestamp: true},
	}
	for _, candidate := range candidates {
		value := gjson.GetBytes(body, candidate.path)
		if !value.Exists() {
			continue
		}
		if retry := parseOpenAICompatRetryAfterValue(value, candidate.timestamp, now); retry != nil {
			return retry
		}
	}
	if openAICompatAccountQuotaLikeMessage(strings.ToLower(summarizeOpenAICompatError(body))) {
		duration := openAICompatAccountQuotaRetryWait
		return &duration
	}
	return nil
}

func parseOpenAICompatRetryAfterValue(value gjson.Result, timestamp bool, now time.Time) *time.Duration {
	switch value.Type {
	case gjson.String:
		return parseOpenAICompatRetryAfterString(value.String(), timestamp, now)
	case gjson.Number:
		number := value.Float()
		if number <= 0 {
			return nil
		}
		if timestamp {
			return durationUntilUnix(number, now)
		}
		duration := time.Duration(number * float64(time.Second))
		if duration <= 0 {
			return nil
		}
		return &duration
	default:
		return nil
	}
}

func parseOpenAICompatRetryAfterString(raw string, timestamp bool, now time.Time) *time.Duration {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if parsed, err := strconv.ParseFloat(trimmed, 64); err == nil {
		if parsed <= 0 {
			return nil
		}
		if timestamp {
			return durationUntilUnix(parsed, now)
		}
		duration := time.Duration(parsed * float64(time.Second))
		if duration <= 0 {
			return nil
		}
		return &duration
	}
	if duration, err := time.ParseDuration(trimmed); err == nil && duration > 0 {
		return &duration
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, http.TimeFormat} {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			duration := time.Until(parsed)
			if duration > 0 {
				return &duration
			}
		}
	}
	if timestamp {
		return nil
	}
	if parsed, err := http.ParseTime(trimmed); err == nil {
		duration := parsed.Sub(now)
		if duration > 0 {
			return &duration
		}
	}
	return nil
}

func durationUntilUnix(value float64, now time.Time) *time.Duration {
	if value <= 0 {
		return nil
	}
	var target time.Time
	switch {
	case value >= 1e12:
		target = time.UnixMilli(int64(value))
	case value >= 1e9:
		target = time.Unix(int64(value), 0)
	default:
		return nil
	}
	duration := target.Sub(now)
	if duration <= 0 {
		return nil
	}
	return &duration
}

func normalizeOpenAICompatStatus(code int, message string) int {
	lower := strings.ToLower(strings.TrimSpace(message))
	switch {
	case openAICompatPaymentLikeMessage(lower) && code != http.StatusPaymentRequired && code != http.StatusForbidden:
		return http.StatusPaymentRequired
	case openAICompatQuotaLikeMessage(lower) && code != http.StatusTooManyRequests:
		return http.StatusTooManyRequests
	case openAICompatAvailabilityMessage(lower) && (code == http.StatusBadRequest || code == http.StatusForbidden):
		return http.StatusServiceUnavailable
	default:
		return code
	}
}

func openAICompatPaymentLikeMessage(message string) bool {
	return containsAny(message,
		"payment required",
		"insufficient balance",
		"balance insufficient",
		"account balance insufficient",
		"余额不足",
		"账户余额不足",
		"帐户余额不足",
		"钱包余额不足",
		"充值后重试",
	)
}

func openAICompatQuotaLikeMessage(message string) bool {
	if openAICompatAccountQuotaLikeMessage(message) {
		return true
	}
	return containsAny(message,
		"insufficient_quota",
		"quota exhausted",
		"quota_exhausted",
		"rate limit",
		"rate_limit",
		"too many requests",
		"resource exhausted",
		"额度已用尽",
		"额度不足",
		"频率限制",
	)
}

func openAICompatAccountQuotaLikeMessage(message string) bool {
	return containsAny(message,
		"usage limit",
		"billing cycle",
		"quota will be refreshed",
		"refreshed in the next cycle",
		"quota-upgrade",
		"monthly quota",
	)
}

func openAICompatAvailabilityMessage(message string) bool {
	return containsAny(message,
		"no available key",
		"no available api key",
		"no available channel",
		"channel unavailable",
		"upstream unavailable",
		"provider unavailable",
		"no healthy upstream",
		"no available upstream",
		"无可用 key",
		"无可用key",
		"无可用渠道",
		"渠道不可用",
		"上游不可用",
	)
}

func containsAny(message string, patterns ...string) bool {
	for _, pattern := range patterns {
		if pattern != "" && strings.Contains(message, pattern) {
			return true
		}
	}
	return false
}

func logOpenAICompatUpstreamError(profile openAICompatProfile, auth *cliproxyauth.Auth, routeModel string, statusCode int, retryAfter *time.Duration, contentType string, body []byte) {
	entry := log.WithFields(log.Fields{
		"provider":    profile.KindOrFallback(auth),
		"compat_kind": profile.Kind,
		"model":       strings.TrimSpace(routeModel),
		"status":      statusCode,
	})
	if auth != nil {
		if authID := strings.TrimSpace(auth.ID); authID != "" {
			entry = entry.WithField("auth_id", authID)
		}
		if compatName := strings.TrimSpace(auth.Attributes["compat_name"]); compatName != "" {
			entry = entry.WithField("compat_name", compatName)
		}
	}
	if retryAfter != nil {
		entry = entry.WithField("retry_after", retryAfter.String())
	}
	entry.Warnf("openai compat upstream error: %s", helps.SummarizeErrorBody(contentType, body))
}

func newOpenAICompatStatusErr(profile openAICompatProfile, auth *cliproxyauth.Auth, routeModel string, statusCode int, headers http.Header, contentType string, body []byte) statusErr {
	retryAfter := openAICompatRetryAfter(headers, body)
	logOpenAICompatUpstreamError(profile, auth, routeModel, statusCode, retryAfter, contentType, body)
	message := summarizeOpenAICompatError(body)
	return statusErr{
		code:               normalizeOpenAICompatStatus(statusCode, message),
		providerStatusCode: statusCode,
		msg:                message,
		errorCode:          firstNonEmptyJSONValue(body, "error.code", "code", "error.type", "type", "error.err_code"),
		retryAfter:         retryAfter,
	}
}

func (p openAICompatProfile) KindOrFallback(auth *cliproxyauth.Auth) string {
	if p.Kind != "" {
		return p.Kind
	}
	if auth != nil {
		if auth.Attributes != nil {
			if providerKey := strings.TrimSpace(auth.Attributes["provider_key"]); providerKey != "" {
				return providerKey
			}
		}
		if provider := strings.TrimSpace(auth.Provider); provider != "" {
			return provider
		}
	}
	return "openai-compatibility"
}
