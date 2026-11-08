package claude

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
)

const (
	ClaudeDeviceIDsMetadataKey = "claude_device_ids"
	ClaudeDevicePoolSize       = 1
	claudeDeviceIDByteSize     = 32
)

// claudeDevicePoolMu guards every concurrent access to a Claude credential's
// Auth.Metadata map, not just the device pool. A single Auth is shared by all
// in-flight requests using that credential, and Go maps are not safe for
// concurrent read/write, so the account-profile and refresh paths have to take
// the same lock as the pool paths. Reaching into Auth.Metadata directly from a
// request path is a data race even when the keys differ.
var claudeDevicePoolMu sync.Mutex

// GenerateDeviceIDPool creates the fixed-size device pool stored with a Claude credential.
func GenerateDeviceIDPool() ([]string, error) {
	deviceIDs := make([]string, 0, ClaudeDevicePoolSize)
	seen := make(map[string]struct{}, ClaudeDevicePoolSize)
	for len(deviceIDs) < ClaudeDevicePoolSize {
		deviceID, errDeviceID := generateDeviceID()
		if errDeviceID != nil {
			return nil, errDeviceID
		}
		if _, exists := seen[deviceID]; exists {
			continue
		}
		seen[deviceID] = struct{}{}
		deviceIDs = append(deviceIDs, deviceID)
	}
	return deviceIDs, nil
}

func generateDeviceID() (string, error) {
	data := make([]byte, claudeDeviceIDByteSize)
	if _, errRead := rand.Read(data); errRead != nil {
		return "", fmt.Errorf("generate Claude device ID: %w", errRead)
	}
	return hex.EncodeToString(data), nil
}

// NormalizeDeviceIDPool returns the first valid device ID in canonical form.
func NormalizeDeviceIDPool(raw any) []string {
	var values []string
	switch typed := raw.(type) {
	case []string:
		values = typed
	case []any:
		values = make([]string, 0, len(typed))
		for _, value := range typed {
			if text, ok := value.(string); ok {
				values = append(values, text)
			}
		}
	default:
		return nil
	}

	deviceIDs := make([]string, 0, min(len(values), ClaudeDevicePoolSize))
	seen := make(map[string]struct{}, ClaudeDevicePoolSize)
	for _, value := range values {
		deviceID := strings.ToLower(strings.TrimSpace(value))
		if !ValidDeviceID(deviceID) {
			continue
		}
		if _, exists := seen[deviceID]; exists {
			continue
		}
		seen[deviceID] = struct{}{}
		deviceIDs = append(deviceIDs, deviceID)
		if len(deviceIDs) == ClaudeDevicePoolSize {
			break
		}
	}
	return deviceIDs
}

// HasCanonicalDeviceIDPool reports whether raw stores exactly one valid device ID.
func HasCanonicalDeviceIDPool(raw any) bool {
	var values []string
	switch typed := raw.(type) {
	case []string:
		values = typed
	case []any:
		values = make([]string, 0, len(typed))
		for _, value := range typed {
			text, ok := value.(string)
			if !ok {
				return false
			}
			values = append(values, text)
		}
	default:
		return false
	}
	normalized := NormalizeDeviceIDPool(values)
	return len(values) == ClaudeDevicePoolSize && len(normalized) == ClaudeDevicePoolSize && values[0] == normalized[0]
}

// EnsureDeviceIDPool repairs or creates the single-device pool in credential metadata.
func EnsureDeviceIDPool(metadata map[string]any) ([]string, bool, error) {
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	return ensureDeviceIDPoolLocked(metadata)
}

// EnsureDeviceIDPoolFor lazily initializes the metadata map and then ensures the
// pool, both under the device pool lock.
//
// A single *Auth is shared by every concurrent request that selects the same
// credential, so initializing the map field outside this lock races with the
// writes below and can abort the process with "concurrent map writes". Callers
// holding a shared credential must reach the pool through this package rather
// than touching the map directly.
func EnsureDeviceIDPoolFor(metadata *map[string]any) ([]string, bool, error) {
	if metadata == nil {
		return nil, false, fmt.Errorf("ensure Claude device pool: metadata pointer is nil")
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		*metadata = make(map[string]any)
	}
	return ensureDeviceIDPoolLocked(*metadata)
}

