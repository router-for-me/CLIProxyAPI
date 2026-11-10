package management

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func (h *Handler) SetCredentialModelCatalogHook(hook coreauth.CredentialModelCatalogHook) {
	h.credentialModelCatalog = hook
}

func (h *Handler) modelManagementCredential(c *gin.Context) (*coreauth.Auth, bool) {
	var a *coreauth.Auth
	if c.Param("id") != "" {
		var ok bool
		a, ok = h.hubCredential(c, true)
		if !ok {
			return nil, false
		}
	} else {
		if h.authManager == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "credential manager unavailable"})
			return nil, false
		}
		name := strings.TrimSpace(c.Query("name"))
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
			return nil, false
		}
		a, _ = h.lookupAuthFile(name, strings.TrimSpace(c.Query("auth_index")))
		if a == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
			return nil, false
		}
	}
	if !coreauth.SupportsCredentialModels(a) || (h.cfg != nil && h.cfg.Home.Enabled) {
		c.JSON(http.StatusConflict, gin.H{"error": coreauth.ErrCredentialModelsUnsupported.Error()})
		return nil, false
	}
	return a, true
}

func (h *Handler) readCredentialModelCatalog(c *gin.Context, a *coreauth.Auth, refresh bool) (*coreauth.CredentialModelCatalog, error) {
	if h.credentialModelCatalog != nil {
		return h.credentialModelCatalog(c.Request.Context(), a, refresh)
	}
	if refresh || strings.EqualFold(a.Provider, "antigravity") {
		return nil, coreauth.ErrCredentialModelsUnavailable
	}
	models := registry.GetStaticModelDefinitionsByChannel(a.Provider)
	if strings.EqualFold(a.Provider, "codex") {
		switch strings.ToLower(strings.TrimSpace(a.Attributes["plan_type"])) {
		case "free":
			models = registry.GetCodexFreeModels()
		case "plus":
			models = registry.GetCodexPlusModels()
		case "team", "business", "go":
			models = registry.GetCodexTeamModels()
		}
	}
	return &coreauth.CredentialModelCatalog{Models: models, Source: registry.ModelCatalogStatusForProvider(a.Provider)}, nil
}

func (h *Handler) defaultCredentialModelEntries(a *coreauth.Auth, catalog []*registry.ModelInfo) []coreauth.CredentialModel {
	aliases := coreauth.OAuthModelAliasesFromAttributes(a.Attributes)
	if h.cfg != nil {
		aliases = append(aliases, h.cfg.OAuthModelAlias[coreauth.OAuthModelAliasChannel(a.Provider, a.AuthKind())]...)
	}
	out := make([]coreauth.CredentialModel, 0, len(catalog)+len(aliases))
	seen := make(map[string]bool)
	add := func(name, alias string) {
		entry := coreauth.CredentialModel{Name: name, Alias: alias}
		if entry.Alias == name {
			entry.Alias = ""
		}
		key := strings.ToLower(entry.PublicName())
		if name != "" && !seen[key] {
			out = append(out, entry)
			seen[key] = true
		}
	}
	// Reserve explicit mappings first so inherited names cannot mask their aliases.
	mapped := make(map[string]bool)
	for _, alias := range aliases {
		if alias.Name == "" || alias.Alias == "" {
			continue
		}
		key := strings.ToLower(alias.Alias)
		if seen[key] {
			continue
		}
		add(alias.Name, alias.Alias)
		mapped[strings.ToLower(alias.Name)] = true
	}
	for _, model := range catalog {
		if model != nil && !mapped[strings.ToLower(model.ID)] {
			add(model.ID, "")
		}
	}
	return out
}

func (h *Handler) credentialModelsPayload(a *coreauth.Auth, catalog *coreauth.CredentialModelCatalog) gin.H {
	models, configured := a.CredentialModels()
	if !configured {
		models = h.defaultCredentialModelEntries(a, catalog.Models)
	}
	if models == nil {
		models = []coreauth.CredentialModel{}
	}
	available := catalog.Models
	if available == nil {
		available = []*registry.ModelInfo{}
	}
	registered := make([]gin.H, 0)
	for _, model := range registry.GetGlobalRegistry().GetModelsForClient(a.ID) {
		if model == nil {
			continue
		}
		name := model.MetadataModelID
		if name == "" {
			name = strings.TrimPrefix(model.ID, a.Prefix+"/")
		}
		registered = append(registered, gin.H{"id": model.ID, "name": name})
	}
	return gin.H{
		"models": models, "configured": configured, "available_models": available,
		"registered_models": registered, "source": catalog.Source, "catalog_available": catalog.Models != nil,
		"enabled_models": a.Metadata["enabled_models"], "excluded_models": a.Metadata["excluded_models"],
		"capabilities": gin.H{
			"manual_models":                !strings.EqualFold(a.Provider, "antigravity"),
			"aliases":                      true,
			"thinking":                     true,
			"max_context_length":           true,
			"modalities":                   true,
			"is_compat":                    true,
			"support_configuration_update": strings.EqualFold(a.Provider, "codex"),
			"merge":                        true,
			"reset":                        true,
		},
	}
}

