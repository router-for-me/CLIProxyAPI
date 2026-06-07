package auth

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

const (
	miniMaxM2SafeTotalTokens        int64 = 180000
	miniMaxM3RequiredMetadataKey          = "__cliproxy_minimax_m3_required"
	miniMaxM3StandardModel                = "MiniMax-M3"
	miniMaxLargeToolHistoryMessages       = 100
	miniMaxLargeToolHistoryTools          = 40
)

func filterMiniMaxM3RequiredExecutionModels(routeModel string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, candidates []string) []string {
	candidates = rewriteMiniMaxM3HighspeedRouteToStandard(routeModel, opts, candidates)
	candidates = filterClaudeSonnetMiniMaxM3Highspeed(routeModel, opts, candidates)
	candidates = filterMiniMaxLargeToolHistoryM3Highspeed(req, opts, candidates)
	if len(candidates) == 0 || !miniMaxCandidateSetCanRouteToM3(routeModel, opts, candidates) {
		return candidates
	}
	if !miniMaxCandidateSetNeedsM3RequestCheck(candidates) {
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

func miniMaxCandidateSetNeedsM3RequestCheck(candidates []string) bool {
	for _, candidate := range candidates {
		if !isMiniMaxModel(candidate) {
			continue
		}
		if !isMiniMaxM3SeriesModel(candidate) || isMiniMaxM3HighspeedModel(candidate) {
			return true
		}
	}
	return false
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
	required := miniMaxPayloadRequiresM3(payload)
	if opts.Metadata != nil {
		opts.Metadata[miniMaxM3RequiredMetadataKey] = required
	}
	return required
}

func miniMaxPayloadRequiresM3(payload []byte) bool {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return false
	}
	root := gjson.ParseBytes(payload)
	return parsedRequestHasMiniMaxM3PartType(root, isMiniMaxM3MultimodalPartType) ||
		miniMaxParsedRequestExceedsM2SafeContext(payload, root)
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
	return parsedRequestHasMiniMaxM3PartType(gjson.ParseBytes(payload), match)
}

func parsedRequestHasMiniMaxM3PartType(root gjson.Result, match func(string) bool) bool {
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
	walk(root)
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
	return miniMaxParsedRequestExceedsM2SafeContext(payload, gjson.ParseBytes(payload))
}

func miniMaxParsedRequestExceedsM2SafeContext(payload []byte, root gjson.Result) bool {
	outputBudget := requestedOutputTokenBudget(payload)
	if outputBudget >= miniMaxM2SafeTotalTokens {
		return true
	}
	remaining := miniMaxM2SafeTotalTokens - outputBudget
	if remaining <= 0 {
		return true
	}
	return miniMaxRoutingStringBytesAtLeast(root, remaining)
}

func miniMaxRoutingStringBytesAtLeast(root gjson.Result, threshold int64) bool {
	if threshold <= 0 {
		return true
	}
	var total int64
	var walk func(gjson.Result) bool
	walk = func(node gjson.Result) bool {
		switch node.Type {
		case gjson.String:
			rawLen := int64(len(node.Raw))
			if rawLen > 2 {
				total += rawLen - 2
				if total >= threshold {
					return false
				}
			}
		case gjson.JSON:
			node.ForEach(func(_, child gjson.Result) bool {
				return walk(child)
			})
		}
		return true
	}
	walk(root)
	return total >= threshold
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
