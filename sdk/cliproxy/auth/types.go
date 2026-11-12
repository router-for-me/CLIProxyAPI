package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	baseauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

// PostAuthHook defines a function that is called after an Auth record is created
// but before it is persisted to storage. This allows for modification of the
// Auth record (e.g., injecting metadata) based on external context.
type PostAuthHook func(context.Context, *Auth) error

// RequestInfo holds information extracted from the HTTP request.
// It is injected into the context passed to PostAuthHook.
type RequestInfo struct {
	Query   url.Values
	Headers http.Header
}

type requestInfoKey struct{}

// WithRequestInfo returns a new context with the given RequestInfo attached.
func WithRequestInfo(ctx context.Context, info *RequestInfo) context.Context {
	return context.WithValue(ctx, requestInfoKey{}, info)
}

// GetRequestInfo retrieves the RequestInfo from the context, if present.
func GetRequestInfo(ctx context.Context) *RequestInfo {
	if val, ok := ctx.Value(requestInfoKey{}).(*RequestInfo); ok {
		return val
	}
	return nil
}

// Auth encapsulates the runtime state and metadata associated with a single credential.
type Auth struct {
	// ID uniquely identifies the auth record across restarts.
	ID string `json:"id"`
	// RegistrationEpoch tracks monotonic registration cycles across unregister/re-register.
	RegistrationEpoch uint64 `json:"registration_epoch,omitempty"`
	// CredentialVersion tracks monotonic credential replacements for secret material.
	CredentialVersion uint64 `json:"credential_version,omitempty"`
	// Generation tracks monotonic mutations to resolve scheduler/reconcile snapshot races.
	Generation uint64 `json:"generation,omitempty"`
	// Index is a stable runtime identifier derived from auth metadata (not persisted).
	Index string `json:"-"`
	// Provider is the upstream provider key (e.g. "gemini", "claude").
	Provider string `json:"provider"`
	// Prefix optionally namespaces models for routing (e.g., "teamA/gemini-3-pro-preview").
	Prefix string `json:"prefix,omitempty"`
	// FileName stores the relative or absolute path of the backing auth file.
	FileName string `json:"-"`
	// Storage holds the token persistence implementation used during login flows.
	Storage baseauth.TokenStorage `json:"-"`
	// CodexRefreshPatch carries an immutable pending credential update, never configuration.
	CodexRefreshPatch *codex.CredentialRefreshPatch `json:"-"`
	// Label is an optional human readable label for logging.
	Label string `json:"label,omitempty"`
	// Status is the lifecycle status managed by the AuthManager.
	Status Status `json:"status"`
	// StatusMessage holds a short description for the current status.
	StatusMessage string `json:"status_message,omitempty"`
	// Disabled indicates the auth is intentionally disabled by operator.
	Disabled bool `json:"disabled"`
	// Unavailable flags transient provider unavailability (e.g. quota exceeded).
	Unavailable bool `json:"unavailable"`
	// ProxyURL overrides the global proxy setting for this auth if provided.
	ProxyURL string `json:"proxy_url,omitempty"`
	// Attributes stores provider specific metadata needed by executors (immutable configuration).
	Attributes map[string]string `json:"attributes,omitempty"`
	// Metadata stores runtime mutable provider state (e.g. tokens, cookies).
	Metadata map[string]any `json:"metadata,omitempty"`
	// Quota captures recent quota information for load balancers.
	Quota QuotaState `json:"quota"`
	// LastError stores the last failure encountered while executing or refreshing.
	LastError *Error `json:"last_error,omitempty"`
	// CreatedAt is the creation timestamp in UTC.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt is the last modification timestamp in UTC.
	UpdatedAt time.Time `json:"updated_at"`
	// LastRefreshedAt records the last successful refresh time in UTC.
	LastRefreshedAt time.Time `json:"last_refreshed_at"`
	// NextRefreshAfter is the earliest time a refresh should retrigger.
	NextRefreshAfter time.Time `json:"next_refresh_after"`
	// RefreshFailures tracks consecutive refresh failures for exponential backoff (in-memory only).
	RefreshFailures int `json:"-"`
	// NextRetryAfter is the earliest time a retry should retrigger.
	NextRetryAfter time.Time `json:"next_retry_after"`
	// ModelStates tracks per-model runtime availability data.
	ModelStates map[string]*ModelState `json:"model_states,omitempty"`

	// Runtime carries non-serialisable data used during execution (in-memory only).
	Runtime any `json:"-"`

	// RejectedAccessToken tracks the access token rejected by upstream 401 until a refresh succeeds.
	RejectedAccessToken string `json:"-"`
	// RejectedSIWCAccessToken tracks the nested SIWC access token rejected by upstream 401 until replaced.
	RejectedSIWCAccessToken string `json:"-"`

	Success int64 `json:"-"`
	Failed  int64 `json:"-"`

	// cooldownRecordedSeq is the execution sequence watermark captured when
	// the current auth-level cooldown was recorded. Successes from executions
	// stamped at or below it were dispatched before the cooldown existed and
	// must not clear it. Runtime only: zero after restart so restored
	// cooldowns can still be cleared by any success.
	cooldownRecordedSeq uint64 `json:"-"`

	recentRequests recentRequestRing `json:"-"`
	indexAssigned  bool              `json:"-"`
	// fileSnapshotPath and fileSnapshotDigest bind an unversioned file-synthesized
	// Auth snapshot to the source file path and SHA-256 byte digest it observed.
	// Cleared when committed to a Manager so runtime clones are not treated as
	// unversioned file snapshots.
	fileSnapshotPath   string `json:"-"`
	fileSnapshotDigest string `json:"-"`
}

var readAuthSnapshotFileFunc = os.ReadFile

