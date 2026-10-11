package claudemaster

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// The person sees why claude-master refused, in claude-master's own words; an upstream error
// still gets the generic line and never its text.
func TestClientErrorNamesTheSelectorsRefusal(t *testing.T) {
	ctx := withBackendAttempt(context.Background())
	noteBackendRefusal(ctx, &coreauth.Error{Code: "auth_unavailable", Message: "no auth available"})
	noteBackendRefusal(ctx, backendRefuse(backendRefusalNotFound, "subscription alpha does not serve model claude-test-model (HTTP 404)"))
	noteBackendRefusal(ctx, backendRefuse(backendRefusalUnavailable, "a later, vaguer reason"))
	noteBackendRefusal(ctx, &coreauth.Error{Code: "auth_unavailable", Message: "no auth available"})
	for name, write := range map[string]func(context.Context, http.ResponseWriter, *interfaces.ErrorMessage){"protocol": writeBackendUpstreamError, "native": writeBackendNativeUpstreamError} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			write(ctx, w, &interfaces.ErrorMessage{StatusCode: 503, Error: errors.New("private-upstream-error")})
			body := w.Body.String()
			// A not-found refusal is a 404 not_found_error, as Anthropic's would be, so the client stops
			// instead of retrying a 503 ten times.
			if w.Code != 404 || !strings.Contains(body, `"type":"not_found_error"`) || !strings.Contains(body, "claude-master: subscription alpha does not serve model claude-test-model (HTTP 404)") {
				t.Fatalf("refusal not surfaced as Anthropic would: %d %s", w.Code, body)
			}
			if w.Header().Get("Claude-Master-Reason") == "" {
				t.Fatal("the reason header is missing")
			}
			if strings.Contains(body, "later, vaguer") || strings.Contains(body, "no auth available") || strings.Contains(body, "private-upstream-error") || strings.Contains(body, "Configured inference failed") {
				t.Fatalf("wrong text in the client error: %s", body)
			}
			plain := httptest.NewRecorder()
			write(withBackendAttempt(context.Background()), plain, &interfaces.ErrorMessage{StatusCode: 502, Error: errors.New("private-upstream-error")})
			if !strings.Contains(plain.Body.String(), "Configured inference failed") || strings.Contains(plain.Body.String(), "private-upstream-error") {
				t.Fatalf("a request with no refusal must get the generic line: %s", plain.Body.String())
			}
		})
	}
}

