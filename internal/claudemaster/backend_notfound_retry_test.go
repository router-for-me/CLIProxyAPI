package claudemaster

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// bodilessUpstreamErr is an upstream error response as the executor reports one.
type bodilessUpstreamErr struct {
	status  int
	headers http.Header
	body    []byte
}

func (e *bodilessUpstreamErr) Error() string                { return "upstream " + http.StatusText(e.status) }
func (e *bodilessUpstreamErr) StatusCode() int              { return e.status }
func (e *bodilessUpstreamErr) ResponseHeaders() http.Header { return e.headers }
func (e *bodilessUpstreamErr) ResponseBody() []byte         { return e.body }

// scriptedExecutor answers each call with the next scripted error (nil = success) and counts calls.
type scriptedExecutor struct {
	coreauth.ProviderExecutor
	answers []error
	calls   int
	auths   []string
}

func (e *scriptedExecutor) next(auth *coreauth.Auth) error {
	e.calls++
	e.auths = append(e.auths, backendAuthID(auth))
	if len(e.answers) == 0 {
		return nil
	}
	err := e.answers[0]
	e.answers = e.answers[1:]
	return err
}

func (e *scriptedExecutor) Execute(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	if err := e.next(auth); err != nil {
		return coreexecutor.Response{}, err
	}
	return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *scriptedExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}

func (e *scriptedExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	if err := e.next(auth); err != nil {
		return nil, err
	}
	ch := make(chan coreexecutor.StreamChunk)
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}

func bodiless404() error {
	return &bodilessUpstreamErr{status: http.StatusNotFound, headers: http.Header{"Content-Type": {"text/html"}, "Cf-Ray": {"ray-1"}}, body: []byte("<html>not found</html>")}
}

func anthropic404() error {
	return &bodilessUpstreamErr{status: http.StatusNotFound, headers: http.Header{"Content-Type": {"application/json"}}, body: []byte(`{"type":"error","error":{"type":"not_found_error","message":"model: claude-test-model"}}`)}
}

func quickRetry(t *testing.T) {
	saved := backendNotFoundRetryDelay
	backendNotFoundRetryDelay = time.Millisecond
	t.Cleanup(func() { backendNotFoundRetryDelay = saved })
}

// A 404 with no Anthropic error is retried once, on the same subscription, and a success on the
// retry leaves no upstream error on the request (nothing to relay to the session).
func TestBodilessNotFoundIsRetriedOnceOnTheSameSubscription(t *testing.T) {
	quickRetry(t)
	for name, call := range map[string]func(context.Context, coreauth.ProviderExecutor, *coreauth.Auth) error{
		"execute": func(ctx context.Context, ex coreauth.ProviderExecutor, a *coreauth.Auth) error {
			_, err := ex.Execute(ctx, a, coreexecutor.Request{Model: "claude-test-model"}, coreexecutor.Options{})
			return err
		},
		"count": func(ctx context.Context, ex coreauth.ProviderExecutor, a *coreauth.Auth) error {
			_, err := ex.CountTokens(ctx, a, coreexecutor.Request{Model: "claude-test-model"}, coreexecutor.Options{})
			return err
		},
		"stream": func(ctx context.Context, ex coreauth.ProviderExecutor, a *coreauth.Auth) error {
			_, err := ex.ExecuteStream(ctx, a, coreexecutor.Request{Model: "claude-test-model"}, coreexecutor.Options{})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			inner := &scriptedExecutor{answers: []error{bodiless404(), nil}}
			ex := withBackendUpstreamRecording(inner)
			ctx := withBackendAttempt(context.Background())
			auth := backendSeriesTestAuth("profile-a", "claude")
			if err := call(ctx, ex, auth); err != nil {
				t.Fatalf("the retry's success was not returned: %v", err)
			}
			if inner.calls != 2 || inner.auths[0] != "profile-a" || inner.auths[1] != "profile-a" {
				t.Fatalf("want exactly two calls on profile-a, got %d on %v", inner.calls, inner.auths)
			}
			if backendUpstreamErrorFrom(ctx) != nil {
				t.Fatal("a successful retry must leave no upstream error to relay")
			}
		})
	}
}

// The retry is one per request: a second bodiless 404 is returned (and recorded, so the session
// gets it exactly), and the executor is not asked a third time.
func TestBodilessNotFoundIsRetriedOnlyOnce(t *testing.T) {
	quickRetry(t)
	inner := &scriptedExecutor{answers: []error{bodiless404(), bodiless404(), nil}}
	ex := withBackendUpstreamRecording(inner)
	ctx := withBackendAttempt(context.Background())
	_, err := ex.Execute(ctx, backendSeriesTestAuth("profile-a", "claude"), coreexecutor.Request{Model: "claude-test-model"}, coreexecutor.Options{})
	if err == nil {
		t.Fatal("two 404s must surface as an error")
	}
	if inner.calls != 2 {
		t.Fatalf("want exactly two calls, got %d", inner.calls)
	}
	upstream := backendUpstreamErrorFrom(ctx)
	if upstream == nil || upstream.status != http.StatusNotFound || string(upstream.body) != "<html>not found</html>" {
		t.Fatalf("the second 404 must be recorded exactly for the session: %#v", upstream)
	}
	// and the scheduler's second ask on the bound subscription is refused honestly
	selector := &backendSeriesSelector{authIDs: []string{"profile-a"}, provider: "claude", names: map[string]string{"profile-a": "alpha"}}
	t.Cleanup(selector.Stop)
	recordBackendAttempt(ctx, coreauth.Result{AuthID: "profile-a", Model: "claude-test-model", Error: &coreauth.Error{HTTPStatus: http.StatusNotFound}})
	reason := selector.boundUnavailableErrorLocked(ctx, "profile-a", "claude-test-model").Error()
	for _, want := range []string{"subscription alpha", "HTTP 404 with no Anthropic error", "twice", "/model"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("refusal lacks %q: %s", want, reason)
		}
	}
	if strings.Contains(reason, "does not serve") {
		t.Fatalf("a bodiless 404 must not be called a model the subscription does not serve: %s", reason)
	}
}