func canonicalAuthFileDigest(data []byte) string {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return ""
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var parsed any
	if errDecode := decoder.Decode(&parsed); errDecode == nil {
		var trailing any
		if errTrailing := decoder.Decode(&trailing); errTrailing == io.EOF {
			if canonical, errMarshal := json.Marshal(parsed); errMarshal == nil {
				sum := sha256.Sum256(canonical)
				return hex.EncodeToString(sum[:])
			}
		}
	}
	sum := sha256.Sum256(trimmed)
	return hex.EncodeToString(sum[:])
}

// BindFileSnapshot binds an unversioned file-synthesized Auth snapshot to the
// source file path and canonical SHA-256 digest of the bytes from which it was parsed.
func (a *Auth) BindFileSnapshot(path string, data []byte) {
	if a == nil || len(data) == 0 {
		return
	}
	digest := canonicalAuthFileDigest(data)
	if digest == "" {
		return
	}
	a.fileSnapshotPath = strings.TrimSpace(path)
	a.fileSnapshotDigest = digest
}

const invalidatedFileSnapshotDigest = "invalidated"

// ClearFileSnapshot removes any unversioned file-observation binding once the
// Auth is committed to runtime state or normalized for comparison.
func (a *Auth) ClearFileSnapshot() {
	if a == nil {
		return
	}
	a.fileSnapshotPath = ""
	a.fileSnapshotDigest = ""
}

// InvalidateFileSnapshot marks an unversioned file snapshot as discarded so a
// subsequent re-read of identical or reformatted file content is not suppressed.
func (a *Auth) InvalidateFileSnapshot() {
	if !a.HasFileSnapshot() {
		return
	}
	a.fileSnapshotDigest = invalidatedFileSnapshotDigest
}

// HasFileSnapshot reports whether this Auth is an unversioned file-synthesized
// snapshot bound to a specific file observation digest.
func (a *Auth) HasFileSnapshot() bool {
	return a != nil && a.Generation == 0 && a.RegistrationEpoch == 0 && a.fileSnapshotDigest != ""
}

// FileSnapshotPath returns the backing file path bound to this file snapshot.
func (a *Auth) FileSnapshotPath() string {
	if a == nil {
		return ""
	}
	path := strings.TrimSpace(a.fileSnapshotPath)
	if path == "" && a.Attributes != nil {
		path = strings.TrimSpace(a.Attributes[AttributePath])
		if path == "" {
			path = strings.TrimSpace(a.Attributes[AttributeSource])
		}
	}
	return path
}

// SameFileSnapshot reports whether two unversioned file snapshots observed the
// same canonical file content.
func (a *Auth) SameFileSnapshot(other *Auth) bool {
	if !a.HasFileSnapshot() || !other.HasFileSnapshot() {
		return false
	}
	if a.fileSnapshotDigest == invalidatedFileSnapshotDigest || other.fileSnapshotDigest == invalidatedFileSnapshotDigest {
		return false
	}
	return a.fileSnapshotDigest == other.fileSnapshotDigest && a.fileSnapshotPath == other.fileSnapshotPath
}

// FileSnapshotMatchesDisk reports whether the backing file on disk still
// exists, is readable, and matches the canonical digest observed when this
// snapshot was synthesized.
func (a *Auth) FileSnapshotMatchesDisk() bool {
	if !a.HasFileSnapshot() {
		return true
	}
	path := a.FileSnapshotPath()
	if path == "" {
		return false
	}
	data, errRead := readAuthSnapshotFileFunc(path)
	if errRead != nil || len(data) == 0 {
		return false
	}
	return canonicalAuthFileDigest(data) == a.fileSnapshotDigest
}

const (
	AttributeAuthIndexSeed   = "auth_index_seed"
	AttributePluginVirtual   = "plugin_virtual"
	AttributeVirtualSource   = "virtual_source"
	pluginVirtualAttrEnabled = "true"
)

// MarkPluginVirtualAuth marks an auth that was expanded from a plugin-owned source file.
func MarkPluginVirtualAuth(auth *Auth, sourcePath string, ordinal int) {
	if auth == nil {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes[AttributePluginVirtual] = pluginVirtualAttrEnabled
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath != "" {
		auth.Attributes[AttributeVirtualSource] = sourcePath
	}
	seedID := strings.TrimSpace(auth.ID)
	if seedID == "" {
		seedID = strings.TrimSpace(auth.FileName)
	}
	if seedID == "" {
		seedID = strconv.Itoa(ordinal)
	}
	auth.Attributes[AttributeAuthIndexSeed] = strings.Join([]string{
		strings.ToLower(strings.TrimSpace(auth.Provider)),
		sourcePath,
		seedID,
		strconv.Itoa(ordinal),
	}, "|")
}

// IsPluginVirtualAuth reports whether an auth was expanded from a plugin-owned source file.
func IsPluginVirtualAuth(auth *Auth) bool {
	if auth == nil || len(auth.Attributes) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes[AttributePluginVirtual]), pluginVirtualAttrEnabled)
}

const (
	recentRequestBucketSeconds int64 = 10 * 60
	recentRequestBucketCount         = 20
)

type recentRequestBucket struct {
	bucketID int64
	success  int64
	failed   int64
}

type recentRequestRing struct {
	buckets [recentRequestBucketCount]recentRequestBucket
}

type RecentRequestBucket struct {
	Time    string `json:"time"`
	Success int64  `json:"success"`
	Failed  int64  `json:"failed"`
}

