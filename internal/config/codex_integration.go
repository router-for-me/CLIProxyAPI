package config

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
)

var codexIntegrationProviders = map[string]struct{}{
	"antigravity": {},
	"xai":         {},
}

// NormalizeCodexIntegration normalizes and validates the optional Codex integration config.
func (cfg *Config) NormalizeCodexIntegration() error {
	if cfg == nil {
		return nil
	}

	integration := &cfg.CodexIntegration
	integration.CodexHome = strings.TrimSpace(integration.CodexHome)
	integration.CatalogFile = strings.TrimSpace(integration.CatalogFile)
	if integration.CatalogFile == "" {
		integration.CatalogFile = DefaultCodexCatalogFile
	}
	integration.MultiAgentMode = strings.ToLower(strings.TrimSpace(integration.MultiAgentMode))
	if integration.MultiAgentMode == "" {
		integration.MultiAgentMode = DefaultCodexMultiAgentMode
	}
	if integration.Models == nil {
		integration.Models = DefaultCodexIntegrationConfig().Models
	}

	if err := validateCodexCatalogFile(integration.CatalogFile); err != nil {
		return err
	}
	if integration.MultiAgentMode != DefaultCodexMultiAgentMode {
		return fmt.Errorf("codex-integration.multi-agent-mode: unsupported value %q; only %q is currently verified", integration.MultiAgentMode, DefaultCodexMultiAgentMode)
	}
	if integration.Enabled && integration.LoopbackAccess && !isStrictLoopbackHost(cfg.Host) {
		return fmt.Errorf("codex-integration.loopback-access: host %q is not a strict loopback address", cfg.Host)
	}

	seen := make(map[string]struct{}, len(integration.Models))
	featured := 0
	for i := range integration.Models {
		model := &integration.Models[i]
		model.Slug = strings.ToLower(strings.TrimSpace(model.Slug))
		model.Provider = strings.ToLower(strings.TrimSpace(model.Provider))
		model.UpstreamModel = strings.TrimSpace(model.UpstreamModel)
		model.DisplayName = strings.TrimSpace(model.DisplayName)

		if _, ok := codexIntegrationProviders[model.Provider]; !ok {
			return fmt.Errorf("codex-integration.models[%d].provider: unsupported provider %q", i, model.Provider)
		}
		if model.Slug == "" || !strings.HasPrefix(model.Slug, model.Provider+"/") || strings.Count(model.Slug, "/") != 1 {
			return fmt.Errorf("codex-integration.models[%d].slug: %q must use the reserved %q namespace", i, model.Slug, model.Provider+"/")
		}
		if strings.TrimPrefix(model.Slug, model.Provider+"/") == "" {
			return fmt.Errorf("codex-integration.models[%d].slug: model segment is empty", i)
		}
		if model.UpstreamModel == "" {
			return fmt.Errorf("codex-integration.models[%d].upstream-model: value is required", i)
		}
		key := strings.ToLower(model.Slug)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("codex-integration.models[%d].slug: duplicate slug %q", i, model.Slug)
		}
		seen[key] = struct{}{}
		if model.Featured {
			featured++
		}
	}
	if featured > 4 {
		return fmt.Errorf("codex-integration.models: %d featured third-party models exceed the four slots available after the official GPT model", featured)
	}

	return nil
}

func validateCodexCatalogFile(name string) error {
	if filepath.IsAbs(name) {
		return fmt.Errorf("codex-integration.catalog-file: absolute paths are not allowed")
	}
	clean := filepath.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.Base(clean) != clean {
		return fmt.Errorf("codex-integration.catalog-file: %q must be a file name inside CODEX_HOME", name)
	}
	return nil
}

func isStrictLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
