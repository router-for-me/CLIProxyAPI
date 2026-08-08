package auth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// This file pins the hazards found while auditing the single-attempt guarantee of
// ExecuteWithAuthOnce and DoHTTPOnce. Each test states the contract that closed
// one of them, so a later change that re-opens the hazard fails here.
//
// The through line is a boundary: a provider executor constructs and owns its own
// http.Client, so the manager can observe that path but cannot police it. Every
// guarantee that needs control of the client - redirect policy, credential
// stripping, a hard cap on upstream sends - therefore lives in DoHTTPOnce, and on
// the executor path the manager reports facts instead of pretending to enforce.

// hazardExecutorClient mirrors helps.NewProxyAwareHTTPClient: a plain http.Client
// whose Transport is the context RoundTripper and which sets no CheckRedirect.
// internal/runtime/executor/helps/proxy_helpers.go:28-62 builds exactly this.
func hazardExecutorClient(ctx context.Context) *http.Client {
	rt, _ := ctx.Value("cliproxy.roundtripper").(http.RoundTripper)
	return &http.Client{Transport: rt}
}

// An executor-owned client follows redirects and net/http replays the body on a
// 307, so one ExecuteWithAuthOnce can produce two upstream creates. The manager
// cannot prevent that - it does not own the client - so it must not hide it:
// RequestCount reports every send, which is the fact a caller of a paid create
// reconciles against.
func TestExecuteWithAuthOnce_ExecutorRedirectReplayIsCounted(t *testing.T) {
	var upstreamCreates atomic.Int32
	var redirects atomic.Int32
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/create", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if len(body) > 0 {
			upstreamCreates.Add(1)
		}
		if redirects.Add(1) == 1 {
			http.Redirect(w, r, server.URL+"/create", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	executor := &onceTestExecutor{provider: "codex"}
	executor.executeFunc = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/create", bytes.NewReader([]byte(`{"paid":true}`)))
		resp, errDo := hazardExecutorClient(ctx).Do(httpReq)
		if errDo != nil {
			return cliproxyexecutor.Response{}, errDo
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
	}
	manager, _ := newOnceTestManager(t, executor, newOnceTestAuth("replay-auth"))

	_, facts, err := manager.ExecuteWithAuthOnce(context.Background(), onceRequest("replay-auth"))
	if err != nil {
		t.Fatalf("ExecuteWithAuthOnce() error = %v", err)
	}
	if got := upstreamCreates.Load(); got != 2 {
		t.Fatalf("upstream paid creates = %d, want 2 for an executor that follows a 307", got)
	}
	if int(facts.RequestCount) != int(upstreamCreates.Load()) {
		t.Fatalf("facts.RequestCount = %d, want %d: attempt facts must not under-report upstream sends", facts.RequestCount, upstreamCreates.Load())
	}
	if got := executor.executeCalls.Load(); got != 1 {
		t.Fatalf("executor invocations = %d, want 1", got)
	}
}

// The manager transport observes; it never mutates. An earlier design deleted the
// Location header off a 3xx to keep a shared client from following it, which
// corrupted an upstream response the caller may legitimately need to read.
func TestExecuteWithAuthOnce_ObservingTransportPreservesUpstreamResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "https://elsewhere.example.com/next")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	var seenLocation atomic.Value
	seenLocation.Store("")
	executor := &onceTestExecutor{provider: "codex"}
	executor.executeFunc = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		httpReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
		client := hazardExecutorClient(ctx)
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, errDo := client.Do(httpReq)
		if errDo != nil {
			return cliproxyexecutor.Response{}, errDo
		}
		defer func() { _ = resp.Body.Close() }()
		seenLocation.Store(resp.Header.Get("Location"))
		return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
	}
	manager, _ := newOnceTestManager(t, executor, newOnceTestAuth("location-auth"))

	if _, _, err := manager.ExecuteWithAuthOnce(context.Background(), onceRequest("location-auth")); err != nil {
		t.Fatalf("ExecuteWithAuthOnce() error = %v", err)
	}
	if got, _ := seenLocation.Load().(string); got != "https://elsewhere.example.com/next" {
		t.Fatalf("Location header seen by the executor = %q, want it untouched", got)
	}
}

