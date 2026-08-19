package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	executionregistry "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type routeExhaustionTestExecutor struct {
	provider   string
	failErrors map[string]error
}

func newRouteExhaustionTestExecutor(provider string) *routeExhaustionTestExecutor {
	return &routeExhaustionTestExecutor{
		provider:   provider,
		failErrors: make(map[string]error),
	}
}

func (e *routeExhaustionTestExecutor) Identifier() string                { return e.provider }
func (*routeExhaustionTestExecutor) ShouldPrepareRequestAuth(*Auth) bool { return false }
func (e *routeExhaustionTestExecutor) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (e *routeExhaustionTestExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if err, ok := e.failErrors[auth.ID]; ok && err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"choices":[{"message":{"content":"ok"}}]}`)}, nil
}
func (e *routeExhaustionTestExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if err, ok := e.failErrors[auth.ID]; ok && err != nil {
		return nil, err
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"choices":[{"delta":{"content":"ok"}}]}\n\n`)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}
func (e *routeExhaustionTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (*routeExhaustionTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}
func (e *routeExhaustionTestExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if err, ok := e.failErrors[auth.ID]; ok && err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{}, nil
}

func registerRouteTestAuth(t *testing.T, mgr *Manager, provider string, model string, errToReturn error) string {
	t.Helper()
	authID := fmt.Sprintf("%s-auth-%s", provider, uuid.NewString())
	auth := &Auth{
		ID:         authID,
		Provider:   provider,
		Attributes: map[string]string{"disable_cooling": "true"},
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	if _, err := mgr.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return authID
}

// A. Non-stream public exhaustion with three routes/classes/statuses
func TestRouteExhaustion_ThreeRoutes(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	mgr.SetRetryConfig(3, 5*time.Second, 3)

	execClaude := newRouteExhaustionTestExecutor("claude")
	execGemini := newRouteExhaustionTestExecutor("gemini")
	execCodex := newRouteExhaustionTestExecutor("codex")

	mgr.RegisterExecutor(execClaude)
	mgr.RegisterExecutor(execGemini)
	mgr.RegisterExecutor(execCodex)

	model := "test-model-" + uuid.NewString()

	id1 := registerRouteTestAuth(t, mgr, "claude", model, nil)
	id2 := registerRouteTestAuth(t, mgr, "gemini", model, nil)
	id3 := registerRouteTestAuth(t, mgr, "codex", model, nil)

	execClaude.failErrors[id1] = &Error{Code: "rate_limit", Message: "429 Rate Limit", HTTPStatus: 429}
	execGemini.failErrors[id2] = &Error{Code: "rate_limit", Message: "429 Rate Limit", HTTPStatus: 429}
	execCodex.failErrors[id3] = &Error{Code: "rate_limit", Message: "429 Rate Limit", HTTPStatus: 429}

	_, err := mgr.Execute(context.Background(), []string{"claude", "gemini", "codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatalf("Execute() expected error, got nil")
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "attempted routes:") || !strings.Contains(errStr, "claude:429") || !strings.Contains(errStr, "gemini:429") || !strings.Contains(errStr, "codex:429") {
		t.Errorf("unexpected error string: %s", errStr)
	}

	var authErr *Error
	if !errors.As(err, &authErr) || authErr == nil {
		t.Fatalf("errors.As(*Error) failed")
	}
	if authErr.HTTPStatus != 429 {
		t.Errorf("expected status 429, got %d", authErr.HTTPStatus)
	}
}

// B. Security redaction
func TestRouteExhaustion_SecurityRedaction(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	mgr.SetRetryConfig(3, 5*time.Second, 3)

	exec := newRouteExhaustionTestExecutor("gemini")
	mgr.RegisterExecutor(exec)

	model := "redaction-model-" + uuid.NewString()

	secretAuthID := "secret-auth-id-999"
	fakeKey := "sk-secret-api-key-12345"
	emailFilename := "user@secret.com.json"
	baseURL := "https://internal.secret.net/v1"
	privateModel := "private-alias-99"

	auth := &Auth{
		ID:         secretAuthID,
		Provider:   "gemini",
		Attributes: map[string]string{"api_key": fakeKey, "credential_file": emailFilename, "base_url": baseURL, "disable_cooling": "true"},
		Metadata:   map[string]any{"private_model": privateModel},
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	if _, err := mgr.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	exec.failErrors[secretAuthID] = &Error{Code: "upstream_error", Message: "502 Bad Gateway to " + baseURL, HTTPStatus: 502}

	_, err := mgr.Execute(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatalf("Execute() expected error")
	}

	summaryIdx := strings.Index(err.Error(), "attempted routes:")
	if summaryIdx == -1 {
		t.Fatalf("summary missing in error: %s", err.Error())
	}
	summaryPart := err.Error()[summaryIdx:]

	sensitiveTokens := []string{secretAuthID, fakeKey, emailFilename, baseURL, privateModel}
	for _, tok := range sensitiveTokens {
		if strings.Contains(summaryPart, tok) {
			t.Errorf("sensitive token %q leaked in summary part: %s", tok, summaryPart)
		}
	}
}

// C. Duplicate and Cap behavior
func TestRouteExhaustion_DedupAndCap(t *testing.T) {
	tracker := newRouteAttemptTracker()

	// Dedup test
	authGemini := &Auth{Provider: "gemini"}
	authCodex := &Auth{Provider: "codex"}
	err502 := &Error{HTTPStatus: 502}
	err429 := &Error{HTTPStatus: 429}

	tracker.Record(authGemini, err502)
	tracker.Record(authGemini, err502) // dup
	tracker.Record(authCodex, err429)
	tracker.Record(authCodex, err429) // dup

	if summary := tracker.Summary(); summary != "attempted routes: [gemini:502, codex:429]" {
		t.Errorf("dedup failed: %s", summary)
	}

	// Cap test (> 16 unique attempts)
	bigTracker := newRouteAttemptTracker()
	for i := 0; i < 20; i++ {
		provider := fmt.Sprintf("provider-%d", i)
		bigTracker.Record(&Auth{Provider: provider}, &Error{HTTPStatus: 400 + i})
	}
	bigSummary := bigTracker.Summary()
	if !strings.Contains(bigSummary, "... (+4 omitted)") {
		t.Errorf("expected omitted count in summary, got: %s", bigSummary)
	}
}

// D. No-candidate path: exact existing auth_not_found behavior unchanged
func TestRouteExhaustion_NoCandidatePath(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	_, err := mgr.Execute(context.Background(), []string{"unknown-provider"}, cliproxyexecutor.Request{Model: "nonexistent"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatalf("expected error")
	}
	if strings.Contains(err.Error(), "attempted routes:") {
		t.Errorf("no candidate path should not contain attempted routes summary, got: %s", err.Error())
	}
}

// E. ExecuteCount full exhaustion summary
func TestRouteExhaustion_ExecuteCount(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	exec := newRouteExhaustionTestExecutor("openai")
	mgr.RegisterExecutor(exec)

	model := "count-model-" + uuid.NewString()
	authID := registerRouteTestAuth(t, mgr, "openai", model, nil)
	exec.failErrors[authID] = &Error{Code: "bad_gateway", Message: "502 Bad Gateway", HTTPStatus: 502}

	_, err := mgr.ExecuteCount(context.Background(), []string{"openai"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !strings.Contains(err.Error(), "attempted routes: [openai:502]") {
		t.Errorf("ExecuteCount error missing summary: %s", err.Error())
	}
}

// F. Pre-commit ExecuteStream exhaustion summary
func TestRouteExhaustion_ExecuteStream(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	exec := newRouteExhaustionTestExecutor("claude")
	mgr.RegisterExecutor(exec)

	model := "stream-model-" + uuid.NewString()
	authID := registerRouteTestAuth(t, mgr, "claude", model, nil)
	exec.failErrors[authID] = &Error{Code: "rate_limit", Message: "429 Too Many Requests", HTTPStatus: 429}

	_, err := mgr.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatalf("ExecuteStream() expected error")
	}
	if !strings.Contains(err.Error(), "attempted routes: [claude:429]") {
		t.Errorf("ExecuteStream error missing summary, got err=%v", err)
	}
}

// G. Healthy fallback success: no summary leaks into successful response
func TestRouteExhaustion_HealthyFallbackSuccess(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	execClaude := newRouteExhaustionTestExecutor("claude")
	execGemini := newRouteExhaustionTestExecutor("gemini")

	mgr.RegisterExecutor(execClaude)
	mgr.RegisterExecutor(execGemini)

	model := "fallback-model-" + uuid.NewString()
	id1 := registerRouteTestAuth(t, mgr, "claude", model, nil)
	_ = registerRouteTestAuth(t, mgr, "gemini", model, nil)

	execClaude.failErrors[id1] = &Error{Code: "bad_gateway", Message: "502 Bad Gateway", HTTPStatus: 502}

	resp, err := mgr.Execute(context.Background(), []string{"claude", "gemini"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() unexpected error = %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatalf("empty response payload")
	}
}

// H. Request-invalid / cancellation early abort remains unchanged
func TestRouteExhaustion_RequestInvalidEarlyAbort(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	exec := newRouteExhaustionTestExecutor("claude")
	mgr.RegisterExecutor(exec)

	model := "invalid-model-" + uuid.NewString()
	id1 := registerRouteTestAuth(t, mgr, "claude", model, nil)
	id2 := registerRouteTestAuth(t, mgr, "claude", model, nil)

	exec.failErrors[id1] = &Error{Code: "invalid_request", Message: "400 Bad Request", HTTPStatus: 400}
	exec.failErrors[id2] = &Error{Code: "invalid_request", Message: "400 Bad Request", HTTPStatus: 400}

	_, err := mgr.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatalf("expected error")
	}
	var authErr *Error
	if !errors.As(err, &authErr) || authErr.HTTPStatus != 400 {
		t.Errorf("expected status 400, got: %v", err)
	}
	if strings.Contains(err.Error(), "attempted routes:") {
		t.Errorf("request invalid early abort should not contain attempted routes summary, got: %s", err.Error())
	}
}

type routeExhaustionHomeDispatcher struct {
	responses map[int]string
	callCount int
}

func (d *routeExhaustionHomeDispatcher) HeartbeatOK() bool       { return true }
func (d *routeExhaustionHomeDispatcher) AbortAmbiguousDispatch() {}

func (d *routeExhaustionHomeDispatcher) RPopAuth(_ context.Context, _ string, _ string, _ http.Header, _ int) ([]byte, error) {
	resp, ok := d.responses[d.callCount]
	d.callCount++
	if !ok || resp == "" {
		if d.callCount > 1 && d.responses[d.callCount-2] != "" {
			return []byte(d.responses[d.callCount-2]), nil
		}
		return nil, errors.New("no home auth available")
	}
	return []byte(resp), nil
}

func TestRouteExhaustion_HomeMode(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	mgr.SetRetryConfig(3, 5*time.Second, 3)

	execClaude := newRouteExhaustionTestExecutor("claude")
	execGemini := newRouteExhaustionTestExecutor("gemini")
	mgr.RegisterExecutor(execClaude)
	mgr.RegisterExecutor(execGemini)

	model := "home-model-" + uuid.NewString()

	authID1 := "home-secret-auth-1"
	key1 := "sk-home-secret-key-111"
	file1 := "user1@home-secret.com.json"
	url1 := "https://home1.secret.net/v1"
	alias1 := "secret-home-alias-1"

	authID2 := "home-secret-auth-2"
	key2 := "sk-home-secret-key-222"
	file2 := "user2@home-secret.com.json"
	url2 := "https://home2.secret.net/v1"
	alias2 := "secret-home-alias-2"

	payload1 := fmt.Sprintf(`{"provider":"claude","auth":{"id":%q,"provider":"claude","status":"active","attributes":{"api_key":%q,"credential_file":%q,"base_url":%q,"disable_cooling":"true"},"metadata":{"private_model":%q}}}`, authID1, key1, file1, url1, alias1)
	payload2 := fmt.Sprintf(`{"provider":"gemini","auth":{"id":%q,"provider":"gemini","status":"active","attributes":{"api_key":%q,"credential_file":%q,"base_url":%q,"disable_cooling":"true"},"metadata":{"private_model":%q}}}`, authID2, key2, file2, url2, alias2)

	execClaude.failErrors[authID1] = &Error{Code: "rate_limit", Message: "429 Rate Limit", HTTPStatus: 429}
	execGemini.failErrors[authID2] = &Error{Code: "bad_gateway", Message: "502 Bad Gateway", HTTPStatus: 502}

	t.Run("Exhaustion", func(t *testing.T) {
		dispatcher := &routeExhaustionHomeDispatcher{
			responses: map[int]string{
				0: payload1,
				1: payload2,
			},
		}
		mgr.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)

		_, err := mgr.Execute(context.Background(), []string{"claude", "gemini"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		if err == nil {
			t.Fatalf("Execute() expected error on home route exhaustion")
		}

		errStr := err.Error()
		summaryIdx := strings.Index(errStr, "attempted routes:")
		if summaryIdx == -1 {
			t.Fatalf("summary missing in home route exhaustion error: %s", errStr)
		}
		summaryPart := errStr[summaryIdx:]

		if !strings.Contains(summaryPart, "claude:429") || !strings.Contains(summaryPart, "gemini:502") {
			t.Errorf("expected attempted routes [claude:429, gemini:502] in summary, got: %s", summaryPart)
		}

		var authErr *Error
		if !errors.As(err, &authErr) || authErr == nil {
			t.Fatalf("errors.As(*Error) failed on home route exhaustion error")
		}
		if authErr.HTTPStatus != 502 {
			t.Errorf("expected preserved cause status 502, got %d", authErr.HTTPStatus)
		}
		if authErr.Code != "bad_gateway" {
			t.Errorf("expected preserved cause code bad_gateway, got %s", authErr.Code)
		}

		secrets := []string{authID1, authID2, key1, key2, file1, file2, url1, url2, alias1, alias2}
		for _, sec := range secrets {
			if strings.Contains(summaryPart, sec) {
				t.Errorf("sensitive secret %q leaked in home route summary: %s", sec, summaryPart)
			}
		}
	})

	t.Run("HealthyFallbackSuccess", func(t *testing.T) {
		execGemini.failErrors[authID2] = nil
		dispatcher := &routeExhaustionHomeDispatcher{
			responses: map[int]string{
				0: payload1,
				1: payload2,
			},
		}
		mgr.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)

		resp, err := mgr.Execute(context.Background(), []string{"claude", "gemini"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		if err != nil {
			t.Fatalf("Execute() unexpected error on healthy home fallback: %v", err)
		}
		if len(resp.Payload) == 0 {
			t.Errorf("expected non-empty payload on home fallback success")
		}
	})
}

type routeExhaustionHomeNoModelDispatcher struct {
	payloads []string
	count    int
}

func (d *routeExhaustionHomeNoModelDispatcher) HeartbeatOK() bool       { return true }
func (d *routeExhaustionHomeNoModelDispatcher) AbortAmbiguousDispatch() {}
func (d *routeExhaustionHomeNoModelDispatcher) RPopAuth(_ context.Context, _ string, _ string, _ http.Header, _ int) ([]byte, error) {
	if d.count >= len(d.payloads) {
		return nil, errors.New("no home auth available")
	}
	next := d.payloads[d.count]
	d.count++
	if next == "" {
		return nil, errors.New("no home auth available")
	}
	return []byte(next), nil
}

// I. Home no-execution-models path: a dispatched auth blocked for its upstream model yields a
// sanitized attempted-routes diagnostic (records the no_model outcome) instead of an empty error.
func TestRouteExhaustion_HomeNoExecutionModelsDiagnostic(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	mgr.SetRetryConfig(3, 5*time.Second, 3)

	exec := newRouteExhaustionTestExecutor("claude")
	mgr.RegisterExecutor(exec)

	authID := "home-no-model-auth"
	fakeKey := "sk-home-no-model-secret-123"
	credFile := "user-no-model@home-secret.com.json"

	payload := fmt.Sprintf(
		`{"model":"upstream-blocked-1","provider":"claude","auth":{"id":%q,"provider":"claude","status":"active","attributes":{"api_key":%q,"credential_file":%q,"disable_cooling":"true"},"unavailable":true,"next_retry_after":"2030-01-02T03:04:05Z"}}`,
		authID, fakeKey, credFile,
	)

	dispatcher := &routeExhaustionHomeNoModelDispatcher{payloads: []string{payload}}
	mgr.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)

	_, err := mgr.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-3-5-sonnet"}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatalf("Execute() expected error on home no-execution-models route")
	}

	summaryIdx := strings.Index(err.Error(), "attempted routes:")
	if summaryIdx == -1 {
		t.Fatalf("no-model home path missing attempted-routes diagnostic: %s", err.Error())
	}
	summaryPart := err.Error()[summaryIdx:]

	if !strings.Contains(summaryPart, "claude:") {
		t.Errorf("expected claude provider recorded in summary, got: %s", summaryPart)
	}
	for _, secret := range []string{authID, fakeKey, credFile} {
		if strings.Contains(summaryPart, secret) {
			t.Errorf("sensitive token %q leaked in home no-model summary: %s", secret, summaryPart)
		}
	}
}

// routeExhaustionHeaderCause is a generic non-*Error cause that exposes
// headers the same way upstream errors (streamBootstrapError,
// modelCooldownError) do, so handlers collecting passthrough headers from the
// final routed error can still surface them through route exhaustion.
type routeExhaustionHeaderCause struct {
	msg     string
	headers http.Header
}

func (e *routeExhaustionHeaderCause) Error() string { return e.msg }
func (e *routeExhaustionHeaderCause) Headers() http.Header {
	return e.headers.Clone()
}

// routeExhaustionNoHeaderCause is a generic non-*Error cause that exposes no
// headers, mirroring ordinary upstream failures.
type routeExhaustionNoHeaderCause struct{ msg string }

func (e *routeExhaustionNoHeaderCause) Error() string { return e.msg }

// J. Wrapper contract: a wrapped cause exposing headers retains them, while
// Error/Unwrap and the sanitized route summary stay intact.
func TestRouteExhaustion_HeadersForwarded(t *testing.T) {
	tracker := newRouteAttemptTracker()
	tracker.Record(&Auth{Provider: "gemini"}, &Error{HTTPStatus: 429})

	cause := &routeExhaustionHeaderCause{
		msg:     "upstream retry-after",
		headers: http.Header{"Retry-After": {"1"}, "X-Request-Id": {"req-123"}},
	}
	err := wrapRouteExhaustion(cause, tracker)

	// errors.As / errors.Is must still traverse the wrapper.
	var unwrapped *routeExhaustionHeaderCause
	if !errors.As(err, &unwrapped) || unwrapped == nil {
		t.Fatalf("errors.As(*routeExhaustionHeaderCause) failed, err=%v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is(err, cause) = false")
	}

	// Sanitized route summary retained.
	if !strings.Contains(err.Error(), "attempted routes: [gemini") {
		t.Errorf("unexpected error string, summary missing routes: %s", err.Error())
	}

	// Headers readable via the same assertion handlers use; values exact.
	he, ok := err.(interface{ Headers() http.Header })
	if !ok || he == nil {
		t.Fatalf("routeExhaustionError must implement Headers(), err=%T", err)
	}
	hdr := he.Headers()
	if hdr.Get("Retry-After") != "1" {
		t.Errorf("Headers().Get(Retry-After) = %q, want 1", hdr.Get("Retry-After"))
	}
	if hdr.Get("X-Request-Id") != "req-123" {
		t.Errorf("Headers().Get(X-Request-Id) = %q, want req-123", hdr.Get("X-Request-Id"))
	}

	// Forwarded map is a copy: mutating it must not touch the caller's map.
	hdr.Set("Retry-After", "999")
	if cause.headers.Get("Retry-After") != "1" {
		t.Errorf("wrapped headers mutated caller map: got %q", cause.headers.Get("Retry-After"))
	}
}

// K. Wrapper contract: a cause without Headers yields nil, matching the
// convention other header-carriers follow for absent headers.
func TestRouteExhaustion_HeadersAbsentNil(t *testing.T) {
	tracker := newRouteAttemptTracker()
	tracker.Record(&Auth{Provider: "openai"}, &Error{HTTPStatus: 502})

	err := wrapRouteExhaustion(&routeExhaustionNoHeaderCause{msg: "502 Bad Gateway"}, tracker)

	he, ok := err.(interface{ Headers() http.Header })
	if !ok || he == nil {
		t.Fatalf("routeExhaustionError must implement Headers(), err=%T", err)
	}
	if got := he.Headers(); got != nil {
		t.Errorf("Headers() = %v, want nil for cause without headers", got)
	}
}

// L. Stream-level: headers reach the returned route-exhaustion error so the
// stream/error handlers can surface passthrough headers.
func TestRouteExhaustion_ExecuteStreamHeaders(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	mgr.SetRetryConfig(3, 5*time.Second, 3)

	exec := newRouteExhaustionTestExecutor("claude")
	mgr.RegisterExecutor(exec)

	model := "stream-hdr-model-" + uuid.NewString()
	authID := registerRouteTestAuth(t, mgr, "claude", model, nil)
	exec.failErrors[authID] = &routeExhaustionHeaderCause{
		msg:     "429 Too Many Requests",
		headers: http.Header{"Retry-After": {"1"}, "X-Request-Id": {"req-777"}},
	}

	_, err := mgr.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatalf("ExecuteStream() expected error on route exhaustion")
	}
	if !strings.Contains(err.Error(), "attempted routes: [claude") {
		t.Errorf("ExecuteStream error missing route summary: %v", err)
	}

	he, ok := err.(interface{ Headers() http.Header })
	if !ok || he == nil {
		t.Fatalf("ExecuteStream() error must implement Headers(), got %T", err)
	}
	hdr := he.Headers()
	if hdr.Get("Retry-After") != "1" {
		t.Errorf("Headers().Get(Retry-After) = %q, want 1", hdr.Get("Retry-After"))
	}
	if hdr.Get("X-Request-Id") != "req-777" {
		t.Errorf("Headers().Get(X-Request-Id) = %q, want req-777", hdr.Get("X-Request-Id"))
	}
}

// routeExhaustionNestedCause is a header-carrier that also unwraps to an inner
// header-carrier, so the first/outermost carrier must win per errors.As.
type routeExhaustionNestedCause struct {
	inner   *routeExhaustionHeaderCause
	headers http.Header
}

func (e *routeExhaustionNestedCause) Error() string        { return "outer wrapped cause" }
func (e *routeExhaustionNestedCause) Unwrap() error        { return e.inner }
func (e *routeExhaustionNestedCause) Headers() http.Header { return e.headers }

// M. Wrapper contract: nil receiver returns nil, not a panic.
func TestRouteExhaustion_HeadersNilReceiver(t *testing.T) {
	var e *routeExhaustionClonedError
	if hdr := e.Headers(); hdr != nil {
		t.Errorf("Headers() = %v, want nil for nil receiver", hdr)
	}
	if hdr := e.SafeResponseHeaders(); hdr != nil {
		t.Errorf("SafeResponseHeaders() = %v, want nil for nil receiver", hdr)
	}
}

// N. Wrapper contract: errors.As starts at the cause and returns the
// first/outermost carrier; an inner carrier must not shadow it, and the
// forwarded map is a fresh clone even when the outer carrier returns raw.
func TestRouteExhaustion_HeadersNestedOutermostWins(t *testing.T) {
	tracker := newRouteAttemptTracker()
	tracker.Record(&Auth{Provider: "gemini"}, &Error{HTTPStatus: 429})

	inner := &routeExhaustionHeaderCause{
		msg:     "inner retry-after",
		headers: http.Header{"Retry-After": {"inner"}, "Inner": {"1"}},
	}
	outer := &routeExhaustionNestedCause{
		inner:   inner,
		headers: http.Header{"Retry-After": {"outer"}, "Outer": {"1"}},
	}
	err := wrapRouteExhaustion(outer, tracker)

	he, ok := err.(interface{ Headers() http.Header })
	if !ok || he == nil {
		t.Fatalf("routeExhaustionError must implement Headers(), err=%T", err)
	}
	hdr := he.Headers()
	if hdr.Get("Retry-After") != "outer" {
		t.Errorf("Headers().Get(Retry-After) = %q, want outer", hdr.Get("Retry-After"))
	}
	if hdr.Get("Outer") != "1" {
		t.Errorf("Headers().Get(Outer) = %q, want 1", hdr.Get("Outer"))
	}
	if hdr.Get("Inner") != "" {
		t.Errorf("headers from inner carrier leaked, outermost must win: %q", hdr.Get("Inner"))
	}

	// The inner carrier remains reachable through the Unwrap chain.
	var unwrapped *routeExhaustionHeaderCause
	if !errors.As(err, &unwrapped) || unwrapped == nil {
		t.Fatalf("errors.As(*routeExhaustionHeaderCause) failed, err=%v", err)
	}

	// Forwarded map is a fresh clone of the outer cause's raw map.
	hdr.Set("Retry-After", "999")
	if outer.headers.Get("Retry-After") != "outer" {
		t.Errorf("wrapped headers mutated caller map: got %q", outer.headers.Get("Retry-After"))
	}
	if inner.headers.Get("Retry-After") != "inner" {
		t.Errorf("inner caller map mutated: got %q", inner.headers.Get("Retry-After"))
	}
}

// O. Wrapper contract: the Error message appends the sanitized summary exactly
// once and never mutates the cause's *Error.Message.
func TestRouteExhaustion_SummaryAppendedOnceMessageUnchanged(t *testing.T) {
	tracker := newRouteAttemptTracker()
	tracker.Record(&Auth{Provider: "gemini"}, &Error{HTTPStatus: 429})
	tracker.Record(&Auth{Provider: "claude"}, &Error{HTTPStatus: 502})

	original := &Error{
		Code:       "bad_gateway",
		Message:    "502 Bad Gateway",
		Retryable:  true,
		HTTPStatus: 502,
	}
	var originalMessage = original.Message

	err := wrapRouteExhaustion(original, tracker)
	if err == nil || err == original {
		t.Fatalf("wrapRouteExhaustion must return a wrapper (got %T, same=%v)", err, err == original)
	}

	// Message contains the summary exactly once, appended after the cause.
	got := err.Error()
	if strings.Count(got, "attempted routes:") != 1 {
		t.Errorf("summary appended more than once, got: %s", got)
	}
	wantPrefix := original.Error()
	if !strings.HasPrefix(got, wantPrefix+"; ") {
		t.Errorf("message should start with cause message, got: %s", got)
	}
	if !strings.Contains(got, "attempted routes: [gemini:429, claude:502]") {
		t.Errorf("expected both recorded routes in summary, got: %s", got)
	}

	// The cause's stored Message is never mutated.
	if original.Message != originalMessage {
		t.Errorf("cause *Error.Message mutated: %q -> %q", originalMessage, original.Message)
	}

	// The wrapper unwraps the ORIGINAL cause, so identity/typed access is preserved.
	var authErr *Error
	if !errors.As(err, &authErr) || authErr == nil {
		t.Fatalf("errors.As(*Error) failed on wrapped error")
	}
	if authErr.HTTPStatus != 502 || !authErr.Retryable || authErr.Code != "bad_gateway" {
		t.Errorf("wrapped cause lost typed fields, got code=%q status=%d retry=%v", authErr.Code, authErr.HTTPStatus, authErr.Retryable)
	}
	if !errors.Is(err, original) {
		t.Errorf("errors.Is(wrappedError, original) = false, want true (original cause preserved)")
	}
}

// P. Wrapper contract: SafeResponseHeaders forwards the Home busy error's trusted
// Retry-After through route exhaustion via the same access path handlers use.
func TestRouteExhaustion_SafeResponseHeadersForwarded(t *testing.T) {
	tracker := newRouteAttemptTracker()
	tracker.Record(&Auth{Provider: "gemini"}, &Error{HTTPStatus: 429})

	cause := NewHomeConcurrencyBusyError("credential concurrency limit exceeded", 7*time.Second)
	err := wrapRouteExhaustion(cause, tracker)

	se, ok := err.(interface{ SafeResponseHeaders() http.Header })
	if !ok || se == nil {
		t.Fatalf("wrapped error must implement SafeResponseHeaders(), err=%T", err)
	}
	if got := se.SafeResponseHeaders().Get("Retry-After"); got != "7" {
		t.Errorf("SafeResponseHeaders().Get(Retry-After) = %q, want 7", got)
	}

	// Each call yields a fresh map: mutating one result must not affect the next
	// (the underlying Home busy error keeps its own Retry-After).
	first := se.SafeResponseHeaders()
	first.Set("Retry-After", "999")
	if got := se.SafeResponseHeaders().Get("Retry-After"); got != "7" {
		t.Errorf("SafeResponseHeaders returned a shared map: got %q after mutating a prior call", got)
	}
}

// Q. Wrapper contract: a cause without SafeResponseHeaders yields nil.
func TestRouteExhaustion_SafeResponseHeadersAbsentNil(t *testing.T) {
	tracker := newRouteAttemptTracker()
	tracker.Record(&Auth{Provider: "openai"}, &Error{HTTPStatus: 502})

	err := wrapRouteExhaustion(&routeExhaustionNoHeaderCause{msg: "502 Bad Gateway"}, tracker)

	se, ok := err.(interface{ SafeResponseHeaders() http.Header })
	if !ok || se == nil {
		t.Fatalf("wrapped error must implement SafeResponseHeaders(), err=%T", err)
	}
	if got := se.SafeResponseHeaders(); got != nil {
		t.Errorf("SafeResponseHeaders() = %v, want nil for cause without it", got)
	}

	// Header cause (generic Headers only) also yields nil for SafeResponseHeaders.
	headerErr := wrapRouteExhaustion(&routeExhaustionHeaderCause{msg: "429", headers: http.Header{"Retry-After": {"1"}}}, tracker)
	if hse, ok := headerErr.(interface{ SafeResponseHeaders() http.Header }); ok && hse != nil {
		if got := hse.SafeResponseHeaders(); got != nil {
			t.Errorf("SafeResponseHeaders() = %v, want nil for Headers-only cause", got)
		}
	}
}

func TestRouteExhaustion_PreservesStructuredJSONRequestFault(t *testing.T) {
	tracker := newRouteAttemptTracker()
	tracker.Record(&Auth{Provider: "openai"}, &Error{HTTPStatus: 502})

	rawJSON := `{"error":{"type":"invalid_request_error","code":"cyber_policy","message":"blocked"}}`
	cause := errors.New(rawJSON)
	wrapped := wrapRouteExhaustion(cause, tracker)

	if !json.Valid([]byte(wrapped.Error())) {
		t.Fatalf("wrapped.Error() corrupted structured JSON: %s", wrapped.Error())
	}
	if wrapped.Error() != rawJSON {
		t.Fatalf("wrapped.Error() = %q, want original %q", wrapped.Error(), rawJSON)
	}
}
