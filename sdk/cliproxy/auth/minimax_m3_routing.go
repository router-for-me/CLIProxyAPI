package auth

import (
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tiktoken-go/tokenizer"
)

const (
	miniMaxM2SafeTotalTokens        int64 = 180000
	miniMaxM3RequiredMetadataKey          = "__cliproxy_minimax_m3_required"
	miniMaxM3StandardModel                = "MiniMax-M3"
	miniMaxLargeToolHistoryMessages       = 100
	miniMaxLargeToolHistoryTools          = 40
)

var (
	miniMaxRoutingTokenizerOnce sync.Once
	miniMaxRoutingTokenizer     tokenizer.Codec
	miniMaxRoutingTokenizerErr  error
)

func filterMiniMaxM3RequiredExecutionModels(routeModel string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, candidates []string) []string {
	candidates = rewriteMiniMaxM3HighspeedRouteToStandard(routeModel, opts, candidates)
	candidates = filterClaudeSonnetMiniMaxM3Highspeed(routeModel, opts, candidates)
	candidates = filterMiniMaxLargeToolHistoryM3Highspeed(req, opts, candidates)
	if len(candidates) == 0 || !miniMaxCandidateSetCanRouteToM3(routeModel, opts, candidates) {
		return candidates
	}
	if !miniMaxRequestRequiresM3(req, opts) {
		return candidates
	}

	filtered := make([]string, 0, len(candidates))
	removed := false
	for _, candidate := range candidates {
		if isMiniMaxModel(candidate) && !isMiniMaxM3SeriesModel(candidate) {
			removed = true
			continue
		}
		if isMiniMaxM3HighspeedModel(candidate) {
			removed = true
			continue
		}
		filtered = append(filtered, candidate)
	}
	if !removed {
		return candidates
	}
	return filtered
}

func rewriteMiniMaxM3HighspeedRouteToStandard(routeModel string, opts cliproxyexecutor.Options, candidates []string) []string {
	if len(candidates) == 0 {
		return candidates
	}
	if !isMiniMaxM3HighspeedModel(routeModel) &&
		!isMiniMaxM3HighspeedModel(requestedModelAliasFromOptions(opts, routeModel)) {
		return candidates
	}

	out := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	changed := false
	for _, candidate := range candidates {
		resolved := candidate
		if isMiniMaxM3HighspeedModel(candidate) {
			resolved = preserveResolvedModelSuffix(miniMaxM3StandardModel, thinking.ParseSuffix(candidate))
			changed = true
		}
		key := strings.ToLower(strings.TrimSpace(resolved))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, resolved)
	}
	if !changed {
		return candidates
	}
	return out
}

func filterClaudeSonnetMiniMaxM3Highspeed(routeModel string, opts cliproxyexecutor.Options, candidates []string) []string {
	if len(candidates) == 0 {
		return candidates
	}
	if !isClaudeSonnet46FallbackModel(routeModel) &&
		!isClaudeSonnet46FallbackModel(requestedModelAliasFromOptions(opts, routeModel)) {
		return candidates
	}

	filtered := make([]string, 0, len(candidates))
	removed := false
	for _, candidate := range candidates {
		if isMiniMaxM3HighspeedModel(candidate) {
			removed = true
			continue
		}
		filtered = append(filtered, candidate)
	}
	if !removed {
		return candidates
	}
	return filtered
}

func filterMiniMaxLargeToolHistoryM3Highspeed(req cliproxyexecutor.Request, opts cliproxyexecutor.Options, candidates []string) []string {
	if len(candidates) < 2 || !miniMaxRequestHasLargeToolHistory(req, opts) {
		return candidates
	}

	filtered := make([]string, 0, len(candidates))
	removed := false
	for _, candidate := range candidates {
		if isMiniMaxM3HighspeedModel(candidate) {
			removed = true
			continue
		}
		filtered = append(filtered, candidate)
	}
	if !removed || len(filtered) == 0 {
		return candidates
	}
	return filtered
}

