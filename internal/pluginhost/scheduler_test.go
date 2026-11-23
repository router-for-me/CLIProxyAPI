package pluginhost

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
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

func TestHostPickAuthUnhandledCallsLowerPriorityScheduler(t *testing.T) {
	var lowCalls int
	host := newHostWithRecords(
		capabilityRecord{
			id:       "low",
			priority: 1,
			meta:     pluginapi.Metadata{Name: "low", Version: "1.0.0"},
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				lowCalls++
				if req.Plugin.Name != "low" {
					t.Fatalf("req.Plugin.Name = %q, want low", req.Plugin.Name)
				}
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

	resp, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-low"))
	if errPick != nil {
		t.Fatalf("PickAuth() error = %v, want nil", errPick)
	}
	if !handled {
		t.Fatal("PickAuth() handled = false, want true")
	}
	if resp.AuthID != "auth-low" {
		t.Fatalf("PickAuth() AuthID = %q, want auth-low", resp.AuthID)
	}
	if lowCalls != 1 {
		t.Fatalf("low calls = %d, want 1", lowCalls)
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

func TestHostPickAuthPreservesExcludeCandidatesWhenUnhandled(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "scheduler",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
			return pluginapi.SchedulerPickResponse{
				Handled:           false,
				ExcludeCandidates: []string{" auth-1 ", "", "auth-2", "auth-1"},
			}, nil
		})}},
	})

	resp, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-1", "auth-2", "auth-3"))
	if errPick != nil {
		t.Fatalf("PickAuth() error = %v, want nil", errPick)
	}
	if handled {
		t.Fatal("PickAuth() handled = true, want false")
	}
	if !slices.Equal(resp.ExcludeCandidates, []string{"auth-1", "auth-2"}) {
		t.Fatalf("PickAuth() ExcludeCandidates = %v, want [auth-1 auth-2]", resp.ExcludeCandidates)
	}
}

func TestHostPickAuthNormalizesExcludeCandidatesWithDelegateBuiltin(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "scheduler",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
			return pluginapi.SchedulerPickResponse{
				Handled:           true,
				DelegateBuiltin:   pluginapi.SchedulerBuiltinRoundRobin,
				ExcludeCandidates: []string{" auth-1 ", " ", "auth-1"},
			}, nil
		})}},
	})

	resp, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-1", "auth-2"))
	if errPick != nil {
		t.Fatalf("PickAuth() error = %v, want nil", errPick)
	}
	if !handled {
		t.Fatal("PickAuth() handled = false, want true")
	}
	if resp.DelegateBuiltin != pluginapi.SchedulerBuiltinRoundRobin {
		t.Fatalf("PickAuth() DelegateBuiltin = %q, want %q", resp.DelegateBuiltin, pluginapi.SchedulerBuiltinRoundRobin)
	}
	if !slices.Equal(resp.ExcludeCandidates, []string{"auth-1"}) {
		t.Fatalf("PickAuth() ExcludeCandidates = %v, want [auth-1]", resp.ExcludeCandidates)
	}
}

