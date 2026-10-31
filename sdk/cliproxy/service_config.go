package cliproxy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
)

func (s *Service) currentConfig() *config.Config {
	if s == nil {
		return nil
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	return cfg
}

func (s *Service) currentWatcher() *WatcherWrapper {
	if s == nil {
		return nil
	}
	s.cfgMu.RLock()
	w := s.watcher
	s.cfgMu.RUnlock()
	return w
}

func (s *Service) setWatcher(w *WatcherWrapper) {
	if s == nil {
		return
	}
	s.cfgMu.Lock()
	s.watcher = w
	s.cfgMu.Unlock()
}

func (s *Service) applyConfigUpdate(newCfg *config.Config) {
	s.applyConfigUpdateWithAuthSynthesis(context.Background(), newCfg, true)
}

func (s *Service) applyWatcherConfigUpdate(newCfg *config.Config) {
	s.applyConfigUpdateWithAuthSynthesis(context.Background(), newCfg, false)
}

type configCommit struct {
	cfg              *config.Config
	sequence         uint64
	preparedSelector *preparedRoutingSelector
}

type preparedRoutingSelector struct {
	selector coreauth.Selector
}

func (p *preparedRoutingSelector) stop() {
	if p == nil || p.selector == nil {
		return
	}
	selector := p.selector
	p.selector = nil
	if stoppable, ok := selector.(coreauth.StoppableSelector); ok {
		stoppable.Stop()
	}
}

// Called with configRuntimeMu held, including during shutdown.
func (s *Service) stopPendingRoutingSelector() {
	s.pendingRoutingSelector.stop()
	s.pendingRoutingSelector = nil
}

type routingRuntimeState struct {
	strategy                 string
	sessionAffinity          bool
	sessionAffinityTTL       time.Duration
	sessionAffinitySubagents bool
	sessionAffinityRebindOn  string
}

func normalizedRoutingRuntimeState(cfg *config.Config) routingRuntimeState {
	state := routingRuntimeState{
		strategy:                 "round-robin",
		sessionAffinityTTL:       time.Hour,
		sessionAffinitySubagents: true,
		sessionAffinityRebindOn:  coreauth.SessionAffinityRebindOnAny,
	}
	if cfg == nil {
		return state
	}

	switch strings.ToLower(strings.TrimSpace(cfg.Routing.Strategy)) {
	case "weighted-round-robin", "weightedroundrobin", "wrr":
		state.strategy = "weighted-round-robin"
	case "fill-first", "fillfirst", "ff":
		state.strategy = "fill-first"
	}
	state.sessionAffinity = cfg.Routing.SessionAffinity
	if ttl := strings.TrimSpace(cfg.Routing.SessionAffinityTTL); ttl != "" {
		if parsed, errParse := time.ParseDuration(ttl); errParse == nil && parsed > 0 {
			if parsed < time.Second {
				parsed = time.Second
			}
			state.sessionAffinityTTL = parsed
		}
	}
	if state.sessionAffinity && cfg.Routing.SessionAffinitySubagents != nil {
		state.sessionAffinitySubagents = *cfg.Routing.SessionAffinitySubagents
	}
	if state.sessionAffinity && strings.EqualFold(strings.TrimSpace(cfg.Routing.SessionAffinityRebindOn), coreauth.SessionAffinityRebindOnQuota) {
		state.sessionAffinityRebindOn = coreauth.SessionAffinityRebindOnQuota
	}
	return state
}

// NewRoutingSelector applies native routing and session-affinity settings. A
// non-nil fallback replaces only the built-in account policy beneath affinity.
func NewRoutingSelector(cfg *config.Config, fallback coreauth.Selector) coreauth.Selector {
	return routingSelector(normalizedRoutingRuntimeState(cfg), fallback)
}

func newRoutingSelector(state routingRuntimeState) coreauth.Selector {
	return routingSelector(state, nil)
}

func buildRoutingSelector(cfg *config.Config, factory SelectorFactory) (coreauth.Selector, error) {
	if factory == nil {
		return NewRoutingSelector(cfg, nil), nil
	}
	selector, err := factory(cfg.CloneForRuntime())
	if err != nil {
		return nil, fmt.Errorf("cliproxy: construct routing selector: %w", err)
	}
	if selector == nil {
		return nil, fmt.Errorf("cliproxy: selector factory returned nil")
	}
	return selector, nil
}

func routingSelector(state routingRuntimeState, selector coreauth.Selector) coreauth.Selector {
	if selector == nil {
		switch state.strategy {
		case "weighted-round-robin":
			selector = &coreauth.WeightedRoundRobinSelector{}
		case "fill-first":
			selector = &coreauth.FillFirstSelector{}
		default:
			selector = &coreauth.RoundRobinSelector{}
		}
	}
	if state.sessionAffinity {
		subagents := state.sessionAffinitySubagents
		selector = coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{
			Fallback:         selector,
			TTL:              state.sessionAffinityTTL,
			SubagentAffinity: &subagents,
			RebindOn:         state.sessionAffinityRebindOn,
		})
	}
	return selector
}

