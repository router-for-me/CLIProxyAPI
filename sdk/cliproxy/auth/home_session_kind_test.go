package auth

import (
	"context"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type sessionKindCaptureDispatcher struct {
	fixtureHomeDispatcher
	headers         http.Header
	sessionID       string
	parentSessionID string
}

func (d *sessionKindCaptureDispatcher) RPopAuthWithSessionHierarchy(_ context.Context, _ string, sessionID, parentSessionID string, headers http.Header, _ int, _ string, _ *int, _ []string, _ string) ([]byte, error) {
	d.headers = headers.Clone()
	d.sessionID = sessionID
	d.parentSessionID = parentSessionID
	return d.payload, nil
}

func TestHomeDispatchPreservesForkAndCompactionKind(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		metadata map[string]any
		want     string
	}{
		{name: "body fork", body: `{"thread_id":"child","metadata":{"forked_from_thread_id":"parent"}}`, want: "fork"},
		{name: "compaction", body: `{"session_id":"child","parent_session_id":"parent"}`, metadata: map[string]any{cliproxyexecutor.NodeKindMetadataKey: "compaction"}, want: "compaction"},
		{name: "ordinary child", body: `{"session_id":"child","parent_session_id":"parent"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dispatcher := &sessionKindCaptureDispatcher{fixtureHomeDispatcher: fixtureHomeDispatcher{payload: []byte(`{"auth":{"id":"kind-auth","provider":"kind-provider"}}`)}}
			manager := newHomeSelectionTestManager(t, dispatcher)
			manager.RegisterExecutor(schedulerTestExecutor{provider: "kind-provider"})
			headers := http.Header{"User-Agent": {"test-client"}}
			opts := cliproxyexecutor.Options{Headers: headers, OriginalRequest: []byte(tc.body), Metadata: tc.metadata}
			opts.EnsureMetadata()
			selection, errSelection := manager.pickHomeDispatchSelection(t.Context(), "model", opts)
			if errSelection != nil {
				t.Fatal(errSelection)
			}
			selection.End("test_complete")
			if dispatcher.sessionID == "" || dispatcher.parentSessionID == "" || dispatcher.sessionID == dispatcher.parentSessionID {
				t.Fatalf("lost session hierarchy: child=%q parent=%q", dispatcher.sessionID, dispatcher.parentSessionID)
			}
			if got := dispatcher.headers.Get("X-Node-Kind"); got != tc.want {
				t.Fatalf("node kind=%q, want %q", got, tc.want)
			}
			if got := headers.Get("X-Node-Kind"); got != "" {
				t.Fatalf("caller headers mutated: %q", got)
			}
		})
	}
}
