package pluginhost

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	_ "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator/builtin"
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

func normalizeExecutorFormats(raw []string) []sdktranslator.Format {
	if len(raw) == 0 {
		return nil
	}
	out := make([]sdktranslator.Format, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		format := normalizeExecutorFormatName(item)
		if format == "" {
			continue
		}
		key := format.String()
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, format)
	}
	return out
}

func normalizeExecutorFormatName(raw string) sdktranslator.Format {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "none":
		return ""
	case "chat-completions", "chat_completions", "openai-chat-completions", "openai_chat_completions":
		return sdktranslator.FormatOpenAI
	case "responses", "openai-responses", "openai_responses":
		return sdktranslator.FormatOpenAIResponse
	case "anthropic":
		return sdktranslator.FormatClaude
	default:
		return sdktranslator.FromString(strings.TrimSpace(raw))
	}
}

func executorFormatContains(formats []sdktranslator.Format, target sdktranslator.Format) bool {
	if target == "" {
		return false
	}
	for _, format := range formats {
		if format == target {
			return true
		}
	}
	return false
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
	records := h.activeRecordsFromSnapshot(snap)
	registrations := make([]modelClientRegistration, 0)
	nextClients := make(map[string]struct{})
	nextProviders := make(map[string]string)
	nextModelRegistrations := make(map[string]pluginModelRegistration)
	for _, record := range records {
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
	for _, record := range h.activeRecords() {
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
	if h == nil || registrar == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
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
	if h == nil || provider == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
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
	if h == nil || provider == nil || auth == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
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
