package cliproxy

import (
	"context"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/wsrelay"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	log "github.com/sirupsen/logrus"
)

// newDefaultAuthManager creates a default authentication manager with supported OAuth providers.
func newDefaultAuthManager() *sdkAuth.Manager {
	return sdkAuth.NewManager(
		sdkAuth.GetTokenStore(),
		sdkAuth.NewCodexAuthenticator(),
		sdkAuth.NewClaudeAuthenticator(),
		sdkAuth.NewXAIAuthenticator(),
		sdkAuth.NewDevinAuthenticator(),
		sdkAuth.NewMetaAuthenticator(),
	)
}

func (s *Service) ensureAuthUpdateQueue(ctx context.Context) {
	if s == nil {
		return
	}
	if s.authUpdates == nil {
		s.authUpdates = make(chan watcher.AuthUpdate, 256)
	}
	if s.authQueueStop != nil {
		return
	}
	queueCtx, cancel := context.WithCancel(ctx)
	s.authQueueStop = cancel
	go s.consumeAuthUpdates(queueCtx)
}

func (s *Service) consumeAuthUpdates(ctx context.Context) {
	ctx = coreauth.WithSkipPersist(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case update, ok := <-s.authUpdates:
			if !ok {
				return
			}
			updates := []watcher.AuthUpdate{update}
		labelDrain:
			for {
				select {
				case nextUpdate := <-s.authUpdates:
					updates = append(updates, nextUpdate)
				default:
					break labelDrain
				}
			}
			s.handleAuthUpdates(ctx, updates)
		}
	}
}

func (s *Service) emitAuthUpdate(ctx context.Context, update watcher.AuthUpdate) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.watcher != nil && s.watcher.DispatchRuntimeAuthUpdate(update) {
		return
	}
	if s.authUpdates != nil {
		select {
		case s.authUpdates <- update:
			return
		default:
			log.Debugf("auth update queue saturated, applying inline action=%v id=%s", update.Action, update.ID)
		}
	}
	s.handleAuthUpdate(ctx, update)
}

func (s *Service) handleAuthUpdate(ctx context.Context, update watcher.AuthUpdate) {
	s.handleAuthUpdates(ctx, []watcher.AuthUpdate{update})
}

