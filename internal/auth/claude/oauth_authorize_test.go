package claude

import (
	"net/url"
	"strings"
	"testing"

	"github.com/MoeForge/moego/base64"
)

func TestGenerateStateMatchesNativeClaudeCode(t *testing.T) {
	seen := make(map[string]struct{})
	for range 4 {
		state, errGenerate := GenerateState()
		if errGenerate != nil {
			t.Fatalf("GenerateState() error = %v", errGenerate)
		}
		if len(state) != 43 {
			t.Fatalf("state length = %d, want 43", len(state))
		}
		decoded, errDecode := base64.RawURLEncoding.DecodeString(state)
		if errDecode != nil || len(decoded) != 32 {
			t.Fatalf("state must encode 32 random bytes as unpadded base64url: decoded length = %d, error = %v", len(decoded), errDecode)
		}
		if _, duplicate := seen[state]; duplicate {
			t.Fatal("GenerateState() reused a state")
		}
		seen[state] = struct{}{}
	}
}

func TestGenerateAuthURLUsesNativeClaudeCodeScopes(t *testing.T) {
	state := strings.Repeat("Ab9-_", 8) + "AbA"
	auth := &ClaudeAuth{}
	authURL, returnedState, errGenerate := auth.GenerateAuthURL(state, &PKCECodes{CodeChallenge: "challenge"})
	if errGenerate != nil {
		t.Fatalf("GenerateAuthURL() error = %v", errGenerate)
	}
	parsed, errParse := url.Parse(authURL)
	if errParse != nil {
		t.Fatalf("parse authorization URL: %v", errParse)
	}
	const wantScope = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	if got := parsed.Query().Get("scope"); got != wantScope {
		t.Errorf("authorization scope = %q, want %q", got, wantScope)
	}
	if returnedState != state || parsed.Query().Get("state") != state {
		t.Error("authorization URL did not preserve the supplied state")
	}
}
