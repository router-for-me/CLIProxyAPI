package cliproxy

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/store/mongostate"
	log "github.com/sirupsen/logrus"
)

func (s *Service) initCircuitBreakerFailureStore(ctx context.Context) error {
	if s == nil || s.coreManager == nil {
		return nil
	}
	runtimeCfg, _, found, err := mongostate.LoadRuntimeConfig(s.configPath)
	if err != nil {
		return err
	}
	mongostate.ApplyEnvOverrides(&runtimeCfg)
	if !found && !runtimeCfg.Enabled {
		s.coreManager.SetCircuitBreakerFailureStore(nil)
		return nil
	}
	if !runtimeCfg.Enabled {
		s.coreManager.SetCircuitBreakerFailureStore(nil)
		return nil
	}
	if runtimeCfg.URI == "" {
		return fmt.Errorf("mongostate: enabled but uri is empty")
	}
	storeCfg := runtimeCfg.ToStoreConfig("circuit-breaker-failures")
	store, err := mongostate.NewCircuitBreakerFailureStore(
		ctx,
		storeCfg,
		mongostate.DefaultCircuitBreakerFailureStateCollection,
		mongostate.DefaultCircuitBreakerFailureEventCollection,
		mongostate.DefaultCircuitBreakerFailureEventTTLDays,
	)
	if err != nil {
		return err
	}
	s.circuitBreakerFailureStore = store
	s.coreManager.SetCircuitBreakerFailureStore(store)
	log.Info("circuit-breaker failure store initialized")
	return nil
}
