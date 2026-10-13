package main

import (
	"context"
	"fmt"
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

// fakeHostHTTPClient records requests and returns a canned response, standing
// in for the host-injected HostHTTPClient.
type fakeHostHTTPClient struct {
	called bool
	got    pluginapi.HTTPRequest
	resp   pluginapi.HTTPResponse
	err    error
}

func (f *fakeHostHTTPClient) Do(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	f.called = true
	f.got = req
	return f.resp, f.err
}

func (f *fakeHostHTTPClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, fmt.Errorf("not implemented")
}

// TestKimiClientRoutesThroughHostHTTPClient verifies the kimi_web_search client
// uses the host-injected HTTPClient (host transport policy, request-log) when
// one is present instead of its raw http.Client.
func TestKimiClientRoutesThroughHostHTTPClient(t *testing.T) {
	fake := &fakeHostHTTPClient{resp: pluginapi.HTTPResponse{
		StatusCode: 200,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte(`{"search_results": [{"title": "t", "url": "https://example.com"}]}`),
	}}
	client := newKimiClient([]string{"sk-test"}, "")
	// Poison the raw client so any bypass fails loudly.
	client.http = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("raw http client must not be used when host HTTP client is present")
	})}

	ctx := withHostHTTP(context.Background(), fake)
	_, err := client.search(ctx, false, kimiSearchRequest{TextQuery: "q"})
	if err != nil {
		t.Fatalf("search error = %v", err)
	}
	if !fake.called {
		t.Fatal("host HTTP client was not used")
	}
	if fake.got.Method != http.MethodPost {
		t.Fatalf("method = %q, want POST", fake.got.Method)
	}
	if !strings.HasSuffix(fake.got.URL, kimiSearchBasicPath) {
		t.Fatalf("url = %q, want suffix %s", fake.got.URL, kimiSearchBasicPath)
	}
	if got := fake.got.Headers.Get("Authorization"); got != "Bearer sk-test" {
		t.Fatalf("authorization = %q, want Bearer sk-test", got)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestKimiCodingClientRoutesThroughHostHTTPClient verifies the kimi_coding
// client also routes through the host-injected HTTPClient.
func TestKimiCodingClientRoutesThroughHostHTTPClient(t *testing.T) {
	fake := &fakeHostHTTPClient{resp: pluginapi.HTTPResponse{
		StatusCode: 200,
		Body: []byte(`{"output": [
			{"type": "message", "content": [{"type": "output_text", "text": "answer"}]}
		]}`),
	}}
	client := newKimiCodingClientWithOptions([]string{"sk-test"}, "", "", &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("raw http client must not be used when host HTTP client is present")
		}),
	})

	ctx := withHostHTTP(context.Background(), fake)
	_, _, err := client.search(ctx, []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("coding search error = %v", err)
	}
	if !fake.called {
		t.Fatal("host HTTP client was not used for kimi_coding")
	}
}

// TestLenientProbeDoesNotAdvanceCredentialRotation verifies the availability
// probe resolves the kimi_responses target without consuming a rotation tick,
// so the execution that follows still rotates across the pool.
func TestLenientProbeDoesNotAdvanceCredentialRotation(t *testing.T) {
	entry := func(id string) pluginapi.HostAuthFileEntry {
		return pluginapi.HostAuthFileEntry{Provider: "kimi", BaseURL: "https://api.kimi.com/coding", ID: id}
	}
	entries := []pluginapi.HostAuthFileEntry{entry("cred-a"), entry("cred-b")}

	kimiResponsesCredentialCounter.Store(0)
	if _, ok := peekKimiResponsesTargetForModel(entries, ""); !ok {
		t.Fatal("peek expected target")
	}
	if got := kimiResponsesCredentialCounter.Load(); got != 0 {
		t.Fatalf("peek advanced rotation counter to %d, want 0", got)
	}
	if _, ok := resolveKimiResponsesTargetForModel(entries, ""); !ok {
		t.Fatal("resolve expected target")
	}
	if got := kimiResponsesCredentialCounter.Load(); got != 1 {
		t.Fatalf("resolve advanced rotation counter to %d, want 1", got)
	}
}

