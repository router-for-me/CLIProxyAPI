package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
)

// claudeWebSearchOwnerLimit bounds how many encrypted web search values are
// attributed at once. An evicted value is treated as foreign, which only costs
// the replay of that citation or result.
const claudeWebSearchOwnerLimit = 16384

var (
	claudeWebSearchOwnerMu sync.RWMutex
	claudeWebSearchOwners  = NewBoundedLRU[string, string](claudeWebSearchOwnerLimit, nil)
)

func currentClaudeWebSearchOwners() *BoundedLRU[string, string] {
	claudeWebSearchOwnerMu.RLock()
	defer claudeWebSearchOwnerMu.RUnlock()
	return claudeWebSearchOwners
}

func claudeWebSearchOwnerKey(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}

// RecordClaudeWebSearchOwner remembers owner as the credential that received
// value, an encrypted_index or encrypted_content Anthropic minted for it.
func RecordClaudeWebSearchOwner(value, owner string) {
	key := claudeWebSearchOwnerKey(value)
	owner = strings.TrimSpace(owner)
	if key == "" || owner == "" {
		return
	}
	owners := currentClaudeWebSearchOwners()
	if recorded, ok := owners.Get(key); ok && recorded == owner {
		return
	}
	owners.Delete(key)
	owners.GetOrAdd(key, func() string { return owner })
}

// ClaudeWebSearchOwner returns the credential recorded for value.
func ClaudeWebSearchOwner(value string) (string, bool) {
	key := claudeWebSearchOwnerKey(value)
	if key == "" {
		return "", false
	}
	return currentClaudeWebSearchOwners().Get(key)
}

// ClearClaudeWebSearchOwners forgets every recorded owner.
func ClearClaudeWebSearchOwners() {
	claudeWebSearchOwnerMu.Lock()
	defer claudeWebSearchOwnerMu.Unlock()
	claudeWebSearchOwners = NewBoundedLRU[string, string](claudeWebSearchOwnerLimit, nil)
}
