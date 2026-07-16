package auth

import (
	"context"
	"net/http"
	"testing"
)

func markResultTestAuth(t *testing.T, m *Manager, id string) *Auth {
	t.Helper()
	for _, a := range m.snapshotAuths() {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("auth %q not found", id)
	return nil
}

// A failure CAUSED by a client cancellation (context.Canceled → no upstream
// status) must not penalize the account: no Failed++, no cooldown, no suspend.
func TestMarkResult_SkipsCancelCausedFailure(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "cancel-noop", Provider: "claude", Status: StatusActive}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "claude", Model: "gpt-5.5",
		Success: false, Error: &Error{Message: "context canceled"}, // no HTTPStatus → status 0
	})

	got := markResultTestAuth(t, m, auth.ID)
	if got.Failed != 0 {
		t.Fatalf("Failed = %d, want 0 (a cancel-caused failure must not penalize the account)", got.Failed)
	}
	if st := got.ModelStates["gpt-5.5"]; st != nil && (!st.NextRetryAfter.IsZero() || st.Unavailable) {
		t.Fatalf("cancel-caused failure cooled the account: NextRetryAfter=%v Unavailable=%v", st.NextRetryAfter, st.Unavailable)
	}
}

// A REAL upstream failure (429) that carries a genuine status must STILL cool the
// account even under a coincident client cancel — it is a real signal about the
// credential, so the cancel guard must not swallow it.
func TestMarkResult_RecordsRealStatusFailureUnderCancel(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "real-429", Provider: "claude", Status: StatusActive}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "claude", Model: "gpt-5.5",
		Success: false, Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"},
	})

	got := markResultTestAuth(t, m, auth.ID)
	st := got.ModelStates["gpt-5.5"]
	if st == nil || st.NextRetryAfter.IsZero() {
		t.Fatalf("a real 429 under a coincident cancel must still cool the account, got ModelState=%+v", st)
	}
}
