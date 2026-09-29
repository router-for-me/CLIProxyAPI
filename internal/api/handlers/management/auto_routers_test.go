package management

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// TestValidateTierMappingsSingleModel keeps the legacy single-model validation
// behaviour: a mapped tier requires a non-empty model, and priorities must
// reference a pinned provider.
func TestValidateTierMappingsSingleModel(t *testing.T) {
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "simple", Model: "gpt-4o-mini"},
	}); msg != "" {
		t.Fatalf("valid single-model mapping rejected: %s", msg)
	}
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "complex", Model: ""},
	}); msg == "" {
		t.Fatal("expected empty-model mapping to be rejected")
	}
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "complex", Model: "claude-sonnet-4-5", Priorities: []store.ProviderPriority{{Provider: "openai", Priority: 5}}},
	}); msg == "" {
		t.Fatal("expected priority referencing an unpinned provider to be rejected")
	}
}

// TestValidateTierMappingsMultiTarget verifies the multi-target form: a tier
// with a non-empty Targets list resolves without a Model, every target must
// carry a model, and the target strategy must be supported.
func TestValidateTierMappingsMultiTarget(t *testing.T) {
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "complex", Targets: []store.TierTarget{{Model: "claude-sonnet-4-5", Weight: 7}, {Model: "gpt-4o", Weight: 3}}, TargetStrategy: "weighted"},
	}); msg != "" {
		t.Fatalf("valid multi-target mapping rejected: %s", msg)
	}
	// Default strategy (empty) is allowed.
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "complex", Targets: []store.TierTarget{{Model: "claude-sonnet-4-5"}}},
	}); msg != "" {
		t.Fatalf("default strategy rejected: %s", msg)
	}
	// A target with a blank model is rejected.
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "complex", Targets: []store.TierTarget{{Model: ""}}},
	}); msg == "" {
		t.Fatal("expected blank target model to be rejected")
	}
	// A tier with neither Model nor Targets is rejected.
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "simple"},
	}); msg == "" {
		t.Fatal("expected tier without any target to be rejected")
	}
	// Unknown target strategies are rejected.
	for _, s := range []string{"round-robin", "random", "bogus"} {
		if msg := validateTierMappings([]store.TierMapping{
			{Tier: "simple", Targets: []store.TierTarget{{Model: "gpt-4o-mini"}}, TargetStrategy: s},
		}); msg == "" {
			t.Fatalf("expected target strategy %q to be rejected", s)
		}
	}
}

// TestValidateTierMappingsTargetRouting verifies per-target routing rules: the
// target strategy must be a supported value, and a target priority must
// reference one of the target's pinned providers.
func TestValidateTierMappingsTargetRouting(t *testing.T) {
	// Valid per-target routing (providers + strategy + priorities).
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "complex", Targets: []store.TierTarget{{
			Model:      "claude-sonnet-4-5",
			Providers:  []string{"anthropic", "openai"},
			Strategy:   "failover",
			Priorities: []store.ProviderPriority{{Provider: "anthropic", Priority: 10}},
		}}},
	}); msg != "" {
		t.Fatalf("valid per-target routing rejected: %s", msg)
	}
	// Unknown per-target strategy is rejected.
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "complex", Targets: []store.TierTarget{{Model: "claude-sonnet-4-5", Strategy: "round-robin"}}},
	}); msg == "" {
		t.Fatal("expected unknown per-target strategy to be rejected")
	}
	// A target priority referencing an unpinned provider is rejected.
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "complex", Targets: []store.TierTarget{{
			Model:      "claude-sonnet-4-5",
			Providers:  []string{"anthropic"},
			Priorities: []store.ProviderPriority{{Provider: "openai", Priority: 5}},
		}}},
	}); msg == "" {
		t.Fatal("expected per-target priority on an unpinned provider to be rejected")
	}
}

