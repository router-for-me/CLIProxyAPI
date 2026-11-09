package cliproxy

import (
	"context"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelconfig"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func buildCredentialModels(a *coreauth.Auth, catalog []*ModelInfo, excluded []string, restrictToCatalog bool) ([]*ModelInfo, bool) {
	entries, configured := a.CredentialModels()
	if !configured {
		return catalog, false
	}
	out := make([]*ModelInfo, 0, len(entries))
	for _, entry := range entries {
		var source *ModelInfo
		for _, candidate := range catalog {
			if candidate != nil && strings.EqualFold(candidate.ID, entry.Name) {
				source = candidate
				break
			}
		}
		if restrictToCatalog && source == nil {
			continue
		}
		if source == nil {
			source = registry.LookupStaticModelInfoByChannel(entry.Name, a.Provider)
		}
		if source == nil {
			source = &ModelInfo{ID: entry.Name, Object: "model", OwnedBy: a.Provider, Type: a.Provider}
		}
		if len(applyExcludedModels([]*ModelInfo{source}, excluded)) == 0 {
			continue
		}
		model := cloneModelInfoForCatalogRoute(source)
		model.Thinking = modelconfig.NormalizeThinkingSupport(source.Thinking)
		model.SupportedInputModalities = append([]string(nil), source.SupportedInputModalities...)
		model.SupportedOutputModalities = append([]string(nil), source.SupportedOutputModalities...)
		model.ID, model.MetadataModelID = entry.PublicName(), entry.Name
		model.Name = rewriteModelInfoName(model.Name, source.ID, model.ID)
		model.UserDefined = true
		if entry.MaxContextLength != nil {
			model.ContextLength, model.MaxContextLength = *entry.MaxContextLength, *entry.MaxContextLength
		}
		if entry.Thinking != nil {
			model.Thinking, model.ExplicitThinking = entry.Thinking, true
		}
		if entry.IsCompat != nil {
			model.IsCompat = *entry.IsCompat
		}
		if entry.SupportConfigurationUpdate != nil {
			model.SupportConfigurationUpdate = *entry.SupportConfigurationUpdate
		}
		if entry.InputModalities != nil {
			model.SupportedInputModalities = *entry.InputModalities
			model.ExplicitInputModalities = true
		}
		if entry.OutputModalities != nil {
			model.SupportedOutputModalities = *entry.OutputModalities
		}
		out = append(out, &model)
	}
	return out, true
}

func credentialDefaultModels(a *coreauth.Auth) []*ModelInfo {
	if strings.EqualFold(a.Provider, "codex") {
		switch strings.ToLower(strings.TrimSpace(a.Attributes["plan_type"])) {
		case "free":
			return registry.GetCodexFreeModels()
		case "plus":
			return registry.GetCodexPlusModels()
		case "team", "business", "go":
			return registry.GetCodexTeamModels()
		default:
			return registry.GetCodexProModels()
		}
	}
	return registry.GetStaticModelDefinitionsByChannel(a.Provider)
}

