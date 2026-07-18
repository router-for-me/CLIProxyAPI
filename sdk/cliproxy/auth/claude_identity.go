package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	claudeUserHashDomain    = "cli-proxy-api:claude-user-id:v1"
	claudeAccountUUIDDomain = "cli-proxy-api:claude-account:v1"
	claudeSessionUUIDDomain = "cli-proxy-api:claude-session:v1"
	claudeUserQueryOpenTag  = "<user_query>"
	claudeUserQueryCloseTag = "</user_query>"
)

var claudeUserIDPattern = regexp.MustCompile(`^user_([a-fA-F0-9]{64})_account_([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})_session_([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)

// ClaudeRequestIdentity is the normalized identity sent to Claude-compatible upstreams.
type ClaudeRequestIdentity struct {
	UserID        string
	UserHash      string
	AccountID     string
	SessionID     string
	Deterministic bool
}

type canonicalClaudeUserMessage struct {
	Version int                          `json:"version"`
	Role    string                       `json:"role"`
	Parts   []canonicalClaudeContentPart `json:"parts"`
}

type canonicalClaudeContentPart struct {
	Type    string          `json:"type"`
	Text    string          `json:"text,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type claudeUserQuerySelection struct {
	TextPartIndex int
	QueryText     string
}

type claudeUserQueryStatus int

const (
	claudeUserQueryAbsent claudeUserQueryStatus = iota
	claudeUserQueryValid
	claudeUserQueryInvalid
)

// ParseClaudeUserID validates and decomposes a Claude Code user_id.
func ParseClaudeUserID(userID string) (ClaudeRequestIdentity, bool) {
	matches := claudeUserIDPattern.FindStringSubmatch(userID)
	if len(matches) != 4 {
		return ClaudeRequestIdentity{}, false
	}
	return ClaudeRequestIdentity{
		UserID:    userID,
		UserHash:  matches[1],
		AccountID: matches[2],
		SessionID: matches[3],
	}, true
}

// IsValidClaudeUserID reports whether userID matches the strict Claude Code format.
func IsValidClaudeUserID(userID string) bool {
	_, valid := ParseClaudeUserID(userID)
	return valid
}

// GenerateRandomClaudeRequestIdentity creates a valid identity for requests that
// do not contain a user-authored message that can provide a deterministic seed.
func GenerateRandomClaudeRequestIdentity() ClaudeRequestIdentity {
	randomBytes := make([]byte, 32)
	if _, errRandom := rand.Read(randomBytes); errRandom != nil {
		randomFallback := sha256.Sum256([]byte(uuid.New().String()))
		randomBytes = randomFallback[:]
	}
	userHash := hex.EncodeToString(randomBytes)
	accountID := uuid.New().String()
	sessionID := uuid.New().String()
	return buildClaudeRequestIdentity(userHash, accountID, sessionID, false)
}

// ResolveClaudeRequestIdentity preserves a valid client identity or derives one
// from the earliest wrapped user query, falling back to the first user-authored
// message. Pure tool-result messages are skipped.
func ResolveClaudeRequestIdentity(payload []byte) ([]byte, ClaudeRequestIdentity, error) {
	existingUserID := gjson.GetBytes(payload, "metadata.user_id").String()
	if existingIdentity, valid := ParseClaudeUserID(existingUserID); valid {
		return payload, existingIdentity, nil
	}

	canonicalMessage, found, errCanonical := firstCanonicalClaudeUserMessage(payload)
	if errCanonical != nil {
		return nil, ClaudeRequestIdentity{}, errCanonical
	}

	identity := GenerateRandomClaudeRequestIdentity()
	if found {
		identity = deriveClaudeRequestIdentity(canonicalMessage)
	}

	updatedPayload, errSet := ApplyClaudeRequestIdentity(payload, identity)
	if errSet != nil {
		return nil, ClaudeRequestIdentity{}, errSet
	}
	return updatedPayload, identity, nil
}

// ApplyClaudeRequestIdentity writes identity into metadata.user_id while
// preserving all unrelated request metadata.
func ApplyClaudeRequestIdentity(payload []byte, identity ClaudeRequestIdentity) ([]byte, error) {
	if !IsValidClaudeUserID(identity.UserID) {
		return nil, fmt.Errorf("invalid Claude request identity")
	}
	updatedPayload, errSet := sjson.SetBytes(payload, "metadata.user_id", identity.UserID)
	if errSet != nil {
		return nil, fmt.Errorf("set Claude metadata.user_id: %w", errSet)
	}
	return updatedPayload, nil
}

func deriveClaudeRequestIdentity(canonicalMessage []byte) ClaudeRequestIdentity {
	messageHash := sha256.Sum256(append([]byte(claudeUserHashDomain+"\x00"), canonicalMessage...))
	messageDigest := hex.EncodeToString(messageHash[:])
	accountID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(claudeAccountUUIDDomain+"\x00"+messageDigest)).String()
	sessionID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(claudeSessionUUIDDomain+"\x00"+messageDigest)).String()
	userHashInput := strings.Join([]string{claudeUserHashDomain, messageDigest, accountID, sessionID}, "\x00")
	userHash := sha256.Sum256([]byte(userHashInput))
	return buildClaudeRequestIdentity(hex.EncodeToString(userHash[:]), accountID, sessionID, true)
}