// A transport cannot tell a followed redirect from an executor's own second
// request - antigravity's base-URL fallback, a pre-flight, a multipart upload -
// so it must never strip credentials. An earlier design inferred "redirect" from
// a URL differing from the first one observed and sent real requests unauthorized.
func TestExecuteWithAuthOnce_ObservingTransportKeepsCredentialsOnEveryRequest(t *testing.T) {
	var authorized [2]atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/first":
			authorized[0].Store(r.Header.Get("Authorization") != "")
		case "/second":
			authorized[1].Store(r.Header.Get("Authorization") != "")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	executor := &onceTestExecutor{provider: "codex"}
	executor.executeFunc = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		client := hazardExecutorClient(ctx)
		for _, path := range []string{"/first", "/second"} {
			httpReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
			httpReq.Header.Set("Authorization", "Bearer secret-token")
			resp, errDo := client.Do(httpReq)
			if errDo != nil {
				return cliproxyexecutor.Response{}, errDo
			}
			_ = resp.Body.Close()
		}
		return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
	}
	manager, _ := newOnceTestManager(t, executor, newOnceTestAuth("strip-auth"))

	if _, _, err := manager.ExecuteWithAuthOnce(context.Background(), onceRequest("strip-auth")); err != nil {
		t.Fatalf("ExecuteWithAuthOnce() error = %v", err)
	}
	if !authorized[0].Load() || !authorized[1].Load() {
		t.Fatalf("authorized requests = %v/%v, want both; the observing transport must not strip credentials", authorized[0].Load(), authorized[1].Load())
	}
}

// A negative claim needs positive proof. httptrace firing at all is not proof
// that the recorder saw the whole attempt: GetConn and ConnectStart fire for any
// traced request, including a pre-flight that never wrote a byte, while the paid
// request may have gone out on a client the manager cannot observe.
func TestOnceAttemptRecorder_ExecutorPathNeverClaimsNotWritten(t *testing.T) {
	recorder := newOnceAttemptRecorder(false)
	trace := recorder.clientTrace()

	// A pre-flight or token call that never got past connection setup.
	trace.GetConn("api.example.com:443")
	trace.ConnectStart("tcp", "203.0.113.10:443")

	// The paid request then went out on a client the manager cannot observe.
	recorder.markDispatched()

	facts := recorder.facts()
	if !facts.RequestWritten {
		t.Fatalf("facts.RequestWritten = false, want true: an executor-owned attempt must fail safe toward \"may have been sent\"")
	}
	if facts.RequestWrittenObserved {
		t.Fatalf("facts.RequestWrittenObserved = true, want false: nothing was actually observed")
	}
}

// A manager-owned attempt may state the negative, because the manager saw the
// whole attempt through a transport and a trace it installed itself.
func TestOnceAttemptRecorder_OwnedClientReportsNotWritten(t *testing.T) {
	recorder := newOnceAttemptRecorder(true)
	trace := recorder.clientTrace()
	recorder.markDispatched()
	trace.GetConn("api.example.com:443")
	trace.ConnectStart("tcp", "203.0.113.10:443")

	if facts := recorder.facts(); facts.RequestWritten {
		t.Fatalf("facts.RequestWritten = true, want false for a manager-owned attempt that never wrote")
	}
}

// hazardRetryingTransport models what http.Transport and the bundled http2
// Transport do below any wrapping RoundTripper: they resend a request
// internally. See net/http/transport.go shouldRetryRequest and
// h2_bundle.go http2shouldRetryRequest / http2canRetryError (a peer PROTOCOL_ERROR
// is retried whenever GetBody is set, which http.NewRequest always sets for a
// bytes body).
type hazardRetryingTransport struct {
	base http.RoundTripper
}

