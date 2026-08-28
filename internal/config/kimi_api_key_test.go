package config

import "testing"

func TestParseConfigBytesKimiAPIKey(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`kimi-api-key:
  - api-key: " sk-open "
    service: Open-Platform
    region: Domestic
    name: " desk "
    weight: 5
    prefix: " team-kimi "
    proxy-url: " http://proxy.local "
    headers:
      X-Custom: value
    priority: 2
    disable-cooling: true
    models:
      - name: " k3 "
        alias: " kimi-k3 "
    excluded-models:
      - " kimi-k2.5 "
  - api-key: "sk-code"
    service: coding-plan
    region: domestic
  - api-key: "sk-missing-region"
    service: open-platform
  - api-key: "sk-bad-service"
    service: other
  - api-key: ""
    service: coding-plan
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if len(cfg.KimiKey) != 2 {
		t.Fatalf("kimi-api-key count = %d, want 2", len(cfg.KimiKey))
	}
	open := cfg.KimiKey[0]
	if open.APIKey != "sk-open" {
		t.Fatalf("api-key = %q, want sk-open", open.APIKey)
	}
	if open.Service != KimiServiceOpenPlatform {
		t.Fatalf("service = %q, want %s", open.Service, KimiServiceOpenPlatform)
	}
	if open.Region != KimiRegionDomestic {
		t.Fatalf("region = %q, want %s", open.Region, KimiRegionDomestic)
	}
	if open.Name != "desk" {
		t.Fatalf("name = %q, want desk", open.Name)
	}
	if open.Weight == nil || *open.Weight != 5 {
		t.Fatalf("weight = %v, want 5", open.Weight)
	}
	if open.Prefix != "team-kimi" {
		t.Fatalf("prefix = %q, want team-kimi", open.Prefix)
	}
	if open.ProxyURL != "http://proxy.local" {
		t.Fatalf("proxy-url = %q, want http://proxy.local", open.ProxyURL)
	}
	if open.Headers["X-Custom"] != "value" {
		t.Fatalf("X-Custom header = %q, want value", open.Headers["X-Custom"])
	}
	if open.Priority != 2 {
		t.Fatalf("priority = %d, want 2", open.Priority)
	}
	if open.DisableCooling == nil || !*open.DisableCooling {
		t.Fatalf("disable-cooling = %v, want true", open.DisableCooling)
	}
	if len(open.Models) != 1 || open.Models[0].Name != "k3" || open.Models[0].Alias != "kimi-k3" {
		t.Fatalf("models = %#v, want [{name:k3 alias:kimi-k3}]", open.Models)
	}
	if len(open.ExcludedModels) != 1 || open.ExcludedModels[0] != "kimi-k2.5" {
		t.Fatalf("excluded-models = %#v, want [kimi-k2.5]", open.ExcludedModels)
	}
	code := cfg.KimiKey[1]
	if code.APIKey != "sk-code" {
		t.Fatalf("coding-plan api-key = %q, want sk-code", code.APIKey)
	}
	if code.Service != KimiServiceCodingPlan {
		t.Fatalf("coding-plan service = %q, want %s", code.Service, KimiServiceCodingPlan)
	}
	if code.Region != "" {
		t.Fatalf("coding-plan region = %q, want empty", code.Region)
	}
}

func TestKimiEndpointHelpers(t *testing.T) {
	tests := []struct {
		service string
		region  string
		base    string
		chat    string
		claude  string
	}{
		{
			service: KimiServiceCodingPlan,
			region:  "",
			base:    "https://api.kimi.com/coding",
			chat:    "https://api.kimi.com/coding/v1/chat/completions",
			claude:  "https://api.kimi.com/coding",
		},
		{
			service: KimiServiceOpenPlatform,
			region:  KimiRegionDomestic,
			base:    "https://api.moonshot.cn",
			chat:    "https://api.moonshot.cn/v1/chat/completions",
			claude:  "https://api.moonshot.cn/anthropic",
		},
		{
			service: KimiServiceOpenPlatform,
			region:  KimiRegionInternational,
			base:    "https://api.moonshot.ai",
			chat:    "https://api.moonshot.ai/v1/chat/completions",
			claude:  "https://api.moonshot.ai/anthropic",
		},
	}
	for _, tt := range tests {
		if got := KimiBaseURL(tt.service, tt.region); got != tt.base {
			t.Fatalf("KimiBaseURL(%q,%q) = %q, want %q", tt.service, tt.region, got, tt.base)
		}
		if got := KimiOpenAIChatCompletionsURL(tt.service, tt.region); got != tt.chat {
			t.Fatalf("KimiOpenAIChatCompletionsURL(%q,%q) = %q, want %q", tt.service, tt.region, got, tt.chat)
		}
		if got := KimiAnthropicBaseURL(tt.service, tt.region); got != tt.claude {
			t.Fatalf("KimiAnthropicBaseURL(%q,%q) = %q, want %q", tt.service, tt.region, got, tt.claude)
		}
		key := KimiKey{Service: tt.service, Region: tt.region}
		if got := key.GetBaseURL(); got != tt.base {
			t.Fatalf("KimiKey.GetBaseURL(%q,%q) = %q, want %q", tt.service, tt.region, got, tt.base)
		}
	}
}

func TestMatchKimiKeyUsesServiceAndRegion(t *testing.T) {
	entries := []KimiKey{
		{APIKey: "sk-shared", Service: KimiServiceOpenPlatform, Region: KimiRegionDomestic, ProxyURL: "http://open"},
		{APIKey: "sk-shared", Service: KimiServiceCodingPlan, ProxyURL: "http://code"},
	}
	open := MatchKimiKey(entries, "sk-shared", KimiServiceOpenPlatform, KimiRegionDomestic, "", "http://open", "", "")
	if open == nil || open.ProxyURL != "http://open" {
		t.Fatalf("open-platform match = %+v", open)
	}
	code := MatchKimiKey(entries, "sk-shared", KimiServiceCodingPlan, "", "", "http://code", "", "1")
	if code == nil || code.ProxyURL != "http://code" {
		t.Fatalf("coding-plan match = %+v", code)
	}
	if got := MatchKimiKey(entries, "sk-shared", KimiServiceOpenPlatform, KimiRegionInternational, "", "http://open", "", ""); got != nil {
		t.Fatalf("wrong region match = %+v", got)
	}
}

func TestMatchKimiKeyUsesPrefixAndProxyURL(t *testing.T) {
	entries := []KimiKey{
		{APIKey: "sk-shared", Service: KimiServiceCodingPlan, Prefix: "a", ProxyURL: "http://a"},
		{APIKey: "sk-shared", Service: KimiServiceCodingPlan, Prefix: "b", ProxyURL: "http://b"},
	}
	got := MatchKimiKey(entries, "sk-shared", KimiServiceCodingPlan, "", "b", "http://b", "", "0")
	if got == nil || got.Prefix != "b" {
		t.Fatalf("stale index should still match prefix b, got %+v", got)
	}
	if got := MatchKimiKey(entries, "sk-shared", KimiServiceCodingPlan, "", "missing", "", "", ""); got != nil {
		t.Fatalf("unknown prefix match = %+v", got)
	}
}

func TestMatchKimiKeyUsesHeaders(t *testing.T) {
	entries := []KimiKey{
		{APIKey: "sk-shared", Service: KimiServiceCodingPlan, Headers: map[string]string{"X-A": "1"}},
		{APIKey: "sk-shared", Service: KimiServiceCodingPlan, Headers: map[string]string{"X-B": "2"}},
	}
	blobB := FormatSortedHeaders(map[string]string{"X-B": "2"})
	got := MatchKimiKey(entries, "sk-shared", KimiServiceCodingPlan, "", "", "", blobB, "0")
	if got == nil || got.Headers["X-B"] != "2" {
		t.Fatalf("header match = %+v, want X-B", got)
	}
}
