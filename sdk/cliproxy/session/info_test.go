package session

import (
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestExtractSessionInfoAllClients(t *testing.T) {
	t.Parallel()

	// 1. Claude Code with Subagent
	claudeHeaders := http.Header{
		"X-Claude-Code-Session-Id": []string{"claude-root-123"},
		"X-Claude-Code-Agent-Id":   []string{"subagent-checker"},
	}
	info, ok := ExtractSessionInfo(claudeHeaders, nil, nil)
	if !ok {
		t.Fatalf("ExtractSessionInfo failed for Claude Code")
	}
	if info.ClientType != "claude" {
		t.Errorf("ClientType = %q, want claude", info.ClientType)
	}
	if info.SessionID != "claude:claude-root-123:agent:subagent-checker" {
		t.Errorf("SessionID = %q", info.SessionID)
	}
	if info.ParentSessionID != "claude:claude-root-123" {
		t.Errorf("ParentSessionID = %q", info.ParentSessionID)
	}
	if info.AgentName != "subagent-checker" {
		t.Errorf("AgentName = %q", info.AgentName)
	}

	// 2. Codex CLI with parent thread
	codexHeaders := http.Header{
		"Session-Id":               []string{"codex-child-555"},
		"x-codex-parent-thread-id": []string{"codex-parent-111"},
	}
	info, ok = ExtractSessionInfo(codexHeaders, nil, nil)
	if !ok || info.ClientType != "codex" {
		t.Fatalf("ExtractSessionInfo failed for Codex CLI")
	}
	if info.SessionID != "codex:codex-child-555" || info.ParentSessionID != "codex:codex-parent-111" {
		t.Errorf("Codex session ids mismatch: %+v", info)
	}

	// 3. Pi Slot Session
	piHeaders := http.Header{
		"X-Slot-Session-Id": []string{"pi-slot-777"},
	}
	info, ok = ExtractSessionInfo(piHeaders, nil, nil)
	if !ok || info.ClientType != "pi" || info.SessionID != "slot:pi-slot-777" {
		t.Errorf("Pi slot mismatch: %+v", info)
	}

	// 4. OpenCode Session Affinity & Parent
	opencodeHeaders := http.Header{
		"X-Session-Affinity":        []string{"oc-child-999"},
		"X-Parent-Session-Affinity": []string{"oc-parent-333"},
	}
	info, ok = ExtractSessionInfo(opencodeHeaders, nil, nil)
	if !ok || info.ClientType != "opencode" || info.SessionID != "affinity:oc-child-999" || info.ParentSessionID != "affinity:oc-parent-333" {
		t.Errorf("OpenCode mismatch: %+v", info)
	}

	// 5. Antigravity X-Http-Session-Id
	agyHeaders := http.Header{
		"X-Http-Session-Id": []string{"agy-sess-888"},
	}
	info, ok = ExtractSessionInfo(agyHeaders, nil, nil)
	if !ok || info.ClientType != "agy" || info.SessionID != "agy:agy-sess-888" {
		t.Errorf("Antigravity mismatch: %+v", info)
	}

	// 6. Payload with metadata.agent_id and parent_session_id
	payload := []byte(`{
		"session_id": "payload-child-10",
		"parent_session_id": "payload-parent-01",
		"metadata": {
			"agent_id": "analyzer"
		}
	}`)
	metadata := map[string]any{
		cliproxyexecutor.CallerScopeMetadataKey: "test-scope",
	}
	info, ok = ExtractSessionInfo(nil, payload, metadata)
	if !ok || info.ClientType != "generic" {
		t.Fatalf("ExtractSessionInfo failed for payload")
	}
	if info.SessionID != "session:payload-child-10:agent:analyzer" {
		t.Errorf("SessionID = %q", info.SessionID)
	}
	if info.ParentSessionID != "session:payload-parent-01" {
		t.Errorf("ParentSessionID = %q", info.ParentSessionID)
	}
	if info.CallerScope != "test-scope" {
		t.Errorf("CallerScope = %q", info.CallerScope)
	}
}

func TestExtractSessionInfoCanonicalizesPayloadParentForHeaderSessions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		headers    http.Header
		wantParent string
	}{
		{
			name:       "generic header",
			headers:    http.Header{"X-Session-ID": []string{"child"}},
			wantParent: "header:parent",
		},
		{
			name:       "codex header",
			headers:    http.Header{"Session-Id": []string{"child"}},
			wantParent: "codex:parent",
		},
		{
			name:       "claude header",
			headers:    http.Header{"X-Claude-Code-Session-Id": []string{"child"}},
			wantParent: "claude:parent",
		},
	}

	payload := []byte(`{"parent_session_id":"parent"}`)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			info, ok := ExtractSessionInfo(test.headers, payload, nil)
			if !ok {
				t.Fatal("ExtractSessionInfo() returned no session")
			}
			if info.ParentSessionID != test.wantParent {
				t.Fatalf("ParentSessionID = %q, want %q", info.ParentSessionID, test.wantParent)
			}
		})
	}
}

