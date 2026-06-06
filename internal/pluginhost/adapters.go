package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

type registryModelInfo = registry.ModelInfo

type modelRegistry interface {
	RegisterClient(clientID, clientProvider string, models []*registry.ModelInfo)
	UnregisterClient(clientID string)
}

type modelProviderRegistry interface {
	modelRegistry
	GetModelProviders(modelID string) []string
}

type pluginModelRegistration struct {
	pluginID    string
	provider    string
	priority    int
	models      []*registry.ModelInfo
	hasExecutor bool
}

func normalizedExecutorModelScope(caps pluginapi.Capabilities) pluginapi.ExecutorModelScope {
	if caps.Executor == nil {
		return pluginapi.ExecutorModelScopeBoth
	}
	switch caps.ExecutorModelScope {
	case pluginapi.ExecutorModelScopeStatic, pluginapi.ExecutorModelScopeOAuth, pluginapi.ExecutorModelScopeBoth:
		return caps.ExecutorModelScope
	default:
		return pluginapi.ExecutorModelScopeBoth
	}
}

func executorScopeAllowsStaticModels(caps pluginapi.Capabilities) bool {
	if caps.Executor == nil {
		return true
	}
	scope := normalizedExecutorModelScope(caps)
	return scope == pluginapi.ExecutorModelScopeStatic || scope == pluginapi.ExecutorModelScopeBoth
}

func executorScopeAllowsOAuthModels(caps pluginapi.Capabilities) bool {
	if caps.Executor == nil {
		return true
	}
	scope := normalizedExecutorModelScope(caps)
	return scope == pluginapi.ExecutorModelScopeOAuth || scope == pluginapi.ExecutorModelScopeBoth
}

type AuthModelResult struct {
	Provider string
	Models   []*registry.ModelInfo
	Auth     *coreauth.Auth
	Handled  bool
	Err      error
}

func pluginModelInfoToRegistryModelInfo(model pluginapi.ModelInfo) *registry.ModelInfo {
	return &registry.ModelInfo{
		ID:                         model.ID,
		Object:                     model.Object,
		Created:                    model.Created,
		OwnedBy:                    model.OwnedBy,
		Type:                       model.Type,
		DisplayName:                model.DisplayName,
		Name:                       model.Name,
		Version:                    model.Version,
		Description:                model.Description,
		InputTokenLimit:            int(model.InputTokenLimit),
		OutputTokenLimit:           int(model.OutputTokenLimit),
		SupportedGenerationMethods: cloneStringSlice(model.SupportedGenerationMethods),
		ContextLength:              int(model.ContextLength),
		MaxCompletionTokens:        int(model.MaxCompletionTokens),
		SupportedParameters:        cloneStringSlice(model.SupportedParameters),
		SupportedInputModalities:   cloneStringSlice(model.SupportedInputModalities),
		SupportedOutputModalities:  cloneStringSlice(model.SupportedOutputModalities),
		Thinking:                   pluginThinkingSupportToRegistryThinkingSupport(model.Thinking),
		UserDefined:                model.UserDefined,
	}
}

func pluginThinkingSupportToRegistryThinkingSupport(thinking *pluginapi.ThinkingSupport) *registry.ThinkingSupport {
	if thinking == nil {
		return nil
	}
	return &registry.ThinkingSupport{
		Min:            thinking.Min,
		Max:            thinking.Max,
		ZeroAllowed:    thinking.ZeroAllowed,
		DynamicAllowed: thinking.DynamicAllowed,
		Levels:         cloneStringSlice(thinking.Levels),
	}
}

func registryModelInfoToPluginModelInfo(model *registry.ModelInfo) pluginapi.ModelInfo {
	if model == nil {
		return pluginapi.ModelInfo{}
	}
	return pluginapi.ModelInfo{
		ID:                         model.ID,
		Object:                     model.Object,
		Created:                    model.Created,
		OwnedBy:                    model.OwnedBy,
		Type:                       model.Type,
		DisplayName:                model.DisplayName,
		Name:                       model.Name,
		Version:                    model.Version,
		Description:                model.Description,
		InputTokenLimit:            int64(model.InputTokenLimit),
		OutputTokenLimit:           int64(model.OutputTokenLimit),
		SupportedGenerationMethods: cloneStringSlice(model.SupportedGenerationMethods),
		ContextLength:              int64(model.ContextLength),
		MaxCompletionTokens:        int64(model.MaxCompletionTokens),
		SupportedParameters:        cloneStringSlice(model.SupportedParameters),
		SupportedInputModalities:   cloneStringSlice(model.SupportedInputModalities),
		SupportedOutputModalities:  cloneStringSlice(model.SupportedOutputModalities),
		Thinking:                   registryThinkingSupportToPluginThinkingSupport(model.Thinking),
		UserDefined:                model.UserDefined,
	}
}

func registryThinkingSupportToPluginThinkingSupport(thinking *registry.ThinkingSupport) *pluginapi.ThinkingSupport {
	if thinking == nil {
		return nil
	}
	return &pluginapi.ThinkingSupport{
		Min:            thinking.Min,
		Max:            thinking.Max,
		ZeroAllowed:    thinking.ZeroAllowed,
		DynamicAllowed: thinking.DynamicAllowed,
		Levels:         cloneStringSlice(thinking.Levels),
	}
}

func cloneStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return append([]string(nil), in...)
}

func cloneRegistryModels(in []*registry.ModelInfo) []*registry.ModelInfo {
	if len(in) == 0 {
		return nil
	}
	out := make([]*registry.ModelInfo, 0, len(in))
	for _, model := range in {
		if model == nil {
			continue
		}
		copyModel := *model
		copyModel.SupportedGenerationMethods = cloneStringSlice(model.SupportedGenerationMethods)
		copyModel.SupportedParameters = cloneStringSlice(model.SupportedParameters)
		copyModel.SupportedInputModalities = cloneStringSlice(model.SupportedInputModalities)
		copyModel.SupportedOutputModalities = cloneStringSlice(model.SupportedOutputModalities)
		if model.Thinking != nil {
			thinking := *model.Thinking
			thinking.Levels = cloneStringSlice(model.Thinking.Levels)
			copyModel.Thinking = &thinking
		}
		out = append(out, &copyModel)
	}
	return out
}

