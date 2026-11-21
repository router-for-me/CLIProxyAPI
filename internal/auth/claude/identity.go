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

// ReadMetadataStringMap reads a map[string]string metadata entry under the metadata
// lock, copying out string pairs from either map[string]string or map[string]any.
func ReadMetadataStringMap(metadata *map[string]any, key string) map[string]string {
	if metadata == nil {
		return nil
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		return nil
	}
	switch typed := (*metadata)[key].(type) {
	case map[string]string:
		if len(typed) == 0 {
			return nil
		}
		out := make(map[string]string, len(typed))
		for k, v := range typed {
			out[k] = v
		}
		return out
	case map[string]any:
		if len(typed) == 0 {
			return nil
		}
		out := make(map[string]string, len(typed))
		for k, rawVal := range typed {
			if strVal, ok := rawVal.(string); ok {
				out[k] = strVal
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

func copyStringSliceFromAny(raw any) []string {
	switch typed := raw.(type) {
	case []string:
		if len(typed) == 0 {
			return nil
		}
		out := make([]string, 0, len(typed))
		for _, s := range typed {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		if len(typed) == 0 {
			return nil
		}
		out := make([]string, 0, len(typed))
		for _, elem := range typed {
			if s, ok := elem.(string); ok {
				if trimmed := strings.TrimSpace(s); trimmed != "" {
					out = append(out, trimmed)
				}
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

func copyStringMapFromAny(raw any) map[string]string {
	switch typed := raw.(type) {
	case map[string]string:
		if len(typed) == 0 {
			return nil
		}
		out := make(map[string]string, len(typed))
		for k, v := range typed {
			out[k] = v
		}
		return out
	case map[string]any:
		if len(typed) == 0 {
			return nil
		}
		out := make(map[string]string, len(typed))
		for k, rawVal := range typed {
			if strVal, ok := rawVal.(string); ok {
				out[k] = strVal
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

func copyPatternPairsFromAny(raw any) [][2]string {
	switch typed := raw.(type) {
	case [][2]string:
		if len(typed) == 0 {
			return nil
		}
		out := make([][2]string, len(typed))
		copy(out, typed)
		return out
	case []map[string]string:
		if len(typed) == 0 {
			return nil
		}
		out := make([][2]string, 0, len(typed))
		for _, item := range typed {
			if p, ok := item["pattern"]; ok {
				out = append(out, [2]string{p, item["replacement"]})
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []map[string]any:
		if len(typed) == 0 {
			return nil
		}
		out := make([][2]string, 0, len(typed))
		for _, item := range typed {
			p, ok := item["pattern"].(string)
			if !ok {
				continue
			}
			r, _ := item["replacement"].(string)
			out = append(out, [2]string{p, r})
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		if len(typed) == 0 {
			return nil
		}
		out := make([][2]string, 0, len(typed))
		for _, rawItem := range typed {
			switch item := rawItem.(type) {
			case map[string]string:
				if p, ok := item["pattern"]; ok {
					out = append(out, [2]string{p, item["replacement"]})
				}
			case map[string]any:
				p, ok := item["pattern"].(string)
				if !ok {
					continue
				}
				r, _ := item["replacement"].(string)
				out = append(out, [2]string{p, r})
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

// ReadMetadataPatternPairs reads a slice of [pattern, replacement] pairs from metadata under the metadata lock.
func ReadMetadataPatternPairs(metadata *map[string]any, key string) [][2]string {
	if metadata == nil {
		return nil
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		return nil
	}
	return copyPatternPairsFromAny((*metadata)[key])
}

// ReadMetadataHomeCloakPatterns extracts credential_options.cloak.sensitive-patterns-replace
// from metadata under the metadata lock.
func ReadMetadataHomeCloakPatterns(metadata *map[string]any) [][2]string {
	if metadata == nil {
		return nil
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		return nil
	}
	opts, ok := (*metadata)["credential_options"].(map[string]any)
	if !ok || opts == nil {
		return nil
	}
	rawCloak, exists := opts["cloak"]
	if !exists || rawCloak == nil {
		return nil
	}
	cloakMap, ok := rawCloak.(map[string]any)
	if !ok {
		return nil
	}
	return copyPatternPairsFromAny(cloakMap["sensitive-patterns-replace"])
}

// ReadMetadataStringSlice reads a []string metadata entry under the metadata lock,
// copying string elements from either []string or []any.
func ReadMetadataStringSlice(metadata *map[string]any, key string) []string {
	if metadata == nil {
		return nil
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		return nil
	}
	return copyStringSliceFromAny((*metadata)[key])
}

// ReadMetadataHomeCloak extracts credential_options.cloak from metadata under
// the metadata lock without JSON serialization.
func ReadMetadataHomeCloak(metadata *map[string]any) (
	mode string,
	strictMode bool,
	sensitiveWords []string,
	sensitiveWordsReplace map[string]string,
	cacheUserID *bool,
	present bool,
) {
	if metadata == nil {
		return "", false, nil, nil, nil, false
	}
	claudeDevicePoolMu.Lock()
	defer claudeDevicePoolMu.Unlock()

	if *metadata == nil {
		return "", false, nil, nil, nil, false
	}
	opts, ok := (*metadata)["credential_options"].(map[string]any)
	if !ok || opts == nil {
		return "", false, nil, nil, nil, false
	}
	rawCloak, exists := opts["cloak"]
	if !exists || rawCloak == nil {
		return "", false, nil, nil, nil, false
	}
	cloakMap, ok := rawCloak.(map[string]any)
	if !ok {
		return "", false, nil, nil, nil, false
	}
	mode, _ = cloakMap["mode"].(string)
	strictMode, _ = cloakMap["strict-mode"].(bool)
	sensitiveWords = copyStringSliceFromAny(cloakMap["sensitive-words"])
	sensitiveWordsReplace = copyStringMapFromAny(cloakMap["sensitive-words-replace"])
	if rawCache, hasCache := cloakMap["cache-user-id"]; hasCache {
		if b, okBool := rawCache.(bool); okBool {
			cacheCopy := b
			cacheUserID = &cacheCopy
		}
	}
	return mode, strictMode, sensitiveWords, sensitiveWordsReplace, cacheUserID, true
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
