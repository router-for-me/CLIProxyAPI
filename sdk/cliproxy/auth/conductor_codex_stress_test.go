package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type codexStressError struct {
	code       int
	message    string
	retryAfter *time.Duration
}

func (e *codexStressError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func (e *codexStressError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.code
}

func (e *codexStressError) RetryAfter() *time.Duration {
	if e == nil || e.retryAfter == nil {
		return nil
	}
	value := *e.retryAfter
	return &value
}

type codexStressBehavior struct {
	status     int
	retryAfter time.Duration
}

type codexHighChurnExecutor struct {
	delay    time.Duration
	behavior map[string]codexStressBehavior

	mu    sync.Mutex
	calls map[string]int
}

func (e *codexHighChurnExecutor) Identifier() string { return "codex" }

func (e *codexHighChurnExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.delay > 0 {
		time.Sleep(e.delay)
	}
	authID := ""
	if auth != nil {
		authID = auth.ID
	}

	e.mu.Lock()
	if e.calls == nil {
		e.calls = make(map[string]int)
	}
	e.calls[authID]++
	behavior := e.behavior[authID]
	e.mu.Unlock()

	if behavior.status != 0 {
		var retryAfter *time.Duration
		if behavior.retryAfter > 0 {
			value := behavior.retryAfter
			retryAfter = &value
		}
		return cliproxyexecutor.Response{}, &codexStressError{
			code:       behavior.status,
			message:    http.StatusText(behavior.status),
			retryAfter: retryAfter,
		}
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *codexHighChurnExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, fmt.Errorf("not implemented")
}

func (e *codexHighChurnExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *codexHighChurnExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, fmt.Errorf("not implemented")
}

func (e *codexHighChurnExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("not implemented")
}

func (e *codexHighChurnExecutor) snapshotCalls() map[string]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]int, len(e.calls))
	for authID, count := range e.calls {
		out[authID] = count
	}
	return out
}

type codexSelectedRecord struct {
	authID string
	ws     bool
}

