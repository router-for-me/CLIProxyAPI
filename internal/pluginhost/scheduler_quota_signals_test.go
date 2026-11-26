package pluginhost

import (
	"context"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func noopScheduler() schedulerFunc {
	return schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
		return pluginapi.SchedulerPickResponse{}, nil
	})
}

func TestRPCCapabilitiesSchedulerQuotaSignals(t *testing.T) {
	caps := rpcCapabilitiesFromPlugin(pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: noopScheduler()}})
	if caps.SchedulerQuotaSignals {
		t.Fatal("SchedulerQuotaSignals = true by default, want false")
	}
	// Read the RPC wire form of the capabilities with ojson.
	raw, errMarshal := json.Marshal(caps)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	if field := ojson.GetBytes(raw, "scheduler_quota_signals"); field.Exists() {
		t.Fatalf("scheduler_quota_signals = %s, want omitted", field.Raw)
	}

	caps = rpcCapabilitiesFromPlugin(pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
		Scheduler:             noopScheduler(),
		SchedulerQuotaSignals: true,
	}})
	if !caps.SchedulerQuotaSignals {
		t.Fatal("SchedulerQuotaSignals = false, want true")
	}
	raw, errMarshal = json.Marshal(caps)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	if field := ojson.GetBytes(raw, "scheduler_quota_signals"); field.Raw != "true" {
		t.Fatalf("scheduler_quota_signals = %q, want true", field.Raw)
	}

	// Without a scheduler the flag is meaningless and must not be advertised.
	caps = rpcCapabilitiesFromPlugin(pluginapi.Plugin{Capabilities: pluginapi.Capabilities{SchedulerQuotaSignals: true}})
	if caps.SchedulerQuotaSignals {
		t.Fatal("SchedulerQuotaSignals = true without a scheduler, want false")
	}
}

type quotaSignalsSchedulerFunc struct {
	schedulerFunc
}

func (quotaSignalsSchedulerFunc) SchedulerWantsQuotaSignals() bool { return true }

func TestRPCCapabilitiesSchedulerQuotaSignalsViaMethod(t *testing.T) {
	caps := rpcCapabilitiesFromPlugin(pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
		Scheduler: quotaSignalsSchedulerFunc{schedulerFunc: noopScheduler()},
	}})
	if !caps.SchedulerQuotaSignals {
		t.Fatal("SchedulerQuotaSignals = false, want true")
	}
}

func TestHostSchedulerWantsQuotaSignals(t *testing.T) {
	hostDefault := newHostWithRecords(capabilityRecord{
		id:       "default-sched",
		priority: 1,
		plugin:   pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: noopScheduler()}},
	})
	if hostDefault.SchedulerWantsQuotaSignals() {
		t.Fatal("hostDefault.SchedulerWantsQuotaSignals() = true, want false")
	}

	hostQuota := newHostWithRecords(capabilityRecord{
		id:       "quota-sched",
		priority: 1,
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Scheduler:             noopScheduler(),
			SchedulerQuotaSignals: true,
		}},
	})
	if !hostQuota.SchedulerWantsQuotaSignals() {
		t.Fatal("hostQuota.SchedulerWantsQuotaSignals() = false, want true")
	}

	var nilHost *Host
	if nilHost.SchedulerWantsQuotaSignals() {
		t.Fatal("nilHost.SchedulerWantsQuotaSignals() = true, want false")
	}
}