func (s *Service) handleAuthUpdates(ctx context.Context, updates []watcher.AuthUpdate) {
	if s == nil {
		return
	}
	// Keep this path limited to the targeted auths. Global plugin rebuilds and
	// in-flight Antigravity probes can hold authUpdateMu for minutes and stall
	// Management API PATCH responses.
	s.authUpdateMu.Lock()
	locked := true
	defer func() {
		if locked {
			s.authUpdateMu.Unlock()
		}
	}()

	if s.authRevisions == nil {
		s.authRevisions = make(map[string]uint64)
	}

	filtered := make([]watcher.AuthUpdate, 0, len(updates))
	skippedWaits := make([]chan struct{}, 0)
	startedRegs := make([]authRegistrationWait, 0)
	startedWaitByID := make(map[string]authRegistrationWait)
	for _, update := range updates {
		id := authUpdateID(update)
		if id == "" {
			filtered = append(filtered, update)
			continue
		}
		rev := update.Revision()
		if rev > 0 {
			if prevRev, exists := s.authRevisions[id]; exists && rev <= prevRev {
				log.Debugf("skipping stale auth update for %s: rev %d <= processed %d", id, rev, prevRev)
				if ch := s.authRegistrationWaitCh(id); ch != nil {
					skippedWaits = append(skippedWaits, ch)
				}
				continue
			}
			s.authRevisions[id] = rev
		}
		wait := s.beginAuthRegistration(id)
		startedRegs = append(startedRegs, wait)
		startedWaitByID[id] = wait
		filtered = append(filtered, update)
	}
	registrationsFinished := false
	defer func() {
		if !registrationsFinished {
			finishAuthRegistrations(s, startedRegs)
		}
	}()
	if len(filtered) == 0 {
		locked = false
		s.authUpdateMu.Unlock()
		waitAuthRegistrations(skippedWaits)
		return
	}
	updates = coalesceAuthUpdates(filtered)
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	if cfg == nil || s.coreManager == nil {
		locked = false
		s.authUpdateMu.Unlock()
		waitAuthRegistrations(skippedWaits)
		return
	}

	registrationCtx := coreauth.WithDeferredAPIKeyModelAliasRebuild(ctx)
	tasks := make([]modelRegistrationTask, 0, len(updates))
	needsAliasRebuild := false
	for _, update := range updates {
		switch update.Action {
		case watcher.AuthUpdateActionAdd, watcher.AuthUpdateActionModify:
			if update.Auth == nil || update.Auth.ID == "" {
				continue
			}
			auth := s.prepareCoreAuthForModelRegistration(registrationCtx, update.Auth)
			if auth == nil {
				continue
			}
			needsAliasRebuild = true
			authForRegistration := auth
			expectedGeneration := authForRegistration.Generation
			expectedDisabled := authForRegistration.Disabled
			authID := authForRegistration.ID
			wait := startedWaitByID[authID]
			tasks = append(tasks, modelRegistrationTask{
				phase:    modelRegistrationPhase(authForRegistration),
				category: modelRegistrationCategory(authForRegistration),
				run: func(compatCache *openAICompatibilityRegistrationCache) {
					if s.shouldSkipModelRegistration(authID, expectedGeneration, expectedDisabled) {
						return
					}
					s.completeModelRegistrationForAuthWithCache(registrationCtx, authForRegistration, compatCache)
				},
				done: func() {
					if wait.ch != nil {
						finishAuthRegistrations(s, []authRegistrationWait{wait})
					}
				},
			})
		case watcher.AuthUpdateActionDelete:
			id := update.ID
			if id == "" && update.Auth != nil {
				id = update.Auth.ID
			}
			if id == "" {
				continue
			}
			if existing, ok := s.coreManager.GetByID(id); ok && existing != nil && update.Auth != nil {
				if isStaleCoreAuth(existing, update.Auth) {
					log.Debugf("skipping stale auth delete for %s: incoming gen=%d, existing gen=%d", id, update.Auth.Generation, existing.Generation)
					continue
				}
			}
			s.applyCoreAuthRemoval(registrationCtx, id)
			needsAliasRebuild = true
		default:
			log.Debugf("received unknown auth update action: %v", update.Action)
		}
	}

	if needsAliasRebuild {
		s.coreManager.RefreshAPIKeyModelAlias()
	}
	locked = false
	s.authUpdateMu.Unlock()
	s.runModelRegistrationTasks(registrationCtx, tasks)
	finishAuthRegistrations(s, startedRegs)
	registrationsFinished = true
	waitAuthRegistrations(skippedWaits)
}

func coalesceAuthUpdates(updates []watcher.AuthUpdate) []watcher.AuthUpdate {
	if len(updates) <= 1 {
		return updates
	}
	order := make([]string, 0, len(updates))
	byID := make(map[string]watcher.AuthUpdate, len(updates))
	unkeyed := make([]watcher.AuthUpdate, 0)
	for _, update := range updates {
		id := authUpdateID(update)
		if id == "" {
			unkeyed = append(unkeyed, update)
			continue
		}
		if _, exists := byID[id]; !exists {
			order = append(order, id)
		}
		byID[id] = update
	}
	if len(byID) == 0 {
		return unkeyed
	}
	out := make([]watcher.AuthUpdate, 0, len(byID)+len(unkeyed))
	for _, id := range order {
		out = append(out, byID[id])
	}
	out = append(out, unkeyed...)
	return out
}

func authUpdateID(update watcher.AuthUpdate) string {
	if strings.TrimSpace(update.ID) != "" {
		return strings.TrimSpace(update.ID)
	}
	if update.Auth != nil {
		return strings.TrimSpace(update.Auth.ID)
	}
	return ""
}

