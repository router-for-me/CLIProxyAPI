package auth

import (
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
	if got := record.Attributes["header:User-Agent"]; got != misc.CodexCLIUserAgent {
		t.Fatalf("Attributes[header:User-Agent] = %q, want %q", got, misc.CodexCLIUserAgent)
	}
}
