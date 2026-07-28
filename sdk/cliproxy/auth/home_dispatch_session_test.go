package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	internalhome "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type dispatchSessionCaptureDispatcher struct {
	mu       sync.Mutex
	sessions []internalhome.DispatchSession
	headers  []http.Header
}

func (*dispatchSessionCaptureDispatcher) HeartbeatOK() bool { return true }

func (d *dispatchSessionCaptureDispatcher) RPopAuth(_ context.Context, _ string, session internalhome.DispatchSession, headers http.Header, _ int) ([]byte, error) {
	d.mu.Lock()
	d.sessions = append(d.sessions, session)
	d.headers = append(d.headers, headers.Clone())
	d.mu.Unlock()
	return json.Marshal(homeAuthDispatchResponse{Auth: Auth{
		ID:       "home-dispatch-session-auth",
		Provider: "home-dispatch-session",
		Status:   StatusActive,
	}})
}

func (*dispatchSessionCaptureDispatcher) AbortAmbiguousDispatch() {}

func (d *dispatchSessionCaptureDispatcher) last() (internalhome.DispatchSession, http.Header) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.sessions) == 0 {
		return internalhome.DispatchSession{}, nil
	}
	return d.sessions[len(d.sessions)-1], d.headers[len(d.headers)-1]
}

func dispatchOnce(t *testing.T, manager *Manager, opts cliproxyexecutor.Options) {
	t.Helper()
	selection, err := manager.pickHomeDispatchSelection(context.Background(), "gpt-test", opts)
	if err != nil {
		t.Fatalf("pickHomeDispatchSelection() error = %v", err)
	}
	selection.End("test_complete")
}

func newDispatchSessionManager(t *testing.T) (*Manager, *dispatchSessionCaptureDispatcher) {
	t.Helper()
	dispatcher := &dispatchSessionCaptureDispatcher{}
	manager := newHomeSelectionTestManager(t, dispatcher)
	manager.RegisterExecutor(schedulerTestExecutor{provider: "home-dispatch-session"})
	return manager, dispatcher
}

func TestHomeDispatchSendsStructuredIdentity(t *testing.T) {
	manager, dispatcher := newDispatchSessionManager(t)

	dispatchOnce(t, manager, cliproxyexecutor.Options{
		Headers:         http.Header{"User-Agent": {"codex_exec/0.144.6"}, "Session-Id": {"019f7e3b"}, "Thread-Id": {"019f7e3b"}},
		OriginalRequest: []byte(`{"prompt_cache_key":"019f7e3b"}`),
	})

	session, _ := dispatcher.last()
	if session.ID != "codex:019f7e3b" {
		t.Fatalf("session.ID = %q, want codex:019f7e3b", session.ID)
	}
	if session.Source == "" || session.Confidence == "" || session.Scope == "" {
		t.Fatalf("session classification is incomplete: %#v", session)
	}
	if session.ClientType != "codex" {
		t.Fatalf("session.ClientType = %q, want codex", session.ClientType)
	}
	if session.ThreadID != "019f7e3b" {
		t.Fatalf("session.ThreadID = %q, want 019f7e3b", session.ThreadID)
	}
	if !session.ClientProvided {
		t.Fatal("session.ClientProvided = false, want true")
	}
}

// A CPA node that only ever sees the conversation ID must still tell Home about
// the prompt cache key it was canonicalized onto, otherwise Home sees two sessions.
func TestHomeDispatchSendsAliasesForCanonicalizedSessions(t *testing.T) {
	manager, dispatcher := newDispatchSessionManager(t)

	dispatchOnce(t, manager, cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"prompt_cache_key":"cache-1","conversation":{"id":"conv_1"}}`),
	})
	combined, _ := dispatcher.last()
	if combined.ID != "pck:cache-1" {
		t.Fatalf("combined session.ID = %q, want pck:cache-1", combined.ID)
	}
	if len(combined.Aliases) != 1 || combined.Aliases[0] != "conv:conv_1" {
		t.Fatalf("combined session.Aliases = %#v, want [conv:conv_1]", combined.Aliases)
	}

	dispatchOnce(t, manager, cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"conversation":{"id":"conv_1"}}`),
	})
	conversationOnly, _ := dispatcher.last()
	if conversationOnly.ID != "pck:cache-1" {
		t.Fatalf("conversation-only session.ID = %q, want the canonical pck:cache-1", conversationOnly.ID)
	}
	if !containsString(conversationOnly.Aliases, "conv:conv_1") {
		t.Fatalf("conversation-only session.Aliases = %#v, want the observed conv:conv_1", conversationOnly.Aliases)
	}
}

// Home receives every recognized identifier while the representative ID remains
// available through the legacy X-Session-ID compatibility header.
func TestHomeDispatchPublishesRepresentativeAndAliases(t *testing.T) {
	manager, dispatcher := newDispatchSessionManager(t)

	dispatchOnce(t, manager, cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Claude-Code-Session-Id": {"strong-session"},
			"X-Session-Id":             {"weak-session"},
		},
		OriginalRequest: []byte(`{"conversation_id":"body-session"}`),
	})

	session, headers := dispatcher.last()
	if session.ID != "claude:strong-session" {
		t.Fatalf("session.ID = %q, want claude:strong-session", session.ID)
	}
	wantAliases := []string{"header:weak-session", "conv:body-session"}
	if !equalSessionAliases(session.Aliases, wantAliases) {
		t.Fatalf("session.Aliases = %#v, want %#v", session.Aliases, wantAliases)
	}
	if got := headers.Get("X-Session-ID"); got != "claude:strong-session" {
		t.Fatalf("dispatch X-Session-ID = %q, want the canonical claude:strong-session", got)
	}
	if got := headers.Get("X-Claude-Code-Session-Id"); got != "strong-session" {
		t.Fatalf("dispatch X-Claude-Code-Session-Id = %q, want the original client value", got)
	}
}

func TestHomeDispatchCanonicalAliasesAreCallerScoped(t *testing.T) {
	manager, dispatcher := newDispatchSessionManager(t)

	dispatchOnce(t, manager, cliproxyexecutor.Options{
		Metadata:        map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "caller-a"},
		OriginalRequest: []byte(`{"prompt_cache_key":"shared-cache","conversation":{"id":"caller-a"}}`),
	})
	dispatchOnce(t, manager, cliproxyexecutor.Options{
		Metadata:        map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "caller-b"},
		OriginalRequest: []byte(`{"prompt_cache_key":"shared-cache","conversation":{"id":"caller-b"}}`),
	})
	dispatchOnce(t, manager, cliproxyexecutor.Options{
		Metadata:        map[string]any{cliproxyexecutor.CallerScopeMetadataKey: "caller-b"},
		OriginalRequest: []byte(`{"conversation":{"id":"caller-a"}}`),
	})

	session, _ := dispatcher.last()
	if session.ID != "conv:caller-a" {
		t.Fatalf("caller B inherited caller A canonical session: got %q, want conv:caller-a", session.ID)
	}
}

func TestHomeDispatchLeavesHeadersAloneWithoutASession(t *testing.T) {
	manager, dispatcher := newDispatchSessionManager(t)
	original := http.Header{"X-Session-Id": {"   "}}

	dispatchOnce(t, manager, cliproxyexecutor.Options{Headers: original})

	session, headers := dispatcher.last()
	if session.ID != "" {
		t.Fatalf("session.ID = %q, want empty", session.ID)
	}
	if got := headers.Get("X-Session-ID"); got != "   " {
		t.Fatalf("dispatch X-Session-ID = %q, want the untouched client value", got)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
