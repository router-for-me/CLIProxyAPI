package auth

import (
	"context"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

type toolSearchSupportExecutor struct {
	*replaceAwareExecutor
	supported bool

	mu   sync.Mutex
	seen []string
}

func (e *toolSearchSupportExecutor) SupportsNativeToolSearch(_ *Auth, upstreamModel string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seen = append(e.seen, upstreamModel)
	return e.supported
}

func registerToolSearchAuth(t *testing.T, m *Manager, id, provider string, disabled bool, models ...string) {
	t.Helper()
	auth := &Auth{ID: id, Provider: provider, Status: StatusActive, Disabled: disabled}
	if disabled {
		auth.Status = StatusDisabled
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register %s: %v", id, errRegister)
	}
	infos := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		infos = append(infos, &registry.ModelInfo{ID: model})
	}
	registry.GetGlobalRegistry().RegisterClient(id, provider, infos)
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
}

func TestSupportsNativeToolSearchForModel(t *testing.T) {
	yes := &toolSearchSupportExecutor{replaceAwareExecutor: &replaceAwareExecutor{id: "ts-yes"}, supported: true}
	no := &toolSearchSupportExecutor{replaceAwareExecutor: &replaceAwareExecutor{id: "ts-no"}}
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(yes)
	m.RegisterExecutor(no)
	m.RegisterExecutor(&replaceAwareExecutor{id: "ts-plain"})

	registerToolSearchAuth(t, m, "ts-auth-yes-1", "ts-yes", false, "ts-all-yes", "ts-mixed", "ts-prefixed", "ts-with-disabled")
	registerToolSearchAuth(t, m, "ts-auth-yes-2", "ts-yes", false, "ts-all-yes")
	registerToolSearchAuth(t, m, "ts-auth-no", "ts-no", false, "ts-mixed")
	registerToolSearchAuth(t, m, "ts-auth-plain", "ts-plain", false, "ts-plain-only")
	registerToolSearchAuth(t, m, "ts-auth-disabled", "ts-no", true, "ts-with-disabled")

	for _, tc := range []struct {
		model string
		want  bool
	}{
		{"ts-all-yes", true},
		{"ts-mixed", false},
		{"ts-plain-only", false},
		{"ts-no-credentials", false},
		{"team/ts-prefixed", true},
		{"ts-with-disabled", true},
	} {
		if got := m.SupportsNativeToolSearchForModel(tc.model); got != tc.want {
			t.Errorf("SupportsNativeToolSearchForModel(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}

	auth, _ := m.GetByID("ts-auth-yes-2")
	wantUpstream := m.ResolveExecutionModel(auth, "ts-all-yes")
	yes.mu.Lock()
	seen := append([]string(nil), yes.seen...)
	yes.mu.Unlock()
	found := false
	for _, model := range seen {
		found = found || model == wantUpstream
	}
	if !found {
		t.Fatalf("executor saw %v, want the resolved upstream model %q", seen, wantUpstream)
	}

	var absent *Manager
	if absent.SupportsNativeToolSearchForModel("ts-all-yes") {
		t.Fatal("nil manager reports native tool search")
	}
}
