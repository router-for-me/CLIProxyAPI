package session

import (
	"net/http"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestExtractClassificationAndAliases(t *testing.T) {
	claudeUserID := `{"device_id":"d","account_uuid":"","session_id":"11111111-1111-4111-8111-111111111111"}`

	tests := []struct {
		name           string
		headers        http.Header
		payload        string
		metadata       map[string]any
		wantID         string
		wantAliases    []string
		wantSource     string
		wantConfidence string
		wantScope      string
	}{
		{
			name:           "multiple explicit headers collected as representative ID and aliases",
			headers:        http.Header{"X-Gateway-Session-Id": {"gw-1"}, "X-Claude-Code-Session-Id": {"cc-1"}, "Session-Id": {"cx-1"}},
			wantID:         "gateway:gw-1",
			wantAliases:    []string{"claude:cc-1", "codex:cx-1"},
			wantSource:     SourceGatewayHeader,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			name:           "claude code header and metadata collected as representative ID and aliases",
			headers:        http.Header{"X-Claude-Code-Session-Id": {"cc-1"}},
			payload:        `{"metadata":{"user_id":` + quote(claudeUserID) + `}}`,
			wantID:         "claude:cc-1",
			wantAliases:    []string{"claude:11111111-1111-4111-8111-111111111111"},
			wantSource:     SourceClientNativeHeader,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			name:           "claude code metadata only",
			payload:        `{"metadata":{"user_id":` + quote(claudeUserID) + `}}`,
			wantID:         "claude:11111111-1111-4111-8111-111111111111",
			wantSource:     SourceMetadataUserID,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			name:           "claude code legacy metadata suffix",
			payload:        `{"metadata":{"user_id":"user_abc_account__session_11111111-1111-4111-8111-111111111111"}}`,
			wantID:         "claude:11111111-1111-4111-8111-111111111111",
			wantSource:     SourceMetadataUserID,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			name:           "codex hyphenated session header with prompt cache key alias",
			headers:        http.Header{"Session-Id": {"019f7e3b"}, "Thread-Id": {"019f7e3b"}},
			payload:        `{"prompt_cache_key":"019f7e3b"}`,
			wantID:         "codex:019f7e3b",
			wantAliases:    []string{"pck:019f7e3b"},
			wantSource:     SourceClientNativeHeader,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			name:           "opencode session header and generic header aliases",
			headers:        http.Header{"X-Opencode-Session": {"ses_a"}, "X-Session-Id": {"ses_b"}},
			wantID:         "opencode:ses_a",
			wantAliases:    []string{"header:ses_b"},
			wantSource:     SourceClientNativeHeader,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			name:           "opencode generic and affinity headers",
			headers:        http.Header{"X-Session-Id": {"ses_c"}, "X-Session-Affinity": {"ses_c"}},
			wantID:         "header:ses_c",
			wantAliases:    []string{"affinity:ses_c"},
			wantSource:     SourceClientNativeHeader,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			name:           "affinity header alone",
			headers:        http.Header{"X-Session-Affinity": {"ses_d"}},
			wantID:         "affinity:ses_d",
			wantSource:     SourceClientNativeHeader,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			// The header tracks the current thread, so a real root identifier always
			// wins and the header contributes no alias. pi sends prompt_cache_key
			// with the same value, which is what routing uses instead.
			name:           "client request id yields to a real root identifier",
			headers:        http.Header{"X-Client-Request-Id": {"pi-1"}},
			payload:        `{"prompt_cache_key":"pi-1"}`,
			wantID:         "pck:pi-1",
			wantAliases:    nil,
			wantSource:     SourcePromptCacheKey,
			wantConfidence: ConfidenceMedium,
			wantScope:      ScopeSession,
		},
		{
			// A client that sends nothing else still gets affinity from it.
			name:           "client request id is the last-resort root",
			headers:        http.Header{"X-Client-Request-Id": {"pi-1"}},
			wantID:         "clientreq:pi-1",
			wantSource:     SourceClientRequestID,
			wantConfidence: ConfidenceMedium,
			wantScope:      ScopeSession,
		},
		{
			// OpenCode sub-agent: its own session plus the root in the parent header.
			// The parent shares the "header:" prefix so both requests land in one
			// alias group and therefore on one credential.
			name:           "opencode subagent joins the parent session alias group",
			headers:        http.Header{"X-Session-Id": {"ses_child"}, "X-Session-Affinity": {"ses_child"}, "X-Parent-Session-Id": {"ses_root"}},
			wantID:         "header:ses_child",
			wantAliases:    []string{"affinity:ses_child", "header:ses_root"},
			wantSource:     SourceClientNativeHeader,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			name:           "body metadata session id",
			payload:        `{"metadata":{"session_id":"meta-1"}}`,
			wantID:         "session:meta-1",
			wantSource:     SourceBodySession,
			wantConfidence: ConfidenceMedium,
			wantScope:      ScopeSession,
		},
		{
			name:           "prompt cache key keeps conversation alias",
			payload:        `{"prompt_cache_key":"cache-1","conversation":{"id":"conv_1"}}`,
			wantID:         "pck:cache-1",
			wantAliases:    []string{"conv:conv_1"},
			wantSource:     SourcePromptCacheKey,
			wantConfidence: ConfidenceMedium,
			wantScope:      ScopeSession,
		},
		{
			name:           "conversation string form",
			payload:        `{"conversation":"conv_2"}`,
			wantID:         "conv:conv_2",
			wantSource:     SourceBodyConversation,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			name:           "bare metadata user id is user scope fallback",
			payload:        `{"metadata":{"user_id":"user_123"}}`,
			wantID:         "user:user_123",
			wantSource:     SourceLegacyMetadataUser,
			wantConfidence: ConfidenceLow,
			wantScope:      ScopeUser,
		},
		{
			name:           "bare metadata user id is omitted when conversation_id exists",
			payload:        `{"conversation_id":"conv-123","metadata":{"user_id":"user_123"}}`,
			wantID:         "conv:conv-123",
			wantAliases:    nil,
			wantSource:     SourceBodyConversation,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeSession,
		},
		{
			name:           "execution metadata fallback when no root session exists",
			metadata:       map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "exec-1"},
			wantID:         "execution:exec-1",
			wantSource:     SourceExecutionSession,
			wantConfidence: ConfidenceHigh,
			wantScope:      ScopeTransport,
		},
		{
			name:           "thread header fallback when no root session exists",
			headers:        http.Header{"Thread-Id": {"thread-1"}},
			wantID:         "thread:thread-1",
			wantSource:     SourceClientNativeHeader,
			wantConfidence: ConfidenceMedium,
			wantScope:      ScopeThread,
		},
		{
			name:           "derived identity fallback",
			metadata:       map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "ctx:v1:abc"},
			payload:        `{"messages":[{"role":"user","content":"hello"}]}`,
			wantID:         "derived:ctx:v1:abc",
			wantSource:     SourceDerived,
			wantConfidence: ConfidenceMedium,
			wantScope:      ScopeSession,
		},
		{
			name:           "message hash low confidence fallback",
			payload:        `{"messages":[{"role":"user","content":"hello"}]}`,
			wantID:         "msg:",
			wantSource:     SourceMessageHash,
			wantConfidence: ConfidenceLow,
			wantScope:      ScopeSession,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Extract(tt.headers, []byte(tt.payload), tt.metadata)
			if tt.wantID == "msg:" {
				if !strings.HasPrefix(got.ID, "msg:") {
					t.Fatalf("ID = %q, want msg: prefix", got.ID)
				}
			} else if got.ID != tt.wantID {
				t.Fatalf("ID = %q, want %q", got.ID, tt.wantID)
			}
			if !equalStrings(got.Aliases, tt.wantAliases) {
				t.Fatalf("Aliases = %#v, want %#v", got.Aliases, tt.wantAliases)
			}
			if got.Source != tt.wantSource {
				t.Fatalf("Source = %q, want %q", got.Source, tt.wantSource)
			}
			if got.Confidence != tt.wantConfidence {
				t.Fatalf("Confidence = %q, want %q", got.Confidence, tt.wantConfidence)
			}
			if got.Scope != tt.wantScope {
				t.Fatalf("Scope = %q, want %q", got.Scope, tt.wantScope)
			}
		})
	}
}

