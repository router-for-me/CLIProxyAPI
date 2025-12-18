// Package v1 provides versioned API handlers for KorProxy management.
package v1

import (
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/buildinfo"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/routing"
)

// Handler provides v1 API handlers for profiles, routing rules, and diagnostics.
type Handler struct {
	mu     sync.RWMutex
	config *routing.RoutingConfig
}

// NewHandler creates a new v1 API handler with the given routing config.
func NewHandler(cfg *routing.RoutingConfig) *Handler {
	if cfg == nil {
		cfg = routing.DefaultRoutingConfig()
	}
	return &Handler{config: cfg}
}

// SetConfig updates the routing configuration.
func (h *Handler) SetConfig(cfg *routing.RoutingConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.config = cfg
}

// GetConfig returns the current routing configuration.
func (h *Handler) GetConfig() *routing.RoutingConfig {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.config
}

// ProfilesResponse is the response for listing profiles.
type ProfilesResponse struct {
	Profiles        []routing.Profile `json:"profiles"`
	ActiveProfileID *string           `json:"activeProfileId,omitempty"`
}

// ProfileResponse is the response for single profile operations.
type ProfileResponse struct {
	Profile routing.Profile `json:"profile"`
}

// CreateProfileRequest is the request body for creating a profile.
type CreateProfileRequest struct {
	Name                 string                `json:"name"`
	Color                string                `json:"color"`
	Icon                 string                `json:"icon,omitempty"`
	RoutingRules         *routing.RoutingRules `json:"routingRules,omitempty"`
	DefaultProviderGroup *string               `json:"defaultProviderGroup,omitempty"`
}

// UpdateProfileRequest is the request body for updating a profile.
type UpdateProfileRequest struct {
	Name                 *string               `json:"name,omitempty"`
	Color                *string               `json:"color,omitempty"`
	Icon                 *string               `json:"icon,omitempty"`
	RoutingRules         *routing.RoutingRules `json:"routingRules,omitempty"`
	DefaultProviderGroup *string               `json:"defaultProviderGroup,omitempty"`
}

// RoutingRulesResponse is the response for listing routing rules (provider groups).
type RoutingRulesResponse struct {
	ProviderGroups []routing.ProviderGroup `json:"providerGroups"`
	ModelFamilies  routing.ModelFamilies   `json:"modelFamilies"`
}

// ProviderGroupResponse is the response for single provider group operations.
type ProviderGroupResponse struct {
	ProviderGroup routing.ProviderGroup `json:"providerGroup"`
}

// CreateProviderGroupRequest is the request for creating a provider group.
type CreateProviderGroupRequest struct {
	Name              string                    `json:"name"`
	AccountIDs        []string                  `json:"accountIds"`
	SelectionStrategy routing.SelectionStrategy `json:"selectionStrategy"`
}

// UpdateProviderGroupRequest is the request for updating a provider group.
type UpdateProviderGroupRequest struct {
	Name              *string                    `json:"name,omitempty"`
	AccountIDs        []string                   `json:"accountIds,omitempty"`
	SelectionStrategy *routing.SelectionStrategy `json:"selectionStrategy,omitempty"`
}

// DiagnosticsBundle is the response for the debug bundle endpoint.
type DiagnosticsBundle struct {
	Version   string       `json:"version"`
	Timestamp string       `json:"timestamp"`
	System    SystemInfo   `json:"system"`
	Config    ConfigInfo   `json:"config"`
	Health    HealthStatus `json:"health"`
}

// SystemInfo contains system information for diagnostics.
type SystemInfo struct {
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	GoVersion   string `json:"goVersion"`
	AppVersion  string `json:"appVersion"`
	AppCommit   string `json:"appCommit"`
	NumCPU      int    `json:"numCpu"`
	NumRoutines int    `json:"numGoroutines"`
}

// ConfigInfo contains sanitized configuration summary.
type ConfigInfo struct {
	ProfileCount       int `json:"profileCount"`
	ProviderGroupCount int `json:"providerGroupCount"`
}

// HealthStatus represents component health.
type HealthStatus struct {
	Server  string `json:"server"`
	Routing string `json:"routing"`
}

// HealthResponse is the response for the health endpoint.
type HealthResponse struct {
	Status    string       `json:"status"`
	Timestamp string       `json:"timestamp"`
	Version   string       `json:"version"`
	Details   HealthStatus `json:"details"`
}

// ListProfiles returns all profiles.
func (h *Handler) ListProfiles(c *gin.Context) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	profiles := h.config.Profiles
	if profiles == nil {
		profiles = []routing.Profile{}
	}

	c.JSON(http.StatusOK, ProfilesResponse{
		Profiles:        profiles,
		ActiveProfileID: h.config.ActiveProfileID,
	})
}

// CreateProfile creates a new profile.
func (h *Handler) CreateProfile(c *gin.Context) {
	var req CreateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now().UTC()
	profile := routing.Profile{
		ID:                   uuid.New().String(),
		Name:                 req.Name,
		Color:                req.Color,
		Icon:                 req.Icon,
		DefaultProviderGroup: req.DefaultProviderGroup,
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	if req.RoutingRules != nil {
		profile.RoutingRules = *req.RoutingRules
	}

	h.config.Profiles = append(h.config.Profiles, profile)

	if err := routing.SaveConfig(h.config); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save config"})
		return
	}

	c.JSON(http.StatusCreated, ProfileResponse{Profile: profile})
}