func TestHostPickAuthEqualPriorityProviderSchedulersFallback(t *testing.T) {
	var claudeCalls, codexCalls int
	host := newHostWithRecords(
		capabilityRecord{
			id:       "claude-seat-pacer",
			priority: 0,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				claudeCalls++
				if req.Provider != "claude" {
					return pluginapi.SchedulerPickResponse{Handled: false}, nil
				}
				return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "claude-2"}, nil
			})}},
		},
		capabilityRecord{
			id:       "codex-quota-scheduler",
			priority: 0,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				codexCalls++
				if req.Provider != "codex" {
					return pluginapi.SchedulerPickResponse{Handled: false}, nil
				}
				return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "codex-2"}, nil
			})}},
		},
	)

	codexReq := pluginapi.SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5-codex",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "codex-1", Provider: "codex"},
			{ID: "codex-2", Provider: "codex"},
		},
	}
	resp, handled, errPick := host.PickAuth(context.Background(), codexReq)
	if errPick != nil {
		t.Fatalf("PickAuth(codex) error = %v, want nil", errPick)
	}
	if !handled || resp.AuthID != "codex-2" {
		t.Fatalf("PickAuth(codex) = (%#v, %v), want (AuthID=codex-2, true)", resp, handled)
	}
	if claudeCalls != 1 || codexCalls != 1 {
		t.Fatalf("calls after codex = (claude=%d, codex=%d), want (1, 1)", claudeCalls, codexCalls)
	}

	claudeReq := pluginapi.SchedulerPickRequest{
		Provider: "claude",
		Model:    "claude-sonnet-4-6",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "claude-1", Provider: "claude"},
			{ID: "claude-2", Provider: "claude"},
		},
	}
	resp, handled, errPick = host.PickAuth(context.Background(), claudeReq)
	if errPick != nil {
		t.Fatalf("PickAuth(claude) error = %v, want nil", errPick)
	}
	if !handled || resp.AuthID != "claude-2" {
		t.Fatalf("PickAuth(claude) = (%#v, %v), want (AuthID=claude-2, true)", resp, handled)
	}
	if claudeCalls != 2 || codexCalls != 1 {
		t.Fatalf("calls after claude = (claude=%d, codex=%d), want (2, 1)", claudeCalls, codexCalls)
	}

	geminiReq := pluginapi.SchedulerPickRequest{
		Provider:   "gemini",
		Model:      "gemini-2.5-pro",
		Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "gemini-1", Provider: "gemini"}},
	}
	_, handled, errPick = host.PickAuth(context.Background(), geminiReq)
	if errPick != nil {
		t.Fatalf("PickAuth(gemini) error = %v, want nil", errPick)
	}
	if handled {
		t.Fatal("PickAuth(gemini) handled = true, want false")
	}
	if claudeCalls != 3 || codexCalls != 2 {
		t.Fatalf("calls after gemini = (claude=%d, codex=%d), want (3, 2)", claudeCalls, codexCalls)
	}
}

