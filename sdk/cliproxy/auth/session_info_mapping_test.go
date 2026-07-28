package auth

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// TestSessionInfoFromOptionsCopiesEveryIdentityField guards the identity-to-usage
// mapping. It is a plain field copy, so a dropped line would silently report an
// empty session on every usage record and request log instead of failing.
func TestSessionInfoFromOptionsCopiesEveryIdentityField(t *testing.T) {
	t.Parallel()

	const root = "019fa90b-31a4-7841-b252-0a2d5dafbbdc"
	const childThread = "019fa90b-3219-73d2-9aad-9de97611969e"
	const turnID = "019fa90b-3234-73d2-a061-0622e5f8c57c"
	opts := cliproxyexecutor.Options{
		Headers: http.Header{
			"Session-Id":               {root},
			"Thread-Id":                {childThread},
			"X-Codex-Parent-Thread-Id": {root},
			"X-Codex-Turn-Metadata": {`{"turn_id":"` + turnID +
				`","request_kind":"compact","thread_source":"subagent"}`},
			"User-Agent": {"codex_exec/0.145.0 (Mac OS 26.5.2; arm64)"},
		},
	}
	identity := cliproxysession.Extract(opts.Headers, nil, nil)

	got := sessionInfoFromOptions(opts)
	want := coreusage.SessionInfo{
		ID:             identity.ID,
		Source:         identity.Source,
		Confidence:     identity.Confidence,
		Scope:          identity.Scope,
		ClientType:     identity.ClientType,
		ThreadID:       identity.ThreadID,
		ParentThreadID: identity.ParentThreadID,
		RequestKind:    identity.RequestKind,
		ThreadSource:   identity.ThreadSource,
		TurnID:         identity.TurnID,
	}
	if got != want {
		t.Fatalf("sessionInfoFromOptions() = %#v, want %#v", got, want)
	}
	// Guard against the identity itself being empty, which would make the
	// comparison above pass for the wrong reason.
	if got.RequestKind != "compact" || got.ThreadSource != "subagent" || got.TurnID != turnID {
		t.Fatalf("observations not resolved: %#v", got)
	}
}

// TestSessionInfoFromHomeDispatchCopiesEveryField guards the same mapping on the
// Home path, where the session arrives already resolved from the dispatch reply.
func TestSessionInfoFromHomeDispatchCopiesEveryField(t *testing.T) {
	t.Parallel()

	session := home.DispatchSession{
		ID:             "codex:root",
		Source:         "client_native_header",
		Confidence:     "high",
		Scope:          "session",
		ClientType:     "codex",
		ThreadID:       "thread-child",
		ParentThreadID: "thread-root",
		RequestKind:    "title",
		ThreadSource:   "subagent",
		TurnID:         "turn-1",
	}

	got := sessionInfoFromHomeDispatch(session)
	want := coreusage.SessionInfo{
		ID:             session.ID,
		Source:         session.Source,
		Confidence:     session.Confidence,
		Scope:          session.Scope,
		ClientType:     session.ClientType,
		ThreadID:       session.ThreadID,
		ParentThreadID: session.ParentThreadID,
		RequestKind:    session.RequestKind,
		ThreadSource:   session.ThreadSource,
		TurnID:         session.TurnID,
	}
	if got != want {
		t.Fatalf("sessionInfoFromHomeDispatch() = %#v, want %#v", got, want)
	}
}
