package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const testGoUsageLimitBody = `{"type":"error","error":{"type":"GoUsageLimitError","message":"Go usage limit exceeded"}}`

// usageLimitProbeError mimics an executor or native plugin failure: it carries an
// HTTP status without opting into the IsCredentialScoped interface, exactly like the
// opencode-go plugin and the OpenAI-compatible executor.
type usageLimitProbeError struct {
	code int
	msg  string
}

func (e usageLimitProbeError) Error() string   { return e.msg }
func (e usageLimitProbeError) StatusCode() int { return e.code }

// modelScopedProbeError opts out explicitly, the way Codex does when model-level
// cooling is enabled: the classification from the executor must win.
type modelScopedProbeError struct {
	code int
	msg  string
}

func (e modelScopedProbeError) Error() string            { return e.msg }
func (e modelScopedProbeError) StatusCode() int          { return e.code }
func (e modelScopedProbeError) IsCredentialScoped() bool { return false }

// codedProbeError mimics a plugin ABI failure: the machine code travels outside the
// human message, exactly like rpcError.Code in the plugin envelope.
type codedProbeError struct {
	code    int
	machine string
	msg     string
}

func (e codedProbeError) Error() string     { return e.msg }
func (e codedProbeError) StatusCode() int   { return e.code }
func (e codedProbeError) ErrorCode() string { return e.machine }

func TestIsCredentialScopedErrorClassifiesUsageWindow(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "opencode-go usage limit body",
			err:  usageLimitProbeError{code: http.StatusTooManyRequests, msg: testGoUsageLimitBody},
			want: true,
		},
		{
			name: "codex style usage_limit_reached",
			err:  usageLimitProbeError{code: http.StatusTooManyRequests, msg: `{"error":{"type":"usage_limit_reached","message":"You've hit your usage limit."}}`},
			want: true,
		},
		{
			name: "openai insufficient_quota",
			err:  usageLimitProbeError{code: http.StatusTooManyRequests, msg: `{"error":{"code":"insufficient_quota","message":"You exceeded your current quota"}}`},
			want: true,
		},
		{
			name: "organization quota wording",
			err:  usageLimitProbeError{code: http.StatusTooManyRequests, msg: `{"error":{"message":"organization quota exceeded"}}`},
			want: true,
		},
		{
			name: "model scoped quota stays model scoped",
			err:  usageLimitProbeError{code: http.StatusTooManyRequests, msg: `{"error":{"message":"quota exceeded for model foo"}}`},
			want: false,
		},
		{
			name: "bare usage limit wording without account scope",
			err:  usageLimitProbeError{code: http.StatusTooManyRequests, msg: `{"error":{"message":"usage limit exceeded"}}`},
			want: false,
		},
		{
			name: "per-minute token rate limit stays model scoped",
			err:  usageLimitProbeError{code: http.StatusTooManyRequests, msg: `{"error":{"message":"Rate limit reached for gpt-5.6-luna in organization org-x on tokens per min (TPM): Limit 200000, Used 200000"}}`},
			want: false,
		},
		{
			name: "usage limit wording without 429",
			err:  usageLimitProbeError{code: http.StatusBadRequest, msg: testGoUsageLimitBody},
			want: false,
		},
		{
			name: "executor opt-out wins",
			err:  modelScopedProbeError{code: http.StatusTooManyRequests, msg: testGoUsageLimitBody},
			want: false,
		},
		{
			name: "plugin envelope code carries the account scope",
			err:  codedProbeError{code: http.StatusTooManyRequests, machine: "insufficient_quota", msg: "plan limit reached"},
			want: true,
		},
		{
			name: "plugin envelope code without account scope",
			err:  codedProbeError{code: http.StatusTooManyRequests, machine: "rate_limit_exceeded", msg: "too many requests for model foo"},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isCredentialScopedError(tc.err); got != tc.want {
				t.Fatalf("isCredentialScopedError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestSessionAffinityReleasesBoundCredentialUnderCredentialQuotaWindow covers the
// reported failure: a recurring workload (stable session identity) keeps its binding
// on a credential whose whole upstream window is exhausted, so every request of that
// workload fails even though the pool still holds credentials with quota.
func TestSessionAffinityReleasesBoundCredentialUnderCredentialQuotaWindow(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{{ID: "auth-window-a"}, {ID: "auth-window-b"}}
	opts := func() cliproxyexecutor.Options {
		return cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"quota-window-session"}}}
	}

	first, err := selector.Pick(context.Background(), "opencode-go", "opencode-go/deepseek-v4.1-flash", opts(), auths)
	if err != nil || first == nil {
		t.Fatalf("first Pick() = %v, %v", first, err)
	}

	// The bound credential reports a credential-wide usage window, but the caller still
	// offers it as a candidate. The manager hands the affinity selector a slice it has
	// already filtered (prevalidatedAuthCandidatesKey), so the selector must re-check the
	// credential-wide window itself instead of trusting the slice.
	first.Quota = QuotaState{
		Exceeded:      true,
		Reason:        "credential_quota",
		NextRecoverAt: time.Now().Add(time.Hour),
	}

	prevalidated := context.WithValue(context.Background(), prevalidatedAuthCandidatesKey{}, true)
	second, err := selector.Pick(prevalidated, "opencode-go", "opencode-go/deepseek-v4.1-flash", opts(), auths)
	if err != nil || second == nil {
		t.Fatalf("second Pick() = %v, %v", second, err)
	}
	if second.ID == first.ID {
		t.Fatalf("session affinity re-pinned the session to %q while its credential quota window was live", second.ID)
	}
}

// TestManagerCredentialUsageWindowBlocksEveryModel ties the body classification to the
// cooldown it produces: an account-wide usage window must block the credential for
// models that never failed, otherwise a per-model state keeps it selectable.
func TestManagerCredentialUsageWindowBlocksEveryModel(t *testing.T) {
	withQuotaCooldownEnabled(t)

	ctx := context.Background()
	const provider, modelA, modelB = "usage-window-provider", "usage-window-model-a", "usage-window-model-b"

	selector := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer selector.Stop()
	manager := NewManager(nil, selector, nil)

	limited := &Auth{ID: "usage-window-limited", Provider: provider, Status: StatusActive}
	healthy := &Auth{ID: "usage-window-healthy", Provider: provider, Status: StatusActive}
	for _, auth := range []*Auth{limited, healthy} {
		if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
			t.Fatalf("Register(%s): %v", auth.ID, errRegister)
		}
	}

	// Same conversion the request pipeline performs for a failed upstream call.
	execErr := usageLimitProbeError{code: http.StatusTooManyRequests, msg: testGoUsageLimitBody}
	result := Result{
		AuthID:   limited.ID,
		Provider: provider,
		Model:    modelA,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: testGoUsageLimitBody},
	}
	if isCredentialScopedError(execErr) {
		result.CredentialScope = true
	}
	manager.MarkResult(ctx, result)

	stored, ok := manager.GetByID(limited.ID)
	if !ok || stored == nil {
		t.Fatalf("missing auth %q after MarkResult", limited.ID)
	}
	if !stored.Quota.Exceeded || stored.Quota.Reason != "credential_quota" {
		t.Fatalf("credential-wide window not recorded: quota=%+v", stored.Quota)
	}
	if !stored.Unavailable {
		t.Fatalf("credential under an account-wide usage window must be unavailable, got %+v", stored)
	}
	if blocked, _, _ := isAuthBlockedForModel(stored, modelB, time.Now()); !blocked {
		t.Fatalf("credential under a live usage window is still selectable for %q, which it never served", modelB)
	}
}
