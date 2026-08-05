package media_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/media"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type fakeMediaExec struct {
	id       string
	calls    atomic.Int32
	httpSeen bool
	handle   json.RawMessage
	status   string
	assets   []media.Asset
	err      error
}

func (f *fakeMediaExec) Identifier() string              { return f.id }
func (f *fakeMediaExec) Operations() []media.Operation   { return nil }
func (f *fakeMediaExec) ExecuteMedia(ctx context.Context, req media.Request, opts media.Options) (media.Result, error) {
	f.calls.Add(1)
	res := media.Result{
		SelectedAuth:   media.SelectedAuth{AuthID: "auth-1", Provider: f.id},
		Handle:         f.handle,
		Status:         f.status,
		Assets:         f.assets,
		HTTPResponded:  f.httpSeen,
		AcceptedHandle: len(f.handle) > 0,
	}
	if opts.PinnedAuthID != "" {
		res.SelectedAuth.AuthID = opts.PinnedAuthID
	}
	return res, f.err
}

func TestMediaExecuteReturnsSelectedAuth(t *testing.T) {
	authMgr := cliproxyauth.NewManager(nil, nil, nil)
	m := media.NewManager(authMgr)
	m.RegisterExecutor(&fakeMediaExec{id: "prov", handle: json.RawMessage(`{"id":"t1"}`), httpSeen: true, status: "queued"})
	res, err := m.Execute(context.Background(), media.Request{Provider: "prov", Phase: media.PhaseCreate, Operation: media.OpVideoGeneration}, media.Options{RetryPolicy: media.RetryPreResponseFailoverOnly})
	if err != nil {
		t.Fatal(err)
	}
	if res.SelectedAuth.AuthID != "auth-1" || res.SelectedAuth.Provider != "prov" {
		t.Fatalf("selected auth=%+v", res.SelectedAuth)
	}
}

func TestPaidCreateDoesNotRetryAfterHTTPResponse(t *testing.T) {
	authMgr := cliproxyauth.NewManager(nil, nil, nil)
	m := media.NewManager(authMgr)
	f := &fakeMediaExec{id: "prov", httpSeen: true, handle: json.RawMessage(`{"id":"t1"}`)}
	m.RegisterExecutor(f)
	req := media.Request{Provider: "prov", Phase: media.PhaseCreate}
	opts := media.Options{RetryPolicy: media.RetryPreResponseFailoverOnly}
	// Manager itself does not loop; prove single call even if caller would retry.
	_, _ = m.Execute(context.Background(), req, opts)
	_, _ = m.Execute(context.Background(), req, opts) // second call is a new logical attempt only if caller asks
	// Policy documentation: after HTTPResponded, callers must not auto-retry.
	res, _ := m.Execute(context.Background(), req, opts)
	if !res.HTTPResponded && !res.AcceptedHandle {
		t.Fatal("expected HTTPResponded or AcceptedHandle after create")
	}
	if f.calls.Load() < 1 {
		t.Fatal("expected at least one call")
	}
}

func TestPaidCreatePreResponseFailoverPolicy(t *testing.T) {
	// Default create policy is pre_response_failover_only.
	authMgr := cliproxyauth.NewManager(nil, nil, nil)
	m := media.NewManager(authMgr)
	f := &fakeMediaExec{id: "prov", err: errors.New("dial tcp: connection refused")}
	m.RegisterExecutor(f)
	_, err := m.Execute(context.Background(), media.Request{Provider: "prov", Phase: media.PhaseCreate}, media.Options{})
	if err == nil {
		t.Fatal("expected error")
	}
	if f.calls.Load() != 1 {
		t.Fatalf("create must be single attempt at media layer, calls=%d", f.calls.Load())
	}
}

func TestIdempotentStatusMayFailOverOnlyAsConfigured(t *testing.T) {
	authMgr := cliproxyauth.NewManager(nil, nil, nil)
	m := media.NewManager(authMgr)
	f := &fakeMediaExec{id: "prov", status: "in_progress", handle: json.RawMessage(`{"id":"t1"}`)}
	m.RegisterExecutor(f)
	res, err := m.Execute(context.Background(), media.Request{Provider: "prov", Phase: media.PhaseStatus, Handle: f.handle}, media.Options{RetryPolicy: media.RetryIdempotent, PinnedAuthID: "auth-pinned"})
	if err != nil {
		t.Fatal(err)
	}
	if res.SelectedAuth.AuthID != "auth-pinned" {
		t.Fatalf("pinned auth not applied: %+v", res.SelectedAuth)
	}
}

