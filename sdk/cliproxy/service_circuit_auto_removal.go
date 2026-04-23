package cliproxy

import (
	"context"
	"fmt"
	"strings"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/store/mongostate"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	log "github.com/sirupsen/logrus"
)

func (s *Service) initCircuitBreakerAutoRemoval(ctx context.Context) error {
	if s == nil {
		return nil
	}
	enabled, _ := s.circuitBreakerAutoRemovalSetting()
	if !enabled {
		registry.GetGlobalRegistry().SetCircuitBreakerOpenHook(nil)
		if s.circuitBreakerDeletionStore != nil {
			_ = s.circuitBreakerDeletionStore.Close(context.Background())
			s.circuitBreakerDeletionStore = nil
		}
		mongostate.SetGlobalCircuitBreakerDeletionStore(nil)
		return nil
	}

	runtimeCfg, _, _, err := mongostate.LoadRuntimeConfig(s.configPath)
	if err != nil {
		return fmt.Errorf("load state-store config failed: %w", err)
	}
	mongostate.ApplyEnvOverrides(&runtimeCfg)
	if !runtimeCfg.Enabled || strings.TrimSpace(runtimeCfg.URI) == "" {
		return fmt.Errorf("circuit-breaker auto-removal enabled requires Mongo state-store (state-store config enabled=true and uri set)")
	}

	storeCfg := runtimeCfg.ToStoreConfig("circuit-breaker-auto-removal")
	auditStore, err := mongostate.NewCircuitBreakerDeletionStore(ctx, storeCfg, mongostate.DefaultCircuitBreakerDeletionCollection, mongostate.DefaultCircuitBreakerDeletionTTLDays)
	if err != nil {
		return fmt.Errorf("init circuit-breaker deletion store failed: %w", err)
	}
	if s.circuitBreakerDeletionStore != nil {
		_ = s.circuitBreakerDeletionStore.Close(context.Background())
	}
	s.circuitBreakerDeletionStore = auditStore
	mongostate.SetGlobalCircuitBreakerDeletionStore(auditStore)
	registry.GetGlobalRegistry().SetCircuitBreakerOpenHook(s)
	return nil
}

func (s *Service) circuitBreakerAutoRemovalSetting() (enabled bool, threshold int) {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	if s == nil || s.cfg == nil {
		return false, internalconfig.DefaultCircuitBreakerAutoRemoveThreshold
	}
	return s.cfg.CircuitBreakerAutoRemoval.EnabledOrDefault(), s.cfg.CircuitBreakerAutoRemoval.ThresholdOrDefault()
}

// OnCircuitBreakerOpened handles automatic model removal after repeated OPEN transitions.
func (s *Service) OnCircuitBreakerOpened(ctx context.Context, event registry.CircuitBreakerOpenEvent) {
	if s == nil {
		return
	}
	enabled, threshold := s.circuitBreakerAutoRemovalSetting()
	if !enabled || event.OpenCycles < threshold {
		return
	}

	s.circuitAutoRemoveMu.Lock()
	defer s.circuitAutoRemoveMu.Unlock()

	auth, ok := s.latestAuthForModelRegistration(event.ClientID)
	if !ok || auth == nil {
		log.Warnf("circuit auto-removal skipped: auth not found (auth=%s model=%s)", event.ClientID, event.ModelID)
		return
	}

	normalizedModel := normalizeModelForAutoRemoval(auth, event.ModelID)
	runtimeSuspended := false
	if strings.TrimSpace(event.ModelID) != "" {
		registry.GetGlobalRegistry().SuspendClientModel(auth.ID, event.ModelID, "circuit_auto_removed")
		runtimeSuspended = true
	}

	persisted, alreadyRemoved, persistErr := s.persistCircuitBreakerAutoRemoval(auth, normalizedModel)
	if persistErr == nil {
		s.refreshModelRegistrationForAuth(auth)
	}

	record := &mongostate.CircuitBreakerDeletionRecord{
		AuthID:              auth.ID,
		Provider:            strings.ToLower(strings.TrimSpace(auth.Provider)),
		Model:               strings.TrimSpace(event.ModelID),
		NormalizedModel:     normalizedModel,
		OpenCycles:          event.OpenCycles,
		FailureCount:        event.FailureCount,
		ConsecutiveFailures: event.ConsecutiveFailures,
		OpenedAt:            event.OpenedAt,
		Persisted:           persisted,
		AlreadyRemoved:      alreadyRemoved,
		RuntimeSuspended:    runtimeSuspended,
		CreatedAt:           time.Now().UTC(),
	}
	if !event.RecoveryAt.IsZero() {
		recoveryAt := event.RecoveryAt.UTC()
		record.RecoveryAt = &recoveryAt
	}
	if persistErr != nil {
		record.PersistError = persistErr.Error()
		log.Warnf("circuit auto-removal persist failed (auth=%s model=%s normalized=%s): %v", auth.ID, event.ModelID, normalizedModel, persistErr)
	}

	if s.circuitBreakerDeletionStore != nil {
		if err := s.circuitBreakerDeletionStore.Insert(ctx, record); err != nil {
			log.Warnf("failed to insert circuit-breaker deletion audit (auth=%s model=%s): %v", auth.ID, event.ModelID, err)
		}
	}
}

