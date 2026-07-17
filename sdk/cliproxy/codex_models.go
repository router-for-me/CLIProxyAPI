package cliproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

type codexModelCatalogFetcher func(context.Context, *coreauth.Auth) ([]codexauth.ModelCatalogEntry, error)

type codexModelDiscoveryEntry struct {
	generation uint64
	identity   string
	models     []codexauth.ModelCatalogEntry
	ready      bool
	fetching   bool
	attempted  bool
}

func (s *Service) startCodexModelDiscovery(ctx context.Context) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.codexModelsMu.Lock()
	if s.codexModelsCancel != nil {
		s.codexModelsCancel()
	}
	s.codexModelsCtx, s.codexModelsCancel = context.WithCancel(ctx)
	s.codexModelsMu.Unlock()
}

func (s *Service) stopCodexModelDiscovery() {
	if s == nil {
		return
	}
	s.codexModelsMu.Lock()
	if s.codexModelsCancel != nil {
		s.codexModelsCancel()
		s.codexModelsCancel = nil
	}
	s.codexModelsCtx = nil
	s.codexModelsMu.Unlock()
}

func (s *Service) discoveredCodexModelsForAuth(ctx context.Context, auth *coreauth.Auth) ([]*ModelInfo, bool) {
	if !s.codexModelDiscoveryEnabled(auth) {
		return nil, false
	}
	if ctx == nil {
		ctx = context.Background()
	}

	identity := codexModelDiscoveryIdentity(auth)
	s.codexModelsMu.Lock()
	if s.codexModels == nil {
		s.codexModels = make(map[string]*codexModelDiscoveryEntry)
	}
	entry := s.codexModels[auth.ID]
	if entry == nil {
		entry = &codexModelDiscoveryEntry{
			generation: s.nextCodexModelGenerationLocked(),
			identity:   identity,
		}
		s.codexModels[auth.ID] = entry
	} else if entry.identity != identity {
		entry.generation = s.nextCodexModelGenerationLocked()
		entry.identity = identity
		entry.models = nil
		entry.ready = false
		entry.fetching = false
		entry.attempted = false
	}

	ready := entry.ready
	cached := cloneCodexModelCatalogEntries(entry.models)
	fetchCtx := ctx
	if s.codexModelsCtx != nil {
		fetchCtx = s.codexModelsCtx
	}
	if !entry.fetching && !entry.attempted && fetchCtx.Err() == nil {
		entry.fetching = true
		entry.attempted = true
		generation := entry.generation
		authSnapshot := auth.Clone()
		go s.refreshDiscoveredCodexModels(fetchCtx, authSnapshot, identity, generation)
	}
	s.codexModelsMu.Unlock()

	if !ready || len(cached) == 0 {
		return nil, false
	}
	return codexCatalogModelInfos(cached), true
}

