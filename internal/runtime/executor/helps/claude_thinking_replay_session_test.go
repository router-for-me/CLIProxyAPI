package helps

import (
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestClaudeThinkingReplayConversationSessionKey_DelimitsConcatenatedFields(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	req := cliproxyexecutor.Request{Payload: payload}

	// auth.ID="ab" and no caller scope. The raw concatenation of relevant
	// caller fields is "ab".
	keyA := ClaudeThinkingReplayConversationSessionKey(
		&cliproxyauth.Auth{ID: "ab"},
		req,
		cliproxyexecutor.Options{},
	)

	// auth.ID="a" and caller scope "bc". The raw concatenation of the same
	// fields is also "abc", but the field boundaries must differ.
	keyB := ClaudeThinkingReplayConversationSessionKey(
		&cliproxyauth.Auth{ID: "a"},
		req,
		cliproxyexecutor.Options{
			Metadata: map[string]any{
				cliproxyexecutor.CallerScopeMetadataKey: "bc",
			},
		},
	)

	if keyA == keyB {
		t.Fatalf("distinct caller tuples collided on the same replay key: %q", keyA)
	}
}

func TestClaudeThinkingReplayConversationSessionKey_StableForIdenticalInputs(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	req := cliproxyexecutor.Request{Payload: payload}
	opts := cliproxyexecutor.Options{
		Headers: map[string][]string{
			"User-Agent": {"client/1.0"},
		},
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "scope",
		},
	}

	auth := &cliproxyauth.Auth{ID: "auth-id"}
	first := ClaudeThinkingReplayConversationSessionKey(auth, req, opts)
	second := ClaudeThinkingReplayConversationSessionKey(auth, req, opts)
	if first != second {
		t.Fatalf("same inputs produced different keys: %q vs %q", first, second)
	}
}
