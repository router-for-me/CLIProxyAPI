package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	// usageSnapshotName lives in the auth directory. It must not end in .json,
	// or the watcher would load it as a credential.
	usageSnapshotName = "usage-snapshot.cache"
	usageRefreshTick  = time.Minute
	// Meta locks out a login that is polled often (a day-long lockout on
	// 2026-09-02 at shorter intervals), so Muse is asked at most this often.
	usageMetaFloor = 3 * time.Hour
)

// usageRecord is what the last usage poll returned for one credential.
type usageRecord struct {
	Plan    string                 `json:"plan,omitempty"`
	Windows []coreauth.UsageWindow `json:"windows,omitempty"`
	// FetchedAt is the last successful poll; AttemptedAt the last try.
	FetchedAt   time.Time `json:"fetched_at,omitzero"`
	AttemptedAt time.Time `json:"attempted_at,omitzero"`
	Error       string    `json:"error,omitempty"`
	// Signals and ObservedAt carry the Claude/Codex quota headers across a
	// restart, so consume-first does not rank every account as unknown.
	Signals    map[string]string `json:"signals,omitempty"`
	ObservedAt time.Time         `json:"observed_at,omitzero"`
}

type usageStore struct {
	mu      sync.Mutex
	records map[string]usageRecord
	path    string
}

func (s *usageStore) get(id string) usageRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.records[id]
}

func (s *usageStore) set(id string, record usageRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records == nil {
		s.records = make(map[string]usageRecord)
	}
	s.records[id] = record
}

