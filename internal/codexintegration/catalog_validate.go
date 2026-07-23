package codexintegration

import (
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// ValidateCatalog applies Codex's base schema checks and integration invariants.
func ValidateCatalog(catalog Catalog, integration config.CodexIntegrationConfig) error {
	data, err := catalog.Marshal()
	if err != nil {
		return err
	}
	if err = registry.ValidateCodexClientModelsJSON(data); err != nil {
		return fmt.Errorf("validate compiled Codex catalog: %w", err)
	}

	featuredModels := featuredCodexIntegrationModels(integration)
	featuredCount := 1 + len(featuredModels)
	seenPriorities := make(map[int]string, len(catalog.Models))
	for i, model := range catalog.Models {
		slug := catalogString(model, "slug")
		priority := catalogInt(model, "priority")
		if priority <= 0 {
			return fmt.Errorf("compiled Codex catalog model %q has invalid priority %d", slug, priority)
		}
		if previous := seenPriorities[priority]; previous != "" {
			return fmt.Errorf("compiled Codex catalog models %q and %q share priority %d", previous, slug, priority)
		}
		seenPriorities[priority] = slug
		if catalogString(model, "visibility") == "list" && catalogString(model, "multi_agent_version") != config.DefaultCodexMultiAgentMode {
			return fmt.Errorf("compiled Codex catalog model %q must use multi_agent_version %q", slug, config.DefaultCodexMultiAgentMode)
		}
		if strings.HasPrefix(slug, "antigravity/") {
			if supportsSearch, _ := model["supports_search_tool"].(bool); supportsSearch {
				return fmt.Errorf("compiled Codex catalog model %q advertises unverified hosted search", slug)
			}
		}
		if i < featuredCount && priority != i+1 {
			return fmt.Errorf("compiled Codex catalog featured priority %d is not contiguous", priority)
		}
	}

	if integration.Enabled {
		if len(catalog.Models) < featuredCount {
			return fmt.Errorf("compiled Codex catalog featured surface is incomplete")
		}
		configuredSlugs := make(map[string]struct{}, len(integration.Models))
		for _, model := range integration.Models {
			configuredSlugs[model.Slug] = struct{}{}
		}
		if first := catalogString(catalog.Models[0], "slug"); first == "" {
			return fmt.Errorf("compiled Codex catalog has no official featured model")
		} else if _, configured := configuredSlugs[first]; configured {
			return fmt.Errorf("compiled Codex catalog first featured model %q is not official", first)
		}
		for i, want := range featuredModels {
			if got := catalogString(catalog.Models[i+1], "slug"); got != want.Slug {
				return fmt.Errorf("compiled Codex catalog featured model %d is %q, want %q", i+2, got, want.Slug)
			}
		}
	}
	return nil
}

func featuredCodexIntegrationModels(integration config.CodexIntegrationConfig) []config.CodexIntegrationModel {
	featured := make([]config.CodexIntegrationModel, 0, 4)
	for _, model := range integration.Models {
		if model.Visible && model.Featured {
			featured = append(featured, model)
		}
	}
	sortCodexIntegrationModels(featured)
	return featured
}

func sortCodexIntegrationModels(models []config.CodexIntegrationModel) {
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Priority == models[j].Priority {
			return models[i].Slug < models[j].Slug
		}
		return models[i].Priority < models[j].Priority
	})
}