func (s *Service) ensureWebsocketGateway() {
	if s == nil {
		return
	}
	if s.wsGateway != nil {
		return
	}
	opts := wsrelay.Options{
		Path:           "/v1/ws",
		OnConnected:    s.wsOnConnected,
		OnDisconnected: s.wsOnDisconnected,
		LogDebugf:      log.Debugf,
		LogInfof:       log.Infof,
		LogWarnf:       log.Warnf,
	}
	s.wsGateway = wsrelay.NewManager(opts)
}

func (s *Service) wsOnConnected(channelID string) {
	if s == nil || channelID == "" {
		return
	}
	if !strings.HasPrefix(strings.ToLower(channelID), "aistudio-") {
		return
	}
	if s.coreManager != nil {
		if existing, ok := s.coreManager.GetByID(channelID); ok && existing != nil {
			if !existing.Disabled && existing.Status == coreauth.StatusActive {
				return
			}
		}
	}
	now := time.Now().UTC()
	auth := &coreauth.Auth{
		ID:         channelID,  // keep channel identifier as ID
		Provider:   "aistudio", // logical provider for switch routing
		Label:      channelID,  // display original channel id
		Status:     coreauth.StatusActive,
		CreatedAt:  now,
		UpdatedAt:  now,
		Attributes: map[string]string{"runtime_only": "true"},
		Metadata:   map[string]any{"email": channelID}, // metadata drives logging and usage tracking
	}
	log.Infof("websocket provider connected: %s", channelID)
	s.emitAuthUpdate(context.Background(), watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionAdd,
		ID:     auth.ID,
		Auth:   auth,
	})
}

func (s *Service) wsOnDisconnected(channelID string, reason error) {
	if s == nil || channelID == "" {
		return
	}
	if reason != nil {
		if strings.Contains(reason.Error(), "replaced by new connection") {
			log.Infof("websocket provider replaced: %s", channelID)
			return
		}
		log.Warnf("websocket provider disconnected: %s (%v)", channelID, reason)
	} else {
		log.Infof("websocket provider disconnected: %s", channelID)
	}
	ctx := context.Background()
	s.emitAuthUpdate(ctx, watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionDelete,
		ID:     channelID,
	})
}

func (s *Service) applyCoreAuthAddOrUpdate(ctx context.Context, auth *coreauth.Auth) {
	auth = s.prepareCoreAuthForModelRegistration(ctx, auth)
	if auth == nil {
		return
	}
	s.completeModelRegistrationForAuth(ctx, auth)
}

func (s *Service) prepareCoreAuthForModelRegistration(ctx context.Context, auth *coreauth.Auth) *coreauth.Auth {
	if s == nil || s.coreManager == nil || auth == nil || auth.ID == "" {
		return nil
	}
	auth = auth.Clone()
	s.ensureExecutorsForAuthWithContext(ctx, auth, false)

	// IMPORTANT: Update coreManager FIRST, before model registration.
	// This ensures that configuration changes (proxy_url, prefix, etc.) take effect
	// immediately for API calls, rather than waiting for model registration to complete.
	op := "register"
	var err error
	if existing, ok := s.coreManager.GetByID(auth.ID); ok {
		if isStaleCoreAuth(existing, auth) {
			log.Debugf("skipping stale auth update for %s: incoming gen=%d, existing gen=%d", auth.ID, auth.Generation, existing.Generation)
			return existing
		}
		auth.CreatedAt = existing.CreatedAt
		if !existing.Disabled && existing.Status != coreauth.StatusDisabled && !auth.Disabled && auth.Status != coreauth.StatusDisabled {
			auth.LastRefreshedAt = existing.LastRefreshedAt
			auth.NextRefreshAfter = existing.NextRefreshAfter
			if len(auth.ModelStates) == 0 && len(existing.ModelStates) > 0 {
				auth.ModelStates = existing.ModelStates
			}
		}
		op = "update"
		_, err = s.coreManager.Update(ctx, auth)
	} else {
		_, err = s.coreManager.Register(ctx, auth)
	}
	if err != nil {
		log.Errorf("failed to %s auth %s: %v", op, auth.ID, err)
		current, ok := s.coreManager.GetByID(auth.ID)
		if !ok || current.Disabled {
			GlobalModelRegistry().UnregisterClient(auth.ID)
			return nil
		}
		auth = current
	}
	return auth
}