func (t *hazardRetryingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// First send is written and lost; the transport silently replays it.
	if req.GetBody != nil {
		body, _ := req.GetBody()
		retry := req.Clone(req.Context())
		retry.Body = body
		if resp, err := t.base.RoundTrip(retry); err == nil {
			_ = resp.Body.Close()
		}
	}
	return t.base.RoundTrip(req)
}

// A retry performed inside the base transport is invisible to the wrapping
// RoundTripper, so RequestCount must also read httptrace, which fires per wire
// attempt from inside the transport. DoHTTPOnce clears GetBody to stop the replay
// outright; ExecuteWithAuthOnce cannot, because the executor builds the request.
func TestExecuteWithAuthOnce_TransportInternalRetryIsCounted(t *testing.T) {
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		sends.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	executor := &onceTestExecutor{provider: "codex"}
	executor.executeFunc = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		// A provider executor builds its own request; GetBody is set for it.
		httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/create", bytes.NewReader([]byte(`{"paid":true}`)))
		resp, errDo := hazardExecutorClient(ctx).Do(httpReq)
		if errDo != nil {
			return cliproxyexecutor.Response{}, errDo
		}
		defer func() { _ = resp.Body.Close() }()
		return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
	}
	manager, _ := newOnceTestManager(t, executor, newOnceTestAuth("count-auth"))

	base := &hazardRetryingTransport{base: http.DefaultTransport}
	ctx := context.WithValue(context.Background(), roundTripperContextKey{}, http.RoundTripper(base))
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", http.RoundTripper(base))

	_, facts, err := manager.ExecuteWithAuthOnce(ctx, onceRequest("count-auth"))
	if err != nil {
		t.Fatalf("ExecuteWithAuthOnce() error = %v", err)
	}
	if int(facts.RequestCount) != int(sends.Load()) {
		t.Fatalf("facts.RequestCount = %d but upstream saw %d requests; attempt facts must not under-report", facts.RequestCount, sends.Load())
	}
}

// A denied redirect must not read as a success to a caller that branches on the
// error alone: it is returned as the unfollowed 3xx plus an explicit error.
func TestHTTPOnce_DeniedRedirectIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.example.com/create", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	executor := &onceTestExecutor{provider: "codex"}
	manager, _ := newOnceTestManager(t, executor, newOnceTestAuth("deny-auth"))
	auth := onceAuthByID(t, manager, "deny-auth")

	ctx := context.Background()
	req := onceHTTPRequest(t, ctx, http.MethodPost, server.URL+"/create", bytes.NewReader([]byte(`{"paid":true}`)))
	resp, _, err := httpOnceWithAuth(manager, ctx, auth, req, HTTPRedirectDeny)
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err == nil {
		t.Fatalf("httpOnce() error = nil for a policy-denied redirect; a refused attempt must not look like a success")
	}
	var authErr *Error
	if !errors.As(err, &authErr) || authErr.Code != "redirect_denied" {
		t.Fatalf("httpOnce() error = %v, want *Error{Code: \"redirect_denied\"}", err)
	}
	if resp == nil || resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("httpOnce() response = %v, want the unfollowed 3xx", resp)
	}
}

// "The executor is invoked exactly once" is not "one upstream request": registered
// executors run their own attempt loops (antigravity loops attempts x base URLs
// around httpClient.Do). The manager cannot cap that, so RequestCount must expose
// it and the doc comment must scope the guarantee to the executor invocation.
func TestExecuteWithAuthOnce_ExecutorInternalRetryIsCounted(t *testing.T) {
	var upstreamCreates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		upstreamCreates.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	executor := &onceTestExecutor{provider: "codex"}
	executor.executeFunc = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		client := hazardExecutorClient(ctx)
		// The antigravity executor's own attempt loop, reproduced faithfully.
		for attempt := 0; attempt < 3; attempt++ {
			httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/create", bytes.NewReader([]byte(`{"paid":true}`)))
			resp, errDo := client.Do(httpReq)
			if errDo != nil {
				return cliproxyexecutor.Response{}, errDo
			}
			_ = resp.Body.Close()
		}
		return cliproxyexecutor.Response{}, &onceStatusError{status: http.StatusTooManyRequests, message: "rate limited"}
	}
	manager, _ := newOnceTestManager(t, executor, newOnceTestAuth("inner-retry-auth"))

	_, facts, _ := manager.ExecuteWithAuthOnce(context.Background(), onceRequest("inner-retry-auth"))
	if got := upstreamCreates.Load(); got != 3 {
		t.Fatalf("upstream paid creates = %d, want the executor's own 3", got)
	}
	if int(facts.RequestCount) != int(upstreamCreates.Load()) {
		t.Fatalf("facts.RequestCount = %d, want %d; a caller of a paid create must be able to see the extra sends", facts.RequestCount, upstreamCreates.Load())
	}
	if got := executor.executeCalls.Load(); got != 1 {
		t.Fatalf("executor invocations = %d, want 1", got)
	}
}

