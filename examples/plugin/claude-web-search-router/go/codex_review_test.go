package main

import (
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
