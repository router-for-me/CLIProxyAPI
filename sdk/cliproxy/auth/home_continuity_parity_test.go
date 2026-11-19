package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestHomeContinuityGateMatchesLocal(t *testing.T) {
	for _, phase := range []string{"pre_admission", "pre_output_429"} {
		for _, scenario := range []string{"ready", "budget exhausted", "disabled", "declined", "callback failed", "empty", "canceled"} {
			t.Run(phase+"/"+scenario, func(t *testing.T) {
				type result struct {
					outcome                               continuityOutcome
					elapsed                               time.Duration
					calls                                 int
					terminated, budgetExhausted, canceled bool
				}
				results := make([]result, 0, 2)
				for _, remote := range []bool{false, true} {
					start := time.Unix(100, 0)
					clock := newFakeContinuityClock(start)
					manager := NewManager(nil, &FillFirstSelector{}, nil)
					manager.SetContinuityClockForTest(clock)
					manager.RegisterExecutor(&continuityTestExecutor{provider: "codex"})
					calls := 0
					evaluate := func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
						calls++
						switch scenario {
						case "disabled":
							return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityDisabled}, true, nil
						case "declined":
							return pluginapi.SchedulerContinuityResponse{}, false, nil
						case "callback failed":
							return pluginapi.SchedulerContinuityResponse{}, true, errors.New("callback failed")
						}
						decision := pluginapi.SchedulerContinuityWait
						if scenario == "ready" && calls > 1 {
							decision = pluginapi.SchedulerContinuityReady
						}
						return pluginapi.SchedulerContinuityResponse{Decision: decision, PollIntervalMs: 10, WaitBudgetMs: 20}, true, nil
					}
					if remote {
						manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
						dispatcher := &homeContinuityDispatcher{protocol: 1, evaluate: func(int) (pluginapi.SchedulerContinuityResponse, bool, error) {
							if scenario == "empty" {
								return pluginapi.SchedulerContinuityResponse{}, false, nil
							}
							response, handled, errEvaluate := evaluate(t.Context(), pluginapi.SchedulerContinuityRequest{})
							// Home translates callback failure/decline exactly as the local gate does.
							if errEvaluate != nil || !handled {
								return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityDisabled}, true, nil
							}
							return response, handled, nil
						}}
						manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
					} else {
						manager.SetPluginScheduler(&testContinuityScheduler{hasContinuity: true, continuityFunc: evaluate})
						if scenario != "empty" {
							id := fmt.Sprintf("gate-parity-%s-%s", phase, scenario)
							registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "gpt"}})
							t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
							if _, errRegister := manager.Register(t.Context(), &Auth{ID: id, Provider: "codex", Status: StatusActive}); errRegister != nil {
								t.Fatal(errRegister)
							}
						}
					}
					ctx, cancel := context.WithCancel(t.Context())
					if scenario == "canceled" {
						cancel()
					}
					session := &continuitySession{}
					outcome, errGate := manager.runCodexContinuityGate(ctx, session, phase, []string{"codex"}, "gpt", cliproxyexecutor.Options{}, 0, false, 0)
					cancel()
					if errGate != nil && !errors.Is(errGate, context.Canceled) {
						t.Fatal(errGate)
					}
					results = append(results, result{outcome: outcome, elapsed: clock.Now().Sub(start), calls: calls, terminated: session.terminated, budgetExhausted: session.budgetExhausted, canceled: errors.Is(errGate, context.Canceled)})
				}
				if results[0] != results[1] {
					t.Fatalf("local=%+v Home=%+v", results[0], results[1])
				}
			})
		}
	}
}

func TestHomeContinuityRequiresCapabilityAndSkipsUpstreamWebsocket(t *testing.T) {
	for _, protocol := range []int{0, 1} {
		for _, websocket := range []bool{false, true} {
			t.Run(fmt.Sprintf("protocol=%d/websocket=%t", protocol, websocket), func(t *testing.T) {
				manager := NewManager(nil, nil, nil)
				manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
				manager.PublishHomeDispatch(&homeContinuityDispatcher{protocol: protocol}, executionregistry.New(), 1)
				ctx := context.Context(t.Context())
				if websocket {
					ctx = cliproxyexecutor.WithRequiredUpstreamWebsocket(ctx)
				}
				_, supported := manager.canRunCodexContinuity(ctx, []string{"codex"})
				if supported != (protocol == 1 && !websocket) {
					t.Fatalf("supported=%t", supported)
				}
			})
		}
	}
}
