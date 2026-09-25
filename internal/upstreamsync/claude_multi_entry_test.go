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

// TestRenderClaudePoolStrategyAndEntryPriority verifies the Claude fan-out
// carries the pool routing strategy (canonicalized from the raw store value)
// onto every rendered item, and that a per-entry priority pointer overrides
// the row-level default while nil inherits it.
func TestRenderClaudePoolStrategyAndEntryPriority(t *testing.T) {
	prio10, prio5 := 10, 5
	cfg := RenderConfig([]store.UpstreamProvider{{
		ID:              7,
		ProviderType:    TypeClaudeAPIKey,
		Name:            "pool",
		RoutingStrategy: "failover", // raw value in store — canonicalized during render
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{ID: 71, APIKey: "k1", Priority: &prio10},
			{ID: 72, APIKey: "k2", Priority: &prio5},
			{ID: 73, APIKey: "k3"}, // nil priority = inherit row-level
		},
	}})
	if len(cfg.ClaudeKey) != 3 {
		t.Fatalf("ClaudeKey items = %d, want 3", len(cfg.ClaudeKey))
	}
	if cfg.ClaudeKey[0].UpstreamProviderStrategy != "round-robin" {
		t.Fatalf("strategy carry-through = %q, want round-robin (failover canonicalized)", cfg.ClaudeKey[0].UpstreamProviderStrategy)
	}
	if cfg.ClaudeKey[0].Priority != 10 || cfg.ClaudeKey[1].Priority != 5 {
		t.Fatalf("entry priorities = %d,%d, want 10,5", cfg.ClaudeKey[0].Priority, cfg.ClaudeKey[1].Priority)
	}
	if cfg.ClaudeKey[2].Priority != 0 {
		t.Fatalf("inherit priority = %d, want row default 0", cfg.ClaudeKey[2].Priority)
	}
}

// TestRenderClaudePoolStrategyUnsetAndLegacyFallback covers the two strategy
// edges the fan-out test does not: an unset row strategy renders as an empty
// (unset) item strategy — empty means "follow the global routing strategy",
// never a forced default — and the legacy single-key fallback shares
// buildClaudeKey, so it carries the strategy too.
func TestRenderClaudePoolStrategyUnsetAndLegacyFallback(t *testing.T) {
	cfg := RenderConfig([]store.UpstreamProvider{
		{
			ID:              7,
			ProviderType:    TypeClaudeAPIKey,
			RoutingStrategy: "", // unset → items follow the global routing strategy
			APIKeyEntries: []store.UpstreamProviderAPIKey{
				{ID: 71, APIKey: "k1"},
			},
		},
		{
			ID:              8,
			ProviderType:    TypeClaudeAPIKey,
			RoutingStrategy: "fill-first",
			APIKey:          "legacy-single-secret",
			// no entries → legacy single-key fallback path
		},
	})
	if len(cfg.ClaudeKey) != 2 {
		t.Fatalf("ClaudeKey items = %d, want 2", len(cfg.ClaudeKey))
	}
	if cfg.ClaudeKey[0].UpstreamProviderStrategy != "" {
		t.Fatalf("unset row strategy rendered as %q, want empty", cfg.ClaudeKey[0].UpstreamProviderStrategy)
	}
	if cfg.ClaudeKey[1].UpstreamProviderStrategy != "fill-first" {
		t.Fatalf("legacy fallback strategy = %q, want fill-first (buildClaudeKey is shared)", cfg.ClaudeKey[1].UpstreamProviderStrategy)
	}
	if cfg.ClaudeKey[1].APIKey != "legacy-single-secret" {
		t.Fatalf("legacy fallback api key = %q, want parent key", cfg.ClaudeKey[1].APIKey)
	}
}

