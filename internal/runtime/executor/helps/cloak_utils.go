package helps

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// userIDPattern matches Claude Code format: user_[64-hex]_account_[uuid]_session_[uuid]
var userIDPattern = regexp.MustCompile(`^user_[a-fA-F0-9]{64}_account_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}_session_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// deterministicSeedNamespace is the UUID namespace used to derive stable
// account/session UUIDs from a seed string (e.g. an inbound BYOK client's
// metadata.user_id). Kept as a fixed UUIDv5 namespace so the same seed
// always yields the same Claude-Code-shaped triplet across binary versions
// and process restarts.
//
// Value chosen via uuid.NewSHA1(uuid.NameSpaceOID, []byte("cliproxyapi.cloak.user_id.v1"))
// — stable and stamp-free; nothing else needs to be configurable.
var deterministicSeedNamespace = uuid.NewSHA1(uuid.NameSpaceOID, []byte("cliproxyapi.cloak.user_id.v1"))

// generateFakeUserID generates a fake user ID in Claude Code format.
// Format: user_[64-hex-chars]_account_[UUID-v4]_session_[UUID-v4]
func generateFakeUserID() string {
	hexBytes := make([]byte, 32)
	_, _ = rand.Read(hexBytes)
	hexPart := hex.EncodeToString(hexBytes)
	accountUUID := uuid.New().String()
	sessionUUID := uuid.New().String()
	return "user_" + hexPart + "_account_" + accountUUID + "_session_" + sessionUUID
}

// deterministicFakeUserID derives a stable Claude-Code-shaped user_id from
// the given seed string. The same seed always produces the same triplet
// (64-hex digest + account UUID + session UUID), so BYOK clients that send
// a stable per-user identifier in metadata.user_id (e.g. Capy's
// "user_<base62>") get a stable cloaked identity across calls — Anthropic's
// prompt cache can attribute consecutive calls to the same end user and
// reuse the prefix.
//
// The seed is hashed once with SHA-256 to fill the 64-hex slot; the
// account_uuid and session_uuid slots are filled with UUIDv5 over a fixed
// namespace and seed-derived names ("acct|<seed>" / "sess|<seed>"), which
// produces well-formed RFC 4122 UUIDs that pass the strict userIDPattern.
//
// Returns an empty string if the seed is empty (callers should fall back
// to per-API-key cached or fresh random IDs in that case).
func deterministicFakeUserID(seed string) string {
	if seed == "" {
		return ""
	}
	digest := sha256.Sum256([]byte("cliproxyapi.cloak.user_id.v1|" + seed))
	hexPart := hex.EncodeToString(digest[:])
	accountUUID := uuid.NewSHA1(deterministicSeedNamespace, []byte("acct|"+seed)).String()
	sessionUUID := uuid.NewSHA1(deterministicSeedNamespace, []byte("sess|"+seed)).String()
	return "user_" + hexPart + "_account_" + accountUUID + "_session_" + sessionUUID
}

// isValidUserID checks if a user ID matches Claude Code format.
func isValidUserID(userID string) bool {
	return userIDPattern.MatchString(userID)
}

func GenerateFakeUserID() string {
	return generateFakeUserID()
}

// DeterministicFakeUserID is the exported entry point for deriving a
// CC-shaped user_id from a seed string. See deterministicFakeUserID.
func DeterministicFakeUserID(seed string) string {
	return deterministicFakeUserID(seed)
}

func IsValidUserID(userID string) bool {
	return isValidUserID(userID)
}

// ShouldCloak determines if request should be cloaked based on config and client User-Agent.
// Returns true if cloaking should be applied.
func ShouldCloak(cloakMode string, userAgent string) bool {
	switch strings.ToLower(cloakMode) {
	case "always":
		return true
	case "never":
		return false
	default: // "auto" or empty
		// If client is Claude Code, don't cloak
		return !strings.HasPrefix(userAgent, "claude-cli")
	}
}

// isClaudeCodeClient checks if the User-Agent indicates a Claude Code client.
func isClaudeCodeClient(userAgent string) bool {
	return strings.HasPrefix(userAgent, "claude-cli")
}
