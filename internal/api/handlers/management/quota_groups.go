package management

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func providerSupportsOAuthQuotaGroups(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "antigravity", "gemini", "gemini-cli":
		return true
	default:
		return false
	}
}

func effectiveOAuthQuotaGroupsForProvider(groups []config.OAuthQuotaGroup, provider string) []config.OAuthQuotaGroup {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return nil
	}
	out := make([]config.OAuthQuotaGroup, 0, len(groups))
	for _, group := range groups {
		if !group.Enabled {
			continue
		}
		for _, candidate := range group.Providers {
			if strings.EqualFold(strings.TrimSpace(candidate), provider) {
				out = append(out, group)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func lookupOAuthAccountQuotaGroupState(entries []config.OAuthAccountQuotaGroupState, authID, groupID string) (config.OAuthAccountQuotaGroupState, bool) {
	authID = strings.TrimSpace(authID)
	groupID = strings.ToLower(strings.TrimSpace(groupID))
	for _, entry := range entries {
		if strings.TrimSpace(entry.AuthID) == authID && strings.EqualFold(strings.TrimSpace(entry.GroupID), groupID) {
			return entry, true
		}
	}
	return config.OAuthAccountQuotaGroupState{}, false
}

func (h *Handler) GetOAuthQuotaGroups(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"oauth-quota-groups": config.NormalizeOAuthQuotaGroups(h.cfg.OAuthQuotaGroups),
	})
}

func (h *Handler) PutOAuthQuotaGroups(c *gin.Context) {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return
	}
	var entries []config.OAuthQuotaGroup
	if err = json.Unmarshal(data, &entries); err != nil {
		var wrapper struct {
			Items []config.OAuthQuotaGroup `json:"items"`
		}
		if err2 := json.Unmarshal(data, &wrapper); err2 != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
			return
		}
		entries = wrapper.Items
	}
	h.cfg.OAuthQuotaGroups = config.NormalizeOAuthQuotaGroups(entries)
	h.persist(c)
}

