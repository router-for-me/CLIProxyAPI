package main

import (
	"encoding/json"
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
