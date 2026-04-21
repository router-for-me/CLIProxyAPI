package auth

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
)

func TestBuildAuthRecordPersistsConfiguredUserAgent(t *testing.T) {
	authenticator := NewCodexAuthenticator()
	authSvc := &codex.CodexAuth{}
	bundle := &codex.CodexAuthBundle{
		TokenData: codex.CodexTokenData{
			Email:        "codex@example.com",
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
		},
		LastRefresh: "2026-03-31T00:00:00Z",
	}
	opts := &LoginOptions{
		Metadata: map[string]string{
			"user_agent": misc.CodexCLIUserAgent,
		},
	}

	record, err := authenticator.buildAuthRecord(authSvc, bundle, opts)
	if err != nil {
		t.Fatalf("buildAuthRecord() error = %v", err)
	}

	if got, _ := record.Metadata["user_agent"].(string); got != misc.CodexCLIUserAgent {
		t.Fatalf("Metadata[user_agent] = %q, want %q", got, misc.CodexCLIUserAgent)
	}
	if got, _ := record.Metadata["originator"].(string); got != misc.CodexCLIOriginator {
		t.Fatalf("Metadata[originator] = %q, want %q", got, misc.CodexCLIOriginator)
	}
	if got := record.Attributes["header:User-Agent"]; got != misc.CodexCLIUserAgent {
		t.Fatalf("Attributes[header:User-Agent] = %q, want %q", got, misc.CodexCLIUserAgent)
	}
	if got := record.Attributes["header:Originator"]; got != misc.CodexCLIOriginator {
		t.Fatalf("Attributes[header:Originator] = %q, want %q", got, misc.CodexCLIOriginator)
	}
}

func TestBuildAuthRecordPersistsDefaultUserAgentWhenUnset(t *testing.T) {
	authenticator := NewCodexAuthenticator()
	authSvc := &codex.CodexAuth{}
	bundle := &codex.CodexAuthBundle{
		TokenData: codex.CodexTokenData{
			Email:        "codex@example.com",
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
		},
		LastRefresh: "2026-03-31T00:00:00Z",
	}

	record, err := authenticator.buildAuthRecord(authSvc, bundle, nil)
	if err != nil {
		t.Fatalf("buildAuthRecord() error = %v", err)
	}

	if got, _ := record.Metadata["user_agent"].(string); got != misc.CodexCLIUserAgent {
		t.Fatalf("Metadata[user_agent] = %q, want %q", got, misc.CodexCLIUserAgent)
	}
	if got, _ := record.Metadata["originator"].(string); got != misc.CodexCLIOriginator {
		t.Fatalf("Metadata[originator] = %q, want %q", got, misc.CodexCLIOriginator)
	}
	if got := record.Attributes["header:User-Agent"]; got != misc.CodexCLIUserAgent {
		t.Fatalf("Attributes[header:User-Agent] = %q, want %q", got, misc.CodexCLIUserAgent)
	}
	if got := record.Attributes["header:Originator"]; got != misc.CodexCLIOriginator {
		t.Fatalf("Attributes[header:Originator] = %q, want %q", got, misc.CodexCLIOriginator)
	}
}

func TestBuildAuthRecordDerivesUserAgentFromConfiguredOriginator(t *testing.T) {
	authenticator := NewCodexAuthenticator()
	authSvc := &codex.CodexAuth{}
	bundle := &codex.CodexAuthBundle{
		TokenData: codex.CodexTokenData{
			Email:        "codex@example.com",
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
		},
		LastRefresh: "2026-03-31T00:00:00Z",
	}

	record, err := authenticator.buildAuthRecord(authSvc, bundle, &LoginOptions{
		Metadata: map[string]string{
			"originator": "codex_vscode",
		},
	})
	if err != nil {
		t.Fatalf("buildAuthRecord() error = %v", err)
	}

	if got, _ := record.Metadata["originator"].(string); got != "codex_vscode" {
		t.Fatalf("Metadata[originator] = %q, want %q", got, "codex_vscode")
	}
	if got := record.Attributes["header:Originator"]; got != "codex_vscode" {
		t.Fatalf("Attributes[header:Originator] = %q, want %q", got, "codex_vscode")
	}
	if got, _ := record.Metadata["user_agent"].(string); !strings.HasPrefix(got, "codex_vscode/") {
		t.Fatalf("Metadata[user_agent] = %q, want codex_vscode/ prefix", got)
	}
	if got := record.Attributes["header:User-Agent"]; !strings.HasPrefix(got, "codex_vscode/") {
		t.Fatalf("Attributes[header:User-Agent] = %q, want codex_vscode/ prefix", got)
	}
}
