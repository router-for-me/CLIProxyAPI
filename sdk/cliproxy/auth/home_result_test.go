package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type homeResultCapture func(coreusage.Record)

func (capture homeResultCapture) HandleUsage(_ context.Context, record coreusage.Record) {
	capture(record)
}

func TestHomeAuthPreparationReportsCoalescedFailureOnce(t *testing.T) {
	ctx := t.Context()
	manager := NewManager(nil, nil, nil)
	snapshot := &Auth{ID: t.Name(), Index: "home-credential", Provider: "antigravity", Metadata: map[string]any{"access_token": "test-token"}}
	const model = "gemini-2.5-pro"
	const body = `{"error":{"message":"rate limited"}}`
	prepareErr := errors.New("project discovery failed")
	started := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	executor := &issue7727Executor{
		provider: snapshot.Provider,
		prepareFn: func(ctx context.Context, auth *Auth) (*Auth, error) {
			close(started)
			<-release
			ReportHomeAuthPreparationFailure(ctx, auth, http.StatusTooManyRequests, []byte(body))
			return nil, prepareErr
		},
	}
	selection := &HomeDispatchSelection{Auth: snapshot, Executor: executor, Provider: snapshot.Provider}
	barrier := t.Name()
	var records []coreusage.Record
	arrived := make(chan struct{})
	coreusage.RegisterNamedPlugin(t.Name(), homeResultCapture(func(record coreusage.Record) {
		if record.Provider == barrier {
			close(arrived)
		} else if record.AuthID == snapshot.ID {
			records = append(records, record)
		}
	}))
	t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), homeResultCapture(func(coreusage.Record) {})) })

	errs := make(chan error, 2)
	go func() {
		_, errPrepare := manager.prepareHomeRequestAuth(ctx, selection, model+"(high)")
		errs <- errPrepare
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("preparation did not start")
	}
	go func() {
		_, errPrepare := manager.prepareHomeRequestAuth(ctx, selection, model+"(high)")
		errs <- errPrepare
	}()
	waitForHomePrepareFlightWaiters(t, manager, homeAuthPrepareKey(ctx, snapshot), 2)
	unblock()
	for range 2 {
		if errPrepare := <-errs; !errors.Is(errPrepare, prepareErr) {
			t.Fatalf("preparation error = %v, want %v", errPrepare, prepareErr)
		}
	}
	if calls := executor.prepareCalls.Load(); calls != 1 {
		t.Fatalf("preparation calls = %d, want 1", calls)
	}

	ReportHomeAuthPreparationFailure(ctx, snapshot, http.StatusTooManyRequests, []byte(body))
	coreusage.PublishRecord(ctx, coreusage.Record{Provider: barrier})
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("usage queue did not reach the barrier")
	}
	if len(records) != 1 {
		t.Fatalf("usage records = %d, want 1 from the shared Home preparation and none outside it", len(records))
	}
	if record := records[0]; !record.Failed || record.Model != model || record.Fail.StatusCode != http.StatusTooManyRequests || record.Fail.Body != body {
		t.Errorf("incorrect Home preparation failure record: %+v", record)
	}
}
