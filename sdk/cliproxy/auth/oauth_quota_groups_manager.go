package auth

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

func (m *Manager) configFilePathValue() string {
	if m == nil {
		return ""
	}
	if raw, ok := m.configFilePath.Load().(string); ok {
		if trimmed := strings.TrimSpace(raw); trimmed != "" {
			return trimmed
		}
	}
	wd, err := os.Getwd()
	if err != nil || strings.TrimSpace(wd) == "" {
		return ""
	}
	return filepath.Join(wd, "config.yaml")
}

func (m *Manager) persistRuntimeConfigSnapshot(cfg *internalconfig.Config) error {
	if m == nil || cfg == nil {
		return nil
	}
	path := m.configFilePathValue()
	if path == "" {
		return nil
	}
	return internalconfig.SaveConfigPreserveComments(path, cfg)
}

func findOAuthQuotaGroupState(entries []internalconfig.OAuthAccountQuotaGroupState, authID, groupID string) (internalconfig.OAuthAccountQuotaGroupState, bool) {
	authID = strings.TrimSpace(authID)
	groupID = strings.ToLower(strings.TrimSpace(groupID))
	for _, entry := range entries {
		if strings.TrimSpace(entry.AuthID) == authID && strings.EqualFold(strings.TrimSpace(entry.GroupID), groupID) {
			return entry, true
		}
	}
	return internalconfig.OAuthAccountQuotaGroupState{}, false
}

func (m *Manager) setOAuthQuotaGroupAutoStateLocked(auth *Auth, model, provider string, until time.Time, resetTimeSource string, now time.Time) (*internalconfig.Config, bool) {
	if m == nil || auth == nil || until.IsZero() {
		return nil, false
	}
	group, ok := resolveOAuthQuotaGroup(auth, model)
	if !ok {
		return nil, false
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}

	current, _ := findOAuthQuotaGroupState(cfg.OAuthAccountQuotaGroupState, auth.ID, group.ID)
	next := current
	next.AuthID = auth.ID
	next.GroupID = group.ID
	next.AutoSuspendedUntil = until.UTC()
	next.AutoReason = "quota_exhausted"
	next.SourceModel = strings.TrimSpace(model)
	next.SourceProvider = strings.ToLower(strings.TrimSpace(provider))
	next.ResetTimeSource = strings.TrimSpace(resetTimeSource)
	next.UpdatedAt = now.UTC()
	next.UpdatedBy = "system:auto"

	if current.ManualSuspended == next.ManualSuspended &&
		current.ManualReason == next.ManualReason &&
		current.AutoSuspendedUntil.Equal(next.AutoSuspendedUntil) &&
		current.AutoReason == next.AutoReason &&
		current.SourceModel == next.SourceModel &&
		current.SourceProvider == next.SourceProvider &&
		current.ResetTimeSource == next.ResetTimeSource {
		return nil, false
	}

	cfg.OAuthAccountQuotaGroupState = internalconfig.UpsertOAuthAccountQuotaGroupState(cfg.OAuthAccountQuotaGroupState, next)
	return cfg, true
}

func (m *Manager) clearOAuthQuotaGroupAutoStateLocked(auth *Auth, model string, now time.Time) (*internalconfig.Config, bool) {
	if m == nil || auth == nil {
		return nil, false
	}
	group, ok := resolveOAuthQuotaGroup(auth, model)
	if !ok {
		return nil, false
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	current, ok := findOAuthQuotaGroupState(cfg.OAuthAccountQuotaGroupState, auth.ID, group.ID)
	if !ok || current.AutoSuspendedUntil.IsZero() {
		return nil, false
	}

	current.AutoSuspendedUntil = time.Time{}
	current.AutoReason = ""
	current.SourceModel = ""
	current.SourceProvider = ""
	current.ResetTimeSource = ""
	current.UpdatedAt = now.UTC()
	current.UpdatedBy = "system:auto-clear"

	if current.ManualSuspended {
		cfg.OAuthAccountQuotaGroupState = internalconfig.UpsertOAuthAccountQuotaGroupState(cfg.OAuthAccountQuotaGroupState, current)
	} else {
		cfg.OAuthAccountQuotaGroupState = internalconfig.RemoveOAuthAccountQuotaGroupState(cfg.OAuthAccountQuotaGroupState, auth.ID, group.ID)
	}
	return cfg, true
}

// ClearExpiredOAuthQuotaGroupAutoStates removes auto cooldown state that has
// already passed its reset time while preserving manual suspensions.
func (m *Manager) ClearExpiredOAuthQuotaGroupAutoStates(now time.Time) bool {
	if m == nil {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()

	var (
		cfgToPublish  *internalconfig.Config
		authSnapshots []*Auth
	)

	m.mu.Lock()
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}

	entries := internalconfig.NormalizeOAuthAccountQuotaGroupState(cfg.OAuthAccountQuotaGroupState)
	if len(entries) == 0 {
		m.mu.Unlock()
		return false
	}

	nextEntries := make([]internalconfig.OAuthAccountQuotaGroupState, 0, len(entries))
	affectedAuthIDs := make(map[string]struct{})
	changed := false

	for _, entry := range entries {
		if entry.AutoSuspendedUntil.IsZero() || entry.AutoSuspendedUntil.After(now) {
			nextEntries = append(nextEntries, entry)
			continue
		}

		changed = true
		if authID := strings.TrimSpace(entry.AuthID); authID != "" {
			affectedAuthIDs[authID] = struct{}{}
		}

		entry.AutoSuspendedUntil = time.Time{}
		entry.AutoReason = ""
		entry.SourceModel = ""
		entry.SourceProvider = ""
		entry.ResetTimeSource = ""
		entry.UpdatedAt = now
		entry.UpdatedBy = "system:auto-expire"

		if entry.ManualSuspended {
			nextEntries = append(nextEntries, entry)
		}
	}

	if changed {
		cfg.OAuthAccountQuotaGroupState = internalconfig.NormalizeOAuthAccountQuotaGroupState(nextEntries)
		cfgToPublish = cfg
		for authID := range affectedAuthIDs {
			auth := m.auths[authID]
			if auth == nil {
				continue
			}
			updateAggregatedAvailability(auth, now)
			auth.UpdatedAt = now
			authSnapshots = append(authSnapshots, auth.Clone())
		}
	}
	m.mu.Unlock()

	if cfgToPublish == nil {
		return false
	}

	m.SetConfig(cfgToPublish)
	if err := m.persistRuntimeConfigSnapshot(cfgToPublish); err != nil {
		log.WithError(err).Warn("failed to persist expired oauth-account-quota-group-state cleanup")
	}
	if m.scheduler != nil {
		for _, authSnapshot := range authSnapshots {
			if authSnapshot != nil {
				m.scheduler.upsertAuth(authSnapshot)
			}
		}
	}
	return true
}