// TestRenderOpenAICompatPoolStrategyAndEntryPriority verifies the OpenAI
// compat pool renders the row strategy (canonicalized) onto the pool-level
// Strategy field and maps each entry's priority pointer verbatim, so nil
// stays nil and means "inherit the pool Priority".
func TestRenderOpenAICompatPoolStrategyAndEntryPriority(t *testing.T) {
	prio10 := 10
	cfg := RenderConfig([]store.UpstreamProvider{{
		ID:              8,
		ProviderType:    TypeOpenAICompatibility,
		Name:            "pool",
		RoutingStrategy: "priority", // raw alias from store — canonicalized in render
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{ID: 81, APIKey: "k1", Priority: &prio10},
			{ID: 82, APIKey: "k2"}, // inherit → pool Priority (0 when unset)
		},
	}})
	if len(cfg.OpenAICompatibility) != 1 || cfg.OpenAICompatibility[0].Strategy != "fill-first" {
		t.Fatalf("pool strategy = %#v, want fill-first", cfg.OpenAICompatibility)
	}
	entries := cfg.OpenAICompatibility[0].APIKeyEntries
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if entries[0].Priority == nil || *entries[0].Priority != 10 {
		t.Fatalf("entry 0 priority = %#v, want *10", entries[0].Priority)
	}
	if entries[1].Priority != nil {
		t.Fatalf("entry 1 priority = %#v, want nil (inherit pool priority)", entries[1].Priority)
	}
}

// TestSeedInverseCarriesStrategyAndEntryPriority verifies the seed direction
// mirrors the render direction: pool strategies normalize back onto the row
// and per-entry priority pointers survive the round trip. The Claude seed
// drops the runtime IDs (see providerFromClaudeKey) but keeps the strategy —
// unlike the IDs, it is row-level data with a real column to land in.
func TestSeedInverseCarriesStrategyAndEntryPriority(t *testing.T) {
	prio10 := 10
	// Claude: UpstreamProviderStrategy → RoutingStrategy (normalized), IDs dropped per seed semantics
	p := providerFromClaudeKey(config.ClaudeKey{APIKey: "k", Priority: 3, UpstreamProviderStrategy: "fill-first"})
	if p.RoutingStrategy != "fill-first" {
		t.Fatalf("claude seed strategy = %q, want fill-first", p.RoutingStrategy)
	}
	// OpenAI-compat: Strategy → RoutingStrategy (normalized), entry Priority carried
	p2 := providerFromOpenAICompat(config.OpenAICompatibility{
		Name: "pool", Strategy: "round-robin",
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k1", Priority: &prio10}, {APIKey: "k2"}},
	})
	if p2.RoutingStrategy != "round-robin" {
		t.Fatalf("openai seed strategy = %q, want round-robin", p2.RoutingStrategy)
	}
	if p2.APIKeyEntries[0].Priority == nil || *p2.APIKeyEntries[0].Priority != 10 {
		t.Fatal("openai seed entry priority not carried")
	}
	if p2.APIKeyEntries[1].Priority != nil {
		t.Fatal("openai seed entry 1 priority = not nil, want nil")
	}
	// Seed-side canonicalization mirrors the render side: a raw alias written
	// in YAML normalizes to its canonical spelling on the way into the row.
	p3 := providerFromClaudeKey(config.ClaudeKey{APIKey: "k", UpstreamProviderStrategy: "failover"})
	if p3.RoutingStrategy != "round-robin" {
		t.Fatalf("claude seed alias strategy = %q, want round-robin (normalized)", p3.RoutingStrategy)
	}
}

