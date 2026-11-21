package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func newAllowedAuthsTestManager(t *testing.T, model string, ids ...string) (*Manager, map[string]string, *[]string) {
	t.Helper()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(3, 30*time.Second, 0)
	indexes := make(map[string]string, len(ids))
	for _, id := range ids {
		candidate := &Auth{ID: id, Provider: "codex", Status: StatusActive, Attributes: map[string]string{"path": "/auths/" + id + ".json"}}
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		registered, errRegister := manager.Register(context.Background(), candidate)
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		indexes[id] = registered.EnsureIndex()
		if indexes[id] == "" {
			t.Fatalf("auth %s has no index", id)
		}
	}
	var attempts []string
	manager.RegisterExecutor(&mockCustomErrorExecutor{
		identifier: "codex",
		executeFn: func(_ context.Context, selected *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			attempts = append(attempts, selected.ID)
			return cliproxyexecutor.Response{Payload: []byte(selected.ID)}, nil
		},
	})
	return manager, indexes, &attempts
}

func TestAllowedAuthsWithoutPolicyUsesAllCredentials(t *testing.T) {
	const model = "policy-all-model"
	ids := []string{"policy-all-a-" + t.Name(), "policy-all-b-" + t.Name(), "policy-all-c-" + t.Name()}
	manager, _, attempts := newAllowedAuthsTestManager(t, model, ids...)
	for i := 0; i < 6; i++ {
		if _, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for _, id := range *attempts {
		seen[id] = true
	}
	if len(seen) != len(ids) {
		t.Fatalf("unrestricted key used %v, want all %d credentials", *attempts, len(ids))
	}
}

func TestAllowedAuthsRestrictsSelectionToIndex(t *testing.T) {
	const model = "policy-one-model"
	ids := []string{"policy-one-a-" + t.Name(), "policy-one-b-" + t.Name(), "policy-one-c-" + t.Name()}
	manager, indexes, attempts := newAllowedAuthsTestManager(t, model, ids...)
	ctx := WithAllowedAuths(context.Background(), []string{indexes[ids[1]]})
	for i := 0; i < 6; i++ {
		if _, err := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range *attempts {
		if id != ids[1] {
			t.Fatalf("restricted key used %s, attempts=%v", id, *attempts)
		}
	}
	selected, err := manager.SelectAuth(ctx, "codex", model, cliproxyexecutor.Options{})
	if err != nil || selected.ID != ids[1] {
		t.Fatalf("SelectAuth = %v, %v; want %s", selected, err, ids[1])
	}
}

func TestAllowedAuthsAcceptsCredentialID(t *testing.T) {
	const model = "policy-id-model"
	ids := []string{"policy-id-a-" + t.Name(), "policy-id-b-" + t.Name()}
	manager, _, attempts := newAllowedAuthsTestManager(t, model, ids...)
	ctx := WithAllowedAuths(context.Background(), []string{ids[0]})
	for i := 0; i < 4; i++ {
		if _, err := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range *attempts {
		if id != ids[0] {
			t.Fatalf("restricted key used %s", id)
		}
	}
}

func TestAllowedAuthsCooldownDoesNotSpillToOtherCredentials(t *testing.T) {
	withQuotaCooldownEnabled(t)
	const model = "policy-cooldown-model"
	ids := []string{"policy-cd-a-" + t.Name(), "policy-cd-b-" + t.Name()}
	manager, indexes, attempts := newAllowedAuthsTestManager(t, model, ids...)
	retryAfter := time.Hour
	manager.MarkResult(context.Background(), Result{
		AuthID: ids[0], Provider: "codex", Model: model,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}, RetryAfter: &retryAfter,
	})
	ctx := WithAllowedAuths(context.Background(), []string{indexes[ids[0]]})
	_, err := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("expected cooldown error for restricted key")
	}
	var se interface{ StatusCode() int }
	if !errors.As(err, &se) || se.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want 429 cooldown", err)
	}
	if len(*attempts) != 0 {
		t.Fatalf("restricted key fell through to %v", *attempts)
	}
	// An unrestricted request still reaches the healthy credential.
	if _, errFree := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errFree != nil {
		t.Fatal(errFree)
	}
}

func TestAllowedAuthsRejectsPinnedCredentialOutsideList(t *testing.T) {
	const model = "policy-pin-model"
	ids := []string{"policy-pin-a-" + t.Name(), "policy-pin-b-" + t.Name()}
	manager, indexes, attempts := newAllowedAuthsTestManager(t, model, ids...)
	ctx := WithAllowedAuths(context.Background(), []string{indexes[ids[0]]})
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: ids[1]}}
	if _, err := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts); err == nil {
		t.Fatal("expected pinned credential outside the allow-list to be rejected")
	}
	if len(*attempts) != 0 {
		t.Fatalf("pinned disallowed credential executed: %v", *attempts)
	}
}

func TestAllowedAuthsUnknownListYieldsAuthNotFound(t *testing.T) {
	const model = "policy-none-model"
	manager, _, _ := newAllowedAuthsTestManager(t, model, "policy-none-a-"+t.Name())
	ctx := WithAllowedAuths(context.Background(), []string{"does-not-exist"})
	_, err := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	var authErr *Error
	if !errors.As(err, &authErr) || authErr.Code != "auth_not_found" {
		t.Fatalf("error = %v, want auth_not_found", err)
	}
}

func TestAllowedAuthsFromGinContext(t *testing.T) {
	if AllowedAuthsFromContext(context.Background()) != nil {
		t.Fatal("background context must be unrestricted")
	}
	if WithAllowedAuths(context.Background(), []string{" ", ""}) != context.Background() {
		t.Fatal("empty allow-list must not restrict")
	}
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Set(AllowedAuthsGinKey, NewAllowedAuthSet([]string{"abc"}))
	derived := context.WithValue(context.Background(), "gin", ginCtx) //nolint:staticcheck // mirrors handler contexts
	if set := AllowedAuthsFromContext(derived); set == nil || !AuthAllowedBy(set, &Auth{ID: "abc"}) || AuthAllowedBy(set, &Auth{ID: "other", Index: "zzz"}) {
		t.Fatalf("gin allow-list not honored: %v", set)
	}
	var nilGin *gin.Context
	if AllowedAuthsFromContext(context.WithValue(context.Background(), "gin", nilGin)) != nil { //nolint:staticcheck
		t.Fatal("nil gin context must be unrestricted")
	}
}
