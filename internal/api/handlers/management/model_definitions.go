package management

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	cursorauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// GetStaticModelDefinitions returns model metadata for a given channel.
// Cursor definitions are dynamic and come from the cached OAuth model snapshots.
// Channel is provided via path param (:channel) or query param (?channel=...).
func (h *Handler) GetStaticModelDefinitions(c *gin.Context) {
	channel := strings.TrimSpace(c.Param("channel"))
	if channel == "" {
		channel = strings.TrimSpace(c.Query("channel"))
	}
	if channel == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "channel is required"})
		return
	}

	channel = strings.ToLower(strings.TrimSpace(channel))
	models := registry.GetStaticModelDefinitionsByChannel(channel)
	if channel == cursorauth.Provider {
		models = h.cursorModelDefinitions()
	}
	if models == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown channel", "channel": channel})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"channel": channel,
		"models":  models,
	})
}

func (h *Handler) cursorModelDefinitions() []*registry.ModelInfo {
	byID := make(map[string]*registry.ModelInfo)
	if h != nil && h.authManager != nil {
		for _, auth := range h.authManager.List() {
			if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), cursorauth.Provider) || auth.Metadata == nil {
				continue
			}
			raw, errMarshal := json.Marshal(auth.Metadata[cursorauth.ModelCacheKey])
			if errMarshal != nil {
				continue
			}
			var snapshot []cursorauth.ModelDetails
			if errUnmarshal := json.Unmarshal(raw, &snapshot); errUnmarshal != nil {
				continue
			}
			for _, model := range snapshot {
				id := strings.TrimSpace(model.ID)
				if id == "" {
					continue
				}
				displayName := strings.TrimSpace(model.DisplayName)
				if displayName == "" {
					displayName = id
				}
				if existing := byID[id]; existing != nil {
					if existing.DisplayName == existing.ID && displayName != id {
						existing.DisplayName = displayName
					}
					continue
				}
				byID[id] = &registry.ModelInfo{
					ID:          id,
					Object:      "model",
					OwnedBy:     cursorauth.Provider,
					Type:        cursorauth.Provider,
					DisplayName: displayName,
				}
			}
		}
	}

	if len(byID) == 0 {
		for _, model := range registry.GetGlobalRegistry().GetAvailableModelsByProvider(cursorauth.Provider) {
			if model != nil && strings.TrimSpace(model.ID) != "" {
				byID[model.ID] = model
			}
		}
	}

	models := make([]*registry.ModelInfo, 0, len(byID))
	for _, model := range byID {
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool {
		return strings.ToLower(models[i].ID) < strings.ToLower(models[j].ID)
	})
	return models
}