func (h *Host) RegisterModels(ctx context.Context, modelRegistry modelRegistry) {
	if h == nil || modelRegistry == nil {
		return
	}

	snap := h.Snapshot()
	registrations := make([]modelClientRegistration, 0)
	nextClients := make(map[string]struct{})
	nextProviders := make(map[string]string)
	nextModelRegistrations := make(map[string]pluginModelRegistration)
	for _, record := range snap.records {
		modelProvider := record.plugin.Capabilities.ModelProvider
		registrar := record.plugin.Capabilities.ModelRegistrar
		if modelProvider == nil && registrar == nil {
			continue
		}
		if !executorScopeAllowsStaticModels(record.plugin.Capabilities) {
			continue
		}
		var resp pluginapi.ModelRegistrationResponse
		var errRegisterModels error
		if modelProvider != nil {
			modelResp, errStaticModels := h.callModelProviderStaticModels(ctx, record, modelProvider)
			errRegisterModels = errStaticModels
			resp = pluginapi.ModelRegistrationResponse{
				Provider: modelResp.Provider,
				Models:   modelResp.Models,
			}
		} else {
			resp, errRegisterModels = h.callModelRegistrar(ctx, record, registrar)
		}
		if errRegisterModels != nil {
			log.Warnf("pluginhost: model registrar %s failed: %v", record.id, errRegisterModels)
			continue
		}

		provider := strings.ToLower(strings.TrimSpace(resp.Provider))
		if provider == "" || len(resp.Models) == 0 {
			continue
		}

		models := make([]*registry.ModelInfo, 0, len(resp.Models))
		for _, item := range resp.Models {
			model := pluginModelInfoToRegistryModelInfo(item)
			if model == nil || strings.TrimSpace(model.ID) == "" {
				continue
			}
			model.ID = strings.TrimSpace(model.ID)
			models = append(models, model)
		}
		if len(models) == 0 {
			continue
		}

		nextModelRegistrations[record.id] = pluginModelRegistration{
			pluginID:    record.id,
			provider:    provider,
			priority:    record.priority,
			models:      cloneRegistryModels(models),
			hasExecutor: record.plugin.Capabilities.Executor != nil,
		}
		nextProviders[record.id] = provider
		if record.plugin.Capabilities.Executor == nil {
			clientID := "plugin:" + record.id + ":" + provider
			registrations = append(registrations, modelClientRegistration{
				clientID: clientID,
				provider: provider,
				models:   models,
			})
			nextClients[clientID] = struct{}{}
		}
	}
	h.commitModelClients(snap, modelRegistry, registrations, nextClients, nextProviders, nextModelRegistrations)
}

func (h *Host) ModelsForAuth(ctx context.Context, auth *coreauth.Auth) AuthModelResult {
	if h == nil || auth == nil {
		return AuthModelResult{}
	}
	providerKey := normalizeProviderID(auth.Provider)
	if providerKey == "" {
		return AuthModelResult{}
	}
	for _, record := range h.Snapshot().records {
		modelProvider := record.plugin.Capabilities.ModelProvider
		if modelProvider == nil || h.isPluginFused(record.id) {
			continue
		}
		if !executorScopeAllowsOAuthModels(record.plugin.Capabilities) {
			continue
		}
		authProvider := record.plugin.Capabilities.AuthProvider
		if authProvider != nil {
			identifier, okIdentifier := h.callAuthProviderIdentifier(record.id, authProvider)
			if !okIdentifier || normalizeProviderID(identifier) != providerKey {
				continue
			}
		} else {
			recordProvider := normalizeProviderID(h.modelProvider(record.id))
			if recordProvider == "" {
				executor := record.plugin.Capabilities.Executor
				if executor != nil {
					candidate, okCandidate := h.executorProvider(record, executor)
					if okCandidate {
						recordProvider = candidate
					}
				}
			}
			if recordProvider != providerKey {
				continue
			}
		}
		resp, errModels := h.callModelsForAuth(ctx, record, modelProvider, auth)
		if errModels != nil {
			log.Warnf("pluginhost: models for auth %s failed: %v", auth.ID, errModels)
			return AuthModelResult{Handled: true, Err: errModels}
		}
		respProvider := normalizeProviderID(resp.Provider)
		if respProvider != "" && respProvider != providerKey {
			continue
		}
		if respProvider == "" {
			respProvider = providerKey
		}
		models := make([]*registry.ModelInfo, 0, len(resp.Models))
		for _, item := range resp.Models {
			model := pluginModelInfoToRegistryModelInfo(item)
			if model != nil {
				model.ID = strings.TrimSpace(model.ID)
			}
			if model != nil && model.ID != "" {
				models = append(models, model)
			}
		}
		path := ""
		if auth.Attributes != nil {
			path = auth.Attributes["path"]
		}
		var updated *coreauth.Auth
		if authDataHasValue(resp.AuthUpdate) {
			updated = h.AuthDataToCoreAuth(authDataWithDefaults(resp.AuthUpdate, auth), path, auth.FileName)
		}
		return AuthModelResult{Provider: respProvider, Models: models, Auth: updated, Handled: true}
	}
	return AuthModelResult{}
}

func authDataHasValue(data pluginapi.AuthData) bool {
	return strings.TrimSpace(data.Provider) != "" ||
		strings.TrimSpace(data.ID) != "" ||
		strings.TrimSpace(data.FileName) != "" ||
		strings.TrimSpace(data.Label) != "" ||
		strings.TrimSpace(data.Prefix) != "" ||
		strings.TrimSpace(data.ProxyURL) != "" ||
		data.Disabled ||
		len(data.StorageJSON) > 0 ||
		len(data.Metadata) > 0 ||
		len(data.Attributes) > 0 ||
		!data.NextRefreshAfter.IsZero()
}

func authDataWithDefaults(data pluginapi.AuthData, auth *coreauth.Auth) pluginapi.AuthData {
	if auth == nil {
		return data
	}
	if strings.TrimSpace(data.Provider) == "" {
		data.Provider = auth.Provider
	}
	if strings.TrimSpace(data.ID) == "" {
		data.ID = auth.ID
	}
	if strings.TrimSpace(data.FileName) == "" {
		data.FileName = auth.FileName
	}
	if strings.TrimSpace(data.Label) == "" {
		data.Label = auth.Label
	}
	if strings.TrimSpace(data.Prefix) == "" {
		data.Prefix = auth.Prefix
	}
	if strings.TrimSpace(data.ProxyURL) == "" {
		data.ProxyURL = auth.ProxyURL
	}
	if len(data.Metadata) == 0 {
		data.Metadata = cloneAnyMap(auth.Metadata)
	} else {
		metadata := cloneAnyMap(data.Metadata)
		for key, value := range auth.Metadata {
			if _, exists := metadata[key]; !exists {
				metadata[key] = value
			}
		}
		data.Metadata = metadata
	}
	if len(data.Attributes) == 0 {
		data.Attributes = cloneStringMap(auth.Attributes)
	} else {
		attributes := cloneStringMap(data.Attributes)
		for key, value := range auth.Attributes {
			if _, exists := attributes[key]; !exists {
				attributes[key] = value
			}
		}
		data.Attributes = attributes
	}
	if len(data.StorageJSON) == 0 {
		data.StorageJSON = storageJSONFromAuth(auth)
	}
	if data.NextRefreshAfter.IsZero() {
		data.NextRefreshAfter = auth.NextRefreshAfter
	}
	return data
}