func TestExtractSessionInfoRejectsControlCharacters(t *testing.T) {
	t.Parallel()

	payloadWithNewline := []byte(`{"session_id": "test\nsession"}`)
	if _, ok := ExtractSessionInfo(nil, payloadWithNewline, nil); ok {
		t.Fatal("ExtractSessionInfo accepted session_id with newline")
	}

	payloadWithNull := []byte(`{"session_id": "test\u0000session"}`)
	if _, ok := ExtractSessionInfo(nil, payloadWithNull, nil); ok {
		t.Fatal("ExtractSessionInfo accepted session_id with null byte")
	}
}

func TestExtractSessionInfoClaudeNestedParent(t *testing.T) {
	t.Parallel()

	// Nested metadata.user_id containing session_id and parent_session_id
	payload := []byte(`{
		"metadata": {
			"user_id": "{\"session_id\":\"child-session-123\",\"parent_session_id\":\"parent-session-456\",\"agent_id\":\"subagent-worker\"}"
		}
	}`)
	info, ok := ExtractSessionInfo(nil, payload, nil)
	if !ok {
		t.Fatal("ExtractSessionInfo failed on nested Claude user_id")
	}
	if info.SessionID != "claude:child-session-123:agent:subagent-worker" {
		t.Fatalf("expected child session with agent, got %q", info.SessionID)
	}
	if info.ParentSessionID != "claude:parent-session-456" {
		t.Fatalf("expected parent claude:parent-session-456, got %q", info.ParentSessionID)
	}

	// Explicit header + payload body parent_session_id
	headerPayload := []byte(`{"parent_session_id":"parent-session-789"}`)
	headers := http.Header{}
	headers.Set("X-Claude-Code-Session-Id", "header-child-123")
	info2, ok := ExtractSessionInfo(headers, headerPayload, nil)
	if !ok {
		t.Fatal("ExtractSessionInfo failed on header + body parent")
	}
	if info2.SessionID != "claude:header-child-123" {
		t.Fatalf("expected session claude:header-child-123, got %q", info2.SessionID)
	}
	if info2.ParentSessionID != "claude:parent-session-789" {
		t.Fatalf("expected parent claude:parent-session-789, got %q", info2.ParentSessionID)
	}
}

func TestExtractSessionInfoGeminiAndAntigravityHierarchy(t *testing.T) {
	t.Parallel()

	// Gemini cachedContent with parent_session_id
	geminiPayload := []byte(`{"cachedContent":"cache-child-1","parent_session_id":"cache-parent-1"}`)
	info, ok := ExtractSessionInfo(nil, geminiPayload, nil)
	if !ok {
		t.Fatal("ExtractSessionInfo failed on Gemini cachedContent")
	}
	if info.SessionID != "geminicache:cache-child-1" {
		t.Fatalf("expected session geminicache:cache-child-1, got %q", info.SessionID)
	}
	if info.ParentSessionID != "geminicache:cache-parent-1" {
		t.Fatalf("expected parent geminicache:cache-parent-1, got %q", info.ParentSessionID)
	}
	if info.AgentName != "subagent" {
		t.Fatalf("expected subagent, got %q", info.AgentName)
	}

	// Antigravity headers with parent
	headers := http.Header{}
	headers.Set("X-Http-Session-Id", "agy-child-2")
	headers.Set("X-Parent-Session-ID", "agy-parent-2")
	info2, ok := ExtractSessionInfo(headers, nil, nil)
	if !ok {
		t.Fatal("ExtractSessionInfo failed on Antigravity headers")
	}
	if info2.SessionID != "agy:agy-child-2" {
		t.Fatalf("expected session agy:agy-child-2, got %q", info2.SessionID)
	}
	if info2.ParentSessionID != "agy:agy-parent-2" {
		t.Fatalf("expected parent agy:agy-parent-2, got %q", info2.ParentSessionID)
	}
	if info2.AgentName != "subagent" {
		t.Fatalf("expected subagent, got %q", info2.AgentName)
	}
}