// Anthropic's own not_found_error is a verdict, not a transient: no retry, and the refusal keeps
// saying the subscription does not serve the model.
func TestAnthropicNotFoundIsNotRetried(t *testing.T) {
	quickRetry(t)
	inner := &scriptedExecutor{answers: []error{anthropic404(), nil}}
	ex := withBackendUpstreamRecording(inner)
	ctx := withBackendAttempt(context.Background())
	_, err := ex.Execute(ctx, backendSeriesTestAuth("profile-a", "claude"), coreexecutor.Request{Model: "claude-test-model"}, coreexecutor.Options{})
	var upstream backendUpstreamResponseError
	if !errors.As(err, &upstream) || inner.calls != 1 {
		t.Fatalf("an Anthropic not_found_error must be returned at once: calls=%d err=%v", inner.calls, err)
	}
	selector := &backendSeriesSelector{authIDs: []string{"profile-a"}, provider: "claude", names: map[string]string{"profile-a": "alpha"}}
	t.Cleanup(selector.Stop)
	recordBackendAttempt(ctx, coreauth.Result{AuthID: "profile-a", Model: "claude-test-model", Error: &coreauth.Error{HTTPStatus: http.StatusNotFound}})
	reason := selector.boundUnavailableErrorLocked(ctx, "profile-a", "claude-test-model").Error()
	if !strings.Contains(reason, "does not serve model claude-test-model (HTTP 404)") {
		t.Fatalf("an Anthropic not_found_error keeps its wording: %s", reason)
	}
}

// Any other error is not touched by the retry: a 500 goes straight back.
func TestOtherUpstreamErrorsAreNotRetriedHere(t *testing.T) {
	quickRetry(t)
	inner := &scriptedExecutor{answers: []error{&bodilessUpstreamErr{status: 500, body: []byte("")}, nil}}
	ex := withBackendUpstreamRecording(inner)
	ctx := withBackendAttempt(context.Background())
	if _, err := ex.Execute(ctx, backendSeriesTestAuth("profile-a", "claude"), coreexecutor.Request{Model: "claude-test-model"}, coreexecutor.Options{}); err == nil || inner.calls != 1 {
		t.Fatalf("a 500 must be returned once, untouched: calls=%d err=%v", inner.calls, err)
	}
}

// The log line for a non-Anthropic body carries the evidence that was missing on 2026-10-10
// (every line read "type= message="): content type, request-id, cf-ray, server, body size and a
// bounded printable prefix of the body. An Anthropic error body keeps the short line.
func TestUpstreamErrorLogCarriesEvidenceForNonAnthropicBodies(t *testing.T) {
	bodiless := backendUpstreamRecord(&bodilessUpstreamErr{status: 404, headers: http.Header{"Content-Type": {"text/html"}, "Request-Id": {"req_1"}, "Cf-Ray": {"ray-1"}, "Server": {"cloudflare"}}, body: []byte("<html>\n\tnot\x00 found</html>" + strings.Repeat("x", 300))})
	fields := backendUpstreamLogFields("auth-1", "claude-test-model", bodiless)
	got := map[string]any{}
	for i := 0; i+1 < len(fields); i += 2 {
		got[fields[i].(string)] = fields[i+1]
	}
	if got["type"] != "" || got["anthropic_error"] != false || got["content_type"] != "text/html" || got["request_id"] != "req_1" || got["cf_ray"] != "ray-1" || got["server"] != "cloudflare" {
		t.Fatalf("evidence fields wrong: %v", got)
	}
	body := got["body"].(string)
	if len(body) > 120 || !strings.HasPrefix(body, "<html>  not found</html>xxx") || strings.ContainsRune(body, 0) {
		t.Fatalf("body prefix must be printable and bounded: %q", body)
	}
	anthropic := backendUpstreamRecord(anthropic404().(*bodilessUpstreamErr))
	fields = backendUpstreamLogFields("auth-1", "claude-test-model", anthropic)
	if len(fields) != 10 || fields[7] != "not_found_error" {
		t.Fatalf("an Anthropic body keeps the short line: %v", fields)
	}
}

// The usage-poll warning says what failed, without a URL.
func TestUsagePollWarningSaysWhatFailed(t *testing.T) {
	got := backendUsagePollWhy(errors.New("fetch Claude quota: Get \"https://api.example.com/api/oauth/usage\": context deadline exceeded"), false)
	if strings.Contains(got, "https://") || !strings.Contains(got, "context deadline exceeded") {
		t.Fatalf("want the error without its URL: %q", got)
	}
	if got := backendUsagePollWhy(nil, false); got != "usage response carried no weekly figure" {
		t.Fatalf("no figure: %q", got)
	}
	if got := backendUsagePollWhy(errors.New(strings.Repeat("e", 400)), true); len(got) > 200 {
		t.Fatalf("not bounded: %d", len(got))
	}
}