func TestHostPickAuthPropagatesAndAccumulatesExcludeCandidates(t *testing.T) {
	t.Run("filtered for next scheduler and merged when all decline", func(t *testing.T) {
		var secondSeen []string
		host := newHostWithRecords(
			capabilityRecord{
				id:       "first",
				priority: 10,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					return pluginapi.SchedulerPickResponse{
						Handled:           false,
						ExcludeCandidates: []string{" auth-1 ", ""},
					}, nil
				})}},
			},
			capabilityRecord{
				id:       "second",
				priority: 5,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					for _, c := range req.Candidates {
						secondSeen = append(secondSeen, c.ID)
					}
					return pluginapi.SchedulerPickResponse{
						Handled:           false,
						ExcludeCandidates: []string{"auth-2", "auth-1"},
					}, nil
				})}},
			},
		)

		resp, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-1", "auth-2", "auth-3"))
		if errPick != nil {
			t.Fatalf("PickAuth() error = %v, want nil", errPick)
		}
		if handled {
			t.Fatal("PickAuth() handled = true, want false")
		}
		if !slices.Equal(secondSeen, []string{"auth-2", "auth-3"}) {
			t.Fatalf("second scheduler saw %v, want [auth-2 auth-3]", secondSeen)
		}
		if !slices.Equal(resp.ExcludeCandidates, []string{"auth-1", "auth-2"}) {
			t.Fatalf("ExcludeCandidates = %v, want [auth-1 auth-2]", resp.ExcludeCandidates)
		}
	})

	t.Run("merges prior exclusions into DelegateBuiltin response", func(t *testing.T) {
		host := newHostWithRecords(
			capabilityRecord{
				id:       "first",
				priority: 10,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					return pluginapi.SchedulerPickResponse{
						Handled:           false,
						ExcludeCandidates: []string{"auth-1"},
					}, nil
				})}},
			},
			capabilityRecord{
				id:       "second",
				priority: 5,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					return pluginapi.SchedulerPickResponse{
						Handled:           true,
						DelegateBuiltin:   pluginapi.SchedulerBuiltinRoundRobin,
						ExcludeCandidates: []string{"auth-2", "auth-1"},
					}, nil
				})}},
			},
		)

		resp, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-1", "auth-2", "auth-3"))
		if errPick != nil {
			t.Fatalf("PickAuth() error = %v, want nil", errPick)
		}
		if !handled {
			t.Fatal("PickAuth() handled = false, want true")
		}
		if resp.DelegateBuiltin != pluginapi.SchedulerBuiltinRoundRobin {
			t.Fatalf("DelegateBuiltin = %q, want %q", resp.DelegateBuiltin, pluginapi.SchedulerBuiltinRoundRobin)
		}
		if !slices.Equal(resp.ExcludeCandidates, []string{"auth-1", "auth-2"}) {
			t.Fatalf("ExcludeCandidates = %v, want [auth-1 auth-2]", resp.ExcludeCandidates)
		}
	})

	t.Run("stops chain when exclusions empty remaining candidates", func(t *testing.T) {
		var secondCalls int
		host := newHostWithRecords(
			capabilityRecord{
				id:       "first",
				priority: 10,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					return pluginapi.SchedulerPickResponse{
						Handled:           false,
						ExcludeCandidates: []string{"auth-1", "auth-2"},
					}, nil
				})}},
			},
			capabilityRecord{
				id:       "second",
				priority: 5,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					secondCalls++
					return pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinFillFirst}, nil
				})}},
			},
		)

		resp, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-1", "auth-2"))
		if errPick != nil {
			t.Fatalf("PickAuth() error = %v, want nil", errPick)
		}
		if handled {
			t.Fatal("PickAuth() handled = true, want false")
		}
		if secondCalls != 0 {
			t.Fatalf("secondCalls = %d, want 0", secondCalls)
		}
		if !slices.Equal(resp.ExcludeCandidates, []string{"auth-1", "auth-2"}) {
			t.Fatalf("ExcludeCandidates = %v, want [auth-1 auth-2]", resp.ExcludeCandidates)
		}
	})

	t.Run("rejects excluded auth id picked by later scheduler", func(t *testing.T) {
		var secondCalls int
		host := newHostWithRecords(
			capabilityRecord{
				id:       "first",
				priority: 10,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					return pluginapi.SchedulerPickResponse{
						Handled:           false,
						ExcludeCandidates: []string{"auth-1"},
					}, nil
				})}},
			},
			capabilityRecord{
				id:       "second",
				priority: 5,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					secondCalls++
					return pluginapi.SchedulerPickResponse{
						Handled: true,
						AuthID:  "auth-1",
					}, nil
				})}},
			},
		)

		resp, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-1", "auth-2"))
		if errPick != nil {
			t.Fatalf("PickAuth() error = %v, want nil", errPick)
		}
		if handled {
			t.Fatal("PickAuth() handled = true, want false")
		}
		if secondCalls != 1 {
			t.Fatalf("secondCalls = %d, want 1", secondCalls)
		}
		if !slices.Equal(resp.ExcludeCandidates, []string{"auth-1"}) {
			t.Fatalf("ExcludeCandidates = %v, want [auth-1]", resp.ExcludeCandidates)
		}
	})

	t.Run("does not leak prior exclusions when valid AuthID also carries invalid DelegateBuiltin", func(t *testing.T) {
		host := newHostWithRecords(
			capabilityRecord{
				id:       "first",
				priority: 10,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					return pluginapi.SchedulerPickResponse{
						Handled:           false,
						ExcludeCandidates: []string{"auth-1"},
					}, nil
				})}},
			},
			capabilityRecord{
				id:       "second",
				priority: 5,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					return pluginapi.SchedulerPickResponse{
						Handled:         true,
						AuthID:          "auth-2",
						DelegateBuiltin: "unknown",
					}, nil
				})}},
			},
		)

		resp, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-1", "auth-2"))
		if errPick != nil || !handled || resp.AuthID != "auth-2" {
			t.Fatalf("PickAuth() = (%#v, %v, %v), want AuthID=auth-2", resp, handled, errPick)
		}
		if len(resp.ExcludeCandidates) != 0 {
			t.Fatalf("ExcludeCandidates = %v, want empty when AuthID was picked", resp.ExcludeCandidates)
		}
	})
}