func (h *Handler) GetAuthFileQuotaGroups(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusOK, gin.H{"items": []gin.H{}})
		return
	}
	h.authManager.ClearExpiredOAuthQuotaGroupAutoStates(time.Now())
	definitions := config.NormalizeOAuthQuotaGroups(h.cfg.OAuthQuotaGroups)
	stateEntries := config.NormalizeOAuthAccountQuotaGroupState(h.cfg.OAuthAccountQuotaGroupState)
	groupPriority := make(map[string]int, len(definitions))
	for _, group := range definitions {
		groupPriority[group.ID] = group.Priority
	}
	now := time.Now()
	items := make([]gin.H, 0)
	for _, auth := range h.authManager.List() {
		if auth == nil || !providerSupportsOAuthQuotaGroups(auth.Provider) {
			continue
		}
		for _, group := range effectiveOAuthQuotaGroupsForProvider(definitions, auth.Provider) {
			state, _ := lookupOAuthAccountQuotaGroupState(stateEntries, auth.ID, group.ID)
			effectiveStatus := "available"
			if auth.Disabled {
				effectiveStatus = "auth_disabled"
			} else if state.ManualSuspended {
				effectiveStatus = "manual_suspended"
			} else if !state.AutoSuspendedUntil.IsZero() && state.AutoSuspendedUntil.After(now) {
				effectiveStatus = "auto_suspended"
			}
			var autoSuspendedUntil any
			if !state.AutoSuspendedUntil.IsZero() {
				autoSuspendedUntil = state.AutoSuspendedUntil
			}
			var updatedAt any
			if !state.UpdatedAt.IsZero() {
				updatedAt = state.UpdatedAt
			}
			items = append(items, gin.H{
				"auth_id":              auth.ID,
				"provider":             strings.ToLower(strings.TrimSpace(auth.Provider)),
				"group_id":             group.ID,
				"label":                group.Label,
				"effective_status":     effectiveStatus,
				"manual_suspended":     state.ManualSuspended,
				"manual_reason":        state.ManualReason,
				"auto_suspended_until": autoSuspendedUntil,
				"auto_reason":          state.AutoReason,
				"source_model":         state.SourceModel,
				"source_provider":      state.SourceProvider,
				"reset_time_source":    state.ResetTimeSource,
				"updated_at":           updatedAt,
				"updated_by":           state.UpdatedBy,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		authLeft, _ := items[i]["auth_id"].(string)
		authRight, _ := items[j]["auth_id"].(string)
		if authLeft != authRight {
			return authLeft < authRight
		}
		groupLeft, _ := items[i]["group_id"].(string)
		groupRight, _ := items[j]["group_id"].(string)
		if groupPriority[groupLeft] != groupPriority[groupRight] {
			return groupPriority[groupLeft] > groupPriority[groupRight]
		}
		return groupLeft < groupRight
	})
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) PatchAuthFileQuotaGroupsManual(c *gin.Context) {
	var body struct {
		AuthID          string `json:"auth_id"`
		GroupID         string `json:"group_id"`
		ManualSuspended *bool  `json:"manual_suspended"`
		Reason          string `json:"reason"`
		UpdatedBy       string `json:"updated_by"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.ManualSuspended == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	authID := strings.TrimSpace(body.AuthID)
	groupID := strings.ToLower(strings.TrimSpace(body.GroupID))
	if authID == "" || groupID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_id and group_id are required"})
		return
	}

	now := time.Now().UTC()
	current, _ := lookupOAuthAccountQuotaGroupState(h.cfg.OAuthAccountQuotaGroupState, authID, groupID)
	current.AuthID = authID
	current.GroupID = groupID
	current.ManualSuspended = *body.ManualSuspended
	current.ManualReason = strings.TrimSpace(body.Reason)
	current.UpdatedAt = now
	current.UpdatedBy = strings.TrimSpace(body.UpdatedBy)
	if current.UpdatedBy == "" {
		current.UpdatedBy = "management:manual"
	}
	if !current.ManualSuspended {
		current.ManualReason = ""
	}

	if !current.ManualSuspended && current.AutoSuspendedUntil.IsZero() {
		h.cfg.OAuthAccountQuotaGroupState = config.RemoveOAuthAccountQuotaGroupState(h.cfg.OAuthAccountQuotaGroupState, authID, groupID)
	} else {
		h.cfg.OAuthAccountQuotaGroupState = config.UpsertOAuthAccountQuotaGroupState(h.cfg.OAuthAccountQuotaGroupState, current)
	}
	h.persist(c)
}

func (h *Handler) PatchAuthFileQuotaGroupsAutoClear(c *gin.Context) {
	var body struct {
		AuthID    string `json:"auth_id"`
		GroupID   string `json:"group_id"`
		UpdatedBy string `json:"updated_by"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	authID := strings.TrimSpace(body.AuthID)
	groupID := strings.ToLower(strings.TrimSpace(body.GroupID))
	if authID == "" || groupID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_id and group_id are required"})
		return
	}

	current, ok := lookupOAuthAccountQuotaGroupState(h.cfg.OAuthAccountQuotaGroupState, authID, groupID)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}
	current.AutoSuspendedUntil = time.Time{}
	current.AutoReason = ""
	current.SourceModel = ""
	current.SourceProvider = ""
	current.ResetTimeSource = ""
	current.UpdatedAt = time.Now().UTC()
	current.UpdatedBy = strings.TrimSpace(body.UpdatedBy)
	if current.UpdatedBy == "" {
		current.UpdatedBy = "management:auto-clear"
	}

	if current.ManualSuspended {
		h.cfg.OAuthAccountQuotaGroupState = config.UpsertOAuthAccountQuotaGroupState(h.cfg.OAuthAccountQuotaGroupState, current)
	} else {
		h.cfg.OAuthAccountQuotaGroupState = config.RemoveOAuthAccountQuotaGroupState(h.cfg.OAuthAccountQuotaGroupState, authID, groupID)
	}
	h.persist(c)
}
