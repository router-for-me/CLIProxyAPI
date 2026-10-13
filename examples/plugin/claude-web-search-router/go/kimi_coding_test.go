package main

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestKimiCodingClientDefaults(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		baseURL      string
		model        string
		wantEndpoint string
		wantModel    string
	}{
		{name: "defaults", baseURL: "", model: "",
			wantEndpoint: "https://api.kimi.ai/coding/v1/responses", wantModel: "kimi-for-coding"},
		{name: "custom base trims slash", baseURL: "https://example.test/v1/", model: "",
			wantEndpoint: "https://example.test/v1/responses", wantModel: "kimi-for-coding"},
		{name: "base with /v1 suffix", baseURL: "https://api.kimi.ai/coding/v1", model: "",
			wantEndpoint: "https://api.kimi.ai/coding/v1/responses", wantModel: "kimi-for-coding"},
		{name: "custom model", baseURL: "", model: "k2.8-code",
			wantEndpoint: "https://api.kimi.ai/coding/v1/responses", wantModel: "k2.8-code"},
	}
	for _, tc := range cases {
		c := newKimiCodingClientWithOptions([]string{"k1"}, tc.baseURL, tc.model, nil)
		if got := c.endpoint(); got != tc.wantEndpoint {
			t.Fatalf("%s: endpoint() = %q, want %q", tc.name, got, tc.wantEndpoint)
		}
		if got := c.resolveModel(); got != tc.wantModel {
			t.Fatalf("%s: resolveModel() = %q, want %q", tc.name, got, tc.wantModel)
		}
	}
}

func TestKimiCodingClientAvailable(t *testing.T) {
	t.Parallel()
	if newKimiCodingClientWithOptions(nil, "", "", nil).available() {
		t.Fatal("client with no keys should not be available")
	}
	if !newKimiCodingClientWithOptions([]string{"k1", "  ", "k2"}, "", "", nil).available() {
		t.Fatal("client with keys should be available")
	}
	c := newKimiCodingClientWithOptions([]string{"a", "b"}, "", "", nil)
	if c.nextKey() != "a" || c.nextKey() != "b" || c.nextKey() != "a" {
		t.Fatal("nextKey should round-robin keys")
	}
}

func TestParseCodingResponses(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"status": "completed",
		"output": [
			{"type": "web_search_call", "action": {"type": "search", "query": "q",
				"sources": [
					{"type": "url_citation", "url": "https://a.example", "title": "A"},
					{"type": "url_citation", "url": "https://b.example"}
				]}},
			{"type": "reasoning", "summary": [{"type": "summary_text", "text": "thinking"}]},
			{"type": "message", "content": [
				{"type": "output_text", "text": "The answer."},
				{"type": "other", "text": "ignored"}
			]}
		]
	}`)
	answer, hits, err := parseCodingResponses(body)
	if err != nil {
		t.Fatalf("parseCodingResponses error: %v", err)
	}
	if answer != "The answer." {
		t.Fatalf("answer = %q, want %q", answer, "The answer.")
	}
	if len(hits) != 2 || hits[0].URL != "https://a.example" || hits[0].Title != "A" || hits[1].URL != "https://b.example" {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestParseCodingResponsesErrors(t *testing.T) {
	t.Parallel()
	if _, _, err := parseCodingResponses([]byte(`{"error": "bad"}`)); err == nil {
		t.Fatal("expected error for body without output array")
	}
	if _, _, err := parseCodingResponses([]byte(`{"status": "failed", "output": []}`)); err == nil {
		t.Fatal("expected error for empty output")
	}
}

func TestKimiCodingRouteRegistered(t *testing.T) {
	t.Parallel()
	found := false
	for _, b := range defaultWebSearchFallbackChain() {
		if b == backendKimiCoding {
			found = true
		}
	}
	if !found {
		t.Fatal("kimi_coding must be in the default fallback chain")
	}
	reg := pluginRegistration()
	for _, field := range reg.Metadata.ConfigFields {
		if field.Name != "route" {
			continue
		}
		found = false
		for _, v := range field.EnumValues {
			if v == string(backendKimiCoding) {
				found = true
			}
		}
		if !found {
			t.Fatal("kimi_coding must be in the route enum values")
		}
		return
	}
	t.Fatal("route config field not found")
}

func TestKimiCodingExecutionPlans(t *testing.T) {
	cfg := pluginConfig{Enabled: true, Route: "kimi_coding", KimiCodingAPIKeys: []string{"k1"}}
	currentConfig.Store(cfg)
	req := pluginapi.ModelRouteRequest{
		SourceFormat:   "claude",
		RequestedModel: "claude-sonnet-4-6",
	}
	resp, ok := tryRouteBackend(backendKimiCoding, cfg, req)
	if !ok || !resp.Handled || resp.Reason != "claude_code_web_search_kimi_coding" {
		t.Fatalf("tryRouteBackend = %+v, %v", resp, ok)
	}
	plans := executionPlansForRoute(cfg, req, "kimi_coding")
	if len(plans) != 1 || plans[0].backend != backendKimiCoding {
		t.Fatalf("executionPlansForRoute = %+v", plans)
	}
	plans = executionPlansForExecuteRoute(cfg, req, "kimi_coding")
	if len(plans) != 1 || plans[0].backend != backendKimiCoding {
		t.Fatalf("executionPlansForExecuteRoute = %+v", plans)
	}
}
