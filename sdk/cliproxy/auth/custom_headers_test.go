package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestExtractCustomHeadersFromMetadata(t *testing.T) {
	meta := map[string]any{
		"headers": map[string]any{
			" X-Test ": " value ",
			"":         "ignored",
			"X-Empty":  "   ",
			"X-Num":    float64(1),
		},
	}

	got := ExtractCustomHeadersFromMetadata(meta)
	want := map[string]string{"X-Test": "value"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractCustomHeadersFromMetadata() = %#v, want %#v", got, want)
	}
}

func TestApplyCustomHeadersFromMetadata(t *testing.T) {
	auth := &Auth{
		Metadata: map[string]any{
			"headers": map[string]string{
				"X-Test":  "new",
				"X-Empty": "   ",
			},
		},
		Attributes: map[string]string{
			"header:X-Test": "old",
			"keep":          "1",
		},
	}

	ApplyCustomHeadersFromMetadata(auth)

	if got := auth.Attributes["header:X-Test"]; got != "new" {
		t.Fatalf("header:X-Test = %q, want %q", got, "new")
	}
	if _, ok := auth.Attributes["header:X-Empty"]; ok {
		t.Fatalf("expected header:X-Empty to be absent, got %#v", auth.Attributes["header:X-Empty"])
	}
	if got := auth.Attributes["keep"]; got != "1" {
		t.Fatalf("keep = %q, want %q", got, "1")
	}
}

func TestApplyCustomHeadersFromMetadata_CPASessionID(t *testing.T) {
	auth := &Auth{
		Metadata: map[string]any{
			"headers": map[string]string{
				"X-CPA-Session": "$CPA-SESSION-ID",
			},
		},
	}

	ApplyCustomHeadersFromMetadata(auth)

	if got := auth.Attributes["header:X-CPA-Session"]; got != "$CPA-SESSION-ID" {
		t.Fatalf("header:X-CPA-Session = %q, want %q", got, "$CPA-SESSION-ID")
	}
}

type captureHeadersTestExecutor struct {
	gotHeaders http.Header
}