type modelClientRegistration struct {
	clientID string
	provider string
	models   []*registry.ModelInfo
}

func (h *Host) callModelRegistrar(ctx context.Context, record capabilityRecord, registrar pluginapi.ModelRegistrar) (resp pluginapi.ModelRegistrationResponse, err error) {
	if h == nil || registrar == nil || h.isPluginFused(record.id) {
		return pluginapi.ModelRegistrationResponse{}, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "ModelRegistrar.RegisterModels", recovered)
			resp = pluginapi.ModelRegistrationResponse{}
			err = fmt.Errorf("model registrar panic: %v", recovered)
		}
	}()
	return registrar.RegisterModels(ctx, pluginapi.ModelRegistrationRequest{Plugin: record.meta})
}

func (h *Host) callModelProviderStaticModels(ctx context.Context, record capabilityRecord, provider pluginapi.ModelProvider) (resp pluginapi.ModelResponse, err error) {
	if h == nil || provider == nil || h.isPluginFused(record.id) {
		return pluginapi.ModelResponse{}, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "ModelProvider.StaticModels", recovered)
			resp = pluginapi.ModelResponse{}
			err = fmt.Errorf("model provider panic: %v", recovered)
		}
	}()
	return provider.StaticModels(ctx, pluginapi.StaticModelRequest{
		Plugin: record.meta,
		Host:   h.hostConfigSummary(),
	})
}

func (h *Host) callModelsForAuth(ctx context.Context, record capabilityRecord, provider pluginapi.ModelProvider, auth *coreauth.Auth) (resp pluginapi.ModelResponse, err error) {
	if h == nil || provider == nil || auth == nil || h.isPluginFused(record.id) {
		return pluginapi.ModelResponse{}, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "ModelProvider.ModelsForAuth", recovered)
			resp = pluginapi.ModelResponse{}
			err = fmt.Errorf("model provider per-auth models panic: %v", recovered)
		}
	}()
	return provider.ModelsForAuth(ctx, pluginapi.AuthModelRequest{
		Plugin:       record.meta,
		AuthID:       auth.ID,
		AuthProvider: auth.Provider,
		StorageJSON:  storageJSONFromAuth(auth),
		Metadata:     cloneAnyMap(auth.Metadata),
		Attributes:   cloneStringMap(auth.Attributes),
		Host:         h.hostConfigSummary(),
		HTTPClient:   h.newHTTPClient(auth),
	})
}

func (h *Host) commitModelClients(snap *Snapshot, modelRegistry modelRegistry, registrations []modelClientRegistration, nextClients map[string]struct{}, nextProviders map[string]string, nextModelRegistrations map[string]pluginModelRegistration) {
	if h == nil || modelRegistry == nil {
		return
	}

	staleClients := make([]string, 0)
	h.mu.Lock()
	if h.Snapshot() != snap {
		h.mu.Unlock()
		return
	}
	for clientID := range h.modelClientIDs {
		if _, okClient := nextClients[clientID]; !okClient {
			staleClients = append(staleClients, clientID)
		}
	}
	h.modelClientIDs = nextClients
	h.modelProviders = nextProviders
	h.modelRegistrations = nextModelRegistrations
	h.mu.Unlock()

	for _, registration := range registrations {
		modelRegistry.RegisterClient(registration.clientID, registration.provider, registration.models)
	}
	for _, clientID := range staleClients {
		modelRegistry.UnregisterClient(clientID)
	}
}

type executorManager interface {
	Executor(provider string) (coreauth.ProviderExecutor, bool)
	RegisterExecutor(coreauth.ProviderExecutor)
	UnregisterExecutor(provider string)
}

type executorRegistration struct {
	provider string
	adapter  *executorAdapter
}

func (h *Host) RegisterExecutors(manager executorManager, modelRegistry modelProviderRegistry) {
	if h == nil || manager == nil {
		return
	}

	snap := h.Snapshot()
	registrations := h.snapshotModelRegistrations()
	selectedModels := make(map[string][]*registry.ModelInfo)
	providerModels := make(map[string][]*registry.ModelInfo)
	claimedModels := make(map[string]struct{})
	claimedProviders := make(map[string]string)
	for _, registration := range registrations {
		if !registration.hasExecutor {
			appendModelsForProvider(providerModels, registration.provider, registration.models)
		}
	}
	for _, record := range snap.records {
		executor := record.plugin.Capabilities.Executor
		if executor == nil || h.isPluginFused(record.id) {
			continue
		}
		provider, okProvider := h.executorProvider(record, executor)
		if !okProvider {
			continue
		}
		registration := h.modelRegistration(record.id)
		if h.providerHasNativeExecutor(manager, provider) {
			appendModelsForProvider(providerModels, provider, registration.models)
			continue
		}
		if len(registration.models) == 0 {
			continue
		}
		if owner := claimedProviders[provider]; owner != "" && owner != record.id {
			continue
		}
		for _, model := range registration.models {
			modelID := strings.TrimSpace(model.ID)
			if modelID == "" {
				continue
			}
			if _, claimed := claimedModels[modelID]; claimed {
				continue
			}
			if h.modelHasNativeExecutor(manager, modelRegistry, modelID) {
				continue
			}
			claimedModels[modelID] = struct{}{}
			claimedProviders[provider] = record.id
			selectedModels[record.id] = append(selectedModels[record.id], model)
		}
	}

	seenProviders := make(map[string]struct{})
	nextProviders := make(map[string]struct{})
	nextModelClients := make(map[string]struct{})
	executorRegistrations := make([]executorRegistration, 0)
	modelClientRegistrations := make([]modelClientRegistration, 0)
	for _, record := range snap.records {
		executor := record.plugin.Capabilities.Executor
		if executor == nil || h.isPluginFused(record.id) {
			continue
		}

		provider, okProvider := h.executorProvider(record, executor)
		if !okProvider {
			continue
		}
		registration := h.modelRegistration(record.id)
		if len(registration.models) > 0 && len(selectedModels[record.id]) == 0 {
			continue
		}
		if _, seenProvider := seenProviders[provider]; seenProvider {
			continue
		}
		seenProviders[provider] = struct{}{}
		if h.providerHasNativeExecutor(manager, provider) {
			continue
		}

		nextProviders[provider] = struct{}{}
		executorRegistrations = append(executorRegistrations, newExecutorAdapterRegistration(h, record, provider, executor))
		appendModelsForProvider(providerModels, provider, selectedModels[record.id])
		if len(selectedModels[record.id]) > 0 {
			clientID := pluginExecutorModelClientID(record.id, provider)
			modelClientRegistrations = append(modelClientRegistrations, modelClientRegistration{
				clientID: clientID,
				provider: provider,
				models:   selectedModels[record.id],
			})
			nextModelClients[clientID] = struct{}{}
		}
	}
	h.commitExecutorState(snap, manager, modelRegistry, providerModels, executorRegistrations, nextProviders, modelClientRegistrations, nextModelClients)
}

