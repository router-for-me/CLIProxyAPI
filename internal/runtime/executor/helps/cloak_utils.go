package helps

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"regexp"

	"github.com/google/uuid"
)

var claudeMetadataDeviceIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type claudeMetadataUserID struct {
	DeviceID    string `json:"device_id"`
	AccountUUID string `json:"account_uuid"`
	SessionID   string `json:"session_id"`
}

// generateFakeUserID generates metadata.user_id in the JSON string format used
// by Claude Code 2.1.78 and newer.
func generateFakeUserID() string {
	return generateFakeUserIDWithSessionID(uuid.New().String())
}

func generateFakeUserIDWithSessionID(sessionID string) string {
	if _, errParse := uuid.Parse(sessionID); errParse != nil {
		sessionID = uuid.New().String()
	}
	hexBytes := make([]byte, 32)
	_, _ = rand.Read(hexBytes)
	value, _ := json.Marshal(claudeMetadataUserID{
		DeviceID:    hex.EncodeToString(hexBytes),
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
	return generateFakeUserID()
}

func GenerateFakeUserIDWithSessionID(sessionID string) string {
	return generateFakeUserIDWithSessionID(sessionID)
}

func IsValidUserID(userID string) bool {
	return isValidUserID(userID)
}
