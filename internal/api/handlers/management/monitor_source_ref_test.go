package management

import (
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestBuildAuthMonitorSourceRefUsesAuthFilesPath(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "codex-test",
		Provider: "codex",
		FileName: "codex-test.json",
		Attributes: map[string]string{
			"api_key": "sk-test",
		},
	}

	ref := buildAuthMonitorSourceRef(auth)

	if ref.ConfigPath != "/auth-files" {
		t.Fatalf("ConfigPath = %q, want %q", ref.ConfigPath, "/auth-files")
	}
	if ref.EditPath != "/auth-files" {
		t.Fatalf("EditPath = %q, want %q", ref.EditPath, "/auth-files")
	}
}