func (s *Service) credentialModelCatalog(ctx context.Context, a *coreauth.Auth, refresh bool) (*coreauth.CredentialModelCatalog, error) {
	if !coreauth.SupportsCredentialModels(a) {
		return nil, coreauth.ErrCredentialModelsUnsupported
	}
	if s.antigravityHomeEnabled() {
		return nil, coreauth.ErrCredentialModelsUnsupported
	}
	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	if provider == "kiro" {
		status := registry.ModelCatalogStatus{Kind: "account_upstream", SupportsRefresh: true, UpstreamDiscovery: true}
		models := cachedUpstreamModels(a)
		if refresh {
			preparer := executor.NewKiroExecutor(s.cfg)
			var errPrepare error
			if s.coreManager != nil {
				a, errPrepare = s.coreManager.PrepareRequestAuth(ctx, preparer, a)
			} else if preparer.ShouldPrepareRequestAuth(a) {
				a, errPrepare = preparer.PrepareRequestAuth(ctx, a)
			}
			if errPrepare != nil {
				log.WithError(errPrepare).Debug("kiro: model discovery credential preparation failed")
				return nil, coreauth.ErrCredentialModelsRefreshFailed
			}
			client := &kiro.Client{HTTP: helps.NewProxyAwareHTTPClient(ctx, s.cfg, a, 0)}
			var errFetch error
			models, errFetch = client.ListModels(ctx, a.Metadata)
			if errFetch != nil {
				log.WithError(errFetch).Debug("kiro: account model discovery failed")
				return nil, coreauth.ErrCredentialModelsRefreshFailed
			}
			storeUpstreamModels(a, models)
			if s.coreManager != nil {
				if current, ok := s.coreManager.GetByID(a.ID); ok && !current.Disabled {
					s.refreshModelRegistrationForAuthWithContext(ctx, current, nil)
				}
			}
		}
		if models == nil {
			models = registry.GetKiroModels()
		}
		return &coreauth.CredentialModelCatalog{Models: models, Source: status}, nil
	}
	if provider == "qoder" || provider == "qoder-cn" {
		return s.qoderCredentialModelCatalog(ctx, a, refresh)
	}
	if provider == "zcode" || provider == "openai-compatible" {
		return s.upstreamCredentialModelCatalog(ctx, a, provider, refresh)
	}
	status := registry.ModelCatalogStatusForProvider(a.Provider)
	models := credentialDefaultModels(a)
	if strings.EqualFold(a.Provider, "antigravity") {
		hints := s.cachedAntigravityHints(a)
		if refresh {
			key := s.antigravityCapabilityKey(a)
			antigravityCapabilityMu.Lock()
			entry := antigravityCapabilityCache[key]
			previousRevision := entry.hints.revision
			entry.expiresAt = time.Time{}
			antigravityCapabilityCache[key] = entry
			antigravityCapabilityMu.Unlock()
			hints = s.fetchAntigravityModelCapabilityHintsForAuth(ctx, a)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if hints.ModelIDs == nil || hints.revision == previousRevision {
				return nil, coreauth.ErrCredentialModelsRefreshFailed
			}
			if s.coreManager != nil {
				current, ok := s.coreManager.GetByID(a.ID)
				if ok && !current.Disabled {
					s.refreshModelRegistrationForAuthWithContext(ctx, current, nil)
				}
			}
		}
		if hints.ModelIDs == nil {
			return nil, coreauth.ErrCredentialModelsUnavailable
		}
		models = applyAntigravityFetchedModelCapabilities(filterAntigravityModels(models, hints), hints)
	} else if refresh {
		if !status.SupportsRefresh {
			return nil, coreauth.ErrCredentialModelsUnsupported
		}
		if err := registry.RefreshModelCatalogForProvider(ctx, a.Provider); err != nil {
			return nil, coreauth.ErrCredentialModelsRefreshFailed
		}
		models = credentialDefaultModels(a)
	}
	return &coreauth.CredentialModelCatalog{Models: models, Source: status}, nil
}

// upstreamCredentialModelCatalog serves providers whose catalog is the
// credential's own account upstream (zcode, openai-compatible). Reads use the
// cached listing; refresh re-fetches and replaces the cache.
func (s *Service) upstreamCredentialModelCatalog(ctx context.Context, a *coreauth.Auth, provider string, refresh bool) (*coreauth.CredentialModelCatalog, error) {
	status := registry.ModelCatalogStatus{
		Kind:                   "account_upstream",
		AutoRefresh:            true,
		SupportsRefresh:        true,
		UpstreamDiscovery:      true,
		RefreshIntervalSeconds: int64(registry.ModelsRefreshInterval / time.Second),
	}
	if refresh {
		baseURL, apiKey := upstreamModelsEndpoint(a, provider)
		models, errFetch := fetchUpstreamModels(ctx, baseURL, apiKey, a.ProxyURL)
		if errFetch != nil {
			log.WithError(errFetch).Debug("credential upstream model fetch failed")
			return nil, coreauth.ErrCredentialModelsRefreshFailed
		}
		storeUpstreamModels(a, models)
		return &coreauth.CredentialModelCatalog{Models: models, Source: status}, nil
	}
	if cached := cachedUpstreamModels(a); cached != nil {
		return &coreauth.CredentialModelCatalog{Models: cached, Source: status}, nil
	}
	if provider == "zcode" {
		return &coreauth.CredentialModelCatalog{Models: registry.GetZCodeModels(), Source: status}, nil
	}
	return &coreauth.CredentialModelCatalog{Source: status}, nil
}
