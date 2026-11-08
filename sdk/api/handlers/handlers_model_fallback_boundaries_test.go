package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestModelFallbackAfterAuthTerminationIsFinal(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "execute", true: "stream"}[stream], func(t *testing.T) {
			f := newFallbackFixture(t, func(string) fallbackOutcome { return fallbackQuota() }, func(string) fallbackOutcome { return fallbackOK("wrong") }, enabledFallback(map[string][]string{"PRIMARY": {"SECOND"}}))
			f.handler.SetPluginHost(&handlerInterceptorTestHost{interceptRequestAfterAuth: func(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
				return pluginapi.RequestInterceptResponse{Terminate: true, StatusCode: 429, ResponseBody: []byte("plugin rejection")}
			}})
			if stream {
				data, _, errs := f.handler.ExecuteStreamWithAuthManager(context.Background(), "claude", f.primary, []byte(`{}`), "")
				_, msg := readFallbackStream(t, data, errs)
				if msg == nil || !msg.DirectResponse || msg.StatusCode != 429 {
					t.Fatalf("error = %+v", msg)
				}
			} else {
				_, _, msg := f.handler.ExecuteWithAuthManager(context.Background(), "claude", f.primary, []byte(`{}`), "")
				if msg == nil || !msg.DirectResponse || msg.StatusCode != 429 {
					t.Fatalf("error = %+v", msg)
				}
			}
			if len(f.codex.Calls()) != 0 || len(f.claude.Calls()) != 0 {
				t.Fatal("interceptor rejection reached an upstream")
			}
		})
	}
}

func TestModelFallbackNeverUsesExcludedStatuses(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 413, 422} {
		conditions := internalconfig.ModelFallbackConfig{On: []string{"quota_exhausted", "auth_unavailable", "5xx"}}.Conditions()
		// Even a programmatic policy cannot bypass the hard exclusions.
		conditions.Statuses[status] = true
		if reason, ok := modelFallbackReason(context.Background(), conditions, &coreauth.Error{HTTPStatus: status, Message: "RESOURCE_EXHAUSTED quota usage_limit_reached"}); ok {
			t.Fatalf("status %d allowed by %s", status, reason)
		}
	}
}