func (s *Service) codexModelDiscoveryEnabled(auth *coreauth.Auth) bool {
	if s == nil || auth == nil || auth.ID == "" || auth.Disabled {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") || auth.AuthKind() != coreauth.AuthKindOAuth {
		return false
	}
	if codexAuthMetadataString(auth, "access_token") == "" {
		return false
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	enabled := cfg == nil || !cfg.DisableCodexModelDiscovery && !cfg.Home.Enabled
	s.cfgMu.RUnlock()
	return enabled
}

func (s *Service) refreshDiscoveredCodexModels(ctx context.Context, auth *coreauth.Auth, identity string, generation uint64) {
	fetcher := s.codexModelsFetch
	if fetcher == nil {
		fetcher = s.fetchCodexModelCatalog
	}
	models, errFetch := fetcher(ctx, auth)
	if errFetch != nil {
		s.finishCodexModelFetch(auth.ID, identity, generation, nil, false)
		log.WithFields(log.Fields{"auth_id": auth.ID, "provider": "codex"}).Debugf("Codex model discovery failed, keeping fallback catalog: %v", errFetch)
		return
	}
	if len(models) == 0 {
		s.finishCodexModelFetch(auth.ID, identity, generation, nil, false)
		return
	}
	if !s.finishCodexModelFetch(auth.ID, identity, generation, models, true) {
		return
	}
	if ctx.Err() != nil || s.coreManager == nil {
		return
	}
	latest, ok := s.coreManager.GetByID(auth.ID)
	if !ok || latest == nil || latest.Disabled {
		GlobalModelRegistry().UnregisterClient(auth.ID)
		return
	}
	if !s.codexModelDiscoveryResultCurrent(auth.ID, identity, generation) || codexModelDiscoveryIdentity(latest) != identity {
		s.completeModelRegistrationForAuth(ctx, latest)
		return
	}
	s.completeModelRegistrationForAuth(ctx, latest)

	// An auth update can race between the pre-registration checks and the registry
	// write. Restore the newest auth snapshot if that happened.
	current, exists := s.coreManager.GetByID(auth.ID)
	if !s.codexModelDiscoveryResultCurrent(auth.ID, identity, generation) || !exists || current == nil || current.Disabled || codexModelDiscoveryIdentity(current) != identity {
		if !exists || current == nil || current.Disabled {
			GlobalModelRegistry().UnregisterClient(auth.ID)
			return
		}
		s.completeModelRegistrationForAuth(ctx, current)
	}
}

func (s *Service) codexModelDiscoveryResultCurrent(authID, identity string, generation uint64) bool {
	if s == nil || authID == "" {
		return false
	}
	s.codexModelsMu.Lock()
	defer s.codexModelsMu.Unlock()
	entry := s.codexModels[authID]
	return entry != nil && entry.generation == generation && entry.identity == identity && entry.ready
}

func (s *Service) finishCodexModelFetch(authID, identity string, generation uint64, models []codexauth.ModelCatalogEntry, success bool) bool {
	if s == nil || authID == "" {
		return false
	}
	s.codexModelsMu.Lock()
	defer s.codexModelsMu.Unlock()
	entry := s.codexModels[authID]
	if entry == nil || entry.generation != generation || entry.identity != identity {
		return false
	}
	entry.fetching = false
	if success {
		entry.models = cloneCodexModelCatalogEntries(models)
		entry.ready = true
	}
	return success
}

func (s *Service) invalidateCodexModelDiscovery(auth *coreauth.Auth) {
	if s == nil || auth == nil || auth.ID == "" || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return
	}
	identity := codexModelDiscoveryIdentity(auth)
	s.codexModelsMu.Lock()
	defer s.codexModelsMu.Unlock()
	if s.codexModels == nil {
		s.codexModels = make(map[string]*codexModelDiscoveryEntry)
	}
	entry := s.codexModels[auth.ID]
	if entry == nil {
		entry = &codexModelDiscoveryEntry{}
		s.codexModels[auth.ID] = entry
	}
	if entry.identity != "" && entry.identity != identity {
		entry.models = nil
		entry.ready = false
	}
	entry.generation = s.nextCodexModelGenerationLocked()
	entry.identity = identity
	entry.fetching = false
	entry.attempted = false
}

func (s *Service) removeCodexModelDiscovery(authID string) {
	if s == nil || strings.TrimSpace(authID) == "" {
		return
	}
	s.codexModelsMu.Lock()
	delete(s.codexModels, authID)
	s.codexModelsGeneration++
	s.codexModelsMu.Unlock()
}

func (s *Service) nextCodexModelGenerationLocked() uint64 {
	s.codexModelsGeneration++
	return s.codexModelsGeneration
}

func (s *Service) fetchCodexModelCatalog(ctx context.Context, auth *coreauth.Auth) ([]codexauth.ModelCatalogEntry, error) {
	if auth == nil {
		return nil, fmt.Errorf("Codex model discovery auth is nil")
	}
	accessToken := codexAuthMetadataString(auth, "access_token")
	if accessToken == "" {
		return nil, fmt.Errorf("Codex model discovery missing access token")
	}

	baseURL := codexAuthAttributeString(auth, "base_url")
	if baseURL == "" {
		baseURL = codexAuthMetadataString(auth, "base_url")
	}
	userAgent := ""
	s.cfgMu.RLock()
	if s.cfg != nil {
		userAgent = strings.TrimSpace(s.cfg.CodexHeaderDefaults.UserAgent)
	}
	s.cfgMu.RUnlock()

	customRequest := &http.Request{Header: make(http.Header)}
	util.ApplyCustomHeadersFromAttrs(customRequest, auth.Attributes)
	client := &http.Client{}
	if transport, _, errProxy := proxyutil.BuildHTTPTransport(auth.ProxyURL); errProxy != nil {
		return nil, fmt.Errorf("build Codex model discovery proxy transport: %w", errProxy)
	} else if transport != nil {
		client.Transport = transport
	}

	catalog, errFetch := codexauth.FetchModelsCatalog(ctx, client, codexauth.ModelsRequest{
		BaseURL:       baseURL,
		AccessToken:   accessToken,
		AccountID:     codexModelDiscoveryAccountID(auth),
		ClientVersion: codexauth.DefaultModelsClientVersion,
		UserAgent:     userAgent,
		Headers:       customRequest.Header,
		Host:          customRequest.Host,
	})
	if errFetch != nil {
		return nil, errFetch
	}
	return cloneCodexModelCatalogEntries(catalog.Models), nil
}

