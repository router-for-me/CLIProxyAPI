package helps

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// userIDPattern matches Claude Code 2.1.92+ JSON format:
// {"device_id":"[64-hex]","account_uuid":"[uuid]","session_id":"[uuid]"}
var userIDPattern = regexp.MustCompile(`^\{"device_id":"[a-fA-F0-9]{64}","account_uuid":"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}","session_id":"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"\}$`)

// generateFakeUserID generates a fake user ID in Claude Code 2.1.92+ JSON format.
// Format: {"device_id":"[64-hex]","account_uuid":"[UUID-v4]","session_id":"[UUID-v4]"}
// Use generateFakeUserIDWithSession when you have a stable session ID to keep consistent
// with the X-Claude-Code-Session-Id header.
func generateFakeUserID() string {
	return generateFakeUserIDWithSession(uuid.New().String())
}

// generateFakeUserIDWithSession generates a fake user ID using a specific session UUID.
// device_id and account_uuid are random; session_id matches the provided value so that
// metadata.user_id.session_id stays consistent with X-Claude-Code-Session-Id.
func generateFakeUserIDWithSession(sessionID string) string {
	hexBytes := make([]byte, 32)
	_, _ = rand.Read(hexBytes)
	deviceID := hex.EncodeToString(hexBytes)
	accountUUID := uuid.New().String()
	return `{"device_id":"` + deviceID + `","account_uuid":"` + accountUUID + `","session_id":"` + sessionID + `"}`
}

// isValidUserID checks if a user ID matches Claude Code 2.1.92+ JSON format.
func isValidUserID(userID string) bool {
	return userIDPattern.MatchString(userID)
}

func GenerateFakeUserID() string {
	return generateFakeUserID()
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