func buildClaudeRequestIdentity(userHash, accountID, sessionID string, deterministic bool) ClaudeRequestIdentity {
	userID := "user_" + userHash + "_account_" + accountID + "_session_" + sessionID
	return ClaudeRequestIdentity{
		UserID:        userID,
		UserHash:      userHash,
		AccountID:     accountID,
		SessionID:     sessionID,
		Deterministic: deterministic,
	}
}

func firstCanonicalClaudeUserMessage(payload []byte) ([]byte, bool, error) {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return nil, false, nil
	}

	canonicalQueryMessage, foundQuery, errQuery := firstCanonicalClaudeUserQueryMessage(messages)
	if errQuery != nil {
		return nil, false, errQuery
	}
	if foundQuery {
		return canonicalQueryMessage, true, nil
	}

	return firstCanonicalClaudeUserMessageFallback(messages)
}

func firstCanonicalClaudeUserQueryMessage(messages gjson.Result) ([]byte, bool, error) {
	var canonicalMessage []byte
	var canonicalError error
	messages.ForEach(func(_, message gjson.Result) bool {
		if message.Get("role").String() != "user" {
			return true
		}
		selection, queryStatus := findClaudeUserQuerySelection(message)
		if queryStatus == claudeUserQueryAbsent {
			return true
		}
		if queryStatus == claudeUserQueryInvalid {
			return false
		}
		canonical, userAuthored, errCanonical := canonicalizeClaudeUserMessageWithQuery(message, &selection)
		if errCanonical != nil {
			canonicalError = errCanonical
			return false
		}
		if !userAuthored {
			return true
		}
		canonicalMessage = canonical
		return false
	})
	if canonicalError != nil {
		return nil, false, canonicalError
	}
	return canonicalMessage, len(canonicalMessage) > 0, nil
}

func firstCanonicalClaudeUserMessageFallback(messages gjson.Result) ([]byte, bool, error) {
	var canonicalMessage []byte
	var canonicalError error
	messages.ForEach(func(_, message gjson.Result) bool {
		if message.Get("role").String() != "user" {
			return true
		}
		canonical, userAuthored, errCanonical := canonicalizeClaudeUserMessage(message)
		if errCanonical != nil {
			canonicalError = errCanonical
			return false
		}
		if !userAuthored {
			return true
		}
		canonicalMessage = canonical
		return false
	})
	if canonicalError != nil {
		return nil, false, canonicalError
	}
	return canonicalMessage, len(canonicalMessage) > 0, nil
}

func canonicalizeClaudeUserMessage(message gjson.Result) ([]byte, bool, error) {
	return canonicalizeClaudeUserMessageWithQuery(message, nil)
}

func canonicalizeClaudeUserMessageWithQuery(message gjson.Result, selection *claudeUserQuerySelection) ([]byte, bool, error) {
	content := message.Get("content")
	parts := make([]canonicalClaudeContentPart, 0)
	userAuthored := false
	var contentError error

	switch {
	case content.Type == gjson.String:
		text := content.String()
		if selection != nil {
			text = selection.QueryText
		}
		if text == "" {
			return nil, false, nil
		}
		parts = append(parts, canonicalClaudeContentPart{Type: "text", Text: text})
		userAuthored = true
	case content.IsArray():
		partIndex := 0
		content.ForEach(func(_, part gjson.Result) bool {
			currentPartIndex := partIndex
			partIndex++
			partType := strings.TrimSpace(part.Get("type").String())
			if partType == "text" {
				text := part.Get("text").String()
				if selection != nil {
					if currentPartIndex != selection.TextPartIndex {
						return true
					}
					text = selection.QueryText
				}
				parts = append(parts, canonicalClaudeContentPart{Type: "text", Text: text})
				if text != "" {
					userAuthored = true
				}
				return true
			}

			canonicalPayload, errCanonical := canonicalizeJSON(part.Raw)
			if errCanonical != nil {
				contentError = errCanonical
				return false
			}
			if partType == "" {
				partType = "unknown"
			}
			parts = append(parts, canonicalClaudeContentPart{Type: partType, Payload: canonicalPayload})
			if partType != "tool_result" {
				userAuthored = true
			}
			return true
		})
		if contentError != nil {
			return nil, false, fmt.Errorf("canonicalize Claude user content: %w", contentError)
		}
	default:
		if !content.Exists() || content.Type == gjson.Null {
			return nil, false, nil
		}
		canonicalPayload, errCanonical := canonicalizeJSON(content.Raw)
		if errCanonical != nil {
			return nil, false, fmt.Errorf("canonicalize Claude user content: %w", errCanonical)
		}
		parts = append(parts, canonicalClaudeContentPart{Type: "unknown", Payload: canonicalPayload})
		userAuthored = true
	}

	if !userAuthored {
		return nil, false, nil
	}
	canonicalMessage, errMarshal := json.Marshal(canonicalClaudeUserMessage{
		Version: 1,
		Role:    "user",
		Parts:   parts,
	})
	if errMarshal != nil {
		return nil, false, fmt.Errorf("marshal canonical Claude user message: %w", errMarshal)
	}
	return canonicalMessage, true, nil
}