func miniMaxCandidateSetCanRouteToM3(routeModel string, opts cliproxyexecutor.Options, candidates []string) bool {
	routeIsSonnetAlias := isClaudeSonnet46FallbackModel(routeModel) ||
		isClaudeSonnet46FallbackModel(requestedModelAliasFromOptions(opts, routeModel))
	hasMiniMaxM3 := false
	hasLegacyMiniMax := false
	for _, candidate := range candidates {
		if !isMiniMaxModel(candidate) {
			continue
		}
		if isMiniMaxM3SeriesModel(candidate) {
			hasMiniMaxM3 = true
		} else {
			hasLegacyMiniMax = true
		}
	}
	if routeIsSonnetAlias {
		return hasMiniMaxM3 || hasLegacyMiniMax
	}
	return hasMiniMaxM3 && hasLegacyMiniMax
}

func miniMaxRequestRequiresM3(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) bool {
	if opts.Metadata != nil {
		if raw, ok := opts.Metadata[miniMaxM3RequiredMetadataKey]; ok {
			if required, okBool := raw.(bool); okBool {
				return required
			}
		}
	}

	payload := miniMaxRoutingPayload(req, opts)
	required := requestHasMiniMaxM3MultimodalInput(payload) || miniMaxRequestExceedsM2SafeContext(payload)
	if opts.Metadata != nil {
		opts.Metadata[miniMaxM3RequiredMetadataKey] = required
	}
	return required
}

func miniMaxRoutingPayload(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) []byte {
	if len(opts.OriginalRequest) > 0 {
		return opts.OriginalRequest
	}
	return req.Payload
}

func miniMaxRequestHasLargeToolHistory(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) bool {
	messageCount := intMetadataValue(opts.Metadata[cliproxyexecutor.MessageCountMetadataKey])
	toolCount := intMetadataValue(opts.Metadata[cliproxyexecutor.ToolCountMetadataKey])
	if messageCount >= miniMaxLargeToolHistoryMessages || toolCount >= miniMaxLargeToolHistoryTools {
		return true
	}

	payload := miniMaxRoutingPayload(req, opts)
	if messageCount <= 0 {
		messageCount = miniMaxRoutingMessageCount(payload)
	}
	if toolCount <= 0 {
		toolCount = miniMaxRoutingToolHistoryCount(payload)
	}
	return messageCount >= miniMaxLargeToolHistoryMessages || toolCount >= miniMaxLargeToolHistoryTools
}

func miniMaxRoutingMessageCount(payload []byte) int {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return 0
	}
	if messages := gjson.GetBytes(payload, "messages"); messages.IsArray() {
		return len(messages.Array())
	}
	input := gjson.GetBytes(payload, "input")
	if input.IsArray() {
		return len(input.Array())
	}
	if input.Exists() && strings.TrimSpace(input.Raw) != "" {
		return 1
	}
	return 0
}

func miniMaxRoutingToolHistoryCount(payload []byte) int {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return 0
	}
	interactionCount := 0
	var walk func(gjson.Result)
	walk = func(node gjson.Result) {
		if !node.IsObject() && !node.IsArray() {
			return
		}
		if node.IsObject() {
			switch strings.ToLower(strings.TrimSpace(node.Get("type").String())) {
			case "tool_use", "tool_result", "function_call", "function_call_output", "mcp_tool_use", "mcp_tool_result":
				interactionCount++
			}
			if toolCalls := node.Get("tool_calls"); toolCalls.IsArray() {
				interactionCount += len(toolCalls.Array())
			}
		}
		node.ForEach(func(_, child gjson.Result) bool {
			walk(child)
			return true
		})
	}
	if messages := gjson.GetBytes(payload, "messages"); messages.IsArray() {
		walk(messages)
	}
	if input := gjson.GetBytes(payload, "input"); input.IsArray() {
		walk(input)
	}
	if interactionCount > 0 {
		return interactionCount
	}
	return miniMaxRoutingDeclaredToolCount(payload)
}

func miniMaxRoutingDeclaredToolCount(payload []byte) int {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.IsArray() {
		return 0
	}
	return len(tools.Array())
}

