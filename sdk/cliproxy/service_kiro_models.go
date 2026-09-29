package cliproxy

import (
	"context"
	"strings"
	"sync"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	log "github.com/sirupsen/logrus"
)

// kiroModelRefreshInterval matches the remote catalog refresh cadence so the
// live Kiro catalog and models.json stay roughly in step.
const kiroModelRefreshInterval = 3 * time.Hour

var kiroModelUpdaterOnce sync.Once

// StartKiroModelsUpdater starts a background updater that discovers the Kiro
// model catalog from the account's own ListAvailableModels endpoint, first at
// startup and then on the shared refresh interval.
//
// Kiro is not published in the remote models.json catalog, so without this the
// provider would be pinned to its built-in definitions and would never pick up
// newly released models.
func (s *Service) StartKiroModelsUpdater(ctx context.Context) {
	if s == nil {
		return
	}
	if registry.KiroDynamicModelsDisabled() {
		log.Info("Local model mode: live Kiro catalog discovery disabled; using built-in Kiro model definitions")
		return
	}
	kiroModelUpdaterOnce.Do(func() {
		go s.runKiroModelsUpdater(ctx)
	})
}

func (s *Service) runKiroModelsUpdater(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	ticker := time.NewTicker(kiroModelRefreshInterval)
	defer ticker.Stop()

	log.Infof("periodic Kiro model refresh started (interval=%s)", kiroModelRefreshInterval)
	s.refreshKiroModels(ctx, "startup Kiro model refresh")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshKiroModels(ctx, "periodic Kiro model refresh")
		}
	}
}

// refreshKiroModels fetches the live catalog and, when it differs from the
// stored one, re-registers models for every Kiro auth so the new definitions
// become routable without a restart.
func (s *Service) refreshKiroModels(ctx context.Context, label string) {
	if s == nil || s.coreManager == nil {
		return
	}

	entries, source := s.fetchKiroModelCatalog(ctx)
	if len(entries) == 0 {
		log.Debugf("%s: no Kiro credential returned a catalog, keeping current definitions", label)
		return
	}

	models := registry.BuildKiroModelInfos(entries)
	if len(models) == 0 {
		log.Warnf("%s: catalog from %s produced no usable models, keeping current definitions", label, source)
		return
	}
	if !registry.SetKiroDynamicModels(models) {
		log.Infof("%s completed from %s, no changes detected (%d models)", label, source, len(models))
		return
	}

	log.Infof("%s completed from %s, catalog updated (%d models)", label, source, len(models))
	s.reregisterKiroAuths(ctx)
}

// fetchKiroModelCatalog asks each enabled Kiro credential for its catalog and
// returns the first successful response. Model registration is provider-wide
// rather than per-credential, so a single catalog is authoritative.
func (s *Service) fetchKiroModelCatalog(ctx context.Context) ([]registry.KiroModelCatalogEntry, string) {
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	authSvc := kiroauth.NewKiroAuth(cfg, nil)

	for _, item := range s.coreManager.List() {
		if item == nil || item.ID == "" {
			continue
		}
		auth, ok := s.coreManager.GetByID(item.ID)
		if !ok || auth == nil || auth.Disabled {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "kiro") {
			continue
		}
		creds := kiroauth.CredentialsFromAuth(auth)
		if creds == nil || strings.TrimSpace(creds.AccessToken) == "" {
			continue
		}

		models, err := authSvc.ListAvailableModels(ctx, creds)
		if err != nil {
			log.Debugf("Kiro model catalog fetch failed for auth %s: %v", auth.ID, err)
			continue
		}

		entries := make([]registry.KiroModelCatalogEntry, 0, len(models))
		for _, model := range models {
			entries = append(entries, registry.KiroModelCatalogEntry{
				ModelID:         model.ModelID,
				ModelName:       model.ModelName,
				Description:     model.Description,
				MaxInputTokens:  model.MaxInputTokens,
				MaxOutputTokens: model.MaxOutputTokens,
			})
		}
		return entries, "auth " + auth.ID
	}
	return nil, ""
}

// reregisterKiroAuths rebuilds model registrations for every Kiro auth using the
// refreshed catalog, mirroring what the remote catalog refresh callback does.
func (s *Service) reregisterKiroAuths(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	refreshed := 0
	var refreshedMu sync.Mutex
	tasks := make([]modelRegistrationTask, 0)
	for _, item := range s.coreManager.List() {
		if item == nil || item.ID == "" {
			continue
		}
		auth, ok := s.coreManager.GetByID(item.ID)
		if !ok || auth == nil || auth.Disabled {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "kiro") {
			continue
		}
		authForRefresh := auth
		tasks = append(tasks, modelRegistrationTask{
			phase:    modelRegistrationPhase(authForRefresh),
			category: modelRegistrationCategory(authForRefresh),
			run: func(compatCache *openAICompatibilityRegistrationCache) {
				if s.refreshModelRegistrationForAuthWithCache(authForRefresh, compatCache) {
					refreshedMu.Lock()
					refreshed++
					refreshedMu.Unlock()
				}
			},
		})
	}
	s.runModelRegistrationTasks(ctx, tasks)

	if refreshed > 0 {
		log.Infof("re-registered models for %d Kiro auth(s) due to live catalog changes", refreshed)
	}
}
