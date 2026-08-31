package upstreamsync

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// ptrInt is a tiny helper to take the address of an int literal in tests.
func ptrInt(v int) *int { return &v }

// TestClaudeRenderFanOutPerEntry verifies that a single UpstreamProvider
// row with multiple APIKeyEntries fans out into one config.ClaudeKey per
// entry, in order, with row-level fields copied onto each item.
func TestClaudeRenderFanOutPerEntry(t *testing.T) {
	w7 := ptrInt(7)
	w9 := ptrInt(9)
	provider := store.UpstreamProvider{
		ProviderType:            TypeClaudeAPIKey,
		ID:                      42,
		APIKey:                  "parent-fallback-secret",
		Priority:                5,
		Prefix:                  "teamA/",
		BaseURL:                 "https://claude.example",
		ProxyURL:                "http://provider-proxy",
		Headers:                 map[string]string{"X-Header": "row"},
		ExcludedModels:          []string{"row-excluded-model"},
		RebuildMidSystemMessage: true,
		ExperimentalCCHSigning:  true,
		ExtraConfig:             map[string]any{"disable_cooling": true},
		CloakMode:               "row-mode",
		CloakStrictMode:         true,
		CloakSensitiveWords:     []string{"row-secret-word"},
		Models: []store.UpstreamProviderModel{{
			Name:        "claude-3",
			Alias:       "alias-3",
			DisplayName: "Claude 3",
		}},
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{
				ID:         7,
				Name:       "entry-a",
				APIKey:     "entry-a-secret",
				ProxyURL:   "http://entry-a-proxy",
				Weight:     w7,
				SortOrder:  0,
				ProviderID: 42,
			},
			{
				ID:         9,
				Name:       "entry-b",
				APIKey:     "entry-b-secret",
				ProxyURL:   "", // blank → should inherit provider proxy
				Weight:     w9,
				SortOrder:  1,
				ProviderID: 42,
			},
		},
	}

	keys := claudeKeyFromProvider(provider)
	if len(keys) != 2 {
		t.Fatalf("rendered %d claude keys, want 2", len(keys))
	}

	// First entry: proxy override, weight 7, entry ID 7, parent ID 42.
	first := keys[0]
	if first.APIKey != "entry-a-secret" {
		t.Fatalf("entry 0 api key not copied: got %q", first.APIKey)
	}
	if first.ProxyURL != "http://entry-a-proxy" {
		t.Fatalf("entry 0 proxy should override provider proxy, got %q", first.ProxyURL)
	}
	if first.UpstreamProviderID != 42 || first.UpstreamProviderEntryID != 7 {
		t.Fatalf("entry 0 ids = (parent=%d, entry=%d), want (42, 7)", first.UpstreamProviderID, first.UpstreamProviderEntryID)
	}
	if first.Weight == nil || *first.Weight != 7 {
		t.Fatalf("entry 0 weight = %v, want 7", first.Weight)
	}

	// Second entry: blank proxy → inherits provider proxy; entry ID 9.
	second := keys[1]
	if second.APIKey != "entry-b-secret" {
		t.Fatalf("entry 1 api key not copied: got %q", second.APIKey)
	}
	if second.ProxyURL != "http://provider-proxy" {
		t.Fatalf("entry 1 proxy should inherit provider proxy, got %q", second.ProxyURL)
	}
	if second.UpstreamProviderID != 42 || second.UpstreamProviderEntryID != 9 {
		t.Fatalf("entry 1 ids = (parent=%d, entry=%d), want (42, 9)", second.UpstreamProviderID, second.UpstreamProviderEntryID)
	}
	if second.Weight == nil || *second.Weight != 9 {
		t.Fatalf("entry 1 weight = %v, want 9", second.Weight)
	}

	// Row-level fields must be identical across both entries.
	for i, k := range keys {
		if k.Priority != 5 {
			t.Fatalf("entry %d priority = %d, want 5", i, k.Priority)
		}
		if k.Prefix != "teamA/" {
			t.Fatalf("entry %d prefix = %q, want %q", i, k.Prefix, "teamA/")
		}
		if k.BaseURL != "https://claude.example" {
			t.Fatalf("entry %d base url = %q, want provider base url", i, k.BaseURL)
		}
		if !k.RebuildMidSystemMessage || !k.ExperimentalCCHSigning {
			t.Fatalf("entry %d missing row-level toggles: %+v", i, k)
		}
		if !k.DisableCooling {
			t.Fatalf("entry %d disable_cooling toggle not propagated: %+v", i, k)
		}
		if len(k.Models) != 1 || k.Models[0].Name != "claude-3" {
			t.Fatalf("entry %d models not copied: %+v", i, k.Models)
		}
		if k.Headers["X-Header"] != "row" {
			t.Fatalf("entry %d headers not copied: %+v", i, k.Headers)
		}
		if len(k.ExcludedModels) != 1 || k.ExcludedModels[0] != "row-excluded-model" {
			t.Fatalf("entry %d excluded models not copied: %+v", i, k.ExcludedModels)
		}
		if k.Cloak == nil || k.Cloak.Mode != "row-mode" || !k.Cloak.StrictMode {
			t.Fatalf("entry %d cloak not copied: %+v", i, k.Cloak)
		}
	}
}

