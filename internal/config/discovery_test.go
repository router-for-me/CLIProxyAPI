package config

import "testing"

func TestParseConfigBytesDiscoveryIncludeOverridesDefaultExcludes(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
discovery:
  enabled: true
  interfaces:
    include:
      - docker0
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if len(cfg.Discovery.Interfaces.Exclude) != 0 {
		t.Fatalf("default discovery excludes = %v, want none when include is explicit", cfg.Discovery.Interfaces.Exclude)
	}
	if len(cfg.Discovery.Interfaces.Include) != 1 || cfg.Discovery.Interfaces.Include[0] != "docker0" {
		t.Fatalf("discovery includes = %v", cfg.Discovery.Interfaces.Include)
	}
}
