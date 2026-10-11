package executor

import (
	"net/http"
	"testing"
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