func TestModelFallbackCanceledTargetWinsOverPrimary(t *testing.T) {
	f := newFallbackFixture(t, func(string) fallbackOutcome { return fallbackQuota() }, func(string) fallbackOutcome { return fallbackOK("unused") }, enabledFallback(map[string][]string{"PRIMARY": {"SECOND", "THIRD"}}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	_, failure := f.handler.runModelFallback(ctx, f.primary, modelExecutionOptions{}, func(string) *modelAttemptFailure {
		calls++
		err := fallbackQuota().err
		if calls == 2 {
			cancel()
			err = context.Canceled
		}
		return upstreamModelAttemptFailure(executionErrorMessage(err), err)
	})
	if calls != 2 || failure == nil || !errors.Is(failure.msg.Error, context.Canceled) {
		t.Fatalf("calls=%d failure=%+v", calls, failure)
	}
}

func TestModelFallbackBootstrapsBeforeKeepalive(t *testing.T) {
	f := newFallbackFixture(t, func(string) fallbackOutcome { return fallbackQuota() }, func(string) fallbackOutcome { return fallbackOK("ok") }, enabledFallback(map[string][]string{"PRIMARY": {"SECOND"}}))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	ticks := make(chan time.Time, 1)
	ticks <- time.Now()
	heartbeatWritten := false
	data, _, errs, ok := f.handler.ExecuteStreamWithPreFTKeepAlive(c, context.Background(), func(error) {}, "claude", f.primary, []byte(`{}`), "", ticks, func() bool { heartbeatWritten = true; return true })
	body, msg := readFallbackStream(t, data, errs)
	if !ok || msg != nil || body != "ok" || heartbeatWritten {
		t.Fatalf("ok=%v error=%+v body=%q heartbeat=%v", ok, msg, body, heartbeatWritten)
	}
	assertCodexExhausted(t, f)
}

func TestModelFallbackStreamPinsEmptyPolicyAcrossReload(t *testing.T) {
	f := newFallbackFixture(t, func(string) fallbackOutcome { return fallbackQuota() }, func(string) fallbackOutcome { return fallbackOK("wrong") }, enabledFallback(map[string][]string{"PRIMARY": {"SECOND"}}))
	// The HTTP consumer started while fallback was disabled; it may already have
	// sent a heartbeat when a reload enables fallback for new requests.
	ctx := context.WithValue(context.Background(), streamFallbackPlanKey{}, streamFallbackPlan{model: f.primary})
	data, _, errs := f.handler.ExecuteStreamWithAuthManager(ctx, "claude", f.primary, []byte(`{}`), "")
	_, msg := readFallbackStream(t, data, errs)
	if msg == nil || msg.StatusCode != 429 || len(f.claude.Calls()) != 0 {
		t.Fatalf("error=%+v calls=%v", msg, f.claude.Calls())
	}
}

func TestModelFallbackDoesNotSkipMissingTargetWithoutCondition(t *testing.T) {
	cfg := enabledFallback(map[string][]string{"PRIMARY": {"missing-model", "SECOND"}})
	cfg.On = []string{"429"}
	f := newFallbackFixture(t, func(string) fallbackOutcome { return fallbackQuota() }, func(string) fallbackOutcome { return fallbackOK("wrong") }, cfg)
	_, _, msg := f.handler.ExecuteWithAuthManager(context.Background(), "claude", f.primary, []byte(`{}`), "")
	if msg == nil || msg.StatusCode != 429 || len(f.claude.Calls()) != 0 {
		t.Fatalf("error=%+v calls=%v", msg, f.claude.Calls())
	}
}

func TestModelFallbackRouterResolvesEachTargetAndHeaderUsesExecutedModel(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "execute", true: "stream"}[stream], func(t *testing.T) {
			f := newFallbackFixture(t, func(string) fallbackOutcome { return fallbackQuota() }, func(model string) fallbackOutcome { return fallbackOK(model) }, enabledFallback(map[string][]string{"PRIMARY": {"SECOND"}}))
			var routed []string
			f.handler.SetModelRouterHost(&handlerModelRouterTestHost{hasRouters: true, route: func(_ context.Context, req pluginapi.ModelRouteRequest, _ string) (pluginapi.ModelRouteResponse, bool) {
				routed = append(routed, req.RequestedModel)
				if req.RequestedModel == f.second {
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetProvider, Target: "claude", TargetModel: f.third}, true
				}
				return pluginapi.ModelRouteResponse{}, false
			}})
			var body string
			var headers http.Header
			if stream {
				data, hdrs, errs := f.handler.ExecuteStreamWithAuthManager(context.Background(), "claude", f.primary, []byte(`{}`), "")
				payload, msg := readFallbackStream(t, data, errs)
				if msg != nil {
					t.Fatal(msg)
				}
				body, headers = payload, hdrs
			} else {
				payload, hdrs, msg := f.handler.ExecuteWithAuthManager(context.Background(), "claude", f.primary, []byte(`{}`), "")
				if msg != nil {
					t.Fatal(msg)
				}
				body, headers = string(payload), hdrs
			}
			if len(routed) != 2 || routed[0] != f.primary || routed[1] != f.second {
				t.Fatalf("router models=%v", routed)
			}
			if body != f.third || headers.Get(ModelFallbackHeader) != f.third {
				t.Fatalf("body=%q header=%q", body, headers.Get(ModelFallbackHeader))
			}
			if got := f.claude.Calls(); len(got) != 1 || got[0].requestedModel != f.primary {
				t.Fatalf("calls=%+v", got)
			}
		})
	}
}