func normalizeModelForAutoRemoval(auth *coreauth.Auth, modelID string) string {
	model := strings.TrimSpace(modelID)
	if model == "" {
		return ""
	}
	parsed := thinking.ParseSuffix(model)
	if base := strings.TrimSpace(parsed.ModelName); base != "" {
		model = base
	}
	if auth != nil {
		prefix := strings.TrimSpace(auth.Prefix)
		if prefix != "" {
			prefixWithSlash := prefix + "/"
			if strings.HasPrefix(model, prefixWithSlash) {
				model = strings.TrimPrefix(model, prefixWithSlash)
			}
		}
	}
	if idx := strings.LastIndex(model, ":"); idx > 0 {
		switch strings.ToLower(strings.TrimSpace(model[idx+1:])) {
		case "low", "medium", "high", "xhigh":
			model = strings.TrimSpace(model[:idx])
		}
	}
	return strings.TrimSpace(model)
}

func authKindForAutoRemoval(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if v := strings.ToLower(strings.TrimSpace(auth.Attributes["auth_kind"])); v != "" {
			return v
		}
	}
	if kind, _ := auth.AccountInfo(); strings.EqualFold(strings.TrimSpace(kind), "api_key") {
		return "apikey"
	}
	return "oauth"
}

func (s *Service) persistCircuitBreakerAutoRemoval(auth *coreauth.Auth, model string) (persisted bool, alreadyRemoved bool, err error) {
	if s == nil || auth == nil || strings.TrimSpace(auth.ID) == "" {
		return false, false, fmt.Errorf("invalid auth")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return false, false, fmt.Errorf("empty model")
	}

	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	if s.cfg == nil {
		return false, false, fmt.Errorf("config unavailable")
	}
	if strings.TrimSpace(s.configPath) == "" {
		return false, false, fmt.Errorf("config path missing")
	}

	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	authKind := authKindForAutoRemoval(auth)
	changed := false

	switch provider {
	case "openai-compatibility":
		entry := s.resolveOpenAICompatConfig(auth)
		if entry == nil {
			return false, false, fmt.Errorf("openai-compat config entry not found")
		}
		var removed bool
		entry.Models, removed = removeConfigModelsByTarget(entry.Models, model)
		alreadyRemoved = !removed
		changed = removed
		s.cfg.SanitizeOpenAICompatibility()
	case "codex":
		entry := s.resolveConfigCodexKey(auth)
		if entry == nil {
			return false, false, fmt.Errorf("codex api-key config entry not found")
		}
		var removed bool
		entry.Models, removed = removeConfigModelsByTarget(entry.Models, model)
		var excludedAlready, excludedChanged bool
		entry.ExcludedModels, excludedAlready, excludedChanged = ensureExcludedModel(entry.ExcludedModels, model)
		alreadyRemoved = !removed && excludedAlready
		changed = removed || excludedChanged
		s.cfg.SanitizeCodexKeys()
	case "gemini":
		entry := s.resolveConfigGeminiKey(auth)
		if entry == nil {
			return false, false, fmt.Errorf("gemini api-key config entry not found")
		}
		var removed bool
		entry.Models, removed = removeConfigModelsByTarget(entry.Models, model)
		var excludedAlready, excludedChanged bool
		entry.ExcludedModels, excludedAlready, excludedChanged = ensureExcludedModel(entry.ExcludedModels, model)
		alreadyRemoved = !removed && excludedAlready
		changed = removed || excludedChanged
		s.cfg.SanitizeGeminiKeys()
	case "claude":
		entry := s.resolveConfigClaudeKey(auth)
		if entry == nil {
			return false, false, fmt.Errorf("claude api-key config entry not found")
		}
		var removed bool
		entry.Models, removed = removeConfigModelsByTarget(entry.Models, model)
		var excludedAlready, excludedChanged bool
		entry.ExcludedModels, excludedAlready, excludedChanged = ensureExcludedModel(entry.ExcludedModels, model)
		alreadyRemoved = !removed && excludedAlready
		changed = removed || excludedChanged
		s.cfg.SanitizeClaudeKeys()
	case "vertex":
		entry := s.resolveConfigVertexCompatKey(auth)
		if entry == nil {
			return false, false, fmt.Errorf("vertex api-key config entry not found")
		}
		var removed bool
		entry.Models, removed = removeConfigModelsByTarget(entry.Models, model)
		var excludedAlready, excludedChanged bool
		entry.ExcludedModels, excludedAlready, excludedChanged = ensureExcludedModel(entry.ExcludedModels, model)
		alreadyRemoved = !removed && excludedAlready
		changed = removed || excludedChanged
		s.cfg.SanitizeVertexCompatKeys()
	default:
		if authKind == "apikey" {
			return false, false, fmt.Errorf("unsupported api-key provider for auto-removal: %s", provider)
		}
		if s.cfg.OAuthExcludedModels == nil {
			s.cfg.OAuthExcludedModels = make(map[string][]string)
		}
		existing := s.cfg.OAuthExcludedModels[provider]
		updated, excludedAlready, excludedChanged := ensureExcludedModel(existing, model)
		alreadyRemoved = excludedAlready
		changed = excludedChanged
		s.cfg.OAuthExcludedModels[provider] = updated
		s.cfg.OAuthExcludedModels = internalconfig.NormalizeOAuthExcludedModels(s.cfg.OAuthExcludedModels)
	}

	if !changed {
		return false, alreadyRemoved, nil
	}
	if err := sdkconfig.SaveConfigPreserveComments(s.configPath, s.cfg); err != nil {
		return false, alreadyRemoved, err
	}
	return true, alreadyRemoved, nil
}

