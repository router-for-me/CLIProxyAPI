// Package codex provides authentication and token management functionality
// for OpenAI's Codex AI services. It handles OAuth2 token storage, serialization,
// and retrieval for maintaining authenticated sessions with the Codex API.
package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	log "github.com/sirupsen/logrus"
)

var codexFileLocks sync.Map

func lockForFilePath(authFilePath string) *sync.Mutex {
	cleaned := canonicalLockPath(authFilePath)
	if actual, ok := codexFileLocks.Load(cleaned); ok {
		return actual.(*sync.Mutex)
	}
	mu := &sync.Mutex{}
	actual, _ := codexFileLocks.LoadOrStore(cleaned, mu)
	return actual.(*sync.Mutex)
}

func canonicalLockPath(authFilePath string) string {
	cleaned := filepath.Clean(strings.TrimSpace(authFilePath))
	if abs, errAbs := filepath.Abs(cleaned); errAbs == nil && strings.TrimSpace(abs) != "" {
		cleaned = filepath.Clean(abs)
	}
	return cleaned
}

// WithFileLock executes fn while holding both an in-process mutex and an inter-process lockfile for authFilePath.
func WithFileLock(authFilePath string, fn func() error) error {
	mu := lockForFilePath(authFilePath)
	mu.Lock()
	defer mu.Unlock()

	if errDir := os.MkdirAll(filepath.Dir(authFilePath), 0o700); errDir != nil {
		return fmt.Errorf("failed to create credential directory: %w", errDir)
	}
	releaseLockFile, errLock := acquireInterProcessLockFile(authFilePath)
	if errLock != nil {
		return errLock
	}
	defer releaseLockFile()

	return fn()
}

// WriteJSONFileAtomic writes data as JSON to authFilePath atomically via a temporary file in the same directory.
func WriteJSONFileAtomic(authFilePath string, data map[string]any) error {
	dir := filepath.Dir(authFilePath)
	tmpFile, errTmp := os.CreateTemp(dir, filepath.Base(authFilePath)+".tmp.*")
	if errTmp != nil {
		return fmt.Errorf("failed to create temp credential file: %w", errTmp)
	}
	tmpPath := tmpFile.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	if errChmod := tmpFile.Chmod(0o600); errChmod != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to chmod temp credential file: %w", errChmod)
	}
	raw, errMarshal := json.Marshal(data)
	if errMarshal != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to marshal token data: %w", errMarshal)
	}
	if _, errWrite := tmpFile.Write(raw); errWrite != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to write token to temp file: %w", errWrite)
	}
	if errClose := tmpFile.Close(); errClose != nil {
		log.Errorf("codex token storage: close temp token file error: %v", errClose)
		return fmt.Errorf("failed to close temp credential file: %w", errClose)
	}
	if errRename := os.Rename(tmpPath, authFilePath); errRename != nil {
		return fmt.Errorf("failed to replace credential file: %w", errRename)
	}
	cleanupTmp = false
	return nil
}

var codexOuterCredentialKeys = []string{
	"id_token",
	"access_token",
	"refresh_token",
	"account_id",
	"last_refresh",
	"email",
	"expired",
	"plan_type",
}

func cloneStringAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		switch typed := v.(type) {
		case map[string]any:
			dst[k] = cloneStringAnyMap(typed)
		case []string:
			s := make([]string, len(typed))
			copy(s, typed)
			dst[k] = s
		case []any:
			s := make([]any, len(typed))
			copy(s, typed)
			dst[k] = s
		default:
			dst[k] = v
		}
	}
	return dst
}

// MergeWithDiskMap preserves disk credentials when saving a non-refresh snapshot.
// Explicit login replacements and refresh patches are handled separately.
func MergeWithDiskMap(diskMap, incomingMap map[string]any) map[string]any {
	merged := cloneStringAnyMap(diskMap)
	if merged == nil {
		merged = make(map[string]any)
	}
	for key, value := range incomingMap {
		if value == nil {
			continue
		}
		if key == "siwc" || key == "use_siwc" {
			if _, exists := diskMap[key]; exists {
				continue
			}
		}
		if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
			if _, exists := diskMap[key]; exists {
				continue
			}
		}
		merged[key] = value
	}
	for _, key := range codexOuterCredentialKeys {
		if value, exists := diskMap[key]; exists {
			merged[key] = value
		} else if len(diskMap) > 0 {
			delete(merged, key)
		}
	}
	if _, exists := diskMap["siwc"]; !exists && len(diskMap) > 0 {
		delete(merged, "siwc")
	}
	return merged
}

func hasDiskOAuthOuter(diskMap map[string]any) bool {
	if len(diskMap) == 0 {
		return false
	}
	if rt, ok := diskMap["refresh_token"].(string); ok && strings.TrimSpace(rt) != "" {
		return true
	}
	if lr, ok := diskMap["last_refresh"].(string); ok && strings.TrimSpace(lr) != "" {
		return true
	}
	return false
}

