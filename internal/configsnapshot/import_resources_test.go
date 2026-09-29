package configsnapshot

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// TestBuildResourcePlanNilConfig asserts the nil-config guard produces an
// empty, planned-only report instead of panicking.
func TestBuildResourcePlanNilConfig(t *testing.T) {
	plan, err := BuildResourcePlan(nil)
	if err == nil {
		t.Fatal("BuildResourcePlan(nil) error = nil; want non-nil")
	}
	if plan == nil {
		t.Fatal("plan = nil; want non-nil with Planned=true")
	}
	if !plan.Report.Planned {
		t.Fatal("plan.Report.Planned = false; want true")
	}
	if len(plan.Providers) != 0 || len(plan.APIKeys) != 0 {
		t.Fatalf("plan providers=%d apiKeys=%d; want 0/0", len(plan.Providers), len(plan.APIKeys))
	}
}

// TestBuildResourcePlanAllProviderSections pins that every supported
// provider section is represented with the correct provider_type and
// representative preserved fields.
func TestBuildResourcePlanAllProviderSections(t *testing.T) {
	weight := 5
	priority := 2
	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: "sk-claude", BaseURL: "https://claude.example", ProxyURL: "http://proxy:1",
			Priority: 3, Weight: &weight, Disabled: true, Prefix: "teamA/",
			Headers:                 map[string]string{"X-Test": "1"},
			ExcludedModels:          []string{"claude-2"},
			RebuildMidSystemMessage: true,
			DisableCooling:          true,
		}},
		InteractionsKey: []config.GeminiKey{{
			APIKey: "sk-inter", BaseURL: "https://inter.example", Priority: 1,
		}},
		GeminiKey: []config.GeminiKey{{
			APIKey: "sk-gemini", BaseURL: "https://gemini.example", Priority: 2,
		}},
		CodexKey: []config.CodexKey{{
			APIKey: "sk-codex", BaseURL: "https://codex.example", Websockets: true,
			AlphaSearch: true, DisableCooling: true,
		}},
		XAIKey: []config.CodexKey{{
			APIKey: "sk-xai", BaseURL: "https://xai.example",
		}},
		MetaKey: []config.CodexKey{{
			APIKey: "sk-meta", BaseURL: "https://meta.example",
		}},
		VertexCompatAPIKey: []config.VertexCompatKey{{
			APIKey: "sk-vertex", BaseURL: "https://vertex.example",
		}},
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "openai-pool", BaseURL: "https://oai.example", Strategy: "round-robin",
			CircuitBreaker: true, SupportPromptCacheKey: true, DisableCooling: true,
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
				APIKey: "sk-entry", Name: "primary", Weight: &weight, Priority: &priority,
			}},
		}},
		OpenCodeGo: []config.OpenCodeGo{{
			Name: "opencode-pool", BaseURL: "https://ocg.example",
			APIKeyEntries: []config.OpenCodeGoKey{{APIKey: "sk-ocg", Name: "e1"}},
		}},
	}

	plan, err := BuildResourcePlan(cfg)
	if err != nil {
		t.Fatalf("BuildResourcePlan: %v", err)
	}
	if plan == nil || !plan.Report.Planned {
		t.Fatal("plan/Planned missing")
	}

	byType := map[string][]store.UpstreamProvider{}
	for _, p := range plan.Providers {
		byType[p.ProviderType] = append(byType[p.ProviderType], p)
	}
	wantTypes := []string{
		"claude-api-key", "interactions-api-key", "gemini-api-key", "codex-api-key",
		"xai-api-key", "meta-api-key", "vertex-api-key", "openai-compatibility", "opencode-go",
	}
	for _, want := range wantTypes {
		if len(byType[want]) != 1 {
			t.Fatalf("provider_type %q count = %d; want 1", want, len(byType[want]))
		}
	}

	claude := byType["claude-api-key"][0]
	if claude.APIKey != "sk-claude" || claude.BaseURL != "https://claude.example" || claude.ProxyURL != "http://proxy:1" {
		t.Fatalf("claude basics = %+v", claude)
	}
	if !claude.RebuildMidSystemMessage {
		t.Fatal("claude.RebuildMidSystemMessage = false; want true")
	}
	if claude.ExtraConfig[ecDisabled] != true || claude.ExtraConfig[ecDisableCooling] != true {
		t.Fatalf("claude ExtraConfig = %+v; want disabled+disable_cooling", claude.ExtraConfig)
	}
	if got, ok := claude.ExtraConfig[ecWeight].(int); !ok || got != 5 {
		t.Fatalf("claude ExtraConfig weight = %v; want 5", claude.ExtraConfig[ecWeight])
	}
	if !reflect.DeepEqual(claude.Headers, map[string]string{"X-Test": "1"}) {
		t.Fatalf("claude headers = %+v", claude.Headers)
	}
	if !reflect.DeepEqual(claude.ExcludedModels, []string{"claude-2"}) {
		t.Fatalf("claude excluded = %+v", claude.ExcludedModels)
	}

	codex := byType["codex-api-key"][0]
	if !codex.Websockets {
		t.Fatal("codex.Websockets = false; want true")
	}
	if codex.ExtraConfig[ecAlphaSearch] != true || codex.ExtraConfig[ecDisableCooling] != true {
		t.Fatalf("codex ExtraConfig = %+v", codex.ExtraConfig)
	}

	meta := byType["meta-api-key"][0]
	if meta.APIKey != "sk-meta" || meta.BaseURL != "https://meta.example" {
		t.Fatalf("meta basics = %+v", meta)
	}
	if meta.Name != "meta-1" {
		t.Fatalf("meta Name = %q; want positional meta-1", meta.Name)
	}
	if meta.ExtraConfig[ecDisableCooling] == true {
		t.Fatalf("meta ExtraConfig = %+v; want no disable_cooling (unset)", meta.ExtraConfig)
	}

	oai := byType["openai-compatibility"][0]
	if oai.Name != "openai-pool" || !oai.CircuitBreaker || oai.RoutingStrategy != "round-robin" {
		t.Fatalf("openai provider = %+v", oai)
	}
	if oai.ExtraConfig[ecSupportPromptCacheKey] != true {
		t.Fatalf("openai ExtraConfig = %+v", oai.ExtraConfig)
	}
	if len(oai.APIKeyEntries) != 1 {
		t.Fatalf("openai entries = %d; want 1", len(oai.APIKeyEntries))
	}
	entry := oai.APIKeyEntries[0]
	if entry.APIKey != "sk-entry" || entry.Name != "primary" || entry.Weight == nil || *entry.Weight != 5 {
		t.Fatalf("openai entry = %+v", entry)
	}
	if entry.Priority == nil || *entry.Priority != 2 {
		t.Fatalf("openai entry priority = %v", entry.Priority)
	}
	if entry.ID != 0 {
		t.Fatalf("openai entry ID = %d; want 0 (apply path assigns)", entry.ID)
	}

	ocg := byType["opencode-go"][0]
	if ocg.Name != "opencode-pool" || len(ocg.APIKeyEntries) != 1 {
		t.Fatalf("opencode provider = %+v", ocg)
	}

	if plan.Report.Counts["internal_users"]["not_applicable"] != 1 {
		t.Fatalf("internal_users count = %v", plan.Report.Counts)
	}
}

