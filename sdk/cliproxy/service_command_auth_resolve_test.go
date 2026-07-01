package cliproxy

import (
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// TestResolveConfigKeys_MatchCommandAuth verifies that command-backed credentials
// without an api-key or base-url are matched by their command identity during model
// registration, mirroring the Codex resolver.
func TestResolveConfigKeys_MatchCommandAuth(t *testing.T) {
	geminiAuth := &config.CommandAuthConfig{Command: "fetch-gemini"}
	claudeAuth := &config.CommandAuthConfig{Command: "fetch-claude"}
	vertexAuth := &config.CommandAuthConfig{Command: "fetch-vertex"}

	service := &Service{cfg: &config.Config{
		GeminiKey:          []config.GeminiKey{{Auth: geminiAuth}},
		ClaudeKey:          []config.ClaudeKey{{Auth: claudeAuth}},
		VertexCompatAPIKey: []config.VertexCompatKey{{Auth: vertexAuth}},
	}}

	mkAuth := func(provider string, auth *config.CommandAuthConfig) *coreauth.Auth {
		return &coreauth.Auth{
			Provider: provider,
			Attributes: map[string]string{
				"source":                    "config:" + provider + "[abc]",
				coreauth.AttrAuthCommandKey: config.CommandAuthIdentity(auth),
			},
		}
	}

	if got := service.resolveConfigGeminiKey(mkAuth("gemini", geminiAuth)); got == nil || got.Auth != geminiAuth {
		t.Fatalf("gemini resolver did not match command-auth entry: %#v", got)
	}
	if got := service.resolveConfigClaudeKey(mkAuth("claude", claudeAuth)); got == nil || got.Auth != claudeAuth {
		t.Fatalf("claude resolver did not match command-auth entry: %#v", got)
	}
	if got := service.resolveConfigVertexCompatKey(mkAuth("vertex", vertexAuth)); got == nil || got.Auth != vertexAuth {
		t.Fatalf("vertex resolver did not match command-auth entry: %#v", got)
	}
}
