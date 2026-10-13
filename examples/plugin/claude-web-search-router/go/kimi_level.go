package main

import (
	"strings"

	"github.com/tidwall/gjson"
)

type kimiSearchLevel string

const (
	kimiLevelAuto  kimiSearchLevel = "auto"
	kimiLevelBasic kimiSearchLevel = "basic"
	kimiLevelPro   kimiSearchLevel = "pro"
	kimiLevelFetch kimiSearchLevel = "fetch"
	kimiLevelModel kimiSearchLevel = "model"
)

// kimiSearchPlan is the resolved per-request search plan for the Kimi backend.
type kimiSearchPlan struct {
	level    kimiSearchLevel
	limit    int
	sites    []string
	blocked  []string
	fetchURL string
	// escalate retries an empty Basic result set through Pro (empty calls are free).
	escalate bool
}

// claudeWebSearchToolConfig is the inbound Claude web_search tool declaration.
type claudeWebSearchToolConfig struct {
	MaxUses        int
	AllowedDomains []string
	BlockedDomains []string
}

// extractClaudeWebSearchToolConfig reads the first typed web_search tool's
// domain filters and max_uses from a Claude Messages request body.
func extractClaudeWebSearchToolConfig(body []byte) claudeWebSearchToolConfig {
	var cfg claudeWebSearchToolConfig
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return cfg
	}
	for _, tool := range tools.Array() {
		if !isClaudeTypedWebSearchToolType(tool.Get("type").String()) {
			continue
		}
		cfg.MaxUses = int(tool.Get("max_uses").Int())
		cfg.AllowedDomains = stringListFromGJSON(tool.Get("allowed_domains"))
		cfg.BlockedDomains = stringListFromGJSON(tool.Get("blocked_domains"))
		break
	}
	return cfg
}

func stringListFromGJSON(raw gjson.Result) []string {
	if !raw.IsArray() {
		return nil
	}
	out := make([]string, 0, len(raw.Array()))
	for _, item := range raw.Array() {
		if s := strings.TrimSpace(item.String()); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// resolveKimiSearchPlan maps a Claude web_search request onto a Kimi search
// level. kimi_search_level=auto picks the cheapest API that can serve the
// request; an explicit level is honored as-is (fetch without a URL degrades
// to basic).
func resolveKimiSearchPlan(cfg pluginConfig, toolCfg claudeWebSearchToolConfig, query string) kimiSearchPlan {
	limit := toolCfg.MaxUses
	if cfg.KimiMaxResults > 0 {
		limit = cfg.KimiMaxResults
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}

	sites := toolCfg.AllowedDomains
	if len(sites) == 0 {
		sites = cfg.KimiSites
	}
	sites = normalizeKimiSites(sites)

	plan := kimiSearchPlan{
		limit:   limit,
		sites:   sites,
		blocked: toolCfg.BlockedDomains,
	}

	level := kimiSearchLevel(strings.ToLower(strings.TrimSpace(cfg.KimiSearchLevel)))
	switch level {
	case kimiLevelBasic, kimiLevelPro, kimiLevelModel:
		plan.level = level
		return plan
	case kimiLevelFetch:
		if u := extractURLFromQuery(query); u != "" {
			plan.level = kimiLevelFetch
			plan.fetchURL = u
			return plan
		}
		plan.level = kimiLevelBasic
		return plan
	}

	// kimi_search_level=auto
	if u := fetchIntentURL(query); u != "" {
		plan.level = kimiLevelFetch
		plan.fetchURL = u
		return plan
	}
	if len(sites) > 0 || isDeepQuery(query) {
		plan.level = kimiLevelPro
		return plan
	}
	plan.level = kimiLevelBasic
	plan.escalate = cfg.KimiProEscalation
	return plan
}

func normalizeKimiSites(sites []string) []string {
	out := make([]string, 0, len(sites))
	for _, s := range sites {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

// fetchIntentURL returns a URL when the query is a bare URL or pairs an
// explicit fetch/read intent verb with a URL.
func fetchIntentURL(query string) string {
	u := extractURLFromQuery(query)
	if u == "" {
		return ""
	}
	if strings.TrimSpace(query) == u {
		return u
	}
	lower := strings.ToLower(query)
	for _, verb := range kimiFetchIntentVerbs {
		if strings.Contains(lower, verb) {
			return u
		}
	}
	return ""
}

var kimiFetchIntentVerbs = []string{
	"fetch", "read this", "read the", "summarize", "summarise",
	"open ", "what does this page", "content of",
}

// extractURLFromQuery finds the first http(s) URL in a query string.
func extractURLFromQuery(query string) string {
	for _, field := range strings.Fields(query) {
		f := strings.Trim(field, `"'<>()[]{},;`)
		if strings.HasPrefix(f, "http://") || strings.HasPrefix(f, "https://") {
			return f
		}
	}
	return ""
}

// kimiDeepQuerySignals marks queries that benefit from Pro's ranked content
// chunks: research-style, comparative, or freshness-sensitive questions.
var kimiDeepQuerySignals = []string{
	"compare", "comparison", "versus", " vs ", " vs. ",
	"review", "analy", "report", "research", "in-depth", "deep dive",
	"benchmark", "pros and cons", "latest", "breaking", "recent",
	"comprehensive", "detailed", "explain in detail", "tutorial",
	"who won", "how much", "price of", "earnings",
}

const kimiDeepQueryMinRunes = 140

func isDeepQuery(query string) bool {
	if len([]rune(query)) >= kimiDeepQueryMinRunes {
		return true
	}
	lower := strings.ToLower(query)
	for _, signal := range kimiDeepQuerySignals {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	return false
}