// TestBuildResourcePlanKeylessRowsRemainDistinct pins that two keyless
// OpenAI-compatibility rows with different entry sets produce different
// identities and are both retained in the plan.
func TestBuildResourcePlanKeylessRowsRemainDistinct(t *testing.T) {
	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				BaseURL: "https://same.example",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "sk-A", Name: "a"},
				},
			},
			{
				BaseURL: "https://same.example",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "sk-B", Name: "b"},
				},
			},
		},
	}
	plan, err := BuildResourcePlan(cfg)
	if err != nil {
		t.Fatalf("BuildResourcePlan: %v", err)
	}
	if len(plan.Providers) != 2 {
		t.Fatalf("providers = %d; want 2 (no silent collapse)", len(plan.Providers))
	}
	seen := map[string]bool{}
	for _, p := range plan.Providers {
		k := dupKey(p)
		if seen[k] {
			t.Fatalf("two keyless rows with different entry sets share identity %s", k)
		}
		seen[k] = true
	}
}

// TestBuildResourcePlanDuplicateIdentityRecorded pins that duplicate
// identities produce errors and keep every source row in the plan.
func TestBuildResourcePlanDuplicateIdentityRecorded(t *testing.T) {
	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{
			{APIKey: "sk-dup", BaseURL: "https://dup.example"},
			{APIKey: "sk-dup", BaseURL: "https://dup.example"},
		},
	}
	plan, err := BuildResourcePlan(cfg)
	if err != nil {
		t.Fatalf("BuildResourcePlan: %v", err)
	}
	if len(plan.Providers) != 2 {
		t.Fatalf("providers = %d; want 2 (rows retained)", len(plan.Providers))
	}
	if len(plan.Report.Errors) == 0 {
		t.Fatal("report.Errors empty; want duplicate-identity error")
	}
}

