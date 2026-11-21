package helps

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"
	"strings"

	"github.com/google/uuid"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
)

// DerivedSessionID returns the first context-derived session identity in metadata order.
func DerivedSessionID(metadataSets ...map[string]any) string {
	for _, metadata := range metadataSets {
		if derivedID := cliproxysession.DerivedID(metadata); derivedID != "" {
			return derivedID
		}
	}
	return ""
}

// DerivedSessionUUID maps a derived session identity to a provider-scoped stable UUID.
func DerivedSessionUUID(provider string, metadataSets ...map[string]any) string {
	return stableProviderSessionUUID(provider, "derived-session", DerivedSessionID(metadataSets...))
}

// ProviderSessionUUID prefers a long-lived execution session and falls back to the derived identity.
func ProviderSessionUUID(provider string, metadataSets ...map[string]any) string {
	for _, metadata := range metadataSets {
		if executionID := metadataString(metadata, cliproxyexecutor.ExecutionSessionMetadataKey); executionID != "" {
			return stableProviderSessionUUID(provider, "execution-session", executionID)
		}
	}
	return DerivedSessionUUID(provider, metadataSets...)
}

func stableProviderSessionUUID(provider string, kind string, identityValue string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	identityValue = strings.TrimSpace(identityValue)
	if provider == "" || identityValue == "" {
		return ""
	}
	identity := strings.Join([]string{"cli-proxy-api", provider, kind, identityValue}, "\x00")
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(identity)).String()
}

// DerivedAntigravitySessionID maps a derived session identity to Antigravity's negative decimal format.
func DerivedAntigravitySessionID(metadataSets ...map[string]any) string {
	derivedID := DerivedSessionID(metadataSets...)
	if derivedID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("cli-proxy-api:antigravity:derived-session\x00" + derivedID))
	value := int64(binary.BigEndian.Uint64(sum[:8])) & 0x7FFFFFFFFFFFFFFF
	return "-" + strconv.FormatInt(value, 10)
}

// AntigravitySessionIDForAuth deterministically maps the request's canonical
// (or execution/derived) session identity scoped to a specific credential into
// Antigravity's negative int63 decimal format ("-<int63>").
func AntigravitySessionIDForAuth(authID string, metadataSets ...map[string]any) string {
	kind, identityValue := antigravitySessionIdentity(metadataSets...)
	if identityValue == "" {
		return ""
	}
	authID = strings.TrimSpace(authID)
	input := strings.Join([]string{"cli-proxy-api", "antigravity", kind, authID, identityValue}, "\x00")
	sum := sha256.Sum256([]byte(input))
	value := int64(binary.BigEndian.Uint64(sum[:8])) & 0x7FFFFFFFFFFFFFFF
	return "-" + strconv.FormatInt(value, 10)
}

func antigravitySessionIdentity(metadataSets ...map[string]any) (string, string) {
	for _, metadata := range metadataSets {
		if canonicalID := metadataString(metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey); canonicalID != "" {
			return "canonical-session", canonicalID
		}
	}
	for _, metadata := range metadataSets {
		if executionID := metadataString(metadata, cliproxyexecutor.ExecutionSessionMetadataKey); executionID != "" {
			return "execution-session", executionID
		}
	}
	if derivedID := DerivedSessionID(metadataSets...); derivedID != "" {
		return "derived-session", derivedID
	}
	return "", ""
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}
