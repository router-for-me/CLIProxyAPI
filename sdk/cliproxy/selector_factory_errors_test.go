package cliproxy

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type factoryOwnedSelector struct{ stops atomic.Int32 }

func (*factoryOwnedSelector) Pick(context.Context, string, string, executor.Options, []*coreauth.Auth) (*coreauth.Auth, error) {
	return nil, errors.New("not used")
}
func (s *factoryOwnedSelector) Stop() { s.stops.Add(1) }

func factoryErrorService(factory SelectorFactory) (*Service, *factoryOwnedSelector) {
	original := &config.Config{}
	previous := &factoryOwnedSelector{}
	state := normalizedRoutingRuntimeState(original)
	return &Service{cfg: original, coreManager: coreauth.NewManager(nil, previous, nil), appliedRoutingState: &state, selectorFactory: factory}, previous
}

func TestSelectorFactoryRejectedReloadDoesNotPublishConfig(t *testing.T) {
	for _, failure := range []string{"error", "nil"} {
		t.Run(failure, func(t *testing.T) {
			service, previous := factoryErrorService(func(*config.Config) (coreauth.Selector, error) {
				if failure == "error" {
					return nil, errors.New("invalid custom policy")
				}
				return nil, nil
			})
			original := service.currentConfig()
			updated := original.CloneForRuntime()
			updated.Routing.Strategy = "fill-first"
			updated.APIKeys = []string{"replacement-key"}
			if service.applyConfigUpdateWithAuthSynthesis(context.Background(), updated, false) {
				t.Fatal("rejected factory accepted reload")
			}
			if service.currentConfig() != original {
				t.Fatal("rejected factory published configuration")
			}
			if service.configSequence != 0 {
				t.Fatalf("rejected factory advanced sequence to %d", service.configSequence)
			}
			if service.coreManager.Selector() != previous || previous.stops.Load() != 0 {
				t.Fatal("rejected factory replaced or stopped active selector")
			}
		})
	}
}

func TestSelectorFactoryConfigSnapshotIsIndependent(t *testing.T) {
	cfg := &config.Config{}
	cfg.APIKeys = []string{"original-key"}
	cfg.Routing.Strategy = "fill-first"
	selector := &factoryOwnedSelector{}
	got, err := buildRoutingSelector(cfg, func(snapshot *config.Config) (coreauth.Selector, error) {
		snapshot.APIKeys[0] = "changed-key"
		snapshot.Routing.Strategy = "round-robin"
		return selector, nil
	})
	if err != nil || got != selector {
		t.Fatalf("buildRoutingSelector: selector=%v err=%v", got, err)
	}
	if cfg.APIKeys[0] != "original-key" || cfg.Routing.Strategy != "fill-first" {
		t.Fatal("factory modified the caller's configuration")
	}
}

func TestSelectorFactoryShutdownStopsUnappliedPreparation(t *testing.T) {
	prepared := &factoryOwnedSelector{}
	service, previous := factoryErrorService(func(*config.Config) (coreauth.Selector, error) { return prepared, nil })
	updated := service.currentConfig().CloneForRuntime()
	updated.Routing.Strategy = "fill-first"
	service.commitConfigUpdate(updated)
	if err := service.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if prepared.stops.Load() != 1 || previous.stops.Load() != 1 {
		t.Fatalf("shutdown stops: prepared=%d active=%d", prepared.stops.Load(), previous.stops.Load())
	}
}

func TestSelectorFactoryCanceledCommitStopsPreparedSelector(t *testing.T) {
	prepared := &factoryOwnedSelector{}
	service, previous := factoryErrorService(func(*config.Config) (coreauth.Selector, error) { return prepared, nil })
	updated := service.currentConfig().CloneForRuntime()
	updated.Routing.Strategy = "fill-first"
	commit := service.commitConfigUpdate(updated)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if service.applyConfigRuntime(ctx, commit, false) {
		t.Fatal("canceled runtime apply succeeded")
	}
	if prepared.stops.Load() != 1 {
		t.Fatalf("prepared selector stopped %d times, want 1", prepared.stops.Load())
	}
	if service.coreManager.Selector() != previous || previous.stops.Load() != 0 {
		t.Fatal("canceled apply changed active selector")
	}
}

func TestSelectorFactorySupersededCommitStopsPreparedSelector(t *testing.T) {
	var allocated []*factoryOwnedSelector
	service, previous := factoryErrorService(func(*config.Config) (coreauth.Selector, error) {
		selector := &factoryOwnedSelector{}
		allocated = append(allocated, selector)
		return selector, nil
	})
	updated := service.currentConfig().CloneForRuntime()
	updated.Routing.Strategy = "fill-first"
	older := service.commitConfigUpdate(updated)
	newer := service.commitConfigUpdate(updated.CloneForRuntime())
	if len(allocated) != 2 {
		t.Fatalf("allocated %d selectors, want 2 pending preparations", len(allocated))
	}
	if allocated[0].stops.Load() != 1 {
		t.Fatal("superseding commit did not stop previous preparation")
	}
	if service.applyConfigRuntime(context.Background(), older, false) {
		t.Fatal("stale runtime apply succeeded")
	}
	if allocated[0].stops.Load() != 1 || allocated[1].stops.Load() != 0 {
		t.Fatal("stale cleanup stopped wrong selector or stopped twice")
	}
	if !service.applyManagerConfig(context.Background(), newer) {
		t.Fatal("newest manager apply failed")
	}
	if service.coreManager.Selector() != allocated[1] || allocated[1].stops.Load() != 0 {
		t.Fatal("prepared selector was not transferred to manager")
	}
	if previous.stops.Load() != 1 {
		t.Fatal("manager did not stop replaced selector")
	}
	service.coreManager.StopAutoRefresh()
	if allocated[1].stops.Load() != 1 {
		t.Fatal("manager did not own installed selector cleanup")
	}
}