type authRegistrationWait struct {
	id string
	ch chan struct{}
}

func (s *Service) authRegistrationWaitCh(id string) chan struct{} {
	if s == nil {
		return nil
	}
	s.authRegWaitMu.Lock()
	defer s.authRegWaitMu.Unlock()
	if s.authRegWaiters == nil {
		return nil
	}
	return s.authRegWaiters[id]
}

func (s *Service) beginAuthRegistration(id string) authRegistrationWait {
	ch := make(chan struct{})
	if s == nil || id == "" {
		close(ch)
		return authRegistrationWait{id: id, ch: ch}
	}
	s.authRegWaitMu.Lock()
	if s.authRegWaiters == nil {
		s.authRegWaiters = make(map[string]chan struct{})
	}
	s.authRegWaiters[id] = ch
	s.authRegWaitMu.Unlock()
	return authRegistrationWait{id: id, ch: ch}
}

func finishAuthRegistrations(s *Service, waits []authRegistrationWait) {
	if len(waits) == 0 {
		return
	}
	if s != nil {
		s.authRegWaitMu.Lock()
		for _, wait := range waits {
			if wait.ch == nil {
				continue
			}
			if s.authRegWaiters != nil && s.authRegWaiters[wait.id] == wait.ch {
				delete(s.authRegWaiters, wait.id)
			}
		}
		s.authRegWaitMu.Unlock()
	}
	for _, wait := range waits {
		if wait.ch == nil {
			continue
		}
		select {
		case <-wait.ch:
		default:
			close(wait.ch)
		}
	}
}

func waitAuthRegistrations(waits []chan struct{}) {
	for _, ch := range waits {
		if ch == nil {
			continue
		}
		<-ch
	}
}

func (s *Service) shouldSkipModelRegistration(authID string, expectedGeneration uint64, expectedDisabled bool) bool {
	if s == nil || s.coreManager == nil || strings.TrimSpace(authID) == "" {
		return true
	}
	current, ok := s.coreManager.GetByID(authID)
	if !ok || current == nil {
		return !expectedDisabled
	}
	if expectedGeneration > 0 && current.Generation > expectedGeneration {
		return true
	}
	return current.Disabled != expectedDisabled
}

// isStaleCoreAuth reports whether an incoming auth update is older than the current
// state in coreManager, based on registration epoch and generation.
func isStaleCoreAuth(existing, incoming *coreauth.Auth) bool {
	if existing == nil || incoming == nil {
		return false
	}
	// If incoming has an explicit registration epoch that is older than existing, it's stale.
	if incoming.RegistrationEpoch > 0 && incoming.RegistrationEpoch < existing.RegistrationEpoch {
		return true
	}
	// Versioned snapshots with an older generation must not overwrite a newer persist.
	// Generation 0 is left unversioned (file-watcher synthesizer snapshots).
	if incoming.Generation > 0 && incoming.Generation < existing.Generation {
		return true
	}
	return false
}

func (s *Service) completeModelRegistrationForAuth(ctx context.Context, auth *coreauth.Auth) {
	s.completeModelRegistrationForAuthWithCache(ctx, auth, nil)
}

func (s *Service) completeModelRegistrationForAuthWithCache(ctx context.Context, auth *coreauth.Auth, compatCache *openAICompatibilityRegistrationCache) {
	if s == nil || s.coreManager == nil || auth == nil || auth.ID == "" {
		return
	}
	if ctx != nil && ctx.Err() != nil {
		return
	}
	s.registerModelsForAuthWithCache(ctx, auth, compatCache)
	if ctx != nil && ctx.Err() != nil {
		return
	}
	s.coreManager.ReconcileRegistryModelStates(ctx, auth.ID)

	// Refresh the scheduler entry so that the auth's supportedModelSet is rebuilt
	// from the now-populated global model registry. Without this, newly added auths
	// have an empty supportedModelSet (because Register/Update upserts into the
	// scheduler before registerModelsForAuth runs) and are invisible to the scheduler.
	s.coreManager.RefreshSchedulerEntry(auth.ID)
}

