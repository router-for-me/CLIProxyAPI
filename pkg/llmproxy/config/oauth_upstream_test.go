package config

import "testing"

func TestSanitizeOAuthUpstream_NormalizesKeysAndValues(t *testing.T) {
	cfg := &Config{
		OAuthUpstream: map[string]string{
			" Claude ":          " https://api.anthropic.com/ ",
			"gemini_cli":        "https://cloudcode-pa.googleapis.com///",
			" GitHub  Copilot ": "https://api.githubcopilot.com/",
			"":                  "https://ignored.example.com",
			"cursor":            "   ",
		},
	}

	cfg.SanitizeOAuthUpstream()

	if got := cfg.OAuthUpstream["claude"]; got != "https://api.anthropic.com" {
		t.Fatalf("expected normalized claude URL, got %q", got)
	}
	if got := cfg.OAuthUpstream["gemini-cli"]; got != "https://cloudcode-pa.googleapis.com" {
		t.Fatalf("expected normalized gemini-cli URL, got %q", got)
	}
	if got := cfg.OAuthUpstream["github-copilot"]; got != "https://api.githubcopilot.com" {
		t.Fatalf("expected normalized github-copilot URL, got %q", got)
	}
	if _, ok := cfg.OAuthUpstream[""]; ok {
		t.Fatal("did not expect empty channel key to survive sanitization")
	}
	if _, ok := cfg.OAuthUpstream["cursor"]; ok {
		t.Fatal("did not expect empty URL cursor entry to survive sanitization")
	}
}

func TestOAuthUpstreamURL_LowercasesChannelLookup(t *testing.T) {
	cfg := &Config{
		OAuthUpstream: map[string]string{
			"claude":         "https://custom-claude.example.com",
			"github-copilot": "https://custom-copilot.example.com",
		},
	}

	if got := cfg.OAuthUpstreamURL(" Claude "); got != "https://custom-claude.example.com" {
		t.Fatalf("expected case-insensitive lookup to match, got %q", got)
	}
	if got := cfg.OAuthUpstreamURL("github_copilot"); got != "https://custom-copilot.example.com" {
		t.Fatalf("expected underscore channel lookup normalization, got %q", got)
	}
	if got := cfg.OAuthUpstreamURL("codex"); got != "" {
		t.Fatalf("expected missing channel to return empty string, got %q", got)
	}
}
