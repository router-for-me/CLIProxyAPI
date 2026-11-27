package session

import (
	"net/http"
	"strings"
	"testing"

	cpasession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
)

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

var corpus = []struct {
	name   string
	h      http.Header
	meta   map[string]any
	id     string
	parent string
	kind   Kind
}{
	{"claude main", hdr("X-Claude-Code-Session-Id", "11111111-1111-4111-8111-111111111111"), nil, "claude:11111111-1111-4111-8111-111111111111", "", Main},
	{"claude main agent header", hdr("X-Claude-Code-Session-Id", "s1", "X-Claude-Code-Agent-Id", "main"), nil, "claude:s1", "", Main},
	{"claude subagent", hdr("X-Claude-Code-Session-Id", "s1", "X-Claude-Code-Agent-Id", "a7"), nil, "claude:s1:agent:a7", "claude:s1", Subagent},
	{"claude nested subagent", hdr("X-Claude-Code-Session-Id", "s1", "X-Claude-Code-Agent-Id", "a8", "X-Claude-Code-Parent-Agent-Id", "a7"), nil, "claude:s1:agent:a8", "claude:s1:agent:a7", Subagent},
	{"codex session", hdr("Session-Id", "c1"), nil, "codex:c1", "", Main},
	{"codex session underscore", hdr("Session_id", "c1"), nil, "codex:c1", "", Main},
	{"codex same thread", hdr("Session-Id", "c1", "Thread-Id", "c1"), nil, "codex:c1", "", Main},
	{"codex thread child", hdr("Session-Id", "c1", "Thread-Id", "t2"), nil, "codex:t2", "codex:c1", Subagent},
	{"codex parent header", hdr("Session-Id", "c2", "X-Codex-Parent-Thread-Id", "c1"), nil, "codex:c2", "codex:c1", Subagent},
	{"codex turn metadata", hdr("X-Codex-Turn-Metadata", `{"session_id":"c1","thread_id":"t9","subagent_kind":"thread_spawn","parent_thread_id":"c1"}`), nil, "codex:t9", "codex:c1", Subagent},
	{"codex named agent", hdr("Session-Id", "c1", "X-Openai-Subagent", "true", "X-Codex-Turn-Metadata", `{"agent_name":"/root/reviewer"}`), nil, "codex:c1:agent:reviewer", "codex:c1", Subagent},
	{"codex fork", hdr("Session-Id", "c3", "X-Codex-Turn-Metadata", `{"forked_from_thread_id":"c1"}`), nil, "codex:c3", "codex:c1", Fork},
	{"generic session", hdr("X-Session-ID", "g1", "X-Parent-Session-ID", "g0"), nil, "header:g1", "header:g0", Subagent},
	{"opencode affinity", hdr("X-Session-Affinity", "o1"), nil, "affinity:o1", "", Main},
	{"task", hdr("X-Task-Id", "k1", "X-Parent-Task-Id", "k0"), nil, "task:k1", "task:k0", Subagent},
	{"conversation", hdr("X-Conversation-Id", "v1"), nil, "conv:v1", "", Main},
	{"thread", hdr("X-Thread-Id", "th1"), nil, "thread:th1", "", Main},
	{"websocket execution", nil, map[string]any{"execution_session_id": "ws1"}, "execution:ws1", "", Main},
	{"headers win over websocket", hdr("Session-Id", "c1"), map[string]any{"execution_session_id": "ws1"}, "codex:c1", "", Main},
	{"control characters rejected", hdr("X-Claude-Code-Session-Id", "bad\x01id"), nil, "", "", ""},
	{"oversized rejected", hdr("Session-Id", strings.Repeat("x", 300)), nil, "", "", ""},
	{"nothing", hdr("Authorization", "Bearer sk-secret", "Idempotency-Key", "i1"), nil, "", "", ""},
}

func TestFromRequest(t *testing.T) {
	for _, tc := range corpus {
		t.Run(tc.name, func(t *testing.T) {
			ref, ok := FromRequest(tc.h, tc.meta)
			if ok != (tc.id != "") || ref.ID != tc.id || ref.ParentID != tc.parent || ref.Kind != tc.kind {
				t.Fatalf("got %+v ok=%v", ref, ok)
			}
			if ok && (ref.Key == "" || strings.Contains(ref.Key, tc.id) || (tc.parent != "") != (ref.ParentKey != "")) {
				t.Fatalf("key must be a hash and parent key must follow parent: %+v", ref)
			}
		})
	}
}

// TestParityWithCPA pins the extractor to CPA's own header precedence so an
// SDK upgrade that changes identities fails here instead of silently
// splitting threads across accounts.
func TestParityWithCPA(t *testing.T) {
	for _, tc := range corpus {
		t.Run(tc.name, func(t *testing.T) {
			info, ok := cpasession.ExtractSessionInfo(tc.h, nil, tc.meta)
			ref, mine := FromRequest(tc.h, tc.meta)
			if ok != mine || info.SessionID != ref.ID || info.ParentSessionID != ref.ParentID {
				t.Fatalf("CPA %q/%q ok=%v, plugin %q/%q ok=%v", info.SessionID, info.ParentSessionID, ok, ref.ID, ref.ParentID, mine)
			}
		})
	}
}

func TestRequestIDsNeverIdentifySessions(t *testing.T) {
	for _, h := range []http.Header{
		hdr("X-Client-Request-Id", "per-request-1"),
		hdr("X-Request-Id", "r1"),
		hdr("Idempotency-Key", "i1"),
	} {
		if ref, ok := FromRequest(h, nil); ok {
			t.Fatalf("request ID became a session: %v → %+v", h, ref)
		}
	}
	if _, ok := FromUsage("clientreq:abc", "", "k"); ok {
		t.Fatal("usage record clientreq: session accepted")
	}
}

func TestCallerScopeIsolatesAndMatchesUsage(t *testing.T) {
	h := hdr("X-Claude-Code-Session-Id", "s1")
	a, _ := FromRequest(h, map[string]any{"caller_scope": cpasession.CallerScope("key-a")})
	b, _ := FromRequest(h, map[string]any{"caller_scope": cpasession.CallerScope("key-b")})
	if a.Key == b.Key {
		t.Fatal("different callers share a session key")
	}
	u, ok := FromUsage("claude:s1", "", "key-a")
	if !ok || u.Key != a.Key {
		t.Fatalf("usage-derived key %q does not match pick key %q", u.Key, a.Key)
	}
	if CallerScope("key-a") != cpasession.CallerScope("key-a") {
		t.Fatal("caller scope diverged from CPA")
	}
}