func TestSchedulerQuotaSignals_RPCRoundTrip(t *testing.T) {
	observedAt := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	var received pluginapi.SchedulerPickRequest
	plugin := validTestPlugin("rpc-quota-scheduler")
	plugin.Capabilities.Scheduler = schedulerFunc(func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
		received = req
		return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-a"}, nil
	})
	plugin.Capabilities.SchedulerQuotaSignals = true

	lookup := newTestSymbolLookup(&testPlugin{registerResult: plugin})
	host := New()
	registered, errRegister := registerRPCPlugin(context.Background(), host, "rpc-quota-scheduler", lookup, pluginabi.MethodPluginRegister, nil)
	if errRegister != nil {
		t.Fatalf("registerRPCPlugin() error = %v", errRegister)
	}
	if !registered.Capabilities.SchedulerQuotaSignals {
		t.Fatal("registered SchedulerQuotaSignals = false, want true")
	}

	hostWithRPC := newHostWithRecords(capabilityRecord{id: "rpc-quota-scheduler", priority: 1, plugin: registered})
	if !hostWithRPC.SchedulerWantsQuotaSignals() {
		t.Fatal("SchedulerWantsQuotaSignals() = false after RPC registration, want true")
	}
	resp, handled, errPick := hostWithRPC.PickAuth(context.Background(), pluginapi.SchedulerPickRequest{
		Provider: "codex",
		Candidates: []pluginapi.SchedulerAuthCandidate{{
			ID:       "auth-a",
			Provider: "codex",
			Quota: &pluginapi.SchedulerQuotaSnapshot{
				ObservedAt: observedAt,
				Signals:    map[string]string{"X-Codex-Secondary-Reset-At": "1794130700"},
			},
		}},
	})
	if errPick != nil || !handled || resp.AuthID != "auth-a" {
		t.Fatalf("PickAuth() = %+v, %v, %v; want auth-a handled", resp, handled, errPick)
	}
	if len(received.Candidates) != 1 || received.Candidates[0].Quota == nil {
		t.Fatalf("plugin received candidates %+v, want one with a quota snapshot", received.Candidates)
	}
	quota := received.Candidates[0].Quota
	if !quota.ObservedAt.Equal(observedAt) || quota.Signals["X-Codex-Secondary-Reset-At"] != "1794130700" {
		t.Fatalf("plugin received quota %+v, want the host snapshot", quota)
	}
}

func TestSchedulerQuotaSignals_OnlyOptedInSchedulersInChainSeeQuota(t *testing.T) {
	var plainReq, quotaReq pluginapi.SchedulerPickRequest
	plain := schedulerFunc(func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
		plainReq = req
		return pluginapi.SchedulerPickResponse{}, nil
	})
	quota := schedulerFunc(func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
		quotaReq = req
		return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-a"}, nil
	})
	host := newHostWithRecords(
		capabilityRecord{id: "plain-sched", priority: 2, plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: plain}}},
		capabilityRecord{id: "quota-sched", priority: 1, plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Scheduler:             quota,
			SchedulerQuotaSignals: true,
		}}},
	)
	if !host.SchedulerWantsQuotaSignals() {
		t.Fatal("SchedulerWantsQuotaSignals() = false with one opted-in scheduler, want true")
	}

	signals := map[string]string{"X-Codex-Secondary-Reset-At": "1794130700"}
	resp, handled, errPick := host.PickAuth(context.Background(), pluginapi.SchedulerPickRequest{
		Provider: "codex",
		Candidates: []pluginapi.SchedulerAuthCandidate{{
			ID:       "auth-a",
			Provider: "codex",
			Quota:    &pluginapi.SchedulerQuotaSnapshot{Signals: signals},
		}},
	})
	if errPick != nil || !handled || resp.AuthID != "auth-a" {
		t.Fatalf("PickAuth() = %+v, %v, %v; want auth-a handled", resp, handled, errPick)
	}
	if len(plainReq.Candidates) != 1 || plainReq.Candidates[0].Quota != nil {
		t.Fatalf("scheduler without opt-in received %+v, want one candidate without quota", plainReq.Candidates)
	}
	if len(quotaReq.Candidates) != 1 || quotaReq.Candidates[0].Quota == nil {
		t.Fatalf("opted-in scheduler received %+v, want one candidate with quota", quotaReq.Candidates)
	}
	quotaReq.Candidates[0].Quota.Signals["X-Codex-Secondary-Reset-At"] = "0"
	if signals["X-Codex-Secondary-Reset-At"] != "1794130700" {
		t.Fatal("scheduler mutation leaked into the host's quota signals")
	}
}