// TestRenderCircuitBreakerCarryThrough verifies the row-level circuit_breaker
// opt-in (design G3) renders onto every fan-out item — the Claude
// UpstreamProviderCircuitBreaker carry-through, the OpenAI-compat and
// opencode-go CircuitBreaker fields — and that a row without the opt-in
// renders it false on all paths, including the Claude legacy single-key
// fallback (buildClaudeKey is shared).
func TestRenderCircuitBreakerCarryThrough(t *testing.T) {
	cfg := RenderConfig([]store.UpstreamProvider{
		{
			ID:             7,
			ProviderType:   TypeClaudeAPIKey,
			Name:           "pooled",
			CircuitBreaker: true,
			APIKeyEntries: []store.UpstreamProviderAPIKey{
				{ID: 71, APIKey: "k1"},
				{ID: 72, APIKey: "k2"},
			},
		},
		{
			ID:           8,
			ProviderType: TypeClaudeAPIKey,
			Name:         "legacy",
			APIKey:       "legacy-single-secret",
			// no entries → legacy single-key fallback path
		},
		{
			ID:             9,
			ProviderType:   TypeOpenAICompatibility,
			Name:           "compat-pool",
			BaseURL:        "https://compat.example.com/v1",
			CircuitBreaker: true,
			APIKeyEntries: []store.UpstreamProviderAPIKey{
				{ID: 91, APIKey: "ck1"},
			},
		},
		{
			ID:             10,
			ProviderType:   TypeOpenCodeGo,
			Name:           "ocgo-pool",
			BaseURL:        "https://opencode.ai/zen/go/v1",
			CircuitBreaker: true,
			APIKeyEntries: []store.UpstreamProviderAPIKey{
				{ID: 101, APIKey: "ok1"},
			},
		},
	})
	if len(cfg.ClaudeKey) != 3 {
		t.Fatalf("ClaudeKey items = %d, want 3", len(cfg.ClaudeKey))
	}
	for i := 0; i < 2; i++ {
		if !cfg.ClaudeKey[i].UpstreamProviderCircuitBreaker {
			t.Fatalf("ClaudeKey[%d].UpstreamProviderCircuitBreaker = false, want true (fan-out item)", i)
		}
	}
	if cfg.ClaudeKey[2].UpstreamProviderCircuitBreaker {
		t.Fatal("legacy fallback UpstreamProviderCircuitBreaker = true, want false (row opted out)")
	}
	if len(cfg.OpenAICompatibility) != 1 || !cfg.OpenAICompatibility[0].CircuitBreaker {
		t.Fatalf("OpenAICompatibility CircuitBreaker not carried: %+v", cfg.OpenAICompatibility)
	}
	if len(cfg.OpenCodeGo) != 1 || !cfg.OpenCodeGo[0].CircuitBreaker {
		t.Fatalf("OpenCodeGo CircuitBreaker not carried: %+v", cfg.OpenCodeGo)
	}
}

// TestSeedInverseCarriesCircuitBreaker verifies the seed direction round-trips
// the opt-in: the Claude UpstreamProviderCircuitBreaker carry-through and the
// OpenAI-compat CircuitBreaker field land on the row's circuit_breaker column.
func TestSeedInverseCarriesCircuitBreaker(t *testing.T) {
	p := providerFromClaudeKey(config.ClaudeKey{APIKey: "k", UpstreamProviderCircuitBreaker: true})
	if !p.CircuitBreaker {
		t.Fatal("claude seed circuit_breaker not carried")
	}
	p2 := providerFromOpenAICompat(config.OpenAICompatibility{
		Name: "pool", CircuitBreaker: true,
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k1"}},
	})
	if !p2.CircuitBreaker {
		t.Fatal("openai seed circuit_breaker not carried")
	}
	// Default off stays off through the seed.
	p3 := providerFromClaudeKey(config.ClaudeKey{APIKey: "k"})
	if p3.CircuitBreaker {
		t.Fatal("claude seed default circuit_breaker = true, want false")
	}
}

// TestRenderSkipsDisabledEntries verifies that entries with Disabled=true
// stay persisted but never reach the rendered config: the OpenAI-compat
// entries list and the Claude fan-out both drop them, while active
// siblings render unchanged.
func TestRenderSkipsDisabledEntries(t *testing.T) {
	provider := store.UpstreamProvider{
		ProviderType: TypeOpenAICompatibility,
		ID:           9,
		Name:         "compat",
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{ID: 31, APIKey: "compat-live-secret", Name: "live"},
			{ID: 32, APIKey: "compat-off-secret", Name: "off", Disabled: true},
		},
	}
	got := openAICompatFromProvider(provider)
	if len(got.APIKeyEntries) != 1 {
		t.Fatalf("rendered %d entries, want 1 (disabled entry skipped)", len(got.APIKeyEntries))
	}
	if got.APIKeyEntries[0].APIKey != "compat-live-secret" {
		t.Fatalf("wrong entry rendered: %q", got.APIKeyEntries[0].APIKey)
	}

	claude := store.UpstreamProvider{
		ProviderType: TypeClaudeAPIKey,
		ID:           10,
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{ID: 41, APIKey: "claude-live-secret", Name: "live"},
			{ID: 42, APIKey: "claude-off-secret", Name: "off", Disabled: true},
		},
	}
	keys := claudeKeyFromProvider(claude)
	if len(keys) != 1 {
		t.Fatalf("rendered %d claude keys, want 1 (disabled entry skipped)", len(keys))
	}
	if keys[0].APIKey != "claude-live-secret" {
		t.Fatalf("wrong claude key rendered: %q", keys[0].APIKey)
	}
}