// TestResolveKimiResponsesTargetCodingAliases verifies every coding alias the
// host normalizer accepts classifies as the coding family, so a host whose
// only kimi credential is a kimi-ai coding credential still gets pinned
// instead of falling back to the ineligible unpinned "kimi" provider.
func TestResolveKimiResponsesTargetCodingAliases(t *testing.T) {
	entry := func(provider, baseURL, id string) pluginapi.HostAuthFileEntry {
		return pluginapi.HostAuthFileEntry{Provider: provider, BaseURL: baseURL, ID: id}
	}
	entries := []pluginapi.HostAuthFileEntry{
		entry("kimi.ai", "https://api.kimi.ai/coding", "cred-coding"),
	}
	aliases := []string{
		"kimi-k2.8", "k2.8", "kimi-k2.8-code", "k2.8-code",
		"kimi-k2.8-preview", "k2.8-preview", "kimi-k2.7-code", "k2.7-code",
		"kimi-for-coding", "for-coding",
		"kimi-k2.7-code-highspeed", "k2.7-code-highspeed",
		"kimi-for-coding-highspeed", "for-coding-highspeed",
	}
	for _, alias := range aliases {
		target, ok := resolveKimiResponsesTargetForModel(entries, alias)
		if !ok {
			t.Fatalf("alias %q: expected coding credential pin", alias)
		}
		if target.authID != "cred-coding" {
			t.Fatalf("alias %q: authID = %q, want cred-coding", alias, target.authID)
		}
		if target.provider != "kimi-ai" {
			t.Fatalf("alias %q: provider = %q, want kimi-ai", alias, target.provider)
		}
		if target.model != alias {
			t.Fatalf("alias %q: model = %q, want the override passed through", alias, target.model)
		}
	}

	// Platform aliases keep pinning platform credentials.
	platformEntries := []pluginapi.HostAuthFileEntry{
		entry("kimi", "https://api.moonshot.ai", "cred-platform"),
	}
	target, ok := resolveKimiResponsesTargetForModel(platformEntries, "kimi-k3")
	if !ok || target.authID != "cred-platform" {
		t.Fatalf("kimi-k3 target = %+v ok=%v, want cred-platform", target, ok)
	}
	target, ok = resolveKimiResponsesTargetForModel(platformEntries, "k3(1024)")
	if !ok || target.authID != "cred-platform" {
		t.Fatalf("k3(1024) target = %+v ok=%v, want cred-platform", target, ok)
	}
}

// TestHostFromURLNormalizesTerminalDot and TestDomainMatchesIgnoresTerminalDots
// verify that absolute DNS names with a terminal dot cannot evade
// blocked-domain matching.
func TestHostFromURLNormalizesTerminalDot(t *testing.T) {
	if got := hostFromURL("https://blocked.example./a"); got != "blocked.example" {
		t.Fatalf("hostFromURL terminal dot = %q, want blocked.example", got)
	}
	if got := hostFromURL("http://blocked.example.:8080/x"); got != "blocked.example" {
		t.Fatalf("hostFromURL terminal dot with port = %q, want blocked.example", got)
	}
}

func TestDomainMatchesIgnoresTerminalDots(t *testing.T) {
	if !domainMatches("blocked.example.", []string{"blocked.example"}) {
		t.Fatal("host terminal dot did not match plain rule")
	}
	if !domainMatches("blocked.example", []string{"blocked.example."}) {
		t.Fatal("plain host did not match terminal-dot rule")
	}
	if domainMatches("blocked.example.", nil) {
		t.Fatal("empty rules must not match")
	}
}

// TestHostHTTPBridgeRequiresCallbackID verifies the host HTTP bridge is only
// constructed with a live callback ID, since ExecutorRequest.HTTPClient is
// tagged json:"-" and never reaches the plugin in production.
func TestHostHTTPBridgeRequiresCallbackID(t *testing.T) {
	if got := newHostHTTPBridge(""); got != nil {
		t.Fatalf("newHostHTTPBridge(\"\") = %v, want nil", got)
	}
	hc := newHostHTTPBridge("cb-1")
	if hc == nil {
		t.Fatal("newHostHTTPBridge(\"cb-1\") = nil, want bridge")
	}
	var _ pluginapi.HostHTTPClient = hc
}

// TestHostHTTPBridgeDoWithoutHostFails verifies the bridge surfaces the host
// callback failure instead of falling back to a direct connection.
func TestHostHTTPBridgeDoWithoutHostFails(t *testing.T) {
	hc := newHostHTTPBridge("cb-missing")
	if _, err := hc.Do(context.Background(), pluginapi.HTTPRequest{
		Method: http.MethodPost,
		URL:    "https://api.moonshot.ai/v1/tools/search",
		Body:   []byte(`{}`),
	}); err == nil {
		t.Fatal("bridge Do without a host succeeded; want host callback error")
	}
}

