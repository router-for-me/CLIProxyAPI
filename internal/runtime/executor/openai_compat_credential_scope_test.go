package executor

import (
	"net/http"
	"testing"
	"time"
)

// TestNewOpenAICompatStatusErrorClassifiesAccountUsageWindow pins the classification at
// the executor, where statusErr advertises IsCredentialScoped(): an account-wide usage
// window must not be reported as a model-scoped failure, otherwise the cooldown only
// covers the requested model and session affinity keeps pinning a workload to a key
// whose window is already exhausted.
func TestNewOpenAICompatStatusErrorClassifiesAccountUsageWindow(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "opencode-go usage limit type",
			body: `{"type":"error","error":{"type":"GoUsageLimitError","message":"Go usage limit exceeded"}}`,
			want: true,
		},
		{
			name: "openai account quota",
			body: `{"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`,
			want: true,
		},
		{
			name: "codex style account window",
			body: `{"error":{"type":"usage_limit_reached","message":"You've hit your usage limit."}}`,
			want: true,
		},
		{
			name: "model scoped quota",
			body: `{"error":{"message":"quota exceeded for model foo"}}`,
			want: false,
		},
		{
			name: "per minute token limit",
			body: `{"error":{"message":"Rate limit reached on tokens per min (TPM): Limit 200000, Used 200000"}}`,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := newOpenAICompatStatusError(http.StatusTooManyRequests, http.Header{}, []byte(tc.body))
			if got := err.IsCredentialScoped(); got != tc.want {
				t.Fatalf("IsCredentialScoped() = %v, want %v", got, tc.want)
			}
		})
	}

	if got := newOpenAICompatStatusError(http.StatusBadRequest, http.Header{}, []byte(`{"error":{"type":"usage_limit_reached"}}`)).IsCredentialScoped(); got {
		t.Fatal("a non-429 status must stay model scoped")
	}
}

// TestOpenAICompatStreamDataErrorClassifiesAccountUsageWindow covers the streaming
// path, which builds its own statusErr from the SSE error payload.
func TestOpenAICompatStreamDataErrorClassifiesAccountUsageWindow(t *testing.T) {
	t.Parallel()

	accountWindow, ok := openAICompatStreamDataError(
		[]byte(`{"status":429,"error":{"type":"GoUsageLimitError","message":"Go usage limit exceeded"}}`), "")
	if !ok {
		t.Fatal("expected an error for the account usage window payload")
	}
	if !accountWindow.IsCredentialScoped() {
		t.Fatalf("stream account usage window must be credential scoped: %+v", accountWindow)
	}

	perMinute, ok := openAICompatStreamDataError(
		[]byte(`{"status":429,"error":{"message":"Rate limit reached on tokens per min (TPM)"}}`), "")
	if !ok {
		t.Fatal("expected an error for the per-minute payload")
	}
	if perMinute.IsCredentialScoped() {
		t.Fatalf("per-minute limit must stay model scoped: %+v", perMinute)
	}
}

// TestOpenAICompatStreamDataErrorInfersStatusForUsageWindow covers the status-less SSE
// shape emitted by BuildOpenAIResponsesStreamErrorChunk: without the inference the
// payload is normalized to 502, stays model scoped, and the spent credential keeps being
// selected for its other models.
func TestOpenAICompatStreamDataErrorInfersStatusForUsageWindow(t *testing.T) {
	t.Parallel()

	err, ok := openAICompatStreamDataError(
		[]byte(`{"type":"error","error":{"type":"usage_limit_reached","message":"You've hit your usage limit.","resets_in_seconds":300}}`), "error")
	if !ok {
		t.Fatal("expected an error for the status-less usage window payload")
	}
	if err.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("StatusCode() = %d, want 429", err.StatusCode())
	}
	if !err.IsCredentialScoped() {
		t.Fatalf("status-less usage window must be credential scoped: %+v", err)
	}
	if err.RetryAfter() == nil || *err.RetryAfter() != 300*time.Second {
		t.Fatalf("RetryAfter() = %v, want 300s from resets_in_seconds", err.RetryAfter())
	}

	perMinute, ok := openAICompatStreamDataError(
		[]byte(`{"type":"error","error":{"message":"Rate limit reached on tokens per min (TPM)"}}`), "error")
	if !ok {
		t.Fatal("expected an error for the status-less per-minute payload")
	}
	if perMinute.StatusCode() != http.StatusBadGateway || perMinute.IsCredentialScoped() {
		t.Fatalf("status-less per-minute limit must stay a model-scoped 502: %+v", perMinute)
	}
}

// TestNewOpenAICompatStatusErrorHonorsUsageWindowReset checks that a recognized account
// window uses the reset advertised by the body instead of the one-second initial ramp.
func TestNewOpenAICompatStatusErrorHonorsUsageWindowReset(t *testing.T) {
	t.Parallel()

	body := []byte(`{"error":{"type":"usage_limit_reached","message":"You've hit your usage limit.","resets_in_seconds":3600}}`)
	err := newOpenAICompatStatusError(http.StatusTooManyRequests, http.Header{}, body)
	if !err.IsCredentialScoped() {
		t.Fatalf("usage window must be credential scoped: %+v", err)
	}
	if err.RetryAfter() == nil || *err.RetryAfter() != time.Hour {
		t.Fatalf("RetryAfter() = %v, want 1h from resets_in_seconds", err.RetryAfter())
	}

	// An explicit Retry-After header still wins.
	headers := http.Header{"Retry-After": []string{"120"}}
	withHeader := newOpenAICompatStatusError(http.StatusTooManyRequests, headers, body)
	if withHeader.RetryAfter() == nil || *withHeader.RetryAfter() != 120*time.Second {
		t.Fatalf("RetryAfter() = %v, want the Retry-After header value", withHeader.RetryAfter())
	}
}