// ReadDeviceIDPool returns the stored pool value, initializing the map when
// needed, under the device pool lock. Slice values are copied so a caller can
// never mutate the stored credential identity after the lock is released.
func ReadDeviceIDPool(metadata *map[string]any) any {
	if metadata == nil {
		return nil
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		*metadata = make(map[string]any)
		return nil
	}
	switch stored := (*metadata)[ClaudeDeviceIDsMetadataKey].(type) {
	case []string:
		return append([]string(nil), stored...)
	case []any:
		return append([]any(nil), stored...)
	default:
		return stored
	}
}

// StoreDeviceIDPool writes a defensive copy of deviceIDs under the device pool lock.
func StoreDeviceIDPool(metadata *map[string]any, deviceIDs []string) {
	if metadata == nil {
		return
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		*metadata = make(map[string]any)
	}
	(*metadata)[ClaudeDeviceIDsMetadataKey] = append([]string(nil), deviceIDs...)
}

// ReadMetadataString reads a string-valued metadata entry under the metadata
// lock, so it cannot observe a map being concurrently written by another path.
func ReadMetadataString(metadata *map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		return ""
	}
	value, _ := (*metadata)[key].(string)
	return value
}

// StoreMetadataString writes a string-valued metadata entry under the metadata
// lock, initializing the map when needed. Empty values are skipped so callers can
// forward optional fields without erasing a previously resolved value.
func StoreMetadataString(metadata *map[string]any, key, value string) {
	if metadata == nil || strings.TrimSpace(value) == "" {
		return
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		*metadata = make(map[string]any)
	}
	(*metadata)[key] = value
}

// StoreMetadataValue writes an arbitrary metadata entry under the metadata lock,
// initializing the map when needed.
func StoreMetadataValue(metadata *map[string]any, key string, value any) {
	if metadata == nil {
		return
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		*metadata = make(map[string]any)
	}
	(*metadata)[key] = value
}

// EnsureMetadataMap initializes the metadata map under the metadata lock.
func EnsureMetadataMap(metadata *map[string]any) {
	if metadata == nil {
		return
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		*metadata = make(map[string]any)
	}
}

// ensureDeviceIDPoolLocked requires claudeDevicePoolMu to be held.
func ensureDeviceIDPoolLocked(metadata map[string]any) ([]string, bool, error) {
	if metadata == nil {
		return nil, false, fmt.Errorf("ensure Claude device pool: metadata is nil")
	}
	rawDeviceIDs := metadata[ClaudeDeviceIDsMetadataKey]
	deviceIDs := NormalizeDeviceIDPool(rawDeviceIDs)
	changed := !HasCanonicalDeviceIDPool(rawDeviceIDs)
	seen := make(map[string]struct{}, ClaudeDevicePoolSize)
	for _, deviceID := range deviceIDs {
		seen[deviceID] = struct{}{}
	}
	for len(deviceIDs) < ClaudeDevicePoolSize {
		deviceID, errDeviceID := generateDeviceID()
		if errDeviceID != nil {
			return nil, false, errDeviceID
		}
		if _, exists := seen[deviceID]; exists {
			continue
		}
		seen[deviceID] = struct{}{}
		deviceIDs = append(deviceIDs, deviceID)
	}

	if changed {
		metadata[ClaudeDeviceIDsMetadataKey] = append([]string(nil), deviceIDs...)
	}
	return append([]string(nil), deviceIDs...), changed, nil
}

// SelectDeviceID returns the credential's sole device ID after validating the conversation session.
func SelectDeviceID(deviceIDs []string, sessionID string) (string, error) {
	deviceIDs = NormalizeDeviceIDPool(deviceIDs)
	if len(deviceIDs) != ClaudeDevicePoolSize {
		return "", fmt.Errorf("select Claude device ID: device pool has %d entries, want %d", len(deviceIDs), ClaudeDevicePoolSize)
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("select Claude device ID: session ID is empty")
	}
	return deviceIDs[0], nil
}

// ValidDeviceID reports whether a value matches Claude Code's lowercase 64-hex device format.
func ValidDeviceID(value string) bool {
	if len(value) != claudeDeviceIDByteSize*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, errDecode := hex.DecodeString(value)
	return errDecode == nil && len(decoded) == claudeDeviceIDByteSize
}