func pluginExecutorModelClientID(pluginID, provider string) string {
	return "plugin:" + pluginID + ":" + provider + ":executor"
}

func (h *Host) commitExecutorState(snap *Snapshot, manager executorManager, modelRegistry modelRegistry, providerModels map[string][]*registry.ModelInfo, registrations []executorRegistration, nextProviders map[string]struct{}, modelClientRegistrations []modelClientRegistration, nextModelClients map[string]struct{}) {
	if h == nil || manager == nil {
		return
	}

	h.mu.Lock()
	if h.Snapshot() != snap {
		h.mu.Unlock()
		return
	}

	h.providerModels = make(map[string][]*registryModelInfo, len(providerModels))
	for provider, models := range providerModels {
		h.providerModels[provider] = cloneRegistryModels(models)
	}

	staleProviders := make([]string, 0)
	for provider := range h.executorProviders {
		if _, okProvider := nextProviders[provider]; !okProvider {
			staleProviders = append(staleProviders, provider)
		}
	}
	h.executorProviders = nextProviders
	if nextModelClients == nil {
		nextModelClients = make(map[string]struct{})
	}
	staleModelClients := make([]string, 0)
	for clientID := range h.executorModelClientIDs {
		if _, okClient := nextModelClients[clientID]; !okClient {
			staleModelClients = append(staleModelClients, clientID)
		}
	}
	h.executorModelClientIDs = nextModelClients

	for _, registration := range registrations {
		if registration.adapter == nil || registration.provider == "" {
			continue
		}
		manager.RegisterExecutor(registration.adapter)
	}
	for _, provider := range staleProviders {
		existing, okExecutor := manager.Executor(provider)
		if !okExecutor || !h.ownsExecutor(existing) {
			continue
		}
		manager.UnregisterExecutor(provider)
	}
	h.mu.Unlock()

	if modelRegistry == nil {
		return
	}
	for _, registration := range modelClientRegistrations {
		modelRegistry.RegisterClient(registration.clientID, registration.provider, registration.models)
	}
	for _, clientID := range staleModelClients {
		modelRegistry.UnregisterClient(clientID)
	}
}

func newExecutorAdapterRegistration(h *Host, record capabilityRecord, provider string, executor pluginapi.ProviderExecutor) executorRegistration {
	return executorRegistration{
		provider: provider,
		adapter: &executorAdapter{
			host:     h,
			pluginID: record.id,
			provider: provider,
			executor: executor,
		},
	}
}

func (h *Host) snapshotModelRegistrations() []pluginModelRegistration {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	registrations := make([]pluginModelRegistration, 0, len(h.modelRegistrations))
	for _, registration := range h.modelRegistrations {
		registration.models = cloneRegistryModels(registration.models)
		registrations = append(registrations, registration)
	}
	sort.SliceStable(registrations, func(i, j int) bool {
		if registrations[i].priority == registrations[j].priority {
			return registrations[i].pluginID < registrations[j].pluginID
		}
		return registrations[i].priority > registrations[j].priority
	})
	return registrations
}

func (h *Host) modelRegistration(pluginID string) pluginModelRegistration {
	if h == nil {
		return pluginModelRegistration{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	registration := h.modelRegistrations[pluginID]
	registration.models = cloneRegistryModels(registration.models)
	return registration
}

func (h *Host) executorProvider(record capabilityRecord, executor pluginapi.ProviderExecutor) (string, bool) {
	provider := h.modelProvider(record.id)
	if provider == "" {
		identifier, okIdentifier := h.callExecutorIdentifier(record.id, executor)
		if !okIdentifier {
			return "", false
		}
		provider = identifier
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	return provider, provider != ""
}

func (h *Host) callExecutorIdentifier(pluginID string, executor pluginapi.ProviderExecutor) (provider string, ok bool) {
	if h == nil || executor == nil || h.isPluginFused(pluginID) {
		return "", false
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(pluginID, "Executor.Identifier", recovered)
			provider = ""
			ok = false
		}
	}()
	return executor.Identifier(), true
}

func (h *Host) providerHasNativeExecutor(manager executorManager, provider string) bool {
	if h == nil || manager == nil {
		return false
	}
	existing, okExecutor := manager.Executor(provider)
	return okExecutor && existing != nil && !h.ownsExecutor(existing)
}

func (h *Host) modelHasNativeExecutor(manager executorManager, modelRegistry modelProviderRegistry, modelID string) bool {
	if h == nil || manager == nil || modelRegistry == nil {
		return false
	}
	for _, provider := range modelRegistry.GetModelProviders(modelID) {
		if h.providerHasNativeExecutor(manager, provider) {
			return true
		}
	}
	return false
}

func appendModelsForProvider(out map[string][]*registry.ModelInfo, provider string, models []*registry.ModelInfo) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" || len(models) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(out[provider])+len(models))
	for _, model := range out[provider] {
		if model != nil && strings.TrimSpace(model.ID) != "" {
			seen[strings.TrimSpace(model.ID)] = struct{}{}
		}
	}
	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		if _, exists := seen[modelID]; exists {
			continue
		}
		seen[modelID] = struct{}{}
		out[provider] = append(out[provider], cloneRegistryModels([]*registry.ModelInfo{model})...)
	}
}

