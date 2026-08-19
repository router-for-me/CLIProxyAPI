// Package qoder provides authentication and token handling for Qoder API.
package qoder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

// QoderTokenStorage stores OAuth2 token information for Qoder API authentication.
// It maintains compatibility with the existing auth system while adding Qoder-specific fields.
type QoderTokenStorage struct {
	// Token is the OAuth2 access token used for authenticating API requests.
	Token string `json:"token"`
	// RefreshToken is used to obtain new access tokens when the current one expires.
	RefreshToken string `json:"refresh_token"`
	// UserID is the unique identifier for the Qoder user.
	UserID string `json:"user_id"`
	// Name is the user's display name.
	Name string `json:"name"`
	// Email is the Qoder account email address associated with this token.
	Email string `json:"email"`
	// ExpireTime is the timestamp when the current access token expires (milliseconds epoch).
	ExpireTime int64 `json:"expire_time"`
	// Type indicates the authentication provider type, always "qoder" for this storage.
	Type string `json:"type"`
	// LastRefresh is the timestamp of the last token refresh operation.
	LastRefresh string `json:"last_refresh"`
	// MachineID is the persistent machine identifier for this installation.
	MachineID string `json:"machine_id,omitempty"`
	// MachineToken is the machine-specific token (if returned by auth server).
	MachineToken string `json:"machine_token,omitempty"`
	// MachineType is the type of machine registration.
	MachineType string `json:"machine_type,omitempty"`
	// ModelConfigs caches the raw upstream model_config entries from the most
	// recent /algo/api/v2/model/list response, keyed by model id (e.g.
	// "dfmodel" -> {"key":"dfmodel","format":"openai","is_vl":true, ...}).
	// Persisted to disk so per-model fields survive restarts; mutated through
	// SetModelConfigs / GetModelConfig only so concurrent FetchQoderModels +
	// chat traffic never race on the underlying map.
	ModelConfigs map[string]json.RawMessage `json:"model_configs,omitempty"`

	// UsageInfo caches the most recent /api/v2/quota/usage response.
	// Populated by FetchQoderUsage; not persisted to disk (in-memory only).
	// Access must go through SetUsageInfo / GetUsageInfo.
	UsageInfo *QoderUsageInfo `json:"-"`

	// usageMu guards UsageInfo against concurrent FetchQoderUsage writes
	// vs management-listing reads (buildAuthFileEntry).
	usageMu sync.RWMutex `json:"-"`

	// Metadata holds arbitrary key-value pairs injected via hooks.
	// It is not exported to JSON directly to allow flattening during serialization.
	Metadata map[string]any `json:"-"`

	// modelConfigMu guards ModelConfigs against concurrent
	// FetchQoderModels writes vs ExecuteStream reads. The map fetched
	// from /algo/api/v2/model/list is replaced wholesale, but the lookup
	// path (GetModelConfig) still reads it during chat requests.
	modelConfigMu sync.RWMutex `json:"-"`
}

// QoderUsageInfo holds the parsed /api/v2/quota/usage response.
type QoderUsageInfo struct {
	// UserQuota is the personal credit quota.
	UserQuota QoderQuota `json:"userQuota"`
	// OrgResourcePackage is the org-level resource package.
	OrgResourcePackage QoderQuota `json:"orgResourcePackage"`
	// TotalUsagePercentage is the combined usage percentage (0–1).
	TotalUsagePercentage float64 `json:"totalUsagePercentage"`
	// IsQuotaExceeded indicates whether the quota is exhausted.
	IsQuotaExceeded bool `json:"isQuotaExceeded"`
	// ExpiresAt is the quota reset timestamp in milliseconds epoch.
	ExpiresAt int64 `json:"expiresAt"`
}

// QoderQuota holds a single quota bucket (user or org).
type QoderQuota struct {
	Total      float64 `json:"total"`
	Used       float64 `json:"used"`
	Remaining  float64 `json:"remaining"`
	Percentage float64 `json:"percentage"`
	Unit       string  `json:"unit"`
}

