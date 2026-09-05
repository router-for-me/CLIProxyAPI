package management

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/apikeyusage"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const minimumManagedAPIKeyLength = 24

type apiKeyProfileResponse struct {
	ID             string             `json:"id"`
	Name           string             `json:"name"`
	APIKey         string             `json:"api-key,omitempty"`
	KeyFingerprint string             `json:"key-fingerprint"`
	Disabled       bool               `json:"disabled,omitempty"`
	AllowedModels  []string           `json:"allowed-models,omitempty"`
	Weekly         config.APIKeyLimit `json:"weekly,omitempty"`
	Monthly        config.APIKeyLimit `json:"monthly,omitempty"`
}

// GetAPIKeyProfiles returns named downstream keys and accounting settings.
func (h *Handler) GetAPIKeyProfiles(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusOK, gin.H{"api-key-profiles": []config.APIKeyProfile{}})
		return
	}
	h.mu.Lock()
	configuredProfiles := append([]config.APIKeyProfile(nil), h.cfg.APIKeyProfiles...)
	profiles := make([]apiKeyProfileResponse, 0, len(configuredProfiles))
	for i := range configuredProfiles {
		profiles = append(profiles, newAPIKeyProfileResponse(configuredProfiles[i], false))
	}
	settings := h.cfg.APIKeyUsage
	h.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"api-key-profiles": profiles, "api-key-usage": settings})
}

// PostAPIKeyProfile creates a named key, generating a strong secret when omitted.
func (h *Handler) PostAPIKeyProfile(c *gin.Context) {
	var profile config.APIKeyProfile
	if err := c.ShouldBindJSON(&profile); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": err.Error()})
		return
	}
	profile.Name = strings.TrimSpace(profile.Name)
	if profile.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name_required", "message": "name is required"})
		return
	}
	profile.APIKey = strings.TrimSpace(profile.APIKey)
	if profile.APIKey == "" {
		generated, err := generateManagedAPIKey()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "key_generation_failed", "message": err.Error()})
			return
		}
		profile.APIKey = generated
	}
	if profile.ID = strings.TrimSpace(profile.ID); profile.ID == "" {
		profile.ID = generatedProfileID(profile.Name)
	}
	profile = normalizeProfileInput(profile)
	if err := validateProfile(profile); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_profile", "message": err.Error()})
		return
	}

	h.mu.Lock()
	if conflict := profileConflict(h.cfg.APIKeyProfiles, profile, ""); conflict != "" {
		h.mu.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": "profile_conflict", "message": conflict})
		return
	}
	h.cfg.APIKeyProfiles = append(h.cfg.APIKeyProfiles, profile)
	h.cfg.APIKeyUsage.Enabled = true
	h.cfg.NormalizeAPIKeyProfiles()
	snapshot, ok := h.saveConfigAndSnapshotLocked(c)
	h.mu.Unlock()
	if !ok {
		return
	}
	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), snapshot)
	c.JSON(http.StatusCreated, gin.H{"profile": newAPIKeyProfileResponse(profile, true)})
}

// PutAPIKeyProfile replaces one named key policy while keeping its stable ID.
func (h *Handler) PutAPIKeyProfile(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "id_required", "message": "profile id is required"})
		return
	}
	var replacement config.APIKeyProfile
	if err := c.ShouldBindJSON(&replacement); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": err.Error()})
		return
	}

	h.mu.Lock()
	index := profileIndex(h.cfg.APIKeyProfiles, id)
	if index < 0 {
		h.mu.Unlock()
		c.JSON(http.StatusNotFound, gin.H{"error": "profile_not_found"})
		return
	}
	replacement.ID = h.cfg.APIKeyProfiles[index].ID
	if strings.TrimSpace(replacement.APIKey) == "" {
		replacement.APIKey = h.cfg.APIKeyProfiles[index].APIKey
	}
	replacement = normalizeProfileInput(replacement)
	if err := validateProfile(replacement); err != nil {
		h.mu.Unlock()
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_profile", "message": err.Error()})
		return
	}
	if conflict := profileConflict(h.cfg.APIKeyProfiles, replacement, replacement.ID); conflict != "" {
		h.mu.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": "profile_conflict", "message": conflict})
		return
	}
	h.cfg.APIKeyProfiles[index] = replacement
	h.cfg.NormalizeAPIKeyProfiles()
	snapshot, ok := h.saveConfigAndSnapshotLocked(c)
	h.mu.Unlock()
	if !ok {
		return
	}
	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), snapshot)
	c.JSON(http.StatusOK, gin.H{"profile": newAPIKeyProfileResponse(replacement, false)})
}

// DeleteAPIKeyProfile removes a named key. Historical usage remains available.
func (h *Handler) DeleteAPIKeyProfile(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	h.mu.Lock()
	index := profileIndex(h.cfg.APIKeyProfiles, id)
	if index < 0 {
		h.mu.Unlock()
		c.JSON(http.StatusNotFound, gin.H{"error": "profile_not_found"})
		return
	}
	h.cfg.APIKeyProfiles = append(h.cfg.APIKeyProfiles[:index], h.cfg.APIKeyProfiles[index+1:]...)
	snapshot, ok := h.saveConfigAndSnapshotLocked(c)
	h.mu.Unlock()
	if !ok {
		return
	}
	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), snapshot)
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// PutAPIKeyUsageSettings updates persistence settings independently of profiles.
func (h *Handler) PutAPIKeyUsageSettings(c *gin.Context) {
	var settings config.APIKeyUsageConfig
	if err := c.ShouldBindJSON(&settings); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": err.Error()})
		return
	}
	settings.DatabasePath = strings.TrimSpace(settings.DatabasePath)
	settings.Timezone = strings.TrimSpace(settings.Timezone)
	if settings.Timezone == "" {
		settings.Timezone = config.DefaultAPIKeyUsageTimezone
	}
	if _, err := time.LoadLocation(settings.Timezone); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_timezone", "message": err.Error()})
		return
	}
	if settings.RetentionDays <= 0 {
		settings.RetentionDays = config.DefaultAPIKeyUsageRetentionDays
	}
	h.mu.Lock()
	h.cfg.APIKeyUsage = settings
	snapshot, ok := h.saveConfigAndSnapshotLocked(c)
	h.mu.Unlock()
	if !ok {
		return
	}
	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), snapshot)
	c.JSON(http.StatusOK, gin.H{"api-key-usage": settings})
}