func TestManagerExecute_CodexErrorStormConvergesToHealthyPool(t *testing.T) {
	const model = "gpt-5.4"

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 32)

	wsHealthy := []string{"codex-storm-ws-ok-0", "codex-storm-ws-ok-1", "codex-storm-ws-ok-2", "codex-storm-ws-ok-3"}
	wsBad502 := []string{"codex-storm-ws-502-0", "codex-storm-ws-502-1", "codex-storm-ws-502-2", "codex-storm-ws-502-3"}
	httpHealthy := []string{
		"codex-storm-http-ok-0", "codex-storm-http-ok-1", "codex-storm-http-ok-2", "codex-storm-http-ok-3",
		"codex-storm-http-ok-4", "codex-storm-http-ok-5", "codex-storm-http-ok-6", "codex-storm-http-ok-7",
	}
	httpBad401 := []string{
		"codex-storm-http-401-0", "codex-storm-http-401-1", "codex-storm-http-401-2",
		"codex-storm-http-401-3", "codex-storm-http-401-4", "codex-storm-http-401-5",
	}
	httpBad429 := []string{
		"codex-storm-http-429-0", "codex-storm-http-429-1", "codex-storm-http-429-2",
		"codex-storm-http-429-3", "codex-storm-http-429-4", "codex-storm-http-429-5",
	}

	allIDs := append(append(append(append([]string{}, wsHealthy...), wsBad502...), httpHealthy...), append(httpBad401, httpBad429...)...)
	behavior := make(map[string]codexStressBehavior, len(wsBad502)+len(httpBad401)+len(httpBad429))
	for _, authID := range wsBad502 {
		behavior[authID] = codexStressBehavior{status: http.StatusBadGateway}
	}
	for _, authID := range httpBad401 {
		behavior[authID] = codexStressBehavior{status: http.StatusUnauthorized}
	}
	for _, authID := range httpBad429 {
		behavior[authID] = codexStressBehavior{status: http.StatusTooManyRequests, retryAfter: 500 * time.Millisecond}
	}

	executor := &codexHighChurnExecutor{
		delay:    1 * time.Millisecond,
		behavior: behavior,
	}
	manager.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	for _, authID := range allIDs {
		auth := &Auth{
			ID:       authID,
			Provider: "codex",
			Status:   StatusActive,
			Attributes: map[string]string{
				"priority": "0",
			},
		}
		if containsString(wsHealthy, authID) || containsString(wsBad502, authID) {
			auth.Attributes["websockets"] = "true"
		}
		reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", authID, errRegister)
		}
		manager.RefreshSchedulerEntry(authID)
	}
	t.Cleanup(func() {
		for _, authID := range allIDs {
			reg.UnregisterClient(authID)
		}
	})

	req := cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"model":"gpt-5.4","input":"ping"}`),
	}

	var (
		recordMu sync.Mutex
		records  []codexSelectedRecord
		wg       sync.WaitGroup
		errCh    = make(chan error, 1)
	)

	const workers = 24
	const requestsPerWorker = 24
	for worker := 0; worker < workers; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			ws := worker%3 == 0
			for i := 0; i < requestsPerWorker; i++ {
				ctx := context.Background()
				if ws {
					ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)
				}

				selectedAuthID := ""
				_, errExec := manager.Execute(ctx, []string{"codex"}, req, cliproxyexecutor.Options{
					Metadata: map[string]any{
						cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(authID string) {
							selectedAuthID = authID
						},
					},
				})
				if errExec != nil {
					select {
					case errCh <- fmt.Errorf("worker %d request %d execute error: %w", worker, i, errExec):
					default:
					}
					return
				}
				if selectedAuthID == "" {
					select {
					case errCh <- fmt.Errorf("worker %d request %d selected auth is empty", worker, i):
					default:
					}
					return
				}

				recordMu.Lock()
				records = append(records, codexSelectedRecord{authID: selectedAuthID, ws: ws})
				recordMu.Unlock()
			}
		}()
	}
	wg.Wait()

	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}

	wsDistinct := distinctSelectedIDs(records, true)
	plainDistinct := distinctSelectedIDs(records, false)
	if len(wsDistinct) < 2 {
		t.Fatalf("expected websocket traffic to rotate across at least 2 auths, got %v", wsDistinct)
	}
	if len(plainDistinct) < 6 {
		t.Fatalf("expected plain traffic to rotate across at least 6 auths, got %v", plainDistinct)
	}

	unhealthyIDs := append(append([]string{}, wsBad502...), httpBad401...)
	unhealthyIDs = append(unhealthyIDs, httpBad429...)
	unhealthySet := make(map[string]struct{}, len(unhealthyIDs))
	for _, authID := range unhealthyIDs {
		unhealthySet[authID] = struct{}{}
		primeCtx := context.Background()
		if containsString(wsBad502, authID) {
			primeCtx = cliproxyexecutor.WithDownstreamWebsocket(primeCtx)
		}
		_, _ = manager.Execute(primeCtx, []string{"codex"}, req, cliproxyexecutor.Options{
			Metadata: map[string]any{
				cliproxyexecutor.PinnedAuthMetadataKey: authID,
			},
		})
	}

	callsBeforeSettle := executor.snapshotCalls()
	settleRecords := make([]codexSelectedRecord, 0, 180)
	for i := 0; i < 180; i++ {
		ws := i%3 == 0
		ctx := context.Background()
		if ws {
			ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)
		}

		selectedAuthID := ""
		_, errExec := manager.Execute(ctx, []string{"codex"}, req, cliproxyexecutor.Options{
			Metadata: map[string]any{
				cliproxyexecutor.SelectedAuthCallbackMetadataKey: func(authID string) {
					selectedAuthID = authID
				},
			},
		})
		if errExec != nil {
			t.Fatalf("settle request %d error = %v", i, errExec)
		}
		if selectedAuthID == "" {
			t.Fatalf("settle request %d selected auth is empty", i)
		}
		if _, bad := unhealthySet[selectedAuthID]; bad {
			t.Fatalf("settle request %d selected unhealthy auth %q", i, selectedAuthID)
		}
		if ws && !containsString(wsHealthy, selectedAuthID) {
			t.Fatalf("settle websocket request %d selected non-websocket-healthy auth %q", i, selectedAuthID)
		}
		settleRecords = append(settleRecords, codexSelectedRecord{authID: selectedAuthID, ws: ws})
	}

	callsAfterSettle := executor.snapshotCalls()
	for _, authID := range unhealthyIDs {
		if callsAfterSettle[authID] != callsBeforeSettle[authID] {
			t.Fatalf("expected unhealthy auth %q to stop receiving traffic after settle: before=%d after=%d", authID, callsBeforeSettle[authID], callsAfterSettle[authID])
		}
	}

	if got := distinctSelectedIDs(settleRecords, true); len(got) < 2 {
		t.Fatalf("expected settled websocket traffic to still rotate, got %v", got)
	}
	if got := distinctSelectedIDs(settleRecords, false); len(got) < 4 {
		t.Fatalf("expected settled plain traffic to still rotate, got %v", got)
	}
}

func distinctSelectedIDs(records []codexSelectedRecord, ws bool) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, record := range records {
		if record.ws != ws {
			continue
		}
		if _, ok := seen[record.authID]; ok {
			continue
		}
		seen[record.authID] = struct{}{}
		out = append(out, record.authID)
	}
	return out
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