// CredentialModels manages one file-backed credential without changing global
// catalogs. Hub routes are owner/channel-manager scoped; management routes use
// the existing administrator policy.
func (h *Handler) CredentialModels(c *gin.Context) {
	a, ok := h.modelManagementCredential(c)
	if !ok {
		return
	}
	catalog, err := h.readCredentialModelCatalog(c, a, false)
	if err != nil {
		if !errors.Is(err, coreauth.ErrCredentialModelsUnavailable) {
			credentialModelsError(c, err)
			return
		}
		catalog = &coreauth.CredentialModelCatalog{Source: registry.ModelCatalogStatusForProvider(a.Provider)}
	}
	if c.Request.Method == http.MethodGet {
		c.JSON(http.StatusOK, h.credentialModelsPayload(a, catalog))
		return
	}
	if c.Request.Method != http.MethodDelete {
		var body struct {
			Models json.RawMessage `json:"models"`
			Remove []string        `json:"remove"`
		}
		decoder := json.NewDecoder(c.Request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid model management request"})
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid trailing request data"})
			return
		}
		if len(body.Models) == 0 && (c.Request.Method == http.MethodPut || len(body.Remove) == 0) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "models is required"})
			return
		}
		if c.Request.Method == http.MethodPut && len(body.Remove) > 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "remove is only supported by PATCH"})
			return
		}
		updates := []coreauth.CredentialModel{}
		if len(body.Models) > 0 {
			updates, err = coreauth.DecodeCredentialModels(body.Models)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}
		for _, model := range updates {
			if strings.HasPrefix(model.Name, a.Prefix+"/") || strings.HasPrefix(model.Alias, a.Prefix+"/") {
				c.JSON(http.StatusBadRequest, gin.H{"error": "model names must not include the credential routing prefix"})
				return
			}
			if model.SupportConfigurationUpdate != nil && !strings.EqualFold(a.Provider, "codex") {
				c.JSON(http.StatusBadRequest, gin.H{"error": "support-configuration-update is only supported by Codex credentials"})
				return
			}
		}
		for _, name := range body.Remove {
			if strings.TrimSpace(name) == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "remove contains an empty model name"})
				return
			}
		}
		h.authStatusMu.Lock()
		current, exists := h.authManager.GetByID(a.ID)
		if !exists {
			h.authStatusMu.Unlock()
			c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
			return
		}
		a = current
		if c.Request.Method == http.MethodPatch {
			base, configured := a.CredentialModels()
			if !configured {
				if catalog.Models == nil {
					h.authStatusMu.Unlock()
					credentialModelsError(c, coreauth.ErrCredentialModelsUnavailable)
					return
				}
				base = h.defaultCredentialModelEntries(a, catalog.Models)
			}
			updates = mergeCredentialModelEntries(base, updates, body.Remove)
		}
		if a.Metadata == nil {
			a.Metadata = make(map[string]any)
		}
		a.Metadata["models"] = updates
		a.UpdatedAt = time.Now()
		err = h.persistHubCredential(c.Request.Context(), a)
		h.authStatusMu.Unlock()
	} else {
		h.authStatusMu.Lock()
		current, exists := h.authManager.GetByID(a.ID)
		if !exists {
			h.authStatusMu.Unlock()
			c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
			return
		}
		a = current
		delete(a.Metadata, "models")
		a.UpdatedAt = time.Now()
		err = h.persistHubCredential(c.Request.Context(), a)
		h.authStatusMu.Unlock()
	}
	if err != nil {
		credentialModelsError(c, err)
		return
	}
	if c.Param("id") != "" {
		c.Set("hubAction", "credential.models.update")
	}
	c.JSON(http.StatusOK, h.credentialModelsPayload(a, catalog))
}

func mergeCredentialModelEntries(base, updates []coreauth.CredentialModel, remove []string) []coreauth.CredentialModel {
	removed := make(map[string]bool, len(remove))
	for _, name := range remove {
		removed[strings.ToLower(strings.TrimSpace(name))] = true
	}
	out := make([]coreauth.CredentialModel, 0, len(base)+len(updates))
	indexes := make(map[string]int)
	for _, entry := range base {
		key := strings.ToLower(entry.PublicName())
		if removed[key] {
			continue
		}
		indexes[key] = len(out)
		out = append(out, entry)
	}
	for _, entry := range updates {
		key := strings.ToLower(entry.PublicName())
		if index, exists := indexes[key]; exists {
			out[index] = entry
		} else {
			indexes[key] = len(out)
			out = append(out, entry)
		}
	}
	return out
}

// DiscoverCredentialModels refreshes only the source, never the user's overrides.
func (h *Handler) DiscoverCredentialModels(c *gin.Context) {
	a, ok := h.modelManagementCredential(c)
	if !ok {
		return
	}
	catalog, err := h.readCredentialModelCatalog(c, a, true)
	if err != nil {
		credentialModelsError(c, err)
		return
	}
	if current, exists := h.authManager.GetByID(a.ID); exists {
		a = current
	} else {
		c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
		return
	}
	if c.Param("id") != "" {
		c.Set("hubAction", "credential.models.discover")
	}
	c.JSON(http.StatusOK, h.credentialModelsPayload(a, catalog))
}

func credentialModelsError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, coreauth.ErrCredentialModelsUnsupported):
		c.JSON(http.StatusConflict, gin.H{"error": coreauth.ErrCredentialModelsUnsupported.Error()})
	case errors.Is(err, coreauth.ErrCredentialModelsUnavailable):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": coreauth.ErrCredentialModelsUnavailable.Error()})
	case errors.Is(err, coreauth.ErrCredentialModelsRefreshFailed):
		// Wrapped errors carry the upstream reason so the panel can show why discovery failed.
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
	default:
		log.WithError(err).Error("credential model operation failed")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "credential model operation failed"})
	}
}