// The outage of 2026-10-09: a conversation bound to a subscription that answered 404 for its
// model was refused with "the current Claude subscription is unavailable", which reads like an
// outage. The refusal now names the subscription, the model, the status and the way out.
func TestBoundSubscriptionRefusalNamesModelAndStatus(t *testing.T) {
	selector := &backendSeriesSelector{authIDs: []string{"profile-a", "profile-b"}, provider: "claude", names: map[string]string{"profile-a": "alpha"}}
	t.Cleanup(selector.Stop)
	authA := backendSeriesTestAuth("profile-a", "claude")
	authB := backendSeriesTestAuth("profile-b", "claude")
	headers := http.Header{"X-Claude-Code-Session-Id": []string{"session-404"}}
	ctx := withBackendAttempt(context.Background())
	if _, err := selector.Pick(ctx, "claude", "claude-test-model", coreexecutor.Options{
		Headers: headers, OriginalRequest: []byte(`{"model":"claude-test-model","messages":[{"role":"user","content":"start"}]}`),
	}, []*coreauth.Auth{authA, authB}); err != nil {
		t.Fatal(err)
	}
	recordBackendAttempt(ctx, coreauth.Result{AuthID: "profile-a", Model: "claude-test-model", Error: &coreauth.Error{HTTPStatus: http.StatusNotFound, Message: "private-upstream-text"}})
	got, err := selector.Pick(ctx, "claude", "claude-test-model", coreexecutor.Options{
		Headers: headers, OriginalRequest: []byte(`{"model":"claude-test-model","messages":[{"role":"user","content":"start"},{"role":"assistant","content":[{"type":"text","text":"hi"}]},{"role":"user","content":"more"}]}`),
	}, []*coreauth.Auth{authB})
	if err == nil || got != nil {
		t.Fatalf("an upstream 404 must not move the conversation: got %#v", got)
	}
	msg := err.Error()
	for _, want := range []string{"subscription alpha", "claude-test-model", "404", "/model"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("refusal lacks %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "private-upstream-text") || strings.Contains(msg, "profile-a") {
		t.Fatalf("refusal leaks upstream text or the runtime id: %s", msg)
	}
	if backendRefusal(ctx) != msg {
		t.Fatalf("the refusal was not recorded for the client's error: %q", backendRefusal(ctx))
	}
}

// Every pool subscription out of weekly quota: the session gets what Anthropic sends for a weekly
// limit (429 rate_limit_error with the unified rate-limit headers and retry-after), so the client
// shows its "weekly limit, resets at" and waits, instead of a 503 it retries at once.
func TestQuotaRefusalLooksLikeAnthropicsWeeklyLimit(t *testing.T) {
	ctx := withBackendAttempt(context.Background())
	resetAt := time.Now().Add(90 * time.Hour).Truncate(time.Second)
	noteBackendRefusal(ctx, &backendRefusalError{kind: backendRefusalQuota, reason: "ordered inference subscriptions are exhausted; no fallback is configured", resetAt: resetAt})
	w := httptest.NewRecorder()
	writeBackendNativeUpstreamError(ctx, w, &interfaces.ErrorMessage{StatusCode: 503, Error: errors.New("private-upstream-error")})
	body := w.Body.String()
	if w.Code != 429 || !strings.Contains(body, `"type":"rate_limit_error"`) || !strings.Contains(body, "would exceed your account's rate limit") {
		t.Fatalf("quota refusal is not Anthropic's 429: %d %s", w.Code, body)
	}
	h := w.Header()
	if h.Get("Anthropic-Ratelimit-Unified-Status") != "rejected" || h.Get("Anthropic-Ratelimit-Unified-Representative-Claim") != "seven_day" || h.Get("Anthropic-Ratelimit-Unified-Reset") != strconv.FormatInt(resetAt.Unix(), 10) || h.Get("X-Should-Retry") != "true" {
		t.Fatalf("quota headers are not Anthropic's: %v", h)
	}
	if retry, err := strconv.Atoi(h.Get("Retry-After")); err != nil || retry < 89*3600 || retry > 91*3600 {
		t.Fatalf("retry-after does not point at the reset: %q", h.Get("Retry-After"))
	}
	if strings.Contains(body, "private-upstream-error") || strings.Contains(body, "Configured inference failed") {
		t.Fatalf("wrong text in the quota error: %s", body)
	}
}

// An upstream error recorded in this request is relayed exactly, status, body and headers, even
// though the pool refused afterwards: the client reacts to Anthropic's own answer.
func TestRecordedUpstreamErrorIsRelayedExactly(t *testing.T) {
	ctx := withBackendAttempt(context.Background())
	noteBackendUpstreamError(ctx, backendTestUpstreamError{status: 404, headers: http.Header{"Request-Id": {"req_test"}, "X-Account": {"private-account"}}, body: []byte(`{"type":"error","error":{"type":"not_found_error","message":"model: claude-test-model"}}`)})
	noteBackendRefusal(ctx, backendRefuse(backendRefusalNotFound, "subscription alpha does not serve model claude-test-model (HTTP 404)"))
	for name, write := range map[string]func(context.Context, http.ResponseWriter, *interfaces.ErrorMessage){"protocol": writeBackendUpstreamError, "native": writeBackendNativeUpstreamError} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			write(ctx, w, &interfaces.ErrorMessage{StatusCode: 503, Error: errors.New("private-upstream-error")})
			if w.Code != 404 || w.Body.String() != `{"type":"error","error":{"type":"not_found_error","message":"model: claude-test-model"}}` {
				t.Fatalf("upstream error not relayed exactly: %d %s", w.Code, w.Body.String())
			}
			if w.Header().Get("Request-Id") != "req_test" {
				t.Fatalf("request-id dropped: %v", w.Header())
			}
			if name == "protocol" && w.Header().Get("X-Account") != "" {
				t.Fatalf("protocol path leaked a private header: %v", w.Header())
			}
		})
	}
}

type backendTestUpstreamError struct {
	status  int
	headers http.Header
	body    []byte
}

func (e backendTestUpstreamError) Error() string                { return "private upstream text" }
func (e backendTestUpstreamError) StatusCode() int              { return e.status }
func (e backendTestUpstreamError) ResponseHeaders() http.Header { return e.headers.Clone() }
func (e backendTestUpstreamError) ResponseBody() []byte         { return append([]byte(nil), e.body...) }

// A failure that is not a dead login or a used-up quota is this request's: the scheduler must not
// bench the subscription for it.
func TestFailuresOtherThanLoginAndQuotaAreRequestScoped(t *testing.T) {
	for status, want := range map[int]string{404: coreauth.ErrorCodeRequestScoped, 400: coreauth.ErrorCodeRequestScoped, 529: coreauth.ErrorCodeRequestScoped, 500: coreauth.ErrorCodeRequestScoped, 401: "", 429: ""} {
		result := coreauth.Result{AuthID: "profile-a", Error: &coreauth.Error{HTTPStatus: status, Message: "private"}}
		backendScopeFailureToRequest(&result)
		if result.Error.Code != want {
			t.Fatalf("status %d: code %q, want %q", status, result.Error.Code, want)
		}
	}
	kept := coreauth.Result{AuthID: "profile-a", Error: &coreauth.Error{HTTPStatus: 404, Code: coreauth.ErrorCodeForceCooldown}}
	backendScopeFailureToRequest(&kept)
	if kept.Error.Code != coreauth.ErrorCodeForceCooldown {
		t.Fatal("an explicit code was overwritten")
	}
	ok := coreauth.Result{AuthID: "profile-a", Success: true}
	backendScopeFailureToRequest(&ok)
}
