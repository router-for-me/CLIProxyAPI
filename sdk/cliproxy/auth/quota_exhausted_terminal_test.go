package auth

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// quotaTerminalTestError stands in for the upstream 429 an executor returns on
// the last attempt of a request: exhausted mirrors a structured code such as
// usage_limit_reached, hint the upstream reset hint.
type quotaTerminalTestError struct {
	exhausted        bool
	credentialScoped bool
	hint             time.Duration
}

func (e quotaTerminalTestError) Error() string {
	return `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`
}
func (e quotaTerminalTestError) StatusCode() int          { return http.StatusTooManyRequests }
func (e quotaTerminalTestError) IsQuotaExhausted() bool   { return e.exhausted }
func (e quotaTerminalTestError) IsCredentialScoped() bool { return e.credentialScoped }
func (e quotaTerminalTestError) RetryAfter() *time.Duration {
	hint := e.hint
	return &hint
}

// The request whose attempts exhaust the last credentials ends with the
// upstream 429 wrapped in the pool cooldown terminal error. It must carry the
// same retry guidance as the selection-time cooldown the next request gets:
// a final usage limit when every candidate is explicitly exhausted for long,
// otherwise Retry-After.
func TestPoolCooldownTerminalErrorFollowsQuotaExhaustionEvidence(t *testing.T) {
	tests := []struct {
		name    string
		members []quotaTerminalTestError
		want    bool
		// wantRetryAfter is the expected Retry-After in seconds when want is false.
		wantRetryAfter time.Duration
	}{
		{
			name:    "every credential explicitly exhausted",
			members: []quotaTerminalTestError{{exhausted: true, hint: 3 * time.Hour}, {exhausted: true, hint: 4 * time.Hour}},
			want:    true,
		},
		{
			name:    "every credential exhausted for the whole credential",
			members: []quotaTerminalTestError{{exhausted: true, credentialScoped: true, hint: 3 * time.Hour}, {exhausted: true, credentialScoped: true, hint: 4 * time.Hour}},
			want:    true,
		},
		{
			name:           "mixed exhausted and unclassified",
			members:        []quotaTerminalTestError{{exhausted: true, hint: 3 * time.Hour}, {hint: 2 * time.Hour}},
			wantRetryAfter: 2 * time.Hour,
		},
		{
			name:           "unclassified long rate limits",
			members:        []quotaTerminalTestError{{hint: 3 * time.Hour}, {hint: 4 * time.Hour}},
			wantRetryAfter: 3 * time.Hour,
		},
		{
			name:           "short exhaustion",
			members:        []quotaTerminalTestError{{exhausted: true, hint: time.Minute}, {exhausted: true, hint: 2 * time.Minute}},
			wantRetryAfter: time.Minute,
		},
	}
	for _, stream := range []bool{false, true} {
		for _, test := range tests {
			t.Run(fmt.Sprintf("%s stream=%v", test.name, stream), func(t *testing.T) {
				prev := quotaCooldownDisabled.Load()
				quotaCooldownDisabled.Store(false)
				t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

				const model = "quota-terminal-model"
				manager := NewManager(nil, nil, nil)
				executor := &authFallbackExecutor{
					id:                "codex",
					executeErrors:     map[string]error{},
					streamFirstErrors: map[string]error{},
				}
				manager.RegisterExecutor(executor)
				reg := registry.GetGlobalRegistry()
				for _, member := range test.members {
					authID := "quota-terminal-" + uuid.NewString()
					executor.executeErrors[authID] = member
					executor.streamFirstErrors[authID] = member
					reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { reg.UnregisterClient(authID) })
					if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: "codex", Status: StatusActive}); errRegister != nil {
						t.Fatalf("Register() error = %v", errRegister)
					}
				}

				var errTerminal error
				if stream {
					result, errStream := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
					errTerminal = errStream
					if errStream == nil && result != nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								errTerminal = chunk.Err
							}
						}
					}
				} else {
					_, errTerminal = manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				}
				if errTerminal == nil {
					t.Fatal("request succeeded on an exhausted pool")
				}
				if got := statusCodeFromError(errTerminal); got != http.StatusTooManyRequests {
					t.Fatalf("status = %d, want 429", got)
				}
				if !HasTrustedRetryDeadline(errTerminal) {
					t.Fatalf("terminal error %T lost its trusted deadline; a raw upstream Retry-After could pass through", errTerminal)
				}
				headers := SafeResponseHeaders(errTerminal)
				if test.want {
					if !IsUsageLimitReachedError(errTerminal) || headers.Get("Retry-After") != "" || headers.Get("X-Should-Retry") != "false" {
						t.Fatalf("terminal %T: want a final usage limit without Retry-After; usage limit=%v headers=%v", errTerminal, IsUsageLimitReachedError(errTerminal), headers)
					}
				} else {
					seconds, errParse := strconv.Atoi(headers.Get("Retry-After"))
					want := int(test.wantRetryAfter.Seconds())
					if IsUsageLimitReachedError(errTerminal) || headers.Get("X-Should-Retry") != "" || errParse != nil || seconds < want-5 || seconds > want {
						t.Fatalf("terminal %T: want a retryable cooldown with Retry-After ~%d; usage limit=%v headers=%v", errTerminal, want, IsUsageLimitReachedError(errTerminal), headers)
					}
				}

				// The next request is answered from selection and must agree.
				_, errNext := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				assertPoolUsageLimit(t, test.name+" next request", errNext, test.want)
			})
		}
	}
}
