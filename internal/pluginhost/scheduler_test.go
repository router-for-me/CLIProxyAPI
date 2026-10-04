package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestHostPickAuthUsesHighestPrioritySchedulerOnly(t *testing.T) {
	var highCalls int
	var lowCalls int
	host := newHostWithRecords(
		capabilityRecord{
			id:       "low",
			priority: 1,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				lowCalls++
				return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-low"}, nil
			})}},
		},
		capabilityRecord{
			id:       "high",
			priority: 10,
			meta:     pluginapi.Metadata{Name: "high", Version: "1.0.0"},
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(ctx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				highCalls++
				if req.Plugin.Name != "high" {
					t.Fatalf("req.Plugin.Name = %q, want high", req.Plugin.Name)
				}
				return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-high"}, nil
			})}},
		},
	)

	resp, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-high", "auth-low"))
	if errPick != nil {
		t.Fatalf("PickAuth() error = %v, want nil", errPick)
	}
	if !handled {
		t.Fatal("PickAuth() handled = false, want true")
	}
	if resp.AuthID != "auth-high" {
		t.Fatalf("PickAuth() AuthID = %q, want auth-high", resp.AuthID)
	}
	if highCalls != 1 {
		t.Fatalf("high calls = %d, want 1", highCalls)
	}
	if lowCalls != 0 {
		t.Fatalf("low calls = %d, want 0", lowCalls)
	}
}

func TestHostPickAuthReturnsSchedulerError(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "scheduler",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
			return pluginapi.SchedulerPickResponse{}, errors.New("tenant quota exhausted")
		})}},
	})

	_, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-1"))
	if !handled {
		t.Fatal("PickAuth() handled = false, want true")
	}
	if errPick == nil || !strings.Contains(errPick.Error(), "tenant quota exhausted") {
		t.Fatalf("PickAuth() error = %v, want tenant quota exhausted", errPick)
	}
}

func TestHostPickAuthPanicFusesAndFallsBack(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "scheduler",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
			panic("boom")
		})}},
	})

	_, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-1"))
	if handled {
		t.Fatal("PickAuth() handled = true, want false")
	}
	if errPick != nil {
		t.Fatalf("PickAuth() error = %v, want nil", errPick)
	}
	if !host.isPluginFused("scheduler") {
		t.Fatal("scheduler plugin was not fused after panic")
	}
}

func TestHostPickAuthUnhandledDoesNotCallLowerPriorityScheduler(t *testing.T) {
	var lowCalls int
	host := newHostWithRecords(
		capabilityRecord{
			id:       "low",
			priority: 1,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				lowCalls++
				return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-low"}, nil
			})}},
		},
		capabilityRecord{
			id:       "high",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				return pluginapi.SchedulerPickResponse{Handled: false}, nil
			})}},
		},
	)

	_, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-low"))
	if errPick != nil {
		t.Fatalf("PickAuth() error = %v, want nil", errPick)
	}
	if handled {
		t.Fatal("PickAuth() handled = true, want false")
	}
	if lowCalls != 0 {
		t.Fatalf("low calls = %d, want 0", lowCalls)
	}
}

func TestHostPickAuthInvalidResponseFallsBack(t *testing.T) {
	tests := []struct {
		name string
		resp pluginapi.SchedulerPickResponse
	}{
		{
			name: "unknown auth id",
			resp: pluginapi.SchedulerPickResponse{Handled: true, AuthID: "missing"},
		},
		{
			name: "unknown delegate",
			resp: pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: "unknown"},
		},
		{
			name: "handled without decision",
			resp: pluginapi.SchedulerPickResponse{Handled: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := newHostWithRecords(capabilityRecord{
				id: "scheduler",
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					return tt.resp, nil
				})}},
			})

			_, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-1"))
			if errPick != nil {
				t.Fatalf("PickAuth() error = %v, want nil", errPick)
			}
			if handled {
				t.Fatal("PickAuth() handled = true, want false")
			}
		})
	}
}

func TestHostPickAuthPrefersValidAuthIDOverInvalidDelegate(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "scheduler",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
			return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-a", DelegateBuiltin: "unknown"}, nil
		})}},
	})

	resp, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-a"))
	if errPick != nil {
		t.Fatalf("PickAuth() error = %v, want nil", errPick)
	}
	if !handled {
		t.Fatal("PickAuth() handled = false, want true")
	}
	if resp.AuthID != "auth-a" {
		t.Fatalf("PickAuth() AuthID = %q, want auth-a", resp.AuthID)
	}
}

func TestHostPickAuthAllowsKnownBuiltinDelegates(t *testing.T) {
	for _, delegate := range []string{pluginapi.SchedulerBuiltinRoundRobin, pluginapi.SchedulerBuiltinFillFirst} {
		t.Run(delegate, func(t *testing.T) {
			host := newHostWithRecords(capabilityRecord{
				id: "scheduler",
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					return pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: delegate}, nil
				})}},
			})

			resp, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-1"))
			if errPick != nil {
				t.Fatalf("PickAuth() error = %v, want nil", errPick)
			}
			if !handled {
				t.Fatal("PickAuth() handled = false, want true")
			}
			if resp.DelegateBuiltin != delegate {
				t.Fatalf("PickAuth() DelegateBuiltin = %q, want %q", resp.DelegateBuiltin, delegate)
			}
		})
	}
}