// TestClaudeRenderLegacyFallback verifies that a Claude provider without
// any child entries still produces exactly one key, using the parent
// APIKey and provider proxy, with entry ID zero (legacy behavior).
func TestClaudeRenderLegacyFallback(t *testing.T) {
	provider := store.UpstreamProvider{
		ProviderType: TypeClaudeAPIKey,
		ID:           99,
		APIKey:       "parent-secret",
		ProxyURL:     "http://provider-proxy",
		BaseURL:      "https://claude.example",
	}

	keys := claudeKeyFromProvider(provider)
	if len(keys) != 1 {
		t.Fatalf("rendered %d claude keys, want 1 (legacy fallback)", len(keys))
	}
	k := keys[0]
	if k.APIKey != "parent-secret" {
		t.Fatalf("legacy key api key = %q, want parent APIKey", k.APIKey)
	}
	if k.ProxyURL != "http://provider-proxy" {
		t.Fatalf("legacy key proxy = %q, want provider proxy", k.ProxyURL)
	}
	if k.UpstreamProviderID != 99 {
		t.Fatalf("legacy key parent id = %d, want 99", k.UpstreamProviderID)
	}
	if k.UpstreamProviderEntryID != 0 {
		t.Fatalf("legacy key entry id = %d, want 0", k.UpstreamProviderEntryID)
	}
	if k.Weight != nil {
		t.Fatalf("legacy key weight = %v, want nil", k.Weight)
	}
}

// TestRenderConfigAppendsClaudeFanOut verifies that the RenderConfig
// dispatcher expands the Claude slice returned by claudeKeyFromProvider.
func TestRenderConfigAppendsClaudeFanOut(t *testing.T) {
	providers := []store.UpstreamProvider{
		{
			ProviderType: TypeClaudeAPIKey,
			ID:           1,
			APIKey:       "parent-only-secret",
			APIKeyEntries: []store.UpstreamProviderAPIKey{
				{ID: 11, APIKey: "child-1-secret", Weight: ptrInt(2)},
				{ID: 12, APIKey: "child-2-secret", Weight: ptrInt(3)},
			},
		},
	}
	cfg := RenderConfig(providers)
	if len(cfg.ClaudeKey) != 2 {
		t.Fatalf("rendered %d claude keys, want 2", len(cfg.ClaudeKey))
	}
	if cfg.ClaudeKey[0].APIKey != "child-1-secret" || cfg.ClaudeKey[0].UpstreamProviderEntryID != 11 {
		t.Fatalf("first key = %+v", cfg.ClaudeKey[0])
	}
	if cfg.ClaudeKey[1].APIKey != "child-2-secret" || cfg.ClaudeKey[1].UpstreamProviderEntryID != 12 {
		t.Fatalf("second key = %+v", cfg.ClaudeKey[1])
	}
}

// TestOpenAICompatRenderPropagatesEntryWeight ensures OpenAI Compatibility
// renderer copies the store entry's Weight into the rendered config entry,
// matching the renderer seam used by Task 1's weight persistence.
func TestOpenAICompatRenderPropagatesEntryWeight(t *testing.T) {
	w := ptrInt(11)
	provider := store.UpstreamProvider{
		ProviderType: TypeOpenAICompatibility,
		ID:           8,
		Name:         "compat",
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{ID: 21, APIKey: "compat-entry-secret", Weight: w},
		},
	}
	got := openAICompatFromProvider(provider)
	if len(got.APIKeyEntries) != 1 {
		t.Fatalf("rendered %d entries, want 1", len(got.APIKeyEntries))
	}
	e := got.APIKeyEntries[0]
	if e.Weight == nil || *e.Weight != 11 {
		t.Fatalf("entry weight = %v, want 11", e.Weight)
	}
	if e.UpstreamProviderEntryID != 21 {
		t.Fatalf("entry id = %d, want 21", e.UpstreamProviderEntryID)
	}
	if e.APIKey != "compat-entry-secret" {
		t.Fatalf("entry api key not copied: got %q", e.APIKey)
	}
}

