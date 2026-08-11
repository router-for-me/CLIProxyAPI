// Package management: Manage LiteLLM external sync.
//
// This file implements the external LiteLLM sync under
// /v0/management/litellm/settings and /v0/management/litellm/sync/run, plus
// the local "sync to NixLLM" push under /v0/management/litellm/sync/nixllm.
// An operator configures a base URL + master API key for an EXTERNAL LiteLLM
// instance, and the sync pulls (upserts) that instance's Internal Users and
// API Keys into the local litellm_* tables. The external sync is
// management-only: it never touches the runtime tables and never deletes local
// rows (upsert-only). The "sync to NixLLM" push copies the Manage-LiteLLM
// Internal Users (litellm_internal_users) into the runtime internal_users
// table that the proxy enforces; it needs no external connection.
//
//	GET  /litellm/settings        — current settings (master key is masked)
//	PUT  /litellm/settings        — partial-merge update (master_key optional)
//	POST /litellm/sync/run        — run a sync immediately, return fresh settings
//	POST /litellm/sync/nixllm     — push Manage-LiteLLM users to runtime internal_users
//
// A background sweep (StartLiteLLMSyncSweep) runs the same sync on the
// configured interval when enabled.
package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// liteLLMSyncMaxPages caps pagination so a misbehaving remote cannot trigger an
// unbounded number of requests in a single sync pass.
const liteLLMSyncMaxPages = 100

// liteLLMSyncPageSize is the page size used when talking to the external
// LiteLLM paginated list endpoints.
const liteLLMSyncPageSize = 100

// liteLLMSpendLogPageSize is kept smaller because spend-log rows can contain
// large request/response payloads even when the row count is modest.
const liteLLMSpendLogPageSize = 25

// liteLLMSyncMaxBody is the limit on a single external response body read.
const liteLLMSyncMaxBody = 8 << 20 // 8 MiB

// defaultSyncClient is a shared HTTP client for external LiteLLM requests.
// No TLS customization is used anywhere else in the codebase; 30s timeout
// mirrors pricingsource/external.go.
var defaultSyncClient = &http.Client{Timeout: 30 * time.Second}

// Overlap guard for the background sweep (mirrors the alert sweep).
var liteLLMSyncMutex sync.Mutex

// requireLiteLLMSync returns the Manage-LiteLLM sync + user/key stores.
// Returns false (after writing a 503 response) when not wired.
func (h *Handler) requireLiteLLMSync(c *gin.Context) (*store.LiteLLMSyncStore, *store.LiteLLMUserStore, *store.LiteLLMKeyStore, bool) {
	if h == nil {
		h.pgNotConfigured(c)
		return nil, nil, nil, false
	}
	h.mu.Lock()
	syncStore, users, keys := h.litellmSync, h.litellmUsers, h.litellmKeys
	h.mu.Unlock()
	if syncStore == nil || users == nil || keys == nil {
		h.pgNotConfigured(c)
		return nil, nil, nil, false
	}
	return syncStore, users, keys, true
}

// GetLiteLLMSyncSettings handles GET /v0/management/litellm/settings.
func (h *Handler) GetLiteLLMSyncSettings(c *gin.Context) {
	syncStore, _, _, ok := h.requireLiteLLMSync(c)
	if !ok {
		return
	}
	set, err := syncStore.Get(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": set})
}

// liteLLMSyncSettingsRequest is the partial-merge body for PUT /litellm/settings.
// Pointer-typed fields are applied only when non-nil. master_key: nil = keep the
// stored key, "" = clear it, non-empty = rotate (sealed at rest).
type liteLLMSyncSettingsRequest struct {
	Enabled         *bool   `json:"enabled,omitempty"`
	IntervalSeconds *int    `json:"interval_seconds,omitempty"`
	BaseURL         *string `json:"base_url,omitempty"`
	MasterKey       *string `json:"master_key,omitempty"`
}

// PutLiteLLMSyncSettings handles PUT /v0/management/litellm/settings.
func (h *Handler) PutLiteLLMSyncSettings(c *gin.Context) {
	syncStore, _, _, ok := h.requireLiteLLMSync(c)
	if !ok {
		return
	}
	var req liteLLMSyncSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
		return
	}
	current, err := syncStore.Get(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	set := current
	if req.Enabled != nil {
		set.Enabled = *req.Enabled
	}
	if req.IntervalSeconds != nil {
		set.IntervalSeconds = *req.IntervalSeconds
	}
	if req.BaseURL != nil {
		set.BaseURL = strings.TrimSpace(*req.BaseURL)
	}
	// Validate the base URL scheme (http/https) when non-empty so the sync
	// runner never dials an unexpected scheme.
	if set.BaseURL != "" {
		u, errParse := url.Parse(set.BaseURL)
		if errParse != nil || (u.Scheme != "http" && u.Scheme != "https") {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
				"type":    "invalid_request",
				"message": "base_url must be a valid http(s) URL",
			}})
			return
		}
	}
	persisted, err := syncStore.Upsert(c.Request.Context(), set, req.MasterKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": persisted})
}

