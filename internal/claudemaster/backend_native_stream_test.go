package claudemaster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type backendNativeGatedBody struct {
	ready, release chan struct{}
	once           sync.Once
	payload        []byte
	err            error
	started        bool
}

func (b *backendNativeGatedBody) Read(buffer []byte) (int, error) {
	if !b.started {
		b.started = true
		close(b.ready)
		<-b.release
	}
	if len(b.payload) > 0 {
		count := copy(buffer, b.payload)
		b.payload = b.payload[count:]
		return count, nil
	}
	if b.err != nil {
		return 0, b.err
	}
	return 0, io.EOF
}

func (b *backendNativeGatedBody) Close() error {
	b.once.Do(func() { close(b.release) })
	return nil
}

type backendNativeHeaderWriter struct {
	recorder  *httptest.ResponseRecorder
	committed chan struct{}
	once      sync.Once
}

func (w *backendNativeHeaderWriter) Header() http.Header { return w.recorder.Header() }
func (w *backendNativeHeaderWriter) WriteHeader(status int) {
	w.recorder.WriteHeader(status)
	w.once.Do(func() { close(w.committed) })
}
func (w *backendNativeHeaderWriter) Write(payload []byte) (int, error) {
	count, errWrite := w.recorder.Write(payload)
	w.once.Do(func() { close(w.committed) })
	return count, errWrite
}
func (w *backendNativeHeaderWriter) Flush() {
	w.recorder.Flush()
	w.once.Do(func() { close(w.committed) })
}

type backendNativeStreamTransport struct {
	body   io.ReadCloser
	status int
	calls  atomic.Int32
}

func (rt *backendNativeStreamTransport) RoundTripperFor(*coreauth.Auth) http.RoundTripper {
	return rt
}

func (rt *backendNativeStreamTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Host != masterAPIHost {
		return nil, errors.New("synthetic native transport refused an unexpected host")
	}
	rt.calls.Add(1)
	status := rt.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"text/event-stream"}, "Request-Id": {"native-headers-first"}, "X-Future-Native-Header": {"preserved"}},
		Body:       rt.body,
		Request:    request,
	}, nil
}

func newBackendNativeResponseFixture(t *testing.T) *Backend {
	t.Helper()
	opts := writeSyntheticBackendCredential(t, canonicalTestTempDir(t), "native-response.json", map[string]any{
		"type": "claude", "access_token": integrationClaudeToken,
		"account_uuid": integrationClaudeAccount, "claude_device_ids": []string{integrationClaudeDevice},
	})
	opts.Provider, opts.Model, opts.UseRequestModel = "claude", "", true
	store, auth, errLoad := loadBackendCredential(t.Context(), opts)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	selector := backendPersistedSelector(t, []BackendCredential{{AuthDir: opts.AuthDir, Provider: "claude", AuthID: opts.AuthID}}, []*coreauth.Auth{auth}, "")
	cfg := backendConfig(opts.AuthDir)
	// A configured transport route intentionally wins over injected transports.
	// Clear the production direct route before creating this isolated fixture.
	cfg.ProxyURL = ""
	manager := coreauth.NewManager(store, selector, nil)
	manager.SetConfig(cfg)
	// Even an accidentally unconfigured fixture must never reach the network.
	manager.SetRoundTripperProvider(&backendNativeStreamTransport{status: http.StatusServiceUnavailable, body: io.NopCloser(strings.NewReader("synthetic transport was not configured"))})
	manager.SetRetryConfig(0, 0, 1)
	setBackendResultPolicy(manager, selector)
	manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(cfg))
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: backendAuthSelectionModel}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(coreauth.WithSkipPersist(t.Context()), auth); errRegister != nil {
		t.Fatal(errRegister)
	}
	lifetime, cancel := context.WithCancel(t.Context())
	backend := &Backend{manager: manager, store: store, authIDs: []string{auth.ID}, seriesSelector: selector, cancel: cancel}
	backend.handler = newBackendHandler(lifetime, opts, handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	t.Cleanup(func() { _ = backend.Close() })
	return backend
}