func miniMaxRequestHasImageInput(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) bool {
	return requestHasMiniMaxM3ImageInput(miniMaxRoutingPayload(req, opts))
}

func requestHasMiniMaxM3ImageInput(payload []byte) bool {
	return requestHasMiniMaxM3PartType(payload, isMiniMaxM3ImagePartType)
}

func requestHasMiniMaxM3MultimodalInput(payload []byte) bool {
	return requestHasMiniMaxM3PartType(payload, isMiniMaxM3MultimodalPartType)
}

func requestHasMiniMaxM3PartType(payload []byte, match func(string) bool) bool {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return false
	}
	found := false
	var walk func(gjson.Result) bool
	walk = func(node gjson.Result) bool {
		if found {
			return false
		}
		if node.Type == gjson.JSON {
			if partType := strings.ToLower(strings.TrimSpace(node.Get("type").String())); match(partType) {
				found = true
				return false
			}
			node.ForEach(func(_, child gjson.Result) bool {
				return walk(child)
			})
		}
		return !found
	}
	walk(gjson.ParseBytes(payload))
	return found
}

func isMiniMaxM3ImagePartType(partType string) bool {
	switch partType {
	case "image", "image_url", "input_image":
		return true
	default:
		return false
	}
}

func isMiniMaxM3MultimodalPartType(partType string) bool {
	switch partType {
	case "image", "image_url", "input_image", "video", "video_url", "input_video":
		return true
	default:
		return false
	}
}

func miniMaxRequestExceedsM2SafeContext(payload []byte) bool {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return false
	}
	inputTokens := estimateMiniMaxRoutingInputTokens(payload)
	if inputTokens <= 0 {
		return false
	}
	totalBudget := inputTokens + requestedOutputTokenBudget(payload)
	return totalBudget >= miniMaxM2SafeTotalTokens
}

func estimateMiniMaxRoutingInputTokens(payload []byte) int64 {
	root := gjson.ParseBytes(payload)
	segments := make([]string, 0, 64)
	collectMiniMaxRoutingStrings(root, &segments)
	joined := strings.TrimSpace(strings.Join(segments, "\n"))
	if joined == "" {
		return 0
	}
	miniMaxRoutingTokenizerOnce.Do(func() {
		miniMaxRoutingTokenizer, miniMaxRoutingTokenizerErr = tokenizer.Get(tokenizer.O200kBase)
	})
	if miniMaxRoutingTokenizerErr != nil || miniMaxRoutingTokenizer == nil {
		return roughTokenEstimate(joined)
	}
	count, err := miniMaxRoutingTokenizer.Count(joined)
	if err != nil {
		return roughTokenEstimate(joined)
	}
	return int64(count)
}

func collectMiniMaxRoutingStrings(node gjson.Result, segments *[]string) {
	switch node.Type {
	case gjson.String:
		if text := strings.TrimSpace(node.String()); text != "" {
			*segments = append(*segments, text)
		}
	case gjson.JSON:
		node.ForEach(func(_, child gjson.Result) bool {
			collectMiniMaxRoutingStrings(child, segments)
			return true
		})
	}
}

func requestedOutputTokenBudget(payload []byte) int64 {
	for _, path := range []string{
		"max_tokens",
		"max_completion_tokens",
		"generation_config.max_output_tokens",
		"generationConfig.maxOutputTokens",
		"maxOutputTokens",
	} {
		value := gjson.GetBytes(payload, path)
		if value.Exists() && value.Int() > 0 {
			return value.Int()
		}
	}
	return 0
}

func roughTokenEstimate(text string) int64 {
	runes := int64(len([]rune(text)))
	if runes == 0 {
		return 0
	}
	return (runes + 2) / 3
}

func isMiniMaxModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	return strings.HasPrefix(model, "minimax-")
}

func isMiniMaxM3SeriesModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	return model == "minimax-m3" || strings.HasPrefix(model, "minimax-m3-")
}

func isMiniMaxM3HighspeedModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	return isMiniMaxM3SeriesModel(model) && strings.Contains(model, "highspeed")
}