func (h *Host) ModelsForProvider(provider string) []*registry.ModelInfo {
	if h == nil {
		return nil
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return cloneRegistryModels(h.providerModels[provider])
}

func (h *Host) HasExecutorCandidateProvider(provider string) bool {
	if h == nil {
		return false
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return false
	}
	for _, record := range h.Snapshot().records {
		executor := record.plugin.Capabilities.Executor
		if executor == nil || h.isPluginFused(record.id) {
			continue
		}
		candidate, okCandidate := h.executorProvider(record, executor)
		if okCandidate && candidate == provider {
			return true
		}
	}
	return false
}

func (h *Host) ownsExecutor(executor coreauth.ProviderExecutor) bool {
	adapter, okAdapter := executor.(*executorAdapter)
	return okAdapter && adapter != nil && adapter.host == h
}

func (h *Host) modelProvider(pluginID string) string {
	if h == nil {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.modelProviders[pluginID]
}

func (h *Host) RegisterFrontendAuthProviders() {
	if h == nil {
		return
	}

	nextKeys := make(map[string]struct{})
	for _, record := range h.Snapshot().records {
		provider := record.plugin.Capabilities.FrontendAuthProvider
		if provider == nil || h.isPluginFused(record.id) {
			continue
		}
		adapter := &accessAdapter{
			host:     h,
			pluginID: record.id,
			provider: provider,
		}
		key := strings.TrimSpace(adapter.Identifier())
		if key == "" {
			continue
		}
		sdkaccess.RegisterProvider(key, adapter)
		nextKeys[key] = struct{}{}
	}

	h.pruneStaleAccessProviders(nextKeys)
}

func (h *Host) pruneStaleAccessProviders(nextKeys map[string]struct{}) {
	if h == nil {
		return
	}

	staleKeys := make([]string, 0)
	h.mu.Lock()
	for key := range h.accessProviderKeys {
		if _, okKey := nextKeys[key]; !okKey {
			staleKeys = append(staleKeys, key)
		}
	}
	h.accessProviderKeys = nextKeys
	h.mu.Unlock()

	for _, key := range staleKeys {
		sdkaccess.UnregisterProvider(key)
	}
}

func (h *Host) RegisterUsagePlugins() {
	if h == nil {
		return
	}

	for _, record := range h.Snapshot().records {
		plugin := record.plugin.Capabilities.UsagePlugin
		if plugin == nil || h.isPluginFused(record.id) {
			continue
		}
		coreusage.RegisterNamedPlugin("plugin:"+record.id, &usageAdapter{
			host:     h,
			pluginID: record.id,
			plugin:   plugin,
		})
	}
}

func (h *Host) refreshThinkingProviders(records []capabilityRecord) {
	thinking.ClearPluginProviders()
	if h == nil {
		return
	}
	for _, record := range records {
		applier := record.plugin.Capabilities.ThinkingApplier
		if applier == nil || h.isPluginFused(record.id) {
			continue
		}
		provider, okProvider := h.callThinkingIdentifier(record, applier)
		if !okProvider {
			continue
		}
		thinking.RegisterPluginProvider(record.id, provider, record.priority, &thinkingAdapter{
			host:     h,
			pluginID: record.id,
			provider: provider,
			applier:  applier,
		})
	}
}

func (h *Host) callThinkingIdentifier(record capabilityRecord, applier pluginapi.ThinkingApplier) (provider string, ok bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "ThinkingApplier.Identifier", recovered)
			provider = ""
			ok = false
		}
	}()
	provider = strings.ToLower(strings.TrimSpace(applier.Identifier()))
	if provider == "" {
		return "", false
	}
	return provider, true
}

func (h *Host) currentUsagePlugin(pluginID string) pluginapi.UsagePlugin {
	if h == nil || strings.TrimSpace(pluginID) == "" {
		return nil
	}
	for _, record := range h.Snapshot().records {
		if record.id != pluginID {
			continue
		}
		if h.isPluginFused(record.id) {
			return nil
		}
		return record.plugin.Capabilities.UsagePlugin
	}
	return nil
}

func (h *Host) fusePlugin(id, method string, recovered any) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.fused[id] = fmt.Sprintf("%s panic: %v", method, recovered)
	h.mu.Unlock()
	thinking.UnregisterPluginProviders(id)
	log.WithField("plugin_id", id).WithField("method", method).Errorf("pluginhost: plugin panic recovered: %v\n%s", recovered, debug.Stack())
}

func (h *Host) isPluginFused(id string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	_, fused := h.fused[id]
	h.mu.Unlock()
	return fused
}

type accessAdapter struct {
	host     *Host
	pluginID string
	provider pluginapi.FrontendAuthProvider
}

func (a *accessAdapter) Identifier() (identifier string) {
	if a == nil || a.provider == nil {
		return ""
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			if a.host != nil {
				a.host.fusePlugin(a.pluginID, "FrontendAuthProvider.Identifier", recovered)
			}
			identifier = ""
		}
	}()
	pluginID := strings.TrimSpace(a.pluginID)
	providerID := strings.TrimSpace(a.provider.Identifier())
	if pluginID == "" || providerID == "" {
		return ""
	}
	return "plugin:" + pluginID + ":" + providerID
}

func (a *accessAdapter) Authenticate(ctx context.Context, r *http.Request) (result *sdkaccess.Result, authErr *sdkaccess.AuthError) {
	if a == nil || a.provider == nil || a.host.isPluginFused(a.pluginID) {
		return nil, sdkaccess.NewNotHandledError()
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			a.host.fusePlugin(a.pluginID, "FrontendAuthProvider.Authenticate", recovered)
			result = nil
			authErr = sdkaccess.NewNotHandledError()
		}
	}()

	body, errReadAll := readAndRestoreRequestBody(r)
	if errReadAll != nil {
		return nil, sdkaccess.NewInternalAuthError("failed to read plugin auth request body", errReadAll)
	}
	resp, errAuthenticate := a.provider.Authenticate(ctx, pluginapi.FrontendAuthRequest{
		Method:  r.Method,
		Path:    r.URL.Path,
		Headers: cloneHeader(r.Header),
		Query:   cloneValues(r.URL.Query()),
		Body:    bytes.Clone(body),
	})
	if errAuthenticate != nil || !resp.Authenticated {
		return nil, sdkaccess.NewNotHandledError()
	}
	providerID := a.Identifier()
	if providerID == "" {
		return nil, sdkaccess.NewNotHandledError()
	}
	return &sdkaccess.Result{
		Provider:  providerID,
		Principal: resp.Principal,
		Metadata:  cloneStringMap(resp.Metadata),
	}, nil
}

type executorAdapter struct {
	host     *Host
	pluginID string
	provider string
	executor pluginapi.ProviderExecutor
}

func (a *executorAdapter) Identifier() string {
	if a == nil {
		return ""
	}
	return a.provider
}

