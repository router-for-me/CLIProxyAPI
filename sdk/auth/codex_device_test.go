package auth

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
)

func TestBuildAuthRecord_DefaultWebsocketsEnabled(t *testing.T) {
	a := NewCodexAuthenticator()
	authSvc := &codex.CodexAuth{}
	authBundle := &codex.CodexAuthBundle{
		TokenData: codex.CodexTokenData{
			Email: "codex-user@example.com",
		},
	}

	record, err := a.buildAuthRecord(authSvc, authBundle)
	if err != nil {
		t.Fatalf("buildAuthRecord returned error: %v", err)
	}
	if record == nil {
		t.Fatal("buildAuthRecord returned nil record")
	}
	if record.Metadata == nil {
		t.Fatal("record metadata is nil")
	}
	raw, ok := record.Metadata["websockets"]
	if !ok {
		t.Fatal("expected metadata.websockets to be present")
	}
	enabled, ok := raw.(bool)
	if !ok {
		t.Fatalf("expected metadata.websockets to be bool, got %T", raw)
	}
	if !enabled {
		t.Fatal("expected metadata.websockets to default to true")
	}
}
