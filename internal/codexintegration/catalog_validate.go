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
		if i < 5 && priority != i+1 {
			return fmt.Errorf("compiled Codex catalog featured priority %d is not contiguous", priority)
		}
	}

	if integration.Enabled {
		wantFeatured := []string{"gpt-5.6-sol"}
		featured := make([]config.CodexIntegrationModel, 0, 4)
		for _, model := range integration.Models {
			if model.Visible && model.Featured {
				featured = append(featured, model)
			}
		}
		sortCodexIntegrationModels(featured)
		for _, model := range featured {
			wantFeatured = append(wantFeatured, model.Slug)
		}
		if len(wantFeatured) != 5 || len(catalog.Models) < 5 {
			return fmt.Errorf("compiled Codex catalog featured surface is incomplete")
		}
		for i, want := range wantFeatured {
			if got := catalogString(catalog.Models[i], "slug"); got != want {
				return fmt.Errorf("compiled Codex catalog featured model %d is %q, want %q", i+1, got, want)
			}
		}
	}
	return nil
}

func sortCodexIntegrationModels(models []config.CodexIntegrationModel) {
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Priority == models[j].Priority {
			return models[i].Slug < models[j].Slug
		}
		return models[i].Priority < models[j].Priority
	})
}