// GetAPIKeyUsageSummary returns configured profiles and current period counters.
func (h *Handler) GetAPIKeyUsageSummary(c *gin.Context) {
	service := h.apiKeyUsageService()
	if service == nil || !service.Enabled() {
		c.JSON(http.StatusOK, gin.H{"enabled": false, "profiles": []any{}, "models": []any{}})
		return
	}
	period := strings.TrimSpace(c.DefaultQuery("period", "week"))
	summary, err := service.SummaryForPeriod(c.Request.Context(), period, time.Now())
	if err != nil {
		if errors.Is(err, apikeyusage.ErrDisabled) {
			c.JSON(http.StatusOK, gin.H{"enabled": false, "profiles": []any{}, "models": []any{}})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "usage_summary_failed", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"enabled": true, "summary": summary})
}

// GetAPIKeyUsageEvents returns a bounded, filterable event page.
func (h *Handler) GetAPIKeyUsageEvents(c *gin.Context) {
	service := h.apiKeyUsageService()
	if service == nil || !service.Enabled() {
		c.JSON(http.StatusOK, apikeyusage.EventsPage{Events: []apikeyusage.Event{}, Limit: 100})
		return
	}
	start, err := parseOptionalRFC3339(c.Query("start"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_start", "message": err.Error()})
		return
	}
	end, err := parseOptionalRFC3339(c.Query("end"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_end", "message": err.Error()})
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	page, err := service.Events(c.Request.Context(), c.Query("profile_id"), start, end, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "usage_events_failed", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, page)
}

func (h *Handler) apiKeyUsageService() *apikeyusage.Service {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	service := h.apiKeyUsage
	h.mu.Unlock()
	return service
}

func generateManagedAPIKey() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return "sk-cpa-" + base64.RawURLEncoding.EncodeToString(secret), nil
}

func newAPIKeyProfileResponse(profile config.APIKeyProfile, revealSecret bool) apiKeyProfileResponse {
	response := apiKeyProfileResponse{
		ID:             profile.ID,
		Name:           profile.Name,
		KeyFingerprint: apikeyusage.Fingerprint(profile.APIKey),
		Disabled:       profile.Disabled,
		AllowedModels:  append([]string(nil), profile.AllowedModels...),
		Weekly:         profile.Weekly,
		Monthly:        profile.Monthly,
	}
	if revealSecret {
		response.APIKey = profile.APIKey
	}
	return response
}

func generatedProfileID(name string) string {
	var builder strings.Builder
	for _, value := range strings.ToLower(strings.TrimSpace(name)) {
		if unicode.IsLetter(value) || unicode.IsDigit(value) {
			builder.WriteRune(value)
			continue
		}
		if builder.Len() > 0 && !strings.HasSuffix(builder.String(), "-") {
			builder.WriteByte('-')
		}
	}
	base := strings.Trim(builder.String(), "-")
	if base == "" {
		base = "user"
	}
	return base + "-" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
}

func normalizeProfileInput(profile config.APIKeyProfile) config.APIKeyProfile {
	profile.ID = strings.TrimSpace(profile.ID)
	profile.Name = strings.TrimSpace(profile.Name)
	profile.APIKey = strings.TrimSpace(profile.APIKey)
	models := make([]string, 0, len(profile.AllowedModels))
	seen := make(map[string]struct{}, len(profile.AllowedModels))
	for _, raw := range profile.AllowedModels {
		model := strings.TrimSpace(raw)
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		models = append(models, model)
	}
	profile.AllowedModels = models
	return profile
}

func validateProfile(profile config.APIKeyProfile) error {
	if profile.ID == "" {
		return errors.New("id is required")
	}
	if profile.Name == "" {
		return errors.New("name is required")
	}
	if len(profile.APIKey) < minimumManagedAPIKeyLength {
		return fmt.Errorf("api-key must contain at least %d characters", minimumManagedAPIKeyLength)
	}
	if profile.Weekly.Requests < 0 || profile.Weekly.Tokens < 0 || profile.Monthly.Requests < 0 || profile.Monthly.Tokens < 0 {
		return errors.New("usage limits cannot be negative")
	}
	return nil
}

func profileConflict(profiles []config.APIKeyProfile, candidate config.APIKeyProfile, exceptID string) string {
	for i := range profiles {
		profile := profiles[i]
		if strings.EqualFold(profile.ID, exceptID) {
			continue
		}
		if strings.EqualFold(profile.ID, candidate.ID) {
			return "a profile with this id already exists"
		}
		if profile.APIKey == candidate.APIKey {
			return "a profile with this api-key already exists"
		}
	}
	return ""
}

func profileIndex(profiles []config.APIKeyProfile, id string) int {
	for i := range profiles {
		if strings.EqualFold(profiles[i].ID, strings.TrimSpace(id)) {
			return i
		}
	}
	return -1
}

func parseOptionalRFC3339(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, value)
}