func ensureExcludedModel(existing []string, model string) ([]string, bool, bool) {
	target := strings.ToLower(strings.TrimSpace(model))
	if target == "" {
		return existing, false, false
	}
	normalized := internalconfig.NormalizeExcludedModels(existing)
	for _, item := range normalized {
		if strings.EqualFold(strings.TrimSpace(item), target) {
			return normalized, true, false
		}
	}
	normalized = append(normalized, target)
	normalized = internalconfig.NormalizeExcludedModels(normalized)
	return normalized, false, true
}

func removeConfigModelsByTarget[T modelEntry](models []T, targetModel string) ([]T, bool) {
	target := strings.ToLower(strings.TrimSpace(targetModel))
	if target == "" || len(models) == 0 {
		return models, false
	}
	out := make([]T, 0, len(models))
	removed := false
	for i := range models {
		entry := models[i]
		name := strings.ToLower(strings.TrimSpace(entry.GetName()))
		alias := strings.ToLower(strings.TrimSpace(entry.GetAlias()))
		if target == alias || target == name {
			removed = true
			continue
		}
		out = append(out, entry)
	}
	return out, removed
}

func (s *Service) resolveOpenAICompatConfig(auth *coreauth.Auth) *sdkconfig.OpenAICompatibility {
	if s == nil || auth == nil || s.cfg == nil {
		return nil
	}
	candidates := make([]string, 0, 4)
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["compat_name"]); v != "" {
			candidates = append(candidates, v)
		}
		if v := strings.TrimSpace(auth.Attributes["provider_key"]); v != "" {
			candidates = append(candidates, v)
		}
	}
	if v := strings.TrimSpace(auth.Label); v != "" {
		candidates = append(candidates, v)
	}
	if v := strings.TrimSpace(auth.Provider); v != "" {
		candidates = append(candidates, v)
	}
	for i := range s.cfg.OpenAICompatibility {
		entry := &s.cfg.OpenAICompatibility[i]
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(entry.Name)) {
				return entry
			}
		}
	}
	if _, compatName, ok := openAICompatInfoFromAuth(auth); ok && compatName != "" {
		for i := range s.cfg.OpenAICompatibility {
			entry := &s.cfg.OpenAICompatibility[i]
			if strings.EqualFold(strings.TrimSpace(entry.Name), compatName) {
				return entry
			}
		}
	}
	return nil
}
