package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProviderAffinityOverridesLoadSaveAndClone(t *testing.T) {
	for _, prefix := range []string{"port: 8317\n", "config-version: 8\nserver: {port: 8317}\n"} {
		raw := []byte(prefix + `routing:
  strategy: fill-first
  session-affinity: true
  session-affinity-overrides:
    claude: {strategy: round-robin, precedence: priority}
`)
		cfg, err := ParseConfigBytes(raw)
		if err != nil {
			t.Fatal(err)
		}
		policy := cfg.Routing.SessionAffinityOverrides["claude"]
		if policy.Strategy != "round-robin" || policy.Precedence != "priority" {
			t.Fatalf("policy = %+v", policy)
		}
		clone := cfg.CloneForRuntime()
		clone.Routing.SessionAffinityOverrides["claude"] = SessionAffinityOverride{Precedence: "session"}
		if cfg.Routing.SessionAffinityOverrides["claude"].Precedence != "priority" {
			t.Fatal("clone aliases source map")
		}
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err = SaveConfigPreserveComments(path, cfg); err != nil {
			t.Fatal(err)
		}
		reloaded, err := LoadConfig(path)
		if err != nil || reloaded.Routing.SessionAffinityOverrides["claude"] != policy {
			t.Fatalf("round trip: %v %v", reloaded, err)
		}
	}
}

func TestProviderAffinityOverridesValidation(t *testing.T) {
	for _, fields := range []string{
		"claude: {precedence: unknown}", "claude: {strategy: unknown}",
		"mixed: {precedence: priority}", "'': {precedence: priority}",
		"claude: {precedence: priority}, anthropic: {precedence: priority}",
	} {
		raw := []byte("config-version: 8\nserver: {port: 8317}\nrouting:\n  session-affinity-overrides: {" + fields + "}\n")
		if _, err := ParseConfigBytes(raw); err == nil {
			t.Errorf("accepted %s", fields)
		}
		if err := ValidateV8Config(raw); err == nil {
			t.Errorf("management accepted %s", fields)
		}
	}
	valid := []byte("config-version: 8\nserver: {port: 8317}\nrouting:\n  session-affinity-overrides:\n    claude: {strategy: rr, precedence: priority}\n")
	if err := ValidateV8Config(valid); err != nil {
		t.Fatal(err)
	}
}