func (s *Service) applyCoreAuthRemoval(ctx context.Context, id string) {
	if s == nil || id == "" {
		return
	}
	if s.coreManager == nil {
		return
	}
	id = strings.TrimSpace(id)
	var provider string
	if existing, ok := s.coreManager.GetByID(id); ok && existing != nil {
		provider = strings.TrimSpace(existing.Provider)
	}
	GlobalModelRegistry().UnregisterClient(id)
	s.coreManager.Remove(ctx, id)
	if strings.EqualFold(provider, "codex") {
		executor.CloseCodexWebsocketSessionsForAuthID(id, "auth_removed")
	}
	if strings.EqualFold(provider, "xai") {
		executor.CloseXAIWebsocketSessionsForAuthID(id, "auth_removed")
	}
}

func (s *Service) applyRetryConfig(cfg *config.Config) {
	if s == nil || s.coreManager == nil || cfg == nil {
		return
	}
	maxInterval := time.Duration(cfg.MaxRetryInterval) * time.Second
	s.coreManager.SetRetryConfig(cfg.RequestRetry, maxInterval, cfg.MaxRetryCredentials)
	coreauth.SetTransientErrorCooldownSeconds(cfg.TransientErrorCooldownSeconds)
}

func (s *Service) configureCooldownStateStore(cfg *config.Config) {
	_ = s.configureCooldownStateStoreContext(context.Background(), cfg, false)
}

func (s *Service) configureCooldownStateStoreContext(ctx context.Context, cfg *config.Config, persistOld bool) bool {
	if s == nil || s.coreManager == nil {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	return s.coreManager.SwapCooldownStateStore(ctx, s.resolveCooldownStateStore(cfg), persistOld)
}

func (s *Service) resolveCooldownStateStore(cfg *config.Config) coreauth.CooldownStateStore {
	if cfg == nil || !cfg.SaveCooldownStatus || cfg.Home.Enabled {
		return nil
	}
	if s != nil && s.cooldownStateStore != nil {
		return s.cooldownStateStore
	}
	authDir, errResolve := resolveCooldownStateAuthDir(cfg)
	if errResolve != nil {
		log.Warnf("failed to resolve cooldown state directory: %v", errResolve)
		return nil
	}
	if authDir == "" {
		return nil
	}
	return coreauth.NewFileCooldownStateStoreWithAuthDir(authDir, authDir)
}

func resolveCooldownStateAuthDir(cfg *config.Config) (string, error) {
	if cfg == nil {
		return "", nil
	}
	authDir, errAuthDir := util.ResolveAuthDir(cfg.AuthDir)
	if errAuthDir != nil {
		return "", errAuthDir
	}
	return authDir, nil
}

func openAICompatInfoFromAuth(a *coreauth.Auth) (providerKey string, compatName string, ok bool) {
	if a == nil {
		return "", "", false
	}
	if len(a.Attributes) > 0 {
		providerKey = strings.TrimSpace(a.Attributes["provider_key"])
		compatName = strings.TrimSpace(a.Attributes["compat_name"])
		if compatName != "" {
			if providerKey == "" {
				providerKey = compatName
			}
			return util.OpenAICompatibleProviderKey(providerKey), compatName, true
		}
	}
	if strings.EqualFold(strings.TrimSpace(a.Provider), "openai-compatibility") {
		compatName = strings.TrimSpace(a.Label)
		providerKey = compatName
		if providerKey == "" {
			providerKey = "openai-compatibility"
		}
		return util.OpenAICompatibleProviderKey(providerKey), compatName, true
	}
	return "", "", false
}