func TestHostPickAuthExplicitDecisionStopsChain(t *testing.T) {
	t.Run("DelegateBuiltin stops chain", func(t *testing.T) {
		var lowCalls int
		host := newHostWithRecords(
			capabilityRecord{
				id:       "high",
				priority: 10,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					return pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinFillFirst}, nil
				})}},
			},
			capabilityRecord{
				id:       "low",
				priority: 1,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					lowCalls++
					return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-1"}, nil
				})}},
			},
		)

		resp, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-1"))
		if errPick != nil || !handled || resp.DelegateBuiltin != pluginapi.SchedulerBuiltinFillFirst {
			t.Fatalf("PickAuth() = (%#v, %v, %v), want DelegateBuiltin=%q", resp, handled, errPick, pluginapi.SchedulerBuiltinFillFirst)
		}
		if lowCalls != 0 {
			t.Fatalf("lowCalls = %d, want 0", lowCalls)
		}
	})

	t.Run("Reject stops chain", func(t *testing.T) {
		var lowCalls int
		host := newHostWithRecords(
			capabilityRecord{
				id:       "high",
				priority: 10,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					return pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: "quota_exceeded"}, nil
				})}},
			},
			capabilityRecord{
				id:       "low",
				priority: 1,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					lowCalls++
					return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-1"}, nil
				})}},
			},
		)

		resp, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-1"))
		if errPick != nil || !handled || !resp.Reject {
			t.Fatalf("PickAuth() = (%#v, %v, %v), want handled terminal reject", resp, handled, errPick)
		}
		if lowCalls != 0 {
			t.Fatalf("lowCalls = %d, want 0", lowCalls)
		}
	})

	t.Run("error stops chain", func(t *testing.T) {
		var lowCalls int
		host := newHostWithRecords(
			capabilityRecord{
				id:       "high",
				priority: 10,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					return pluginapi.SchedulerPickResponse{}, errors.New("denied")
				})}},
			},
			capabilityRecord{
				id:       "low",
				priority: 1,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					lowCalls++
					return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-1"}, nil
				})}},
			},
		)

		_, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-1"))
		if !handled || errPick == nil || !strings.Contains(errPick.Error(), "denied") {
			t.Fatalf("PickAuth() = (%v, %v), want handled error denied", handled, errPick)
		}
		if lowCalls != 0 {
			t.Fatalf("lowCalls = %d, want 0", lowCalls)
		}
	})
}

func TestHostPickAuthPanicOrInvalidResponseCallsNextScheduler(t *testing.T) {
	t.Run("panic fuses first and calls next", func(t *testing.T) {
		host := newHostWithRecords(
			capabilityRecord{
				id:       "high",
				priority: 10,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					panic("boom")
				})}},
			},
			capabilityRecord{
				id:       "low",
				priority: 1,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-1"}, nil
				})}},
			},
		)

		resp, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-1"))
		if errPick != nil || !handled || resp.AuthID != "auth-1" {
			t.Fatalf("PickAuth() = (%#v, %v, %v), want AuthID=auth-1", resp, handled, errPick)
		}
		if !host.isPluginFused("high") {
			t.Fatal("high scheduler was not fused after panic")
		}
	})

	t.Run("invalid response falls through to next", func(t *testing.T) {
		host := newHostWithRecords(
			capabilityRecord{
				id:       "high",
				priority: 10,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "missing"}, nil
				})}},
			},
			capabilityRecord{
				id:       "low",
				priority: 1,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-1"}, nil
				})}},
			},
		)

		resp, handled, errPick := host.PickAuth(context.Background(), schedulerRequest("auth-1"))
		if errPick != nil || !handled || resp.AuthID != "auth-1" {
			t.Fatalf("PickAuth() = (%#v, %v, %v), want AuthID=auth-1", resp, handled, errPick)
		}
	})
}

