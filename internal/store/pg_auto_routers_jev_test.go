package store

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter"
)

func newTestAutoRouterStore(t *testing.T) *AutoRouterStore {
	t.Helper()
	pg := newTestPostgresStore(t, "test_auto_routers_jev")
	s := NewAutoRouterStore(pg)
	if s == nil {
		t.Fatal("NewAutoRouterStore returned nil")
	}
	return s
}

func TestAutoRouterJevKnobsPersist(t *testing.T) {
	s := newTestAutoRouterStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, AutoRouter{
		Name: "jev-test", ModelID: "router:jev-test", Enabled: true,
		JevEnabled: true, JevMinConfidence: 0.65, JevTimeoutMs: 300,
		JevModelOverride: "jev-preview",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = s.Delete(ctx, created.ID) }()

	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.JevEnabled {
		t.Error("jev_enabled did not persist")
	}
	if got.JevMinConfidence != 0.65 {
		t.Errorf("jev_min_confidence = %v, want 0.65", got.JevMinConfidence)
	}
	if got.JevTimeoutMs != 300 {
		t.Errorf("jev_timeout_ms = %d, want 300", got.JevTimeoutMs)
	}
	if got.JevModelOverride != "jev-preview" {
		t.Errorf("jev_model_override = %q, want jev-preview", got.JevModelOverride)
	}
}

func TestAutoRouterJevKnobsDefaultOff(t *testing.T) {
	s := newTestAutoRouterStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, AutoRouter{Name: "jev-default", ModelID: "router:jev-default", Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = s.Delete(ctx, created.ID) }()

	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.JevEnabled {
		t.Error("jev_enabled must default to false")
	}
	if got.JevModelOverride != "" {
		t.Errorf("jev_model_override = %q, want empty", got.JevModelOverride)
	}
}

func TestAutoRouterJevKnobsPartialUpdate(t *testing.T) {
	s := newTestAutoRouterStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, AutoRouter{
		Name: "jev-update", ModelID: "router:jev-update", Enabled: true,
		JevEnabled: true, JevMinConfidence: 0.5, JevTimeoutMs: 400, JevModelOverride: "jev-1.13.0",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = s.Delete(ctx, created.ID) }()

	// Toggling the feature off must leave the tuning knobs untouched, so an
	// operator can switch it back on without re-entering them.
	off := false
	if errUpdate := s.Update(ctx, created.ID, AutoRouterUpdate{JevEnabled: &off}); errUpdate != nil {
		t.Fatalf("Update: %v", errUpdate)
	}
	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.JevEnabled {
		t.Error("jev_enabled must be false after the update")
	}
	if got.JevMinConfidence != 0.5 {
		t.Errorf("jev_min_confidence = %v, want 0.5 (untouched)", got.JevMinConfidence)
	}
	if got.JevTimeoutMs != 400 {
		t.Errorf("jev_timeout_ms = %d, want 400 (untouched)", got.JevTimeoutMs)
	}
	if got.JevModelOverride != "jev-1.13.0" {
		t.Errorf("jev_model_override = %q, want jev-1.13.0 (untouched)", got.JevModelOverride)
	}

	// Clearing the override must be expressible (non-nil empty string).
	empty := ""
	if errUpdate := s.Update(ctx, created.ID, AutoRouterUpdate{JevModelOverride: &empty}); errUpdate != nil {
		t.Fatalf("Update clear: %v", errUpdate)
	}
	cleared, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if cleared.JevModelOverride != "" {
		t.Errorf("jev_model_override = %q, want empty after clear", cleared.JevModelOverride)
	}
}

func TestBridgeAutoRouterConfigCarriesJevKnobs(t *testing.T) {
	cfg := BridgeAutoRouterConfig(&AutoRouter{
		ID: "pk-jev", Name: "r", ModelID: "router:r", Enabled: true,
		JevEnabled: true, JevMinConfidence: 0.7, JevTimeoutMs: 250, JevModelOverride: "  jev-preview  ",
	})
	if cfg == nil {
		t.Fatal("BridgeAutoRouterConfig returned nil")
	}
	if !cfg.JevEnabled {
		t.Error("JevEnabled did not bridge")
	}
	if cfg.JevMinConfidence != 0.7 {
		t.Errorf("JevMinConfidence = %v, want 0.7", cfg.JevMinConfidence)
	}
	if cfg.JevTimeoutMs != 250 {
		t.Errorf("JevTimeoutMs = %d, want 250", cfg.JevTimeoutMs)
	}
	if cfg.JevModelOverride != "jev-preview" {
		t.Errorf("JevModelOverride = %q, want the trimmed value", cfg.JevModelOverride)
	}
}

