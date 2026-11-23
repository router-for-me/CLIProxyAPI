package helps

import (
	"hash/fnv"
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

// HashAntigravitySessionID hashes input with 64-bit FNV-1a and formats the
// signed int64 value as a base-10 decimal string, matching Antigravity Hub's
// request.sessionId format (~50% positive, ~50% negative).
func HashAntigravitySessionID(input []byte) string {
	h := fnv.New64a()
	_, _ = h.Write(input)
	return strconv.FormatInt(int64(h.Sum64()), 10)
}

// DerivedAntigravitySessionID maps a derived session identity to Antigravity's
// signed 64-bit decimal sessionId format.
func DerivedAntigravitySessionID(metadataSets ...map[string]any) string {
	derivedID := DerivedSessionID(metadataSets...)
	if derivedID == "" {
		return ""
	}
	// Place the variable identity before the fixed domain suffix so trailing-byte
	// variations (e.g. sequential session or subagent IDs) still avalanche into the sign bit of FNV-1a.
	return HashAntigravitySessionID([]byte(derivedID + "\x00cli-proxy-api:antigravity:derived-session"))
}

// AntigravitySessionIDForAuth deterministically maps the request's canonical
// (or execution/derived) session identity scoped to a specific credential into
// Antigravity's signed 64-bit decimal sessionId format.
func AntigravitySessionIDForAuth(authID string, metadataSets ...map[string]any) string {
	kind, identityValue := antigravitySessionIdentity(metadataSets...)
	return formatAntigravitySessionIDForAuth(kind, authID, identityValue)
}

// ScopeAntigravitySessionIDForAuth deterministically scopes an explicit
// incoming request.sessionId to a specific credential in Antigravity's signed
// 64-bit decimal sessionId format. When authID is empty, the trimmed incoming
// sessionId is returned unchanged.
func ScopeAntigravitySessionIDForAuth(authID, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return sessionID
	}
	return formatAntigravitySessionIDForAuth("explicit-session", authID, sessionID)
}

func formatAntigravitySessionIDForAuth(kind, authID, identityValue string) string {
	identityValue = strings.TrimSpace(identityValue)
	if identityValue == "" {
		return ""
	}
	authID = strings.TrimSpace(authID)
	// Place variable identity and authID before the fixed domain suffix so
	// sequential trailing bytes avalanche cleanly into the high sign bit of FNV-1a.
	input := strings.Join([]string{identityValue, authID, kind, "antigravity", "cli-proxy-api"}, "\x00")
	return HashAntigravitySessionID([]byte(input))
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
