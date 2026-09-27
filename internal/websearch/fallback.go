package websearch

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	log "github.com/sirupsen/logrus"
)

// Entry formats supported by the proxy-side search fallback. Values mirror
// sdk/translator.Format identifiers.
const (
	FallbackFormatClaude         = "claude"
	FallbackFormatOpenAI         = "openai"
	FallbackFormatOpenAIResponse = "openai-response"
	FallbackFormatCodex          = "codex"
	FallbackFormatGemini         = "gemini"
)

// Executor runs one non-streaming upstream attempt for the fallback loop.
type Executor func(ctx context.Context, payload []byte) (body []byte, err error)

// ProxyCall is one model-issued search request the proxy must execute.
type ProxyCall struct {
	ID    string
	Name  string
	Query string
}

// ShouldFallback reports whether a request should run proxy-side web search:
// enabled, declares a server search tool, routes to upstreams without native
// support, and has at least one available search provider.
func ShouldFallback(cfg Config, format string, providers []string, model string, payload []byte, stream bool) bool {
	if !cfg.Enabled {
		return false
	}
	format = normalizeFallbackFormat(format)
	if !supportedFallbackFormat(format) {
		return false
	}
	if stream && !synthesisSupported(format) {
		return false
	}
	if !HasServerSearchTool(format, payload) {
		return false
	}
	if NativeSearchSupported(format, providers, model, payload) {
		return false
	}
	if len(resolveCandidates(cfg.WithDefaults(), "")) == 0 {
		log.Debug("websearch: fallback skipped, no provider available")
		return false
	}
	return true
}

// NativeSearchSupported reports whether any candidate provider can execute
// the declared server search tool natively.
func NativeSearchSupported(format string, providers []string, model string, payload []byte) bool {
	format = normalizeFallbackFormat(format)
	for _, provider := range providers {
		switch normalizeProvider(provider) {
		case "claude", "codex", "xai":
			// Claude is native; Codex and xAI keep Responses
			// web_search ungated.
			return true
		case "antigravity":
			if antigravityNativeSearch(format, model, payload) {
				return true
			}
		case "gemini":
			if geminiNativeSearch(format, model, payload) {
				return true
			}
		}
	}
	return false
}

// antigravityNativeSearch mirrors the translator gate: a capable route model
// plus search-only tools plus an allowing tool choice. Mixed or incapable
// requests drop the typed tool, so they need the proxy fallback.
func antigravityNativeSearch(format, model string, payload []byte) bool {
	if registry.AntigravityWebSearchModelFor(model) == "" {
		return false
	}
	return searchOnlyTools(format, payload) && choiceAllowsSearch(format, payload)
}

// geminiNativeSearch reports whether the Gemini executor can run the search
// natively. It mirrors translator/common.GeminiModelSupportsWebSearch:
// static catalog veto first, then dynamic probe flags. (The translator
// package cannot be imported here; it would close an import cycle via
// internal/util, so the check is replicated.)
func geminiNativeSearch(format, model string, payload []byte) bool {
	switch format {
	case FallbackFormatOpenAIResponse, FallbackFormatCodex, FallbackFormatGemini, FallbackFormatClaude:
	default:
		return false
	}
	if !geminiModelSupportsWebSearch(model) {
		return false
	}
	return choiceAllowsSearch(format, payload)
}

func geminiModelSupportsWebSearch(modelID string) bool {
	info := registry.LookupModelInfo(modelID)
	infoAG := registry.LookupModelInfo(modelID, "antigravity")
	if info != nil && info.NativeCapabilities != nil && info.NativeCapabilities.WebSearch != nil && !*info.NativeCapabilities.WebSearch {
		return false
	}
	if infoAG != nil && infoAG.NativeCapabilities != nil && infoAG.NativeCapabilities.WebSearch != nil && !*infoAG.NativeCapabilities.WebSearch {
		return false
	}
	if info != nil && info.NativeCapabilities != nil && info.NativeCapabilities.WebSearch != nil && *info.NativeCapabilities.WebSearch {
		return true
	}
	if infoAG != nil && infoAG.NativeCapabilities != nil && infoAG.NativeCapabilities.WebSearch != nil && *infoAG.NativeCapabilities.WebSearch {
		return true
	}
	if registry.AntigravityWebSearchModelFor(modelID) != "" {
		return true
	}
	return (info != nil && info.SupportsWebSearch) || (infoAG != nil && infoAG.SupportsWebSearch)
}

// Run executes the proxy-side search loop: server search tools are rewritten
// to function calls, each model-issued search runs through the provider
// chain, results are appended to the request, and the loop repeats until the
// model answers without searching or the search budget is exhausted.
func Run(ctx context.Context, cfg Config, format string, payload []byte, exec Executor) ([]byte, error) {
	cfg = cfg.WithDefaults()
	format = normalizeFallbackFormat(format)
	rewritten, names := RewriteForFallback(format, payload)
	if len(names) == 0 {
		return exec(ctx, payload)
	}
	nameSet := make(map[string]bool, len(names))
	for _, name := range names {
		nameSet[name] = true
	}
	current := rewritten
	searches := 0
	var totals usageTotals
	for iteration := 0; ; iteration++ {
		body, errExec := exec(ctx, current)
		if errExec != nil {
			return nil, errExec
		}
		totals.add(parseUsage(format, body))
		calls := ExtractProxyCalls(format, body, nameSet)
		if len(calls) == 0 {
			return withUsage(format, body, totals), nil
		}
		if searches >= cfg.MaxSearches || iteration > cfg.MaxSearches+1 {
			final, errFinal := exec(ctx, StripProxyTools(format, current, nameSet))
			if errFinal != nil {
				return nil, errFinal
			}
			totals.add(parseUsage(format, final))
			return withUsage(format, final, totals), nil
		}
		outputs := make([]string, 0, len(calls))
		for _, call := range calls {
			if searches >= cfg.MaxSearches {
				outputs = append(outputs, "Error: web search budget exhausted.")
				continue
			}
			searches++
			outputs = append(outputs, runSingleSearch(ctx, cfg, call.Query))
		}
		current = AppendSearchResults(format, current, body, calls, outputs)
	}
}

func runSingleSearch(ctx context.Context, cfg Config, query string) string {
	if strings.TrimSpace(query) == "" {
		return "Error: empty search query; provide a query string."
	}
	response, errSearch := Execute(ctx, cfg, SearchRequest{Query: query})
	if errSearch != nil {
		return "Error: " + errSearch.Error()
	}
	if text := FormatForLLM(response); strings.TrimSpace(text) != "" {
		return text
	}
	return "Error: no search results."
}

func normalizeFallbackFormat(format string) string {
	return strings.ToLower(strings.TrimSpace(format))
}

func supportedFallbackFormat(format string) bool {
	switch normalizeFallbackFormat(format) {
	case FallbackFormatClaude, FallbackFormatOpenAI, FallbackFormatOpenAIResponse, FallbackFormatCodex, FallbackFormatGemini:
		return true
	default:
		return false
	}
}

func normalizeProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	// OpenAI-compat executors use namespaced keys; none has native search.
	if strings.HasPrefix(provider, "openai-compatible-") || strings.HasPrefix(provider, "openai-compatibility") {
		return "openai-compat"
	}
	return provider
}