// TestBuildResourcePlanClientAPIKeys pins hash-based client key conversion
// and that plaintext secrets do not appear in report structures.
func TestBuildResourcePlanClientAPIKeys(t *testing.T) {
	secret := "sk-testsecret123456"
	cfg := &config.Config{}
	cfg.APIKeys = []string{secret, ""}
	plan, err := BuildResourcePlan(cfg)
	if err != nil {
		t.Fatalf("BuildResourcePlan: %v", err)
	}
	if len(plan.APIKeys) != 1 {
		t.Fatalf("api keys = %d; want 1 (empty skipped)", len(plan.APIKeys))
	}
	k := plan.APIKeys[0]
	if k.KeyHash == "" || k.KeyHash == secret {
		t.Fatalf("key hash = %q; want hashed", k.KeyHash)
	}
	if k.Status != "active" {
		t.Fatalf("status = %q; want active", k.Status)
	}
	if k.KeyPrefix == "" || len(k.KeyPrefix) > 8 {
		t.Fatalf("key prefix = %q", k.KeyPrefix)
	}
	// No plaintext secret anywhere in the report.
	for kind, msgs := range plan.Report.Errors {
		for _, m := range msgs {
			if containsSecret(m, secret) {
				t.Fatalf("plaintext secret leaked in Errors[%s]: %s", kind, m)
			}
		}
	}
	for kind, msgs := range plan.Report.Unsupported {
		for _, m := range msgs {
			if containsSecret(m, secret) {
				t.Fatalf("plaintext secret leaked in Unsupported[%s]: %s", kind, m)
			}
		}
	}
}

func containsSecret(s, secret string) bool {
	return len(secret) > 0 && len(s) >= len(secret) && (s == secret ||
		(len(s) > len(secret) && (indexOf(s, secret) >= 0)))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestPlanEntriesNormalizeWeight pins the planner's contract: every entry
// produced by BuildResourcePlan carries a non-nil Weight that is at least
// 1, regardless of whether the source config left it nil, zero, or
// negative. This is the planner-boundary normalization the round-2
// weighted selector (docs/plans/2026-09-17-omniroute-round-2-design.md)
// relies on so downstream code can dereference Weight unconditionally.
func TestPlanEntriesNormalizeWeight(t *testing.T) {
	zero := 0
	neg := -2
	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "oai-pool",
			BaseURL: "https://oai.example",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{
				{APIKey: "sk-a", Name: "a", Weight: &zero},
				{APIKey: "sk-b", Name: "b", Weight: &neg},
				{APIKey: "sk-c", Name: "c", Weight: nil},
			},
		}},
		OpenCodeGo: []config.OpenCodeGo{{
			Name:    "ocg-pool",
			BaseURL: "https://ocg.example",
			APIKeyEntries: []config.OpenCodeGoKey{
				{APIKey: "sk-x", Name: "x", Weight: &zero},
				{APIKey: "sk-y", Name: "y", Weight: &neg},
				{APIKey: "sk-z", Name: "z", Weight: nil},
			},
		}},
	}
	plan, err := BuildResourcePlan(cfg)
	if err != nil {
		t.Fatalf("BuildResourcePlan: %v", err)
	}
	for _, p := range plan.Providers {
		for _, e := range p.APIKeyEntries {
			if e.Weight == nil {
				t.Errorf("%s entry %q: Weight = nil; want non-nil >= 1", p.ProviderType, e.Name)
				continue
			}
			if *e.Weight < 1 {
				t.Errorf("%s entry %q: Weight = %d; want >= 1", p.ProviderType, e.Name, *e.Weight)
			}
		}
	}
}

// TestPlanEntriesPreserveValidWeight pins that explicit positive weights
// flow through the planner untouched.
func TestPlanEntriesPreserveValidWeight(t *testing.T) {
	three := 3
	seven := 7
	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "oai-pool",
			BaseURL: "https://oai.example",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{
				{APIKey: "sk-a", Name: "a", Weight: &three},
				{APIKey: "sk-b", Name: "b", Weight: &seven},
			},
		}},
		OpenCodeGo: []config.OpenCodeGo{{
			Name:    "ocg-pool",
			BaseURL: "https://ocg.example",
			APIKeyEntries: []config.OpenCodeGoKey{
				{APIKey: "sk-x", Name: "x", Weight: &seven},
				{APIKey: "sk-y", Name: "y", Weight: &three},
			},
		}},
	}
	plan, err := BuildResourcePlan(cfg)
	if err != nil {
		t.Fatalf("BuildResourcePlan: %v", err)
	}
	bySection := map[string]map[string]int{}
	for _, p := range plan.Providers {
		bySection[p.ProviderType] = map[string]int{}
		for _, e := range p.APIKeyEntries {
			if e.Weight == nil {
				t.Errorf("%s entry %q: Weight = nil; want non-nil", p.ProviderType, e.Name)
				continue
			}
			bySection[p.ProviderType][e.Name] = *e.Weight
		}
	}
	if got := bySection["openai-compatibility"]["a"]; got != 3 {
		t.Errorf("openai a weight = %d; want 3", got)
	}
	if got := bySection["openai-compatibility"]["b"]; got != 7 {
		t.Errorf("openai b weight = %d; want 7", got)
	}
	if got := bySection["opencode-go"]["x"]; got != 7 {
		t.Errorf("opencode x weight = %d; want 7", got)
	}
	if got := bySection["opencode-go"]["y"]; got != 3 {
		t.Errorf("opencode y weight = %d; want 3", got)
	}
}

