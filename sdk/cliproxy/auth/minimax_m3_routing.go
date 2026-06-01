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
	miniMaxM2SafeTotalTokens     int64 = 180000
	miniMaxM3RequiredMetadataKey       = "__cliproxy_minimax_m3_required"
)

var (
	miniMaxRoutingTokenizerOnce sync.Once
	miniMaxRoutingTokenizer     tokenizer.Codec
	miniMaxRoutingTokenizerErr  error
)

func filterMiniMaxM3RequiredExecutionModels(routeModel string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, candidates []string) []string {
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
		filtered = append(filtered, candidate)
	}
	if !removed {
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

func requestHasMiniMaxM3MultimodalInput(payload []byte) bool {
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
			if partType := strings.ToLower(strings.TrimSpace(node.Get("type").String())); isMiniMaxM3MultimodalPartType(partType) {
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