// TestProviderFromClaudeKeyPreservesEntryRowMetadata verifies that an
// individual Claude config item converted into a store UpstreamProvider
// keeps its scalar row-level fields. The seed path deliberately drops
// UpstreamProviderID, UpstreamProviderEntryID, and Weight: the store
// allocates fresh IDs on Create and the validator rejects positive
// child IDs that do not belong to the freshly minted parent. These
// fields are runtime renderer/synthesizer metadata and are re-stamped
// on every reload — see the implementation comment in providerFromClaudeKey.
func TestProviderFromClaudeKeyPreservesEntryRowMetadata(t *testing.T) {
	w := ptrInt(5)
	ck := config.ClaudeKey{
		APIKey:                  "rendered-entry-secret",
		Weight:                  w,
		ProxyURL:                "http://entry-proxy",
		Priority:                1,
		Prefix:                  "x/",
		BaseURL:                 "https://claude.example",
		UpstreamProviderID:      42,
		UpstreamProviderEntryID: 7,
	}

	p := providerFromClaudeKey(ck)
	if p.APIKey != "rendered-entry-secret" {
		t.Fatalf("seed api key not copied: %q", p.APIKey)
	}
	if p.ProxyURL != "http://entry-proxy" {
		t.Fatalf("seed proxy not copied: %q", p.ProxyURL)
	}
	if p.Priority != 1 || p.Prefix != "x/" || p.BaseURL != "https://claude.example" {
		t.Fatalf("seed row-level fields not copied: %+v", p)
	}
	if len(p.APIKeyEntries) != 0 {
		t.Fatalf("seed must not fabricate child entries: got %d", len(p.APIKeyEntries))
	}
}

// TestProviderFromClaudeKeyZeroEntryIDDropsWeight ensures manual YAML
// without a child entry ID does NOT fabricate a store-side entry weight
// or child row: the legacy seed path stays "one parent row, no entries".
func TestProviderFromClaudeKeyZeroEntryIDDropsWeight(t *testing.T) {
	w := ptrInt(9)
	ck := config.ClaudeKey{
		APIKey: "yaml-secret",
		Weight: w,
	}

	p := providerFromClaudeKey(ck)
	if p.APIKey != "yaml-secret" {
		t.Fatalf("legacy seed api key not copied: %q", p.APIKey)
	}
	if len(p.APIKeyEntries) != 0 {
		t.Fatalf("legacy seed should not fabricate child entries: got %d", len(p.APIKeyEntries))
	}
}

// TestProviderFromOpenAICompatPropagatesEntryWeight ensures OpenAI entry
// weight flows into the store row used for first-boot seeding.
func TestProviderFromOpenAICompatPropagatesEntryWeight(t *testing.T) {
	w := ptrInt(13)
	compat := config.OpenAICompatibility{
		Name: "team-openai",
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
			Name:                    "primary",
			APIKey:                  "compat-secret",
			UpstreamProviderEntryID: 99,
			Weight:                  w,
		}},
	}

	p := providerFromOpenAICompat(compat)
	if len(p.APIKeyEntries) != 1 {
		t.Fatalf("seed produced %d entries, want 1", len(p.APIKeyEntries))
	}
	e := p.APIKeyEntries[0]
	if e.Weight == nil || *e.Weight != 13 {
		t.Fatalf("seed entry weight = %v, want 13", e.Weight)
	}
	if e.ID != 0 {
		t.Fatalf("seed entry id = %d, want 0 (Create allocates a fresh parent; child IDs are runtime metadata only)", e.ID)
	}
	if e.APIKey != "compat-secret" {
		t.Fatalf("seed entry api key not copied: %q", e.APIKey)
	}
	if e.Name != "primary" {
		t.Fatalf("seed entry name not copied: %q", e.Name)
	}
}