// TestBuildResourcePlanDoesNotMutateConfig pins input immutability: a deep
// snapshot of the config before the call must equal the config after.
func TestBuildResourcePlanDoesNotMutateConfig(t *testing.T) {
	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: "sk-x", BaseURL: "https://x.example",
			Headers:        map[string]string{"H": "v"},
			ExcludedModels: []string{"m1"},
			Models:         []config.ClaudeModel{{Name: "n", Alias: "a"}},
		}},
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "p", BaseURL: "https://p.example",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k", Name: "n"}},
			Models:        []config.OpenAICompatibilityModel{{Name: "mn", Alias: "ma"}},
		}},
	}
	cfg.APIKeys = []string{"sk-secret-abc"}
	before := deepSnapshotConfig(t, cfg)

	if _, err := BuildResourcePlan(cfg); err != nil {
		t.Fatalf("BuildResourcePlan: %v", err)
	}
	after := deepSnapshotConfig(t, cfg)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("config mutated by BuildResourcePlan")
	}
}

// deepSnapshotConfig re-marshals the config via reflection-free structural
// copy of the fields the planner touches. json.Marshal would be simpler but
// ClaudeKey contains renderer-only unexported-free fields that marshal
// cleanly; the snapshot uses encoding/json round-trip for full coverage.
func deepSnapshotConfig(t *testing.T, cfg *config.Config) string {
	t.Helper()
	raw, err := jsonMarshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return string(raw)
}

// TestModelExtraFieldsPreserved pins MaxContextLength / IsCompat round-trip
// into the model Thinking map so the values survive without dedicated columns.
func TestModelExtraFieldsPreserved(t *testing.T) {
	cfg := &config.Config{
		GeminiKey: []config.GeminiKey{{
			APIKey: "sk-g",
			Models: []config.GeminiModel{{
				Name: "gemini-3", Alias: "g3",
				MaxContextLength: 200000, IsCompat: true,
			}},
		}},
	}
	plan, err := BuildResourcePlan(cfg)
	if err != nil {
		t.Fatalf("BuildResourcePlan: %v", err)
	}
	p := plan.Providers[0]
	if len(p.Models) != 1 {
		t.Fatalf("models = %d; want 1", len(p.Models))
	}
	thinking := p.Models[0].Thinking
	if thinking == nil {
		t.Fatal("model Thinking map = nil; want extras recorded")
	}
	if thinking[ecModelMaxContextLength] != 200000 {
		t.Fatalf("max-context-length = %v", thinking[ecModelMaxContextLength])
	}
	if thinking[ecModelIsCompat] != true {
		t.Fatalf("is-compat = %v", thinking[ecModelIsCompat])
	}
}

// TestBuildResourcePlanNeuralwattPreservesServiceTier pins that the
// planner captures config.NeuralwattKey.ServiceTier into the provider
// row's ExtraConfig under the raw "service_tier" key the render-side
// codexKeyFromProvider reads. Without this, a PG-first deployment silently
// drops the billing-tier selection on every reload (PG row → render →
// merged.NeuralwattKey overwrites the in-memory cfg). Mirrors the
// convertCodexKeys alpha_search assertion at line 122 above.
func TestBuildResourcePlanNeuralwattPreservesServiceTier(t *testing.T) {
	cfg := &config.Config{NeuralwattKey: []config.NeuralwattKey{{
		APIKey:      "sk-neuralwatt",
		BaseURL:     "https://api.neuralwatt.com/v1",
		ServiceTier: "flex",
	}}}

	plan, err := BuildResourcePlan(cfg)
	if err != nil {
		t.Fatalf("BuildResourcePlan: %v", err)
	}
	if len(plan.Providers) != 1 {
		t.Fatalf("Providers len = %d; want 1", len(plan.Providers))
	}
	p := plan.Providers[0]
	if p.ProviderType != ptNeuralwattAPIKey {
		t.Fatalf("ProviderType = %q; want %q", p.ProviderType, ptNeuralwattAPIKey)
	}
	if p.APIKey != "sk-neuralwatt" || p.BaseURL != "https://api.neuralwatt.com/v1" {
		t.Fatalf("provider basics = %+v", p)
	}
	if p.Name != "neuralwatt-1" {
		t.Fatalf("Name = %q; want positional neuralwatt-1", p.Name)
	}
	if p.ExtraConfig["service_tier"] != "flex" {
		t.Fatalf("ExtraConfig[service_tier] = %v; want flex", p.ExtraConfig["service_tier"])
	}
}
