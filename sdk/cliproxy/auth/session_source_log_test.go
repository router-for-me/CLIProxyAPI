package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestSessionAffinityCacheMissLogsSource pins audit item P6 step 1: the
// cache-miss log must carry the extraction source (session id prefix such as
// "derived:" or "msg:") so an operator can quantify how much traffic lands on
// the message-hash fallback, the last-resort signal that can fuse distinct
// sessions onto one credential.
func TestSessionAffinityCacheMissLogsSource(t *testing.T) {
	previousLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	hook := logtest.NewLocal(log.StandardLogger())
	t.Cleanup(func() {
		hook.Reset()
		log.SetLevel(previousLevel)
	})

	ctx := context.Background()
	provider := "source-log-provider"
	model := "source-log-model"

	manager := NewManager(nil, nil, nil)
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	defer affinity.Stop()
	manager.SetSelector(affinity)
	manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
	auth := &Auth{ID: provider + "-a", Provider: provider, Status: StatusActive}
	if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
		t.Fatalf("Register(): %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.DerivedSessionIDMetadataKey: "source-log-session",
	}}
	picked, _, errPick := manager.pickNext(ctx, provider, model, opts, nil)
	if errPick != nil {
		t.Fatalf("pickNext(): %v", errPick)
	}
	if picked == nil || picked.ID != auth.ID {
		t.Fatalf("pickNext() auth = %#v, want %s", picked, auth.ID)
	}

	var sawSource bool
	for _, entry := range hook.AllEntries() {
		if entry.Level != log.DebugLevel {
			continue
		}
		if !strings.HasPrefix(entry.Message, "session-affinity: cache miss, new binding") {
			continue
		}
		if !strings.Contains(entry.Message, "source=derived") {
			t.Fatalf("cache-miss debug entry lacks source=derived: %q", entry.Message)
		}
		sawSource = true
	}
	if !sawSource {
		t.Fatal("no cache-miss debug entry captured")
	}
}
