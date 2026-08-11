package access

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	// MetadataClientKeyID identifies the stable client key ID in access results.
	MetadataClientKeyID = "client_key_id"

	// MetadataClientKeyAlias identifies the user-facing client key alias in access results.
	MetadataClientKeyAlias = "client_key_alias"
)

// FallbackClientKeyID returns a deterministic grouping identifier without
// returning the raw client API key when no explicit ID is configured.
func FallbackClientKeyID(apiKey string) string {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(apiKey))
	return "key_" + hex.EncodeToString(sum[:8])
}
