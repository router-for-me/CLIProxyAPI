package cliproxy

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/store/mongostate"
	log "github.com/sirupsen/logrus"
)

func (s *Service) initErrorEventStore(ctx context.Context) error {
	if s == nil {
		mongostate.SetGlobalErrorEventStore(nil)
		return nil
	}
	runtimeCfg, _, found, err := mongostate.LoadRuntimeConfig(s.configPath)
	if err != nil {
		mongostate.SetGlobalErrorEventStore(nil)
		log.Warnf("error event store disabled: load state-store config failed: %v", err)
		return nil
	}
	mongostate.ApplyEnvOverrides(&runtimeCfg)
	if !found && !runtimeCfg.Enabled {
		mongostate.SetGlobalErrorEventStore(nil)
		return nil
	}
	if !runtimeCfg.Enabled {
		mongostate.SetGlobalErrorEventStore(nil)
		return nil
	}
	if runtimeCfg.URI == "" {
		mongostate.SetGlobalErrorEventStore(nil)
		log.Warn("error event store disabled: state-store enabled but uri is empty")
		return nil
	}
	storeCfg := runtimeCfg.ToStoreConfig("error-events")
	store, err := mongostate.NewErrorEventStore(
		ctx,
		storeCfg,
		mongostate.DefaultErrorEventCollection,
		mongostate.DefaultErrorEventTTLDays,
	)
	if err != nil {
		mongostate.SetGlobalErrorEventStore(nil)
		log.Warnf("error event store disabled: initialize Mongo store failed: %v", err)
		return nil
	}
	if s.errorEventStore != nil {
		_ = s.errorEventStore.Close(context.Background())
	}
	s.errorEventStore = store
	mongostate.SetGlobalErrorEventStore(store)
	log.Info("error event store initialized")
	return nil
}