// SetMetadata allows external callers to inject metadata into the storage before saving.
func (ts *QoderTokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// SetModelConfigs replaces the cached per-model configs atomically.
// Callers (FetchQoderModels) hand in the freshly-fetched table; readers
// (ExecuteStream via GetModelConfig) see either the previous full set or
// the new full set, never a half-built map.
func (ts *QoderTokenStorage) SetModelConfigs(configs map[string]json.RawMessage) {
	if ts == nil {
		return
	}
	ts.modelConfigMu.Lock()
	ts.ModelConfigs = configs
	ts.modelConfigMu.Unlock()
}

// GetModelConfig returns the cached upstream model entry for the given key
// (or false if no fetch has populated the cache yet / the key is unknown).
// The returned RawMessage is safe to read; callers must not mutate it.
func (ts *QoderTokenStorage) GetModelConfig(key string) (json.RawMessage, bool) {
	if ts == nil {
		return nil, false
	}
	ts.modelConfigMu.RLock()
	defer ts.modelConfigMu.RUnlock()
	raw, ok := ts.ModelConfigs[key]
	return raw, ok
}

// SetUsageInfo replaces the cached quota-usage snapshot atomically.
// Callers (FetchQoderUsage background goroutine) hand in the freshly-fetched
// info; readers (buildAuthFileEntry on the management listing path) see
// either the previous full snapshot or the new one, never a torn pointer.
func (ts *QoderTokenStorage) SetUsageInfo(info *QoderUsageInfo) {
	if ts == nil {
		return
	}
	ts.usageMu.Lock()
	ts.UsageInfo = info
	ts.usageMu.Unlock()
}

// GetUsageInfo returns the cached quota-usage snapshot (or nil if none has
// been fetched yet). Safe for concurrent use with SetUsageInfo.
func (ts *QoderTokenStorage) GetUsageInfo() *QoderUsageInfo {
	if ts == nil {
		return nil
	}
	ts.usageMu.RLock()
	defer ts.usageMu.RUnlock()
	return ts.UsageInfo
}

// ModelConfigKeys returns the sorted list of cached model keys (used in
// error messages). Locks ModelConfigs while building the slice.
func (ts *QoderTokenStorage) ModelConfigKeys() []string {
	if ts == nil {
		return nil
	}
	ts.modelConfigMu.RLock()
	defer ts.modelConfigMu.RUnlock()
	if len(ts.ModelConfigs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(ts.ModelConfigs))
	for k := range ts.ModelConfigs {
		keys = append(keys, k)
	}
	return keys
}

// SaveTokenToFile serializes the Qoder token storage to a JSON file.
// This method creates the necessary directory structure and writes the token
// data in JSON format to the specified file path for persistent storage.
// It merges any injected metadata into the top-level JSON object.
//
// Parameters:
//   - authFilePath: The full path where the token file should be saved
//
// Returns:
//   - error: An error if the operation fails, nil otherwise
func (ts *QoderTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "qoder"

	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	data, errMerge := misc.MergeMetadata(ts, ts.Metadata)
	if errMerge != nil {
		return fmt.Errorf("failed to merge metadata: %w", errMerge)
	}

	// Write to a temp file and atomically rename onto the target path.
	// os.Create + Encode leaves a TOCTOU window where the file watcher
	// sees an empty (just-truncated) or partially-written file; a temp
	// write eliminates that window because the rename is atomic on the
	// same filesystem.
	tmp, err := os.CreateTemp(filepath.Dir(authFilePath), ".tmp-qoder-*")
	if err != nil {
		return fmt.Errorf("failed to create temp token file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if err = json.NewEncoder(tmp).Encode(data); err != nil {
		return fmt.Errorf("failed to write token to temp file: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	if err = os.Rename(tmpName, authFilePath); err != nil {
		return fmt.Errorf("failed to commit token file: %w", err)
	}
	cleanup = false // rename succeeded, don't remove
	return nil
}

// IsExpired checks if the token has expired or will expire within the given duration
func (ts *QoderTokenStorage) IsExpired(bufferDuration int64) bool {
	if ts.ExpireTime == 0 {
		return true
	}
	now := time.Now().UnixMilli()
	return ts.ExpireTime-now-bufferDuration <= 0
}
