package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// TestTryRouteBackendKimiResponsesKimiAIProvider verifies that the kimi_responses
// route is offered when the host only has a kimi.ai credential, which
// AvailableProviders exposes under the canonical "kimi-ai" key.
func TestTryRouteBackendKimiResponsesKimiAIProvider(t *testing.T) {
	cfg := pluginConfig{Route: string(backendKimiResponses)}
	req := pluginapi.ModelRouteRequest{
		AvailableProviders: []string{"kimi-ai"},
	}
	resp, ok := tryRouteBackend(backendKimiResponses, cfg, req)
	if !ok {
		t.Fatalf("kimi_responses route declined with only kimi-ai provider available (reason %q)", resp.Reason)
	}
	if !resp.Handled {
		t.Fatalf("kimi_responses route response not handled with only kimi-ai provider available")
	}
}

// TestTryRouteBackendKimiResponsesKimiDotAIProvider verifies the dotted alias too.
func TestTryRouteBackendKimiResponsesKimiDotAIProvider(t *testing.T) {
	cfg := pluginConfig{Route: string(backendKimiResponses)}
	req := pluginapi.ModelRouteRequest{
		AvailableProviders: []string{"kimi.ai"},
	}
	if _, ok := tryRouteBackend(backendKimiResponses, cfg, req); !ok {
		t.Fatalf("kimi_responses route declined with kimi.ai provider available")
	}
}

// TestResolveKimiSearchPlanAutoFetchRespectsAllowedDomains verifies that the
// auto level does not pick the unfiltered fetch path when the request carries
// an allowed_domains restriction and the URL is outside those domains.
func TestResolveKimiSearchPlanAutoFetchRespectsAllowedDomains(t *testing.T) {
	cfg := pluginConfig{KimiSearchLevel: "auto"}
	toolCfg := claudeWebSearchToolConfig{AllowedDomains: []string{"example.com"}}

	plan := resolveKimiSearchPlan(cfg, toolCfg, "please fetch https://other.com/page")
	if plan.level == kimiLevelFetch {
		t.Fatalf("auto level chose fetch for URL outside allowed_domains; restriction would be bypassed")
	}
	if plan.level != kimiLevelPro {
		t.Fatalf("auto level with allowed_domains and outside URL = %q, want pro", plan.level)
	}
	if len(plan.sites) != 1 || plan.sites[0] != "example.com" {
		t.Fatalf("auto level with allowed_domains kept sites %v, want [example.com]", plan.sites)
	}

	// A URL inside the allowed domains still takes the fetch path.
	plan = resolveKimiSearchPlan(cfg, toolCfg, "summarize https://docs.example.com/spec")
	if plan.level != kimiLevelFetch {
		t.Fatalf("auto level for URL inside allowed_domains = %q, want fetch", plan.level)
	}
	if !strings.Contains(plan.fetchURL, "docs.example.com") {
		t.Fatalf("auto level fetch URL = %q, want docs.example.com", plan.fetchURL)
	}
}

// TestResolveKimiSearchPlanAutoFetchNoRestrictionUnchanged verifies that without
// a site restriction auto still picks fetch for bare URLs.
func TestResolveKimiSearchPlanAutoFetchNoRestrictionUnchanged(t *testing.T) {
	cfg := pluginConfig{KimiSearchLevel: "auto"}
	plan := resolveKimiSearchPlan(cfg, claudeWebSearchToolConfig{}, "https://example.com/a")
	if plan.level != kimiLevelFetch {
		t.Fatalf("auto level for bare URL without restrictions = %q, want fetch", plan.level)
	}
}

// TestResolveKimiResponsesTargetForcesEntryProvider verifies the pinned target
// carries the host-canonical provider of the selected credential, so the forced
// provider bucket in host auth selection actually contains the pinned auth ID.
func TestResolveKimiResponsesTargetForcesEntryProvider(t *testing.T) {
	entry := func(provider, baseURL, id string) pluginapi.HostAuthFileEntry {
		return pluginapi.HostAuthFileEntry{Provider: provider, BaseURL: baseURL, ID: id}
	}
	cases := []struct {
		name     string
		entries  []pluginapi.HostAuthFileEntry
		wantProv string
	}{
		{name: "kimi.com credential", entries: []pluginapi.HostAuthFileEntry{
			entry("kimi", "https://api.kimi.com/coding", "c1")}, wantProv: "kimi"},
		{name: "kimi.ai credential", entries: []pluginapi.HostAuthFileEntry{
			entry("kimi.ai", "https://api.kimi.ai/coding", "c2")}, wantProv: "kimi-ai"},
		{name: "canonical kimi-ai credential", entries: []pluginapi.HostAuthFileEntry{
			entry("kimi-ai", "https://api.moonshot.ai", "c3")}, wantProv: "kimi-ai"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, ok := resolveKimiResponsesTargetFromEntries(tc.entries)
			if !ok {
				t.Fatal("expected target")
			}
			if target.provider != tc.wantProv {
				t.Fatalf("target.provider = %q, want %q", target.provider, tc.wantProv)
			}
		})
	}
}

