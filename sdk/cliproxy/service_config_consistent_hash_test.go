package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestRoutingStateConsistentHashRequiresSessionAffinity(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		routing  internalconfig.RoutingConfig
		wantHash bool
	}{
		{name: "default", routing: internalconfig.RoutingConfig{}, wantHash: false},
		{name: "hash without affinity", routing: internalconfig.RoutingConfig{SessionAffinityConsistentHash: true}, wantHash: false},
		{name: "affinity without hash", routing: internalconfig.RoutingConfig{SessionAffinity: true}, wantHash: false},
		{name: "affinity with hash", routing: internalconfig.RoutingConfig{SessionAffinity: true, SessionAffinityConsistentHash: true}, wantHash: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			state := normalizedRoutingRuntimeState(&internalconfig.Config{Routing: testCase.routing})
			if state.sessionAffinityHash != testCase.wantHash {
				t.Fatalf("sessionAffinityHash = %v, want %v", state.sessionAffinityHash, testCase.wantHash)
			}
		})
	}
}

func TestRoutingSelectorConsistentHashAgreesAcrossInstances(t *testing.T) {
	state := normalizedRoutingRuntimeState(&internalconfig.Config{Routing: internalconfig.RoutingConfig{
		SessionAffinity:               true,
		SessionAffinityConsistentHash: true,
	}})
	first, okFirst := newRoutingSelector(state).(*coreauth.SessionAffinitySelector)
	second, okSecond := newRoutingSelector(state).(*coreauth.SessionAffinitySelector)
	if !okFirst || !okSecond {
		t.Fatalf("selector types = %T, %T; want *auth.SessionAffinitySelector", first, second)
	}
	defer first.Stop()
	defer second.Stop()

	auths := []*coreauth.Auth{
		{ID: "claude-a", Provider: "claude", Status: coreauth.StatusActive},
		{ID: "claude-b", Provider: "claude", Status: coreauth.StatusActive},
		{ID: "claude-c", Provider: "claude", Status: coreauth.StatusActive},
	}
	opts := func(session string) cliproxyexecutor.Options {
		return cliproxyexecutor.Options{
			Headers:         http.Header{"X-Session-Affinity": []string{session}},
			OriginalRequest: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
			Metadata:        map[string]any{},
		}
	}
	pick := func(selector *coreauth.SessionAffinitySelector, session string) string {
		t.Helper()
		auth, err := selector.Pick(context.Background(), "claude", "claude-haiku-5-5", opts(session), auths)
		if err != nil || auth == nil {
			t.Fatalf("Pick(%s) = %v, %v", session, auth, err)
		}
		return auth.ID
	}
	pick(second, "warm-up")
	for i := 0; i < 30; i++ {
		session := fmt.Sprintf("config-session-%d", i)
		if a, b := pick(first, session), pick(second, session); a != b {
			t.Fatalf("session %s: instances picked %s and %s", session, a, b)
		}
	}
}
