package synthesizer

import (
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/watcher/diff"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestNewStableIDGenerator(t *testing.T) {
	gen := NewStableIDGenerator()
	if gen == nil {
		t.Fatal("expected non-nil generator")
	}
	if gen.counters == nil {
		t.Fatal("expected non-nil counters map")
	}
}

func TestStableIDGenerator_Next(t *testing.T) {
	tests := []struct {
		name       string
		kind       string
		parts      []string
		wantPrefix string
	}{
		{
			name:       "basic gemini apikey",
			kind:       "gemini:apikey",
			parts:      []string{"test-key", ""},
			wantPrefix: "gemini:apikey:",
		},
		{
			name:       "claude with base url",
			kind:       "claude:apikey",
			parts:      []string{"sk-ant-xxx", "https://api.anthropic.com"},
			wantPrefix: "claude:apikey:",
		},
		{
			name:       "empty parts",
			kind:       "codex:apikey",
			parts:      []string{},
			wantPrefix: "codex:apikey:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gen := NewStableIDGenerator()
			id, short := gen.Next(tt.kind, tt.parts...)

			if !strings.Contains(id, tt.wantPrefix) {
				t.Errorf("expected id to contain %q, got %q", tt.wantPrefix, id)
			}
			if short == "" {
				t.Error("expected non-empty short id")
			}
			if len(short) != 12 {
				t.Errorf("expected short id length 12, got %d", len(short))
			}
		})
	}
}

func TestStableIDGenerator_Stability(t *testing.T) {
	gen1 := NewStableIDGenerator()
	gen2 := NewStableIDGenerator()

	id1, _ := gen1.Next("gemini:apikey", "test-key", "https://api.example.com")
	id2, _ := gen2.Next("gemini:apikey", "test-key", "https://api.example.com")

	if id1 != id2 {
		t.Errorf("same inputs should produce same ID: got %q and %q", id1, id2)
	}
}

func TestStableIDGenerator_CollisionHandling(t *testing.T) {
	gen := NewStableIDGenerator()

	id1, short1 := gen.Next("gemini:apikey", "same-key")
	id2, short2 := gen.Next("gemini:apikey", "same-key")

	if id1 == id2 {
		t.Error("collision should be handled with suffix")
	}
	if short1 == short2 {
		t.Error("short ids should differ")
	}
	if !strings.Contains(short2, "-1") {
		t.Errorf("second short id should contain -1 suffix, got %q", short2)
	}
}

func TestStableIDGenerator_NilReceiver(t *testing.T) {
	var gen *StableIDGenerator = nil
	id, short := gen.Next("test:kind", "part")

	if id != "test:kind:000000000000" {
		t.Errorf("expected test:kind:000000000000, got %q", id)
	}
	if short != "000000000000" {
		t.Errorf("expected 000000000000, got %q", short)
	}
}