// TestSharedKimiClientPersistsAcrossCalls verifies the shared client is reused
// for identical config (so the round-robin key counter advances across
// requests) and rebuilt when the config changes.
func TestSharedKimiClientPersistsAcrossCalls(t *testing.T) {
	sharedKimiClientMu.Lock()
	sharedKimiClientPtr, sharedKimiClientKeys, sharedKimiClientBase = nil, nil, ""
	sharedKimiClientMu.Unlock()

	first := sharedKimiClient([]string{"k1", "k2"}, "")
	second := sharedKimiClient([]string{"k1", "k2"}, "")
	if first != second {
		t.Fatal("sharedKimiClient returned a new client for identical config")
	}
	if got := first.nextKey(); got != "k1" {
		t.Fatalf("first key = %q, want k1", got)
	}
	if got := second.nextKey(); got != "k2" {
		t.Fatalf("second key across shared client = %q, want k2 (round-robin must advance)", got)
	}

	third := sharedKimiClient([]string{"k3"}, "")
	if third == first {
		t.Fatal("sharedKimiClient kept the old client after key config changed")
	}
}

// TestResolveKimiSearchPlanExplicitFetchRespectsAllowedDomains verifies the
// explicit fetch level honors allowed_domains too: an outside URL must not be
// fetched unfiltered, while a matching URL keeps the fetch path.
func TestResolveKimiSearchPlanExplicitFetchRespectsAllowedDomains(t *testing.T) {
	cfg := pluginConfig{KimiSearchLevel: "fetch"}
	toolCfg := claudeWebSearchToolConfig{AllowedDomains: []string{"example.com"}}

	plan := resolveKimiSearchPlan(cfg, toolCfg, "https://other.com/page")
	if plan.level != kimiLevelPro {
		t.Fatalf("explicit fetch with allowed_domains and outside URL = %q, want pro", plan.level)
	}
	if len(plan.sites) != 1 || plan.sites[0] != "example.com" {
		t.Fatalf("explicit fetch fallback sites = %v, want [example.com]", plan.sites)
	}

	plan = resolveKimiSearchPlan(cfg, toolCfg, "https://docs.example.com/spec")
	if plan.level != kimiLevelFetch {
		t.Fatalf("explicit fetch with URL inside allowed_domains = %q, want fetch", plan.level)
	}
}

// TestResolveKimiSearchPlanAutoTimeWindowPromotesToPro verifies that auto
// level routes time-windowed searches through Pro, because Basic drops the
// time window and escalation only applies to empty Basic results.
func TestResolveKimiSearchPlanAutoTimeWindowPromotesToPro(t *testing.T) {
	cfg := pluginConfig{
		KimiSearchLevel: "auto",
		KimiTimeWindow:  &kimiTimeWindow{Start: "2026-01", End: "2026-06"},
	}
	plan := resolveKimiSearchPlan(cfg, claudeWebSearchToolConfig{}, "average rainfall Norway")
	if plan.level != kimiLevelPro {
		t.Fatalf("auto level with kimi_time_window = %q, want pro", plan.level)
	}
}

// TestResolveKimiResponsesTargetListFailureDegradesUnpinned verifies that when
// host.auth.list is unavailable (no host in tests), resolution degrades to the
// unpinned coding default instead of declining the backend entirely.
func TestResolveKimiResponsesTargetListFailureDegradesUnpinned(t *testing.T) {
	target, ok := resolveKimiResponsesTarget(pluginConfig{})
	if !ok {
		t.Fatal("expected degraded unpinned target when host.auth.list fails")
	}
	if target.model != kimiResponsesCodingModel || target.authID != "" || target.provider != "kimi" {
		t.Fatalf("degraded target = %+v, want unpinned coding default", target)
	}
}

// TestResolveKimiSearchPlanBasicAllowedDomainsPromotedToPro verifies that an
// explicit basic level cannot silently drop a request-level allowed_domains
// restriction (Basic clears sites before the upstream call).
func TestResolveKimiSearchPlanBasicAllowedDomainsPromotedToPro(t *testing.T) {
	cfg := pluginConfig{KimiSearchLevel: "basic"}
	toolCfg := claudeWebSearchToolConfig{AllowedDomains: []string{"example.com"}}
	plan := resolveKimiSearchPlan(cfg, toolCfg, "any query here")
	if plan.level != kimiLevelPro {
		t.Fatalf("explicit basic with allowed_domains = %q, want pro", plan.level)
	}
}

