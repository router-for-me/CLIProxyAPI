package config

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

const (
	OAuthQuotaGroupClaude45 = "claude_45"
	OAuthQuotaGroupG3Pro    = "g3_pro"
	OAuthQuotaGroupG3Flash  = "g3_flash"
)

func canonicalOAuthQuotaGroupID(id string) string {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "google-claude", "claude", OAuthQuotaGroupClaude45:
		return OAuthQuotaGroupClaude45
	case "google-gemini-pro", "gemini-pro", OAuthQuotaGroupG3Pro:
		return OAuthQuotaGroupG3Pro
	case "google-gemini-flash", "gemini-flash", OAuthQuotaGroupG3Flash:
		return OAuthQuotaGroupG3Flash
	default:
		return strings.ToLower(strings.TrimSpace(id))
	}
}

func legacyGeminiImageGroupIDs(sourceModel string) []string {
	sourceModel = strings.ToLower(strings.TrimSpace(sourceModel))
	switch {
	case sourceModel == "":
		return []string{OAuthQuotaGroupG3Pro, OAuthQuotaGroupG3Flash}
	case strings.Contains(sourceModel, "flash"):
		return []string{OAuthQuotaGroupG3Flash}
	case strings.Contains(sourceModel, "pro"), strings.Contains(sourceModel, "image"), strings.HasPrefix(sourceModel, "imagen"):
		return []string{OAuthQuotaGroupG3Pro}
	default:
		return []string{OAuthQuotaGroupG3Pro, OAuthQuotaGroupG3Flash}
	}
}

func normalizedOAuthQuotaGroupStateGroupIDs(entry OAuthAccountQuotaGroupState) []string {
	groupID := strings.ToLower(strings.TrimSpace(entry.GroupID))
	switch groupID {
	case "":
		return nil
	case "google-gemini-image":
		return legacyGeminiImageGroupIDs(entry.SourceModel)
	default:
		return []string{canonicalOAuthQuotaGroupID(groupID)}
	}
}

func normalizedOAuthQuotaGroupIDsForRemoval(groupID string) []string {
	switch strings.ToLower(strings.TrimSpace(groupID)) {
	case "":
		return nil
	case "google-gemini-image":
		return []string{OAuthQuotaGroupG3Pro, OAuthQuotaGroupG3Flash}
	default:
		return []string{canonicalOAuthQuotaGroupID(groupID)}
	}
}

// OAuthQuotaGroup defines a logical quota family shared across OAuth-backed accounts.
type OAuthQuotaGroup struct {
	ID        string   `yaml:"id" json:"id"`
	Label     string   `yaml:"label" json:"label"`
	Providers []string `yaml:"providers,omitempty" json:"providers,omitempty"`
	Patterns  []string `yaml:"patterns,omitempty" json:"patterns,omitempty"`
	Priority  int      `yaml:"priority" json:"priority"`
	Enabled   bool     `yaml:"enabled" json:"enabled"`

	enabledSet bool `yaml:"-" json:"-"`
}

// OAuthAccountQuotaGroupState stores persisted manual/auto suspension state for
// an auth account and one resolved quota group.
type OAuthAccountQuotaGroupState struct {
	AuthID             string    `yaml:"auth_id" json:"auth_id"`
	GroupID            string    `yaml:"group_id" json:"group_id"`
	ManualSuspended    bool      `yaml:"manual_suspended,omitempty" json:"manual_suspended,omitempty"`
	ManualReason       string    `yaml:"manual_reason,omitempty" json:"manual_reason,omitempty"`
	AutoSuspendedUntil time.Time `yaml:"auto_suspended_until,omitempty" json:"auto_suspended_until,omitempty"`
	AutoReason         string    `yaml:"auto_reason,omitempty" json:"auto_reason,omitempty"`
	SourceModel        string    `yaml:"source_model,omitempty" json:"source_model,omitempty"`
	SourceProvider     string    `yaml:"source_provider,omitempty" json:"source_provider,omitempty"`
	ResetTimeSource    string    `yaml:"reset_time_source,omitempty" json:"reset_time_source,omitempty"`
	UpdatedAt          time.Time `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`
	UpdatedBy          string    `yaml:"updated_by,omitempty" json:"updated_by,omitempty"`
}