func TestHostPickAuthTerminalRejection(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "scheduler",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
			return pluginapi.SchedulerPickResponse{
				Handled:      true,
				Reject:       true,
				RejectCode:   "quota_exceeded",
				RejectReason: "all candidates exceed quota",
			}, nil
		})}},
	})

	resp, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-1"))
	if errPick != nil {
		t.Fatalf("PickAuth() error = %v, want nil", errPick)
	}
	if !handled {
		t.Fatal("PickAuth() handled = false, want true")
	}
	if !resp.Reject {
		t.Fatal("PickAuth() resp.Reject = false, want true")
	}
	if resp.RejectCode != "quota_exceeded" {
		t.Fatalf("PickAuth() RejectCode = %q, want quota_exceeded", resp.RejectCode)
	}
	if resp.RejectReason != "all candidates exceed quota" {
		t.Fatalf("PickAuth() RejectReason = %q, want all candidates exceed quota", resp.RejectReason)
	}
}

func TestHostPickAuthTerminalRejectionWhitespaceDefaults(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "scheduler",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
			return pluginapi.SchedulerPickResponse{
				Handled:      true,
				Reject:       true,
				RejectCode:   "   ",
				RejectReason: "\t  \n",
			}, nil
		})}},
	})

	resp, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-1"))
	if errPick != nil {
		t.Fatalf("PickAuth() error = %v, want nil", errPick)
	}
	if !handled || !resp.Reject {
		t.Fatalf("PickAuth() handled=%v, reject=%v, want true/true", handled, resp.Reject)
	}
	if resp.RejectCode != "auth_unavailable" {
		t.Fatalf("PickAuth() RejectCode = %q, want auth_unavailable", resp.RejectCode)
	}
	if resp.RejectReason != "scheduler rejected candidate selection" {
		t.Fatalf("PickAuth() RejectReason = %q, want scheduler rejected candidate selection", resp.RejectReason)
	}
}

func TestHostSchedulerWantsAcrossPriorities(t *testing.T) {
	hostDefault := newHostWithRecords(capabilityRecord{
		id:       "default-sched",
		priority: 1,
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				return pluginapi.SchedulerPickResponse{}, nil
			}),
			SchedulerAcrossPriorities: false,
		}},
	})
	if hostDefault.SchedulerWantsAcrossPriorities() {
		t.Fatal("hostDefault.SchedulerWantsAcrossPriorities() = true, want false")
	}

	hostAcross := newHostWithRecords(capabilityRecord{
		id:       "across-sched",
		priority: 1,
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				return pluginapi.SchedulerPickResponse{}, nil
			}),
			SchedulerAcrossPriorities: true,
		}},
	})
	if !hostAcross.SchedulerWantsAcrossPriorities() {
		t.Fatal("hostAcross.SchedulerWantsAcrossPriorities() = false, want true")
	}

	var nilHost *Host
	if nilHost.SchedulerWantsAcrossPriorities() {
		t.Fatal("nilHost.SchedulerWantsAcrossPriorities() = true, want false")
	}
}

func schedulerRequest(ids ...string) pluginapi.SchedulerPickRequest {
	req := pluginapi.SchedulerPickRequest{
		Provider: "test",
		Model:    "test-model",
	}
	for _, id := range ids {
		req.Candidates = append(req.Candidates, pluginapi.SchedulerAuthCandidate{ID: id})
	}
	return req
}

func TestRPCSchedulerPickSendsCandidateQuota(t *testing.T) {
	client := &capturePluginClient{}
	adapter := &rpcPluginAdapter{id: "scheduler", client: client}
	observedAt := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

	_, errPick := adapter.Pick(context.Background(), pluginapi.SchedulerPickRequest{
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "observed", Quota: &pluginapi.SchedulerQuotaObservation{
				ObservedAt: observedAt,
				Signals:    map[string]string{"Anthropic-Ratelimit-Unified-7d-Utilization": "0.65"},
			}},
			{ID: "unobserved"},
		},
	})
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}

	var sent struct {
		Candidates []struct {
			ID    string
			Quota *struct {
				ObservedAt string
				Signals    map[string]string
			}
		}
	}
	if errUnmarshal := json.Unmarshal(client.requests[pluginabi.MethodSchedulerPick], &sent); errUnmarshal != nil {
		t.Fatalf("decode sent request: %v", errUnmarshal)
	}
	observed := sent.Candidates[0].Quota
	if observed == nil || observed.ObservedAt != "2026-10-04T12:00:00Z" ||
		observed.Signals["Anthropic-Ratelimit-Unified-7d-Utilization"] != "0.65" {
		t.Fatalf("sent quota = %#v", observed)
	}
	if sent.Candidates[1].Quota != nil {
		t.Fatalf("sent quota for unobserved candidate = %#v, want null", sent.Candidates[1].Quota)
	}
}
