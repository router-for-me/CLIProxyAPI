package claudemaster

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// A refusal caused by Anthropic's own answer is relayed, not a pool without accounts: it is counted
// apart, so the NoAccountAvailable alarm stays for requests no account could take.
func TestRelayedUpstreamErrorIsNotCountedAsNoAccount(t *testing.T) {
	selector := &backendSeriesSelector{authIDs: []string{"profile-a"}, provider: "claude", names: map[string]string{"profile-a": "alpha"}}
	t.Cleanup(selector.Stop)

	ctx := withBackendAttempt(context.Background())
	recordBackendAttempt(ctx, coreauth.Result{AuthID: "profile-a", Model: "claude-test-model", Error: &coreauth.Error{HTTPStatus: http.StatusNotFound}})
	relayed := selector.boundUnavailableErrorLocked(ctx, "profile-a", "claude-test-model")
	var r *backendRefusalError
	if !errors.As(relayed, &r) || !r.relayed {
		t.Fatalf("a refusal after an upstream answer must be marked relayed: %#v", relayed)
	}
	selector.noteSelection("session-1", "claude-test-model", nil, relayed, time.Millisecond)

	none := withBackendAttempt(context.Background())
	recordBackendAttempt(none, coreauth.Result{AuthID: "profile-a", Model: "claude-test-model", Error: &coreauth.Error{}})
	refused := selector.boundUnavailableErrorLocked(none, "profile-a", "claude-test-model")
	if errors.As(refused, &r) && r.relayed {
		t.Fatal("a failure with no upstream answer is not relayed")
	}
	selector.noteSelection("session-2", "claude-test-model", nil, refused, time.Millisecond)
	selector.noteSelection("session-3", "claude-test-model", nil, selector.quotaRefusalLocked("out"), time.Millisecond)

	stats := selector.statsLocked()
	if stats.relayed != 1 || stats.refusals != 2 {
		t.Fatalf("relayed %d refusals %d, want 1 and 2", stats.relayed, stats.refusals)
	}
}
