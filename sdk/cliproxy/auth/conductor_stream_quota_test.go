package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type streamQuotaError struct {
	customStatusError
	credentialScoped bool
}

func (e streamQuotaError) IsCredentialScoped() bool { return e.credentialScoped }

type payloadBackedStreamQuotaError struct {
	streamQuotaError
}

func (payloadBackedStreamQuotaError) StreamErrorPayloadEncoded() bool { return true }

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

func TestExecuteStreamPayloadBackedQuotaErrorPreservesFrameAndRotatesNextRequest(t *testing.T) {
	withQuotaCooldownEnabled(t)
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.SetRetryConfig(0, 0, 2)
	model := "payload-backed-stream-quota"
	highID, lowID := "payload-backed-stream-quota-high", "payload-backed-stream-quota-low"
	for _, candidate := range []*Auth{
		{ID: highID, Provider: "claude", Status: StatusActive, Attributes: map[string]string{"priority": "4"}},
		{ID: lowID, Provider: "claude", Status: StatusActive, Attributes: map[string]string{"priority": "3"}},
	} {
		registry.GetGlobalRegistry().RegisterClient(candidate.ID, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(candidate.ID) })
		if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
			t.Fatal(errRegister)
		}
	}

	const startFrame = "event: message_start\ndata: {\"type\":\"message_start\"}\n\n"
	const errorFrame = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\"}}\n\n"
	const stopFrame = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	retryAfter := time.Hour
	quotaErr := payloadBackedStreamQuotaError{streamQuotaError{
		customStatusError: customStatusError{code: http.StatusTooManyRequests, msg: "subscription quota exhausted", retryAfter: &retryAfter},
		credentialScoped:  true,
	}}
	var attempts []string
	manager.RegisterExecutor(&customStreamMockExecutor{
		identifier: "claude",
		streamFn: func(_ context.Context, selected *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			attempts = append(attempts, selected.ID)
			chunks := make(chan cliproxyexecutor.StreamChunk, 2)
			if selected.ID == highID {
				chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(startFrame)}
				chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(errorFrame), Err: quotaErr}
			} else {
				chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(stopFrame)}
			}
			close(chunks)
			return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
		},
	})

	collect := func() (string, int) {
		result, errStream := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
		if errStream != nil {
			t.Fatalf("ExecuteStream() error = %v", errStream)
		}
		var body string
		var failures int
		for chunk := range result.Chunks {
			body += string(chunk.Payload)
			if chunk.Err != nil {
				failures++
			}
		}
		return body, failures
	}

	firstBody, firstFailures := collect()
	if firstBody != startFrame+errorFrame || firstFailures != 0 {
		t.Fatalf("started stream changed: body=%q failures=%d", firstBody, firstFailures)
	}
	secondBody, secondFailures := collect()
	if secondBody != stopFrame || secondFailures != 0 {
		t.Fatalf("next request did not use healthy credential: body=%q failures=%d", secondBody, secondFailures)
	}
	if got := fmt.Sprint(attempts); got != fmt.Sprint([]string{highID, lowID}) {
		t.Fatalf("attempts = %v, want no replay then next credential", attempts)
	}
}

func TestExecuteStreamPayloadBackedQuotaErrorRetriesBeforeOutput(t *testing.T) {
	withQuotaCooldownEnabled(t)
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.SetRetryConfig(0, 0, 2)
	model := "payload-backed-bootstrap-quota"
	highID, lowID := "payload-backed-bootstrap-high", "payload-backed-bootstrap-low"
	for _, candidate := range []*Auth{
		{ID: highID, Provider: "claude", Status: StatusActive, Attributes: map[string]string{"priority": "4"}},
		{ID: lowID, Provider: "claude", Status: StatusActive, Attributes: map[string]string{"priority": "3"}},
	} {
		registry.GetGlobalRegistry().RegisterClient(candidate.ID, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(candidate.ID) })
		if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
			t.Fatal(errRegister)
		}
	}

	const errorFrame = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\"}}\n\n"
	const stopFrame = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	quotaErr := payloadBackedStreamQuotaError{streamQuotaError{
		customStatusError: customStatusError{code: http.StatusTooManyRequests, msg: "subscription quota exhausted"},
		credentialScoped:  true,
	}}
	var attempts []string
	manager.RegisterExecutor(&customStreamMockExecutor{
		identifier: "claude",
		streamFn: func(_ context.Context, selected *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			attempts = append(attempts, selected.ID)
			chunks := make(chan cliproxyexecutor.StreamChunk, 1)
			if selected.ID == highID {
				chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(errorFrame), Err: quotaErr}
			} else {
				chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(stopFrame)}
			}
			close(chunks)
			return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
		},
	})

	result, errStream := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	var body string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected terminal error after failover: %v", chunk.Err)
		}
		body += string(chunk.Payload)
	}
	if body != stopFrame {
		t.Fatalf("bootstrap error leaked or healthy response changed: %q", body)
	}
	if got := fmt.Sprint(attempts); got != fmt.Sprint([]string{highID, lowID}) {
		t.Fatalf("attempts = %v, want same-request failover", attempts)
	}
}
