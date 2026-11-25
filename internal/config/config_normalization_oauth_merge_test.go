package config

import (
	"reflect"
	"testing"
)

func TestSanitizeOAuthModelAliasMergeCollidingKeys(t *testing.T) {
	cfg := &Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			"Gemini": {
				{Name: "gemini-2.5-pro", Alias: "g-pro"},
				{Name: "gemini-2.5-flash", Alias: "shared-alias"},
			},
			" gemini ": {
				{Name: "gemini-2.5-flash-lite", Alias: "g-lite"},
				{Name: "gemini-other", Alias: "SHARED-ALIAS"},
			},
		},
	}

	cfg.SanitizeOAuthModelAlias()

	got := cfg.OAuthModelAlias["gemini"]
	want := []OAuthModelAlias{
		{Name: "gemini-2.5-flash-lite", Alias: "g-lite"},
		{Name: "gemini-other", Alias: "SHARED-ALIAS"},
		{Name: "gemini-2.5-pro", Alias: "g-pro"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SanitizeOAuthModelAlias() merged mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestSanitizeOAuthSettingsMergeCollidingKeys(t *testing.T) {
	cfg := &Config{
		OAuthSettings: map[string][]OAuthModelSetting{
			"Gemini": {
				{Name: "gemini-2.5-pro", MaxContextLength: 1000},
				{Name: "gemini-2.5-flash", MaxContextLength: 2000},
			},
			"gemini": {
				{Name: "gemini-2.5-flash", MaxContextLength: 4000},
				{Name: "gemini-2.5-flash-lite", MaxContextLength: 3000},
			},
		},
	}

	cfg.SanitizeOAuthSettings()

	got := cfg.OAuthSettings["gemini"]
	want := []OAuthModelSetting{
		{Name: "gemini-2.5-pro", MaxContextLength: 1000},
		{Name: "gemini-2.5-flash", MaxContextLength: 4000},
		{Name: "gemini-2.5-flash-lite", MaxContextLength: 3000},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SanitizeOAuthSettings() merged mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestSanitizeOAuthRequestScopedErrorsMergeCollidingKeys(t *testing.T) {
	cfg := &Config{
		OAuthRequestScopedErrors: map[string][]RequestScopedErrorRule{
			"Gemini": {
				{Status: 400, Match: []string{"invalid_argument"}, Action: "skip"},
			},
			" gemini ": {
				{Status: 429, Match: []string{"rate_limit"}, Action: "retry"},
			},
		},
	}

	cfg.SanitizeOAuthRequestScopedErrors()

	got := cfg.OAuthRequestScopedErrors["gemini"]
	want := []RequestScopedErrorRule{
		{Status: 429, Match: []string{"rate_limit"}, MatchRegexr: []string{}, Action: "retry"},
		{Status: 400, Match: []string{"invalid_argument"}, MatchRegexr: []string{}, Action: "skip"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SanitizeOAuthRequestScopedErrors() merged mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestNormalizeOAuthExcludedModelsMergeCollidingKeys(t *testing.T) {
	in := map[string][]string{
		"Gemini":   {"gemini-2.5-pro", "gemini-2.5-flash"},
		" gemini ": {"gemini-2.5-flash", "gemini-2.5-flash-lite"},
	}

	got := NormalizeOAuthExcludedModels(in)
	if len(got) != 1 {
		t.Fatalf("expected 1 normalized provider key, got %d: %#v", len(got), got)
	}
	want := []string{"gemini-2.5-flash", "gemini-2.5-flash-lite", "gemini-2.5-pro"}
	if !reflect.DeepEqual(got["gemini"], want) {
		t.Fatalf("NormalizeOAuthExcludedModels() merged mismatch:\ngot:  %#v\nwant: %#v", got["gemini"], want)
	}
}