func TestHostSchedulerWantsAcrossPrioritiesAndPerSchedulerNarrowing(t *testing.T) {
	var firstSeen, secondSeen []string
	host := newHostWithRecords(
		capabilityRecord{
			id:       "high-tier-only",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Scheduler: schedulerFunc(func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					for _, c := range req.Candidates {
						firstSeen = append(firstSeen, c.ID)
					}
					return pluginapi.SchedulerPickResponse{
						Handled:           false,
						ExcludeCandidates: []string{"auth-high-1"},
					}, nil
				}),
				SchedulerAcrossPriorities: false,
			}},
		},
		capabilityRecord{
			id:       "low-across",
			priority: 1,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Scheduler: schedulerFunc(func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					for _, c := range req.Candidates {
						secondSeen = append(secondSeen, c.ID)
					}
					return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-low-1"}, nil
				}),
				SchedulerAcrossPriorities: true,
			}},
		},
	)

	if !host.SchedulerWantsAcrossPriorities() {
		t.Fatal("SchedulerWantsAcrossPriorities() = false, want true when any active scheduler opts in")
	}

	req := pluginapi.SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5-codex",
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "auth-high-1", Provider: "codex", Priority: -5},
			{ID: "auth-high-2", Provider: "codex", Priority: -5},
			{ID: "auth-low-1", Provider: "codex", Priority: -10},
		},
	}
	resp, handled, errPick := host.PickAuth(context.Background(), req)
	if errPick != nil || !handled || resp.AuthID != "auth-low-1" {
		t.Fatalf("PickAuth() = (%#v, %v, %v), want AuthID=auth-low-1", resp, handled, errPick)
	}
	if !slices.Equal(firstSeen, []string{"auth-high-1", "auth-high-2"}) {
		t.Fatalf("first scheduler saw %v, want [auth-high-1 auth-high-2]", firstSeen)
	}
	if !slices.Equal(secondSeen, []string{"auth-high-2", "auth-low-1"}) {
		t.Fatalf("second scheduler saw %v, want [auth-high-2 auth-low-1]", secondSeen)
	}

	t.Run("non-across scheduler cannot select lower-tier candidate", func(t *testing.T) {
		var lowCalls int
		hostStrict := newHostWithRecords(
			capabilityRecord{
				id:       "high-tier-only",
				priority: 10,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
					Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
						return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-low-1"}, nil
					}),
					SchedulerAcrossPriorities: false,
				}},
			},
			capabilityRecord{
				id:       "low-across",
				priority: 1,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
					Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
						lowCalls++
						return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-low-1"}, nil
					}),
					SchedulerAcrossPriorities: true,
				}},
			},
		)

		respStrict, handledStrict, errStrict := hostStrict.PickAuth(context.Background(), req)
		if errStrict != nil || !handledStrict || respStrict.AuthID != "auth-low-1" {
			t.Fatalf("PickAuth() = (%#v, %v, %v), want fallback to low-across auth-low-1", respStrict, handledStrict, errStrict)
		}
		if lowCalls != 1 {
			t.Fatalf("lowCalls = %d, want 1 (high-tier-only selection of lower-tier candidate must be rejected)", lowCalls)
		}
	})

	t.Run("ignores fused across-priorities scheduler", func(t *testing.T) {
		hostFused := newHostWithRecords(
			capabilityRecord{
				id:       "tier-only",
				priority: 10,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
					Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
						return pluginapi.SchedulerPickResponse{Handled: false}, nil
					}),
					SchedulerAcrossPriorities: false,
				}},
			},
			capabilityRecord{
				id:       "across-fused",
				priority: 1,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
					Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
						return pluginapi.SchedulerPickResponse{Handled: false}, nil
					}),
					SchedulerAcrossPriorities: true,
				}},
			},
		)
		hostFused.fusePlugin("across-fused", "test", "boom")
		if hostFused.SchedulerWantsAcrossPriorities() {
			t.Fatal("SchedulerWantsAcrossPriorities() = true after across scheduler fused, want false")
		}
		if hostFused.SchedulerWantsAllPriorityCandidates() {
			t.Fatal("SchedulerWantsAllPriorityCandidates() = true with only one remaining non-across scheduler, want false")
		}
		var nilHost *Host
		if nilHost.SchedulerWantsAllPriorityCandidates() {
			t.Fatal("nilHost.SchedulerWantsAllPriorityCandidates() = true, want false")
		}
	})

	t.Run("promotes next tier for subsequent non-across scheduler after highest tier excluded", func(t *testing.T) {
		var secondTierSeen []string
		hostPromote := newHostWithRecords(
			capabilityRecord{
				id:       "first-tier-only",
				priority: 10,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
					Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
						return pluginapi.SchedulerPickResponse{
							Handled:           false,
							ExcludeCandidates: []string{"auth-high-1", "auth-high-2"},
						}, nil
					}),
					SchedulerAcrossPriorities: false,
				}},
			},
			capabilityRecord{
				id:       "second-tier-only",
				priority: 5,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
					Scheduler: schedulerFunc(func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
						for _, c := range req.Candidates {
							secondTierSeen = append(secondTierSeen, c.ID)
						}
						return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-low-1"}, nil
					}),
					SchedulerAcrossPriorities: false,
				}},
			},
		)

		respPromote, handledPromote, errPromote := hostPromote.PickAuth(context.Background(), req)
		if errPromote != nil || !handledPromote || respPromote.AuthID != "auth-low-1" {
			t.Fatalf("PickAuth() = (%#v, %v, %v), want AuthID=auth-low-1", respPromote, handledPromote, errPromote)
		}
		if !slices.Equal(secondTierSeen, []string{"auth-low-1"}) {
			t.Fatalf("second-tier-only saw %v, want [auth-low-1]", secondTierSeen)
		}
	})
}

