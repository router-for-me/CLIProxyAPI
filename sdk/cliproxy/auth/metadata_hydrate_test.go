package auth

import "testing"

func TestHydrateAuthFromMetadata_Prefix(t *testing.T) {
	cases := []struct {
		name       string
		metadata   map[string]any
		wantPrefix string
	}{
		{"nil metadata", nil, ""},
		{"empty metadata", map[string]any{}, ""},
		{"no prefix key", map[string]any{"email": "test"}, ""},
		{"simple prefix", map[string]any{"prefix": "qwenai"}, "qwenai"},
		{"prefix with spaces", map[string]any{"prefix": "  qwenai  "}, "qwenai"},
		{"prefix with leading slash", map[string]any{"prefix": "/qwenai"}, "qwenai"},
		{"prefix with trailing slash", map[string]any{"prefix": "qwenai/"}, "qwenai"},
		{"prefix with both slashes", map[string]any{"prefix": "/qwenai/"}, "qwenai"},
		{"prefix containing slash rejected", map[string]any{"prefix": "qwen/ai"}, ""},
		{"empty prefix after trim", map[string]any{"prefix": "  "}, ""},
		{"slash-only prefix", map[string]any{"prefix": "///"}, ""},
		{"non-string prefix ignored", map[string]any{"prefix": 42}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth := &Auth{Metadata: tc.metadata}
			HydrateAuthFromMetadata(auth)
			if auth.Prefix != tc.wantPrefix {
				t.Fatalf("Prefix = %q, want %q", auth.Prefix, tc.wantPrefix)
			}
		})
	}
}

func TestHydrateAuthFromMetadata_NilAuth(t *testing.T) {
	// Must not panic on nil auth.
	HydrateAuthFromMetadata(nil)
}

func TestHydrateAuthFromMetadata_CustomHeaders(t *testing.T) {
	auth := &Auth{
		Metadata: map[string]any{
			"headers": map[string]any{
				"X-Custom": "value",
			},
		},
		Attributes: map[string]string{},
	}
	HydrateAuthFromMetadata(auth)
	if auth.Attributes["header:X-Custom"] != "value" {
		t.Fatalf("custom header not applied: %v", auth.Attributes)
	}
}
