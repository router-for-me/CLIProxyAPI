package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestManagerPluginSchedulerExclusionsPreserveFillFirst(t *testing.T) {
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	for _, authID := range []string{"auth-a", "auth-b"} {
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: "gemini"}); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", authID, errRegister)
		}
	}

	manager.SetPluginScheduler(&fakePluginScheduler{
		resp:    pluginapi.SchedulerPickResponse{Handled: true, ExcludedAuthIDs: []string{"auth-a"}},
		handled: true,
	})
	got, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if got == nil || got.ID != "auth-b" {
		t.Fatalf("pickNext() auth = %#v, want auth-b after auth-a exclusion", got)
	}
}

func TestManagerPluginSchedulerAllExclusionsFailClosed(t *testing.T) {
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}
	manager.SetPluginScheduler(&fakePluginScheduler{
		resp:    pluginapi.SchedulerPickResponse{Handled: true, ExcludedAuthIDs: []string{"auth-a"}},
		handled: true,
	})
	got, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if got != nil {
		t.Fatalf("pickNext() auth = %#v, want nil when all candidates are excluded", got)
	}
	if errPick == nil {
		t.Fatal("pickNext() error = nil, want an unavailable-auth error")
	}
}
