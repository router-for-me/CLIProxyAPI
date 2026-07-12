package management

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

// GetExposedModels returns the list of model IDs currently exposed at /v1/models.
func (h *Handler) GetExposedModels(c *gin.Context) {
	h.mu.Lock()
	models := h.cfg.ExposedModels
	h.mu.Unlock()

	if models == nil {
		models = []string{}
	}
	c.JSON(http.StatusOK, gin.H{"models": models})
}

// PutExposedModels replaces the entire exposed models list.
func (h *Handler) PutExposedModels(c *gin.Context) {
	var req struct {
		Models []string `json:"models"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.saveExposedModels(req.Models); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"models": h.cfg.ExposedModels})
}

// PostExposedModels toggles individual models in the exposed list.
func (h *Handler) PostExposedModels(c *gin.Context) {
	var req struct {
		Model    string `json:"model"`
		Exposed  bool   `json:"exposed"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "model is required"})
		return
	}

	h.mu.Lock()
	set := make(map[string]struct{}, len(h.cfg.ExposedModels))
	for _, m := range h.cfg.ExposedModels {
		set[m] = struct{}{}
	}
	if req.Exposed {
		set[req.Model] = struct{}{}
	} else {
		delete(set, req.Model)
	}
	models := make([]string, 0, len(set))
	for m := range set {
		models = append(models, m)
	}
	h.mu.Unlock()
	sort.Strings(models)

	if err := h.saveExposedModels(models); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"models": h.cfg.ExposedModels})
}

func (h *Handler) saveExposedModels(models []string) error {
	h.mu.Lock()
	h.cfg.ExposedModels = config.NormalizeExposedModels(models)
	h.mu.Unlock()

	if err := h.saveConfigAndReload(); err != nil {
		return err
	}

	// Invalidate the model registry cache so /v1/models reflects the change.
	reg := registry.GetGlobalRegistry()
	if reg != nil {
		reg.InvalidateAvailableModelsCache()
	}

	return nil
}

// GetSubscriptions lists detected auth files with their provider and available models.
func (h *Handler) GetSubscriptions(c *gin.Context) {
	authDir, err := util.ResolveAuthDir(h.cfg.AuthDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	entries, err := os.ReadDir(authDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	reg := registry.GetGlobalRegistry()
	subscriptions := make([]gin.H, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			continue
		}
		fullPath := filepath.Join(authDir, e.Name())
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		var metadata map[string]any
		if err := json.Unmarshal(data, &metadata); err != nil {
			continue
		}
		provider := ""
		if t, ok := metadata["type"].(string); ok && t != "" {
			provider = strings.ToLower(t)
		}
		if provider == "openai-compatibility" {
			if cn, ok := metadata["compat_name"].(string); ok && cn != "" {
				provider = cn
			}
		}

		authID := e.Name()
		if h.authManager != nil {
			for _, auth := range h.authManager.List() {
				if auth.FileName == e.Name() || auth.ID == e.Name() {
					authID = auth.ID
					if auth.Provider != "" {
						provider = auth.Provider
					}
					break
				}
			}
		}

		models := reg.GetModelsForClient(authID)
		modelList := make([]gin.H, 0, len(models))
		for _, m := range models {
			entry := gin.H{"id": m.ID}
			if m.DisplayName != "" {
				entry["display_name"] = m.DisplayName
			}
			if m.OwnedBy != "" {
				entry["owned_by"] = m.OwnedBy
			}
			modelList = append(modelList, entry)
		}

		subscriptions = append(subscriptions, gin.H{
			"file":     e.Name(),
			"provider": provider,
			"models":   modelList,
		})
	}

	c.JSON(http.StatusOK, gin.H{"subscriptions": subscriptions})
}

func (h *Handler) saveConfigAndReload() error {
	path := h.configFilePath
	if path == "" {
		path = "config.yaml"
	}

	if err := config.SaveConfigPreserveComments(path, h.cfg); err != nil {
		return err
	}

	if h.configReloadHook != nil {
		h.configReloadHook(context.Background(), h.cfg)
	} else {
		log.Warn("config reload hook not set, exposed models change may not take effect until restart")
	}

	return nil
}