func (s *Service) applyConfigUpdateWithAuthSynthesis(ctx context.Context, newCfg *config.Config, synthesizeConfigAuths bool) bool {
	commit := s.commitConfigUpdate(newCfg)
	if commit.cfg == nil {
		return false
	}
	return s.applyConfigRuntime(ctx, commit, synthesizeConfigAuths)
}

// commitConfigUpdate validates custom selectors before publishing configuration.
// Other runtime work remains deferred. Catalog source generations change here so
// an older commit cannot restart stale readers.
func (s *Service) commitConfigUpdate(newCfg *config.Config) configCommit {
	if s == nil {
		return configCommit{}
	}
	if s.selectorFactory != nil {
		// Match applyConfigRuntime's lock order while inspecting applied routing.
		s.configRuntimeMu.Lock()
		defer s.configRuntimeMu.Unlock()
	}

	s.configUpdateMu.Lock()
	defer s.configUpdateMu.Unlock()

	if newCfg == nil {
		s.cfgMu.RLock()
		newCfg = s.cfg
		s.cfgMu.RUnlock()
	}
	if newCfg == nil {
		return configCommit{}
	}
	if errValidate := newCfg.ValidateCredentialWeights(); errValidate != nil {
		log.WithError(errValidate).Warn("rejected config update with invalid credential weights")
		return configCommit{}
	}

	if errValidate := newCfg.Models.Validate(); errValidate != nil {
		log.WithError(errValidate).Warn("rejected invalid model catalog sources")
		return configCommit{}
	}
	var prepared *preparedRoutingSelector
	if s.selectorFactory != nil && s.coreManager != nil {
		routingState := normalizedRoutingRuntimeState(newCfg)
		if s.appliedRoutingState == nil || *s.appliedRoutingState != routingState {
			selector, errSelector := buildRoutingSelector(newCfg, s.selectorFactory)
			if errSelector != nil {
				log.WithError(errSelector).Error("rejected config update with invalid routing selector")
				return configCommit{}
			}
			prepared = &preparedRoutingSelector{selector: selector}
		}
		s.stopPendingRoutingSelector()
		s.pendingRoutingSelector = prepared
	}
	s.cfgMu.Lock()
	s.cfg = newCfg
	s.cfgMu.Unlock()
	s.cancelStaleAntigravityProbes("")
	s.configSequence++
	registry.UpdateModelCatalogSources(newCfg.Models, newCfg.Home.Enabled)
	return configCommit{cfg: newCfg, sequence: s.configSequence, preparedSelector: prepared}
}

func (s *Service) configCommitCurrent(commit configCommit) bool {
	if s == nil || commit.sequence == 0 {
		return false
	}
	s.configUpdateMu.Lock()
	current := s.configSequence == commit.sequence
	s.configUpdateMu.Unlock()
	return current
}

