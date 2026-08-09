// Package management: Manage LiteLLM external sync.
//
// This file implements the external LiteLLM sync under
// /v0/management/litellm/settings and /v0/management/litellm/sync/run.
// An operator configures a base URL + master API key for an EXTERNAL LiteLLM
// instance, and the sync pulls (upserts) that instance's Internal Users and
// API Keys into the local litellm_* tables. The sync is management-only: it
// never touches the runtime tables and never deletes local rows (upsert-only).
//
//	GET  /litellm/settings    — current settings (master key is masked)
//	PUT  /litellm/settings    — partial-merge update (master_key optional)
//	POST /litellm/sync/run    — run a sync immediately, return fresh settings
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
func (h *Handler) RunLiteLLMSync(c *gin.Context) {
	syncStore, users, keys, ok := h.requireLiteLLMSync(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	result := h.runLiteLLMSync(ctx, syncStore, users, keys)
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

// liteLLMSyncResult is the outcome of a single sync pass.
type liteLLMSyncResult struct {
	status string
	users  int
	keys   int
	err    error
}

// runLiteLLMSync performs one synchronous sync pass: fetch users, upsert them;
// fetch keys, upsert them; record the outcome on the settings row.
func (h *Handler) runLiteLLMSync(ctx context.Context, syncStore *store.LiteLLMSyncStore, users *store.LiteLLMUserStore, keys *store.LiteLLMKeyStore) liteLLMSyncResult {
	set, err := syncStore.Get(ctx)
	if err != nil {
		return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: err}
	}
	if strings.TrimSpace(set.BaseURL) == "" {
		err := errors.New("litellm sync: base_url is not configured")
		_ = syncStore.RecordSync(ctx, store.LiteLLMSyncStatusFailed, err.Error(), 0, 0)
		return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: err}
	}
	masterKey, err := syncStore.MasterKey(ctx)
	if err != nil {
		_ = syncStore.RecordSync(ctx, store.LiteLLMSyncStatusFailed, err.Error(), 0, 0)
		return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: err}
	}
	if masterKey == "" {
		err := errors.New("litellm sync: master API key is not configured")
		_ = syncStore.RecordSync(ctx, store.LiteLLMSyncStatusFailed, err.Error(), 0, 0)
		return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: err}
	}
	base := strings.TrimRight(set.BaseURL, "/")

	// 1. Pull internal users.
	remoteUsers, err := h.fetchLiteLLMUsers(ctx, base, masterKey)
	if err != nil {
		_ = syncStore.RecordSync(ctx, store.LiteLLMSyncStatusFailed, err.Error(), 0, 0)
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

	// 2. Pull API keys (attached to users by user_id; budgets → key policy).
	remoteKeys, err := h.fetchLiteLLMKeys(ctx, base, masterKey)
	if err != nil {
		_ = syncStore.RecordSync(ctx, store.LiteLLMSyncStatusFailed, err.Error(), len(remoteUsers), 0)
		return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: err}
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

	userCount := len(remoteUsers)
	keyCount := len(remoteKeys)
	if err := syncStore.RecordSync(ctx, store.LiteLLMSyncStatusOK, "", userCount, keyCount); err != nil {
		return liteLLMSyncResult{status: store.LiteLLMSyncStatusFailed, err: err}
	}
	return liteLLMSyncResult{status: store.LiteLLMSyncStatusOK, users: userCount, keys: keyCount}
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
					ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
					defer cancel()
					h.runLiteLLMSync(ctx, st, u, k)
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