// TestValidateTierMappingsDuplicateTargets guards against a regression where a
// single tier could list the same target model more than once. Without the
// guard, the weighted selection silently collapses the two entries into one
// effective weight (e.g. gpt-4o x2 weight 5+3 → only gpt-4o ever picked),
// misleading operators about their distribution.
func TestValidateTierMappingsDuplicateTargets(t *testing.T) {
	if msg := validateTierMappings([]store.TierMapping{
		{Tier: "complex", Targets: []store.TierTarget{
			{Model: "gpt-4o", Weight: 5},
			{Model: "gpt-4o", Weight: 3},
		}},
	}); msg == "" {
		t.Fatal("expected duplicate target models to be rejected")
	}
}

// Regression: the four classifier knobs must survive the request DTO. They were
// dropped here once — the store and the gate both handled them, but the
// management DTO never carried them, so a router created with jev_enabled:true
// came back with the classifier off and no error to explain it.
func TestDecodeAutoRouterJevFields(t *testing.T) {
	body := `{
		"name": "r1", "model_id": "router:r1", "jev_enabled": true,
		"jev_min_confidence": 0.65, "jev_timeout_ms": 250,
		"jev_model_override": "jev-preview"
	}`
	var req createAutoRouterRequest
	if errDecode := json.Unmarshal([]byte(body), &req); errDecode != nil {
		t.Fatalf("decode create request: %v", errDecode)
	}
	if !req.JevEnabled {
		t.Error("jev_enabled must decode true")
	}
	if req.JevMinConfidence != 0.65 {
		t.Errorf("jev_min_confidence = %v, want 0.65", req.JevMinConfidence)
	}
	if req.JevTimeoutMs != 250 {
		t.Errorf("jev_timeout_ms = %d, want 250", req.JevTimeoutMs)
	}
	if req.JevModelOverride != "jev-preview" {
		t.Errorf("jev_model_override = %q", req.JevModelOverride)
	}
}

// An update that omits the knobs must leave them untouched (nil), while an
// explicit false/0 must be distinguishable from absent so the operator can turn
// the classifier off without clearing their other settings.
func TestDecodeAutoRouterJevUpdateDistinguishesAbsentFromZero(t *testing.T) {
	var absent updateAutoRouterRequest
	if errDecode := json.Unmarshal([]byte(`{"name":"r1"}`), &absent); errDecode != nil {
		t.Fatalf("decode absent: %v", errDecode)
	}
	if absent.JevEnabled != nil || absent.JevMinConfidence != nil ||
		absent.JevTimeoutMs != nil || absent.JevModelOverride != nil {
		t.Errorf("absent knobs must stay nil (preserve), got %+v", absent)
	}

	var explicit updateAutoRouterRequest
	if errDecode := json.Unmarshal([]byte(`{"jev_enabled":false,"jev_min_confidence":0}`), &explicit); errDecode != nil {
		t.Fatalf("decode explicit: %v", errDecode)
	}
	if explicit.JevEnabled == nil || *explicit.JevEnabled {
		t.Error("explicit jev_enabled:false must decode to a non-nil false")
	}
	if explicit.JevMinConfidence == nil || *explicit.JevMinConfidence != 0 {
		t.Error("explicit jev_min_confidence:0 must decode to a non-nil zero")
	}
}

// TestDecodeAutoRouterVisionBridgeFields asserts the create-side DTO carries
// the per-router vision bridge routing fields (providers/strategy/priorities).
// Mirrors the Jev decode test for the same reason: regression coverage for
// "the store and runtime both handle them, but the DTO never carried them so
// the operator's settings silently dropped on save".
func TestDecodeAutoRouterVisionBridgeFields(t *testing.T) {
	body := `{
		"name": "r1", "model_id": "router:r1",
		"vision_bridge_model": "gemma-4-31b",
		"vision_bridge_providers": ["opencode", "zai"],
		"vision_bridge_strategy": "priority",
		"vision_bridge_priorities": [
			{"provider": "opencode", "priority": 10},
			{"provider": "zai",     "priority": 5}
		]
	}`
	var req createAutoRouterRequest
	if errDecode := json.Unmarshal([]byte(body), &req); errDecode != nil {
		t.Fatalf("decode create request: %v", errDecode)
	}
	if req.VisionBridgeModel != "gemma-4-31b" {
		t.Errorf("vision_bridge_model = %q", req.VisionBridgeModel)
	}
	if len(req.VisionBridgeProviders) != 2 || req.VisionBridgeProviders[0] != "opencode" {
		t.Errorf("vision_bridge_providers = %v", req.VisionBridgeProviders)
	}
	if req.VisionBridgeStrategy != "priority" {
		t.Errorf("vision_bridge_strategy = %q", req.VisionBridgeStrategy)
	}
	if len(req.VisionBridgePriorities) != 2 || req.VisionBridgePriorities[0].Provider != "opencode" || req.VisionBridgePriorities[0].Priority != 10 {
		t.Errorf("vision_bridge_priorities = %+v", req.VisionBridgePriorities)
	}
}

