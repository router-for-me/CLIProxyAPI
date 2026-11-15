package management

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	proxxgroups "github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/groups"
)

type proxxGroupMember struct {
	Model         string   `json:"model"`
	Providers     []string `json:"providers"`
	Available     bool     `json:"available"`
	CooldownUntil *string  `json:"cooldown_until"`
	Credentials   int      `json:"credentials"`
}

type proxxGroup struct {
	Name     string             `json:"name"`
	Strategy string             `json:"strategy"`
	Hidden   bool               `json:"hidden"`
	Members  []proxxGroupMember `json:"members"`
}

// GetProxxModelGroups returns the active proxx model groups with the live availability of
// each member (GET /v8/management/proxx/groups).
func (h *Handler) GetProxxModelGroups(c *gin.Context) {
	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	active := proxxgroups.List()
	out := make([]proxxGroup, 0, len(active))
	for _, group := range active {
		entry := proxxGroup{Name: group.Name, Strategy: group.Strategy, Hidden: group.Hidden, Members: make([]proxxGroupMember, 0, len(group.Models))}
		for _, model := range group.Models {
			member := proxxGroupMember{Model: model, Providers: []string{}}
			if manager != nil {
				availability := manager.ProxxModelAvailability(c.Request.Context(), model)
				if availability.Providers != nil {
					member.Providers = availability.Providers
				}
				member.Available = availability.Available
				member.Credentials = availability.Credentials
				if !availability.CooldownUntil.IsZero() {
					until := availability.CooldownUntil.UTC().Format(time.RFC3339)
					member.CooldownUntil = &until
				}
			}
			entry.Members = append(entry.Members, member)
		}
		out = append(out, entry)
	}
	c.JSON(http.StatusOK, gin.H{"groups": out})
}
