package cliproxy

import (
	"context"
	"reflect"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxypool"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// applyProxyPools builds the pool manager for cfg and installs the auth proxy
// pool resolver. It is a no-op when the pool configuration is unchanged.
func (s *Service) applyProxyPools(cfg *config.Config) {
	if s == nil || s.coreManager == nil {
		return
	}
	var declarations []config.ProxyPoolConfig
	if cfg != nil {
		declarations = cfg.ProxyPools
	}
	configs, errConvert := proxypool.ConfigsFromConfig(declarations)
	if errConvert != nil {
		log.WithError(errConvert).Warn("failed to apply proxy pools configuration")
		return
	}

	s.proxyPoolMu.Lock()
	if reflect.DeepEqual(s.appliedProxyPools, configs) && (s.proxyPoolManager != nil || len(configs) == 0) {
		s.proxyPoolMu.Unlock()
		return
	}
	manager, errNew := proxypool.NewManager(configs)
	if errNew != nil {
		s.proxyPoolMu.Unlock()
		log.WithError(errNew).Warn("failed to create proxy pool manager")
		return
	}
	if manager != nil {
		manager.Start(context.Background())
	}
	previous := s.proxyPoolManager
	s.proxyPoolManager = manager
	s.appliedProxyPools = configs
	s.coreManager.SetProxyPoolResolver(s)
	s.proxyPoolMu.Unlock()

	if previous != nil {
		previous.Close()
	}
	if manager != nil {
		s.refreshProxyPoolBindings(context.Background())
	}
}

// closeProxyPools stops probe goroutines and clears the resolver state.
func (s *Service) closeProxyPools() {
	if s == nil {
		return
	}
	s.proxyPoolMu.Lock()
	manager := s.proxyPoolManager
	s.proxyPoolManager = nil
	s.appliedProxyPools = nil
	s.proxyPoolMu.Unlock()
	if manager != nil {
		manager.Close()
	}
}

// ResolveAuthProxy implements coreauth.ProxyPoolResolver.
func (s *Service) ResolveAuthProxy(_ context.Context, auth *coreauth.Auth) (string, string, error) {
	if s == nil || auth == nil {
		return "", "", nil
	}
	poolID := coreauth.ProxyPoolID(auth)
	if poolID == "" {
		return "", "", nil
	}
	s.proxyPoolMu.Lock()
	manager := s.proxyPoolManager
	s.proxyPoolMu.Unlock()
	if manager == nil {
		return "", "", nil
	}
	node, errResolve := manager.ResolveNode(auth.Provider, auth.ID, poolID, coreauth.ProxyPoolNode(auth))
	if errResolve != nil {
		return "", "", errResolve
	}
	return node, node, nil
}

// ResolvePoolNode implements coreauth.ProxyPoolResolver for OAuth login
// sessions that reference a pool before the auth record exists.
func (s *Service) ResolvePoolNode(_ context.Context, provider, authID, poolID string) (string, string, error) {
	if s == nil {
		return "", "", nil
	}
	s.proxyPoolMu.Lock()
	manager := s.proxyPoolManager
	s.proxyPoolMu.Unlock()
	if manager == nil {
		return "", "", nil
	}
	node, errResolve := manager.Resolve(provider, authID, poolID)
	if errResolve != nil {
		return "", "", errResolve
	}
	return node, node, nil
}

// proxyPoolStrategy returns the strategy of poolID, or an empty strategy when
// the pool is not currently configured.
func (s *Service) proxyPoolStrategy(poolID string) proxypool.Strategy {
	if s == nil || strings.TrimSpace(poolID) == "" {
		return ""
	}
	s.proxyPoolMu.Lock()
	manager := s.proxyPoolManager
	s.proxyPoolMu.Unlock()
	if manager == nil {
		return ""
	}
	strategy, errStrategy := manager.Strategy(poolID)
	if errStrategy != nil {
		return ""
	}
	return strategy
}

// persistProxyPoolBinding writes a newly assigned sticky node back to the auth
// record's backing store. Watcher-driven registrations run with persistence
// disabled, so the sticky binding needs an explicit save.
func (s *Service) persistProxyPoolBinding(ctx context.Context, auth *coreauth.Auth, priorNode string) {
	if s == nil || s.coreManager == nil || auth == nil {
		return
	}
	if coreauth.ProxyPoolID(auth) == "" || strings.TrimSpace(auth.ProxyURL) == "" {
		return
	}
	node := coreauth.ProxyPoolNode(auth)
	prior := strings.TrimSpace(priorNode)
	if node == "" || node == prior {
		return
	}
	// Round-robin assignments rotate on every re-registration, so only the
	// first assignment is written. Later rotations stay in memory to avoid a
	// write-back loop with the auth file watcher.
	if prior != "" && s.proxyPoolStrategy(coreauth.ProxyPoolID(auth)) != proxypool.StrategySticky {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	stored, preempted := s.coreManager.PersistMetadataEntry(ctx, auth.ID, auth.RegistrationEpoch, coreauth.MetadataProxyPoolNode, node, nil)
	if !stored && !preempted {
		log.WithFields(log.Fields{
			"auth_id":    auth.ID,
			"provider":   auth.Provider,
			"proxy_pool": coreauth.ProxyPoolID(auth),
			"node":       proxyutil.Redact(node),
		}).Debug("proxy pool binding was not persisted")
	}
}

// refreshProxyPoolBindings re-resolves existing accounts after the pool
// configuration changed. Explicit auth proxies are never overwritten.
func (s *Service) refreshProxyPoolBindings(ctx context.Context) {
	if s == nil || s.coreManager == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.proxyPoolMu.Lock()
	manager := s.proxyPoolManager
	s.proxyPoolMu.Unlock()
	if manager == nil {
		return
	}
	for _, auth := range s.coreManager.List() {
		if auth == nil {
			continue
		}
		poolID := coreauth.ProxyPoolID(auth)
		if poolID == "" {
			continue
		}
		strategy, errStrategy := manager.Strategy(poolID)
		if errStrategy != nil {
			continue
		}
		sticky := strategy == proxypool.StrategySticky
		currentProxy := strings.TrimSpace(auth.ProxyURL)
		boundNode := coreauth.ProxyPoolNode(auth)
		// A proxy that does not match the pool-assigned node is an explicit
		// operator setting and must never be overwritten.
		if currentProxy != "" && currentProxy != boundNode {
			continue
		}
		node, errResolve := manager.ResolveNode(auth.Provider, auth.ID, poolID, boundNode)
		if errResolve != nil {
			log.WithFields(log.Fields{
				"auth_id":    auth.ID,
				"provider":   auth.Provider,
				"proxy_pool": poolID,
			}).Warnf("failed to re-resolve proxy pool binding: %v", errResolve)
			continue
		}
		if node == strings.TrimSpace(auth.ProxyURL) && (!sticky || node == coreauth.ProxyPoolNode(auth)) {
			continue
		}
		if _, _, errModify := s.coreManager.Modify(ctx, auth.ID, auth.RegistrationEpoch, func(current *coreauth.Auth) error {
			current.ProxyURL = node
			if current.Metadata == nil {
				current.Metadata = make(map[string]any)
			}
			current.Metadata[coreauth.MetadataProxyPoolNode] = node
			return nil
		}); errModify != nil {
			log.WithFields(log.Fields{
				"auth_id":    auth.ID,
				"provider":   auth.Provider,
				"proxy_pool": poolID,
			}).Warnf("failed to update proxy pool binding: %v", errModify)
		}
	}
}