// MergeCodexCredentialsFromDisk protects disk credentials during configuration
// saves while allowing intentional changes or deletions of non-credential settings.
func MergeCodexCredentialsFromDisk(diskMap, incomingMetadata map[string]any) map[string]any {
	merged := cloneStringAnyMap(incomingMetadata)
	if merged == nil {
		merged = make(map[string]any)
	}
	if hasDiskOAuthOuter(diskMap) {
		for _, key := range codexOuterCredentialKeys {
			if value, exists := diskMap[key]; exists {
				merged[key] = value
			} else {
				delete(merged, key)
			}
		}
	} else {
		for _, key := range codexOuterCredentialKeys {
			if value, exists := diskMap[key]; exists {
				if inVal, hadIn := merged[key]; !hadIn || inVal == nil || (func() bool {
					s, ok := inVal.(string)
					return ok && strings.TrimSpace(s) == ""
				}()) {
					merged[key] = value
				}
			}
		}
	}
	if value, exists := diskMap["siwc"]; exists {
		if siwcMap, ok := value.(map[string]any); ok {
			merged["siwc"] = cloneStringAnyMap(siwcMap)
		} else {
			merged["siwc"] = value
		}
	}
	if value, exists := diskMap["use_siwc"]; exists {
		if _, specified := incomingMetadata["use_siwc"]; !specified {
			merged["use_siwc"] = value
		}
	}
	return merged
}

// CodexTokenStorage stores OAuth2 token information for OpenAI Codex API authentication.
// It maintains compatibility with the existing auth system while adding Codex-specific fields
// for managing access tokens, refresh tokens, and user account information.
type CodexTokenStorage struct {
	// IDToken is the JWT ID token containing user claims and identity information.
	IDToken string `json:"id_token"`
	// AccessToken is the OAuth2 access token used for authenticating API requests.
	AccessToken string `json:"access_token"`
	// RefreshToken is used to obtain new access tokens when the current one expires.
	RefreshToken string `json:"refresh_token"`
	// AccountID is the OpenAI account identifier associated with this token.
	AccountID string `json:"account_id"`
	// LastRefresh is the timestamp of the last token refresh operation.
	LastRefresh string `json:"last_refresh"`
	// Email is the OpenAI account email address associated with this token.
	Email string `json:"email"`
	// Type indicates the authentication provider type, always "codex" for this storage.
	Type string `json:"type"`
	// Expire is the timestamp when the current access token expires.
	Expire string `json:"expired"`
	// PlanType indicates the ChatGPT subscription plan type, defaults to "free" if not present.
	PlanType string `json:"plan_type,omitempty"`
	// UseSIWC controls whether API requests use the Sign in with ChatGPT (SIWC) credential section.
	UseSIWC *bool `json:"use_siwc,omitempty"`
	// SIWC holds the Sign in with ChatGPT OAuth credential fields.
	SIWC map[string]any `json:"siwc,omitempty"`

	// ReplaceCredentials permits an explicit login to replace existing outer tokens.
	ReplaceCredentials bool `json:"-"`
	// RefreshPatch updates only refreshed partitions during a direct storage save.
	RefreshPatch *CredentialRefreshPatch `json:"-"`

	// Metadata holds arbitrary key-value pairs injected via hooks.
	// It is not exported to JSON directly to allow flattening during serialization.
	Metadata map[string]any `json:"-"`
}

// SetMetadata allows external callers to inject metadata into the storage before saving.
func (ts *CodexTokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// SaveTokenToFile serializes the Codex token storage to a JSON file under a per-file and inter-process lock,
// reading and merging any existing JSON on disk before atomically replacing the file.
func (ts *CodexTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "codex"
	_ = strconv.IntSize

	return WithFileLock(authFilePath, func() error {
		data, errMerge := misc.MergeMetadata(ts, ts.Metadata)
		if errMerge != nil {
			return fmt.Errorf("failed to merge metadata: %w", errMerge)
		}

		existingBytes, errRead := os.ReadFile(authFilePath)
		if errRead != nil && !os.IsNotExist(errRead) {
			return fmt.Errorf("failed to read existing credentials: %w", errRead)
		}
		if errRead == nil {
			var diskMap map[string]any
			if errUnmarshal := json.Unmarshal(existingBytes, &diskMap); errUnmarshal != nil || diskMap == nil {
				return fmt.Errorf("invalid existing credential JSON")
			}
			if ts.RefreshPatch != nil {
				return WriteJSONFileAtomic(authFilePath, ts.RefreshPatch.Apply(diskMap))
			}
			merged := MergeWithDiskMap(diskMap, data)
			if ts.ReplaceCredentials {
				for _, key := range codexOuterCredentialKeys {
					if value, exists := data[key]; exists {
						merged[key] = value
					}
				}
			}
			data = merged
		}

		return WriteJSONFileAtomic(authFilePath, data)
	})
}