func findClaudeUserQuerySelection(message gjson.Result) (claudeUserQuerySelection, claudeUserQueryStatus) {
	content := message.Get("content")
	if content.Type == gjson.String {
		queryText, queryStatus := extractStandaloneClaudeUserQuery(content.String())
		if queryStatus != claudeUserQueryValid {
			return claudeUserQuerySelection{}, queryStatus
		}
		return claudeUserQuerySelection{TextPartIndex: 0, QueryText: queryText}, claudeUserQueryValid
	}
	if !content.IsArray() {
		return claudeUserQuerySelection{}, claudeUserQueryAbsent
	}

	selectedPartIndex := -1
	selectedQueryText := ""
	partIndex := 0
	queryStatus := claudeUserQueryAbsent
	content.ForEach(func(_, part gjson.Result) bool {
		currentPartIndex := partIndex
		partIndex++
		if strings.TrimSpace(part.Get("type").String()) != "text" {
			return true
		}
		queryText, partQueryStatus := extractStandaloneClaudeUserQuery(part.Get("text").String())
		if partQueryStatus == claudeUserQueryAbsent {
			return true
		}
		if partQueryStatus == claudeUserQueryInvalid || selectedPartIndex >= 0 {
			queryStatus = claudeUserQueryInvalid
			return false
		}
		selectedPartIndex = currentPartIndex
		selectedQueryText = queryText
		queryStatus = claudeUserQueryValid
		return true
	})
	if queryStatus != claudeUserQueryValid {
		return claudeUserQuerySelection{}, queryStatus
	}
	return claudeUserQuerySelection{
		TextPartIndex: selectedPartIndex,
		QueryText:     selectedQueryText,
	}, claudeUserQueryValid
}

func extractStandaloneClaudeUserQuery(text string) (string, claudeUserQueryStatus) {
	trimmedText := strings.TrimSpace(text)
	hasOpeningBoundary := strings.HasPrefix(trimmedText, claudeUserQueryOpenTag)
	hasClosingBoundary := strings.HasSuffix(trimmedText, claudeUserQueryCloseTag)
	if !hasOpeningBoundary && !hasClosingBoundary {
		return "", claudeUserQueryAbsent
	}
	if !hasOpeningBoundary || !hasClosingBoundary {
		return "", claudeUserQueryInvalid
	}
	if strings.Count(trimmedText, claudeUserQueryOpenTag) != 1 || strings.Count(trimmedText, claudeUserQueryCloseTag) != 1 {
		return "", claudeUserQueryInvalid
	}

	queryStart := len(claudeUserQueryOpenTag)
	queryEnd := len(trimmedText) - len(claudeUserQueryCloseTag)
	if queryEnd < queryStart {
		return "", claudeUserQueryInvalid
	}
	queryText := trimmedText[queryStart:queryEnd]
	if strings.TrimSpace(queryText) == "" {
		return "", claudeUserQueryInvalid
	}
	return queryText, claudeUserQueryValid
}

func canonicalizeJSON(rawJSON string) (json.RawMessage, error) {
	var decoded any
	decoder := json.NewDecoder(strings.NewReader(rawJSON))
	decoder.UseNumber()
	if errDecode := decoder.Decode(&decoded); errDecode != nil {
		return nil, errDecode
	}
	decoded = normalizeClaudeBinaryContent(decoded)
	canonicalJSON, errMarshal := json.Marshal(decoded)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.RawMessage(canonicalJSON), nil
}

func normalizeClaudeBinaryContent(value any) any {
	switch typedValue := value.(type) {
	case []any:
		normalizedValues := make([]any, len(typedValue))
		for index, item := range typedValue {
			normalizedValues[index] = normalizeClaudeBinaryContent(item)
		}
		return normalizedValues
	case map[string]any:
		normalizedMap := make(map[string]any, len(typedValue)+2)
		for key, item := range typedValue {
			normalizedMap[key] = normalizeClaudeBinaryContent(item)
		}

		encodingType, _ := typedValue["type"].(string)
		encodedData, hasEncodedData := typedValue["data"].(string)
		if encodingType != "base64" || !hasEncodedData {
			return normalizedMap
		}

		compactEncodedData := strings.Map(func(character rune) rune {
			switch character {
			case ' ', '\t', '\r', '\n':
				return -1
			default:
				return character
			}
		}, encodedData)
		binaryData, errDecode := base64.StdEncoding.DecodeString(compactEncodedData)
		if errDecode != nil {
			binaryData, errDecode = base64.RawStdEncoding.DecodeString(compactEncodedData)
		}
		if errDecode != nil {
			return normalizedMap
		}

		binaryDigest := sha256.Sum256(binaryData)
		delete(normalizedMap, "data")
		normalizedMap["data_sha256"] = hex.EncodeToString(binaryDigest[:])
		normalizedMap["data_size"] = len(binaryData)
		return normalizedMap
	default:
		return value
	}
}
