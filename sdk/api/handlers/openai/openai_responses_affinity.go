package openai

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	defaultResponsesAuthAffinityTTL        = 6 * time.Hour
	defaultResponsesAuthAffinityMaxEntries = 8192
)

type responsesAuthAffinityEntry struct {
	authID    string
	expiresAt time.Time
}

type responsesAuthAffinityStore struct {
	mu              sync.Mutex
	ttl             time.Duration
	maxEntries      int
	byResponseID    map[string]responsesAuthAffinityEntry
	byEncryptedHash map[string]responsesAuthAffinityEntry
}

func newResponsesAuthAffinityStore(ttl time.Duration, maxEntries int) *responsesAuthAffinityStore {
	if ttl <= 0 {
		ttl = defaultResponsesAuthAffinityTTL
	}
	if maxEntries <= 0 {
		maxEntries = defaultResponsesAuthAffinityMaxEntries
	}
	return &responsesAuthAffinityStore{
		ttl:             ttl,
		maxEntries:      maxEntries,
		byResponseID:    make(map[string]responsesAuthAffinityEntry),
		byEncryptedHash: make(map[string]responsesAuthAffinityEntry),
	}
}

var responsesAuthAffinity = newResponsesAuthAffinityStore(defaultResponsesAuthAffinityTTL, defaultResponsesAuthAffinityMaxEntries)

func resetResponsesAuthAffinityForTests() {
	responsesAuthAffinity = newResponsesAuthAffinityStore(defaultResponsesAuthAffinityTTL, defaultResponsesAuthAffinityMaxEntries)
}

func resolvePinnedAuthIDForResponses(rawJSON []byte) string {
	if len(rawJSON) == 0 {
		return ""
	}
	if responseID := strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String()); responseID != "" {
		if authID, ok := responsesAuthAffinity.lookupResponseID(responseID); ok {
			return authID
		}
	}
	for _, encrypted := range extractReasoningEncryptedFromInput(rawJSON) {
		if authID, ok := responsesAuthAffinity.lookupEncrypted(encrypted); ok {
			return authID
		}
	}
	return ""
}

func rememberResponsesAuthAffinity(authID string, responsePayload []byte) {
	authID = strings.TrimSpace(authID)
	if authID == "" || len(responsePayload) == 0 {
		return
	}
	responseID := strings.TrimSpace(gjson.GetBytes(responsePayload, "id").String())
	if responseID == "" {
		responseID = strings.TrimSpace(gjson.GetBytes(responsePayload, "response.id").String())
	}
	if responseID != "" {
		responsesAuthAffinity.rememberResponseID(responseID, authID)
	}
	for _, encrypted := range extractReasoningEncryptedFromOutput(responsePayload) {
		responsesAuthAffinity.rememberEncrypted(encrypted, authID)
	}
}

func rememberResponsesAuthAffinityFromSSE(authID string, sseChunk []byte) {
	authID = strings.TrimSpace(authID)
	if authID == "" || len(sseChunk) == 0 {
		return
	}
	for _, line := range bytes.Split(sseChunk, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		event := bytes.TrimSpace(line[5:])
		if len(event) == 0 || bytes.Equal(event, []byte("[DONE]")) {
			continue
		}
		if !gjson.ValidBytes(event) {
			continue
		}

		eventType := strings.TrimSpace(gjson.GetBytes(event, "type").String())
		switch eventType {
		case "response.completed":
			response := gjson.GetBytes(event, "response")
			if response.Exists() {
				rememberResponsesAuthAffinity(authID, []byte(response.Raw))
			}
		case "response.output_item.done", "response.output_item.added":
			item := gjson.GetBytes(event, "item")
			if item.Exists() && strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "reasoning") {
				if encrypted := strings.TrimSpace(item.Get("encrypted_content").String()); encrypted != "" {
					responsesAuthAffinity.rememberEncrypted(encrypted, authID)
				}
			}
		}
	}
}

func isInvalidEncryptedContentError(errMsg *interfaces.ErrorMessage) bool {
	if errMsg == nil || errMsg.Error == nil || errMsg.StatusCode != 400 {
		return false
	}
	errText := strings.ToLower(errMsg.Error.Error())
	if strings.Contains(errText, "invalid_encrypted_content") {
		return true
	}
	return strings.Contains(errText, "encrypted content could not be decrypted or parsed")
}