// TestResolveKimiSearchPlanFetchFallbackAllowedDomainsPromotedToPro verifies
// the no-URL fallback of explicit fetch level also honors allowed_domains.
func TestResolveKimiSearchPlanFetchFallbackAllowedDomainsPromotedToPro(t *testing.T) {
	cfg := pluginConfig{KimiSearchLevel: "fetch"}
	toolCfg := claudeWebSearchToolConfig{AllowedDomains: []string{"example.com"}}
	plan := resolveKimiSearchPlan(cfg, toolCfg, "query without any url")
	if plan.level != kimiLevelPro {
		t.Fatalf("fetch fallback without URL and with allowed_domains = %q, want pro", plan.level)
	}
}

// TestHostFromURLNormalizesHost verifies host extraction strips ports and
// userinfo so blocked-domain matching cannot be evaded.
func TestHostFromURLNormalizesHost(t *testing.T) {
	cases := map[string]string{
		"https://blocked.example:443/a":     "blocked.example",
		"https://user@blocked.example/x":    "blocked.example",
		"http://blocked.example":            "blocked.example",
		"https://sub.blocked.example/y?z=1": "sub.blocked.example",
	}
	for raw, want := range cases {
		if got := hostFromURL(raw); got != want {
			t.Fatalf("hostFromURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestResolveKimiResponsesTargetConfiguredPlatformModelPrefersPlatformCred
// verifies that a configured platform model pins a platform credential instead
// of a coding one (a platform model on a coding endpoint fails upstream).
func TestResolveKimiResponsesTargetConfiguredPlatformModelPrefersPlatformCred(t *testing.T) {
	entry := func(provider, baseURL, id string) pluginapi.HostAuthFileEntry {
		return pluginapi.HostAuthFileEntry{Provider: provider, BaseURL: baseURL, ID: id}
	}
	entries := []pluginapi.HostAuthFileEntry{
		entry("kimi", "https://api.kimi.com/coding", "cred-coding"),
		entry("kimi", "https://api.moonshot.ai", "cred-platform"),
	}
	target, ok := resolveKimiResponsesTargetForModel(entries, kimiResponsesPlatformModel)
	if !ok {
		t.Fatal("expected platform target for platform model override")
	}
	if target.authID != "cred-platform" {
		t.Fatalf("target.authID = %q, want cred-platform", target.authID)
	}
	if target.model != kimiResponsesPlatformModel {
		t.Fatalf("target.model = %q", target.model)
	}
}

// TestResolveKimiResponsesTargetRotatesCredentials verifies that several
// compatible credentials are pinned in turn rather than always the first.
func TestResolveKimiResponsesTargetRotatesCredentials(t *testing.T) {
	entry := func(id string) pluginapi.HostAuthFileEntry {
		return pluginapi.HostAuthFileEntry{Provider: "kimi", BaseURL: "https://api.kimi.com/coding", ID: id}
	}
	entries := []pluginapi.HostAuthFileEntry{entry("cred-a"), entry("cred-b")}
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		target, ok := resolveKimiResponsesTargetFromEntries(entries)
		if !ok {
			t.Fatal("expected target")
		}
		seen[target.authID] = true
	}
	if !seen["cred-a"] || !seen["cred-b"] {
		t.Fatalf("rotation saw %v, want both cred-a and cred-b", seen)
	}
}

// TestExecutePlanModelDropsAnswerFromBlockedSources verifies that when
// blocked_domains remove a model-level source, the model-written answer is
// discarded instead of being kept without its citation.
func TestExecutePlanModelDropsAnswerFromBlockedSources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != kimiResponsesPath {
			t.Errorf("path = %s, want %s", r.URL.Path, kimiResponsesPath)
		}
		_, _ = w.Write([]byte(`{
			"output": [
				{"type": "web_search_call", "action": {"sources": [
					{"type": "url", "url": "https://blocked.example/x", "title": "blocked"},
					{"type": "url", "url": "https://allowed.example/y", "title": "allowed"}
				]}},
				{"type": "message", "content": [{"type": "output_text", "text": "answer text"}]}
			]
		}`))
	}))
	defer server.Close()

	client := newKimiClientWithOptions([]string{"sk-test"}, server.URL, server.Client())
	plan := kimiSearchPlan{level: kimiLevelModel, blocked: []string{"blocked.example"}}
	hits, answer, err := client.executePlan(context.Background(), plan, pluginConfig{}, "query")
	if err != nil {
		t.Fatalf("executePlan error = %v", err)
	}
	if answer != "" {
		t.Fatalf("answer = %q, want empty after blocked-source removal", answer)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].URL, "allowed.example") {
		t.Fatalf("hits = %+v, want only the allowed source", hits)
	}

	// Without removals the answer is preserved.
	plan = kimiSearchPlan{level: kimiLevelModel, blocked: []string{"other.example"}}
	_, answer, err = client.executePlan(context.Background(), plan, pluginConfig{}, "query")
	if err != nil {
		t.Fatalf("executePlan error = %v", err)
	}
	if answer != "answer text" {
		t.Fatalf("answer = %q, want preserved when no source is blocked", answer)
	}
}