func TestExtractCapturesThreadsWithoutOverridingRootSession(t *testing.T) {
	headers := http.Header{
		"Session-Id":                 {"root-1"},
		"Thread-Id":                  {"thread-1"},
		"X-Codex-Parent-Thread-Id":   {"thread-parent"},
		"X-Codex-Turn-Metadata":      {`{"turn_id":"turn-1"}`},
		"X-Gateway-Parent-Thread-Id": {"gateway-parent"},
	}

	got := Extract(headers, nil, map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "transport-1"})

	if got.ID != "codex:root-1" {
		t.Fatalf("ID = %q, want codex:root-1", got.ID)
	}
	if got.Scope != ScopeSession {
		t.Fatalf("Scope = %q, want %q", got.Scope, ScopeSession)
	}
	for _, id := range got.IDs() {
		if id == "thread:thread-1" || id == "execution:transport-1" {
			t.Fatalf("non-root identifier %q was added as a session alias", id)
		}
	}
	if got.ThreadID != "thread-1" {
		t.Fatalf("ThreadID = %q, want thread-1", got.ThreadID)
	}
	// X-Gateway-Parent-Thread-Id is listed first, so the unified protocol wins.
	if got.ParentThreadID != "gateway-parent" {
		t.Fatalf("ParentThreadID = %q, want gateway-parent", got.ParentThreadID)
	}
	if got.ClientType != ClientTypeCodex {
		t.Fatalf("ClientType = %q, want %q", got.ClientType, ClientTypeCodex)
	}
}

