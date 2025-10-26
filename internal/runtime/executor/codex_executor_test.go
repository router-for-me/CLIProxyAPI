package executor

import (
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"testing"
)

func Test_deriveCopilotBaseFromToken_NoMarker(t *testing.T) {
	got := deriveCopilotBaseFromToken("tid=abc;exp=12345")
	if got != "" {
		t.Fatalf("expected empty when no proxy-ep marker, got %q", got)
	}
}

func Test_deriveCopilotBaseFromToken_HostOnly(t *testing.T) {
	tok := "tid=abc;proxy-ep=proxy.individual.githubcopilot.com;exp=12345"
	got := deriveCopilotBaseFromToken(tok)
	want := "https://proxy.individual.githubcopilot.com/backend-api/codex"
	if got != want {
		t.Fatalf("unexpected derived base, want %q, got %q", want, got)
	}
}

func Test_deriveCopilotBaseFromToken_WithScheme(t *testing.T) {
	tok := "tid=abc;proxy-ep=https://proxy.corp.githubcopilot.com;exp=12345"
	got := deriveCopilotBaseFromToken(tok)
	want := "https://proxy.corp.githubcopilot.com/backend-api/codex"
	if got != want {
		t.Fatalf("unexpected derived base, want %q, got %q", want, got)
	}
}

func Test_copilotCreds_MetadataBaseURLPreferred(t *testing.T) {
	a := &cliproxyauth.Auth{
		Provider: "copilot",
		Metadata: map[string]any{
			"access_token": "tid=abc;proxy-ep=proxy.individual.githubcopilot.com;exp=1",
			"base_url":     "https://custom.example/backend-api/codex",
		},
	}
	_, bases := copilotCreds(a)
	if len(bases) == 0 {
		t.Fatalf("expected base candidates")
	}
	base := bases[0].Base
	want := "https://custom.example"
	if base != want {
		t.Fatalf("expected sanitized base_url=%q from metadata, got %q", want, base)
	}
}

func Test_copilotCreds_DoesNotDeriveFromToken(t *testing.T) {
	a := &cliproxyauth.Auth{
		Provider:   "copilot",
		Attributes: map[string]string{},
		Metadata: map[string]any{
			"access_token": "tid=abc;proxy-ep=proxy.individual.githubcopilot.com;exp=1",
		},
	}
	_, bases := copilotCreds(a)
	if len(bases) != 0 {
		t.Fatalf("expected no base candidates derived from token, got %v", bases)
	}
}