func TestExtractSessionInfoNestedAntigravityRequest(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"project_id": "proj-123",
		"request": {
			"parentSessionId": "parent-sess-456",
			"sessionId": "child-sess-789"
		}
	}`)
	info, ok := ExtractSessionInfo(nil, payload, nil)
	if !ok {
		t.Fatal("ExtractSessionInfo failed on nested Antigravity payload")
	}
	if info.SessionID != "session:child-sess-789" {
		t.Fatalf("SessionID = %q, want session:child-sess-789", info.SessionID)
	}
	if info.ParentSessionID != "session:parent-sess-456" {
		t.Fatalf("ParentSessionID = %q, want session:parent-sess-456", info.ParentSessionID)
	}
}

func TestExtractSessionInfoThreadAndConversation(t *testing.T) {
	t.Parallel()

	// 1. Thread Header
	threadHeaders := http.Header{
		"X-Thread-Id": []string{"thread-abc-123"},
	}
	info, ok := ExtractSessionInfo(threadHeaders, nil, nil)
	if !ok || info.ClientType != "openai-thread" || info.SessionID != "thread:thread-abc-123" {
		t.Fatalf("thread header failed: %+v", info)
	}

	// 2. Conversation Header
	convHeaders := http.Header{
		"X-Conversation-Id": []string{"conv-xyz-789"},
	}
	info, ok = ExtractSessionInfo(convHeaders, nil, nil)
	if !ok || info.ClientType != "conv" || info.SessionID != "conv:conv-xyz-789" {
		t.Fatalf("conversation header failed: %+v", info)
	}

	// 3. Payload Thread with parent
	threadPayload := []byte(`{
		"thread_id": "thread-child-1",
		"parent_thread_id": "thread-parent-1"
	}`)
	info, ok = ExtractSessionInfo(nil, threadPayload, nil)
	if !ok || info.ClientType != "openai-thread" || info.SessionID != "thread:thread-child-1" || info.ParentSessionID != "thread:thread-parent-1" {
		t.Fatalf("thread payload failed: %+v", info)
	}

	// 4. Payload Conversation with parent
	convPayload := []byte(`{
		"conversation_id": "conv-child-2",
		"parent_conversation_id": "conv-parent-2"
	}`)
	info, ok = ExtractSessionInfo(nil, convPayload, nil)
	if !ok || info.ClientType != "conv" || info.SessionID != "conv:conv-child-2" || info.ParentSessionID != "conv:conv-parent-2" {
		t.Fatalf("conv payload failed: %+v", info)
	}
}

func TestExtractSessionInfoSelfReferentialParentFiltered(t *testing.T) {
	t.Parallel()

	headers := http.Header{
		"X-Session-ID": []string{"self-session-123"},
	}
	payload := []byte(`{"parent_session_id": "self-session-123"}`)
	info, ok := ExtractSessionInfo(headers, payload, nil)
	if !ok {
		t.Fatal("ExtractSessionInfo failed")
	}
	if info.SessionID != "header:self-session-123" {
		t.Fatalf("SessionID = %q, want header:self-session-123", info.SessionID)
	}
	if info.ParentSessionID != "" {
		t.Fatalf("ParentSessionID = %q, want empty (self-referential parent must be rejected)", info.ParentSessionID)
	}
}

func TestDeprecatedInMemorySessionTreeStoreCompatibility(t *testing.T) {
	t.Parallel()

	var store SessionTreeStore = NewInMemorySessionTreeStore(100, 0)
	node := store.RecordNode(SessionTreeInfo{
		SessionID:       "child-1",
		ParentSessionID: "root-1",
		AgentName:       "reviewer",
		ClientType:      "pi",
	})
	if node == nil || node.SessionID != "child-1" || node.TreeDepth != 1 {
		t.Fatalf("RecordNode returned invalid node: %+v", node)
	}

	gotNode, ok := store.GetNode("child-1")
	if !ok || gotNode == nil || gotNode.SessionID != "child-1" {
		t.Fatalf("GetNode failed: %+v", gotNode)
	}

	tree := store.GetTree("root-1")
	if len(tree) != 1 || tree[0].SessionID != "child-1" {
		t.Fatalf("GetTree failed: %+v", tree)
	}

	ancestors := store.Ancestors("child-1")
	if len(ancestors) != 1 || ancestors[0] != "root-1" {
		t.Fatalf("Ancestors failed: %+v", ancestors)
	}

	if !store.UpdateAffinity("child-1", "auth-99", "claude", "sonnet") {
		t.Fatal("UpdateAffinity failed")
	}

	if store.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", store.Len())
	}

	store.Clear()
	if store.Len() != 0 {
		t.Fatalf("Len() after Clear = %d, want 0", store.Len())
	}
}

func TestExtractSessionInfoClaudePayloadOutranksGenericHeader(t *testing.T) {
	t.Parallel()

	headers := http.Header{
		"X-Session-ID": []string{"generic-fallback-session"},
	}
	payload := []byte(`{
		"metadata": {
			"user_id": "{\"session_id\":\"claude-real-session\",\"agent_id\":\"reviewer\"}"
		}
	}`)
	info, ok := ExtractSessionInfo(headers, payload, nil)
	if !ok {
		t.Fatal("ExtractSessionInfo failed")
	}
	if info.ClientType != "claude" || info.SessionID != "claude:claude-real-session:agent:reviewer" {
		t.Fatalf("expected claude identity to outrank generic header, got %+v", info)
	}
}

func TestExtractSessionInfoPromptCacheKeyAndClientReqAndMetadata(t *testing.T) {
	t.Parallel()

	// 1. prompt_cache_key
	payloadPCK := []byte(`{"prompt_cache_key":"prompt-key-123","conversation":{"id":"conv-456"}}`)
	info, ok := ExtractSessionInfo(nil, payloadPCK, nil)
	if !ok || info.SessionID != "pck:prompt-key-123" || info.ParentSessionID != "conv:conv-456" {
		t.Fatalf("pck extraction failed: %+v", info)
	}

	// 2. plain metadata.user_id
	payloadUser := []byte(`{"metadata":{"user_id":"user-999"}}`)
	info, ok = ExtractSessionInfo(nil, payloadUser, nil)
	if !ok || info.SessionID != "user:user-999" {
		t.Fatalf("user_id extraction failed: %+v", info)
	}

	// 3. X-Client-Request-Id
	headersReqID := http.Header{"X-Client-Request-Id": []string{"client-req-001"}}
	info, ok = ExtractSessionInfo(headersReqID, nil, nil)
	if !ok || info.SessionID != "clientreq:client-req-001" {
		t.Fatalf("clientreq extraction failed: %+v", info)
	}

	// 4. ExecutionSessionMetadataKey
	meta := map[string]any{
		"execution_session_id": "exec-777",
	}
	info, ok = ExtractSessionInfo(nil, nil, meta)
	if !ok || info.SessionID != "execution:exec-777" {
		t.Fatalf("execution extraction failed: %+v", info)
	}
}

func TestExtractSessionInfoNestedRequestAgent(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"request": {
			"sessionId": "child-sess-1",
			"metadata": {
				"agent_id": "worker-sub"
			}
		}
	}`)
	info, ok := ExtractSessionInfo(nil, payload, nil)
	if !ok {
		t.Fatal("ExtractSessionInfo failed on nested request")
	}
	if info.SessionID != "session:child-sess-1:agent:worker-sub" {
		t.Fatalf("SessionID = %q, want session:child-sess-1:agent:worker-sub", info.SessionID)
	}
	if info.ParentSessionID != "session:child-sess-1" {
		t.Fatalf("ParentSessionID = %q, want session:child-sess-1", info.ParentSessionID)
	}
	if info.AgentName != "worker-sub" {
		t.Fatalf("AgentName = %q, want worker-sub", info.AgentName)
	}
}