func TestExtractReportsThreadsWithoutAnySession(t *testing.T) {
	got := Extract(http.Header{"X-Gateway-Thread-Id": {"thread-only"}}, nil, nil)

	if got.ID != "thread:thread-only" {
		t.Fatalf("ID = %q, want thread:thread-only", got.ID)
	}
	if got.ThreadID != "thread-only" {
		t.Fatalf("ThreadID = %q, want thread-only", got.ThreadID)
	}
}

func TestExtractRejectsUnsafeExplicitIdentifiers(t *testing.T) {
	tests := []struct {
		name    string
		headers http.Header
	}{
		{name: "control character", headers: http.Header{"X-Gateway-Session-Id": {"bad\nvalue"}}},
		{name: "oversized", headers: http.Header{"X-Gateway-Session-Id": {strings.Repeat("a", 257)}}},
		{name: "blank", headers: http.Header{"X-Gateway-Session-Id": {"   "}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Extract(tt.headers, nil, nil); got.ID != "" {
				t.Fatalf("ID = %q, want empty", got.ID)
			}
		})
	}
}

func TestExtractSkipsInvalidMultiValueHeaderEntries(t *testing.T) {
	headers := http.Header{"X-Session-Id": {"bad\nvalue", "good-value"}}

	if got := Extract(headers, nil, nil); got.ID != "header:good-value" {
		t.Fatalf("ID = %q, want header:good-value", got.ID)
	}
}

func TestExtractHeaderNamesAreCaseInsensitive(t *testing.T) {
	headers := http.Header{}
	headers["x-claude-code-session-id"] = []string{"cc-lower"}

	if got := Extract(headers, nil, nil); got.ID != "claude:cc-lower" {
		t.Fatalf("ID = %q, want claude:cc-lower", got.ID)
	}
}

