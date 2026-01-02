package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	baseauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth"
)

// Auth encapsulates the runtime state and metadata associated with a single credential.
type Auth struct {
	// ID uniquely identifies the auth record across restarts.
	ID string `json:"id"`
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
	// NextRetryAfter is the earliest time a retry should retrigger.
	NextRetryAfter time.Time `json:"next_retry_after"`
	// ModelStates tracks per-model runtime availability data.
	ModelStates map[string]*ModelState `json:"model_states,omitempty"`

	// Runtime carries non-serialisable data used during execution (in-memory only).
	Runtime any `json:"-"`

	indexAssigned bool `json:"-"`
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
}

// Clone shallow copies the Auth structure, duplicating maps to avoid accidental mutation.
func (a *Auth) Clone() *Auth {
	if a == nil {
		return nil
	}
	copyAuth := *a
	if len(a.Attributes) > 0 {
		copyAuth.Attributes = make(map[string]string, len(a.Attributes))
		for key, value := range a.Attributes {
			copyAuth.Attributes[key] = value
		}
	}
	if len(a.Metadata) > 0 {
		copyAuth.Metadata = make(map[string]any, len(a.Metadata))
		for key, value := range a.Metadata {
			copyAuth.Metadata[key] = value
		}
	}
	if len(a.ModelStates) > 0 {
		copyAuth.ModelStates = make(map[string]*ModelState, len(a.ModelStates))
		for key, state := range a.ModelStates {
			copyAuth.ModelStates[key] = state.Clone()
		}
	}
	copyAuth.Runtime = a.Runtime
	return &copyAuth
}

func stableAuthIndex(seed string) string {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:8])
}

// EnsureIndex returns a stable index derived from the auth file name or API key.
func (a *Auth) EnsureIndex() string {
	if a == nil {
		return ""
	}
	if a.indexAssigned && a.Index != "" {
		return a.Index
	}

	seed := strings.TrimSpace(a.FileName)
	if seed != "" {
		seed = "file:" + seed
	} else if a.Attributes != nil {
		if apiKey := strings.TrimSpace(a.Attributes["api_key"]); apiKey != "" {
			seed = "api_key:" + apiKey
		}
	}
	if seed == "" {
		if id := strings.TrimSpace(a.ID); id != "" {
			seed = "id:" + id
		} else {
			return ""
		}
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

func (a *Auth) AccountInfo() (string, string) {
	if a == nil {
		return "", ""
	}
	// For Gemini CLI, include project ID in the OAuth account info if present.
	if strings.ToLower(a.Provider) == "gemini-cli" {
		if a.Metadata != nil {
			email, _ := a.Metadata["email"].(string)
			email = strings.TrimSpace(email)
			if email != "" {
				if p, ok := a.Metadata["project_id"].(string); ok {
					p = strings.TrimSpace(p)
					if p != "" {
						return "oauth", email + " (" + p + ")"
					}
				}
				return "oauth", email
			}
		}
	}

	// For iFlow provider, prioritize OAuth type if email is present
	if strings.ToLower(a.Provider) == "iflow" {
		if a.Metadata != nil {
			if email, ok := a.Metadata["email"].(string); ok {
				email = strings.TrimSpace(email)
				if email != "" {
					return "oauth", email
				}
			}
		}
	}

	// Check metadata for email first (OAuth-style auth)
	if a.Metadata != nil {
		if v, ok := a.Metadata["email"].(string); ok {
			email := strings.TrimSpace(v)
			if email != "" {
				return "oauth", email
			}
		}
	}
	// Fall back to API key (API-key auth)
	if a.Attributes != nil {
		if v := a.Attributes["api_key"]; v != "" {
			return "api_key", v
		}
	}
	return "", ""
}

// ExpirationTime attempts to extract the credential expiration timestamp from metadata.
// It inspects common keys such as "expired", "expire", "expires_at", and also
// nested "token" objects to remain compatible with legacy auth file formats.
func (a *Auth) ExpirationTime() (time.Time, bool) {
	if a == nil {
		return time.Time{}, false
	}
	if ts, ok := expirationFromMap(a.Metadata); ok {
		return ts, true
	}
	return time.Time{}, false
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
	if factory == nil {
		return nil
	}
	if lead := factory(); lead != nil && *lead > 0 {
		return lead
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
