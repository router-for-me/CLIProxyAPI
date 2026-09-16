package configsnapshot

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

func TestMapResourcesRejectsNilDependencies(t *testing.T) {
	_, err := MapResources(context.Background(), nil, nil, nil, &config.Config{})
	if err == nil {
		t.Fatal("expected nil dependency error")
	}
}

func TestImportReportCounts(t *testing.T) {
	var report ImportReport
	report.Add("upstream_providers", "created")
	report.Add("upstream_providers", "updated")
	if report.Created("upstream_providers") != 1 || report.Updated("upstream_providers") != 1 {
		t.Fatalf("unexpected report counts: %#v", report.Counts)
	}
}

func TestConfigProvidersMapsAllSectionsAndPreservesFields(t *testing.T) {
	weight := 7
	cfg := &config.Config{
		ClaudeKey:           []config.ClaudeKey{{APIKey: " claude ", Weight: &weight, RebuildMidSystemMessage: true, Models: []config.ClaudeModel{{Name: "m", IsCompat: true}}}},
		InteractionsKey:     []config.GeminiKey{{APIKey: "interact"}},
		GeminiKey:           []config.GeminiKey{{APIKey: "gemini"}},
		CodexKey:            []config.CodexKey{{APIKey: "codex", Websockets: true}},
		XAIKey:              []config.XAIKey{{APIKey: "xai"}},
		VertexCompatAPIKey:  []config.VertexCompatKey{{APIKey: "vertex"}},
		OpenAICompatibility: []config.OpenAICompatibility{{Name: "named", BaseURL: "url", APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k", Weight: &weight}}}, {Name: "keyless"}},
		OpenCodeGo:          []config.OpenCodeGo{{Name: "go", APIKeyEntries: []config.OpenCodeGoKey{{APIKey: "g"}}, Models: []config.OpenCodeGoModel{{Name: "gm", WireFormat: "anthropic"}}}},
	}
	got := configProviders(cfg)
	if len(got) != 9 {
		t.Fatalf("got %d providers, want 9: %#v", len(got), got)
	}
	if got[0].APIKey != "claude" || !got[0].RebuildMidSystemMessage || len(got[0].Models) != 1 {
		t.Fatalf("claude fields lost: %#v", got[0])
	}
	if got[1].ProviderType != "interactions-api-key" {
		t.Fatalf("interactions missing: %#v", got)
	}
	if got[7].Name != "keyless" || got[7].APIKey != "" {
		t.Fatalf("keyless row dropped/incorrect: %#v", got[7])
	}
	if got[8].ProviderType != "opencode-go" || got[8].Models[0].WireFormat != "anthropic" {
		t.Fatalf("opencode fields lost: %#v", got[8])
	}
}

func TestProviderIdentityUsesNameForNamedRows(t *testing.T) {
	if providerIdentity("openai-compatibility", "named") != providerIdentity("openai-compatibility", "named") {
		t.Fatal("identity unstable")
	}
}

func TestConfigProvidersDeduplicatesSameIdentity(t *testing.T) {
	cfg := &config.Config{GeminiKey: []config.GeminiKey{{APIKey: "dup"}, {APIKey: "dup"}, {APIKey: "dup2"}}}
	got := configProviders(cfg)
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2 (dedupe of identical api-key entries): %#v", len(got), got)
	}
	seen := map[string]int{}
	for _, p := range got {
		seen[providerIdentity(p.ProviderType, p.APIKey)]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Fatalf("identity %q duplicated %d times", id, n)
		}
	}
}

func TestConfigProvidersPreservesInputConfig(t *testing.T) {
	cfg := &config.Config{GeminiKey: []config.GeminiKey{{APIKey: "  spaced  ", Models: []config.GeminiModel{{Name: "m"}}}}}
	_ = configProviders(cfg)
	if cfg.GeminiKey[0].APIKey != "  spaced  " {
		t.Fatalf("config mutated: %q", cfg.GeminiKey[0].APIKey)
	}
}

func TestNormalizeWhitespaceCleansSlicesAndMaps(t *testing.T) {
	p := store.UpstreamProvider{ProviderType: "claude-api-key", APIKey: " k ", ExcludedModels: []string{" a ", ""}, Headers: map[string]string{" H ": " V "}, Models: []store.UpstreamProviderModel{{Name: " m "}}}
	got := normalizeProviderForCompare(p)
	if got.APIKey != "k" {
		t.Fatalf("api key not trimmed: %q", got.APIKey)
	}
	if len(got.ExcludedModels) != 1 || got.ExcludedModels[0] != "a" {
		t.Fatalf("excluded not cleaned: %#v", got.ExcludedModels)
	}
	if got.Headers["h"] != "V" {
		t.Fatalf("headers not cleaned: %#v", got.Headers)
	}
	if got.Models[0].Name != "m" {
		t.Fatalf("model name not cleaned: %q", got.Models[0].Name)
	}
}

var _ store.UpstreamProviderStore