func stripEncryptedReasoningInput(rawJSON []byte) ([]byte, bool) {
	input := gjson.GetBytes(rawJSON, "input")
	if !input.Exists() || !input.IsArray() {
		return rawJSON, false
	}

	items := input.Array()
	filtered := make([]string, 0, len(items))
	removed := false
	for _, item := range items {
		itemType := strings.TrimSpace(item.Get("type").String())
		encrypted := strings.TrimSpace(item.Get("encrypted_content").String())
		if strings.EqualFold(itemType, "reasoning") && encrypted != "" {
			removed = true
			continue
		}
		filtered = append(filtered, item.Raw)
	}
	if !removed {
		return rawJSON, false
	}

	rawArray := "[]"
	if len(filtered) > 0 {
		rawArray = "[" + strings.Join(filtered, ",") + "]"
	}
	updated, errSet := sjson.SetRawBytes(rawJSON, "input", []byte(rawArray))
	if errSet != nil {
		return rawJSON, false
	}
	return updated, true
}

func extractReasoningEncryptedFromInput(rawJSON []byte) []string {
	input := gjson.GetBytes(rawJSON, "input")
	if !input.IsArray() {
		return nil
	}
	out := make([]string, 0, 2)
	for _, item := range input.Array() {
		if !strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "reasoning") {
			continue
		}
		encrypted := strings.TrimSpace(item.Get("encrypted_content").String())
		if encrypted != "" {
			out = append(out, encrypted)
		}
	}
	return out
}

func extractReasoningEncryptedFromOutput(rawJSON []byte) []string {
	output := gjson.GetBytes(rawJSON, "output")
	if !output.IsArray() {
		return nil
	}
	out := make([]string, 0, 2)
	for _, item := range output.Array() {
		if !strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "reasoning") {
			continue
		}
		encrypted := strings.TrimSpace(item.Get("encrypted_content").String())
		if encrypted != "" {
			out = append(out, encrypted)
		}
	}
	return out
}

func encryptedContentHash(encrypted string) string {
	sum := sha256.Sum256([]byte(encrypted))
	return hex.EncodeToString(sum[:])
}

func (s *responsesAuthAffinityStore) lookupResponseID(responseID string) (string, bool) {
	responseID = strings.TrimSpace(responseID)
	if s == nil || responseID == "" {
		return "", false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byResponseID[responseID]
	if !ok {
		return "", false
	}
	if now.After(entry.expiresAt) {
		delete(s.byResponseID, responseID)
		return "", false
	}
	return entry.authID, true
}

func (s *responsesAuthAffinityStore) lookupEncrypted(encrypted string) (string, bool) {
	encrypted = strings.TrimSpace(encrypted)
	if s == nil || encrypted == "" {
		return "", false
	}
	key := encryptedContentHash(encrypted)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byEncryptedHash[key]
	if !ok {
		return "", false
	}
	if now.After(entry.expiresAt) {
		delete(s.byEncryptedHash, key)
		return "", false
	}
	return entry.authID, true
}

func (s *responsesAuthAffinityStore) rememberResponseID(responseID, authID string) {
	responseID = strings.TrimSpace(responseID)
	authID = strings.TrimSpace(authID)
	if s == nil || responseID == "" || authID == "" {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	s.byResponseID[responseID] = responsesAuthAffinityEntry{authID: authID, expiresAt: now.Add(s.ttl)}
}

func (s *responsesAuthAffinityStore) rememberEncrypted(encrypted, authID string) {
	encrypted = strings.TrimSpace(encrypted)
	authID = strings.TrimSpace(authID)
	if s == nil || encrypted == "" || authID == "" {
		return
	}
	now := time.Now()
	key := encryptedContentHash(encrypted)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	s.byEncryptedHash[key] = responsesAuthAffinityEntry{authID: authID, expiresAt: now.Add(s.ttl)}
}

func (s *responsesAuthAffinityStore) pruneLocked(now time.Time) {
	for key, entry := range s.byResponseID {
		if now.After(entry.expiresAt) {
			delete(s.byResponseID, key)
		}
	}
	for key, entry := range s.byEncryptedHash {
		if now.After(entry.expiresAt) {
			delete(s.byEncryptedHash, key)
		}
	}
	if len(s.byResponseID) <= s.maxEntries && len(s.byEncryptedHash) <= s.maxEntries {
		return
	}
	// Coarse pressure relief: drop all expired first (done above), then clear oldest pressure by resetting maps.
	// This keeps affinity bounded and avoids unbounded memory growth in long-lived proxies.
	if len(s.byResponseID) > s.maxEntries {
		s.byResponseID = make(map[string]responsesAuthAffinityEntry, s.maxEntries/2)
	}
	if len(s.byEncryptedHash) > s.maxEntries {
		s.byEncryptedHash = make(map[string]responsesAuthAffinityEntry, s.maxEntries/2)
	}
}
