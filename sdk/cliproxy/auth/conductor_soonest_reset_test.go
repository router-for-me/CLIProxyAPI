package auth

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// soonestResetExecutor succeeds and reports the configured weekly reset for
// each auth as Codex response headers.
type soonestResetExecutor struct {
	mu      sync.Mutex
	resets  map[string]time.Duration
	pickedN []string
}

func (*soonestResetExecutor) Identifier() string { return "codex" }

func (e *soonestResetExecutor) Execute(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.pickedN = append(e.pickedN, auth.ID)
	after, ok := e.resets[auth.ID]
	e.mu.Unlock()
	if ok {
		internallogging.SetResponseHeaders(ctx, http.Header{
			"X-Codex-Primary-Used-Percent":        []string{"10"},
			"X-Codex-Primary-Window-Minutes":      []string{"10080"},
			"X-Codex-Primary-Reset-After-Seconds": []string{strconv.Itoa(int(after.Seconds()))},
		})
	}
	return cliproxyexecutor.Response{Payload: []byte("{}")}, nil
}

func (e *soonestResetExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (*soonestResetExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*soonestResetExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (*soonestResetExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *soonestResetExecutor) setReset(id string, after time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resets[id] = after
}

func (e *soonestResetExecutor) last() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.pickedN) == 0 {
		return ""
	}
	return e.pickedN[len(e.pickedN)-1]
}

func TestManagerSoonestResetRoutesByObservedResponseHeaders(t *testing.T) {
	executor := &soonestResetExecutor{resets: map[string]time.Duration{}}
	manager := NewManager(nil, &SoonestResetSelector{}, nil)
	manager.RegisterExecutor(executor)

	model := "gpt-soonest-reset-routing"
	ids := []string{"soonest-reset-a", "soonest-reset-b"}
	for _, id := range ids {
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: id, Provider: "codex", Status: StatusActive}); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", id, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}

	execute := func() string {
		t.Helper()
		ctx := internallogging.WithResponseHeadersHolder(context.Background())
		if _, errExecute := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute != nil {
			t.Fatalf("Execute() error = %v", errExecute)
		}
		return executor.last()
	}

	// Nothing observed yet: fill-first order. The response reports a reset for a in 2 days.
	executor.setReset(ids[0], 2*24*time.Hour)
	if got := execute(); got != ids[0] {
		t.Fatalf("first pick = %q, want %q", got, ids[0])
	}

	// b becomes known with a later reset (for example a snapshot restored after a restart).
	if _, errUpdate := manager.Update(context.Background(), &Auth{
		ID:       ids[1],
		Provider: "codex",
		Status:   StatusActive,
		Quota: QuotaState{
			ObservedAt: time.Now(),
			Signals: map[string]string{
				"X-Codex-Primary-Window-Minutes":      "10080",
				"X-Codex-Primary-Reset-After-Seconds": strconv.Itoa(int((5 * 24 * time.Hour).Seconds())),
			},
		},
	}); errUpdate != nil {
		t.Fatalf("Update(%s) error = %v", ids[1], errUpdate)
	}
	// a's observed reset (2 days) is sooner than b's (5 days), so a stays selected.
	// a's window then resets upstream and the response reports a fresh 7-day window.
	executor.setReset(ids[0], 7*24*time.Hour)
	if got := execute(); got != ids[0] {
		t.Fatalf("second pick = %q, want %q (observed 2d reset beats 5d)", got, ids[0])
	}
	// The new observation moves a behind b.
	if got := execute(); got != ids[1] {
		t.Fatalf("third pick = %q, want %q (observed 7d reset loses to 5d)", got, ids[1])
	}
}