// RunLiteLLMSync handles POST /v0/management/litellm/sync/run. It runs a sync
// synchronously (with a bounded timeout) and returns the fresh settings row so
// the dashboard can render the last-sync outcome without a second round-trip.
// Besides users + keys it also pulls spend logs into the runtime usage_events
// table, so a following "sync to NixLLM" migrates the freshest data.
func (h *Handler) RunLiteLLMSync(c *gin.Context) {
	syncStore, users, keys, ok := h.requireLiteLLMSync(c)
	if !ok {
		return
	}
	runtimeKeys, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	runtimeUsers, ok := h.requireUsers(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()
	result := h.runLiteLLMSync(ctx, syncStore, users, keys, runtimeKeys, usage, runtimeUsers)
	if result.err != nil && result.status == store.LiteLLMSyncStatusFailed {
		// Still surface settings (with last_sync_error) + a 502.
		set, _ := syncStore.Get(ctx)
		c.JSON(http.StatusBadGateway, gin.H{"settings": set, "error": gin.H{
			"type": "sync_failed", "message": result.err.Error(),
		}})
		return
	}
	set, err := syncStore.Get(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": set})
}

// RunLiteLLMSyncNixLLM handles POST /v0/management/litellm/sync/nixllm. It
// pushes the Manage-LiteLLM Internal Users (litellm_internal_users) into the
// runtime internal_users table synchronously (with a bounded timeout) and
// returns the fresh settings row so the dashboard can render the last-sync
// outcome without a second round-trip. No external connection is required for
// users/keys (the source is the local litellm_* tables); the spend-log phase
// pulls GET /spend/logs from the external instance and so requires base_url +
// master key. An optional body selects the phases:
//
//	{"include_usage": true}  — overwrite runtime user spend with LiteLLM values
//	{"include_keys":  true}  — migrate API Keys into the runtime api_keys table
//	{"include_logs":  true}  — import spend logs (usage_events) + reconcile spend
func (h *Handler) RunLiteLLMSyncNixLLM(c *gin.Context) {
	syncStore, users, keys, ok := h.requireLiteLLMSync(c)
	if !ok {
		return
	}
	runtimeUsers, ok := h.requireUsers(c)
	if !ok {
		return
	}
	runtimeKeys, usage, _, _, ok := h.requirePG(c)
	if !ok {
		return
	}
	includeUsage, includeKeys, includeLogs := false, false, false
	if c.Request.ContentLength != 0 {
		var req struct {
			IncludeUsage *bool `json:"include_usage"`
			IncludeKeys  *bool `json:"include_keys"`
			IncludeLogs  *bool `json:"include_logs"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": err.Error()}})
			return
		}
		if req.IncludeUsage != nil {
			includeUsage = *req.IncludeUsage
		}
		if req.IncludeKeys != nil {
			includeKeys = *req.IncludeKeys
		}
		if req.IncludeLogs != nil {
			includeLogs = *req.IncludeLogs
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()
	result := h.runLiteLLMSyncNixLLM(ctx, syncStore, users, keys, runtimeUsers, runtimeKeys, usage, includeUsage, includeKeys, includeLogs)
	if result.err != nil && result.status == store.LiteLLMSyncStatusFailed {
		// Still surface settings (with last_nixllm_sync_error) + a 502.
		set, _ := syncStore.Get(ctx)
		c.JSON(http.StatusBadGateway, gin.H{"settings": set, "error": gin.H{
			"type": "sync_failed", "message": result.err.Error(),
		}})
		return
	}
	set, err := syncStore.Get(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"settings": set})
}

// liteLLMSyncResult is the outcome of a single sync pass.
type liteLLMSyncResult struct {
	status string
	users  int
	keys   int
	logs   int
	err    error
}

// runLiteLLMSync performs one synchronous external sync pass: fetch users,
// upsert them; fetch keys, upsert them; then pull spend logs into the runtime
// usage_events table so a subsequent "sync to NixLLM" migrates the freshest
// data possible. runtimeKeys/usage/runtimeUsers may be nil, in which case the
// spend-log phase is skipped (the sweep passes whatever is wired).
func (h *Handler) runLiteLLMSync(ctx context.Context, syncStore *store.LiteLLMSyncStore, users *store.LiteLLMUserStore, keys *store.LiteLLMKeyStore, runtimeKeys *store.APIKeyStore, usage *store.UsageStore, runtimeUsers *store.UserStore) liteLLMSyncResult {
	set, err := syncStore.Get(ctx)
	if err != nil {
		return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: err}
	}
	if strings.TrimSpace(set.BaseURL) == "" {
		err := errors.New("litellm sync: base_url is not configured")
		_ = syncStore.RecordSync(ctx, store.LiteLLMSyncStatusFailed, err.Error(), 0, 0, 0)
		return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: err}
	}
	masterKey, err := syncStore.MasterKey(ctx)
	if err != nil {
		_ = syncStore.RecordSync(ctx, store.LiteLLMSyncStatusFailed, err.Error(), 0, 0, 0)
		return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: err}
	}
	if masterKey == "" {
		err := errors.New("litellm sync: master API key is not configured")
		_ = syncStore.RecordSync(ctx, store.LiteLLMSyncStatusFailed, err.Error(), 0, 0, 0)
		return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: err}
	}
	base := strings.TrimRight(set.BaseURL, "/")

	// 1. Pull internal users.
	remoteUsers, err := h.fetchLiteLLMUsers(ctx, base, masterKey)
	if err != nil {
		_ = syncStore.RecordSync(ctx, store.LiteLLMSyncStatusFailed, err.Error(), 0, 0, 0)
		return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: err}
	}
	for i := range remoteUsers {
		if _, upsErr := users.Upsert(ctx, remoteUsers[i]); upsErr != nil {
			// Skip un-importable rows; a single bad user must not abort the
			// whole sync. Log and keep going.
			log.WithError(upsErr).WithField("user_id", remoteUsers[i].ID).
				Warn("management: litellm sync: skipping un-importable user")
		}
	}
	userCount := len(remoteUsers)

	// 2. Pull API keys (attached to users by user_id; budgets → key policy).
	remoteKeys, err := h.fetchLiteLLMKeys(ctx, base, masterKey)
	if err != nil {
		_ = syncStore.RecordSync(ctx, store.LiteLLMSyncStatusFailed, err.Error(), userCount, 0, 0)
		return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, users: userCount, err: err}
	}
	if len(remoteKeys) == 0 {
		// A misconfigured/foreign /key/list shape that returns 200 but no rows
		// is suspicious — flag it so empty key syncs are not silently accepted.
		log.Warn("management: litellm sync: key list returned 0 parseable keys; " +
			"lokal keys left unchanged (upsert-only). Verify the external /key/list response shape.")
	}
	for i := range remoteKeys {
		k := remoteKeys[i]
		_, upsErr := keys.Upsert(ctx, k.Key, k.Secret, k.HashOverride, k.PrefixOverride, k.Policy)
		if upsErr != nil {
			log.WithError(upsErr).WithField("key_id", k.Key.ID).
				Warn("management: litellm sync: skipping un-importable key")
		}
	}
	keyCount := len(remoteKeys)

	// 3. Pull spend logs from the external instance into the runtime
	// usage_events table (idempotent per request_id) so "sync to NixLLM" with
	// "Include usage logs" migrates the freshest data available. Skipped when
	// the runtime stores are not wired.
	//
	// The reported "logs pulled" count is the number of rows fetched from the
	// external instance (fetchedLogs), not the rows newly inserted this pass.
	// The insert is idempotent (ON CONFLICT (request_id) DO NOTHING), so on a
	// re-sync nearly all rows already exist and "imported" would collapse to 0
	// — misleading the operator into thinking nothing synced. The fetched count
	// is stable and reflects the true pull volume.
	fetchedLogs := 0
	if runtimeKeys != nil && usage != nil && runtimeUsers != nil {
		remoteLogs, errLogs := h.fetchLiteLLMSpendLogs(ctx, base, masterKey)
		fetchedLogs = len(remoteLogs)
		if errLogs != nil {
			_ = syncStore.RecordSync(ctx, store.LiteLLMSyncStatusFailed, errLogs.Error(), userCount, keyCount, 0)
			return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, users: userCount, keys: keyCount, err: errLogs}
		}
		if len(remoteLogs) == 0 {
			log.Warn("management: litellm sync: spend-log fetch returned 0 rows for the lookback window; " +
				"verify the external /spend/logs/v2 response shape and that the window contains activity")
		}
		if len(remoteLogs) > 0 {
			events := h.remoteSpendLogsToEvents(ctx, remoteLogs, runtimeKeys, keys)
			importedLogs, errIns := usage.ImportLiteLLMSpendLogs(ctx, events)
			log.Infof("management: litellm sync: spend logs imported: fetched=%d imported=%d", len(remoteLogs), importedLogs)
			if errIns != nil {
				_ = syncStore.RecordSync(ctx, store.LiteLLMSyncStatusFailed, errIns.Error(), userCount, keyCount, importedLogs)
				return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, users: userCount, keys: keyCount, logs: importedLogs, err: errIns}
			}
			// Failed spend logs also carry the upstream error message, which
			// usage_events intentionally drops. Import it into usage_errors
			// (idempotent per request_id) so error drill-down preserves the
			// message. A failure here is non-fatal to the sync: the events were
			// already imported, so we log and keep going.
			errorRows := h.remoteSpendLogsToErrors(ctx, remoteLogs, runtimeKeys, keys)
			if len(errorRows) > 0 {
				if _, errErr := usage.ImportLiteLLMErrors(ctx, errorRows); errErr != nil {
					log.WithError(errErr).Warn("management: litellm sync: import usage errors from spend logs failed")
				}
			}
			if importedLogs > 0 {
				if _, errRec := runtimeUsers.ReconcileAll(ctx, time.Time{}); errRec != nil {
					log.WithError(errRec).Warn("management: litellm sync: reconcile spend after log import failed")
				}
			}
		}
	}

	if err := syncStore.RecordSync(ctx, store.LiteLLMSyncStatusOK, "", userCount, keyCount, fetchedLogs); err != nil {
		return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: err}
	}
	return liteLLMSyncResult{status: store.LiteLLMSyncStatusOK, users: userCount, keys: keyCount, logs: fetchedLogs}
}

// runLiteLLMSyncNixLLM performs one "sync to NixLLM" pass across the requested
// phases: it first refreshes from the external instance (users + keys pulled
// into the litellm_* tables, spend logs into usage_events) so the migration
// uses the freshest data, then imports Internal Users → runtime internal_users
// and (optionally) API Keys → runtime api_keys. Users are always imported (the
// base phase). A failure in any phase records a failed outcome but users/keys
// already written are kept.
func (h *Handler) runLiteLLMSyncNixLLM(ctx context.Context, syncStore *store.LiteLLMSyncStore, litellmUsers *store.LiteLLMUserStore, litellmKeys *store.LiteLLMKeyStore, runtimeUsers *store.UserStore, runtimeKeys *store.APIKeyStore, usage *store.UsageStore, includeUsage, includeKeys, includeLogs bool) liteLLMSyncResult {
	outcome := store.NixLLMSyncOutcome{IncludedUsage: includeUsage || includeLogs}

	// Phase 0: refresh from the external instance. "Sync now" pulls users +
	// keys + spend logs (into usage_events) so the migration below always runs
	// against just-synced data. Skipped when the external instance is not
	// configured; spend logs can only come from the external instance, so
	// include_logs without a configuration is an error.
	set, errSet := syncStore.Get(ctx)
	if errSet != nil {
		outcome.Error = errSet.Error()
		_ = syncStore.RecordNixLLMSync(ctx, outcome)
		return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: errSet}
	}
	refreshConfigured := strings.TrimSpace(set.BaseURL) != ""
	if refreshConfigured {
		if mk, errKey := syncStore.MasterKey(ctx); errKey != nil || mk == "" {
			refreshConfigured = false
		}
	}
	if refreshConfigured {
		pull := h.runLiteLLMSync(ctx, syncStore, litellmUsers, litellmKeys, runtimeKeys, usage, runtimeUsers)
		if pull.err != nil && pull.status == store.LiteLLMSyncStatusFailed {
			outcome.Error = pull.err.Error()
			_ = syncStore.RecordNixLLMSync(ctx, outcome)
			return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: pull.err}
		}
		if includeLogs {
			// The pull already imported spend logs into usage_events (and
			// reconciled user spend), so the migration reports that count.
			outcome.Logs = pull.logs
		}
	} else if includeLogs {
		err := errors.New("litellm sync: usage-log import requires base_url + master key (configure in Sync Settings)")
		outcome.Error = err.Error()
		_ = syncStore.RecordNixLLMSync(ctx, outcome)
		return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: err}
	}

	// Phase 1: internal users (always) — from the (now fresh) litellm_* tables.
	srcUsers, err := litellmUsers.ListAll(ctx)
	if err != nil {
		outcome.Error = err.Error()
		_ = syncStore.RecordNixLLMSync(ctx, outcome)
		return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: err}
	}
	outcome.SourceUpdatedAt = maxLiteLLMUsersUpdatedAt(srcUsers)
	// When logs are imported, user spend is derived by the pull's ReconcileAll,
	// so the coarse snapshot overwrite is skipped to avoid double-counting.
	importedUsers, _, err := runtimeUsers.ImportLiteLLMUsers(ctx, srcUsers, includeUsage && !includeLogs)
	if err != nil {
		outcome.Error = err.Error()
		_ = syncStore.RecordNixLLMSync(ctx, outcome)
		return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: err}
	}
	outcome.Users = importedUsers

	// Phase 2: API keys (optional) — from the (now fresh) litellm_* tables.
	if includeKeys {
		srcKeys, errKeys := litellmKeys.ListAll(ctx)
		if errKeys != nil {
			outcome.Error = errKeys.Error()
			_ = syncStore.RecordNixLLMSync(ctx, outcome)
			return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: errKeys}
		}
		policies, errPol := litellmKeys.ListAllPolicies(ctx)
		if errPol != nil {
			outcome.Error = errPol.Error()
			_ = syncStore.RecordNixLLMSync(ctx, outcome)
			return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: errPol}
		}
		importedKeys, _, errImp := runtimeKeys.ImportLiteLLMKeys(ctx, srcKeys, policies)
		if errImp != nil {
			outcome.Error = errImp.Error()
			_ = syncStore.RecordNixLLMSync(ctx, outcome)
			return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: errImp}
		}
		outcome.Keys = importedKeys
	}

	if err := syncStore.RecordNixLLMSync(ctx, outcome); err != nil {
		return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: err}
	}
	return liteLLMSyncResult{status: store.LiteLLMSyncStatusOK, users: outcome.Users, keys: outcome.Keys, logs: outcome.Logs}
}

// maxLiteLLMUsersUpdatedAt returns the newest updated_at across the supplied
// Manage-LiteLLM users, or nil when the list is empty. It is the "last update
// from LiteLLM Internal Users" the dashboard surfaces next to the NixLLM sync
// timestamp so an operator can see whether the runtime users are behind the
// Manage-LiteLLM data.
func maxLiteLLMUsersUpdatedAt(users []store.LiteLLMUser) *time.Time {
	var maxT *time.Time
	for i := range users {
		if users[i].UpdatedAt.IsZero() {
			continue
		}
		if maxT == nil || users[i].UpdatedAt.After(*maxT) {
			t := users[i].UpdatedAt
			maxT = &t
		}
	}
	return maxT
}

// remoteSpendLog carries a parsed LiteLLM spend-log row plus the hashed API
// key (SHA-256) so the migration can resolve the runtime api_keys.id before
// inserting the usage event. ErrorMsg is the upstream error message from the
// row's metadata.error_information (only populated on failed requests); it is
// not part of UsageEvent (which intentionally drops it) and is imported into
// usage_errors instead.
type remoteSpendLog struct {
	Event    store.UsageEvent
	KeyHash  string
	ErrorMsg string
}

// liteLLMSpendLogLookback bounds the spend-log import to a rolling window. The
// paginated LiteLLM endpoint requires a date range, and a bounded window keeps
// the sync useful without trying to transfer an unbounded log history.
const liteLLMSpendLogLookback = 30 * 24 * time.Hour

// fetchLiteLLMSpendLogs pulls spend-log rows from the external instance with
// pagination. LiteLLM's legacy /spend/logs endpoint is not paginated and can
// return an arbitrarily large response; /spend/logs/v2 requires a date range
// and returns the array under "data" with total_pages metadata.
func (h *Handler) fetchLiteLLMSpendLogs(ctx context.Context, base, masterKey string) ([]remoteSpendLog, error) {
	var all []remoteSpendLog
	end := time.Now().UTC()
	start := end.Add(-liteLLMSpendLogLookback)
	for page := 1; page <= liteLLMSyncMaxPages; page++ {
		params := url.Values{}
		params.Set("start_date", start.Format("2006-01-02 15:04:05"))
		params.Set("end_date", end.Format("2006-01-02 15:04:05"))
		params.Set("page", strconv.Itoa(page))
		params.Set("page_size", strconv.Itoa(liteLLMSpendLogPageSize))
		endpoint := fmt.Sprintf("%s/spend/logs/v2?%s", base, params.Encode())
		body, totalPages, err := h.liteLLMGet(ctx, endpoint, masterKey)
		if err != nil {
			return nil, err
		}
		parsed, parsedTotalPages, err := parseLiteLLMSpendLogs(body)
		if err != nil {
			return nil, err
		}
		if len(parsed) == 0 {
			// A 200-but-unparseable /spend/logs response is suspicious; flag it
			// so an empty log migration is not silently accepted.
			log.Warn("management: litellm sync: /spend/logs returned 0 parseable rows; " +
				"verify the external /spend/logs response shape.")
			break
		}
		all = append(all, parsed...)
		tp := totalPages
		if tp <= 0 {
			tp = parsedTotalPages
		}
		if tp > 0 && page >= tp {
			break
		}
		if len(parsed) < liteLLMSpendLogPageSize {
			break
		}
	}
	return all, nil
}

// parseLiteLLMSpendLogs parses a /spend/logs response body into remoteSpendLog
// values. LiteLLM has returned the array under different keys across versions
// ("data", "logs", "spend_logs", or a bare array). Rows without a request_id
// are skipped — they have no stable dedup key and would duplicate on re-sync.
func parseLiteLLMSpendLogs(body []byte) ([]remoteSpendLog, int, error) {
	items, err := extractLiteLLMArray(body, "data", "logs", "spend_logs")
	if err != nil {
		return nil, 0, err
	}
	out := make([]remoteSpendLog, 0, len(items))
	for _, item := range items {
		var raw map[string]any
		if err := json.Unmarshal(item, &raw); err != nil || raw == nil {
			continue
		}
		requestID := strings.TrimSpace(strAny(raw["request_id"]))
		if requestID == "" {
			continue
		}
		ev := store.UsageEvent{
			RequestID:           requestID,
			Provider:            "litellm",
			Model:               strAny(raw["model"]),
			Alias:               strAny(raw["alias"]),
			Endpoint:            strAny(raw["call_type"]),
			InputTokens:         int64Any(raw["prompt_tokens"]),
			OutputTokens:        int64Any(raw["completion_tokens"]),
			CachedTokens:        cacheTokensOf(raw, "cache_read_input_tokens"),
			CacheCreationTokens: cacheTokensOf(raw, "cache_creation_input_tokens"),
			TotalTokens:         int64Any(raw["total_tokens"]),
			CostUSD:             floatAny(raw["spend"]),
			OriginalCostUSD:     floatAny(raw["spend"]),
			Generate:            false,
		}
		if ev.TotalTokens == 0 {
			ev.TotalTokens = ev.InputTokens + ev.OutputTokens
		}
		if t := timePtrAny(raw["startTime"]); t != nil {
			ev.RequestedAt = *t
		} else if t := timePtrAny(raw["start_time"]); t != nil {
			ev.RequestedAt = *t
		}
		// Latency: request_duration_ms (float64 milliseconds → int64).
		if d, ok := raw["request_duration_ms"].(float64); ok {
			ev.LatencyMs = int64(d)
		} else if d := int64Any(raw["request_duration_ms"]); d > 0 {
			ev.LatencyMs = d
		}
		// TTFT: completionStartTime - startTime (only when both timestamps are
		// RFC3339 strings, giving sub-ms precision).
		compStart := timePtrAny(raw["completionStartTime"])
		if compStart != nil && !ev.RequestedAt.IsZero() {
			d := compStart.Sub(ev.RequestedAt)
			if d > 0 {
				ev.TTFTMs = d.Milliseconds()
			}
		}
		// Status: a non-empty string like "200" or "500". Treat any non-2xx
		// value as failed; absence of the field means success.
		statusStr := strAny(raw["status"])
		ev.Failed = false
		ev.FailStatusCode = 0
		if statusStr != "" {
			if code, parseErr := strconv.Atoi(statusStr); parseErr == nil {
				ev.FailStatusCode = code
				if code < 200 || code >= 300 {
					ev.Failed = true
				}
			}
		}

		// Error message from metadata.error_information.
		errorMsg := extractErrorMsg(raw)

		out = append(out, remoteSpendLog{
			Event:    ev,
			KeyHash:  strings.TrimSpace(strAny(raw["api_key"])),
			ErrorMsg: errorMsg,
		})
	}
	totalPages := 0
	var env struct {
		TotalPages int `json:"total_pages"`
	}
	if err := json.Unmarshal(body, &env); err == nil {
		totalPages = env.TotalPages
	}
	return out, totalPages, nil
}

// extractErrorMsg pulls the upstream error message out of a spend-log row's
// metadata.error_information (LiteLLM renders the error detail there as a
// nested object with error_code/error_message). Returns "" when absent.
func extractErrorMsg(raw map[string]any) string {
	meta, ok := raw["metadata"].(map[string]any)
	if !ok {
		return ""
	}
	info, ok := meta["error_information"].(map[string]any)
	if !ok {
		return ""
	}
	msg := strings.TrimSpace(strAny(info["error_message"]))
	if msg == "" {
		msg = strings.TrimSpace(strAny(info["error_message_en"]))
	}
	return msg
}

// cacheTokensOf reads a cache-token counter off a spend-log row. LiteLLM
// stores these under metadata.additional_usage_values (not as top-level
// columns); some versions expose them flat on the row, so fall back to that.
func cacheTokensOf(raw map[string]any, key string) int64 {
	if meta, ok := raw["metadata"].(map[string]any); ok {
		if av, ok := meta["additional_usage_values"].(map[string]any); ok {
			if v := int64Any(av[key]); v > 0 {
				return v
			}
		}
	}
	return int64Any(raw[key])
}

// int64Any coerces a JSON value to int64 (tokens count), tolerating float64,
// json.Number, and numeric strings.
func int64Any(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	default:
		return 0
	}
}

// remoteSpendLogsToEvents resolves each spend log's hashed api_key to the
// runtime api_keys.id and stamps the owning user_id, then returns the
// UsageEvents ready for the idempotent bulk insert. Logs whose key is not yet
// migrated are still imported with an empty api_key_id so cost attribution is
// not lost entirely. Resolution is two-tier: the runtime api_keys table is
// tried first, and when it has no match the key is looked up in the
// Manage-LiteLLM store (litellm_api_keys) and upserted into the runtime table
// so the read-path key_alias JOIN resolves and later lookups hit the runtime
// table directly.
func (h *Handler) remoteSpendLogsToEvents(ctx context.Context, logs []remoteSpendLog, runtimeKeys *store.APIKeyStore, litellmKeys *store.LiteLLMKeyStore) []store.UsageEvent {
	events := make([]store.UsageEvent, 0, len(logs))
	hashToIDs := map[string]struct{ keyID, userID string }{}
	for i := range logs {
		lg := logs[i]
		ev := lg.Event
		hash := lg.KeyHash
		if ids, ok := hashToIDs[hash]; ok {
			ev.APIKeyID = ids.keyID
			ev.UserID = ids.userID
		} else if hash != "" {
			if ids, ok := h.resolveSpendLogKey(ctx, hash, runtimeKeys, litellmKeys); ok {
				ev.APIKeyID = ids.keyID
				ev.UserID = ids.userID
				hashToIDs[hash] = ids
			}
		}
		events = append(events, ev)
	}
	return events
}

// resolveSpendLogKey resolves a hashed API key to its runtime id + owner,
// migrating the key from the Manage-LiteLLM store into the runtime api_keys
// table when it is not already present. Returns ok=false when the key cannot
// be resolved (empty hash, or unknown to both stores).
func (h *Handler) resolveSpendLogKey(ctx context.Context, hash string, runtimeKeys *store.APIKeyStore, litellmKeys *store.LiteLLMKeyStore) (struct{ keyID, userID string }, bool) {
	if hash == "" {
		return struct{ keyID, userID string }{}, false
	}
	if key, _, err := runtimeKeys.LookupByHash(ctx, hash); err == nil && key != nil {
		return struct{ keyID, userID string }{key.ID, key.UserID}, true
	}
	// Not in the runtime table: try the Manage-LiteLLM store and migrate it.
	if litellmKeys == nil {
		return struct{ keyID, userID string }{}, false
	}
	lk, err := litellmKeys.LookupByHash(ctx, hash)
	if err != nil || lk == nil {
		return struct{ keyID, userID string }{}, false
	}
	runtimeKey := store.APIKey{
		ID:         lk.ID,
		Name:       lk.Name,
		KeyAlias:   lk.KeyAlias,
		KeyHash:    lk.KeyHash,
		KeyPrefix:  lk.KeyPrefix,
		Status:     lk.Status,
		UserID:     lk.UserID,
		ExpiresAt:  lk.ExpiresAt,
		LastUsedAt: lk.LastUsedAt,
		Metadata:   lk.Metadata,
	}
	if _, errUp := runtimeKeys.Upsert(ctx, runtimeKey, nil); errUp != nil {
		// A failed migration must not abort the sync; the log row still imports
		// with an empty api_key_id so spend attribution is not lost.
		log.WithError(errUp).WithField("key_hash", hash).
			Warn("management: litellm sync: migrating spend-log key to runtime api_keys failed")
		return struct{ keyID, userID string }{}, false
	}
	return struct{ keyID, userID string }{lk.ID, lk.UserID}, true
}

// remoteSpendLogsToErrors builds the failed-attempt rows for the error-message
// import. Only spend logs marked Failed are carried (success rows have no
// error message and belong exclusively in usage_events). The hashed api_key is
// resolved to the runtime api_keys.id + owner user_id exactly as in
// remoteSpendLogsToEvents so usage_errors rows attribute to the same key.
func (h *Handler) remoteSpendLogsToErrors(ctx context.Context, logs []remoteSpendLog, runtimeKeys *store.APIKeyStore, litellmKeys *store.LiteLLMKeyStore) []store.UsageError {
	out := make([]store.UsageError, 0, len(logs))
	hashToIDs := map[string]struct{ keyID, userID string }{}
	for i := range logs {
		lg := logs[i]
		if !lg.Event.Failed {
			continue
		}
		ev := lg.Event
		ue := store.UsageError{
			RequestID:           ev.RequestID,
			APIKeyID:            ev.APIKeyID,
			UserID:              ev.UserID,
			Provider:            ev.Provider,
			ExecutorType:        ev.ExecutorType,
			Model:               ev.Model,
			Alias:               ev.Alias,
			Endpoint:            ev.Endpoint,
			InputTokens:         ev.InputTokens,
			OutputTokens:        ev.OutputTokens,
			ReasoningTokens:     ev.ReasoningTokens,
			CachedTokens:        ev.CachedTokens,
			CacheCreationTokens: ev.CacheCreationTokens,
			TotalTokens:         ev.TotalTokens,
			CostUSD:             ev.CostUSD,
			OriginalCostUSD:     ev.OriginalCostUSD,
			LatencyMs:           ev.LatencyMs,
			TTFTMs:              ev.TTFTMs,
			FailStatusCode:      ev.FailStatusCode,
			ErrorMessage:        lg.ErrorMsg,
			RequestedAt:         ev.RequestedAt,
		}
		hash := lg.KeyHash
		if ids, ok := hashToIDs[hash]; ok {
			ue.APIKeyID = ids.keyID
			ue.UserID = ids.userID
		} else if ids, ok := h.resolveSpendLogKey(ctx, hash, runtimeKeys, litellmKeys); ok {
			ue.APIKeyID = ids.keyID
			ue.UserID = ids.userID
			hashToIDs[hash] = ids
		}
		out = append(out, ue)
	}
	return out
}

// remoteKeyCarrier carries a parsed LiteLLM key plus the raw plaintext secret
// (when the remote returns one) and the hash/prefix overrides (when it returns
// only a hash) so the sync can upsert the row without losing key identity.
type remoteKeyCarrier struct {
	Key            LiteLLMKeyPtr
	Secret         string
	HashOverride   string
	PrefixOverride string
	Policy         *store.LiteLLMPolicy
}

// LiteLLMKeyPtr is a local alias so the carrier type reads cleanly.
type LiteLLMKeyPtr = store.LiteLLMKey

// fetchLiteLLMUsers pulls all internal users from the external instance with
// pagination and parses them into store.LiteLLMUser values.
func (h *Handler) fetchLiteLLMUsers(ctx context.Context, base, masterKey string) ([]store.LiteLLMUser, error) {
	var all []store.LiteLLMUser
	for page := 1; page <= liteLLMSyncMaxPages; page++ {
		endpoint := fmt.Sprintf("%s/user/list?page=%d&page_size=%d", base, page, liteLLMSyncPageSize)
		body, totalPages, err := h.liteLLMGet(ctx, endpoint, masterKey)
		if err != nil {
			return nil, err
		}
		parsed, parsedTotalPages, err := parseLiteLLMUsers(body)
		if err != nil {
			return nil, err
		}
		all = append(all, parsed...)
		if len(parsed) == 0 {
			break
		}
		tp := totalPages
		if tp <= 0 {
			tp = parsedTotalPages
		}
		if tp > 0 && page >= tp {
			break
		}
		if len(parsed) < liteLLMSyncPageSize {
			break
		}
	}
	return all, nil
}

// fetchLiteLLMKeys pulls all API keys from the external instance with
// pagination and parses them into key carriers.
//
// Two query parameters matter (per the LiteLLM OpenAPI spec):
//   - size (default 10, max 100): the server ignores an unknown `page_size`,
//     so without `size` the first page is capped at 10 keys and the loop's
//     "len(parsed) < page_size" break fires after one page (only ~10 keys
//     imported out of 67). We send size=100.
//   - return_full_object (default false): when false the array elements are
//     bare token strings, so the key carries no name/budget/model/owner. We
//     send return_full_object=true so each element is a UserAPIKeyAuth object
//     with complete metadata.
//
// Pagination follows the server-reported total_pages so a large list is
// traversed page by page without overflowing memory.
func (h *Handler) fetchLiteLLMKeys(ctx context.Context, base, masterKey string) ([]remoteKeyCarrier, error) {
	var all []remoteKeyCarrier
	for page := 1; page <= liteLLMSyncMaxPages; page++ {
		endpoint := fmt.Sprintf(
			"%s/key/list?page=%d&size=%d&return_full_object=true",
			base, page, liteLLMSyncPageSize)
		body, totalPages, err := h.liteLLMGet(ctx, endpoint, masterKey)
		if err != nil {
			return nil, err
		}
		parsed, parsedTotalPages, err := parseLiteLLMKeys(body)
		if err != nil {
			return nil, err
		}
		if len(parsed) == 0 {
			// The external instance returned 200 but the response shape did not
			// match any of the envelopes the parser understands. Log the actual
			// JSON structure (keys present + first row keys) so the shape can be
			// diagnosed and supported.
			logLiteLLMShapeDiagnostic(body)
			return all, nil
		}
		all = append(all, parsed...)
		tp := totalPages
		if tp <= 0 {
			tp = parsedTotalPages
		}
		if tp > 0 && page >= tp {
			break
		}
		if len(parsed) < liteLLMSyncPageSize {
			break
		}
	}
	return all, nil
}

// logLiteLLMShapeDiagnostic inspects an unparseable-or-empty /key/list
// response and logs why the sync produced no keys, distinguishing a genuinely
// empty list from an unrecognized shape:
//   - an empty "keys"/"data" array is reported as such (no keys exist),
//   - a present non-array value under a known label, or an unknown envelope,
//     is reported as a shape mismatch with the keys actually present.
func logLiteLLMShapeDiagnostic(body []byte) {
	var obj map[string]json.RawMessage
	knownLabels := []string{"data", "keys", "detail"}
	arrayLen := -1
	knownLabelFound := ""
	knownLabelKind := "absent"
	totalCount := ""

	if err := json.Unmarshal(body, &obj); err == nil {
		// total_count often tells us if the remote simply has no rows.
		if raw, ok := obj["total_count"]; ok {
			totalCount = strings.TrimSpace(string(raw))
		}
		for _, label := range knownLabels {
			raw, ok := obj[label]
			if !ok {
				continue
			}
			knownLabelFound = label
			var arr []map[string]any
			if err := json.Unmarshal(raw, &arr); err == nil {
				arrayLen = len(arr)
				knownLabelKind = "array"
			} else {
				knownLabelKind = "non-array"
			}
			break
		}
	}

	if knownLabelFound != "" && arrayLen == 0 {
		// The remote returned a recognized list label but it is empty.
		log.Warnf("management: litellm sync: /key/list returned an empty %q list (total_count=%s); "+
			"no API keys to import from the external instance", knownLabelFound, totalCount)
		return
	}

	// Otherwise capture the shape for diagnosis.
	topKeys := []string{}
	first := map[string]any(nil)
	if obj != nil {
		for k := range obj {
			topKeys = append(topKeys, k)
		}
		for k, raw := range obj {
			var arr []map[string]any
			if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
				first = arr[0]
				if !containsStr(topKeys, k) {
					topKeys = append(topKeys, k)
				}
			}
		}
	} else {
		// Maybe a bare array — capture the first element's keys.
		var arr []map[string]any
		if err := json.Unmarshal(body, &arr); err == nil && len(arr) > 0 {
			first = arr[0]
		}
	}
	firstKeys := []string{}
	for k := range first {
		firstKeys = append(firstKeys, k)
	}
	log.Warnf("management: litellm sync: /key/list shape not recognized. "+
		"known_label_found=%v known_label_kind=%q array_len=%d total_count=%s top_level_keys=%v first_entry_keys=%v",
		knownLabelFound, knownLabelKind, arrayLen, totalCount, topKeys, firstKeys)
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// liteLLMGet performs an authenticated GET against the external instance and
// returns the body plus the server-reported total_pages (0 when absent).
func (h *Handler) liteLLMGet(ctx context.Context, endpoint, masterKey string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("litellm sync: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+masterKey)
	req.Header.Set("Accept", "application/json")
	resp, err := defaultSyncClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("litellm sync: request %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, liteLLMSyncMaxBody+1))
	if err != nil {
		return nil, 0, fmt.Errorf("litellm sync: read %s: %w", endpoint, err)
	}
	if len(body) > liteLLMSyncMaxBody {
		return nil, 0, fmt.Errorf("litellm sync: response too large (>%d bytes)", liteLLMSyncMaxBody)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, 0, fmt.Errorf("litellm sync: %s: HTTP %d: %s", endpoint, resp.StatusCode, truncateMessage(string(body)))
	}
	// Best-effort total_pages peek.
	totalPages := 0
	var meta struct {
		TotalPages int `json:"total_pages"`
	}
	if err := json.Unmarshal(body, &meta); err == nil {
		totalPages = meta.TotalPages
	}
	return body, totalPages, nil
}

func truncateMessage(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// parseLiteLLMUsers parses a /user/list response body into LiteLLMUser values.
// LiteLLM returns an array under "users". Malformed entries are skipped rather
// than failing the whole parse.
func parseLiteLLMUsers(body []byte) ([]store.LiteLLMUser, int, error) {
	var envelope struct {
		Users      []map[string]any `json:"users"`
		TotalCount int              `json:"total_count"`
		TotalPages int              `json:"total_pages"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, 0, fmt.Errorf("litellm sync: parse users response: %w", err)
	}
	out := make([]store.LiteLLMUser, 0, len(envelope.Users))
	for _, raw := range envelope.Users {
		u := store.LiteLLMUser{}
		u.ID = strAny(raw["user_id"])
		if u.ID == "" {
			continue
		}
		u.UserAlias = strAny(raw["user_alias"])
		u.UserEmail = strAny(raw["user_email"])
		u.UserRole = strAny(raw["user_role"])
		if u.UserRole == "" {
			u.UserRole = store.LiteLLMUserRole
		}
		u.Models = strSliceAny(raw["models"])
		u.Metadata = mapAny(raw["metadata"])
		u.MaxBudget = floatPtrAny(raw["max_budget"])
		u.BudgetDuration = strAny(raw["budget_duration"])
		u.RPMLimit = int64PtrAny(raw["rpm_limit"])
		u.TPMLimit = int64PtrAny(raw["tpm_limit"])
		u.MaxParallelRequests = intPtrAny(raw["max_parallel_requests"])
		u.Spend = floatAny(raw["spend"])
		if t := timePtrAny(raw["created_at"]); t != nil {
			u.CreatedAt = *t
		}
		if t := timePtrAny(raw["updated_at"]); t != nil {
			u.UpdatedAt = *t
		}
		out = append(out, u)
	}
	return out, envelope.TotalPages, nil
}

// parseLiteLLMKeys parses a /key/list response body into key carriers.
// LiteLLM has returned the array under different keys across versions
// ("data", "keys", "detail", or a bare top-level array), and per its OpenAPI
// schema the array items are `anyOf [string, UserAPIKeyAuth, DeletedToken]` —
// i.e. an item may be a bare token string (the SHA-256 hash) rather than an
// object. Every usable item is kept; items with no usable identity are
// skipped. The server-reported total_pages is also returned so the caller can
// page through a large list without relying on a fixed page_size.
func parseLiteLLMKeys(body []byte) ([]remoteKeyCarrier, int, error) {
	items, err := extractLiteLLMArray(body, "data", "keys", "detail")
	if err != nil {
		return nil, 0, err
	}
	out := make([]remoteKeyCarrier, 0, len(items))
	for _, item := range items {
		if carrier, ok := liteLLMKeyFromItem(item); ok {
			out = append(out, carrier)
		}
	}
	// Best-effort total_pages from the enclosing object.
	totalPages := 0
	var env struct {
		TotalPages int `json:"total_pages"`
	}
	if err := json.Unmarshal(body, &env); err == nil {
		totalPages = env.TotalPages
	}
	return out, totalPages, nil
}

// liteLLMKeyFromItem converts one array item (an object or a bare token
// string) into a key carrier. Returns false when the item carries no usable
// key identity.
func liteLLMKeyFromItem(item json.RawMessage) (remoteKeyCarrier, bool) {
	// Object form: UserAPIKeyAuth / DeletedToken / legacy variants.
	var obj map[string]any
	if err := json.Unmarshal(item, &obj); err == nil && obj != nil {
		return liteLLMKeyFromRow(obj)
	}
	// Bare string form: the token hash itself.
	var s string
	if err := json.Unmarshal(item, &s); err == nil && strings.TrimSpace(s) != "" {
		token := strings.TrimSpace(s)
		prefix := store.SecretPrefixOf(token)
		return remoteKeyCarrier{
			Key: store.LiteLLMKey{
				ID:       token,
				Name:     "litellm-" + prefix,
				KeyAlias: "",
				Status:   store.LiteLLMKeyStatusActive,
			},
			HashOverride:   token,
			PrefixOverride: prefix,
		}, true
	}
	return remoteKeyCarrier{}, false
}

// liteLLMKeyFromRow converts a single parsed key row into a carrier. Returns
// false when the row carries no usable key identity. Field names follow the
// LiteLLM OpenAPI /key/list schema (UserAPIKeyAuth): the identity is the
// hashed "token" (there is no key_id); the plaintext secret may be present as
// "api_key"; budget is carried as "max_budget"; "blocked" maps to a revoked
// status.
func liteLLMKeyFromRow(raw map[string]any) (remoteKeyCarrier, bool) {
	key := store.LiteLLMKey{}
	if id := strAny(raw["key_id"]); id != "" {
		key.ID = id
	} else if id := strAny(raw["id"]); id != "" {
		key.ID = id
	}
	key.Name = strAny(raw["key_name"])
	key.KeyAlias = strAny(raw["key_alias"])
	key.Status = strAny(raw["status"])
	if key.Status == "" {
		key.Status = store.LiteLLMKeyStatusActive
	}
	if b, ok := raw["blocked"].(bool); ok && b {
		key.Status = "revoked"
	}
	key.UserID = strAny(raw["user_id"])
	key.Spend = floatAny(raw["spend"])
	key.Tags = strSliceAny(raw["tags"])
	key.Metadata = mapAny(raw["metadata"])
	if et := raw["expires"]; et != nil {
		key.ExpiresAt = timePtrAny(et)
	}
	if et := raw["epoch_expire"]; et != nil && key.ExpiresAt == nil {
		key.ExpiresAt = timePtrAny(et)
	}

	carrier := remoteKeyCarrier{Key: key}
	policy := &store.LiteLLMPolicy{
		RPMLimit:            intPtrAny(raw["rpm_limit"]),
		TPMLimit:            intPtrAny(raw["tpm_limit"]),
		MaxParallelRequests: intPtrAny(raw["max_parallel_requests"]),
		AllowedModels:       strSliceAny(raw["models"]),
		// LiteLLM carries the lifetime budget as max_budget (not "budget").
		BudgetUSD:      floatPtrAny(raw["max_budget"]),
		BudgetDuration: strAny(raw["budget_duration"]),
	}
	if aliases, ok := raw["aliases"].(map[string]any); ok {
		policy.Aliases = make(map[string]string, len(aliases))
		for m, a := range aliases {
			policy.Aliases[m] = strAny(a)
		}
	}
	// "blocked_models" is the allow/deny model list when present; the schema
	// also surfaces a boolean "blocked" (key-level ban), which we mapped to
	// status above.
	if bl, _ := raw["blocked_models"].([]any); len(bl) > 0 {
		policy.BlockedModels = strSliceAny(raw["blocked_models"])
	}
	carrier.Policy = policy

	// Key identity per the OpenAPI schema: "token" is the SHA-256 hash and
	// "api_key" may carry the plaintext secret for privileged callers; there is
	// no "key_id". We always prefer the plaintext (only when it looks like an
	// sk- secret) and otherwise fall back to the token hash. "key" is honored
	// as a fallback hash for non-conforming instances, but never mistaken for a
	// secret unless it carries the sk- marker.
	secret := ""
	for _, k := range []string{"api_key", "key"} {
		v := strings.TrimSpace(strAny(raw[k]))
		if strings.HasPrefix(v, "sk-") {
			secret = v
			break
		}
	}
	hash := strings.TrimSpace(strAny(raw["token"]) + strAny(raw["hash"]))
	if hash == "" && secret == "" {
		k := strings.TrimSpace(strAny(raw["key"]))
		if k != "" {
			hash = k
		}
	}
	if secret != "" {
		carrier.Secret = secret
	} else if hash == "" {
		// No plaintext and no hashed identity — skip this row.
		return carrier, false
	} else {
		carrier.HashOverride = hash
		carrier.PrefixOverride = store.SecretPrefixOf(hash)
	}
	if carrier.Key.ID == "" {
		// Derive a stable ID from the hashed token so re-syncs match; when only
		// a plaintext secret is present, hash it for a deterministic ID.
		if hash != "" {
			carrier.Key.ID = hash
		} else {
			carrier.Key.ID = store.HashSecret(carrier.Secret)
		}
	}
	return carrier, true
}

// extractLiteLLMArray pulls the array of raw items out of a response body,
// trying the supplied JSON object keys in order, then a bare top-level array.
// Items are returned as raw JSON so the caller can handle each element (object
// or bare string token) individually without one malformed row failing the
// whole array.
func extractLiteLLMArray(body []byte, labels ...string) ([]json.RawMessage, error) {
	var rawArr []json.RawMessage
	if err := json.Unmarshal(body, &rawArr); err == nil && len(rawArr) > 0 {
		return rawArr, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("litellm sync: parse response body: %w", err)
	}
	for _, label := range labels {
		raw, ok := obj[label]
		if !ok {
			continue
		}
		var rows []json.RawMessage
		if err := json.Unmarshal(raw, &rows); err == nil {
			return rows, nil
		}
	}
	return nil, nil
}

// StartLiteLLMSyncSweep launches the background auto-sync loop. It is
// nil-safe and self-reschedules: it reads the interval + enabled state from the
// store each tick so operator changes take effect without a restart. Mirrors
// StartAlertSweep. Syncs are skipped when disabled, unconfigured, or already
// running.
func (h *Handler) StartLiteLLMSyncSweep() {
	if h == nil {
		return
	}
	h.mu.Lock()
	syncStore := h.litellmSync
	h.mu.Unlock()
	if syncStore == nil {
		return
	}
	go func() {
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				run := func() {
					if !liteLLMSyncMutex.TryLock() {
						return // already running
					}
					defer liteLLMSyncMutex.Unlock()
					h.mu.Lock()
					st, u, k := h.litellmSync, h.litellmUsers, h.litellmKeys
					rk, us, ru := h.pgAPIKeys, h.pgUsage, h.pgUsers
					h.mu.Unlock()
					if st == nil || u == nil || k == nil {
						return
					}
					cfg, err := st.Get(context.Background())
					if err != nil || !cfg.Enabled {
						return // disabled or unreadable
					}
					if strings.TrimSpace(cfg.BaseURL) == "" {
						return
					}
					set, keyErr := st.MasterKey(context.Background())
					if keyErr != nil || set == "" {
						return
					}
					ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
					defer cancel()
					h.runLiteLLMSync(ctx, st, u, k, rk, us, ru)
				}
				run()
			}
			interval := syncStore.Interval(context.Background())
			if interval <= 0 {
				interval = time.Duration(store.LiteLLMSyncDefaultInterval) * time.Second
			}
			timer.Reset(interval)
		}
	}()
}

// --- shared JSON coercion helpers (defensive parsing) -----------------------

func strAny(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	default:
		return ""
	}
}

