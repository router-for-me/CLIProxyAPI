package main

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestResolveKimiSearchPlanExplicitLevels(t *testing.T) {
	for _, level := range []string{"basic", "pro", "model"} {
		cfg := pluginConfig{KimiSearchLevel: level}
		plan := resolveKimiSearchPlan(cfg, claudeWebSearchToolConfig{}, "any query here")
		if plan.level != kimiSearchLevel(level) {
			t.Fatalf("level %q → plan.level = %q", level, plan.level)
		}
	}
}

func TestResolveKimiSearchPlanAutoBasic(t *testing.T) {
	cfg := pluginConfig{KimiSearchLevel: "auto", KimiProEscalation: true}
	plan := resolveKimiSearchPlan(cfg, claudeWebSearchToolConfig{}, "what is the capital of France")
	if plan.level != kimiLevelBasic {
		t.Fatalf("plan.level = %q, want basic", plan.level)
	}
	if !plan.escalate {
		t.Fatal("escalate = false, want true for auto basic")
	}
}

func TestResolveKimiSearchPlanAutoProWithAllowedDomains(t *testing.T) {
	cfg := pluginConfig{KimiSearchLevel: "auto"}
	toolCfg := claudeWebSearchToolConfig{AllowedDomains: []string{"nature.com", "science.org"}}
	plan := resolveKimiSearchPlan(cfg, toolCfg, "2025 Nobel Prize in Physics awarded work")
	if plan.level != kimiLevelPro {
		t.Fatalf("plan.level = %q, want pro (sites mapping)", plan.level)
	}
	if len(plan.sites) != 2 || plan.sites[0] != "nature.com" {
		t.Fatalf("sites = %v", plan.sites)
	}
}

func TestResolveKimiSearchPlanAutoProWithConfiguredSites(t *testing.T) {
	cfg := pluginConfig{KimiSearchLevel: "auto", KimiSites: []string{"docs.example"}}
	plan := resolveKimiSearchPlan(cfg, claudeWebSearchToolConfig{}, "simple question")
	if plan.level != kimiLevelPro {
		t.Fatalf("plan.level = %q, want pro", plan.level)
	}
}

func TestResolveKimiSearchPlanAutoProDeepQuery(t *testing.T) {
	cfg := pluginConfig{KimiSearchLevel: "auto"}
	plan := resolveKimiSearchPlan(cfg, claudeWebSearchToolConfig{}, "compare the latest benchmarks of the top coding models")
	if plan.level != kimiLevelPro {
		t.Fatalf("plan.level = %q, want pro for deep query", plan.level)
	}
}

func TestResolveKimiSearchPlanAutoFetch(t *testing.T) {
	cfg := pluginConfig{KimiSearchLevel: "auto"}
	plan := resolveKimiSearchPlan(cfg, claudeWebSearchToolConfig{}, "https://example.com/spec.pdf")
	if plan.level != kimiLevelFetch || plan.fetchURL != "https://example.com/spec.pdf" {
		t.Fatalf("plan = %+v, want fetch", plan)
	}
}

func TestResolveKimiSearchPlanExplicitFetchWithoutURLFallsBack(t *testing.T) {
	cfg := pluginConfig{KimiSearchLevel: "fetch"}
	plan := resolveKimiSearchPlan(cfg, claudeWebSearchToolConfig{}, "no link in this query")
	if plan.level != kimiLevelBasic {
		t.Fatalf("plan.level = %q, want basic fallback", plan.level)
	}
}

func TestResolveKimiSearchPlanLimitClamps(t *testing.T) {
	cfg := pluginConfig{KimiSearchLevel: "basic"}
	plan := resolveKimiSearchPlan(cfg, claudeWebSearchToolConfig{MaxUses: 99}, "q")
	if plan.limit != 20 {
		t.Fatalf("limit = %d, want 20 cap", plan.limit)
	}
	cfg.KimiMaxResults = 7
	plan = resolveKimiSearchPlan(cfg, claudeWebSearchToolConfig{MaxUses: 3}, "q")
	if plan.limit != 7 {
		t.Fatalf("limit = %d, want explicit kimi_max_results", plan.limit)
	}
	cfg.KimiMaxResults = 0
	plan = resolveKimiSearchPlan(cfg, claudeWebSearchToolConfig{}, "q")
	if plan.limit != 5 {
		t.Fatalf("limit = %d, want default 5", plan.limit)
	}
}

