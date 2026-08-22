package helps

import (
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/sjson"
)

func TestClaudeThinkingReplayConversationSessionKey_DelimitsConcatenatedFields(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}],"client_metadata":{"conversation_id":"conv-1"}}`)
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

	if keyA == "" || keyB == "" {
		t.Fatalf("conversation key empty: %q, %q", keyA, keyB)
	}
	if keyA == keyB {
		t.Fatalf("distinct caller tuples collided on the same replay key: %q", keyA)
	}
}

func TestClaudeThinkingReplayConversationSessionKey_IgnoresToolsList(t *testing.T) {
	base := []byte(`{"messages":[{"role":"user","content":"hello"}],"client_metadata":{"conversation_id":"conv-tools"}}`)
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
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}],"client_metadata":{"conversation_id":"conv-header"}}`)
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
	if lowerKey == "" || upperKey == "" {
		t.Fatalf("conversation key empty: %q, %q", lowerKey, upperKey)
	}
	if lowerKey != upperKey {
		t.Fatalf("lowercase and canonical headers produced different keys: %q vs %q", lowerKey, upperKey)
	}
}

func TestClaudeThinkingReplayConversationSessionKey_IgnoresWhitespaceOnlyHeaders(t *testing.T) {
	basePayload := []byte(`{"messages":[{"role":"user","content":"hello"}],"client_metadata":{"conversation_id":"conv-ws"}}`)
	req := cliproxyexecutor.Request{Payload: basePayload}
	auth := &cliproxyauth.Auth{ID: "auth-id"}

	withWhitespace := cliproxyexecutor.Options{
		Headers: map[string][]string{
			"User-Agent":        {"client/1.0"},
			"X-App":             {"   "},
			"X-Codex-Client-Id": {"\t\n"},
		},
	}
	withoutWhitespace := cliproxyexecutor.Options{
		Headers: map[string][]string{
			"User-Agent": {"client/1.0"},
		},
	}

	withKey := ClaudeThinkingReplayConversationSessionKey(auth, req, withWhitespace)
	withoutKey := ClaudeThinkingReplayConversationSessionKey(auth, req, withoutWhitespace)
	if withKey == "" || withoutKey == "" {
		t.Fatalf("conversation key empty: %q, %q", withKey, withoutKey)
	}
	if withKey != withoutKey {
		t.Fatalf("whitespace-only headers changed the replay key: %q vs %q", withKey, withoutKey)
	}

	// A whitespace-only conversation header must not become the nonce.
	wsNoncePayload := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	wsNonceReq := cliproxyexecutor.Request{Payload: wsNoncePayload}
	wsOpts := cliproxyexecutor.Options{
		Headers: map[string][]string{
			"X-Conversation-Id": {"   "},
		},
	}
	if got := ClaudeThinkingReplayConversationSessionKey(auth, wsNonceReq, wsOpts); got != "" {
		t.Fatalf("whitespace-only conversation header produced a key: %q", got)
	}
}

func TestClaudeThinkingReplayConversationSessionKey_StableForIdenticalInputs(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}],"client_metadata":{"conversation_id":"conv-stable"}}`)
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
	if first == "" {
		t.Fatal("conversation key empty for stable inputs")
	}
	if first != second {
		t.Fatalf("same inputs produced different keys: %q vs %q", first, second)
	}
}

func TestClaudeThinkingReplayConversationSessionKey_DistinctForDifferentConversationIDs(t *testing.T) {
	basePayload := []byte(`{"messages":[{"role":"user","content":"hello"}],"client_metadata":{"conversation_id":"conv-a"}}`)
	reqA := cliproxyexecutor.Request{Payload: basePayload}
	reqB := cliproxyexecutor.Request{Payload: []byte(`{"messages":[{"role":"user","content":"hello"}],"client_metadata":{"conversation_id":"conv-b"}}`)}
	auth := &cliproxyauth.Auth{ID: "auth-id"}
	opts := cliproxyexecutor.Options{}

	keyA := ClaudeThinkingReplayConversationSessionKey(auth, reqA, opts)
	keyB := ClaudeThinkingReplayConversationSessionKey(auth, reqB, opts)
	if keyA == "" || keyB == "" {
		t.Fatalf("conversation key empty: %q, %q", keyA, keyB)
	}
	if keyA == keyB {
		t.Fatalf("different conversation ids produced the same key: %q", keyA)
	}
}

func TestClaudeThinkingReplayConversationSessionKey_EmptyWhenNoConversationNonce(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	req := cliproxyexecutor.Request{Payload: payload}
	auth := &cliproxyauth.Auth{ID: "auth-id"}

	if got := ClaudeThinkingReplayConversationSessionKey(auth, req, cliproxyexecutor.Options{}); got != "" {
		t.Fatalf("expected empty key without conversation nonce, got %q", got)
	}
}