func (a *executorAdapter) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (resp coreexecutor.Response, err error) {
	if a == nil || a.executor == nil || a.host.isPluginFused(a.pluginID) {
		return coreexecutor.Response{}, fmt.Errorf("plugin executor %s is unavailable", a.Identifier())
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			a.host.fusePlugin(a.pluginID, "Executor.Execute", recovered)
			resp = coreexecutor.Response{}
			err = fmt.Errorf("plugin executor %s panic: %v", a.Identifier(), recovered)
		}
	}()

	pluginResp, errExecute := a.executor.Execute(ctx, buildExecutorRequest(a.host, a.provider, auth, req, opts))
	if errExecute != nil {
		return coreexecutor.Response{}, errExecute
	}
	return coreexecutor.Response{
		Payload:  bytes.Clone(pluginResp.Payload),
		Metadata: cloneAnyMap(pluginResp.Metadata),
		Headers:  cloneHeader(pluginResp.Headers),
	}, nil
}

func (a *executorAdapter) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (result *coreexecutor.StreamResult, err error) {
	if a == nil || a.executor == nil || a.host.isPluginFused(a.pluginID) {
		return nil, fmt.Errorf("plugin executor %s is unavailable", a.Identifier())
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			a.host.fusePlugin(a.pluginID, "Executor.ExecuteStream", recovered)
			result = nil
			err = fmt.Errorf("plugin executor %s stream panic: %v", a.Identifier(), recovered)
		}
	}()

	pluginResp, errExecuteStream := a.executor.ExecuteStream(ctx, buildExecutorRequest(a.host, a.provider, auth, req, opts))
	if errExecuteStream != nil {
		return nil, errExecuteStream
	}
	return &coreexecutor.StreamResult{
		Headers: cloneHeader(pluginResp.Headers),
		Chunks:  mapExecutorStreamChunks(ctx, pluginResp.Chunks),
	}, nil
}

func (a *executorAdapter) Refresh(ctx context.Context, auth *coreauth.Auth) (refreshed *coreauth.Auth, err error) {
	if a == nil || a.executor == nil || a.host.isPluginFused(a.pluginID) {
		return nil, fmt.Errorf("plugin executor %s is unavailable", a.Identifier())
	}
	record := a.host.authProviderRecord(authProvider(auth))
	if record == nil || record.plugin.Capabilities.AuthProvider == nil {
		return auth.Clone(), nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			a.host.fusePlugin(record.id, "AuthProvider.RefreshAuth", recovered)
			refreshed = nil
			err = fmt.Errorf("plugin executor %s refresh panic: %v", a.Identifier(), recovered)
		}
	}()

	pluginResp, errRefresh := record.plugin.Capabilities.AuthProvider.RefreshAuth(ctx, pluginapi.AuthRefreshRequest{
		AuthID:       authID(auth),
		AuthProvider: authProvider(auth),
		StorageJSON:  storageJSONFromAuth(auth),
		Metadata:     cloneAnyMap(authMetadata(auth)),
		Attributes:   authAttributes(auth),
		Host:         a.host.hostConfigSummary(),
		HTTPClient:   a.host.newHTTPClient(auth),
	})
	if errRefresh != nil {
		return nil, errRefresh
	}
	data := pluginResp.Auth
	if strings.TrimSpace(data.Provider) == "" {
		data.Provider = authProvider(auth)
	}
	if strings.TrimSpace(data.ID) == "" {
		data.ID = authID(auth)
	}
	if strings.TrimSpace(data.FileName) == "" && auth != nil {
		data.FileName = auth.FileName
	}
	if strings.TrimSpace(data.Label) == "" && auth != nil {
		data.Label = auth.Label
	}
	if strings.TrimSpace(data.Prefix) == "" && auth != nil {
		data.Prefix = auth.Prefix
	}
	if strings.TrimSpace(data.ProxyURL) == "" && auth != nil {
		data.ProxyURL = auth.ProxyURL
	}
	if len(data.Metadata) == 0 && auth != nil {
		data.Metadata = cloneAnyMap(auth.Metadata)
	}
	if len(data.Attributes) == 0 && auth != nil {
		data.Attributes = cloneStringMap(auth.Attributes)
	}
	if len(data.StorageJSON) == 0 {
		data.StorageJSON = storageJSONFromAuth(auth)
	}
	if pluginResp.NextRefreshAfter.IsZero() && auth != nil {
		data.NextRefreshAfter = auth.NextRefreshAfter
	}
	if !pluginResp.NextRefreshAfter.IsZero() {
		data.NextRefreshAfter = pluginResp.NextRefreshAfter
	}
	next := a.host.AuthDataToCoreAuth(data, "", data.FileName)
	if next == nil {
		return nil, fmt.Errorf("plugin executor %s refresh returned invalid auth data", a.Identifier())
	}
	if auth != nil {
		next.CreatedAt = auth.CreatedAt
		next.UpdatedAt = auth.UpdatedAt
	}
	return next, nil
}

func (a *executorAdapter) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (resp coreexecutor.Response, err error) {
	if a == nil || a.executor == nil || a.host.isPluginFused(a.pluginID) {
		return coreexecutor.Response{}, fmt.Errorf("plugin executor %s is unavailable", a.Identifier())
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			a.host.fusePlugin(a.pluginID, "Executor.CountTokens", recovered)
			resp = coreexecutor.Response{}
			err = fmt.Errorf("plugin executor %s count tokens panic: %v", a.Identifier(), recovered)
		}
	}()

	pluginResp, errCountTokens := a.executor.CountTokens(ctx, buildExecutorRequest(a.host, a.provider, auth, req, opts))
	if errCountTokens != nil {
		return coreexecutor.Response{}, errCountTokens
	}
	return coreexecutor.Response{
		Payload:  bytes.Clone(pluginResp.Payload),
		Metadata: cloneAnyMap(pluginResp.Metadata),
		Headers:  cloneHeader(pluginResp.Headers),
	}, nil
}