// TestRenderClaudePerEntryMaxConcurrentAndAutoDisable verifies the Claude
// fan-out copies each entry's per-entry concurrency fields
// (MaxConcurrent/MaxWaitMs) verbatim — nil stays nil (unlimited) — and
// stamps the provider's auto-disable fields onto every rendered item.
func TestRenderClaudePerEntryMaxConcurrentAndAutoDisable(t *testing.T) {
	mc, mw := ptrInt(3), ptrInt(1500)
	cd := 3600
	provider := store.UpstreamProvider{
		ProviderType:               TypeClaudeAPIKey,
		ID:                         42,
		AutoDisableErrorCodes:      []string{"401", "account_suspended"},
		AutoDisableCooldownSeconds: &cd,
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{ID: 7, APIKey: "entry-a-secret", MaxConcurrent: mc, MaxWaitMs: mw},
			{ID: 9, APIKey: "entry-b-secret"}, // nil/0 → unlimited, stays nil
		},
	}
	keys := claudeKeyFromProvider(provider)
	if len(keys) != 2 {
		t.Fatalf("rendered %d claude keys, want 2", len(keys))
	}
	if keys[0].MaxConcurrent == nil || *keys[0].MaxConcurrent != 3 {
		t.Fatalf("entry 0 MaxConcurrent = %v, want 3", keys[0].MaxConcurrent)
	}
	if keys[0].MaxWaitMs == nil || *keys[0].MaxWaitMs != 1500 {
		t.Fatalf("entry 0 MaxWaitMs = %v, want 1500", keys[0].MaxWaitMs)
	}
	if keys[1].MaxConcurrent != nil {
		t.Fatalf("entry 1 MaxConcurrent = %v, want nil (unlimited)", keys[1].MaxConcurrent)
	}
	if keys[1].MaxWaitMs != nil {
		t.Fatalf("entry 1 MaxWaitMs = %v, want nil", keys[1].MaxWaitMs)
	}
	for i, k := range keys {
		if len(k.AutoDisableErrorCodes) != 2 || k.AutoDisableErrorCodes[0] != "401" {
			t.Fatalf("item %d auto-disable codes = %v, want [401 account_suspended]", i, k.AutoDisableErrorCodes)
		}
		if k.AutoDisableCooldownSeconds == nil || *k.AutoDisableCooldownSeconds != 3600 {
			t.Fatalf("item %d cooldown = %v, want 3600", i, k.AutoDisableCooldownSeconds)
		}
	}
}

// TestRenderOpenAICompatPerEntryMaxConcurrentAndAutoDisable covers the same
// carry-through on the OpenAI-compat pool: entry concurrency fields land on
// the entry item, and the provider auto-disable fields land on both the
// pool item and every entry item.
func TestRenderOpenAICompatPerEntryMaxConcurrentAndAutoDisable(t *testing.T) {
	mc, mw := ptrInt(5), ptrInt(800)
	cd := 120
	provider := store.UpstreamProvider{
		ProviderType:               TypeOpenAICompatibility,
		ID:                         8,
		Name:                       "compat",
		AutoDisableErrorCodes:      []string{"429", "overload"},
		AutoDisableCooldownSeconds: &cd,
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{ID: 21, APIKey: "compat-secret", MaxConcurrent: mc, MaxWaitMs: mw},
		},
	}
	got := openAICompatFromProvider(provider)
	if len(got.APIKeyEntries) != 1 {
		t.Fatalf("rendered %d entries, want 1", len(got.APIKeyEntries))
	}
	if len(got.AutoDisableErrorCodes) != 2 || got.AutoDisableErrorCodes[0] != "429" {
		t.Fatalf("pool auto-disable codes = %v, want [429 overload]", got.AutoDisableErrorCodes)
	}
	if got.AutoDisableCooldownSeconds == nil || *got.AutoDisableCooldownSeconds != 120 {
		t.Fatalf("pool cooldown = %v, want 120", got.AutoDisableCooldownSeconds)
	}
	e := got.APIKeyEntries[0]
	if e.MaxConcurrent == nil || *e.MaxConcurrent != 5 {
		t.Fatalf("entry MaxConcurrent = %v, want 5", e.MaxConcurrent)
	}
	if e.MaxWaitMs == nil || *e.MaxWaitMs != 800 {
		t.Fatalf("entry MaxWaitMs = %v, want 800", e.MaxWaitMs)
	}
	if len(e.AutoDisableErrorCodes) != 2 || e.AutoDisableErrorCodes[0] != "429" {
		t.Fatalf("entry auto-disable codes = %v, want [429 overload]", e.AutoDisableErrorCodes)
	}
	if e.AutoDisableCooldownSeconds == nil || *e.AutoDisableCooldownSeconds != 120 {
		t.Fatalf("entry cooldown = %v, want 120", e.AutoDisableCooldownSeconds)
	}
}