func (e *captureHeadersTestExecutor) Identifier() string { return "test-provider" }
func (e *captureHeadersTestExecutor) Models() []string   { return []string{"test-model"} }
func (e *captureHeadersTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	httpReq := httptest.NewRequest("POST", "https://api.example.com", nil).WithContext(ctx)
	util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes, opts.Headers)
	e.gotHeaders = httpReq.Header.Clone()
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}
func (e *captureHeadersTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	httpReq := httptest.NewRequest("POST", "https://api.example.com", nil).WithContext(ctx)
	util.ApplyCustomHeadersFromAttrs(httpReq, auth.Attributes, opts.Headers)
	e.gotHeaders = httpReq.Header.Clone()
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"id":"1"}`)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}
func (e *captureHeadersTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (e *captureHeadersTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *captureHeadersTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManagerExecute_CPASessionID_AffinityOnAndOff(t *testing.T) {
	for _, sessionAffinity := range []bool{false, true} {
		t.Run(map[bool]string{true: "SessionAffinity=true", false: "SessionAffinity=false"}[sessionAffinity], func(t *testing.T) {
			var selector Selector
			if sessionAffinity {
				selector = NewSessionAffinitySelector(&RoundRobinSelector{})
			} else {
				selector = &RoundRobinSelector{}
			}

			exec := &captureHeadersTestExecutor{}
			mgr := NewManager(nil, selector, nil)
			mgr.RegisterExecutor(exec)

			authObj := &Auth{
				ID:       "auth-1",
				Provider: "test-provider",
				Status:   StatusActive,
				Attributes: map[string]string{
					"header:X-Forwarded-Session": "$CPA-SESSION-ID",
					"header:Authorization":       "Bearer $CPA-SESSION-ID",
				},
			}
			registry.GetGlobalRegistry().RegisterClient(authObj.ID, authObj.Provider, []*registry.ModelInfo{{ID: "test-model"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authObj.ID) })
			if _, errReg := mgr.Register(context.Background(), authObj); errReg != nil {
				t.Fatalf("Register error = %v", errReg)
			}

			req := cliproxyexecutor.Request{
				Model:   "test-model",
				Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
			}
			opts := cliproxyexecutor.Options{
				Headers: http.Header{
					"X-Session-ID": []string{"aff-toggle-sess-1"},
				},
			}

			_, errExec := mgr.Execute(context.Background(), []string{"test-provider"}, req, opts)
			if errExec != nil {
				t.Fatalf("Execute() error = %v", errExec)
			}

			wantSession := "header:aff-toggle-sess-1"
			if got := exec.gotHeaders.Get("X-Forwarded-Session"); got != wantSession {
				t.Errorf("X-Forwarded-Session = %q, want %q", got, wantSession)
			}
			if got := exec.gotHeaders.Get("Authorization"); got != "Bearer "+wantSession {
				t.Errorf("Authorization = %q, want %q", got, "Bearer "+wantSession)
			}
		})
	}
}

func TestManagerExecuteStream_CPASessionID_AffinityOnAndOff(t *testing.T) {
	for _, sessionAffinity := range []bool{false, true} {
		t.Run(map[bool]string{true: "SessionAffinity=true", false: "SessionAffinity=false"}[sessionAffinity], func(t *testing.T) {
			var selector Selector
			if sessionAffinity {
				selector = NewSessionAffinitySelector(&RoundRobinSelector{})
			} else {
				selector = &RoundRobinSelector{}
			}

			exec := &captureHeadersTestExecutor{}
			mgr := NewManager(nil, selector, nil)
			mgr.RegisterExecutor(exec)

			authObj := &Auth{
				ID:       "auth-stream-1",
				Provider: "test-provider",
				Status:   StatusActive,
				Attributes: map[string]string{
					"header:X-Stream-Session": "$CPA-SESSION-ID",
				},
			}
			registry.GetGlobalRegistry().RegisterClient(authObj.ID, authObj.Provider, []*registry.ModelInfo{{ID: "test-model"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authObj.ID) })
			if _, errReg := mgr.Register(context.Background(), authObj); errReg != nil {
				t.Fatalf("Register error = %v", errReg)
			}

			req := cliproxyexecutor.Request{
				Model:   "test-model",
				Payload: []byte(`{"messages":[{"role":"user","content":"stream hello"}]}`),
			}
			opts := cliproxyexecutor.Options{
				Stream: true,
				Headers: http.Header{
					"X-Claude-Code-Session-Id": []string{"claude-stream-sess-42"},
				},
			}

			result, errExec := mgr.ExecuteStream(context.Background(), []string{"test-provider"}, req, opts)
			if errExec != nil {
				t.Fatalf("ExecuteStream() error = %v", errExec)
			}
			if result != nil {
				for range result.Chunks {
				}
			}

			wantSession := "claude:claude-stream-sess-42"
			if got := exec.gotHeaders.Get("X-Stream-Session"); got != wantSession {
				t.Errorf("X-Stream-Session = %q, want %q", got, wantSession)
			}
		})
	}
}

func TestManagerExecute_CPASessionID_InterceptorClearsSession(t *testing.T) {
	exec := &captureHeadersTestExecutor{}
	mgr := NewManager(nil, &RoundRobinSelector{}, nil)
	mgr.RegisterExecutor(exec)

	authObj := &Auth{
		ID:       "auth-clear-1",
		Provider: "test-provider",
		Status:   StatusActive,
		Attributes: map[string]string{
			"header:X-Forwarded-Session": "$CPA-SESSION-ID",
			"header:Authorization":       "Bearer $CPA-SESSION-ID",
			"header:Static-Header":       "keep-me",
		},
	}
	registry.GetGlobalRegistry().RegisterClient(authObj.ID, authObj.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authObj.ID) })
	if _, errReg := mgr.Register(context.Background(), authObj); errReg != nil {
		t.Fatalf("Register error = %v", errReg)
	}

	req := cliproxyexecutor.Request{
		Model:   "test-model",
		Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Session-ID": []string{"stale-session-123"},
		},
		RequestAfterAuthInterceptor: func(ctx context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
			return cliproxyexecutor.RequestAfterAuthInterceptResponse{
				ClearHeaders: []string{"X-Session-ID"},
			}
		},
	}

	// Context initially carries the session identity
	ctx := util.WithSessionID(context.Background(), "header:stale-session-123")

	_, errExec := mgr.Execute(ctx, []string{"test-provider"}, req, opts)
	if errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}

	if _, exists := exec.gotHeaders["X-Forwarded-Session"]; exists {
		t.Errorf("expected X-Forwarded-Session to be omitted when session cleared by interceptor, got %q", exec.gotHeaders.Get("X-Forwarded-Session"))
	}
	if _, exists := exec.gotHeaders["Authorization"]; exists {
		t.Errorf("expected Authorization to be omitted when session cleared by interceptor, got %q", exec.gotHeaders.Get("Authorization"))
	}
	if got := exec.gotHeaders.Get("Static-Header"); got != "keep-me" {
		t.Errorf("Static-Header = %q, want %q", got, "keep-me")
	}
}
