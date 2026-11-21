package pluginhost

import (
	"context"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type continuityFunc func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, error)

func (f continuityFunc) Continuity(ctx context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, error) {
	return f(ctx, req)
}

func TestRPCCapabilitiesIncludeSchedulerContinuity(t *testing.T) {
	plugin := pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				return pluginapi.SchedulerPickResponse{}, nil
			}),
			SchedulerContinuity: continuityFunc(func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, error) {
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityReady}, nil
			}),
		},
	}

	caps := rpcCapabilitiesFromPlugin(plugin)
	if !caps.SchedulerContinuity {
		t.Fatal("SchedulerContinuity = false, want true")
	}
	raw, errMarshal := json.Marshal(caps)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if decoded["scheduler_continuity"] != true {
		t.Fatalf("scheduler_continuity = %#v, want true", decoded["scheduler_continuity"])
	}
}

func TestRPCSchedulerContinuityRoundTrip(t *testing.T) {
	var gotReq pluginapi.SchedulerContinuityRequest
	plugin := validTestPlugin("sched-continuity")
	plugin.Capabilities.Scheduler = schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
		return pluginapi.SchedulerPickResponse{Handled: false}, nil
	})
	plugin.Capabilities.SchedulerContinuity = continuityFunc(func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, error) {
		gotReq = req
		return pluginapi.SchedulerContinuityResponse{
			Decision:       pluginapi.SchedulerContinuityWait,
			PollIntervalMs: 250,
			WaitBudgetMs:   2000,
			Reason:         "waiting_quota",
		}, nil
	})

	lookup := newTestSymbolLookup(&testPlugin{registerResult: plugin})
	registered, errReg := registerRPCPlugin(context.Background(), nil, "sched-continuity", lookup, pluginabi.MethodPluginRegister, nil)
	if errReg != nil {
		t.Fatalf("registerRPCPlugin() error = %v", errReg)
	}
	if registered.Capabilities.SchedulerContinuity == nil {
		t.Fatal("registered SchedulerContinuity = nil, want non-nil")
	}

	host := newHostWithRecords(capabilityRecord{
		id:       "sched-continuity",
		priority: 10,
		meta:     plugin.Metadata,
		plugin:   registered,
	})
	if !host.HasSchedulerContinuity() {
		t.Fatal("HasSchedulerContinuity() = false, want true")
	}

	resp, handled, errEval := host.EvaluateSchedulerContinuity(context.Background(), pluginapi.SchedulerContinuityRequest{
		RequestID: "req-123",
		TraceID:   "trace-abc",
		Provider:  "codex",
		Model:     "gpt-5-codex",
		Phase:     "pre_admission",
		Candidates: []pluginapi.SchedulerContinuityCandidate{
			{ID: "auth-1", Provider: "codex", InCooldown: true},
		},
	})
	if errEval != nil {
		t.Fatalf("EvaluateSchedulerContinuity() error = %v", errEval)
	}
	if !handled {
		t.Fatal("EvaluateSchedulerContinuity() handled = false, want true")
	}
	if resp.Decision != pluginapi.SchedulerContinuityWait || resp.PollIntervalMs != 250 || resp.WaitBudgetMs != 2000 {
		t.Fatalf("resp = %#v, want wait 250ms/2000ms", resp)
	}
	if gotReq.RequestID != "req-123" || gotReq.TraceID != "trace-abc" {
		t.Fatalf("gotReq identity = (%q, %q), want (req-123, trace-abc)", gotReq.RequestID, gotReq.TraceID)
	}
}

func TestHostHasSchedulerContinuityFollowsActiveSchedulerOnly(t *testing.T) {
	host := newHostWithRecords(
		capabilityRecord{
			id:       "low-with-continuity",
			priority: 1,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					return pluginapi.SchedulerPickResponse{}, nil
				}),
				SchedulerContinuity: continuityFunc(func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, error) {
					return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityWait}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "high-without-continuity",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					return pluginapi.SchedulerPickResponse{}, nil
				}),
			}},
		},
	)
	if host.HasSchedulerContinuity() {
		t.Fatal("HasSchedulerContinuity() = true when active higher-priority scheduler lacks capability, want false")
	}
}

func TestHostEvaluateSchedulerContinuityPanicFusesPlugin(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "panic-continuity",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				return pluginapi.SchedulerPickResponse{}, nil
			}),
			SchedulerContinuity: continuityFunc(func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, error) {
				panic("boom")
			}),
		}},
	})

	_, handled, errEval := host.EvaluateSchedulerContinuity(context.Background(), pluginapi.SchedulerContinuityRequest{Provider: "codex"})
	if handled {
		t.Fatal("handled = true after panic, want false")
	}
	if errEval != nil {
		t.Fatalf("error = %v, want nil", errEval)
	}
	if !host.isPluginFused("panic-continuity") {
		t.Fatal("plugin was not fused after continuity panic")
	}
}