func (a *executorAdapter) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (resp *http.Response, err error) {
	if a == nil || a.executor == nil || a.host.isPluginFused(a.pluginID) {
		return nil, fmt.Errorf("plugin executor %s is unavailable", a.Identifier())
	}
	if req == nil {
		return nil, fmt.Errorf("plugin executor %s received nil HTTP request", a.Identifier())
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			a.host.fusePlugin(a.pluginID, "Executor.HttpRequest", recovered)
			resp = nil
			err = fmt.Errorf("plugin executor %s http request panic: %v", a.Identifier(), recovered)
		}
	}()
	body, errReadAll := readAndRestoreRequestBody(req)
	if errReadAll != nil {
		return nil, fmt.Errorf("read plugin http request body: %w", errReadAll)
	}
	pluginResp, errHTTPRequest := a.executor.HttpRequest(ctx, pluginapi.ExecutorHTTPRequest{
		AuthID:       authID(auth),
		AuthProvider: authProvider(auth),
		Method:       req.Method,
		URL:          req.URL.String(),
		Headers:      cloneHeader(req.Header),
		Body:         bytes.Clone(body),
		StorageJSON:  storageJSONFromAuth(auth),
		Metadata:     cloneAnyMap(authMetadata(auth)),
		Attributes:   authAttributes(auth),
		HTTPClient:   a.host.newHTTPClient(auth, a.provider),
	})
	if errHTTPRequest != nil {
		return nil, errHTTPRequest
	}
	status := pluginResp.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	resp = &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     cloneHeader(pluginResp.Headers),
		Body:       io.NopCloser(bytes.NewReader(bytes.Clone(pluginResp.Body))),
		Request:    req,
	}
	return resp, nil
}

type usageAdapter struct {
	host     *Host
	pluginID string
	plugin   pluginapi.UsagePlugin
}

type thinkingAdapter struct {
	host     *Host
	pluginID string
	provider string
	applier  pluginapi.ThinkingApplier
}

func (a *usageAdapter) HandleUsage(ctx context.Context, record coreusage.Record) {
	if a == nil {
		return
	}
	plugin := a.host.currentUsagePlugin(a.pluginID)
	if plugin == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			a.host.fusePlugin(a.pluginID, "UsagePlugin.HandleUsage", recovered)
		}
	}()
	plugin.HandleUsage(ctx, pluginapi.UsageRecord{
		Provider:        record.Provider,
		ExecutorType:    record.ExecutorType,
		Model:           record.Model,
		Alias:           record.Alias,
		APIKey:          record.APIKey,
		AuthID:          record.AuthID,
		AuthIndex:       record.AuthIndex,
		AuthType:        record.AuthType,
		Source:          record.Source,
		ReasoningEffort: record.ReasoningEffort,
		ServiceTier:     record.ServiceTier,
		RequestedAt:     record.RequestedAt,
		Latency:         record.Latency,
		TTFT:            record.TTFT,
		Failed:          record.Failed,
		Failure: pluginapi.UsageFailure{
			StatusCode: record.Fail.StatusCode,
			Body:       record.Fail.Body,
		},
		Detail: pluginapi.UsageDetail{
			InputTokens:         record.Detail.InputTokens,
			OutputTokens:        record.Detail.OutputTokens,
			ReasoningTokens:     record.Detail.ReasoningTokens,
			CachedTokens:        record.Detail.CachedTokens,
			CacheReadTokens:     record.Detail.CacheReadTokens,
			CacheCreationTokens: record.Detail.CacheCreationTokens,
			TotalTokens:         record.Detail.TotalTokens,
		},
		ResponseHeaders: cloneHeader(record.ResponseHeaders),
	})
}

func (a *thinkingAdapter) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) (out []byte, err error) {
	if a == nil || a.applier == nil || a.host == nil || a.host.isPluginFused(a.pluginID) {
		return bytes.Clone(body), nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			a.host.fusePlugin(a.pluginID, "ThinkingApplier.ApplyThinking", recovered)
			out = bytes.Clone(body)
			err = nil
		}
	}()
	resp, errApply := a.applier.ApplyThinking(context.Background(), pluginapi.ThinkingApplyRequest{
		Provider: a.provider,
		Model:    registryModelInfoToPluginModelInfo(modelInfo),
		Config: pluginapi.ThinkingConfig{
			Mode:   config.Mode.String(),
			Budget: config.Budget,
			Level:  string(config.Level),
		},
		Body: bytes.Clone(body),
	})
	if errApply != nil || len(resp.Body) == 0 {
		return bytes.Clone(body), nil
	}
	return bytes.Clone(resp.Body), nil
}

func (h *Host) NormalizeRequest(ctx context.Context, from, to sdktranslator.Format, model string, body []byte, stream bool) []byte {
	current := bytes.Clone(body)
	for _, record := range h.Snapshot().records {
		if h.isPluginFused(record.id) || record.plugin.Capabilities.RequestNormalizer == nil {
			continue
		}
		if normalized, ok := h.callRequestNormalizer(ctx, record, from, to, model, current, stream); ok {
			current = normalized
		}
	}
	return current
}

func (h *Host) TranslateRequest(ctx context.Context, from, to sdktranslator.Format, model string, body []byte, stream bool) ([]byte, bool) {
	for _, record := range h.Snapshot().records {
		if h.isPluginFused(record.id) || record.plugin.Capabilities.RequestTranslator == nil {
			continue
		}
		if translated, ok := h.callRequestTranslator(ctx, record, from, to, model, body, stream); ok {
			return translated, true
		}
	}
	return bytes.Clone(body), false
}

func (h *Host) NormalizeResponseBefore(ctx context.Context, from, to sdktranslator.Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) []byte {
	current := bytes.Clone(body)
	for _, record := range h.Snapshot().records {
		normalizer := record.plugin.Capabilities.ResponseBeforeTranslator
		if h.isPluginFused(record.id) || normalizer == nil {
			continue
		}
		if normalized, ok := h.callResponseNormalizer(ctx, record.id, "ResponseBeforeTranslator.NormalizeResponse", normalizer, from, to, model, originalRequestRawJSON, requestRawJSON, current, stream); ok {
			current = normalized
		}
	}
	return current
}

func (h *Host) TranslateResponse(ctx context.Context, from, to sdktranslator.Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) ([]byte, bool) {
	for _, record := range h.Snapshot().records {
		translator := record.plugin.Capabilities.ResponseTranslator
		if h.isPluginFused(record.id) || translator == nil {
			continue
		}
		if translated, ok := h.callResponseTranslator(ctx, record.id, translator, from, to, model, originalRequestRawJSON, requestRawJSON, body, stream); ok {
			return translated, true
		}
	}
	return bytes.Clone(body), false
}

func (h *Host) NormalizeResponseAfter(ctx context.Context, from, to sdktranslator.Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) []byte {
	current := bytes.Clone(body)
	for _, record := range h.Snapshot().records {
		normalizer := record.plugin.Capabilities.ResponseAfterTranslator
		if h.isPluginFused(record.id) || normalizer == nil {
			continue
		}
		if normalized, ok := h.callResponseNormalizer(ctx, record.id, "ResponseAfterTranslator.NormalizeResponse", normalizer, from, to, model, originalRequestRawJSON, requestRawJSON, current, stream); ok {
			current = normalized
		}
	}
	return current
}

