package config

import "testing"

const compressionV8YAML = `config-version: 8
requests:
  compression:
    enabled: true
    shadow: true
    min-bytes: 2048
    output-styles:
      - id: terse-prose
        level: ultra
      - id: antislop-copy
      - id: bogus-style
    combos:
      - name: shell
        models: ["claude-*"]
        pipeline:
          - engine: session-dedup
          - engine: caveman
            level: ultra
          - engine: not-an-engine
`

const compressionLegacyYAML = `compression:
  enabled: true
  combos:
    - name: standard
      pipeline:
        - engine: headroom
`

func TestCompressionConfigV8Layout(t *testing.T) {
	if err := ValidateV8Config([]byte(compressionV8YAML)); err != nil {
		t.Fatalf("validate: %v", err)
	}
	cfg, err := ParseConfigBytes([]byte(compressionV8YAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cfg.Compression.Enabled || !cfg.Compression.Shadow {
		t.Fatalf("flags lost: %+v", cfg.Compression)
	}
	if cfg.Compression.MinBytes != 2048 {
		t.Fatalf("min-bytes = %d", cfg.Compression.MinBytes)
	}
	if len(cfg.Compression.Combos) != 1 || cfg.Compression.Combos[0].Name != "shell" {
		t.Fatalf("combos = %+v", cfg.Compression.Combos)
	}
	if got := len(cfg.Compression.Combos[0].Pipeline); got != 2 {
		t.Fatalf("unknown engine was not dropped: %d steps", got)
	}
	if cfg.Compression.DefaultCombo != "shell" {
		t.Fatalf("default combo = %q", cfg.Compression.DefaultCombo)
	}
	if len(cfg.Compression.OutputStyles) != 2 {
		t.Fatalf("output styles = %+v", cfg.Compression.OutputStyles)
	}
	if got := cfg.Compression.OutputStyles[0]; got.ID != "terse-prose" || got.Level != "ultra" {
		t.Fatalf("output style = %+v", got)
	}
	if got := cfg.Compression.OutputStyles[1]; got.ID != "antislop-copy" || got.Level != "full" {
		t.Fatalf("output style level default = %+v", got)
	}
	caveman := cfg.Compression.Combos[0].Pipeline[1]
	if caveman.Engine != "caveman" || caveman.Level != "ultra" {
		t.Fatalf("caveman step = %+v", caveman)
	}
	if lvl := cfg.Compression.Combos[0].Pipeline[0].Level; lvl != "" {
		t.Fatalf("non-ladder engine kept a level: %q", lvl)
	}
}

func TestCompressionConfigLegacyLayout(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(compressionLegacyYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cfg.Compression.Enabled {
		t.Fatal("legacy compression block not read")
	}
	if len(cfg.Compression.Combos) != 1 || cfg.Compression.Combos[0].Pipeline[0].Engine != "headroom" {
		t.Fatalf("combos = %+v", cfg.Compression.Combos)
	}
	if cfg.Compression.MinBytes != 4096 {
		t.Fatalf("default min-bytes = %d", cfg.Compression.MinBytes)
	}
}