// QuotaState contains limiter tracking data for a credential.
type QuotaState struct {
	// Exceeded indicates the credential recently hit a quota error.
	Exceeded bool `json:"exceeded"`
	// Reason provides an optional provider specific human readable description.
	Reason string `json:"reason,omitempty"`
	// NextRecoverAt is when the credential may become available again.
	NextRecoverAt time.Time `json:"next_recover_at"`
	// BackoffLevel stores the progressive cooldown exponent used for rate limits.
	BackoffLevel int `json:"backoff_level,omitempty"`
	// ObservedAt is the time the current Signals snapshot was observed.
	ObservedAt time.Time `json:"observed_at,omitempty"`
	// Signals stores bounded, provider-specific quota watermark values observed
	// from upstream response headers or websocket quota events. It is a snapshot
	// of one upstream response, not an accumulation across responses, so an
	// expired watermark cannot linger after the response that produced it.
	// Cooldown transitions must use applyCooldownFields so they cannot replace
	// this snapshot.
	Signals map[string]string `json:"signals,omitempty"`
}

// Clone returns an independent copy of the quota state.
func (q QuotaState) Clone() QuotaState {
	copyQuota := q
	if len(q.Signals) > 0 {
		copyQuota.Signals = make(map[string]string, len(q.Signals))
		for key, value := range q.Signals {
			copyQuota.Signals[key] = value
		}
	}
	return copyQuota
}

// ModelState captures the execution state for a specific model under an auth entry.
type ModelState struct {
	// Status reflects the lifecycle status for this model.
	Status Status `json:"status"`
	// StatusMessage provides an optional short description of the status.
	StatusMessage string `json:"status_message,omitempty"`
	// Unavailable mirrors whether the model is temporarily blocked for retries.
	Unavailable bool `json:"unavailable"`
	// NextRetryAfter defines the per-model retry time.
	NextRetryAfter time.Time `json:"next_retry_after"`
	// LastError records the latest error observed for this model.
	LastError *Error `json:"last_error,omitempty"`
	// Quota retains quota information if this model hit rate limits.
	Quota QuotaState `json:"quota"`
	// UpdatedAt tracks the last update timestamp for this model state.
	UpdatedAt time.Time `json:"updated_at"`
	// cooldownRecordedSeq is the execution sequence watermark captured when
	// the current model cooldown was recorded. Successes from executions
	// stamped at or below it were dispatched before the cooldown existed and
	// must not clear it. Runtime only: zero after restart so restored
	// cooldowns can still be cleared by any success.
	cooldownRecordedSeq uint64 `json:"-"`
	// credentialQuotaShadow holds this model's independent (own-failure)
	// cooldown layer while the outer fields present the auth-level
	// credential quota propagated over it, so quota restoration can drop
	// only the propagated layer and reinstate precisely what the model's
	// own failures justify (issue #6690). It is nil when no independent
	// live block exists beneath an outer credential_quota label. Persisted
	// via CooldownStateRecord.IndependentState (unexported fields are
	// invisible to JSON, so the record owns the serialized form).
	credentialQuotaShadow *ModelState `toml:"credential_quota_shadow,omitempty"`
	// credentialQuotaPropagated marks that credential-quota propagation has
	// already wrapped this state, distinguishing "no independent block"
	// from "not yet propagated" so repeated propagation never mistakes its
	// own earlier layer for an independent block. Persisted via
	// CooldownStateRecord.CredentialQuotaPropagated across restarts,
	// including for shadow-less reporting models.
	credentialQuotaPropagated bool `toml:"credential_quota_propagated,omitempty"`
}

func recentRequestBucketID(now time.Time) int64 {
	if now.IsZero() {
		return 0
	}
	return now.Unix() / recentRequestBucketSeconds
}

func recentRequestBucketIndex(bucketID int64) int {
	mod := bucketID % int64(recentRequestBucketCount)
	if mod < 0 {
		mod += int64(recentRequestBucketCount)
	}
	return int(mod)
}

func formatRecentRequestBucketLabel(bucketID int64) string {
	start := time.Unix(bucketID*recentRequestBucketSeconds, 0).In(time.Local)
	end := start.Add(time.Duration(recentRequestBucketSeconds) * time.Second)
	return start.Format("15:04") + "-" + end.Format("15:04")
}

func (a *Auth) recordRecentRequest(now time.Time, success bool) {
	if a == nil {
		return
	}
	bucketID := recentRequestBucketID(now)
	idx := recentRequestBucketIndex(bucketID)
	bucket := &a.recentRequests.buckets[idx]
	if bucket.bucketID != bucketID {
		bucket.bucketID = bucketID
		bucket.success = 0
		bucket.failed = 0
	}
	if success {
		bucket.success++
		return
	}
	bucket.failed++
}

func (a *Auth) RecentRequestsSnapshot(now time.Time) []RecentRequestBucket {
	out := make([]RecentRequestBucket, 0, recentRequestBucketCount)
	if a == nil {
		return out
	}

	currentBucketID := recentRequestBucketID(now)
	for i := recentRequestBucketCount - 1; i >= 0; i-- {
		bucketID := currentBucketID - int64(i)
		idx := recentRequestBucketIndex(bucketID)
		bucket := a.recentRequests.buckets[idx]
		entry := RecentRequestBucket{
			Time: formatRecentRequestBucketLabel(bucketID),
		}
		if bucket.bucketID == bucketID {
			entry.Success = bucket.success
			entry.Failed = bucket.failed
		}
		out = append(out, entry)
	}

	return out
}

// Clone shallow copies the Auth structure, duplicating maps to avoid accidental mutation.
func (a *Auth) Clone() *Auth {
	if a == nil {
		return nil
	}
	copyAuth := *a
	copyAuth.Quota = a.Quota.Clone()
	if a.Attributes != nil {
		copyAuth.Attributes = make(map[string]string, len(a.Attributes))
		for key, value := range a.Attributes {
			copyAuth.Attributes[key] = value
		}
	}
	if a.Metadata != nil {
		copyAuth.Metadata = cloneMetadataMap(a.Metadata)
	}
	if a.ModelStates != nil {
		copyAuth.ModelStates = make(map[string]*ModelState, len(a.ModelStates))
		for key, state := range a.ModelStates {
			copyAuth.ModelStates[key] = state.Clone()
		}
	}
	copyAuth.Runtime = a.Runtime
	return &copyAuth
}

func cloneMetadataMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		switch typed := value.(type) {
		case map[string]any:
			dst[key] = cloneMetadataMap(typed)
		case []string:
			s := make([]string, len(typed))
			copy(s, typed)
			dst[key] = s
		case []any:
			s := make([]any, len(typed))
			copy(s, typed)
			dst[key] = s
		default:
			dst[key] = value
		}
	}
	return dst
}

func stableAuthIndex(seed string) string {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:8])
}

func (a *Auth) indexSeed() string {
	if a == nil {
		return ""
	}

	if a.Attributes != nil {
		if seed := strings.TrimSpace(a.Attributes[AttributeAuthIndexSeed]); seed != "" {
			return AttributeAuthIndexSeed + ":" + seed
		}
	}

	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	compatName := ""
	baseURL := ""
	apiKey := ""
	filePath := ""
	if a.Attributes != nil {
		compatName = strings.TrimSpace(a.Attributes["compat_name"])
		baseURL = strings.TrimSpace(a.Attributes["base_url"])
		apiKey = strings.TrimSpace(a.Attributes["api_key"])
		filePath = strings.TrimSpace(a.Attributes["path"])
		if filePath == "" {
			filePath = strings.TrimSpace(a.Attributes["source"])
		}
	}

	if filePath == "" {
		filePath = strings.TrimSpace(a.FileName)
	}
	if filePath == "" {
		filePath = strings.TrimSpace(a.ID)
	}

	if filePath != "" && strings.HasSuffix(strings.ToLower(filePath), ".json") {
		abs, errAbs := filepath.Abs(filePath)
		if errAbs == nil && strings.TrimSpace(abs) != "" {
			filePath = abs
		}
		filePath = filepath.Clean(filePath)

		authType := ""
		if a.Metadata != nil {
			if rawType, ok := a.Metadata["type"].(string); ok {
				authType = strings.TrimSpace(rawType)
			}
		}
		if authType == "" {
			authType = strings.TrimSpace(provider)
		}
		authType = strings.ToLower(strings.TrimSpace(authType))
		if authType != "" {
			return authType + ":" + filePath
		}
	}

	apiPrefix := ""
	if apiKey != "" {
		switch {
		case compatName != "" || strings.EqualFold(provider, "openai-compatibility"):
			apiPrefix = "openai-compatibility"
		case strings.EqualFold(provider, "gemini"):
			apiPrefix = "gemini-api-key"
		case strings.EqualFold(provider, "gemini-interactions"):
			apiPrefix = "interactions-api-key"
		case strings.EqualFold(provider, "codex"):
			apiPrefix = "codex-api-key"
		case strings.EqualFold(provider, "xai"):
			apiPrefix = "xai-api-key"
		case strings.EqualFold(provider, "claude"):
			apiPrefix = "claude-api-key"
		case strings.EqualFold(provider, "meta"):
			apiPrefix = "meta-api-key"
		}
	}
	if apiPrefix != "" {
		return apiPrefix + ":" + strings.TrimSpace(baseURL) + "+" + strings.TrimSpace(apiKey)
	}

	if id := strings.TrimSpace(a.ID); id != "" {
		return "id:" + id
	}

	return ""
}

// EnsureIndex returns a stable index derived from the auth file name or credential identity.
func (a *Auth) EnsureIndex() string {
	if a == nil {
		return ""
	}
	if existingIndex := strings.TrimSpace(a.Index); existingIndex != "" {
		a.Index = existingIndex
		a.indexAssigned = true
		return existingIndex
	}

	seed := a.indexSeed()
	if seed == "" {
		return ""
	}

	idx := stableAuthIndex(seed)
	a.Index = idx
	a.indexAssigned = true
	return idx
}

// Clone duplicates a model state including nested error details.
func (m *ModelState) Clone() *ModelState {
	if m == nil {
		return nil
	}
	copyState := *m
	copyState.Quota = m.Quota.Clone()
	if m.LastError != nil {
		copyState.LastError = &Error{
			Code:       m.LastError.Code,
			Message:    m.LastError.Message,
			Retryable:  m.LastError.Retryable,
			HTTPStatus: m.LastError.HTTPStatus,
		}
	}
	if m.credentialQuotaShadow != nil {
		copyState.credentialQuotaShadow = m.credentialQuotaShadow.cloneShallow()
	}
	return &copyState
}

// cloneShallow copies a shadow state one level deep; shadows never nest.
func (m *ModelState) cloneShallow() *ModelState {
	if m == nil {
		return nil
	}
	copyState := *m
	copyState.Quota = m.Quota.Clone()
	copyState.credentialQuotaShadow = nil
	if m.LastError != nil {
		copyState.LastError = &Error{
			Code:       m.LastError.Code,
			Message:    m.LastError.Message,
			Retryable:  m.LastError.Retryable,
			HTTPStatus: m.LastError.HTTPStatus,
		}
	}
	return &copyState
}

func (a *Auth) ProxyInfo() string {
	if a == nil {
		return ""
	}
	proxyStr := strings.TrimSpace(a.ProxyURL)
	if proxyStr == "" {
		return ""
	}
	if idx := strings.Index(proxyStr, "://"); idx > 0 {
		return "via " + proxyStr[:idx] + " proxy"
	}
	return "via proxy"
}

