package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type streamQuotaError struct {
	customStatusError
	credentialScoped bool
}

func (e streamQuotaError) IsCredentialScoped() bool { return e.credentialScoped }

func TestExecuteStreamQuotaFailurePreservesCooldownAndScope(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, credentialScoped := range []bool{true, false} {
		for _, weighted := range []bool{false, true} {
			name := fmt.Sprintf("credential_scope=%t/weighted=%t", credentialScoped, weighted)
			t.Run(name, func(t *testing.T) {
				var selector Selector = &RoundRobinSelector{}
				if weighted {
					selector = &WeightedRoundRobinSelector{}
				}
				manager := NewManager(nil, selector, nil)
				manager.SetRetryConfig(3, 30*time.Second, 0)
				model := "stream-quota-model"
				siblingModel := "stream-quota-sibling"
				highID := "stream-quota-high-" + name
				lowID := "stream-quota-low-" + name
				for _, candidate := range []*Auth{
					{ID: highID, Provider: "codex", Status: StatusActive, Attributes: map[string]string{"priority": "4", AttributeWeight: "1"}},
					{ID: lowID, Provider: "codex", Status: StatusActive, Attributes: map[string]string{"priority": "3", AttributeWeight: "1"}},
				} {
					registry.GetGlobalRegistry().RegisterClient(candidate.ID, "codex", []*registry.ModelInfo{{ID: model}, {ID: siblingModel}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(candidate.ID) })
					if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
						t.Fatal(errRegister)
					}
				}

				retryAfter := time.Hour
				quotaErr := streamQuotaError{
					customStatusError: customStatusError{
						code: http.StatusTooManyRequests, msg: `{"error":{"type":"usage_limit_reached","resets_in_seconds":3600}}`, retryAfter: &retryAfter,
					},
					credentialScoped: credentialScoped,
				}
				if !credentialScoped {
					quotaErr.msg = `{"error":{"type":"rate_limit_error","message":"Model rate limit exceeded"}}`
				}
				var attempts []string
				manager.RegisterExecutor(&customStreamMockExecutor{
					identifier: "codex",
					streamFn: func(_ context.Context, selected *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
						attempts = append(attempts, selected.ID)
						chunks := make(chan cliproxyexecutor.StreamChunk, 2)
						chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.created\"}\n\n")}
						chunks <- cliproxyexecutor.StreamChunk{Err: quotaErr}
						close(chunks)
						return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
					},
				})
				before := time.Now()
				result, errStream := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
				if errStream != nil {
					t.Fatalf("ExecuteStream() error = %v", errStream)
				}
				var payloads, failures int
				for chunk := range result.Chunks {
					if len(chunk.Payload) != 0 {
						payloads++
					}
					if chunk.Err != nil {
						failures++
						if chunk.Err != quotaErr {
							t.Fatalf("stream error = %v, want original quota error", chunk.Err)
						}
					}
				}
				if payloads != 1 || failures != 1 || len(attempts) != 1 || attempts[0] != highID {
					t.Fatalf("started stream must retain its payload and error without replay: payloads=%d failures=%d attempts=%v", payloads, failures, attempts)
				}
				high, _ := manager.GetByID(highID)
				state := high.ModelStates[model]
				if state == nil || state.NextRetryAfter.Before(before.Add(retryAfter)) {
					t.Errorf("model cooldown lost upstream RetryAfter: state=%+v", state)
				}
				if credentialScoped && (high.Quota.Reason != "credential_quota" || high.Quota.NextRecoverAt.Before(before.Add(retryAfter))) {
					t.Errorf("credential cooldown lost scope or RetryAfter: quota=%+v", high.Quota)
				}
				for _, requestedModel := range []string{model, siblingModel} {
					wantID := lowID
					if requestedModel == siblingModel && !credentialScoped {
						wantID = highID
					}
					selected, _, _, errSelect := manager.pickNextMixed(context.Background(), []string{"codex"}, requestedModel, cliproxyexecutor.Options{}, nil)
					if errSelect != nil {
						t.Fatalf("pickNextMixed(%s) error = %v", requestedModel, errSelect)
					}
					if selected.ID != wantID {
						t.Errorf("pickNextMixed(%s) = %s, want %s", requestedModel, selected.ID, wantID)
					}
				}
				if blocked, _, _ := isAuthBlockedForModel(high, model, before.Add(time.Minute)); !blocked {
					t.Error("exhausted model becomes selectable before the upstream reset")
				}
			})
		}
	}
}