func floatAny(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case json.Number:
		f, _ := t.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	default:
		return 0
	}
}

func floatPtrAny(v any) *float64 {
	if v == nil {
		return nil
	}
	f := floatAny(v)
	return &f
}

func int64PtrAny(v any) *int64 {
	if v == nil {
		return nil
	}
	var n int64
	switch t := v.(type) {
	case float64:
		n = int64(t)
	case json.Number:
		n, _ = t.Int64()
	case string:
		n, _ = strconv.ParseInt(t, 10, 64)
	}
	return &n
}

func intPtrAny(v any) *int {
	if v == nil {
		return nil
	}
	var n int
	switch t := v.(type) {
	case float64:
		n = int(t)
	case json.Number:
		i, _ := t.Int64()
		n = int(i)
	case string:
		n, _ = strconv.Atoi(t)
	}
	return &n
}

func strSliceAny(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s := strAny(item)
		if s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mapAny(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// timePtrAny parses a LiteLLM expires value: epoch seconds (number or numeric
// string) or an RFC3339 string.
func timePtrAny(v any) *time.Time {
	switch t := v.(type) {
	case float64:
		if t <= 0 {
			return nil
		}
		ts := time.Unix(int64(t), 0).UTC()
		return &ts
	case json.Number:
		i, _ := t.Int64()
		if i <= 0 {
			return nil
		}
		ts := time.Unix(i, 0).UTC()
		return &ts
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return nil
		}
		if ts, err := time.Parse(time.RFC3339, s); err == nil {
			return &ts
		}
		if i, err := strconv.ParseInt(s, 10, 64); err == nil && i > 0 {
			ts := time.Unix(i, 0).UTC()
			return &ts
		}
		return nil
	default:
		return nil
	}
}