func TestBackendNativeSuccessfulStatusPreservesGenerationCountAndStream(t *testing.T) {
	for _, operation := range []string{"execute", "stream", "count"} {
		for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent} {
			t.Run(fmt.Sprintf("%s/%d", operation, status), func(t *testing.T) {
				backend := newBackendNativeResponseFixture(t)
				responseBody := "raw native response"
				if status == http.StatusNoContent {
					responseBody = ""
				}
				transport := &backendNativeStreamTransport{status: status, body: io.NopCloser(strings.NewReader(responseBody))}
				backend.manager.SetRoundTripperProvider(transport)
				path := "/v1/messages"
				if operation == "count" {
					path += "/count_tokens"
				}
				request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(fmt.Sprintf(`{"model":"claude-opus-5","stream":%t,"messages":[{"role":"user","content":"hello"}]}`, operation == "stream")))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("X-Claude-Code-Session-Id", integrationClaudeSession)
				request = request.WithContext(coreexecutor.WithNativeClaudeProtocolHeaders(request.Context(), request.Header))
				writer := httptest.NewRecorder()
				backend.Handler().ServeHTTP(writer, request)
				if writer.Code != status || writer.Body.String() != responseBody || writer.Header().Get("X-Future-Native-Header") != "preserved" || transport.calls.Load() != 1 {
					t.Fatalf("native success changed status/headers/body or replayed: status=%d headers=%v body=%q attempts=%d", writer.Code, writer.Header(), writer.Body.String(), transport.calls.Load())
				}
			})
		}
	}
}

func TestBackendNativeStreamJournalCommitFailurePreservesResponseAndReleasesGeneration(t *testing.T) {
	const rawStream = "data: native opaque stream\r\n\r\n"
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprintf("commit_failure_%t", failure), func(t *testing.T) {
			backend := newBackendNativeResponseFixture(t)
			selector := backend.seriesSelector
			var commits atomic.Int32
			selector.routes.writeRoute = func(dir, name string, raw []byte) error {
				var route backendSessionRoute
				if err := json.Unmarshal(raw, &route); err != nil {
					return err
				}
				if route.Committed {
					commits.Add(1)
					if failure {
						return errors.New("synthetic journal commit failure")
					}
				}
				return writeBackendSessionRoute(dir, name, raw)
			}
			body := &backendNativeGatedBody{ready: make(chan struct{}), release: make(chan struct{}), payload: []byte(rawStream)}
			defer func() { _ = body.Close() }()
			transport := &backendNativeStreamTransport{body: body}
			backend.manager.SetRoundTripperProvider(transport)
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-opus-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Claude-Code-Session-Id", integrationClaudeSession)
			request = request.WithContext(coreexecutor.WithNativeClaudeProtocolHeaders(request.Context(), request.Header))
			writer := &backendNativeHeaderWriter{recorder: httptest.NewRecorder(), committed: make(chan struct{})}
			done := make(chan struct{})
			go func() { backend.Handler().ServeHTTP(writer, request); close(done) }()
			select {
			case <-writer.committed:
			case <-time.After(5 * time.Second):
				t.Fatal("journal callback delayed successful headers until the body")
			}
			if commits.Load() != 1 {
				t.Fatal("trusted upstream headers did not attempt the origin commit before the body")
			}
			opts := backendAPIBackupTestOptions(integrationClaudeSession, false)
			identity, _ := backendSeriesSessionIDs(opts)
			selector.mu.Lock()
			route, found, errLookup := selector.routes.lookup(identity)
			active := len(selector.activeRoutes[identity])
			selector.mu.Unlock()
			if errLookup != nil || !found || route.Committed == failure || active != 1 {
				t.Fatalf("accepted origin/live guard mismatch: committed=%t failure=%t found=%t active=%d error=%v", route.Committed, failure, found, active, errLookup)
			}
			if errClose := body.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("native response failed to finish after body release")
			}
			selector.mu.Lock()
			active = len(selector.activeRoutes[identity])
			selector.mu.Unlock()
			if active != 0 || writer.recorder.Code != http.StatusOK || writer.recorder.Body.String() != rawStream || transport.calls.Load() != 1 {
				t.Fatalf("commit failure altered/replayed response or leaked active generation: active=%d status=%d body=%q attempts=%d", active, writer.recorder.Code, writer.recorder.Body.String(), transport.calls.Load())
			}
		})
	}
}

func TestBackendNativeCanceledStreamReleasesGenerationBeforeBodyUnblocks(t *testing.T) {
	backend := newBackendNativeResponseFixture(t)
	body := &backendNativeGatedBody{ready: make(chan struct{}), release: make(chan struct{})}
	defer func() { _ = body.Close() }()
	transport := &backendNativeStreamTransport{body: body}
	backend.manager.SetRoundTripperProvider(transport)
	requestCtx, cancelRequest := context.WithCancel(t.Context())
	defer cancelRequest()
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-opus-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Claude-Code-Session-Id", integrationClaudeSession)
	request = request.WithContext(coreexecutor.WithNativeClaudeProtocolHeaders(requestCtx, request.Header))
	writer := &backendNativeHeaderWriter{recorder: httptest.NewRecorder(), committed: make(chan struct{})}
	done := make(chan struct{})
	go func() { backend.Handler().ServeHTTP(writer, request); close(done) }()
	select {
	case <-writer.committed:
	case <-time.After(5 * time.Second):
		t.Fatal("native headers waited for the body")
	}
	cancelRequest()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation waited for the gated upstream body")
	}
	selector := backend.seriesSelector
	identity, _ := backendSeriesSessionIDs(backendAPIBackupTestOptions(integrationClaudeSession, false))
	selector.mu.Lock()
	active := len(selector.activeRoutes[identity])
	route, found, errLookup := selector.routes.lookup(identity)
	selector.mu.Unlock()
	if active != 0 || errLookup != nil || !found || !route.Committed || writer.recorder.Code != http.StatusOK || writer.recorder.Body.Len() != 0 || transport.calls.Load() != 1 {
		t.Fatalf("cancellation leaked/replayed generation or lost accepted origin: active=%d found=%t committed=%t status=%d body=%q attempts=%d error=%v", active, found, route.Committed, writer.recorder.Code, writer.recorder.Body.String(), transport.calls.Load(), errLookup)
	}
}