func TestHostPickAuthIsolatesRequestMutationsAcrossSchedulers(t *testing.T) {
	var secondProvider string
	var secondHeader string
	var secondMeta any
	var secondAttr string
	host := newHostWithRecords(
		capabilityRecord{
			id:       "first",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				req.Providers[0] = "mutated"
				req.Options.Headers["X-Test"][0] = "mutated"
				req.Options.Metadata["k"] = "mutated"
				req.Candidates[0].Attributes["tier"] = "mutated"
				return pluginapi.SchedulerPickResponse{Handled: false}, nil
			})}},
		},
		capabilityRecord{
			id:       "second",
			priority: 5,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				secondProvider = req.Providers[0]
				secondHeader = req.Options.Headers["X-Test"][0]
				secondMeta = req.Options.Metadata["k"]
				secondAttr = req.Candidates[0].Attributes["tier"]
				return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-1"}, nil
			})}},
		},
	)

	req := pluginapi.SchedulerPickRequest{
		Provider:  "codex",
		Providers: []string{"codex"},
		Model:     "gpt-5-codex",
		Options: pluginapi.SchedulerOptions{
			Headers:  map[string][]string{"X-Test": {"orig"}},
			Metadata: map[string]any{"k": "orig"},
		},
		Candidates: []pluginapi.SchedulerAuthCandidate{
			{ID: "auth-1", Provider: "codex", Attributes: map[string]string{"tier": "orig"}},
		},
	}
	resp, handled, errPick := host.PickAuth(context.Background(), req)
	if errPick != nil || !handled || resp.AuthID != "auth-1" {
		t.Fatalf("PickAuth() = (%#v, %v, %v), want AuthID=auth-1", resp, handled, errPick)
	}
	if secondProvider != "codex" || secondHeader != "orig" || secondMeta != "orig" || secondAttr != "orig" {
		t.Fatalf("second scheduler saw mutated inputs: provider=%q header=%q meta=%v attr=%q", secondProvider, secondHeader, secondMeta, secondAttr)
	}
}

func TestHostPickAuthPreservesOverlappingSliceMetadata(t *testing.T) {
	base := []string{"a", "b", "c"}
	for iter := 0; iter < 20; iter++ {
		var firstShort, firstFull []string
		var secondShort, secondFull []string
		var candShort, candFull []string
		host := newHostWithRecords(
			capabilityRecord{
				id:       "first",
				priority: 10,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					firstShort = append([]string(nil), req.Options.Metadata["short"].([]string)...)
					firstFull = append([]string(nil), req.Options.Metadata["full"].([]string)...)
					req.Options.Metadata["short"].([]string)[0] = "mutated"
					return pluginapi.SchedulerPickResponse{Handled: false}, nil
				})}},
			},
			capabilityRecord{
				id:       "second",
				priority: 5,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{Scheduler: schedulerFunc(func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
					secondShort = append([]string(nil), req.Options.Metadata["short"].([]string)...)
					secondFull = append([]string(nil), req.Options.Metadata["full"].([]string)...)
					candShort = append([]string(nil), req.Candidates[0].Metadata["short"].([]string)...)
					candFull = append([]string(nil), req.Candidates[0].Metadata["full"].([]string)...)
					return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-1"}, nil
				})}},
			},
		)

		req := pluginapi.SchedulerPickRequest{
			Provider: "codex",
			Model:    "gpt-5-codex",
			Options: pluginapi.SchedulerOptions{
				Metadata: map[string]any{
					"short": base[:1],
					"full":  base[:3],
				},
			},
			Candidates: []pluginapi.SchedulerAuthCandidate{
				{
					ID:       "auth-1",
					Provider: "codex",
					Metadata: map[string]any{
						"short": base[:1],
						"full":  base[:3],
					},
				},
			},
		}

		resp, handled, errPick := host.PickAuth(context.Background(), req)
		if errPick != nil || !handled || resp.AuthID != "auth-1" {
			t.Fatalf("iter %d: PickAuth() = (%#v, %v, %v), want AuthID=auth-1", iter, resp, handled, errPick)
		}
		if !slices.Equal(firstShort, []string{"a"}) || !slices.Equal(firstFull, []string{"a", "b", "c"}) {
			t.Fatalf("iter %d: first saw short=%v full=%v, want [a] and [a b c]", iter, firstShort, firstFull)
		}
		if !slices.Equal(secondShort, []string{"a"}) || !slices.Equal(secondFull, []string{"a", "b", "c"}) {
			t.Fatalf("iter %d: second saw short=%v full=%v, want [a] and [a b c]", iter, secondShort, secondFull)
		}
		if !slices.Equal(candShort, []string{"a"}) || !slices.Equal(candFull, []string{"a", "b", "c"}) {
			t.Fatalf("iter %d: candidate metadata saw short=%v full=%v, want [a] and [a b c]", iter, candShort, candFull)
		}
		if !slices.Equal(base, []string{"a", "b", "c"}) {
			t.Fatalf("iter %d: backing slice mutated to %v", iter, base)
		}
	}
}

