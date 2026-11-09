package cliproxy

import (
	"context"
	"time"

	qoderauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func (s *Service) qoderModelsForAuth(ctx context.Context, auth *coreauth.Auth) []*ModelInfo {
	if cached := cachedUpstreamModels(auth); cached != nil {
		return cached
	}
	catalog, err := s.fetchQoderCatalog(ctx, auth)
	if err != nil {
		log.WithError(err).Debug("qoder: account model discovery failed")
		return nil
	}
	storeUpstreamModels(auth, catalog.Models)
	return catalog.Models
}

func (s *Service) fetchQoderCatalog(ctx context.Context, auth *coreauth.Auth) (*qoderauth.Catalog, error) {
	preparer := executor.NewQoderExecutor(s.cfg)
	prepared := auth
	var err error
	if s.coreManager != nil {
		prepared, err = s.coreManager.PrepareRequestAuth(ctx, preparer, auth)
	} else if preparer.ShouldPrepareRequestAuth(auth) {
		prepared, err = preparer.PrepareRequestAuth(ctx, auth)
	}
	if err != nil {
		return nil, err
	}
	storage, err := qoderauth.CredentialFromAuth(prepared)
	if err != nil {
		return nil, err
	}
	client := helps.NewProxyAwareHTTPClient(ctx, s.cfg, prepared, 0)
	return qoderauth.FetchCatalog(ctx, client, auth.ID, storage)
}

func (s *Service) qoderCredentialModelCatalog(ctx context.Context, auth *coreauth.Auth, refresh bool) (*coreauth.CredentialModelCatalog, error) {
	status := registry.ModelCatalogStatus{
		Kind:                   "account_upstream",
		AutoRefresh:            true,
		SupportsRefresh:        true,
		UpstreamDiscovery:      true,
		RefreshIntervalSeconds: int64(registry.ModelsRefreshInterval / time.Second),
	}
	if refresh {
		catalog, err := s.fetchQoderCatalog(ctx, auth)
		if err != nil {
			return nil, coreauth.ErrCredentialModelsRefreshFailed
		}
		storeUpstreamModels(auth, catalog.Models)
		if s.coreManager != nil {
			if current, ok := s.coreManager.GetByID(auth.ID); ok && !current.Disabled {
				s.refreshModelRegistrationForAuthWithContext(ctx, current, nil)
			}
		}
		return &coreauth.CredentialModelCatalog{Models: catalog.Models, Source: status}, nil
	}
	return &coreauth.CredentialModelCatalog{Models: cachedUpstreamModels(auth), Source: status}, nil
}