func TestApplyAuthExcludedModelsMeta(t *testing.T) {
	tests := []struct {
		name     string
		auth     *coreauth.Auth
		cfg      *config.Config
		perKey   []string
		authKind string
		wantHash bool
		wantKind string
	}{
		{
			name: "apikey with excluded models",
			auth: &coreauth.Auth{
				Provider:   "gemini",
				Attributes: make(map[string]string),
			},
			cfg:      &config.Config{},
			perKey:   []string{"model-a", "model-b"},
			authKind: "apikey",
			wantHash: true,
			wantKind: "apikey",
		},
		{
			name: "oauth with provider excluded models",
			auth: &coreauth.Auth{
				Provider:   "claude",
				Attributes: make(map[string]string),
			},
			cfg: &config.Config{
				OAuthExcludedModels: map[string][]string{
					"claude": {"claude-2.0"},
				},
			},
			perKey:   nil,
			authKind: "oauth",
			wantHash: true,
			wantKind: "oauth",
		},
		{
			name: "nil auth",
			auth: nil,
			cfg:  &config.Config{},
		},
		{
			name:     "nil config",
			auth:     &coreauth.Auth{Provider: "test"},
			cfg:      nil,
			authKind: "apikey",
		},
		{
			name: "nil attributes initialized",
			auth: &coreauth.Auth{
				Provider:   "gemini",
				Attributes: nil,
			},
			cfg:      &config.Config{},
			perKey:   []string{"model-x"},
			authKind: "apikey",
			wantHash: true,
			wantKind: "apikey",
		},
		{
			name: "apikey with duplicate excluded models",
			auth: &coreauth.Auth{
				Provider:   "gemini",
				Attributes: make(map[string]string),
			},
			cfg:      &config.Config{},
			perKey:   []string{"model-a", "MODEL-A", "model-b", "model-a"},
			authKind: "apikey",
			wantHash: true,
			wantKind: "apikey",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ApplyAuthExcludedModelsMeta(tt.auth, tt.cfg, tt.perKey, tt.authKind)

			if tt.auth != nil && tt.cfg != nil {
				if tt.wantHash {
					if _, ok := tt.auth.Attributes["excluded_models_hash"]; !ok {
						t.Error("expected excluded_models_hash in attributes")
					}
				}
				if tt.wantKind != "" {
					if got := tt.auth.Attributes["auth_kind"]; got != tt.wantKind {
						t.Errorf("expected auth_kind=%s, got %s", tt.wantKind, got)
					}
				}
			}
		})
	}
}

func TestApplyAuthExcludedModelsMeta_OAuthMergeWritesCombinedModels(t *testing.T) {
	auth := &coreauth.Auth{
		Provider:   "claude",
		Attributes: make(map[string]string),
	}
	cfg := &config.Config{
		OAuthExcludedModels: map[string][]string{
			"claude": {"global-a", "shared"},
		},
	}

	ApplyAuthExcludedModelsMeta(auth, cfg, []string{"per", "SHARED"}, "oauth")

	const wantCombined = "global-a,per,shared"
	if gotCombined := auth.Attributes["excluded_models"]; gotCombined != wantCombined {
		t.Fatalf("expected excluded_models=%q, got %q", wantCombined, gotCombined)
	}

	expectedHash := diff.ComputeExcludedModelsHash([]string{"global-a", "per", "shared"})
	if gotHash := auth.Attributes["excluded_models_hash"]; gotHash != expectedHash {
		t.Fatalf("expected excluded_models_hash=%q, got %q", expectedHash, gotHash)
	}
}

func TestAddConfigHeadersToAttrs(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		attrs   map[string]string
		want    map[string]string
	}{
		{
			name: "basic headers",
			headers: map[string]string{
				"Authorization": "Bearer token",
				"X-Custom":      "value",
			},
			attrs: map[string]string{"existing": "key"},
			want: map[string]string{
				"existing":             "key",
				"header:Authorization": "Bearer token",
				"header:X-Custom":      "value",
			},
		},
		{
			name:    "empty headers",
			headers: map[string]string{},
			attrs:   map[string]string{"existing": "key"},
			want:    map[string]string{"existing": "key"},
		},
		{
			name:    "nil headers",
			headers: nil,
			attrs:   map[string]string{"existing": "key"},
			want:    map[string]string{"existing": "key"},
		},
		{
			name:    "nil attrs",
			headers: map[string]string{"key": "value"},
			attrs:   nil,
			want:    nil,
		},
		{
			name: "skip empty keys and values",
			headers: map[string]string{
				"":      "value",
				"key":   "",
				"  ":    "value",
				"valid": "valid-value",
			},
			attrs: make(map[string]string),
			want: map[string]string{
				"header:valid": "valid-value",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addConfigHeadersToAttrs(tt.headers, tt.attrs)
			if !reflect.DeepEqual(tt.attrs, tt.want) {
				t.Errorf("expected %v, got %v", tt.want, tt.attrs)
			}
		})
	}
}
