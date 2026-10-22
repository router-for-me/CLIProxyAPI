package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionAffinityPersistenceConfigDefaultAndRoundTrip(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		want      bool
	}{
		{"default", "routing: {session-affinity: true}\n", false},
		{"nested enabled", "server: {port: 8317}\nrouting: {session-affinity: true, session-affinity-persistence: true}\n", true},
		{"legacy enabled", "port: 8317\nrouting: {session-affinity: true, session-affinity-persistence: true}\n", true},
		{"explicit false", "routing: {session-affinity: true, session-affinity-persistence: false}\n", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg, errParse := ParseConfigBytes([]byte(test.raw))
			if errParse != nil {
				t.Fatal(errParse)
			}
			if cfg.Routing.SessionAffinityPersistence != test.want {
				t.Fatal("wrong persistence setting")
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if errWrite := os.WriteFile(path, []byte(test.raw), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			if errSave := SaveConfigPreserveComments(path, cfg); errSave != nil {
				t.Fatal(errSave)
			}
			reloaded, errLoad := LoadConfig(path)
			if errLoad != nil || reloaded.Routing.SessionAffinityPersistence != test.want {
				t.Fatalf("save/reload changed persistence: %v", errLoad)
			}
			cfg.Routing.SessionAffinityPersistence = !test.want
			if errSave := SaveConfigPreserveComments(path, cfg, true); errSave != nil {
				t.Fatal(errSave)
			}
			reloaded, errLoad = LoadConfig(path)
			if errLoad != nil || reloaded.Routing.SessionAffinityPersistence != !test.want {
				t.Fatalf("updated persistence was not saved: %v", errLoad)
			}
			data, errRead := os.ReadFile(path)
			if errRead != nil {
				t.Fatal(errRead)
			}
			if errValidate := ValidateV8Config(data); errValidate != nil {
				t.Fatal(errValidate)
			}
			if strings.Contains(string(data), "\nsession-affinity-persistence:") {
				t.Fatal("persistence escaped routing block")
			}
		})
	}
}
