package main

import "testing"

func TestConfigurePreservesDefaultBooleansWhenConfigIsPartial(t *testing.T) {
	raw := mustJSON(t, lifecycleRequest{ConfigYAML: []byte("route: codex_web_search\n")})

	if errConfigure := configure(raw); errConfigure != nil {
		t.Fatalf("configure() error = %v", errConfigure)
	}

	cfg := loadedConfig()
	if !cfg.Enabled {
		t.Fatal("Enabled = false, want default true")
	}
	if !cfg.RequireWebSearchOnly {
		t.Fatal("RequireWebSearchOnly = false, want default true")
	}
	if cfg.Route != string(backendCodexWebSearch) {
		t.Fatalf("Route = %q, want codex_web_search", cfg.Route)
	}
}