func codexCatalogModelInfos(entries []codexauth.ModelCatalogEntry) []*ModelInfo {
	models := make([]*ModelInfo, 0, len(entries))
	for _, entry := range entries {
		slug := strings.TrimSpace(entry.Slug)
		if slug == "" {
			continue
		}
		if model := registry.LookupStaticModelInfo(slug); model != nil {
			models = append(models, model)
			continue
		}
		displayName := strings.TrimSpace(entry.DisplayName)
		if displayName == "" {
			displayName = slug
		}
		description := strings.TrimSpace(entry.Description)
		if description == "" {
			description = displayName
		}
		contextLength := entry.ContextWindow
		if contextLength <= 0 {
			contextLength = entry.MaxContextWindow
		}
		model := &ModelInfo{
			ID:                  slug,
			Object:              "model",
			OwnedBy:             "openai",
			Type:                "openai",
			DisplayName:         displayName,
			Version:             slug,
			Description:         description,
			ContextLength:       contextLength,
			SupportedParameters: []string{"tools"},
		}
		levels := codexCatalogReasoningLevels(entry.SupportedReasoningLevels)
		if len(levels) > 0 {
			model.Thinking = &registry.ThinkingSupport{Levels: levels}
		}
		models = append(models, model)
	}
	return registry.WithCodexBuiltins(models)
}

func codexCatalogReasoningLevels(levels []codexauth.ModelCatalogReasoningLevel) []string {
	result := make([]string, 0, len(levels))
	seen := make(map[string]struct{}, len(levels))
	for _, level := range levels {
		effort := strings.ToLower(strings.TrimSpace(level.Effort))
		if effort == "" {
			continue
		}
		if _, exists := seen[effort]; exists {
			continue
		}
		seen[effort] = struct{}{}
		result = append(result, effort)
	}
	return result
}

func codexModelDiscoveryIdentity(auth *coreauth.Auth) string {
	if accountID := codexModelDiscoveryAccountID(auth); accountID != "" {
		return "account:" + accountID
	}
	if email := codexAuthMetadataString(auth, "email"); email != "" {
		return "email:" + strings.ToLower(email)
	}
	if auth == nil {
		return ""
	}
	if accessToken := codexAuthMetadataString(auth, "access_token"); accessToken != "" {
		digest := sha256.Sum256([]byte(accessToken))
		return "token:" + hex.EncodeToString(digest[:8])
	}
	return "auth:" + auth.ID
}

func codexModelDiscoveryAccountID(auth *coreauth.Auth) string {
	if accountID := codexAuthMetadataString(auth, "account_id"); accountID != "" {
		return accountID
	}
	idToken := codexAuthMetadataString(auth, "id_token")
	if idToken == "" {
		return ""
	}
	claims, errParse := codexauth.ParseJWTToken(idToken)
	if errParse != nil || claims == nil {
		return ""
	}
	return strings.TrimSpace(claims.CodexAuthInfo.ChatgptAccountID)
}

func codexAuthMetadataString(auth *coreauth.Auth, key string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	value, _ := auth.Metadata[key].(string)
	return strings.TrimSpace(value)
}

func codexAuthAttributeString(auth *coreauth.Auth, key string) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes[key])
}

func cloneCodexModelCatalogEntries(entries []codexauth.ModelCatalogEntry) []codexauth.ModelCatalogEntry {
	if len(entries) == 0 {
		return nil
	}
	cloned := make([]codexauth.ModelCatalogEntry, len(entries))
	copy(cloned, entries)
	for index := range cloned {
		if len(entries[index].SupportedReasoningLevels) > 0 {
			cloned[index].SupportedReasoningLevels = append([]codexauth.ModelCatalogReasoningLevel(nil), entries[index].SupportedReasoningLevels...)
		}
	}
	return cloned
}