func TestResolveKimiSearchPlanBlockedDomains(t *testing.T) {
	cfg := pluginConfig{KimiSearchLevel: "basic"}
	toolCfg := claudeWebSearchToolConfig{BlockedDomains: []string{"spam.example"}}
	plan := resolveKimiSearchPlan(cfg, toolCfg, "q")
	if len(plan.blocked) != 1 || plan.blocked[0] != "spam.example" {
		t.Fatalf("blocked = %v", plan.blocked)
	}
}

func TestExtractClaudeWebSearchToolConfig(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"type": "web_search_20250305", "name": "web_search", "max_uses": 8,
			 "allowed_domains": ["a.example", "b.example"], "blocked_domains": ["spam.example"]},
			{"type": "custom_tool", "name": "other"}
		]
	}`)
	cfg := extractClaudeWebSearchToolConfig(body)
	if cfg.MaxUses != 8 {
		t.Fatalf("MaxUses = %d", cfg.MaxUses)
	}
	if len(cfg.AllowedDomains) != 2 || cfg.AllowedDomains[1] != "b.example" {
		t.Fatalf("allowed = %v", cfg.AllowedDomains)
	}
	if len(cfg.BlockedDomains) != 1 {
		t.Fatalf("blocked = %v", cfg.BlockedDomains)
	}
}

func TestExtractClaudeWebSearchToolConfigNoTools(t *testing.T) {
	cfg := extractClaudeWebSearchToolConfig([]byte(`{"tools": [{"type": "other"}]}`))
	if cfg.MaxUses != 0 || len(cfg.AllowedDomains) != 0 || len(cfg.BlockedDomains) != 0 {
		t.Fatalf("cfg = %+v, want zero", cfg)
	}
}

func TestDomainMatches(t *testing.T) {
	if !domainMatches("a.b.example", []string{"b.example"}) {
		t.Fatal("subdomain should match parent")
	}
	if !domainMatches("example.com", []string{"example.com"}) {
		t.Fatal("exact match expected")
	}
	if domainMatches("notexample.com", []string{"example.com"}) {
		t.Fatal("suffix without dot boundary must not match")
	}
	if domainMatches("example.com", []string{"other.com"}) {
		t.Fatal("unrelated domain must not match")
	}
}

func TestFilterBlockedDomains(t *testing.T) {
	hits := []claudeWebSearchHit{
		{URL: "https://good.example/a"},
		{URL: "https://spam.example/x"},
		{URL: "https://sub.spam.example/y"},
	}
	out := filterBlockedDomains(hits, []string{"spam.example"})
	if len(out) != 1 || out[0].URL != "https://good.example/a" {
		t.Fatalf("out = %#v", out)
	}
	if got := filterBlockedDomains(hits, nil); len(got) != 3 {
		t.Fatalf("nil blocked should pass through, got %v", got)
	}
}

func TestExtractURLFromQuery(t *testing.T) {
	if got := extractURLFromQuery(`read https://example.com/a, and summarize`); got != "https://example.com/a" {
		t.Fatalf("got %q", got)
	}
	if got := extractURLFromQuery(`no urls here`); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	if got := extractURLFromQuery(`(see https://x.example/p)`); got != "https://x.example/p" {
		t.Fatalf("got %q, want punctuation stripped", got)
	}
}

func TestFetchIntentURL(t *testing.T) {
	if got := fetchIntentURL("https://example.com"); got != "https://example.com" {
		t.Fatalf("bare URL got %q", got)
	}
	if got := fetchIntentURL("summarize https://example.com/article"); got != "https://example.com/article" {
		t.Fatalf("intent+URL got %q", got)
	}
	if got := fetchIntentURL("what replaced https://example.com in 2026"); got != "" {
		t.Fatalf("URL without fetch intent got %q, want empty", got)
	}
	if got := fetchIntentURL("plain query"); got != "" {
		t.Fatalf("no URL got %q", got)
	}
}

