package usage

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

// AntigravityModelQuota describes the quota state for a single model.
type AntigravityModelQuota struct {
	Name             string `json:"name"`
	RemainingPercent *int   `json:"remaining_percent,omitempty"` // 0-100
	ResetTime        string `json:"reset_time,omitempty"`
}

// AntigravityQuotaSnapshot captures Antigravity quota information returned by fetchAvailableModels.
// This is a best-effort in-memory snapshot for observability (it is not persisted).
type AntigravityQuotaSnapshot struct {
	Models    []AntigravityModelQuota `json:"models,omitempty"`
	Forbidden bool                   `json:"forbidden,omitempty"`
	UpdatedAt time.Time              `json:"updated_at"`
}

var antigravityQuotaByAuth sync.Map // authID -> AntigravityQuotaSnapshot

// ParseAntigravityQuotaSnapshot parses fetchAvailableModels JSON response into a snapshot.
// Returns nil when no relevant quotaInfo entries are present.
func ParseAntigravityQuotaSnapshot(body []byte) *AntigravityQuotaSnapshot {
	if len(body) == 0 {
		return nil
	}

	models := gjson.GetBytes(body, "models")
	if !models.Exists() || !models.IsObject() {
		return nil
	}

	out := &AntigravityQuotaSnapshot{}
	for modelName, modelInfo := range models.Map() {
		lower := strings.ToLower(modelName)
		if !strings.Contains(lower, "gemini") && !strings.Contains(lower, "claude") {
			continue
		}

		quota := modelInfo.Get("quotaInfo")
		if !quota.Exists() || !quota.IsObject() {
			continue
		}

		var remainingPercent *int
		if rf := quota.Get("remainingFraction"); rf.Exists() && rf.Type != gjson.Null {
			v := int(math.Round(rf.Float() * 100))
			if v < 0 {
				v = 0
			} else if v > 100 {
				v = 100
			}
			remainingPercent = &v
		}
		resetTime := strings.TrimSpace(quota.Get("resetTime").String())

		if remainingPercent == nil && resetTime == "" {
			continue
		}

		out.Models = append(out.Models, AntigravityModelQuota{
			Name:             modelName,
			RemainingPercent: remainingPercent,
			ResetTime:        resetTime,
		})
	}

	if len(out.Models) == 0 {
		return nil
	}

	sort.Slice(out.Models, func(i, j int) bool {
		return strings.ToLower(out.Models[i].Name) < strings.ToLower(out.Models[j].Name)
	})
	out.UpdatedAt = time.Now()
	return out
}

// NewForbiddenAntigravityQuotaSnapshot creates a snapshot representing a forbidden (403) state.
func NewForbiddenAntigravityQuotaSnapshot() *AntigravityQuotaSnapshot {
	return &AntigravityQuotaSnapshot{
		Forbidden: true,
		UpdatedAt: time.Now(),
	}
}

// UpdateAntigravityQuotaSnapshot stores the latest snapshot for an authID (in-memory).
func UpdateAntigravityQuotaSnapshot(authID string, snapshot *AntigravityQuotaSnapshot) {
	if authID == "" || snapshot == nil {
		return
	}
	antigravityQuotaByAuth.Store(authID, *snapshot)
}

// GetAntigravityQuotaSnapshot returns the most recent snapshot for an authID, if any.
func GetAntigravityQuotaSnapshot(authID string) *AntigravityQuotaSnapshot {
	if authID == "" {
		return nil
	}
	if v, ok := antigravityQuotaByAuth.Load(authID); ok {
		if snap, ok2 := v.(AntigravityQuotaSnapshot); ok2 {
			out := snap
			return &out
		}
	}
	return nil
}