// StartUsageRefresher polls provider usage endpoints for idle credentials until
// ctx ends. It does nothing while routing.usage-refresh.interval is unset, and
// reads the config on every tick, so it follows hot reloads.
func (h *Handler) StartUsageRefresher(ctx context.Context) {
	if h == nil || h.authManager == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(usageRefreshTick)
		defer ticker.Stop()
		for {
			h.refreshUsage(ctx, time.Now())
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func usageRefreshInterval(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.Home.Enabled {
		return 0
	}
	interval, errParse := time.ParseDuration(strings.TrimSpace(cfg.Routing.UsageRefresh.Interval))
	if errParse != nil {
		return 0
	}
	return interval
}

func usageProviderEnabled(cfg *config.Config, provider string) bool {
	if len(cfg.Routing.UsageRefresh.Providers) == 0 {
		return true
	}
	for _, name := range cfg.Routing.UsageRefresh.Providers {
		if strings.EqualFold(strings.TrimSpace(name), provider) {
			return true
		}
	}
	return false
}

// refreshUsage runs one pass: restore persisted quota headers, poll every
// credential that is due, and save the snapshot when anything changed.
func (h *Handler) refreshUsage(ctx context.Context, now time.Time) {
	cfg := h.configSnapshot()
	interval := usageRefreshInterval(cfg)
	if interval <= 0 {
		return
	}
	h.loadUsageSnapshot(cfg)
	changed := false
	for _, auth := range h.authManager.List() {
		if ctx.Err() != nil {
			return
		}
		record := h.usage.get(auth.ID)
		observes := coreauth.ProviderSupportsQuotaObservation(auth.Provider)
		if observes && len(record.Signals) > 0 {
			// Credentials register after startup, so restoring is retried each pass;
			// ObserveQuota ignores it once newer headers exist.
			h.authManager.ObserveQuota(auth.ID, signalHeaders(record.Signals), record.ObservedAt)
		}
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		fetch := usageFetchers[provider]
		every := interval
		if provider == "meta" {
			every = max(every, usageMetaFloor)
		}
		observed := auth.Quota.ObservedAt
		if record.ObservedAt.After(observed) {
			observed = record.ObservedAt
		}
		due := fetch != nil && !auth.Disabled && auth.AuthSourceKind() != coreauth.AuthSourceConfig &&
			usageProviderEnabled(cfg, provider) && now.Sub(record.AttemptedAt) >= every &&
			// An account serving traffic already reports its own headers.
			!(observes && now.Sub(observed) < every)
		if due {
			record = h.pollUsage(ctx, auth, fetch, record, now)
			changed = true
		}
		if current, ok := h.authManager.GetByID(auth.ID); ok && observes && current.Quota.ObservedAt.After(record.ObservedAt) {
			record.Signals, record.ObservedAt = current.Quota.Signals, current.Quota.ObservedAt
			changed = true
		}
		h.usage.set(auth.ID, record)
	}
	if changed {
		h.saveUsageSnapshot()
	}
}

func (h *Handler) pollUsage(ctx context.Context, auth *coreauth.Auth, fetch usageFetcher, record usageRecord, now time.Time) usageRecord {
	ctx, cancel := context.WithTimeout(ctx, defaultAPICallTimeout)
	defer cancel()
	record.AttemptedAt = now
	token, errToken := h.usageToken(ctx, auth)
	if errToken == nil && token == "" {
		errToken = errors.New("no token on the credential")
	}
	if errToken != nil {
		record.Error = "token: " + errToken.Error()
		return record
	}
	client := &http.Client{Transport: h.apiCallTransport(auth, "")}
	result, errFetch := fetch(ctx, client, auth, token)
	if errFetch != nil {
		log.WithError(errFetch).Debugf("usage refresh: %s %s", auth.Provider, auth.ID)
		record.Error = errFetch.Error()
		return record
	}
	if len(result.headers) > 0 {
		h.authManager.ObserveQuota(auth.ID, result.headers, now)
	}
	record.Plan, record.Windows, record.FetchedAt, record.Error = result.plan, result.windows, now, ""
	return record
}

// usageToken returns the bearer token a provider's usage endpoint accepts.
// Muse reports usage on the key-mint call, which takes the device (dca) token.
func (h *Handler) usageToken(ctx context.Context, auth *coreauth.Auth) (string, error) {
	if strings.EqualFold(auth.Provider, "meta") {
		for _, key := range []string{"dca_token", "access_token"} {
			if value, _ := auth.Metadata[key].(string); strings.HasPrefix(strings.TrimSpace(value), "dca:") {
				return strings.TrimSpace(value), nil
			}
		}
		return "", nil
	}
	return h.resolveTokenForAuth(ctx, auth, "")
}

func signalHeaders(signals map[string]string) http.Header {
	headers := make(http.Header, len(signals))
	for key, value := range signals {
		headers.Set(key, value)
	}
	return headers
}

// loadUsageSnapshot reads the persisted snapshot once per auth directory.
func (h *Handler) loadUsageSnapshot(cfg *config.Config) {
	dir, errDir := util.ResolveAuthDir(cfg.AuthDir)
	if errDir != nil || dir == "" {
		return
	}
	path := filepath.Join(dir, usageSnapshotName)
	h.usage.mu.Lock()
	defer h.usage.mu.Unlock()
	if h.usage.path == path {
		return
	}
	h.usage.path = path
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		if !os.IsNotExist(errRead) {
			log.WithError(errRead).Warn("usage refresh: failed to read snapshot")
		}
		return
	}
	var records map[string]usageRecord
	if errUnmarshal := json.Unmarshal(data, &records); errUnmarshal != nil {
		log.WithError(errUnmarshal).Warn("usage refresh: ignoring unreadable snapshot")
		return
	}
	if h.usage.records == nil {
		h.usage.records = make(map[string]usageRecord, len(records))
	}
	for id, record := range records {
		if _, exists := h.usage.records[id]; !exists {
			h.usage.records[id] = record
		}
	}
}

func (h *Handler) saveUsageSnapshot() {
	h.usage.mu.Lock()
	path := h.usage.path
	data, errMarshal := json.Marshal(h.usage.records)
	h.usage.mu.Unlock()
	if path == "" || errMarshal != nil {
		return
	}
	tmp := path + ".tmp"
	if errWrite := os.WriteFile(tmp, data, 0o600); errWrite != nil {
		log.WithError(errWrite).Warn("usage refresh: failed to write snapshot")
		return
	}
	if errRename := os.Rename(tmp, path); errRename != nil {
		log.WithError(errRename).Warn("usage refresh: failed to replace snapshot")
	}
}

// usageEntry is the usage a management view shows for one credential: the main
// Claude/Codex windows from the live quota headers (polled or from traffic),
// plus the windows and plan from the last poll.
func (h *Handler) usageEntry(auth *coreauth.Auth) gin.H {
	record := h.usage.get(auth.ID)
	windows := record.Windows
	if coreauth.ProviderSupportsQuotaObservation(auth.Provider) {
		windows = append(coreauth.QuotaUsageWindows(auth.Quota), record.Windows...)
	}
	entry := gin.H{"windows": windows}
	if record.Plan != "" {
		entry["plan"] = record.Plan
	} else if plan := auth.Quota.Signals["plan"]; plan != "" {
		// Devin's executor records its plan with its quota.
		entry["plan"] = plan
	}
	if !record.FetchedAt.IsZero() {
		entry["fetched_at"] = record.FetchedAt
	}
	if record.Error != "" {
		entry["error"] = record.Error
	}
	return entry
}

// ListCredentialUsage returns every credential the router can pick, file-backed
// and config-backed, with its usage, cooldowns and the routing strategy. It is
// the hub dashboard's single data source.
func (h *Handler) ListCredentialUsage(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	cfg := h.configSnapshot()
	now := time.Now().UTC()
	accounts := make([]gin.H, 0)
	for _, auth := range h.authManager.List() {
		entry := h.buildAuthFileEntry(auth, authFileQuotaContext{v8: true})
		if entry == nil {
			if auth.AuthSourceKind() != coreauth.AuthSourceConfig {
				continue
			}
			entry = configCredentialEntry(auth, now)
			entry["usage"] = h.usageEntry(auth)
		}
		entry["cooldowns"] = coreauth.CooldownSnapshotForAuth(auth, now)
		accounts = append(accounts, entry)
	}
	response := gin.H{"observed_at": now, "accounts": accounts}
	if cfg != nil {
		response["strategy"] = cfg.Routing.Strategy
		response["usage_refresh"] = cfg.Routing.UsageRefresh
	}
	c.JSON(http.StatusOK, response)
}

// configCredentialEntry describes an API-key credential from the config file,
// which the credential file listing leaves out.
func configCredentialEntry(auth *coreauth.Auth, now time.Time) gin.H {
	unavailable, status, statusMessage, nextRetry := reconcileAuthFileCooldownState(auth, now)
	name := strings.TrimSpace(auth.Label)
	if name == "" {
		name = auth.ID
	}
	entry := gin.H{
		"id":              auth.ID,
		"name":            name,
		"label":           auth.Label,
		"provider":        strings.TrimSpace(auth.Provider),
		"source":          "config",
		"disabled":        auth.Disabled,
		"status":          status,
		"status_message":  statusMessage,
		"unavailable":     unavailable,
		"recent_requests": auth.RecentRequestsSnapshot(now),
	}
	if !nextRetry.IsZero() {
		entry["next_retry_after"] = nextRetry
	}
	return entry
}