// DisableCoolingOverride returns the auth-scoped disable_cooling override when present.
// The value is read from metadata key "disable_cooling" (or legacy "disable-cooling").
// The second return value distinguishes explicit false from an absent override.
func (a *Auth) DisableCoolingOverride() (bool, bool) {
	if a == nil || a.Metadata == nil {
		return false, false
	}
	if val, ok := a.Metadata["disable_cooling"]; ok {
		if parsed, okParse := parseBoolAny(val); okParse {
			return parsed, true
		}
	}
	if val, ok := a.Metadata["disable-cooling"]; ok {
		if parsed, okParse := parseBoolAny(val); okParse {
			return parsed, true
		}
	}
	return false, false
}

// ToolPrefixDisabled returns whether the proxy_ tool name prefix should be
// skipped for this auth. When true, tool names are sent to Anthropic unchanged.
// The value is read from metadata key "tool_prefix_disabled" (or "tool-prefix-disabled").
func (a *Auth) ToolPrefixDisabled() bool {
	if a == nil || a.Metadata == nil {
		return false
	}
	for _, key := range []string{"tool_prefix_disabled", "tool-prefix-disabled"} {
		if val, ok := a.Metadata[key]; ok {
			if parsed, okParse := parseBoolAny(val); okParse {
				return parsed
			}
		}
	}
	return false
}

// RequestRetryOverride returns the auth-scoped request_retry override when present.
// The value is read from metadata key "request_retry" (or legacy "request-retry").
// A negative value is treated as unset and falls back to the global request-retry.
func (a *Auth) RequestRetryOverride() (int, bool) {
	if a == nil || a.Metadata == nil {
		return 0, false
	}
	if val, ok := a.Metadata["request_retry"]; ok {
		if parsed, okParse := parseIntAny(val); okParse {
			if parsed < 0 {
				return 0, false
			}
			return parsed, true
		}
	}
	if val, ok := a.Metadata["request-retry"]; ok {
		if parsed, okParse := parseIntAny(val); okParse {
			if parsed < 0 {
				return 0, false
			}
			return parsed, true
		}
	}
	return 0, false
}

func parseBoolAny(val any) (bool, bool) {
	switch typed := val.(type) {
	case bool:
		return typed, true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return false, false
		}
		parsed, err := strconv.ParseBool(trimmed)
		if err != nil {
			return false, false
		}
		return parsed, true
	case float64:
		return typed != 0, true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return false, false
		}
		return parsed != 0, true
	default:
		return false, false
	}
}

func parseIntAny(val any) (int, bool) {
	switch typed := val.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.Atoi(trimmed)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func (a *Auth) AccountInfo() (string, string) {
	if a == nil {
		return "", ""
	}
	switch a.AuthKind() {
	case AuthKindOAuth:
		if a.Metadata != nil {
			if v, ok := a.Metadata["email"].(string); ok {
				email := strings.TrimSpace(v)
				if email != "" {
					return "oauth", email
				}
			}
			if sa, ok := a.Metadata["service_account"].(map[string]any); ok {
				if v, okEmail := sa["client_email"].(string); okEmail {
					email := strings.TrimSpace(v)
					if email != "" {
						return "oauth", email
					}
				}
			}
		}
		return "oauth", ""
	case AuthKindAPIKey:
		if apiKey := authAttribute(a, AttributeAPIKey); apiKey != "" {
			return "api_key", apiKey
		}
		return "api_key", ""
	default:
		return "", ""
	}
}

// IsCodexUseSIWC reports whether a Codex auth record is configured to use its nested "siwc" section.
func IsCodexUseSIWC(a *Auth) bool {
	if a == nil {
		return false
	}
	enabled := false
	if len(a.Attributes) > 0 {
		if raw := strings.TrimSpace(a.Attributes["use_siwc"]); raw != "" {
			if parsed, ok := parseBoolAny(raw); ok {
				enabled = parsed
			}
		}
	}
	if !enabled && a.Metadata != nil {
		if parsed, ok := parseBoolAny(a.Metadata["use_siwc"]); ok {
			enabled = parsed
		}
	}
	return enabled
}

// IsCodexClientUserAgent reports whether userAgent identifies a Codex client.
func IsCodexClientUserAgent(userAgent string) bool {
	ua := strings.ToLower(strings.TrimSpace(userAgent))
	if ua == "" {
		return false
	}
	return strings.Contains(ua, "codex-desktop") ||
		strings.Contains(ua, "codex desktop") ||
		strings.Contains(ua, "codex-rs") ||
		strings.Contains(ua, "codex_cli_rs") ||
		strings.Contains(ua, "codex-tui") ||
		strings.Contains(ua, "codex_exec")
}

func headerValueFromHTTPHeader(headers http.Header, name string) string {
	if headers == nil {
		return ""
	}
	if value := strings.TrimSpace(headers.Get(name)); value != "" {
		return value
	}
	for key, values := range headers {
		if !strings.EqualFold(key, name) {
			continue
		}
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}

// IsCodexClientRequest reports whether the request originates from a Codex client.
func IsCodexClientRequest(ctx context.Context, clientHeaders ...http.Header) bool {
	if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
			if IsCodexClientUserAgent(headerValueFromHTTPHeader(ginCtx.Request.Header, "User-Agent")) {
				return true
			}
		}
	}
	for _, headers := range clientHeaders {
		if IsCodexClientUserAgent(headerValueFromHTTPHeader(headers, "User-Agent")) {
			return true
		}
	}
	return false
}

// IsCodexUseSIWCForRequest reports whether a request should use the nested SIWC credential section.
// Requests from Codex clients ignore use_siwc and always use non-SIWC credentials.
func IsCodexUseSIWCForRequest(ctx context.Context, a *Auth, clientHeaders ...http.Header) bool {
	if IsCodexClientRequest(ctx, clientHeaders...) {
		return false
	}
	return IsCodexUseSIWC(a)
}

