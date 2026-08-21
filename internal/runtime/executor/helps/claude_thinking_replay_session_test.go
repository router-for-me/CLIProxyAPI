package helps

import (
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/sjson"
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

func TestClaudeThinkingReplayConversationSessionKey_IgnoresToolsList(t *testing.T) {
	base := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	a := append([]byte(nil), base...)
	b, _ := sjson.SetRawBytes(base, "tools", []byte(`[{"name":"tool_a"}]`))

	optsA := cliproxyexecutor.Options{}
	optsB := cliproxyexecutor.Options{}

	keyA := ClaudeThinkingReplayConversationSessionKey(&cliproxyauth.Auth{ID: "caller"}, cliproxyexecutor.Request{Payload: a}, optsA)
	keyB := ClaudeThinkingReplayConversationSessionKey(&cliproxyauth.Auth{ID: "caller"}, cliproxyexecutor.Request{Payload: b}, optsB)
	if keyA == "" || keyB == "" {
		t.Fatalf("empty key: %q, %q", keyA, keyB)
	}
	if keyA != keyB {
		t.Fatalf("different tools list changed the replay key: %q vs %q", keyA, keyB)
	}
}

func TestClaudeThinkingReplayConversationSessionKey_ReadsHeadersCaseInsensitively(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	req := cliproxyexecutor.Request{Payload: payload}
	auth := &cliproxyauth.Auth{ID: "auth-id"}

	lowerOpts := cliproxyexecutor.Options{
		Headers: map[string][]string{
			"x-codex-client-id": {"client-lowercase"},
			"x-app":             {"app-lowercase"},
			"user-agent":        {"agent-lowercase"},
		},
	}
	upperOpts := cliproxyexecutor.Options{
		Headers: map[string][]string{
			"X-Codex-Client-Id": {"client-lowercase"},
			"X-App":             {"app-lowercase"},
			"User-Agent":        {"agent-lowercase"},
		},
	}

	lowerKey := ClaudeThinkingReplayConversationSessionKey(auth, req, lowerOpts)
	upperKey := ClaudeThinkingReplayConversationSessionKey(auth, req, upperOpts)
	if lowerKey != upperKey {
		t.Fatalf("lowercase and canonical headers produced different keys: %q vs %q", lowerKey, upperKey)
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
