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
	keyA, _ := ClaudeThinkingReplayConversationSessionKey(
		&cliproxyauth.Auth{ID: "ab"},
		req,
		cliproxyexecutor.Options{},
	)

	// auth.ID="a" and caller scope "bc". The raw concatenation of the same
	// fields is also "abc", but the field boundaries must differ.
	keyB, _ := ClaudeThinkingReplayConversationSessionKey(
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

	keyA, _ := ClaudeThinkingReplayConversationSessionKey(&cliproxyauth.Auth{ID: "caller"}, cliproxyexecutor.Request{Payload: a}, optsA)
	keyB, _ := ClaudeThinkingReplayConversationSessionKey(&cliproxyauth.Auth{ID: "caller"}, cliproxyexecutor.Request{Payload: b}, optsB)
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

	lowerKey, _ := ClaudeThinkingReplayConversationSessionKey(auth, req, lowerOpts)
	upperKey, _ := ClaudeThinkingReplayConversationSessionKey(auth, req, upperOpts)
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

	withKey, _ := ClaudeThinkingReplayConversationSessionKey(auth, req, withWhitespace)
	withoutKey, _ := ClaudeThinkingReplayConversationSessionKey(auth, req, withoutWhitespace)
	if withKey == "" || withoutKey == "" {
		t.Fatalf("conversation key empty: %q, %q", withKey, withoutKey)
	}
	if withKey != withoutKey {
		t.Fatalf("whitespace-only headers changed the replay key: %q vs %q", withKey, withoutKey)
	}

	// A whitespace-only conversation header must not become the nonce, but the
	// no-nonce content-derived fallback should still produce a key.
	wsNoncePayload := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	wsNonceReq := cliproxyexecutor.Request{Payload: wsNoncePayload}
	wsOpts := cliproxyexecutor.Options{
		Headers: map[string][]string{
			"X-Conversation-Id": {"   "},
		},
	}
	got, usedNonce := ClaudeThinkingReplayConversationSessionKey(auth, wsNonceReq, wsOpts)
	if got == "" {
		t.Fatal("whitespace-only conversation header disabled the content fallback")
	}
	if usedNonce {
		t.Fatalf("whitespace-only conversation header was used as nonce: %q", got)
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
	first, _ := ClaudeThinkingReplayConversationSessionKey(auth, req, opts)
	second, _ := ClaudeThinkingReplayConversationSessionKey(auth, req, opts)
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

	keyA, _ := ClaudeThinkingReplayConversationSessionKey(auth, reqA, opts)
	keyB, _ := ClaudeThinkingReplayConversationSessionKey(auth, reqB, opts)
	if keyA == "" || keyB == "" {
		t.Fatalf("conversation key empty: %q, %q", keyA, keyB)
	}
	if keyA == keyB {
		t.Fatalf("different conversation ids produced the same key: %q", keyA)
	}
}

func TestClaudeThinkingReplayConversationSessionKey_StableWithNonceAcrossHistoryCompaction(t *testing.T) {
	// The caller keeps the same conversation nonce but removes the first user
	// turn (history compaction). The nonce-based key must stay the same.
	firstPayload := []byte(`{"messages":[{"role":"user","content":"hello"}],"client_metadata":{"conversation_id":"conv-compact"}}`)
	compactedPayload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"text","text":"hi"}]},{"role":"user","content":"next"}],"client_metadata":{"conversation_id":"conv-compact"}}`)

	auth := &cliproxyauth.Auth{ID: "auth-id"}
	opts := cliproxyexecutor.Options{}

	firstKey, usedNonce1 := ClaudeThinkingReplayConversationSessionKey(auth, cliproxyexecutor.Request{Payload: firstPayload}, opts)
	compactedKey, usedNonce2 := ClaudeThinkingReplayConversationSessionKey(auth, cliproxyexecutor.Request{Payload: compactedPayload}, opts)
	if firstKey == "" || compactedKey == "" {
		t.Fatalf("conversation key empty: %q, %q", firstKey, compactedKey)
	}
	if !usedNonce1 || !usedNonce2 {
		t.Fatalf("expected conversation nonce to drive the key")
	}
	if firstKey != compactedKey {
		t.Fatalf("nonce-based key changed across compaction: %q vs %q", firstKey, compactedKey)
	}
}

func TestClaudeThinkingReplayConversationSessionKey_DerivesContentKeyWhenNoConversationNonce(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	req := cliproxyexecutor.Request{Payload: payload}
	auth := &cliproxyauth.Auth{ID: "auth-id"}

	key, usedNonce := ClaudeThinkingReplayConversationSessionKey(auth, req, cliproxyexecutor.Options{})
	if key == "" {
		t.Fatal("expected a content-derived key without conversation nonce")
	}
	if usedNonce {
		t.Fatalf("expected no conversation nonce, got key %q", key)
	}
}

func TestClaudeThinkingReplayConversationSessionKey_DistinctContentKeysForDifferentOpenings(t *testing.T) {
	a := cliproxyexecutor.Request{Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}
	b := cliproxyexecutor.Request{Payload: []byte(`{"messages":[{"role":"user","content":"world"}]}`)}
	auth := &cliproxyauth.Auth{ID: "auth-id"}
	opts := cliproxyexecutor.Options{}

	keyA, _ := ClaudeThinkingReplayConversationSessionKey(auth, a, opts)
	keyB, _ := ClaudeThinkingReplayConversationSessionKey(auth, b, opts)
	if keyA == "" || keyB == "" {
		t.Fatalf("conversation key empty: %q, %q", keyA, keyB)
	}
	if keyA == keyB {
		t.Fatalf("different content produced the same key: %q", keyA)
	}
}