func TestExtractReturnsEmptyIdentityWithoutSignals(t *testing.T) {
	got := Extract(nil, nil, nil)

	if !got.IsZero() {
		t.Fatalf("Extract() = %#v, want zero identity", got)
	}
	if got.FallbackID() != "" {
		t.Fatalf("FallbackID() = %q, want empty", got.FallbackID())
	}
	if len(got.IDs()) != 0 {
		t.Fatalf("IDs() = %#v, want empty", got.IDs())
	}
}

func TestIdentityIDsDeduplication(t *testing.T) {
	identity := Identity{
		ID:      "claude:cc-1",
		Aliases: []string{"affinity:aff-1", "claude:cc-1", "conv:conv-1", "affinity:aff-1", "  "},
	}
	got := identity.IDs()
	want := []string{"claude:cc-1", "affinity:aff-1", "conv:conv-1"}
	if !equalStrings(got, want) {
		t.Fatalf("Identity.IDs() = %#v, want %#v", got, want)
	}
}

// TestExtractOpenCodeSubagentSharesRootIdentifier locks in the routing outcome:
// the parent and its sub-agent must share at least one identifier so the affinity
// cache resolves them to a single credential.
func TestExtractOpenCodeSubagentSharesRootIdentifier(t *testing.T) {
	parent := Extract(http.Header{
		"X-Session-Id":       {"ses_root"},
		"X-Session-Affinity": {"ses_root"},
		"User-Agent":         {"opencode/1.18.4 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14"},
	}, nil, nil)
	child := Extract(http.Header{
		"X-Session-Id":        {"ses_child"},
		"X-Session-Affinity":  {"ses_child"},
		"X-Parent-Session-Id": {"ses_root"},
		"User-Agent":          {"opencode/1.18.4 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14"},
	}, nil, nil)

	shared := ""
	for _, parentID := range parent.IDs() {
		for _, childID := range child.IDs() {
			if parentID == childID {
				shared = parentID
			}
		}
	}
	if shared == "" {
		t.Fatalf("parent %v and subagent %v share no identifier, so they would bind to different credentials", parent.IDs(), child.IDs())
	}
	if child.ParentThreadID != "ses_root" {
		t.Fatalf("ParentThreadID = %q, want ses_root", child.ParentThreadID)
	}
	if child.ThreadID != "" {
		t.Fatalf("ThreadID = %q, want empty: OpenCode reports no thread header", child.ThreadID)
	}
}

// TestExtractClaudeCodeSubagentThreads mirrors a captured depth-2 Claude Code run:
// every level repeats the root session, the main agent sends no agent identifier,
// and the parent identifier only appears from depth 2 onwards.
func TestExtractClaudeCodeSubagentThreads(t *testing.T) {
	const root = "5fea96b2-0000-4000-8000-000000000000"
	base := func() http.Header {
		return http.Header{
			"X-Claude-Code-Session-Id": {root},
			"X-App":                    {"cli"},
			"Anthropic-Beta":           {"claude-code-20250219"},
			"User-Agent":               {"claude-cli/2.1.220 (external, sdk-cli)"},
		}
	}

	main := Extract(base(), nil, nil)
	if main.ID != "claude:"+root {
		t.Fatalf("main ID = %q, want claude:%s", main.ID, root)
	}
	if main.ThreadID != "" || main.ParentThreadID != "" {
		t.Fatalf("main agent reported thread %q parent %q, want both empty", main.ThreadID, main.ParentThreadID)
	}

	depth1Headers := base()
	depth1Headers.Set("X-Claude-Code-Agent-Id", "a0f476550f303bf2d")
	depth1 := Extract(depth1Headers, nil, nil)
	if depth1.ID != "claude:"+root {
		t.Fatalf("depth-1 ID = %q, want claude:%s", depth1.ID, root)
	}
	if depth1.ThreadID != "a0f476550f303bf2d" {
		t.Fatalf("depth-1 ThreadID = %q, want a0f476550f303bf2d", depth1.ThreadID)
	}
	if depth1.ParentThreadID != "" {
		t.Fatalf("depth-1 ParentThreadID = %q, want empty", depth1.ParentThreadID)
	}

	depth2Headers := base()
	depth2Headers.Set("X-Claude-Code-Agent-Id", "a15f3b017b93ec1d8")
	depth2Headers.Set("X-Claude-Code-Parent-Agent-Id", "a0f476550f303bf2d")
	depth2 := Extract(depth2Headers, nil, nil)
	if depth2.ID != "claude:"+root {
		t.Fatalf("depth-2 ID = %q, want claude:%s", depth2.ID, root)
	}
	if depth2.ThreadID != "a15f3b017b93ec1d8" {
		t.Fatalf("depth-2 ThreadID = %q, want a15f3b017b93ec1d8", depth2.ThreadID)
	}
	if depth2.ParentThreadID != "a0f476550f303bf2d" {
		t.Fatalf("depth-2 ParentThreadID = %q, want a0f476550f303bf2d", depth2.ParentThreadID)
	}
}