// TestRenderOpenCodeGoPerEntryMaxConcurrentAndAutoDisable covers the
// opencode-go path, which mirrors openAICompatFromProviderWithPools
// field-for-field.
func TestRenderOpenCodeGoPerEntryMaxConcurrentAndAutoDisable(t *testing.T) {
	mc, mw := ptrInt(2), ptrInt(400)
	cd := 60
	provider := store.UpstreamProvider{
		ProviderType:               TypeOpenCodeGo,
		ID:                         12,
		Name:                       "ocgo",
		AutoDisableErrorCodes:      []string{"rate_limit"},
		AutoDisableCooldownSeconds: &cd,
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{ID: 31, APIKey: "ocgo-secret", MaxConcurrent: mc, MaxWaitMs: mw},
		},
	}
	cfg := RenderConfig([]store.UpstreamProvider{provider})
	if len(cfg.OpenCodeGo) != 1 {
		t.Fatalf("want 1 opencode-go row, got %d", len(cfg.OpenCodeGo))
	}
	row := cfg.OpenCodeGo[0]
	if len(row.AutoDisableErrorCodes) != 1 || row.AutoDisableErrorCodes[0] != "rate_limit" {
		t.Fatalf("row auto-disable codes = %v, want [rate_limit]", row.AutoDisableErrorCodes)
	}
	if row.AutoDisableCooldownSeconds == nil || *row.AutoDisableCooldownSeconds != 60 {
		t.Fatalf("row cooldown = %v, want 60", row.AutoDisableCooldownSeconds)
	}
	if len(row.APIKeyEntries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(row.APIKeyEntries))
	}
	e := row.APIKeyEntries[0]
	if e.MaxConcurrent == nil || *e.MaxConcurrent != 2 {
		t.Fatalf("entry MaxConcurrent = %v, want 2", e.MaxConcurrent)
	}
	if e.MaxWaitMs == nil || *e.MaxWaitMs != 400 {
		t.Fatalf("entry MaxWaitMs = %v, want 400", e.MaxWaitMs)
	}
	if len(e.AutoDisableErrorCodes) != 1 || e.AutoDisableErrorCodes[0] != "rate_limit" {
		t.Fatalf("entry auto-disable codes = %v, want [rate_limit]", e.AutoDisableErrorCodes)
	}
	if e.AutoDisableCooldownSeconds == nil || *e.AutoDisableCooldownSeconds != 60 {
		t.Fatalf("entry cooldown = %v, want 60", e.AutoDisableCooldownSeconds)
	}
}

// TestRenderSkipsAutoDisabledEntries is the no-regression anchor for the
// auto-disable persistence design (D1): the sink persists auto-disabled
// entries as PG disabled=true (+ auto_disabled flag), so the renderer must
// keep skipping them — both system-auto-disabled and operator-disabled
// entries stay out of config.yaml while active siblings render unchanged.
func TestRenderSkipsAutoDisabledEntries(t *testing.T) {
	provider := store.UpstreamProvider{
		ProviderType: TypeClaudeAPIKey,
		ID:           10,
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{ID: 41, APIKey: "claude-live-secret", Name: "live"},
			{ID: 42, APIKey: "claude-auto-off-secret", Name: "auto-off", Disabled: true, AutoDisabled: true},
			{ID: 43, APIKey: "claude-manual-off-secret", Name: "manual-off", Disabled: true},
		},
	}
	keys := claudeKeyFromProvider(provider)
	if len(keys) != 1 {
		t.Fatalf("rendered %d claude keys, want 1 (disabled entries skipped)", len(keys))
	}
	if keys[0].APIKey != "claude-live-secret" {
		t.Fatalf("wrong claude key rendered: %q", keys[0].APIKey)
	}
}