func TestIsDeepQuery(t *testing.T) {
	if isDeepQuery("weather today") {
		t.Fatal("short simple query marked deep")
	}
	if !isDeepQuery("compare the latest coding model benchmarks") {
		t.Fatal("compare/latest not detected")
	}
	long := ""
	for len(long) < 150 {
		long += "filler "
	}
	if !isDeepQuery(long) {
		t.Fatal("long query not detected")
	}
}

func TestNormalizeKimiSites(t *testing.T) {
	got := normalizeKimiSites([]string{" A.example ", "", "b.example"})
	if len(got) != 2 || got[0] != "a.example" || got[1] != "b.example" {
		t.Fatalf("got %v", got)
	}
	many := []string{"1", "2", "3", "4", "5", "6", "7"}
	if got := normalizeKimiSites(many); len(got) != 5 {
		t.Fatalf("got %v, want capped at 5", got)
	}
}

func TestBuildExecutionPlansForExecuteRouteKimi(t *testing.T) {
	currentConfig.Store(pluginConfig{
		Enabled:           true,
		Route:             string(backendKimiWebSearch),
		KimiAPIKeys:       []string{"sk-test"},
		KimiSearchLevel:   string(kimiLevelAuto),
		KimiProEscalation: true,
	})
	cfg := loadedConfig()
	plans := buildExecutionPlansForExecute(cfg, pluginapi.ModelRouteRequest{
		SourceFormat:   "claude",
		RequestedModel: "claude-sonnet-4-6",
	})
	if len(plans) != 1 || plans[0].backend != backendKimiWebSearch {
		t.Fatalf("plans = %#v, want single kimi plan", plans)
	}
}

func TestBuildExecutionPlansFallbackIncludesKimi(t *testing.T) {
	currentConfig.Store(pluginConfig{
		Enabled:         true,
		Route:           string(backendFallback),
		KimiAPIKeys:     []string{"sk-test"},
		KimiSearchLevel: string(kimiLevelAuto),
	})
	cfg := loadedConfig()
	plans := buildExecutionPlansForExecute(cfg, pluginapi.ModelRouteRequest{
		SourceFormat:   "claude",
		RequestedModel: "claude-sonnet-4-6",
	})
	var found bool
	for _, p := range plans {
		if p.backend == backendKimiWebSearch {
			found = true
		}
	}
	if !found {
		t.Fatalf("fallback plans missing kimi: %#v", plans)
	}
}

func TestRouteModelKimiUnavailable(t *testing.T) {
	currentConfig.Store(pluginConfig{
		Enabled:         true,
		Route:           string(backendKimiWebSearch),
		KimiSearchLevel: string(kimiLevelAuto),
	})
	raw, err := routeModel(mustJSON(t, rpcModelRouteRequest{
		ModelRouteRequest: pluginapi.ModelRouteRequest{
			SourceFormat:       "claude",
			Body:               claudeWebSearchRouteBody(t),
			AvailableProviders: []string{},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeModelRouteResponse(t, raw)
	if resp.Handled {
		t.Fatalf("handled = true without kimi_api_keys: %#v", resp)
	}
}

func TestRouteModelKimiHandled(t *testing.T) {
	currentConfig.Store(pluginConfig{
		Enabled:         true,
		Route:           string(backendKimiWebSearch),
		KimiAPIKeys:     []string{"sk-test"},
		KimiSearchLevel: string(kimiLevelAuto),
	})
	raw, err := routeModel(mustJSON(t, rpcModelRouteRequest{
		ModelRouteRequest: pluginapi.ModelRouteRequest{
			SourceFormat:       "claude",
			Body:               claudeWebSearchRouteBody(t),
			AvailableProviders: []string{},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeModelRouteResponse(t, raw)
	if !resp.Handled || resp.TargetKind != pluginapi.ModelRouteTargetSelf {
		t.Fatalf("resp = %#v", resp)
	}
	if resp.Reason != "claude_code_web_search_orchestrated" {
		t.Fatalf("reason = %q", resp.Reason)
	}
}
