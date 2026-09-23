package auth

import (
	"context"
	"errors"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestWithRequiredAuthKindRestrictsSelectAuth(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	for _, candidate := range []*Auth{
		{ID: "codex-api-key", Provider: "codex", Attributes: map[string]string{AttributeAPIKey: "test-key"}},
		{ID: "codex-oauth", Provider: "codex", Metadata: map[string]any{"access_token": "test-token"}},
	} {
		if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", candidate.ID, errRegister)
		}
	}

	scheduler := &fakePluginScheduler{
		resp:    pluginapi.SchedulerPickResponse{Handled: true, AuthID: "codex-api-key"},
		handled: true,
	}
	manager.SetPluginScheduler(scheduler)

	unrestricted, errSelect := manager.SelectAuth(context.Background(), "codex", "", cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("SelectAuth() error = %v", errSelect)
	}
	if unrestricted == nil || unrestricted.ID != "codex-api-key" {
		t.Fatalf("SelectAuth() auth = %#v, want codex-api-key", unrestricted)
	}

	selected, errSelect := manager.SelectAuth(WithRequiredAuthKind(context.Background(), AuthKindOAuth), "codex", "", cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("SelectAuth(WithRequiredAuthKind oauth) error = %v", errSelect)
	}
	if selected == nil || selected.ID != "codex-oauth" {
		t.Fatalf("SelectAuth(WithRequiredAuthKind oauth) auth = %#v, want codex-oauth", selected)
	}
	if len(scheduler.requests) < 2 || len(scheduler.requests[1].Candidates) != 1 || scheduler.requests[1].Candidates[0].ID != "codex-oauth" {
		t.Fatalf("oauth candidates = %#v, want only codex-oauth", scheduler.requests)
	}

	wrapped, errSelect := manager.SelectAuth(withRequiredAuthKind(context.Background(), AuthKindAPIKey), "codex", "", cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("SelectAuth(withRequiredAuthKind apikey) error = %v", errSelect)
	}
	if wrapped == nil || wrapped.ID != "codex-api-key" {
		t.Fatalf("SelectAuth(withRequiredAuthKind apikey) auth = %#v, want codex-api-key", wrapped)
	}
}

func TestWithRequiredAuthKindEmptyDoesNotHideAuths(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID:         "codex-api-key",
		Provider:   "codex",
		Attributes: map[string]string{AttributeAPIKey: "test-key"},
	}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	selected, errSelect := manager.SelectAuth(WithRequiredAuthKind(context.Background(), ""), "codex", "", cliproxyexecutor.Options{})
	if errSelect != nil {
		t.Fatalf("SelectAuth(empty kind) error = %v", errSelect)
	}
	if selected == nil || selected.ID != "codex-api-key" {
		t.Fatalf("SelectAuth(empty kind) auth = %#v, want codex-api-key", selected)
	}
}

func TestWithRequiredAuthKindUnknownKindFindsNoAuth(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID:       "codex-oauth",
		Provider: "codex",
		Metadata: map[string]any{"access_token": "test-token"},
	}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	selected, errSelect := manager.SelectAuth(WithRequiredAuthKind(context.Background(), "certificate"), "codex", "", cliproxyexecutor.Options{})
	if selected != nil {
		t.Fatalf("SelectAuth(unknown kind) auth = %#v, want nil", selected)
	}
	var authErr *Error
	if !errors.As(errSelect, &authErr) || authErr.Code != "auth_not_found" {
		t.Fatalf("SelectAuth(unknown kind) error = %#v, want auth_not_found", errSelect)
	}
}