// OuterExpirationTime extracts the outer credential expiration timestamp, ignoring the nested "siwc" section.
func (a *Auth) OuterExpirationTime() (time.Time, bool) {
	if a == nil {
		return time.Time{}, false
	}
	tokenStr := authOuterAccessToken(a)
	if tokenStr != "" && a.RejectedAccessToken != "" && a.RejectedAccessToken == tokenStr {
		return time.Unix(0, 0), true
	}
	if tokenStr != "" {
		if jwtExp, ok := parseJWTExp(tokenStr); ok {
			return jwtExp, true
		}
	}
	if ts, ok := expirationFromMap(a.Metadata); ok {
		return ts, true
	}
	return time.Time{}, false
}

// ExpirationTime attempts to extract the active credential expiration timestamp from metadata.
// When use_siwc is enabled on a Codex credential, the active SIWC section expiration is returned.
func (a *Auth) ExpirationTime() (time.Time, bool) {
	if a == nil {
		return time.Time{}, false
	}
	if IsCodexUseSIWC(a) {
		siwcToken := authSIWCTokenField(a, "access_token")
		if a.isSIWCAccessTokenRejected(siwcToken) {
			return time.Unix(0, 0), true
		}
		return a.SIWCExpirationTime()
	}
	return a.OuterExpirationTime()
}

// CodexOuterRefreshLead is the conservative proactive refresh lead time before
// the nominal 10-day Codex OAuth JWT expiration, refreshing tokens well ahead
// of early upstream access-token invalidation.
const CodexOuterRefreshLead = 96 * time.Hour

// CodexSIWCRefreshLead is the minimum remaining validity required before refreshing
// a Sign in with ChatGPT (SIWC) token, matching pi's DEFAULT_OAUTH_MINIMUM_VALIDITY_MS (5 minutes).
const CodexSIWCRefreshLead = 5 * time.Minute

// SIWCStatus represents the read-only validity state of a Codex SIWC credential.
type SIWCStatus string

const (
	SIWCStatusOff     SIWCStatus = "off"
	SIWCStatusValid   SIWCStatus = "valid"
	SIWCStatusInvalid SIWCStatus = "invalid"
)

// SIWCRealExpirationTime extracts the real (unbuffered) expiration timestamp for the nested
// "siwc" access token. It reads "expires_at" (saved from OAuth expires_in) or the JWT "exp"
// claim of "access_token", without using the margin-deducted "expired" field.
func (a *Auth) SIWCRealExpirationTime() (time.Time, bool) {
	if a == nil || a.Metadata == nil {
		return time.Time{}, false
	}
	siwcMap, ok := a.Metadata["siwc"].(map[string]any)
	if !ok || len(siwcMap) == 0 {
		return time.Time{}, false
	}
	if ts, okExp := lookupMetadataTime(siwcMap, "expires_at", "expiresAt"); okExp && !ts.IsZero() {
		return ts, true
	}
	if expiresIn, okExpiresIn := parseRelativeExpirySeconds(siwcMap); okExpiresIn {
		if ts, okTs := lookupMetadataTime(siwcMap, "last_refresh", "lastRefresh", "timestamp", "issued_at", "issuedAt"); okTs && !ts.IsZero() {
			return ts.Add(time.Duration(expiresIn) * time.Second), true
		}
	}
	if rawToken, okTok := siwcMap["access_token"].(string); okTok && strings.TrimSpace(rawToken) != "" {
		if jwtExp, okJWT := parseJWTExp(rawToken); okJWT && !jwtExp.IsZero() {
			return jwtExp, true
		}
	}
	return time.Time{}, false
}

func (a *Auth) isSIWCAccessTokenRejected(siwcToken string) bool {
	if a == nil {
		return false
	}
	siwcToken = strings.TrimSpace(siwcToken)
	if siwcToken == "" {
		return false
	}
	return (a.RejectedSIWCAccessToken != "" && a.RejectedSIWCAccessToken == siwcToken) ||
		(a.RejectedAccessToken != "" && a.RejectedAccessToken == siwcToken)
}

// CodexSIWCStatus evaluates the local SIWC credential status ("off", "valid", or "invalid")
// at the given time without triggering any refresh or upstream request.
func (a *Auth) CodexSIWCStatus(now time.Time) SIWCStatus {
	if a == nil || !IsCodexUseSIWC(a) {
		return SIWCStatusOff
	}
	siwcToken := authSIWCTokenField(a, "access_token")
	if siwcToken == "" {
		return SIWCStatusInvalid
	}
	if now.IsZero() {
		now = time.Now()
	}
	realExp, okExp := a.SIWCRealExpirationTime()
	if !okExp || realExp.IsZero() || !realExp.After(now) {
		return SIWCStatusInvalid
	}
	if a.isSIWCAccessTokenRejected(siwcToken) {
		return SIWCStatusInvalid
	}
	return SIWCStatusValid
}

// SIWCExpirationTime extracts the expiration timestamp from the nested "siwc" metadata section.
func (a *Auth) SIWCExpirationTime() (time.Time, bool) {
	if a == nil || a.Metadata == nil {
		return time.Time{}, false
	}
	siwcMap, ok := a.Metadata["siwc"].(map[string]any)
	if !ok || len(siwcMap) == 0 {
		return time.Time{}, false
	}
	if ts, okExp := expirationFromMap(siwcMap); okExp && !ts.IsZero() {
		return ts, true
	}
	if rawToken, okTok := siwcMap["access_token"].(string); okTok && strings.TrimSpace(rawToken) != "" {
		if jwtExp, okJWT := parseJWTExp(rawToken); okJWT && !jwtExp.IsZero() {
			return jwtExp, true
		}
	}
	return time.Time{}, false
}

