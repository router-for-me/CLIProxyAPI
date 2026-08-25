package store

import (
	"strings"
	"testing"
)

// TestValidateUpstreamProviderEntryNameRules exercises the validation rules for
// per-entry identity on an OpenAI Compatibility provider. The suite covers the
// shared helpers (slug syntax, reserved name, case-insensitive duplicates,
// duplicate ids, non-positive ids) and the constructor path rejecting blank
// api keys. Names carrying whitespace must normalize to a valid slug; invalid
// characters and the reserved key-<digits> form must be rejected.
func TestValidateUpstreamProviderEntryNameRules(t *testing.T) {
	type tcase struct {
		name    string
		entries []UpstreamProviderAPIKey
		wantSub string
	}
	cases := []tcase{
		{
			name: "blank api key",
			entries: []UpstreamProviderAPIKey{
				{APIKey: "", Name: "team-a"},
			},
			wantSub: "api_key",
		},
		{
			name: "whitespace name normalizes",
			entries: []UpstreamProviderAPIKey{
				{APIKey: "placeholder-secret-1", Name: "  Team-A  "},
			},
		},
		{
			name: "invalid characters rejected",
			entries: []UpstreamProviderAPIKey{
				{APIKey: "placeholder-secret-2", Name: "team a!"},
			},
			wantSub: "name",
		},
		{
			name: "reserved key-42 rejected",
			entries: []UpstreamProviderAPIKey{
				{APIKey: "placeholder-secret-3", Name: "key-42"},
			},
			wantSub: "reserved",
		},
		{
			name: "reserved Key-42 rejected",
			entries: []UpstreamProviderAPIKey{
				{APIKey: "placeholder-secret-4", Name: "Key-42"},
			},
			wantSub: "reserved",
		},
		{
			name: "duplicate names case-insensitive",
			entries: []UpstreamProviderAPIKey{
				{APIKey: "placeholder-secret-5", Name: "Team-A"},
				{APIKey: "placeholder-secret-6", Name: "team-a"},
			},
			wantSub: "duplicate",
		},
		{
			name: "duplicate positive ids",
			entries: []UpstreamProviderAPIKey{
				{APIKey: "placeholder-secret-7", Name: "alpha", ID: 9},
				{APIKey: "placeholder-secret-8", Name: "beta", ID: 9},
			},
			wantSub: "duplicate",
		},
		{
			name: "non-positive id rejected",
			entries: []UpstreamProviderAPIKey{
				{APIKey: "placeholder-secret-9", Name: "alpha", ID: -3},
			},
			wantSub: "positive",
		},
		{
			name: "valid entry",
			entries: []UpstreamProviderAPIKey{
				{APIKey: "placeholder-secret-10", Name: "team_a"},
			},
		},
		{
			name: "valid entry with no name",
			entries: []UpstreamProviderAPIKey{
				{APIKey: "placeholder-secret-11"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := UpstreamProvider{
				ProviderType:  "openai-compatibility",
				APIKeyEntries: append([]UpstreamProviderAPIKey(nil), tc.entries...),
			}
			err := validateUpstreamProvider(p)
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.wantSub) {
				t.Fatalf("expected error containing %q, got %q", tc.wantSub, err.Error())
			}
			if strings.Contains(err.Error(), "placeholder-secret") || strings.Contains(err.Error(), "secret-") {
				t.Fatalf("error must not leak api key value, got %q", err.Error())
			}
		})
	}
}

// TestNormalizeUpstreamProviderCopiesEntryValues verifies that persistence
// normalization trims the API key/proxy and lowercases the name in a copy,
// leaving caller-owned values unchanged.
func TestNormalizeUpstreamProviderCopiesEntryValues(t *testing.T) {
	input := UpstreamProvider{
		ProviderType: "openai-compatibility",
		APIKeyEntries: []UpstreamProviderAPIKey{{
			APIKey:   "  placeholder-secret-copy  ",
			Name:     "  Team-A  ",
			ProxyURL: "  http://proxy.example  ",
		}},
	}

	got, err := normalizeUpstreamProvider(input)
	if err != nil {
		t.Fatalf("normalizeUpstreamProvider() error = %v", err)
	}
	if got.APIKeyEntries[0].APIKey != "placeholder-secret-copy" {
		t.Fatalf("normalized API key = %q, want trimmed value", got.APIKeyEntries[0].APIKey)
	}
	if got.APIKeyEntries[0].Name != "team-a" {
		t.Fatalf("normalized name = %q, want team-a", got.APIKeyEntries[0].Name)
	}
	if got.APIKeyEntries[0].ProxyURL != "http://proxy.example" {
		t.Fatalf("normalized proxy URL = %q, want trimmed value", got.APIKeyEntries[0].ProxyURL)
	}
	if input.APIKeyEntries[0].APIKey != "  placeholder-secret-copy  " {
		t.Fatalf("input API key was mutated: %q", input.APIKeyEntries[0].APIKey)
	}
	if input.APIKeyEntries[0].Name != "  Team-A  " {
		t.Fatalf("input name was mutated: %q", input.APIKeyEntries[0].Name)
	}
}
