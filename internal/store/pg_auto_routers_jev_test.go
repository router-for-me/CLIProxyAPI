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