// CodexOuterNeedsRefresh evaluates the outer credential refresh due time and whether it needs refresh at now.
func CodexOuterNeedsRefresh(a *Auth, now time.Time) (dueAt time.Time, hasRefresh bool, needsRefresh bool) {
	if a == nil {
		return time.Time{}, false, false
	}
	if authRefreshToken(a) == "" {
		return time.Time{}, false, false
	}
	hasRefresh = true

	outerToken := authOuterAccessToken(a)
	if outerToken == "" || (a.RejectedAccessToken != "" && a.RejectedAccessToken == outerToken) {
		return now, true, true
	}

	var lastRefresh time.Time
	if ts, ok := authLastRefreshTimestamp(a); ok && !ts.IsZero() {
		lastRefresh = ts
	} else {
		lastRefresh = a.LastRefreshedAt
	}
	expiry, hasExpiry := a.OuterExpirationTime()

	if pref := authPreferredInterval(a); pref > 0 {
		candidates := make([]time.Time, 0, 2)
		if hasExpiry && !expiry.IsZero() {
			candidates = append(candidates, expiry.Add(-pref))
		}
		if lastRefresh.IsZero() {
			return now, true, true
		}
		candidates = append(candidates, lastRefresh.Add(pref))
		next := candidates[0]
		for _, candidate := range candidates[1:] {
			if candidate.Before(next) {
				next = candidate
			}
		}
		if !next.After(now) {
			return now, true, true
		}
		return next, true, false
	}

	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	if provider == "" {
		provider = strings.ToLower(authMetadataString(a, "type"))
	}
	lead := ProviderRefreshLead(provider, a.Runtime)
	if lead == nil && provider == "codex" {
		d := CodexOuterRefreshLead
		lead = &d
	}
	if lead == nil {
		return time.Time{}, false, false
	}
	if *lead <= 0 {
		if hasExpiry && !expiry.IsZero() {
			if !expiry.After(now) {
				return now, true, true
			}
			return expiry, true, false
		}
		return time.Time{}, false, false
	}
	if hasExpiry && !expiry.IsZero() {
		due := expiry.Add(-*lead)
		if !due.After(now) {
			return now, true, true
		}
		return due, true, false
	}
	if !lastRefresh.IsZero() {
		due := lastRefresh.Add(*lead)
		if !due.After(now) {
			return now, true, true
		}
		return due, true, false
	}
	return now, true, true
}

// CodexSIWCNeedsRefresh evaluates the nested "siwc" credential section refresh due time and whether it needs refresh at now.
func CodexSIWCNeedsRefresh(a *Auth, now time.Time) (dueAt time.Time, hasRefresh bool, needsRefresh bool) {
	if a == nil || a.Metadata == nil {
		return time.Time{}, false, false
	}
	siwcMap, ok := a.Metadata["siwc"].(map[string]any)
	if !ok || len(siwcMap) == 0 {
		return time.Time{}, false, false
	}
	refreshToken, _ := siwcMap["refresh_token"].(string)
	clientID, _ := siwcMap["client_id"].(string)
	if strings.TrimSpace(refreshToken) == "" || strings.TrimSpace(clientID) == "" {
		return time.Time{}, false, false
	}
	hasRefresh = true

	siwcToken, _ := siwcMap["access_token"].(string)
	siwcToken = strings.TrimSpace(siwcToken)
	if siwcToken == "" {
		return now, true, true
	}
	if a.isSIWCAccessTokenRejected(siwcToken) {
		return now, true, true
	}
	expiry, hasExpiry := a.SIWCExpirationTime()
	if hasExpiry && !expiry.IsZero() {
		due := expiry.Add(-CodexSIWCRefreshLead)
		if !due.After(now) {
			return now, true, true
		}
		return due, true, false
	}
	if lastRefresh, okLast := lookupMetadataTime(siwcMap, "last_refresh", "lastRefresh"); okLast && !lastRefresh.IsZero() {
		due := lastRefresh.Add(CodexSIWCRefreshLead)
		if !due.After(now) {
			return now, true, true
		}
		return due, true, false
	}
	return now, true, true
}

// AccessTokenExpirationTime returns the expiration time of the specific access_token.
// If the access_token is a JWT, its exp claim takes strict precedence.
func (a *Auth) AccessTokenExpirationTime() (time.Time, bool) {
	if a == nil {
		return time.Time{}, false
	}
	tokenStr := authAccessToken(a)
	if tokenStr == "" {
		return time.Time{}, false
	}
	if (a.RejectedAccessToken != "" && a.RejectedAccessToken == tokenStr) || (IsCodexUseSIWC(a) && a.isSIWCAccessTokenRejected(tokenStr)) {
		return time.Unix(0, 0), true
	}
	if jwtExp, ok := parseJWTExp(tokenStr); ok {
		return jwtExp, true
	}
	if IsCodexUseSIWC(a) {
		if realExp, ok := a.SIWCRealExpirationTime(); ok && !realExp.IsZero() {
			return realExp, true
		}
	}
	return a.ExpirationTime()
}

// HasValidAccessToken returns whether the auth has a non-empty access token that is unexpired at the given time.
func (a *Auth) HasValidAccessToken(now time.Time) bool {
	if a == nil {
		return false
	}
	tokenStr := authAccessToken(a)
	if tokenStr == "" {
		return false
	}
	if exp, ok := a.AccessTokenExpirationTime(); ok {
		return exp.After(now)
	}
	return true
}

var (
	refreshLeadMu        sync.RWMutex
	refreshLeadFactories = make(map[string]func() *time.Duration)
)

func RegisterRefreshLeadProvider(provider string, factory func() *time.Duration) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" || factory == nil {
		return
	}
	refreshLeadMu.Lock()
	refreshLeadFactories[provider] = factory
	refreshLeadMu.Unlock()
}