func TestExtractSessionInfoClaudeMetadataUserIDWithAgentHeader(t *testing.T) {
	t.Parallel()

	headers := http.Header{
		"X-Claude-Code-Agent-Id": []string{"subagent-uuid-123"},
	}
	payload := []byte(`{
		"metadata": {
			"user_id": "{\"device_id\":\"dev-1\",\"session_id\":\"main-sess-456\"}"
		}
	}`)
	info, ok := ExtractSessionInfo(headers, payload, nil)
	if !ok {
		t.Fatal("ExtractSessionInfo failed on Claude metadata user_id with agent header")
	}
	if info.ClientType != "claude" {
		t.Fatalf("ClientType = %q, want claude", info.ClientType)
	}
	if info.SessionID != "claude:main-sess-456:agent:subagent-uuid-123" {
		t.Fatalf("SessionID = %q, want claude:main-sess-456:agent:subagent-uuid-123", info.SessionID)
	}
	if info.ParentSessionID != "claude:main-sess-456" {
		t.Fatalf("ParentSessionID = %q, want claude:main-sess-456", info.ParentSessionID)
	}
	if info.AgentName != "subagent-uuid-123" {
		t.Fatalf("AgentName = %q, want subagent-uuid-123", info.AgentName)
	}
}

func TestExtractSessionInfoNestedSubagentIDAndUserID(t *testing.T) {
	t.Parallel()

	// 1. Nested request with subagent_id
	payloadSub := []byte(`{
		"request": {
			"sessionId": "main-sess-999",
			"metadata": {
				"subagent_id": "worker-sub-999"
			}
		}
	}`)
	info, ok := ExtractSessionInfo(nil, payloadSub, nil)
	if !ok {
		t.Fatal("ExtractSessionInfo failed on nested subagent_id request")
	}
	if info.SessionID != "session:main-sess-999:agent:worker-sub-999" {
		t.Fatalf("SessionID = %q, want session:main-sess-999:agent:worker-sub-999", info.SessionID)
	}
	if info.ParentSessionID != "session:main-sess-999" {
		t.Fatalf("ParentSessionID = %q, want session:main-sess-999", info.ParentSessionID)
	}
	if info.AgentName != "worker-sub-999" {
		t.Fatalf("AgentName = %q, want worker-sub-999", info.AgentName)
	}

	// 2. Nested request with plain user_id
	payloadUser := []byte(`{
		"request": {
			"metadata": {
				"user_id": "nested-user-123"
			}
		}
	}`)
	infoUser, okUser := ExtractSessionInfo(nil, payloadUser, nil)
	if !okUser {
		t.Fatal("ExtractSessionInfo failed on nested user_id request")
	}
	if infoUser.SessionID != "user:nested-user-123" {
		t.Fatalf("SessionID = %q, want user:nested-user-123", infoUser.SessionID)
	}

	// 3. Nested request with promptCacheKey
	payloadPCK := []byte(`{
		"request": {
			"promptCacheKey": "nested-pck-456"
		}
	}`)
	infoPCK, okPCK := ExtractSessionInfo(nil, payloadPCK, nil)
	if !okPCK {
		t.Fatal("ExtractSessionInfo failed on nested promptCacheKey request")
	}
	if infoPCK.SessionID != "pck:nested-pck-456" {
		t.Fatalf("SessionID = %q, want pck:nested-pck-456", infoPCK.SessionID)
	}

	// 4. Nested promptCacheKey when top-level prompt_cache_key is empty string
	payloadPCKShadow := []byte(`{
		"prompt_cache_key": "",
		"request": {
			"promptCacheKey": "nested-pck-valid"
		}
	}`)
	infoShadow, okShadow := ExtractSessionInfo(nil, payloadPCKShadow, nil)
	if !okShadow {
		t.Fatal("ExtractSessionInfo failed on shadowed promptCacheKey request")
	}
	if infoShadow.SessionID != "pck:nested-pck-valid" {
		t.Fatalf("SessionID = %q, want pck:nested-pck-valid", infoShadow.SessionID)
	}
}