// TestExecutePlanFetchRechecksAllowedDomainsPostFetch verifies the fetch path
// re-validates the URL the upstream actually returned (redirect or canonical
// host), not just the pre-fetch URL.
func TestExecutePlanFetchRechecksAllowedDomainsPostFetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != kimiFetchPath {
			t.Errorf("path = %s, want %s", r.URL.Path, kimiFetchPath)
		}
		// Upstream reports a canonical URL outside the allowed domains.
		_, _ = w.Write([]byte(`{"url": "https://other.example/page", "title": "t", "markdown": "m"}`))
	}))
	defer server.Close()

	client := newKimiClientWithOptions([]string{"sk-test"}, server.URL, server.Client())
	plan := kimiSearchPlan{
		level:    kimiLevelFetch,
		fetchURL: "https://docs.example.com/spec",
		sites:    []string{"example.com"},
	}
	_, _, err := client.executePlan(context.Background(), plan, pluginConfig{}, "query")
	if err == nil {
		t.Fatal("executePlan fetch accepted an upstream URL outside allowed_domains")
	}
	if !strings.Contains(err.Error(), "allowed domains") {
		t.Fatalf("error = %v, want mention of allowed domains", err)
	}

	// A canonical URL inside the allowed domains still succeeds.
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"url": "https://docs.example.com/canon", "title": "t", "markdown": "m"}`))
	}))
	defer server2.Close()
	client2 := newKimiClientWithOptions([]string{"sk-test"}, server2.URL, server2.Client())
	hits, _, err := client2.executePlan(context.Background(), plan, pluginConfig{}, "query")
	if err != nil {
		t.Fatalf("executePlan fetch with in-scope canonical URL errored: %v", err)
	}
	if len(hits) != 1 || hits[0].URL != "https://docs.example.com/canon" {
		t.Fatalf("hits = %+v, want the canonical in-scope URL", hits)
	}
}

// TestKimiResponsesModelFamilyCombinedSuffix verifies suffix stripping matches
// the host normalizer's order (thinking suffix first, then [1m]).
func TestKimiResponsesModelFamilyCombinedSuffix(t *testing.T) {
	cases := map[string]string{
		"kimi-k2.8-code[1m](high)": "coding",
		"kimi-k2.8-code(high)":     "coding",
		"kimi-k2.8-code[1m]":       "coding",
		"kimi-k3[1m](1024)":        "platform",
	}
	for model, want := range cases {
		got, ok := kimiResponsesModelFamily(model)
		if !ok || got != want {
			t.Fatalf("family(%q) = %q,%v want %q,true", model, got, ok, want)
		}
	}
}

// TestKimiResponsesModelFamilyK3Variants verifies the registered K3 long-context
// variant classifies as platform so it pins a platform credential.
func TestKimiResponsesModelFamilyK3Variants(t *testing.T) {
	for _, model := range []string{"kimi-k3-256k", "k3-256k"} {
		got, ok := kimiResponsesModelFamily(model)
		if !ok || got != "platform" {
			t.Fatalf("family(%q) = %q,%v want platform,true", model, got, ok)
		}
	}
	target, ok := resolveKimiResponsesTargetForModel([]pluginapi.HostAuthFileEntry{
		{Provider: "kimi", BaseURL: "https://api.moonshot.ai", ID: "cred-platform"},
	}, "kimi-k3-256k")
	if !ok || target.authID != "cred-platform" {
		t.Fatalf("kimi-k3-256k target = %+v ok=%v, want cred-platform", target, ok)
	}
}

// TestKimiResponsesModelFamilyK2PlatformVariants verifies the registered K2
// platform models classify as platform so they pin a Moonshot credential
// instead of falling back to an unpinned target the scheduler can pin onto a
// coding endpoint.
func TestKimiResponsesModelFamilyK2PlatformVariants(t *testing.T) {
	for _, model := range []string{
		"kimi-k2", "k2",
		"kimi-k2-thinking", "k2-thinking",
		"kimi-k2.5", "k2.5",
		"kimi-k2.6", "k2.6",
	} {
		got, ok := kimiResponsesModelFamily(model)
		if !ok || got != "platform" {
			t.Fatalf("family(%q) = %q,%v want platform,true", model, got, ok)
		}
	}
	entries := []pluginapi.HostAuthFileEntry{
		{Provider: "kimi", BaseURL: "https://api.kimi.com/coding", ID: "cred-coding"},
		{Provider: "kimi", BaseURL: "https://api.moonshot.ai", ID: "cred-platform"},
	}
	for _, model := range []string{"kimi-k2", "kimi-k2-thinking", "kimi-k2.5", "kimi-k2.6"} {
		target, ok := resolveKimiResponsesTargetForModel(entries, model)
		if !ok || target.authID != "cred-platform" {
			t.Fatalf("%s target = %+v ok=%v, want cred-platform", model, target, ok)
		}
	}
	// The K2 coding aliases still resolve to the coding credential.
	target, ok := resolveKimiResponsesTargetForModel(entries, "kimi-k2.8-code")
	if !ok || target.authID != "cred-coding" {
		t.Fatalf("kimi-k2.8-code target = %+v ok=%v, want cred-coding", target, ok)
	}
}
