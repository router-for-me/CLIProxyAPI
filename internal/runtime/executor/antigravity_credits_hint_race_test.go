package executor

import (
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// A refresh that publishes while clear sits between its stale-hint read and its reset must win.
func TestAntigravityCreditsClearDoesNotOverwriteConcurrentPublish(t *testing.T) {
	const authID = "ag-hint-race"
	cliproxyauth.SetAntigravityCreditsHint(authID, cliproxyauth.AntigravityCreditsHint{Known: true, Available: false, UpdatedAt: time.Now().Add(-time.Hour)})
	fresh := cliproxyauth.AntigravityCreditsHint{Known: true, Available: true, CreditAmount: 2516}
	published := make(chan struct{})
	antigravityCreditsBeforeHintReset = func(id string) {
		if id != authID {
			return
		}
		go func() {
			publishAntigravityCreditsHint(authID, fresh)
			close(published)
		}()
		// Without the lock the publish lands here, before the reset; with it the publish waits for clear.
		select {
		case <-published:
		case <-time.After(500 * time.Millisecond):
		}
	}
	t.Cleanup(func() {
		antigravityCreditsBeforeHintReset = nil
		cliproxyauth.SetAntigravityCreditsHint(authID, cliproxyauth.AntigravityCreditsHint{})
	})

	clearAntigravityCreditsFailureState(&cliproxyauth.Auth{ID: authID})
	select {
	case <-published:
	case <-time.After(5 * time.Second):
		t.Fatal("publish never completed")
	}
	hint, ok := cliproxyauth.GetAntigravityCreditsHint(authID)
	if !ok || !hint.Known || !hint.Available || hint.CreditAmount != 2516 {
		t.Fatalf("hint = %+v (ok=%v), want the published Known=true Available=true CreditAmount=2516", hint, ok)
	}
}