func (s *Service) applyConfigRuntime(ctx context.Context, commit configCommit, synthesizeConfigAuths bool) bool {
	cfg := commit.cfg
	if s == nil || cfg == nil {
		return false
	}
	s.configRuntimeMu.Lock()
	defer s.configRuntimeMu.Unlock()
	defer commit.preparedSelector.stop()
	if !s.configCommitCurrent(commit) {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}

	if !s.applyManagerConfig(ctx, commit) {
		return false
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	if !s.applyPprofConfigContext(ctx, cfg) {
		return false
	}
	s.applyDiscoveryConfigContext(ctx, cfg)
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	if !s.updateServerClientsContext(ctx, cfg) {
		return false
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}

	registrationCtx := coreauth.WithSkipPersist(ctx)
	s.syncPluginRuntimeConfigForConfig(registrationCtx, cfg)
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	var auths []*coreauth.Auth
	if s.coreManager != nil {
		auths = s.coreManager.List()
	}
	s.registerAvailableExecutors(registrationCtx, executorRegistrationOptions{
		includeBaseline:   cfg.Home.Enabled,
		forceReplaceAuths: true,
		auths:             auths,
	})
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	if synthesizeConfigAuths {
		s.registerConfigAPIKeyAuths(registrationCtx, cfg)
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	if s.coreManager != nil && !cfg.Home.Enabled && cfg.SaveCooldownStatus {
		if errRestoreCooldown := s.coreManager.RestoreCooldownStates(registrationCtx); errRestoreCooldown != nil && ctx.Err() == nil {
			log.Warnf("failed to restore cooldown state after config update: %v", errRestoreCooldown)
		}
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	s.syncPluginModelRuntime(registrationCtx)
	return ctx.Err() == nil
}

func (s *Service) applyManagerConfig(ctx context.Context, commit configCommit) bool {
	if s == nil || s.coreManager == nil || commit.cfg == nil {
		return s != nil && commit.cfg != nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	routingState := normalizedRoutingRuntimeState(commit.cfg)
	if s.appliedRoutingState == nil || *s.appliedRoutingState != routingState {
		var selector coreauth.Selector
		if commit.preparedSelector != nil {
			selector = commit.preparedSelector.selector
			if selector == nil {
				return false
			}
		} else {
			var errSelector error
			selector, errSelector = buildRoutingSelector(commit.cfg, s.selectorFactory)
			if errSelector != nil {
				log.WithError(errSelector).Error("routing config application failed; retaining previous selector")
				return false
			}
		}
		s.coreManager.SetSelector(selector)
		if commit.preparedSelector != nil {
			commit.preparedSelector.selector = nil
			if s.pendingRoutingSelector == commit.preparedSelector {
				s.pendingRoutingSelector = nil
			}
		}
		s.appliedRoutingState = &routingState
	}
	s.applyRetryConfig(commit.cfg)
	store := s.resolveCooldownStateStore(commit.cfg)
	if !s.coreManager.ApplyConfigWithCooldownStateStore(ctx, commit.cfg, store) {
		return false
	}
	s.coreManager.SetOAuthModelAlias(commit.cfg.OAuthModelAlias)
	return true
}

func (s *Service) updateServerClientsContext(ctx context.Context, cfg *config.Config) bool {
	if s == nil || cfg == nil || (ctx != nil && ctx.Err() != nil) {
		return false
	}
	if s.updateServerClientsContextFn != nil {
		return s.updateServerClientsContextFn(ctx, cfg)
	}
	if s.server == nil {
		return true
	}
	return s.server.UpdateClientsContext(ctx, cfg)
}

func (s *Service) reloadConfigFromWatcher() bool {
	w := s.currentWatcher()
	if w == nil {
		return false
	}
	return w.ReloadConfigIfChanged()
}

func (s *Service) registerConfigAPIKeyAuths(ctx context.Context, cfg *config.Config) {
	if s == nil || s.coreManager == nil || cfg == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	configSynth := synthesizer.NewConfigSynthesizer()
	auths, errSynthesize := configSynth.Synthesize(&synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errSynthesize != nil {
		log.Warnf("failed to synthesize config API key auths: %v", errSynthesize)
		return
	}

	registrationCtx := coreauth.WithDeferredAPIKeyModelAliasRebuild(ctx)
	tasks := make([]modelRegistrationTask, 0, len(auths))
	needsAliasRebuild := false
	for _, auth := range auths {
		if !coreauth.IsConfigAPIKeyAuth(auth) {
			continue
		}
		prepared := s.prepareCoreAuthForModelRegistration(registrationCtx, auth)
		if prepared == nil {
			continue
		}
		needsAliasRebuild = true
		authForRegistration := prepared
		tasks = append(tasks, modelRegistrationTask{
			phase:    modelRegistrationPhaseConfigAPIKey,
			category: modelRegistrationCategory(authForRegistration),
			run: func(compatCache *openAICompatibilityRegistrationCache) {
				s.completeModelRegistrationForAuthWithCache(registrationCtx, authForRegistration, compatCache)
			},
		})
	}
	if needsAliasRebuild {
		s.coreManager.RefreshAPIKeyModelAlias()
	}
	s.runModelRegistrationTasks(registrationCtx, tasks)
}

func forceHomeRuntimeConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	cfg.APIKeys = nil
	cfg.UsageStatisticsEnabled = true
	cfg.DisableCooling = true
	cfg.SaveCooldownStatus = false
	cfg.WebsocketAuth = true
	cfg.RemoteManagement.AllowRemote = false
	cfg.RemoteManagement.DisableControlPanel = true
	cfg.Plugins.StoreAuth = nil
}
