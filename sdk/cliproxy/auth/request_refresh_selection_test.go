package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestRequestRefreshPluginSchedulerDemandAdmission(t *testing.T) {
	for _, mode := range []string{"chosen", "delegated-round-robin", "delegated-fill-first", "delegated-weighted"} {
		t.Run(mode, func(t *testing.T) {
			m, a, _, e := demandManager(t, time.Now().Add(-time.Hour))
			scheduler := &fakePluginScheduler{handled: true, resp: pluginapi.SchedulerPickResponse{Handled: true, AuthID: a.ID}}
			switch mode {
			case "delegated-round-robin":
				scheduler.resp.AuthID = ""
				scheduler.resp.DelegateBuiltin = pluginapi.SchedulerBuiltinRoundRobin
			case "delegated-fill-first":
				scheduler.resp.AuthID = ""
				scheduler.resp.DelegateBuiltin = pluginapi.SchedulerBuiltinFillFirst
			case "delegated-weighted":
				scheduler.resp.AuthID = ""
				scheduler.resp.DelegateBuiltin = "weighted-round-robin"
			}
			m.SetPluginScheduler(scheduler)
			_, err := m.Execute(context.Background(), []string{a.Provider}, cliproxyexecutor.Request{Model: "demand-model"}, cliproxyexecutor.Options{})
			if err != nil || e.refreshes.Load() != 1 || e.calls.Load() != 1 {
				t.Fatalf("plugin demand=%v refresh=%d inference=%d", err, e.refreshes.Load(), e.calls.Load())
			}
			if len(scheduler.requests) != 1 || len(scheduler.requests[0].Candidates) != 1 || scheduler.requests[0].Candidates[0].ID != a.ID {
				t.Fatal("expired demand candidate did not reach plugin scheduler")
			}
		})
	}
}