// TestDecodeAutoRouterVisionBridgeUpdateDistinguishesAbsentFromZero mirrors
// the Jev partial-update test for the bridge fields: a request that omits
// them must keep them nil/empty (preserve), while an explicit empty slice /
// explicit "" strategy must be distinguishable so the operator can clear the
// pin to auto-discover.
func TestDecodeAutoRouterVisionBridgeUpdateDistinguishesAbsentFromZero(t *testing.T) {
	var absent updateAutoRouterRequest
	if errDecode := json.Unmarshal([]byte(`{"name":"r1"}`), &absent); errDecode != nil {
		t.Fatalf("decode absent: %v", errDecode)
	}
	if absent.VisionBridgeProviders != nil {
		t.Errorf("absent vision_bridge_providers must stay nil (preserve), got %+v", absent.VisionBridgeProviders)
	}
	if absent.VisionBridgeStrategy != nil {
		t.Errorf("absent vision_bridge_strategy must stay nil (preserve), got %+v", *absent.VisionBridgeStrategy)
	}

	var explicit updateAutoRouterRequest
	if errDecode := json.Unmarshal([]byte(`{"vision_bridge_providers":[],"vision_bridge_strategy":""}`), &explicit); errDecode != nil {
		t.Fatalf("decode explicit: %v", errDecode)
	}
	if explicit.VisionBridgeProviders == nil {
		t.Error("explicit vision_bridge_providers:[] must decode to a non-nil empty slice (clear)")
	}
	if explicit.VisionBridgeStrategy == nil || *explicit.VisionBridgeStrategy != "" {
		t.Error("explicit vision_bridge_strategy:\"\" must decode to a non-nil empty string (clear)")
	}
}

// TestValidateVisionBridgeRoute exercises the strategy/pin invariants.
func TestValidateVisionBridgeRoute(t *testing.T) {
	// Default (empty strategy, no priorities) is allowed.
	if msg := validateVisionBridgeRoute([]string{"opencode"}, "", nil); msg != "" {
		t.Errorf("empty strategy rejected: %s", msg)
	}
	// failover and priority and weighted are allowed.
	for _, s := range []string{"priority", "failover", "weighted"} {
		var msg string
		if s == "weighted" {
			msg = validateVisionBridgeRoute([]string{"opencode"}, s, []store.ProviderPriority{{Provider: "opencode", Priority: 5}})
		} else {
			msg = validateVisionBridgeRoute([]string{"opencode"}, s, nil)
		}
		if msg != "" {
			t.Errorf("%s strategy rejected: %s", s, msg)
		}
	}
	// Unknown strategy is rejected.
	if msg := validateVisionBridgeRoute([]string{"opencode"}, "round-robin", nil); msg == "" {
		t.Error("unknown strategy must be rejected")
	}
	// Weighted without priorities is rejected (each provider must have a weight).
	if msg := validateVisionBridgeRoute([]string{"opencode"}, "weighted", nil); msg == "" {
		t.Error("weighted without priorities must be rejected")
	}
	// Priority referencing an unpinned provider is rejected.
	if msg := validateVisionBridgeRoute([]string{"opencode"}, "priority",
		[]store.ProviderPriority{{Provider: "zai", Priority: 5}}); msg == "" {
		t.Error("priority on unpinned provider must be rejected")
	}
}
