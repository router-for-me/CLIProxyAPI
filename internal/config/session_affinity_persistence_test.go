package config

import "testing"

func TestSessionAffinityPersistencePathLoadsFromRoutingSection(t *testing.T) {
	data := []byte("routing:\n  session-affinity: true\n  session-affinity-persistence-path: \"./session-affinity.json\"\n")
	if err := ValidateV8Config(data); err != nil {
		t.Fatalf("v8 validation rejected routing.session-affinity-persistence-path: %v", err)
	}
	cfg, err := ParseConfigBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Routing.SessionAffinityPersistencePath != "./session-affinity.json" {
		t.Fatalf("SessionAffinityPersistencePath = %q", cfg.Routing.SessionAffinityPersistencePath)
	}
}

func TestSessionAffinityPersistencePathDefaultsEmpty(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("routing:\n  session-affinity: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Routing.SessionAffinityPersistencePath != "" {
		t.Fatalf("SessionAffinityPersistencePath = %q, want empty", cfg.Routing.SessionAffinityPersistencePath)
	}
}