func TestBackendNativeStreamHeadersBeforeBodyAndNoReplay(t *testing.T) {
	const rawStream = ": native heartbeat\r\nevent: message_stop\r\ndata: {\"type\":\"message_stop\"}\r\n\r\n"
	for _, testCase := range []struct {
		name    string
		payload string
		err     error
	}{
		{name: "delayed_body", payload: rawStream},
		{name: "read_error_before_body", err: errors.New("synthetic stream read failed")},
		{name: "empty_body"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			opts := writeSyntheticBackendCredential(t, canonicalTestTempDir(t), "native-stream.json", map[string]any{
				"type": "claude", "access_token": integrationClaudeToken,
				"account_uuid": integrationClaudeAccount, "claude_device_ids": []string{integrationClaudeDevice},
			})
			opts.Provider = "claude"
			store, auth, errLoad := loadBackendCredential(t.Context(), opts)
			if errLoad != nil {
				t.Fatal(errLoad)
			}
			cfg := backendConfig(opts.AuthDir)
			cfg.ProxyURL = ""
			manager := coreauth.NewManager(store, &backendSelector{authID: opts.AuthID, provider: "claude"}, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 1)
			setBackendResultPolicy(manager, nil)
			manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(cfg))
			registry.GetGlobalRegistry().RegisterClient(opts.AuthID, "claude", []*registry.ModelInfo{{ID: backendAuthSelectionModel}})
			t.Cleanup(func() {
				store.seal()
				registry.GetGlobalRegistry().UnregisterClient(opts.AuthID)
			})
			if _, errRegister := manager.Register(coreauth.WithSkipPersist(t.Context()), auth); errRegister != nil {
				t.Fatal(errRegister)
			}
			body := &backendNativeGatedBody{ready: make(chan struct{}), release: make(chan struct{}), payload: []byte(testCase.payload), err: testCase.err}
			defer func() { _ = body.Close() }()
			transport := &backendNativeStreamTransport{body: body}
			manager.SetRoundTripperProvider(transport)
			lifetime, cancel := context.WithCancel(t.Context())
			defer cancel()
			handler := newBackendHandler(lifetime, BackendOptions{Provider: "claude", UseRequestModel: true}, handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-opus-5","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Claude-Code-Session-Id", integrationClaudeSession)
			request = request.WithContext(coreexecutor.WithNativeClaudeProtocolHeaders(request.Context(), request.Header))
			writer := &backendNativeHeaderWriter{recorder: httptest.NewRecorder(), committed: make(chan struct{})}
			done := make(chan struct{})
			go func() {
				handler.ServeHTTP(writer, request)
				close(done)
			}()
			select {
			case <-writer.committed:
			case <-time.After(5 * time.Second):
				t.Fatal("native successful headers waited for a body read")
			}
			if response := writer.recorder.Result(); response.StatusCode != http.StatusOK || response.Header.Get("Request-Id") != "native-headers-first" || response.Header.Get("X-Future-Native-Header") != "preserved" {
				t.Fatalf("initial native headers changed: status=%d headers=%v", response.StatusCode, response.Header)
			}
			// Body reads cannot complete until this explicit release. No clock or
			// latency assertion determines whether headers were sent first.
			if errClose := body.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("native stream did not finish after releasing its body")
			}
			if !bytes.Equal(writer.recorder.Body.Bytes(), []byte(testCase.payload)) {
				t.Fatalf("native body changed: got %q want %q", writer.recorder.Body.Bytes(), testCase.payload)
			}
			if writer.recorder.Code != http.StatusOK || transport.calls.Load() != 1 {
				t.Fatalf("established native stream changed status or replayed: status=%d attempts=%d", writer.recorder.Code, transport.calls.Load())
			}
		})
	}
}
