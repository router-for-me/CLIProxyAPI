package cliproxy

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type devinFetchTestExecutor struct {
	mockBatchTestExecutor
	calls *atomic.Int32
	fetch func() ([]*ModelInfo, error)
}

func (e devinFetchTestExecutor) FetchModels(context.Context, *coreauth.Auth) ([]*ModelInfo, error) {
	e.calls.Add(1)
	return e.fetch()
}

func newDevinModelTestService(t *testing.T, fetch func() ([]*ModelInfo, error)) (*Service, *coreauth.Auth, *atomic.Int32) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	calls := &atomic.Int32{}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	registerDevinFetchTestExecutor(manager, calls, fetch)
	svc := &Service{cfg: &config.Config{}, coreManager: manager, antigravityContext: ctx}
	auth := &coreauth.Auth{
		ID:         "auth-devin-dynamic-" + t.Name(),
		Provider:   "devin",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth", "api_key": "key-1", "excluded_models": "devin/dynamic-excluded"},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	t.Cleanup(func() {
		cancel()
		svc.devinModelWg.Wait()
		GlobalModelRegistry().UnregisterClient(auth.ID)
	})
	return svc, auth, calls
}

// registerDevinFetchTestExecutor (re)installs the fake; re-registration rebinds the real executor.
func registerDevinFetchTestExecutor(manager *coreauth.Manager, calls *atomic.Int32, fetch func() ([]*ModelInfo, error)) {
	manager.RegisterExecutor(devinFetchTestExecutor{mockBatchTestExecutor: mockBatchTestExecutor{provider: "devin"}, calls: calls, fetch: fetch})
}

func registeredModelIDs(authID string) map[string]bool {
	ids := make(map[string]bool)
	for _, m := range GlobalModelRegistry().GetModelsForClient(authID) {
		if m != nil {
			ids[m.ID] = true
		}
	}
	return ids
}

func TestRegisterModelsForAuth_DevinUsesUpstreamModels(t *testing.T) {
	upstream := internalregistry.BuildDevinModelsFromUpstream([]internalregistry.DevinUpstreamModel{
		{UID: "dynamic-only-model-high", Label: "Dynamic Only High"},
		{UID: "dynamic-excluded", Label: "Dynamic Excluded"},
	})
	svc, auth, calls := newDevinModelTestService(t, func() ([]*ModelInfo, error) { return upstream, nil })

	svc.registerModelsForAuth(context.Background(), auth)
	svc.devinModelWg.Wait()

	ids := registeredModelIDs(auth.ID)
	if !ids["devin/dynamic-only-model"] {
		t.Fatalf("upstream model not registered: %v", ids)
	}
	if ids["devin/dynamic-excluded"] {
		t.Errorf("excluded model registered: %v", ids)
	}
	if ids["devin/swe-2"] {
		t.Errorf("catalog-only model still registered after fetch: %v", ids)
	}
	if got := internalregistry.LookupDevinModel("dynamic-only-model"); got == nil || got.Thinking == nil {
		t.Errorf("upstream model not visible to UID resolution: %+v", got)
	}

	// A fresh cache must not trigger another fetch on re-registration.
	svc.registerModelsForAuth(context.Background(), auth)
	svc.devinModelWg.Wait()
	if n := calls.Load(); n != 1 {
		t.Errorf("fetch calls = %d, want 1", n)
	}
}

func TestRegisterModelsForAuth_DevinFetchFailureKeepsCatalogAndBacksOff(t *testing.T) {
	svc, auth, calls := newDevinModelTestService(t, func() ([]*ModelInfo, error) { return nil, errors.New("boom") })

	svc.registerModelsForAuth(context.Background(), auth)
	svc.devinModelWg.Wait()
	svc.registerModelsForAuth(context.Background(), auth)
	svc.devinModelWg.Wait()

	if n := calls.Load(); n != 1 {
		t.Errorf("fetch calls = %d, want 1 during backoff", n)
	}
	ids := registeredModelIDs(auth.ID)
	for _, m := range internalregistry.GetDevinModels() {
		if !ids[m.ID] {
			t.Fatalf("catalog model %s missing after failed fetch: %v", m.ID, ids)
		}
	}
	svc.devinModelMu.Lock()
	entry := svc.devinModelCache[auth.ID]
	svc.devinModelMu.Unlock()
	if entry == nil || entry.failures != 1 || !entry.nextRetry.After(time.Now()) {
		t.Errorf("unexpected cache entry: %+v", entry)
	}
}

func TestDevinModelCredentialChangeRefetches(t *testing.T) {
	upstream := internalregistry.BuildDevinModelsFromUpstream([]internalregistry.DevinUpstreamModel{{UID: "credential-model"}})
	fetch := func() ([]*ModelInfo, error) { return upstream, nil }
	svc, auth, calls := newDevinModelTestService(t, fetch)

	svc.registerModelsForAuth(context.Background(), auth)
	svc.devinModelWg.Wait()
	registerDevinFetchTestExecutor(svc.coreManager, calls, fetch)

	rotated := auth.Clone()
	rotated.Attributes["api_key"] = "key-2"
	if _, errUpdate := svc.coreManager.Update(context.Background(), rotated); errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}
	svc.registerModelsForAuth(context.Background(), rotated)
	svc.devinModelWg.Wait()
	if n := calls.Load(); n != 2 {
		t.Errorf("fetch calls = %d, want 2 after credential change", n)
	}
}

func TestDevinModelRetryDelay(t *testing.T) {
	for failures, want := range map[int]time.Duration{1: 2 * time.Minute, 2: 4 * time.Minute, 4: 16 * time.Minute, 5: 30 * time.Minute, 20: 30 * time.Minute} {
		if got := devinModelRetryDelay(failures); got != want {
			t.Errorf("devinModelRetryDelay(%d) = %s, want %s", failures, got, want)
		}
	}
}