type stubSchedulerExecutor struct {
	id         string
	lastAuthID *string
}

func (e stubSchedulerExecutor) Identifier() string { return e.id }

func (e stubSchedulerExecutor) Execute(_ context.Context, auth *coreauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.lastAuthID != nil && auth != nil {
		*e.lastAuthID = auth.ID
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e stubSchedulerExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e stubSchedulerExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e stubSchedulerExecutor) CountTokens(_ context.Context, _ *coreauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e stubSchedulerExecutor) HttpRequest(_ context.Context, _ *coreauth.Auth, _ *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestManagerMultiNonAcrossSchedulersPromoteLowerTierOnHighestTierExclusion(t *testing.T) {
	t.Run("single provider invokes second non-across scheduler on promoted tier", func(t *testing.T) {
		var firstSeen, secondSeen []string
		var secondCalls int
		host := newHostWithRecords(
			capabilityRecord{
				id:       "first-tier-only",
				priority: 10,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
					Scheduler: schedulerFunc(func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
						for _, c := range req.Candidates {
							firstSeen = append(firstSeen, c.ID)
						}
						return pluginapi.SchedulerPickResponse{
							Handled:           false,
							ExcludeCandidates: []string{"codex-high-1"},
						}, nil
					}),
					SchedulerAcrossPriorities: false,
				}},
			},
			capabilityRecord{
				id:       "second-tier-only",
				priority: 5,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
					Scheduler: schedulerFunc(func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
						secondCalls++
						for _, c := range req.Candidates {
							secondSeen = append(secondSeen, c.ID)
						}
						return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "codex-low-2"}, nil
					}),
					SchedulerAcrossPriorities: false,
				}},
			},
		)

		manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
		var executedAuthID string
		manager.RegisterExecutor(stubSchedulerExecutor{id: "codex", lastAuthID: &executedAuthID})
		manager.SetPluginScheduler(host)

		for _, candidate := range []*coreauth.Auth{
			{ID: "codex-high-1", Provider: "codex", Attributes: map[string]string{"priority": "100"}},
			{ID: "codex-low-1", Provider: "codex", Attributes: map[string]string{"priority": "50"}},
			{ID: "codex-low-2", Provider: "codex", Attributes: map[string]string{"priority": "50"}},
			{ID: "codex-lowest-1", Provider: "codex", Attributes: map[string]string{"priority": "10"}},
		} {
			if _, errReg := manager.Register(context.Background(), candidate); errReg != nil {
				t.Fatalf("Register(%s) error = %v", candidate.ID, errReg)
			}
		}

		if host.SchedulerWantsAcrossPriorities() {
			t.Fatal("host.SchedulerWantsAcrossPriorities() = true, want false")
		}
		if !host.SchedulerWantsAllPriorityCandidates() {
			t.Fatal("host.SchedulerWantsAllPriorityCandidates() = false, want true for multiple active schedulers")
		}
		if manager.PluginSchedulerWantsAcrossPriorities() {
			t.Fatal("manager.PluginSchedulerWantsAcrossPriorities() = true, want false")
		}

		if _, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}); errExec != nil {
			t.Fatalf("Execute() error = %v, want nil", errExec)
		}
		if !slices.Equal(firstSeen, []string{"codex-high-1"}) {
			t.Fatalf("first scheduler saw %v, want [codex-high-1]", firstSeen)
		}
		if secondCalls != 1 {
			t.Fatalf("secondCalls = %d, want 1", secondCalls)
		}
		if !slices.Equal(secondSeen, []string{"codex-low-1", "codex-low-2"}) {
			t.Fatalf("second scheduler saw %v, want [codex-low-1 codex-low-2]", secondSeen)
		}
		if executedAuthID != "codex-low-2" {
			t.Fatalf("executedAuthID = %q, want codex-low-2", executedAuthID)
		}
	})

	t.Run("mixed providers invoke second non-across scheduler on promoted tier", func(t *testing.T) {
		var firstSeen, secondSeen []string
		var secondCalls int
		host := newHostWithRecords(
			capabilityRecord{
				id:       "first-tier-only",
				priority: 10,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
					Scheduler: schedulerFunc(func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
						for _, c := range req.Candidates {
							firstSeen = append(firstSeen, c.ID)
						}
						return pluginapi.SchedulerPickResponse{
							Handled:           false,
							ExcludeCandidates: []string{"ag-high"},
						}, nil
					}),
					SchedulerAcrossPriorities: false,
				}},
			},
			capabilityRecord{
				id:       "second-tier-only",
				priority: 5,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
					Scheduler: schedulerFunc(func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
						secondCalls++
						for _, c := range req.Candidates {
							secondSeen = append(secondSeen, c.ID)
						}
						return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "claude-low-2"}, nil
					}),
					SchedulerAcrossPriorities: false,
				}},
			},
		)

		manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
		var executedAuthID string
		manager.RegisterExecutor(stubSchedulerExecutor{id: "antigravity", lastAuthID: &executedAuthID})
		manager.RegisterExecutor(stubSchedulerExecutor{id: "claude", lastAuthID: &executedAuthID})
		manager.SetPluginScheduler(host)

		for _, candidate := range []*coreauth.Auth{
			{ID: "ag-high", Provider: "antigravity", Attributes: map[string]string{"priority": "100"}},
			{ID: "claude-low-1", Provider: "claude", Attributes: map[string]string{"priority": "50"}},
			{ID: "claude-low-2", Provider: "claude", Attributes: map[string]string{"priority": "50"}},
		} {
			if _, errReg := manager.Register(context.Background(), candidate); errReg != nil {
				t.Fatalf("Register(%s) error = %v", candidate.ID, errReg)
			}
		}

		if _, errExec := manager.Execute(context.Background(), []string{"antigravity", "claude"}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}); errExec != nil {
			t.Fatalf("Execute() error = %v, want nil", errExec)
		}
		if !slices.Equal(firstSeen, []string{"ag-high"}) {
			t.Fatalf("first scheduler saw %v, want [ag-high]", firstSeen)
		}
		if secondCalls != 1 {
			t.Fatalf("secondCalls = %d, want 1", secondCalls)
		}
		if !slices.Equal(secondSeen, []string{"claude-low-1", "claude-low-2"}) {
			t.Fatalf("second scheduler saw %v, want [claude-low-1 claude-low-2]", secondSeen)
		}
		if executedAuthID != "claude-low-2" {
			t.Fatalf("executedAuthID = %q, want claude-low-2", executedAuthID)
		}
	})

	t.Run("builtin fallback stays on highest tier when both non-across schedulers decline without exclusions", func(t *testing.T) {
		host := newHostWithRecords(
			capabilityRecord{
				id:       "first-tier-only",
				priority: 10,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
					Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
						return pluginapi.SchedulerPickResponse{Handled: false}, nil
					}),
					SchedulerAcrossPriorities: false,
				}},
			},
			capabilityRecord{
				id:       "second-tier-only",
				priority: 5,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
					Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
						return pluginapi.SchedulerPickResponse{Handled: false}, nil
					}),
					SchedulerAcrossPriorities: false,
				}},
			},
		)

		manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
		var executedAuthID string
		manager.RegisterExecutor(stubSchedulerExecutor{id: "codex", lastAuthID: &executedAuthID})
		manager.SetPluginScheduler(host)

		for _, candidate := range []*coreauth.Auth{
			{ID: "codex-high-1", Provider: "codex", Attributes: map[string]string{"priority": "100"}},
			{ID: "codex-low-1", Provider: "codex", Attributes: map[string]string{"priority": "50"}},
		} {
			if _, errReg := manager.Register(context.Background(), candidate); errReg != nil {
				t.Fatalf("Register(%s) error = %v", candidate.ID, errReg)
			}
		}

		if _, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{}); errExec != nil {
			t.Fatalf("Execute() error = %v, want nil", errExec)
		}
		if executedAuthID != "codex-high-1" {
			t.Fatalf("executedAuthID = %q, want codex-high-1", executedAuthID)
		}
	})
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