// TestExtractCodexSubagentKeepsRootSession mirrors a captured Codex sub-agent turn:
// session-id and prompt_cache_key stay on the root while thread-id moves to the
// child, and x-client-request-id follows the thread rather than the session.
func TestExtractCodexSubagentKeepsRootSession(t *testing.T) {
	const root = "019fa90b-31a4-7841-b252-0a2d5dafbbdc"
	const childThread = "019fa90b-3219-73d2-9aad-9de97611969e"

	got := Extract(http.Header{
		"Session-Id":               {root},
		"Thread-Id":                {childThread},
		"X-Codex-Parent-Thread-Id": {root},
		"X-Client-Request-Id":      {childThread},
		"X-Openai-Subagent":        {"collab_spawn"},
		"User-Agent":               {"codex_exec/0.145.0 (Mac OS 26.5.2; arm64)"},
	}, []byte(`{"prompt_cache_key":"`+root+`"}`), nil)

	if got.ID != "codex:"+root {
		t.Fatalf("ID = %q, want codex:%s", got.ID, root)
	}
	if got.ThreadID != childThread {
		t.Fatalf("ThreadID = %q, want %s", got.ThreadID, childThread)
	}
	if got.ParentThreadID != root {
		t.Fatalf("ParentThreadID = %q, want %s", got.ParentThreadID, root)
	}
	for _, id := range got.IDs() {
		if strings.Contains(id, childThread) {
			t.Fatalf("thread identifier %q leaked into the root alias group %v", id, got.IDs())
		}
	}
}

// TestExtractWithoutAnyRootSignalStaysZero pins the contract that observation
// fields never manufacture a session. A request with no root identifier must still
// report IsZero and contribute no routing identifier, even when the client type,
// turn metadata and request kind are all recognised.
func TestExtractWithoutAnyRootSignalStaysZero(t *testing.T) {
	got := Extract(http.Header{
		"User-Agent":            {"codex_exec/0.145.0 (Mac OS 26.5.2; arm64)"},
		"X-Codex-Turn-Metadata": {`{"request_kind":"prewarm","thread_source":"user","turn_id":"019fa90b-3234-73d2-a061-0622e5f8c57c"}`},
		"X-App":                 {"cli-bg"},
	}, nil, nil)

	if !got.IsZero() {
		t.Fatalf("IsZero() = false for identity %#v, want true", got)
	}
	if got.ID != "" {
		t.Fatalf("ID = %q, want empty", got.ID)
	}
	if ids := got.IDs(); len(ids) != 0 {
		t.Fatalf("IDs() = %#v, want none: observation fields must not create a routing key", ids)
	}
	// The observations are still reported for logging.
	if got.ClientType != ClientTypeCodex {
		t.Fatalf("ClientType = %q, want %q", got.ClientType, ClientTypeCodex)
	}
	if got.RequestKind != "prewarm" {
		t.Fatalf("RequestKind = %q, want prewarm", got.RequestKind)
	}
}