// UpdateProfile updates an existing profile.
func (h *Handler) UpdateProfile(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile id is required"})
		return
	}

	var req UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	var profile *routing.Profile
	for i := range h.config.Profiles {
		if h.config.Profiles[i].ID == id {
			profile = &h.config.Profiles[i]
			break
		}
	}

	if profile == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
		return
	}

	if req.Name != nil {
		profile.Name = *req.Name
	}
	if req.Color != nil {
		profile.Color = *req.Color
	}
	if req.Icon != nil {
		profile.Icon = *req.Icon
	}
	if req.RoutingRules != nil {
		profile.RoutingRules = *req.RoutingRules
	}
	if req.DefaultProviderGroup != nil {
		profile.DefaultProviderGroup = req.DefaultProviderGroup
	}
	profile.UpdatedAt = time.Now().UTC()

	if err := routing.SaveConfig(h.config); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save config"})
		return
	}

	c.JSON(http.StatusOK, ProfileResponse{Profile: *profile})
}

// DeleteProfile deletes a profile by ID.
func (h *Handler) DeleteProfile(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "profile id is required"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	found := false
	profiles := make([]routing.Profile, 0, len(h.config.Profiles))
	for _, p := range h.config.Profiles {
		if p.ID == id {
			found = true
			continue
		}
		profiles = append(profiles, p)
	}

	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
		return
	}

	h.config.Profiles = profiles

	if h.config.ActiveProfileID != nil && *h.config.ActiveProfileID == id {
		h.config.ActiveProfileID = nil
	}

	if err := routing.SaveConfig(h.config); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save config"})
		return
	}

	c.Status(http.StatusNoContent)
}

// ListRoutingRules returns all provider groups and model families.
func (h *Handler) ListRoutingRules(c *gin.Context) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	groups := h.config.ProviderGroups
	if groups == nil {
		groups = []routing.ProviderGroup{}
	}

	c.JSON(http.StatusOK, RoutingRulesResponse{
		ProviderGroups: groups,
		ModelFamilies:  h.config.ModelFamilies,
	})
}

// CreateRoutingRule creates a new provider group.
func (h *Handler) CreateRoutingRule(c *gin.Context) {
	var req CreateProviderGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	if !req.SelectionStrategy.IsValid() {
		req.SelectionStrategy = routing.SelectionRoundRobin
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	group := routing.ProviderGroup{
		ID:                uuid.New().String(),
		Name:              req.Name,
		AccountIDs:        req.AccountIDs,
		SelectionStrategy: req.SelectionStrategy,
	}

	h.config.ProviderGroups = append(h.config.ProviderGroups, group)

	if err := routing.SaveConfig(h.config); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save config"})
		return
	}

	c.JSON(http.StatusCreated, ProviderGroupResponse{ProviderGroup: group})
}

// UpdateRoutingRule updates an existing provider group.
func (h *Handler) UpdateRoutingRule(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider group id is required"})
		return
	}

	var req UpdateProviderGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	var group *routing.ProviderGroup
	for i := range h.config.ProviderGroups {
		if h.config.ProviderGroups[i].ID == id {
			group = &h.config.ProviderGroups[i]
			break
		}
	}

	if group == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider group not found"})
		return
	}

	if req.Name != nil {
		group.Name = *req.Name
	}
	if req.AccountIDs != nil {
		group.AccountIDs = req.AccountIDs
	}
	if req.SelectionStrategy != nil && req.SelectionStrategy.IsValid() {
		group.SelectionStrategy = *req.SelectionStrategy
	}

	if err := routing.SaveConfig(h.config); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save config"})
		return
	}

	c.JSON(http.StatusOK, ProviderGroupResponse{ProviderGroup: *group})
}

// DeleteRoutingRule deletes a provider group by ID.
func (h *Handler) DeleteRoutingRule(c *gin.Context) {
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider group id is required"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	found := false
	groups := make([]routing.ProviderGroup, 0, len(h.config.ProviderGroups))
	for _, g := range h.config.ProviderGroups {
		if g.ID == id {
			found = true
			continue
		}
		groups = append(groups, g)
	}

	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider group not found"})
		return
	}

	h.config.ProviderGroups = groups

	if err := routing.SaveConfig(h.config); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save config"})
		return
	}

	c.Status(http.StatusNoContent)
}

// GetDiagnosticsBundle returns a debug bundle with system and config info.
func (h *Handler) GetDiagnosticsBundle(c *gin.Context) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	bundle := DiagnosticsBundle{
		Version:   "1.0.0",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		System: SystemInfo{
			OS:          runtime.GOOS,
			Arch:        runtime.GOARCH,
			GoVersion:   runtime.Version(),
			AppVersion:  buildinfo.Version,
			AppCommit:   buildinfo.Commit,
			NumCPU:      runtime.NumCPU(),
			NumRoutines: runtime.NumGoroutine(),
		},
		Config: ConfigInfo{
			ProfileCount:       len(h.config.Profiles),
			ProviderGroupCount: len(h.config.ProviderGroups),
		},
		Health: HealthStatus{
			Server:  "healthy",
			Routing: "healthy",
		},
	}

	c.JSON(http.StatusOK, bundle)
}

// GetHealth returns the health status of the server.
func (h *Handler) GetHealth(c *gin.Context) {
	resp := HealthResponse{
		Status:    "healthy",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Version:   buildinfo.Version,
		Details: HealthStatus{
			Server:  "healthy",
			Routing: "healthy",
		},
	}

	c.JSON(http.StatusOK, resp)
}
