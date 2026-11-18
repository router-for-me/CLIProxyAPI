package discovery

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// DefaultInstancePrefix is the prefix used for default instance names.
	DefaultInstancePrefix = "CPA-"
	instanceIDFilename    = "instance_id"
)

var (
	idMu      sync.Mutex
	cachedIDs = make(map[string]string)
)

func isValidHex4(s string) bool {
	if len(s) != 4 {
		return false
	}
	for i := 0; i < 4; i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'F') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// GetOrGenerateInstanceID retrieves the persistent instance ID from stateDir,
// or generates a new 4-character hex ID (e.g. "8F3B") and persists it atomically.
// Thread-safe, cached per stateDir, and protected against race conditions.
func GetOrGenerateInstanceID(stateDir string) string {
	cleanDir := ""
	if stateDir != "" {
		cleanDir = filepath.Clean(stateDir)
	}

	idMu.Lock()
	defer idMu.Unlock()

	// If already resolved and cached in-process for this directory, return immediately
	if id, ok := cachedIDs[cleanDir]; ok && isValidHex4(id) {
		return id
	}

	if cleanDir != "" {
		idPath := filepath.Join(cleanDir, instanceIDFilename)
		if data, err := os.ReadFile(idPath); err == nil {
			id := strings.TrimSpace(string(data))
			if isValidHex4(id) {
				cachedIDs[cleanDir] = strings.ToUpper(id)
				return cachedIDs[cleanDir]
			}
		}
	}

	// Generate random 2 bytes -> 4 hex chars with cryptographically secure PRNG
	buf := make([]byte, 2)
	if _, err := rand.Read(buf); err != nil {
		// Defensive fallback if entropy source is temporarily unavailable
		return fmt.Sprintf("%04X", os.Getpid()&0xFFFF)
	}
	id := strings.ToUpper(hex.EncodeToString(buf))

	// Persist atomically if stateDir is specified
	if cleanDir != "" {
		if errDir := os.MkdirAll(cleanDir, 0700); errDir == nil {
			idPath := filepath.Join(cleanDir, instanceIDFilename)
			if tmpFile, errTmp := os.CreateTemp(cleanDir, "instance_id_*.tmp"); errTmp == nil {
				tmpPath := tmpFile.Name()
				_ = tmpFile.Chmod(0600)
				_, _ = tmpFile.Write([]byte(id))
				_ = tmpFile.Sync()
				_ = tmpFile.Close()
				if errRename := os.Rename(tmpPath, idPath); errRename != nil {
					_ = os.Remove(tmpPath)
				}
			}
		}
	}

	cachedIDs[cleanDir] = id
	return id
}

// ResetCachedInstanceID resets in-memory cached IDs for test isolation.
func ResetCachedInstanceID() {
	idMu.Lock()
	defer idMu.Unlock()
	cachedIDs = make(map[string]string)
}

// FormatInstanceName returns a DNS-SD instance name that always includes the
// persistent short ID, so LAN advertisements stay unique without a startup browse.
// An empty custom name becomes CPA-<ShortID>; a custom name becomes <name>-<ShortID>.
func FormatInstanceName(customName, instanceID string) string {
	if !isValidHex4(instanceID) {
		instanceID = "0001"
	}
	instanceID = strings.ToUpper(instanceID)
	suffix := "-" + instanceID
	base := sanitizeInstanceName(customName)
	if base == "" || strings.EqualFold(base, DefaultInstancePrefix+instanceID) {
		return DefaultInstancePrefix + instanceID
	}
	if len(base) >= len(suffix) && strings.EqualFold(base[len(base)-len(suffix):], suffix) {
		base = strings.TrimSpace(base[:len(base)-len(suffix)])
	}
	base = strings.TrimSpace(truncateRunesTo(base, 63-len(suffix)))
	if base == "" {
		return DefaultInstancePrefix + instanceID
	}
	return base + suffix
}
