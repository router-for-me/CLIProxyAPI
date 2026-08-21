package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"

	"github.com/google/uuid"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

var claudeMetadataDeviceIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type claudeMetadataUserID struct {
	DeviceID    string `json:"device_id"`
	AccountUUID string `json:"account_uuid"`
	SessionID   string `json:"session_id"`
}

// generateFakeUserID generates metadata.user_id in the JSON string format used
// by Claude Code 2.1.78 and newer.
// The device_id is derived deterministically from the credential seed +
// session, preserving prompt-cache prefix stability even on cache-miss paths.
func generateFakeUserID(apiKey string, auth *cliproxyauth.Auth) string {
	seed := claudeCredentialSeed(apiKey, auth)
	return generateDeterministicFakeUserID(seed, CachedSessionID(apiKey, auth))
}

func generateFakeUserIDWithSessionID(sessionID string) string {
	return generateDeterministicFakeUserID("", sessionID)
}

// GenerateRandomFakeUserIDForSession returns a metadata.user_id with a fresh
// random device_id while keeping the session_id stable. This is the legacy
// per-request random behavior used when cache-user-id is false.
func GenerateRandomFakeUserIDForSession(sessionID string) string {
	return generateDeterministicFakeUserID(uuid.New().String(), sessionID)
}

func generateDeterministicFakeUserID(seed, sessionID string) string {
	if _, errParse := uuid.Parse(sessionID); errParse != nil {
		sessionID = uuid.New().String()
	}
	h := sha256.Sum256([]byte(seed + ":" + sessionID))
	value, _ := json.Marshal(claudeMetadataUserID{
		DeviceID:    hex.EncodeToString(h[:]),
		AccountUUID: "",
		SessionID:   sessionID,
	})
	return string(value)
}

// isValidUserID checks the Claude Code 2.1.220 metadata.user_id shape.
func isValidUserID(userID string) bool {
	var value claudeMetadataUserID
	if errUnmarshal := json.Unmarshal([]byte(userID), &value); errUnmarshal != nil {
		return false
	}
	if !claudeMetadataDeviceIDPattern.MatchString(value.DeviceID) {
		return false
	}
	if _, errParse := uuid.Parse(value.SessionID); errParse != nil {
		return false
	}
	if value.AccountUUID == "" {
		return true
	}
	_, errParse := uuid.Parse(value.AccountUUID)
	return errParse == nil
}

func GenerateFakeUserID() string {
	return generateFakeUserID("", nil)
}

func GenerateFakeUserIDWithSessionID(sessionID string) string {
	return generateFakeUserIDWithSessionID(sessionID)
}

func IsValidUserID(userID string) bool {
	return isValidUserID(userID)
}