// A raw one-shot HTTP call addresses arbitrary endpoints, so a request-shaped
// status says nothing about the credential. A polled job URL that 404s must not
// suspend the model for twelve hours.
func TestDoHTTPOnce_RequestShapedStatusDoesNotCoolCredential(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		wantCool bool
	}{
		{name: "not found", status: http.StatusNotFound},
		{name: "bad request", status: http.StatusBadRequest},
		{name: "conflict", status: http.StatusConflict},
		{name: "unprocessable", status: http.StatusUnprocessableEntity},
		{name: "unauthorized", status: http.StatusUnauthorized, wantCool: true},
		{name: "forbidden", status: http.StatusForbidden, wantCool: true},
		{name: "rate limited", status: http.StatusTooManyRequests, wantCool: true},
		{name: "server error", status: http.StatusInternalServerError, wantCool: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer server.Close()

			manager, hook := newOnceTestManager(t, &onceTestExecutor{provider: "codex"}, newOnceTestAuth("cool-auth"))
			resp, _, err := manager.DoHTTPOnce(context.Background(), HTTPOnceRequest{
				AuthID: "cool-auth",
				Model:  "test-model",
				Method: http.MethodGet,
				URL:    server.URL,
			})
			if err != nil {
				t.Fatalf("DoHTTPOnce() error = %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if got := len(hook.snapshot()); got != 1 {
				t.Fatalf("recorded results = %d, want 1", got)
			}
			stored := onceAuthByID(t, manager, "cool-auth")
			state := stored.ModelStates["test-model"]
			cooled := state != nil && state.Unavailable
			if cooled != tt.wantCool {
				t.Fatalf("model cooled = %v, want %v for status %d", cooled, tt.wantCool, tt.status)
			}
		})
	}
}

// Credential health is model scoped. A raw call that names no model must not
// rewrite it: a successful status poll once cleared the whole credential's quota
// state and resurrected a rate-limited credential into the serving pool.
func TestDoHTTPOnce_UnmodeledCallNeverRewritesCredentialHealth(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{name: "success", status: http.StatusOK},
		{name: "unauthorized", status: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer server.Close()

			auth := newOnceTestAuth("unmodeled-auth")
			auth.Unavailable = true
			auth.Quota = QuotaState{Exceeded: true, Reason: "quota"}
			manager, hook := newOnceTestManager(t, &onceTestExecutor{provider: "codex"}, auth)

			resp, _, err := manager.DoHTTPOnce(context.Background(), HTTPOnceRequest{
				AuthID: "unmodeled-auth",
				Method: http.MethodGet,
				URL:    server.URL,
			})
			if err != nil {
				t.Fatalf("DoHTTPOnce() error = %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			if got := len(hook.snapshot()); got != 1 {
				t.Fatalf("recorded results = %d, want 1: the call must stay observable", got)
			}
			stored := onceAuthByID(t, manager, "unmodeled-auth")
			if !stored.Quota.Exceeded || !stored.Unavailable {
				t.Fatalf("credential health = {unavailable:%v quota:%+v}, want it untouched", stored.Unavailable, stored.Quota)
			}
		})
	}
}
