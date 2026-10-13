package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestKimiResponsesModelForBaseURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		baseURL string
		want    string
	}{
		{"https://api.moonshot.ai", kimiResponsesPlatformModel},
		{"https://api.moonshot.cn", kimiResponsesPlatformModel},
		{"https://api.kimi.com/coding", kimiResponsesCodingModel},
		{"https://api.kimi.ai/coding", kimiResponsesCodingModel},
		{"", kimiResponsesCodingModel},
	}
	for _, tc := range cases {
		if got := kimiResponsesModelForBaseURL(tc.baseURL); got != tc.want {
			t.Fatalf("kimiResponsesModelForBaseURL(%q) = %q, want %q", tc.baseURL, got, tc.want)
		}
	}
}

func TestResolveKimiResponsesTargetFromEntries(t *testing.T) {
	t.Parallel()
	entry := func(provider, baseURL, id string) pluginapi.HostAuthFileEntry {
		return pluginapi.HostAuthFileEntry{Provider: provider, BaseURL: baseURL, ID: id}
	}
	coding := entry("kimi", "https://api.kimi.com/coding", "cred-coding")
	platform := entry("kimi", "https://api.moonshot.ai", "cred-platform")

	cases := []struct {
		name      string
		entries   []pluginapi.HostAuthFileEntry
		wantOK    bool
		wantModel string
		wantAuth  string
	}{
		{name: "no entries", entries: nil, wantOK: false},
		{name: "non-kimi providers", entries: []pluginapi.HostAuthFileEntry{
			entry("codex", "", "c1"), entry("claude", "", "c2"),
		}, wantOK: false},
		{name: "coding credential", entries: []pluginapi.HostAuthFileEntry{coding},
			wantOK: true, wantModel: kimiResponsesCodingModel, wantAuth: "cred-coding"},
		{name: "platform credential", entries: []pluginapi.HostAuthFileEntry{platform},
			wantOK: true, wantModel: kimiResponsesPlatformModel, wantAuth: "cred-platform"},
		{name: "coding preferred over platform", entries: []pluginapi.HostAuthFileEntry{platform, coding},
			wantOK: true, wantModel: kimiResponsesCodingModel, wantAuth: "cred-coding"},
		{name: "empty base url is coding", entries: []pluginapi.HostAuthFileEntry{entry("kimi", "", "cred-oauth")},
			wantOK: true, wantModel: kimiResponsesCodingModel, wantAuth: "cred-oauth"},
		{name: "kimi provider variants", entries: []pluginapi.HostAuthFileEntry{entry("kimi-ai", "https://api.kimi.ai/coding", "cred-ai")},
			wantOK: true, wantModel: kimiResponsesCodingModel, wantAuth: "cred-ai"},
		{name: "disabled skipped", entries: []pluginapi.HostAuthFileEntry{
			{Provider: "kimi", BaseURL: "https://api.kimi.com/coding", ID: "cred-off", Disabled: true}, platform,
		}, wantOK: true, wantModel: kimiResponsesPlatformModel, wantAuth: "cred-platform"},
		{name: "unavailable skipped", entries: []pluginapi.HostAuthFileEntry{
			{Provider: "kimi", BaseURL: "https://api.kimi.com/coding", ID: "cred-down", Unavailable: true}, platform,
		}, wantOK: true, wantModel: kimiResponsesPlatformModel, wantAuth: "cred-platform"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, ok := resolveKimiResponsesTargetFromEntries(tc.entries)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if target.model != tc.wantModel || target.authID != tc.wantAuth {
				t.Fatalf("target = %+v, want model=%q authID=%q", target, tc.wantModel, tc.wantAuth)
			}
		})
	}
}

func TestResolveKimiResponsesTargetConfiguredModel(t *testing.T) {
	t.Parallel()
	target, ok := resolveKimiResponsesTarget(pluginConfig{KimiResponsesModel: " kimi-k3-256k "})
	if !ok {
		t.Fatal("expected configured model target")
	}
	if target.model != "kimi-k3-256k" || target.authID != "" {
		t.Fatalf("target = %+v, want model kimi-k3-256k unpinned", target)
	}
}