func (h *Host) callRequestNormalizer(ctx context.Context, record capabilityRecord, from, to sdktranslator.Format, model string, body []byte, stream bool) (out []byte, ok bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "RequestNormalizer.NormalizeRequest", recovered)
			out = nil
			ok = false
		}
	}()
	resp, errNormalizeRequest := record.plugin.Capabilities.RequestNormalizer.NormalizeRequest(ctx, pluginapi.RequestTransformRequest{
		FromFormat: from.String(),
		ToFormat:   to.String(),
		Model:      model,
		Stream:     stream,
		Body:       bytes.Clone(body),
	})
	if errNormalizeRequest != nil || len(resp.Body) == 0 {
		return nil, false
	}
	return bytes.Clone(resp.Body), true
}

func (h *Host) callRequestTranslator(ctx context.Context, record capabilityRecord, from, to sdktranslator.Format, model string, body []byte, stream bool) (out []byte, ok bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "RequestTranslator.TranslateRequest", recovered)
			out = nil
			ok = false
		}
	}()
	resp, errTranslateRequest := record.plugin.Capabilities.RequestTranslator.TranslateRequest(ctx, pluginapi.RequestTransformRequest{
		FromFormat: from.String(),
		ToFormat:   to.String(),
		Model:      model,
		Stream:     stream,
		Body:       bytes.Clone(body),
	})
	if errTranslateRequest != nil || len(resp.Body) == 0 {
		return nil, false
	}
	return bytes.Clone(resp.Body), true
}

func (h *Host) callResponseNormalizer(ctx context.Context, pluginID, method string, normalizer pluginapi.ResponseNormalizer, from, to sdktranslator.Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) (out []byte, ok bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(pluginID, method, recovered)
			out = nil
			ok = false
		}
	}()
	resp, errNormalizeResponse := normalizer.NormalizeResponse(ctx, pluginapi.ResponseTransformRequest{
		FromFormat:        from.String(),
		ToFormat:          to.String(),
		Model:             model,
		Stream:            stream,
		OriginalRequest:   bytes.Clone(originalRequestRawJSON),
		TranslatedRequest: bytes.Clone(requestRawJSON),
		Body:              bytes.Clone(body),
	})
	if errNormalizeResponse != nil || len(resp.Body) == 0 {
		return nil, false
	}
	return bytes.Clone(resp.Body), true
}

func (h *Host) callResponseTranslator(ctx context.Context, pluginID string, translator pluginapi.ResponseTranslator, from, to sdktranslator.Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) (out []byte, ok bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(pluginID, "ResponseTranslator.TranslateResponse", recovered)
			out = nil
			ok = false
		}
	}()
	resp, errTranslateResponse := translator.TranslateResponse(ctx, pluginapi.ResponseTransformRequest{
		FromFormat:        from.String(),
		ToFormat:          to.String(),
		Model:             model,
		Stream:            stream,
		OriginalRequest:   bytes.Clone(originalRequestRawJSON),
		TranslatedRequest: bytes.Clone(requestRawJSON),
		Body:              bytes.Clone(body),
	})
	if errTranslateResponse != nil || len(resp.Body) == 0 {
		return nil, false
	}
	return bytes.Clone(resp.Body), true
}

func buildExecutorRequest(host *Host, provider string, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) pluginapi.ExecutorRequest {
	return pluginapi.ExecutorRequest{
		AuthID:          authID(auth),
		AuthProvider:    authProvider(auth),
		Model:           req.Model,
		Format:          req.Format.String(),
		Stream:          opts.Stream,
		Alt:             opts.Alt,
		Headers:         cloneHeader(opts.Headers),
		Query:           cloneValues(opts.Query),
		OriginalRequest: bytes.Clone(opts.OriginalRequest),
		SourceFormat:    opts.SourceFormat.String(),
		Payload:         bytes.Clone(req.Payload),
		Metadata:        mergeExecutorMetadata(req.Metadata, opts.Metadata),
		StorageJSON:     storageJSONFromAuth(auth),
		AuthMetadata:    cloneAnyMap(authMetadata(auth)),
		AuthAttributes:  authAttributes(auth),
		HTTPClient:      host.newHTTPClient(auth, provider),
	}
}

func storageJSONFromAuth(auth *coreauth.Auth) []byte {
	if auth == nil {
		return nil
	}
	if rawProvider, okRaw := auth.Storage.(interface{ RawJSON() []byte }); okRaw {
		return bytes.Clone(rawProvider.RawJSON())
	}
	if len(auth.Metadata) == 0 {
		return nil
	}
	data, errMarshal := json.Marshal(auth.Metadata)
	if errMarshal != nil {
		return nil
	}
	return data
}

func authAttributes(auth *coreauth.Auth) map[string]string {
	if auth == nil {
		return nil
	}
	return cloneStringMap(auth.Attributes)
}

func mergeExecutorMetadata(reqMetadata, optsMetadata map[string]any) map[string]any {
	if len(reqMetadata) == 0 && len(optsMetadata) == 0 {
		return nil
	}
	merged := make(map[string]any, len(reqMetadata)+len(optsMetadata))
	for key, value := range reqMetadata {
		merged[key] = value
	}
	for key, value := range optsMetadata {
		merged[key] = value
	}
	return merged
}

func mapExecutorStreamChunks(ctx context.Context, in <-chan pluginapi.ExecutorStreamChunk) <-chan coreexecutor.StreamChunk {
	if ctx == nil {
		ctx = context.Background()
	}
	out := make(chan coreexecutor.StreamChunk)
	if in == nil {
		close(out)
		return out
	}
	go func() {
		defer close(out)
		for {
			var mapped coreexecutor.StreamChunk
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-in:
				if !ok {
					return
				}
				mapped = coreexecutor.StreamChunk{
					Payload: bytes.Clone(chunk.Payload),
					Err:     chunk.Err,
				}
			}
			select {
			case <-ctx.Done():
				return
			case out <- mapped:
			}
		}
	}()
	return out
}

func readAndRestoreRequestBody(r *http.Request) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, nil
	}
	body, errReadAll := io.ReadAll(r.Body)
	if errReadAll != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
		return nil, errReadAll
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func authID(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.ID
}

func authProvider(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.Provider
}

func authMetadata(auth *coreauth.Auth) map[string]any {
	if auth == nil {
		return nil
	}
	return auth.Metadata
}

func cloneHeader(in http.Header) http.Header {
	if len(in) == 0 {
		return nil
	}
	out := make(http.Header, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func cloneValues(in url.Values) url.Values {
	if len(in) == 0 {
		return nil
	}
	out := make(url.Values, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