// TestExtractTurnObservations covers the observation-only fields: they are read
// from what the client reports, bounded, and never promoted to a session alias.
func TestExtractTurnObservations(t *testing.T) {
	const root = "019fa90b-31a4-7841-b252-0a2d5dafbbdc"
	const childThread = "019fa90b-3219-73d2-9aad-9de97611969e"
	const turnID = "019fa90b-3234-73d2-a061-0622e5f8c57c"

	t.Run("codex subagent turn", func(t *testing.T) {
		metadata := `{"installation_id":"i","session_id":"` + root + `","thread_id":"` + childThread +
			`","turn_id":"` + turnID + `","request_kind":"turn","parent_thread_id":"` + root +
			`","subagent_kind":"thread_spawn","thread_source":"subagent"}`
		got := Extract(http.Header{
			"Session-Id":            {root},
			"Thread-Id":             {childThread},
			"X-Codex-Turn-Metadata": {metadata},
			"User-Agent":            {"codex_exec/0.145.0 (Mac OS 26.5.2; arm64)"},
		}, nil, nil)

		if got.RequestKind != "turn" {
			t.Fatalf("RequestKind = %q, want turn", got.RequestKind)
		}
		if got.ThreadSource != "subagent" {
			t.Fatalf("ThreadSource = %q, want subagent", got.ThreadSource)
		}
		if got.TurnID != turnID {
			t.Fatalf("TurnID = %q, want %s", got.TurnID, turnID)
		}
		for _, id := range got.IDs() {
			if strings.Contains(id, turnID) {
				t.Fatalf("turn identifier leaked into the alias group %v", got.IDs())
			}
		}
	})

	t.Run("codex housekeeping kinds are reported verbatim", func(t *testing.T) {
		for _, kind := range []string{"compact", "review", "title", "prewarm"} {
			got := Extract(http.Header{
				"Session-Id":            {root},
				"X-Codex-Turn-Metadata": {`{"request_kind":"` + kind + `","thread_source":"user"}`},
			}, nil, nil)
			if got.RequestKind != kind {
				t.Fatalf("RequestKind = %q, want %q", got.RequestKind, kind)
			}
		}
	})

	t.Run("claude code background", func(t *testing.T) {
		got := Extract(http.Header{
			"X-Claude-Code-Session-Id": {root},
			"X-App":                    {"cli-bg"},
			"Anthropic-Beta":           {"claude-code-20250219"},
		}, nil, nil)
		if got.RequestKind != RequestKindBackground {
			t.Fatalf("RequestKind = %q, want %q", got.RequestKind, RequestKindBackground)
		}
	})

	t.Run("hostile metadata is rejected", func(t *testing.T) {
		cases := []struct{ name, metadata string }{
			{"not json", `not-json`},
			{"control characters", `{"request_kind":"tu\u0001rn"}`},
			{"oversized value", `{"request_kind":"` + strings.Repeat("k", observationValueMaxLength+1) + `"}`},
			{"oversized payload", `{"request_kind":"turn","pad":"` + strings.Repeat("p", codexTurnMetadataMaxBytes) + `"}`},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got := Extract(http.Header{
					"Session-Id":            {root},
					"X-Codex-Turn-Metadata": {tc.metadata},
				}, nil, nil)
				if got.ID != "codex:"+root {
					t.Fatalf("ID = %q, want codex:%s", got.ID, root)
				}
				if got.RequestKind != "" {
					t.Fatalf("RequestKind = %q, want empty", got.RequestKind)
				}
			})
		}
	})
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func quote(value string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, r := range value {
		if r == '"' || r == '\\' {
			builder.WriteByte('\\')
		}
		builder.WriteRune(r)
	}
	builder.WriteByte('"')
	return builder.String()
}
