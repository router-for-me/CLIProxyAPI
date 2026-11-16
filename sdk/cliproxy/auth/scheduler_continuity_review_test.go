package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestContinuityDeclinedBootstrapNeverRedispatches(t *testing.T) {
	m := NewManager(nil, nil, nil)
	ex := &continuity429Executor{status: 429}
	m.RegisterExecutor(ex)
	if _, err := m.Register(context.Background(), &Auth{ID: "declined", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	original := &Error{HTTPStatus: 429, Message: "original upstream error"}
	ctx := WithContinuityRejected429(context.Background(), original)
	_, err := m.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{Stream: true})
	if !errors.Is(err, original) || ex.calls != 0 {
		t.Fatalf("declined bootstrap called upstream or lost original error: calls=%d err=%v", ex.calls, err)
	}
}

func TestContinuityLocal429NeverEntersConfirmedRecovery(t *testing.T) {
	for _, stream := range []bool{false, true} {
		m := NewManager(nil, nil, nil)
		ex := &continuity429Executor{status: 429, local: true}
		m.RegisterExecutor(ex)
		if _, err := m.Register(context.Background(), &Auth{ID: "local", Provider: "codex"}); err != nil {
			t.Fatal(err)
		}
		rejected := 0
		m.SetPluginScheduler(&continuityTestScheduler{decide: func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
			if req.PreviousAttemptRejected429 {
				rejected++
			}
			return pluginapi.SchedulerContinuityResponse{Action: "ready"}, true, nil
		}})
		var err error
		if stream {
			_, err = m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{Stream: true})
		} else {
			_, err = m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{})
		}
		if err == nil || ex.calls != 1 || rejected != 0 {
			t.Fatalf("local 429 replayed: stream=%v calls=%d rejected=%d err=%v", stream, ex.calls, rejected, err)
		}
	}
}

func TestContinuityCandidatesUseOuterCredentialForNativeCodex(t *testing.T) {
	now := time.Now()
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(schedulerTestExecutor{provider: "codex"})
	a := &Auth{ID: "outer", Index: "outer-index", Provider: "codex", Status: StatusError,
		Unavailable: true, RequiresSignIn: true, RejectedSIWCRefreshToken: "siwc-refresh",
		LastError: &Error{Code: "refresh_outcome_unknown"}, Metadata: map[string]any{
			"access_token": "outer-token", "expired": now.Add(time.Hour).Format(time.RFC3339), "use_siwc": true,
			"siwc": map[string]any{"access_token": "siwc-token", "refresh_token": "siwc-refresh", "expires_at": now.Add(-time.Hour).Format(time.RFC3339)},
		}}
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	m.SetPluginScheduler(&continuityTestScheduler{decide: func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
		if len(req.Pick.Candidates) != 1 || req.Pick.Candidates[0].ID != a.ID {
			t.Fatalf("healthy outer token missing: %+v", req.Pick.Candidates)
		}
		return pluginapi.SchedulerContinuityResponse{Action: "ready"}, true, nil
	}})
	opts := cliproxyexecutor.Options{Headers: http.Header{"User-Agent": {"codex_cli_rs/1.0.0"}}}
	_, handled, err := m.schedulerContinuityDecision(context.Background(), []string{"codex"}, "", opts, false)
	if err != nil || !handled {
		t.Fatalf("native credential filtered using SIWC state: handled=%v err=%v", handled, err)
	}
}