func DefaultOAuthQuotaGroups() []OAuthQuotaGroup {
	return []OAuthQuotaGroup{
		{
			ID:        OAuthQuotaGroupClaude45,
			Label:     "Claude",
			Providers: []string{"antigravity", "gemini", "gemini-cli"},
			Patterns: []string{
				"claude-opus-4-6*",
				"claude-opus-4-5*",
				"claude-sonnet-4-6*",
				"claude-sonnet-4-5*",
				"gpt-oss-120b-medium*",
			},
			Priority:  300,
			Enabled:   true,
		},
		{
			ID:        OAuthQuotaGroupG3Pro,
			Label:     "Gemini Pro",
			Providers: []string{"antigravity", "gemini", "gemini-cli"},
			Patterns: []string{
				"gemini-3.1-pro-high*",
				"gemini-3.1-pro-low*",
				"gemini-3-pro-high*",
				"gemini-3-pro-low*",
				"gemini-3-pro-image*",
			},
			Priority:  200,
			Enabled:   true,
		},
		{
			ID:        OAuthQuotaGroupG3Flash,
			Label:     "Gemini Flash",
			Providers: []string{"antigravity", "gemini", "gemini-cli"},
			Patterns: []string{
				"gemini-3-flash*",
				"gemini-3.1-flash*",
				"gemini-3-flash-image*",
				"gemini-3.1-flash-image*",
				"gemini-3-flash-lite*",
				"gemini-3.1-flash-lite*",
			},
			Priority: 100,
			Enabled:  true,
		},
	}
}

func (g *OAuthQuotaGroup) UnmarshalYAML(unmarshal func(any) error) error {
	type rawGroup struct {
		ID        string   `yaml:"id"`
		Label     string   `yaml:"label"`
		Providers []string `yaml:"providers"`
		Patterns  []string `yaml:"patterns"`
		Priority  int      `yaml:"priority"`
		Enabled   *bool    `yaml:"enabled"`
	}

	var raw rawGroup
	if err := unmarshal(&raw); err != nil {
		return err
	}

	g.ID = raw.ID
	g.Label = raw.Label
	g.Providers = raw.Providers
	g.Patterns = raw.Patterns
	g.Priority = raw.Priority
	g.Enabled = false
	g.enabledSet = false
	if raw.Enabled != nil {
		g.Enabled = *raw.Enabled
		g.enabledSet = true
	}
	return nil
}

func (g *OAuthQuotaGroup) UnmarshalJSON(data []byte) error {
	type rawGroup struct {
		ID        string   `json:"id"`
		Label     string   `json:"label"`
		Providers []string `json:"providers"`
		Patterns  []string `json:"patterns"`
		Priority  int      `json:"priority"`
		Enabled   *bool    `json:"enabled"`
	}

	var raw rawGroup
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	g.ID = raw.ID
	g.Label = raw.Label
	g.Providers = raw.Providers
	g.Patterns = raw.Patterns
	g.Priority = raw.Priority
	g.Enabled = false
	g.enabledSet = false
	if raw.Enabled != nil {
		g.Enabled = *raw.Enabled
		g.enabledSet = true
	}
	return nil
}

func defaultOAuthQuotaGroupMap() map[string]OAuthQuotaGroup {
	defaults := DefaultOAuthQuotaGroups()
	out := make(map[string]OAuthQuotaGroup, len(defaults))
	for _, entry := range defaults {
		out[entry.ID] = entry
	}
	return out
}

