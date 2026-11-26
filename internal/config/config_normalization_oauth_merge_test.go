package config

import (
	"reflect"
	"testing"
)

func TestSanitizeOAuthModelAlias_MergesCollidingKeysDeterministically(t *testing.T) {
	openAPITrue := true
	openAPIFalse := false

	expected := []OAuthModelAlias{
		{Name: "model-a", Alias: "alias-a", OpenAPI: &openAPITrue},
		{Name: "model-b", Alias: "shared-alias", Fork: true},
		{Name: "model-c", Alias: "alias-c", OpenAPI: &openAPIFalse},
		{Name: "model-d", Alias: "alias-d"},
	}

	// Sorted raw key order: " gemini " < "Gemini" < "gemini".
	for i := 0; i < 20; i++ {
		cfg := &Config{
			OAuthModelAlias: map[string][]OAuthModelAlias{
				"gemini": {
					{Name: "model-dup-late", Alias: "SHARED-ALIAS"},
					{Name: "model-d", Alias: "alias-d"},
					{Name: "same", Alias: "same"},
				},
				"Gemini": {
					{Name: "model-dup-mid", Alias: "Shared-Alias"},
					{Name: "model-c", Alias: "alias-c", OpenAPI: &openAPIFalse},
				},
				" gemini ": {
					{Name: "model-a", Alias: "alias-a", OpenAPI: &openAPITrue},
					{Name: "model-b", Alias: "shared-alias", Fork: true},
				},
				"GEMINI": {
					{Name: "", Alias: "ignored"},
				},
			},
		}

		cfg.SanitizeOAuthModelAlias()

		if len(cfg.OAuthModelAlias) != 1 {
			t.Fatalf("iteration %d: len(OAuthModelAlias) = %d, want 1 (%#v)", i, len(cfg.OAuthModelAlias), cfg.OAuthModelAlias)
		}
		got := cfg.OAuthModelAlias["gemini"]
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("iteration %d: OAuthModelAlias[gemini] = %#v, want %#v", i, got, expected)
		}
	}
}

func TestSanitizeOAuthSettings_MergesCollidingKeysDeterministically(t *testing.T) {
	openAPITrue := true
	openAPIFalse := false

	// Sorted raw key order: " gemini " < "Gemini" < "gemini".
	// Combined slice before reverse-dedup:
	//   [model-a:100, shared:200, model-b:300, shared:400]
	// Reverse-dedup keeps the last occurrence of "shared" at the end:
	//   [model-a:100, model-b:300, shared:400]
	expected := []OAuthModelSetting{
		{Name: "model-a", MaxContextLength: 100, OpenAPI: &openAPITrue},
		{Name: "model-b", MaxContextLength: 300},
		{Name: "shared", MaxContextLength: 400, OpenAPI: &openAPIFalse},
	}

	for i := 0; i < 20; i++ {
		cfg := &Config{
			OAuthSettings: map[string][]OAuthModelSetting{
				"gemini": {
					{Name: "shared", MaxContextLength: 400, OpenAPI: &openAPIFalse},
				},
				"Gemini": {
					{Name: "model-b", MaxContextLength: 300},
				},
				" gemini ": {
					{Name: "model-a", MaxContextLength: 100, OpenAPI: &openAPITrue},
					{Name: "shared", MaxContextLength: 200},
				},
				"GEMINI": {
					{Name: "   ", MaxContextLength: 999},
				},
			},
		}

		cfg.SanitizeOAuthSettings()

		if len(cfg.OAuthSettings) != 1 {
			t.Fatalf("iteration %d: len(OAuthSettings) = %d, want 1 (%#v)", i, len(cfg.OAuthSettings), cfg.OAuthSettings)
		}
		got := cfg.OAuthSettings["gemini"]
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("iteration %d: OAuthSettings[gemini] = %#v, want %#v", i, got, expected)
		}
	}
}

func TestSanitizeOAuthRequestScopedErrors_MergesCollidingKeysDeterministically(t *testing.T) {
	expected := []RequestScopedErrorRule{
		{Status: 400, Match: []string{"err-a"}, MatchRegexr: []string{}, Action: "stop"},
		{Status: 429, Match: []string{"err-b"}, MatchRegexr: []string{}, Action: "continue"},
		{Status: 500, Match: []string{"err-c"}, MatchRegexr: []string{}, Action: "stop-and-cooldown"},
	}

	// Sorted raw key order: " gemini " < "GEMINI" < "Gemini" < "gemini".
	for i := 0; i < 20; i++ {
		cfg := &Config{
			OAuthRequestScopedErrors: map[string][]RequestScopedErrorRule{
				"gemini": {
					{Status: 500, Match: []string{" err-c "}, Action: " STOP-AND-COOLDOWN "},
				},
				"Gemini": {
					{Status: 429, Match: []string{"err-b"}, Action: "continue"},
					{Status: 0, Match: []string{"invalid"}, Action: "stop"},
				},
				"GEMINI": {
					{Status: 400, Match: []string{"   "}, Action: "stop"},
				},
				" gemini ": {
					{Status: 400, Match: []string{"err-a"}, Action: "stop"},
				},
			},
		}

		cfg.SanitizeOAuthRequestScopedErrors()

		if len(cfg.OAuthRequestScopedErrors) != 1 {
			t.Fatalf("iteration %d: len(OAuthRequestScopedErrors) = %d, want 1 (%#v)", i, len(cfg.OAuthRequestScopedErrors), cfg.OAuthRequestScopedErrors)
		}
		got := cfg.OAuthRequestScopedErrors["gemini"]
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("iteration %d: OAuthRequestScopedErrors[gemini] = %#v, want %#v", i, got, expected)
		}
	}
}

func TestNormalizeOAuthExcludedModels_MergesCollidingKeysDeterministically(t *testing.T) {
	expected := []string{"model-a", "shared-model", "model-b", "model-c"}

	// Sorted raw key order: " gemini " < "GEMINI" < "Gemini" < "gemini".
	for i := 0; i < 20; i++ {
		in := map[string][]string{
			"gemini": {
				"  SHARED-MODEL ",
				"model-c",
			},
			"Gemini": {
				"model-b",
				"shared-model",
			},
			"GEMINI": {
				"   ",
			},
			" gemini ": {
				"Model-A",
				"shared-model",
			},
		}

		got := NormalizeOAuthExcludedModels(in)

		if len(got) != 1 {
			t.Fatalf("iteration %d: len(got) = %d, want 1 (%#v)", i, len(got), got)
		}
		if !reflect.DeepEqual(got["gemini"], expected) {
			t.Fatalf("iteration %d: got[gemini] = %#v, want %#v", i, got["gemini"], expected)
		}
	}
}