func TestKimiResponsesBody(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"system": [{"type": "text", "text": "You are terse."}],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "first question"}]},
			{"role": "assistant", "content": [{"type": "text", "text": "first answer"}, {"type": "tool_use", "id": "t1", "name": "x", "input": {}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": "ignored"}, {"type": "text", "text": "latest question"}]}
		],
		"tools": [{"type": "web_search_20250305", "name": "web_search", "max_uses": 4}]
	}`)
	raw, err := kimiResponsesBody(body, "kimi-k2.8-code", true)
	if err != nil {
		t.Fatalf("kimiResponsesBody error = %v", err)
	}
	var out map[string]any
	if errJSON := json.Unmarshal(raw, &out); errJSON != nil {
		t.Fatalf("unmarshal: %v", errJSON)
	}
	if out["model"] != "kimi-k2.8-code" || out["instructions"] != "You are terse." {
		t.Fatalf("model/instructions mismatch: %v", out)
	}
	if out["stream"] != true || out["store"] != false {
		t.Fatalf("stream/store mismatch: %v", out)
	}
	tools, _ := out["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["type"] != "web_search" {
		t.Fatalf("tools = %v, want single web_search", tools)
	}
	include, _ := out["include"].([]any)
	if len(include) != 1 || include[0] != "web_search_call.action.sources" {
		t.Fatalf("include = %v", include)
	}
	input, _ := out["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("input len = %d, want 3", len(input))
	}
	wantRoles := []string{"user", "assistant", "user"}
	wantTypes := []string{"input_text", "output_text", "input_text"}
	wantTexts := []string{"first question", "first answer", "latest question"}
	for i, item := range input {
		m := item.(map[string]any)
		if m["role"] != wantRoles[i] || m["type"] != "message" {
			t.Fatalf("input[%d] = %v", i, m)
		}
		content := m["content"].([]any)[0].(map[string]any)
		if content["type"] != wantTypes[i] || content["text"] != wantTexts[i] {
			t.Fatalf("input[%d].content = %v", i, content)
		}
	}
}

func TestKimiResponsesBodySystemString(t *testing.T) {
	t.Parallel()
	body := []byte(`{"system": "sys text", "messages": [{"role": "user", "content": "hi"}]}`)
	raw, err := kimiResponsesBody(body, "kimi-k3", false)
	if err != nil {
		t.Fatalf("kimiResponsesBody error = %v", err)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out["instructions"] != "sys text" || out["stream"] != false {
		t.Fatalf("out = %v", out)
	}
}

func TestKimiResponsesBodyNoText(t *testing.T) {
	t.Parallel()
	body := []byte(`{"messages": [{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "x", "content": "only"}]}]}`)
	if _, err := kimiResponsesBody(body, "m", false); err == nil {
		t.Fatal("expected error for textless body")
	}
}

func TestResolveKimiResponsesTargetsEnumeratesCodingPoolInRotationOrder(t *testing.T) {
	t.Parallel()
	entry := func(provider, baseURL, id string) pluginapi.HostAuthFileEntry {
		return pluginapi.HostAuthFileEntry{Provider: provider, BaseURL: baseURL, ID: id}
	}
	entries := []pluginapi.HostAuthFileEntry{
		entry("kimi", "https://api.kimi.com/coding", "cred-a"),
		entry("kimi", "https://api.kimi.com/coding", "cred-b"),
		entry("kimi", "https://api.moonshot.ai", "cred-platform"),
	}
	tick := uint64(0)
	next := func() uint64 { tick++; return tick }

	first := resolveKimiResponsesTargetsAt(entries, "", next)
	if len(first) != 2 {
		t.Fatalf("targets = %+v, want both coding credentials", first)
	}
	if first[0].authID != "cred-a" || first[1].authID != "cred-b" {
		t.Fatalf("rotation order = %q, %q; want cred-a then cred-b", first[0].authID, first[1].authID)
	}
	for _, target := range first {
		if target.model != kimiResponsesCodingModel || target.provider != "kimi" {
			t.Fatalf("target = %+v, want coding model under kimi provider", target)
		}
	}

	second := resolveKimiResponsesTargetsAt(entries, "", next)
	if len(second) != 2 || second[0].authID != "cred-b" || second[1].authID != "cred-a" {
		t.Fatalf("second rotation = %+v; want start advanced to cred-b", second)
	}

	// A single-target resolve stays consistent with the first list element.
	tick = 0
	single, ok := resolveKimiResponsesTargetAt(entries, "", next)
	if !ok || single != first[0] {
		t.Fatalf("single resolve = %+v (%v), want first list element %+v", single, ok, first[0])
	}
}

func TestResolveKimiResponsesTargetsPlatformOverrideEnumeratesPlatformPool(t *testing.T) {
	t.Parallel()
	entry := func(id string) pluginapi.HostAuthFileEntry {
		return pluginapi.HostAuthFileEntry{Provider: "kimi", BaseURL: "https://api.moonshot.ai", ID: id}
	}
	entries := []pluginapi.HostAuthFileEntry{
		{Provider: "kimi", BaseURL: "https://api.kimi.com/coding", ID: "cred-coding"},
		entry("cred-p1"),
		entry("cred-p2"),
	}
	tick := uint64(0)
	targets := resolveKimiResponsesTargetsAt(entries, "kimi-k3", func() uint64 { tick++; return tick })
	if len(targets) != 2 {
		t.Fatalf("targets = %+v, want both platform credentials", targets)
	}
	for _, target := range targets {
		if target.model != "kimi-k3" {
			t.Fatalf("target model = %q, want configured override kimi-k3", target.model)
		}
		if target.authID != "cred-p1" && target.authID != "cred-p2" {
			t.Fatalf("target auth = %q, want a platform credential", target.authID)
		}
	}
}

func TestResolveKimiResponsesTargetsUnknownOverrideDeclined(t *testing.T) {
	t.Parallel()
	entries := []pluginapi.HostAuthFileEntry{
		{Provider: "kimi", BaseURL: "https://api.kimi.com/coding", ID: "cred-a"},
	}
	if targets := resolveKimiResponsesTargetsAt(entries, "mystery-model", func() uint64 { return 1 }); len(targets) != 0 {
		t.Fatalf("targets = %+v, want none for an unknown model family", targets)
	}
}

func TestRunKimiCredentialAttemptsRetrySemantics(t *testing.T) {
	t.Parallel()
	targets := []kimiResponsesTarget{
		{model: "m1", authID: "a", provider: "kimi"},
		{model: "m2", authID: "b", provider: "kimi"},
	}
	cases := []struct {
		name      string
		outcomes  []kimiAttemptOutcome
		wantCalls int
		wantErr   string // substring of the returned error; empty expects success
	}{
		{
			name: "retryable error moves to next credential",
			outcomes: []kimiAttemptOutcome{
				{err: fmt.Errorf("kimi http 429: throttled")},
				{payload: []byte("ok"), status: 200},
			},
			wantCalls: 2,
		},
		{
			name: "retryable http status moves to next credential",
			outcomes: []kimiAttemptOutcome{
				{status: 429},
				{payload: []byte("ok"), status: 200},
			},
			wantCalls: 2,
		},
		{
			name: "non-retryable failure stops immediately",
			outcomes: []kimiAttemptOutcome{
				{err: fmt.Errorf("kimi http 400: bad request")},
			},
			wantCalls: 1,
			wantErr:   "400",
		},
		{
			name: "exhausted pool returns the last error",
			outcomes: []kimiAttemptOutcome{
				{err: fmt.Errorf("kimi http 429: throttled")},
				{err: fmt.Errorf("kimi http 503: unavailable")},
			},
			wantCalls: 2,
			wantErr:   "503",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			last := runKimiCredentialAttempts(targets, "", func(kimiResponsesTarget) kimiAttemptOutcome {
				outcome := tc.outcomes[min(calls, len(tc.outcomes)-1)]
				calls++
				return outcome
			})
			if calls != tc.wantCalls {
				t.Fatalf("attempts = %d, want %d", calls, tc.wantCalls)
			}
			if tc.wantErr == "" {
				if last.err != nil || string(last.payload) != "ok" {
					t.Fatalf("last = %+v, want success payload", last)
				}
				return
			}
			if last.err == nil || !strings.Contains(last.err.Error(), tc.wantErr) {
				t.Fatalf("last.err = %v, want substring %q", last.err, tc.wantErr)
			}
		})
	}
}

func TestRunKimiCredentialAttemptsAppliesRouteProviderToUnpinned(t *testing.T) {
	t.Parallel()
	targets := []kimiResponsesTarget{{model: "m", provider: "kimi"}}
	var seen []kimiResponsesTarget
	runKimiCredentialAttempts(targets, "kimi.ai", func(target kimiResponsesTarget) kimiAttemptOutcome {
		seen = append(seen, target)
		return kimiAttemptOutcome{err: fmt.Errorf("kimi http 429: throttled")}
	})
	if len(seen) != 1 || seen[0].provider != "kimi-ai" {
		t.Fatalf("attempt target = %+v, want unpinned provider replaced by route hint kimi-ai", seen)
	}
}

func TestRunKimiStreamCredentialAttemptsPartialStopsRetry(t *testing.T) {
	t.Parallel()
	targets := []kimiResponsesTarget{
		{model: "m1", authID: "a", provider: "kimi"},
		{model: "m2", authID: "b", provider: "kimi"},
	}
	calls := 0
	last := runKimiStreamCredentialAttempts(targets, "", func(kimiResponsesTarget) kimiStreamAttemptOutcome {
		calls++
		return kimiStreamAttemptOutcome{partial: true, err: fmt.Errorf("kimi http 429: throttled")}
	})
	if calls != 1 {
		t.Fatalf("attempts = %d, want 1: a partial stream must not be retried", calls)
	}
	if !last.partial || last.err == nil {
		t.Fatalf("last = %+v, want the partial error returned", last)
	}
}

func TestRunKimiStreamCredentialAttemptsRetriesBeforeFirstChunk(t *testing.T) {
	t.Parallel()
	targets := []kimiResponsesTarget{
		{model: "m1", authID: "a", provider: "kimi"},
		{model: "m2", authID: "b", provider: "kimi"},
	}
	calls := 0
	last := runKimiStreamCredentialAttempts(targets, "", func(kimiResponsesTarget) kimiStreamAttemptOutcome {
		calls++
		if calls == 1 {
			return kimiStreamAttemptOutcome{err: fmt.Errorf("kimi http 503: unavailable")}
		}
		return kimiStreamAttemptOutcome{status: 200}
	})
	if calls != 2 || last.err != nil || last.status != 200 {
		t.Fatalf("calls = %d last = %+v, want retry then success", calls, last)
	}
}

func TestKimiResponsesTargetWithRouteProvider(t *testing.T) {
	t.Parallel()
	pinned := kimiResponsesTarget{model: "m", authID: "a", provider: "kimi"}
	if got := pinned.withRouteProvider("kimi-ai"); got.provider != "kimi" {
		t.Fatalf("pinned target provider = %q, want credential-derived kimi preserved", got.provider)
	}
	unpinned := kimiResponsesTarget{model: "m", provider: "kimi"}
	if got := unpinned.withRouteProvider("kimi.ai"); got.provider != "kimi-ai" {
		t.Fatalf("unpinned target provider = %q, want kimi-ai from route hint", got.provider)
	}
	if got := unpinned.withRouteProvider("   "); got.provider != "kimi" {
		t.Fatalf("empty hint changed provider to %q", got.provider)
	}
}

func TestKimiRouteProviderHint(t *testing.T) {
	t.Parallel()
	if got := kimiRouteProviderHint([]string{"claude", "kimi"}); got != "" {
		t.Fatalf("plain-kimi hint = %q, want empty (default bucket)", got)
	}
	if got := kimiRouteProviderHint([]string{"kimi-ai"}); got != "kimi-ai" {
		t.Fatalf("kimi-ai hint = %q", got)
	}
	if got := kimiRouteProviderHint([]string{"kimi.ai", "kimi"}); got != "kimi-ai" {
		t.Fatalf("mixed hint = %q, want kimi-ai", got)
	}
	if got := kimiRouteProviderHint(nil); got != "" {
		t.Fatalf("nil hint = %q, want empty", got)
	}
}