// The bridged config must remain a valid resolver input: adding the Jev knobs
// must not disturb tier resolution.
func TestBridgedConfigWithJevStillResolves(t *testing.T) {
	cfg := BridgeAutoRouterConfig(&AutoRouter{
		ID: "pk-jev2", ModelID: "router:jev2", Enabled: true,
		Mappings:   []TierMapping{{Tier: "complex", Model: "gpt-4o"}},
		JevEnabled: true,
	})
	resolved, ok := autorouter.Resolve(autorouter.TierComplex, cfg)
	if !ok || resolved.Model != "gpt-4o" {
		t.Fatalf("Resolve() = %+v, %v; want gpt-4o", resolved, ok)
	}
}

// TestBridgeAutoRouterConfigCarriesVisionBridgeKnobs asserts the bridged config
// carries and trims the per-router vision bridge routing fields (mirrors
// TestBridgeAutoRouterConfigCarriesJevKnobs).
func TestBridgeAutoRouterConfigCarriesVisionBridgeKnobs(t *testing.T) {
	cfg := BridgeAutoRouterConfig(&AutoRouter{
		ID: "pk-vb", Name: "r", ModelID: "router:r", Enabled: true,
		VisionBridgeModel:      "  gemma-4-31b  ",
		VisionBridgeStrategy:   "  priority  ",
		VisionBridgeProviders:  []string{" opencode ", "zai"},
		VisionBridgePriorities: []ProviderPriority{{Provider: " opencode ", Priority: 10}, {Provider: "zai", Priority: 5}},
	})
	if cfg == nil {
		t.Fatal("BridgeAutoRouterConfig returned nil")
	}
	if cfg.VisionBridgeModel != "gemma-4-31b" {
		t.Errorf("VisionBridgeModel = %q, want trimmed", cfg.VisionBridgeModel)
	}
	if cfg.VisionBridgeStrategy != "priority" {
		t.Errorf("VisionBridgeStrategy = %q, want trimmed", cfg.VisionBridgeStrategy)
	}
	if len(cfg.VisionBridgeProviders) != 2 || cfg.VisionBridgeProviders[0] != " opencode " {
		t.Errorf("VisionBridgeProviders = %v, want 2 entries preserved verbatim", cfg.VisionBridgeProviders)
	}
	if len(cfg.VisionBridgePriorities) != 2 || cfg.VisionBridgePriorities[0].Provider != "opencode" {
		t.Errorf("VisionBridgePriorities = %+v, want provider names trimmed", cfg.VisionBridgePriorities)
	}
}

// TestBridgeAutoRouterConfigVisionBridgeDefaultsEmpty asserts an empty pin list
// bridges to nil/empty (auto-discover at runtime) rather than a non-nil empty
// slice that could surprise callers.
func TestBridgeAutoRouterConfigVisionBridgeDefaultsEmpty(t *testing.T) {
	cfg := BridgeAutoRouterConfig(&AutoRouter{
		ID: "pk-vb-empty", ModelID: "router:r", Enabled: true,
		VisionBridgeModel: "gemma-4-31b",
	})
	if cfg == nil {
		t.Fatal("BridgeAutoRouterConfig returned nil")
	}
	if len(cfg.VisionBridgeProviders) != 0 {
		t.Errorf("VisionBridgeProviders = %v, want empty", cfg.VisionBridgeProviders)
	}
	if len(cfg.VisionBridgePriorities) != 0 {
		t.Errorf("VisionBridgePriorities = %v, want empty", cfg.VisionBridgePriorities)
	}
	if cfg.VisionBridgeStrategy != "" {
		t.Errorf("VisionBridgeStrategy = %q, want empty", cfg.VisionBridgeStrategy)
	}
}