// NormalizeOAuthQuotaGroups sanitizes quota-group definitions and injects the v1 bootstrap defaults.
func NormalizeOAuthQuotaGroups(entries []OAuthQuotaGroup) []OAuthQuotaGroup {
	defaults := defaultOAuthQuotaGroupMap()
	if len(entries) == 0 {
		return append([]OAuthQuotaGroup(nil), DefaultOAuthQuotaGroups()...)
	}

	byID := make(map[string]OAuthQuotaGroup, len(defaults))
	for id, entry := range defaults {
		byID[id] = entry
	}

	for _, raw := range entries {
		if strings.EqualFold(strings.TrimSpace(raw.ID), "google-gemini-image") {
			continue
		}
		id := canonicalOAuthQuotaGroupID(raw.ID)
		if id == "" {
			continue
		}
		entry := raw
		entry.ID = id
		entry.Label = strings.TrimSpace(entry.Label)
		entry.Providers = NormalizeExcludedModels(entry.Providers)
		entry.Patterns = NormalizeExcludedModels(entry.Patterns)
		if def, ok := defaults[id]; ok {
			if entry.Label == "" {
				entry.Label = def.Label
			}
			if len(entry.Providers) == 0 {
				entry.Providers = append([]string(nil), def.Providers...)
			}
			if len(entry.Patterns) == 0 {
				entry.Patterns = append([]string(nil), def.Patterns...)
			}
			if entry.Priority == 0 {
				entry.Priority = def.Priority
			}
		}
		if entry.Label == "" || len(entry.Providers) == 0 || len(entry.Patterns) == 0 {
			continue
		}
		if raw.enabledSet {
			entry.Enabled = raw.Enabled
		} else if def, ok := defaults[id]; ok {
			entry.Enabled = def.Enabled
		} else {
			entry.Enabled = true
		}
		byID[id] = entry
	}

	out := make([]OAuthQuotaGroup, 0, len(byID))
	for _, entry := range byID {
		if entry.ID == "" || entry.Label == "" || len(entry.Providers) == 0 || len(entry.Patterns) == 0 {
			continue
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	if len(out) == 0 {
		return append([]OAuthQuotaGroup(nil), DefaultOAuthQuotaGroups()...)
	}
	return out
}

// NormalizeOAuthAccountQuotaGroupState sanitizes persisted account/group suspension state.
func NormalizeOAuthAccountQuotaGroupState(entries []OAuthAccountQuotaGroupState) []OAuthAccountQuotaGroupState {
	if len(entries) == 0 {
		return nil
	}
	byKey := make(map[string]OAuthAccountQuotaGroupState, len(entries))
	for _, raw := range entries {
		authID := strings.TrimSpace(raw.AuthID)
		groupIDs := normalizedOAuthQuotaGroupStateGroupIDs(raw)
		if authID == "" || len(groupIDs) == 0 {
			continue
		}
		for _, groupID := range groupIDs {
			entry := raw
			entry.AuthID = authID
			entry.GroupID = groupID
			entry.ManualReason = strings.TrimSpace(entry.ManualReason)
			entry.AutoReason = strings.TrimSpace(entry.AutoReason)
			entry.SourceModel = strings.TrimSpace(entry.SourceModel)
			entry.SourceProvider = strings.ToLower(strings.TrimSpace(entry.SourceProvider))
			entry.ResetTimeSource = strings.TrimSpace(entry.ResetTimeSource)
			entry.UpdatedBy = strings.TrimSpace(entry.UpdatedBy)

			if !entry.ManualSuspended {
				entry.ManualReason = ""
			}
			if entry.AutoSuspendedUntil.IsZero() {
				entry.AutoReason = ""
				entry.ResetTimeSource = ""
			}
			if !entry.ManualSuspended && entry.AutoSuspendedUntil.IsZero() {
				continue
			}
			byKey[authID+"|"+groupID] = entry
		}
	}
	if len(byKey) == 0 {
		return nil
	}
	out := make([]OAuthAccountQuotaGroupState, 0, len(byKey))
	for _, entry := range byKey {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AuthID != out[j].AuthID {
			return out[i].AuthID < out[j].AuthID
		}
		return out[i].GroupID < out[j].GroupID
	})
	return out
}

// UpsertOAuthAccountQuotaGroupState replaces or inserts a normalized state entry.
func UpsertOAuthAccountQuotaGroupState(entries []OAuthAccountQuotaGroupState, entry OAuthAccountQuotaGroupState) []OAuthAccountQuotaGroupState {
	authID := strings.TrimSpace(entry.AuthID)
	groupIDs := normalizedOAuthQuotaGroupStateGroupIDs(entry)
	if authID == "" || len(groupIDs) == 0 {
		return NormalizeOAuthAccountQuotaGroupState(entries)
	}
	entry.AuthID = authID

	groupSet := make(map[string]struct{}, len(groupIDs))
	for _, groupID := range groupIDs {
		groupSet[groupID] = struct{}{}
	}
	filtered := make([]OAuthAccountQuotaGroupState, 0, len(entries)+len(groupIDs))
	for _, candidate := range entries {
		if strings.TrimSpace(candidate.AuthID) == authID {
			if _, ok := groupSet[canonicalOAuthQuotaGroupID(candidate.GroupID)]; ok {
				continue
			}
		}
		filtered = append(filtered, candidate)
	}
	for _, groupID := range groupIDs {
		next := entry
		next.GroupID = groupID
		filtered = append(filtered, next)
	}
	return NormalizeOAuthAccountQuotaGroupState(filtered)
}

// RemoveOAuthAccountQuotaGroupState removes a persisted state entry for one account/group pair.
func RemoveOAuthAccountQuotaGroupState(entries []OAuthAccountQuotaGroupState, authID, groupID string) []OAuthAccountQuotaGroupState {
	authID = strings.TrimSpace(authID)
	groupIDs := normalizedOAuthQuotaGroupIDsForRemoval(groupID)
	if authID == "" || len(groupIDs) == 0 || len(entries) == 0 {
		return NormalizeOAuthAccountQuotaGroupState(entries)
	}
	groupSet := make(map[string]struct{}, len(groupIDs))
	for _, id := range groupIDs {
		groupSet[id] = struct{}{}
	}
	filtered := make([]OAuthAccountQuotaGroupState, 0, len(entries))
	for _, candidate := range entries {
		if strings.TrimSpace(candidate.AuthID) == authID {
			if _, ok := groupSet[canonicalOAuthQuotaGroupID(candidate.GroupID)]; ok {
				continue
			}
		}
		filtered = append(filtered, candidate)
	}
	return NormalizeOAuthAccountQuotaGroupState(filtered)
}