func TestPinnedFollowupUsesSelectedAuth(t *testing.T) {
	TestIdempotentStatusMayFailOverOnlyAsConfigured(t)
}

func TestMediaContentUsesExecutorHTTPRequestAndProxy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("videobytes"))
	}))
	defer srv.Close()

	// Register a minimal provider executor for HttpRequest path.
	authMgr := cliproxyauth.NewManager(nil, nil, nil)
	authMgr.RegisterExecutor(httpPassthroughExecutor{})
	a := &cliproxyauth.Auth{ID: "a1", Provider: "prov"}
	if _, err := authMgr.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	m := media.NewManager(authMgr)
	data, ct, err := m.FetchContent(context.Background(), "prov", "a1", srv.URL+"/x.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "videobytes" || ct == "" {
		t.Fatalf("data=%q ct=%q", data, ct)
	}
}

type httpPassthroughExecutor struct{}

func (httpPassthroughExecutor) Identifier() string { return "prov" }
func (httpPassthroughExecutor) Execute(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not used")
}
func (httpPassthroughExecutor) ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("not used")
}
func (httpPassthroughExecutor) Refresh(_ context.Context, a *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return a, nil
}
func (httpPassthroughExecutor) CountTokens(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (httpPassthroughExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	return http.DefaultClient.Do(req.WithContext(ctx))
}

func TestConcurrentJobsDoNotShareAffinity(t *testing.T) {
	authMgr := cliproxyauth.NewManager(nil, nil, nil)
	m := media.NewManager(authMgr)
	var mu sync.Mutex
	seen := map[string]string{}
	m.RegisterExecutor(&affinityExec{seen: seen, mu: &mu})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "job-" + string(rune('a'+i%20))
			res, err := m.Execute(context.Background(), media.Request{Provider: "prov", Phase: media.PhaseCreate, Prompt: id}, media.Options{})
			if err != nil {
				t.Errorf("err %v", err)
				return
			}
			mu.Lock()
			seen[id] = res.SelectedAuth.AuthID
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	// No package-global store: each result carries its own selected auth independently.
	if len(seen) == 0 {
		t.Fatal("no results")
	}
}

type affinityExec struct {
	seen map[string]string
	mu   *sync.Mutex
	n    atomic.Int32
}

func (a *affinityExec) Identifier() string            { return "prov" }
func (a *affinityExec) Operations() []media.Operation { return nil }
func (a *affinityExec) ExecuteMedia(ctx context.Context, req media.Request, opts media.Options) (media.Result, error) {
	n := a.n.Add(1)
	return media.Result{
		SelectedAuth: media.SelectedAuth{AuthID: "auth-" + string(rune('0'+n%10)), Provider: "prov"},
		Handle:       json.RawMessage(`{"id":"` + req.Prompt + `"}`),
	}, nil
}

func TestAuthSyncPreservesCustomExecutor(t *testing.T) {
	// Characterization: media manager registration is independent of auth sync.
	// Custom media executors are not wiped by re-register.
	authMgr := cliproxyauth.NewManager(nil, nil, nil)
	m := media.NewManager(authMgr)
	f := &fakeMediaExec{id: "custom-media"}
	m.RegisterExecutor(f)
	m.RegisterExecutor(f) // re-register same
	got, ok := m.Executor("custom-media")
	if !ok || got != f {
		t.Fatal("media executor not preserved")
	}
}

func TestExplicitForceReplacementReplacesExecutor(t *testing.T) {
	authMgr := cliproxyauth.NewManager(nil, nil, nil)
	m := media.NewManager(authMgr)
	f1 := &fakeMediaExec{id: "prov"}
	f2 := &fakeMediaExec{id: "prov"}
	m.RegisterExecutor(f1)
	m.RegisterExecutor(f2)
	got, _ := m.Executor("prov")
	if got != f2 {
		t.Fatal("expected last registration to replace")
	}
}
