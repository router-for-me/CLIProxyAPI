package config

import (
	"strings"
	"testing"
)

func TestEnsureClientAPIKeyPrefix(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"   ", ""},
		{"cpa-valid", "cpa-valid"},
		{"cpa-", "cpa-"},
		{"my-key", "cpa-my-key"},
		{"123456", "cpa-123456"},
		{"  my-key  ", "cpa-my-key"},
		{"  cpa-already  ", "cpa-already"},
	}

	for _, tt := range tests {
		got := EnsureClientAPIKeyPrefix(tt.input)
		if got != tt.want {
			t.Errorf("EnsureClientAPIKeyPrefix(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeClientAPIKeysYAML_V8(t *testing.T) {
	raw := `
access:
  # Client access keys
  api-keys:
    - "key-1" # first key
    - "cpa-key-2" # second key
    - "key-3"
`
	out, changed, err := NormalizeClientAPIKeysYAML([]byte(raw))
	if err != nil {
		t.Fatalf("NormalizeClientAPIKeysYAML error: %v", err)
	}
	if !changed {
		t.Fatal("expected changed = true")
	}

	outStr := string(out)
	if !strings.Contains(outStr, `"cpa-key-1"`) {
		t.Errorf("missing cpa-key-1 in output: %s", outStr)
	}
	if !strings.Contains(outStr, `"cpa-key-2"`) {
		t.Errorf("missing cpa-key-2 in output: %s", outStr)
	}
	if !strings.Contains(outStr, `"cpa-key-3"`) {
		t.Errorf("missing cpa-key-3 in output: %s", outStr)
	}
	if !strings.Contains(outStr, "# Client access keys") {
		t.Errorf("comment dropped: %s", outStr)
	}
	if !strings.Contains(outStr, "# first key") {
		t.Errorf("line comment dropped: %s", outStr)
	}

	// Verify idempotency
	out2, changed2, err2 := NormalizeClientAPIKeysYAML(out)
	if err2 != nil {
		t.Fatalf("second run error: %v", err2)
	}
	if changed2 {
		t.Fatal("second run should not report changed = true")
	}
	if string(out2) != string(out) {
		t.Fatalf("second run modified output: %s vs %s", string(out2), string(out))
	}
}

func TestNormalizeClientAPIKeysYAML_LegacyUntouchedOnDisk(t *testing.T) {
	raw := `
# Legacy layout without v8 roots
api-keys:
  - "legacy-key-1"
  - "cpa-legacy-key-2"
`
	out, changed, err := NormalizeClientAPIKeysYAML([]byte(raw))
	if err != nil {
		t.Fatalf("NormalizeClientAPIKeysYAML error: %v", err)
	}
	if changed {
		t.Fatal("legacy-only document should not be rewritten on disk")
	}
	if string(out) != raw {
		t.Fatalf("expected raw untouched, got: %s", string(out))
	}
}

func TestNormalizeClientAPIKeysYAML_UpstreamIgnored(t *testing.T) {
	raw := `
api-keys:
  codex:
    - name: "pool"
      keys:
        - api-key: "sk-upstream"
`
	out, changed, err := NormalizeClientAPIKeysYAML([]byte(raw))
	if err != nil {
		t.Fatalf("NormalizeClientAPIKeysYAML error: %v", err)
	}
	if changed {
		t.Fatal("upstream provider api-keys mapping must not be modified")
	}
	if string(out) != raw {
		t.Fatalf("content should be untouched, got: %s", string(out))
	}
}

func TestNormalizeClientAPIKeysYAML_Deduplication(t *testing.T) {
	raw := `
access:
  api-keys:
    - "cpa-foo"
    - "foo"
    - "bar"
    - "cpa-bar"
`
	out, changed, err := NormalizeClientAPIKeysYAML([]byte(raw))
	if err != nil {
		t.Fatalf("NormalizeClientAPIKeysYAML error: %v", err)
	}
	if !changed {
		t.Fatal("expected changed = true")
	}

	outStr := string(out)
	if strings.Count(outStr, "cpa-foo") != 1 {
		t.Errorf("expected cpa-foo to appear exactly once, got: %s", outStr)
	}
	if strings.Count(outStr, "cpa-bar") != 1 {
		t.Errorf("expected cpa-bar to appear exactly once, got: %s", outStr)
	}
}