var expireKeys = [...]string{"expired", "expire", "expires_at", "expiresAt", "expiry", "expires"}

func expirationFromMap(meta map[string]any) (time.Time, bool) {
	if meta == nil {
		return time.Time{}, false
	}
	for _, key := range expireKeys {
		if v, ok := meta[key]; ok {
			if ts, ok1 := parseTimeValue(v); ok1 {
				return ts, true
			}
		}
	}
	if expiresIn, okExpiresIn := parseRelativeExpirySeconds(meta); okExpiresIn {
		if timestamp, okTimestamp := parseRelativeExpiryTimestamp(meta); okTimestamp {
			return timestamp.Add(time.Duration(expiresIn) * time.Second), true
		}
	}
	for _, nestedKey := range []string{"token", "Token"} {
		if nested, ok := meta[nestedKey]; ok {
			switch val := nested.(type) {
			case map[string]any:
				if ts, ok1 := expirationFromMap(val); ok1 {
					return ts, true
				}
			case map[string]string:
				temp := make(map[string]any, len(val))
				for k, v := range val {
					temp[k] = v
				}
				if ts, ok1 := expirationFromMap(temp); ok1 {
					return ts, true
				}
			}
		}
	}
	return time.Time{}, false
}

// parseJWTExp extracts the "exp" claim timestamp from a JWT token string without signature verification.
func parseJWTExp(token string) (time.Time, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return time.Time{}, false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payloadSegment := parts[1]
	var (
		payloadBytes []byte
		errDecode    error
	)
	switch len(payloadSegment) % 4 {
	case 2:
		payloadBytes, errDecode = base64.URLEncoding.DecodeString(payloadSegment + "==")
	case 3:
		payloadBytes, errDecode = base64.URLEncoding.DecodeString(payloadSegment + "=")
	default:
		payloadBytes, errDecode = base64.URLEncoding.DecodeString(payloadSegment)
	}
	if errDecode != nil {
		payloadBytes, errDecode = base64.RawURLEncoding.DecodeString(payloadSegment)
		if errDecode != nil {
			return time.Time{}, false
		}
	}
	var claims struct {
		Exp any `json:"exp"`
	}
	if errJSON := json.Unmarshal(payloadBytes, &claims); errJSON != nil {
		return time.Time{}, false
	}
	if claims.Exp == nil {
		return time.Time{}, false
	}
	switch expVal := claims.Exp.(type) {
	case float64:
		if expVal > 0 {
			return normaliseUnix(int64(expVal)), true
		}
	case int64:
		if expVal > 0 {
			return normaliseUnix(expVal), true
		}
	case int:
		if expVal > 0 {
			return normaliseUnix(int64(expVal)), true
		}
	case string:
		if sec, errParse := strconv.ParseInt(strings.TrimSpace(expVal), 10, 64); errParse == nil && sec > 0 {
			return normaliseUnix(sec), true
		}
	}
	return time.Time{}, false
}

func parseRelativeExpirySeconds(meta map[string]any) (int, bool) {
	for _, key := range []string{"expires_in", "expiresIn"} {
		if value, ok := meta[key]; ok {
			if seconds, okSeconds := parseIntAny(value); okSeconds && seconds > 0 {
				return seconds, true
			}
		}
	}
	return 0, false
}

func parseRelativeExpiryTimestamp(meta map[string]any) (time.Time, bool) {
	for _, key := range []string{"timestamp", "issued_at", "issuedAt"} {
		if value, ok := meta[key]; ok {
			if timestamp, okTimestamp := parseTimeValue(value); okTimestamp && !timestamp.IsZero() {
				return timestamp, true
			}
		}
	}
	return time.Time{}, false
}

func ProviderRefreshLead(provider string, runtime any) *time.Duration {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if runtime != nil {
		if eval, ok := runtime.(interface{ RefreshLead() *time.Duration }); ok {
			if lead := eval.RefreshLead(); lead != nil && *lead > 0 {
				return lead
			}
		}
	}
	refreshLeadMu.RLock()
	factory := refreshLeadFactories[provider]
	refreshLeadMu.RUnlock()
	if factory != nil {
		if lead := factory(); lead != nil && *lead > 0 {
			return lead
		}
		return nil
	}
	if provider == "codex" {
		d := CodexOuterRefreshLead
		return &d
	}
	return nil
}

func parseTimeValue(v any) (time.Time, bool) {
	switch value := v.(type) {
	case string:
		s := strings.TrimSpace(value)
		if s == "" {
			return time.Time{}, false
		}
		layouts := []string{
			time.RFC3339,
			time.RFC3339Nano,
			"2006-01-02 15:04:05",
			"2006-01-02 15:04",
			"2006-01-02T15:04:05Z07:00",
		}
		for _, layout := range layouts {
			if ts, err := time.Parse(layout, s); err == nil {
				return ts, true
			}
		}
		if unix, err := strconv.ParseInt(s, 10, 64); err == nil {
			return normaliseUnix(unix), true
		}
	case float64:
		return normaliseUnix(int64(value)), true
	case int:
		return normaliseUnix(int64(value)), true
	case int32:
		return normaliseUnix(int64(value)), true
	case int64:
		return normaliseUnix(value), true
	case json.Number:
		if i, err := value.Int64(); err == nil {
			return normaliseUnix(i), true
		}
		if f, err := value.Float64(); err == nil {
			return normaliseUnix(int64(f)), true
		}
	}
	return time.Time{}, false
}

func normaliseUnix(raw int64) time.Time {
	if raw <= 0 {
		return time.Time{}
	}
	// Heuristic: treat values with millisecond precision (>1e12) accordingly.
	if raw > 1_000_000_000_000 {
		return time.UnixMilli(raw)
	}
	return time.Unix(raw, 0)
}
