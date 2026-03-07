package openai

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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
	defaultResponsesAuthAffinityStoreFile  = "cliproxy_responses_auth_affinity.json"
)

type responsesAuthAffinityEntry struct {
	authID    string
	expiresAt time.Time
}

type responsesAuthAffinityPersistEntry struct {
	AuthID    string    `json:"auth_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

type responsesAuthAffinitySnapshot struct {
	Version         int                                          `json:"version"`
	SavedAt         time.Time                                    `json:"saved_at"`
	ByResponseID    map[string]responsesAuthAffinityPersistEntry `json:"by_response_id"`
	ByEncryptedHash map[string]responsesAuthAffinityPersistEntry `json:"by_encrypted_hash"`
}

type responsesAuthAffinityStore struct {
	mu              sync.Mutex
	ttl             time.Duration
	maxEntries      int
	persistPath     string
	byResponseID    map[string]responsesAuthAffinityEntry
	byEncryptedHash map[string]responsesAuthAffinityEntry
}

func newResponsesAuthAffinityStore(ttl time.Duration, maxEntries int) *responsesAuthAffinityStore {
	return newResponsesAuthAffinityStoreWithPersistence(ttl, maxEntries, resolveResponsesAffinityPersistPath())
}

func newResponsesAuthAffinityStoreWithPersistence(ttl time.Duration, maxEntries int, persistPath string) *responsesAuthAffinityStore {
	if ttl <= 0 {
		ttl = defaultResponsesAuthAffinityTTL
	}
	if maxEntries <= 0 {
		maxEntries = defaultResponsesAuthAffinityMaxEntries
	}
	store := &responsesAuthAffinityStore{
		ttl:             ttl,
		maxEntries:      maxEntries,
		persistPath:     strings.TrimSpace(persistPath),
		byResponseID:    make(map[string]responsesAuthAffinityEntry),
		byEncryptedHash: make(map[string]responsesAuthAffinityEntry),
	}
	store.loadFromDisk()
	return store
}

func resolveResponsesAffinityPersistPath() string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("CLIPROXY_RESPONSES_AFFINITY_PERSIST")), "false") ||
		strings.EqualFold(strings.TrimSpace(os.Getenv("CLIPROXY_RESPONSES_AFFINITY_PERSIST")), "0") {
		return ""
	}
	if explicit := strings.TrimSpace(os.Getenv("CLIPROXY_RESPONSES_AFFINITY_PATH")); explicit != "" {
		return explicit
	}
	return filepath.Join(os.TempDir(), defaultResponsesAuthAffinityStoreFile)
}

var responsesAuthAffinity = newResponsesAuthAffinityStore(defaultResponsesAuthAffinityTTL, defaultResponsesAuthAffinityMaxEntries)

func resetResponsesAuthAffinityForTests() {
	responsesAuthAffinity = newResponsesAuthAffinityStoreWithPersistence(defaultResponsesAuthAffinityTTL, defaultResponsesAuthAffinityMaxEntries, "")
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
	if errMsg == nil || errMsg.Error == nil || errMsg.StatusCode != http.StatusBadRequest {
		return false
	}
	errText := strings.ToLower(errMsg.Error.Error())
	if strings.Contains(errText, "invalid_encrypted_content") {
		return true
	}
	return strings.Contains(errText, "encrypted content could not be decrypted or parsed")
}

func stripEncryptedReasoningInput(rawJSON []byte) ([]byte, bool) {
	updated := rawJSON
	changed := false

	// Strip reasoning items with encrypted_content from the input array.
	input := gjson.GetBytes(updated, "input")
	if input.Exists() && input.IsArray() {
		items := input.Array()
		filtered := make([]string, 0, len(items))
		removedReasoning := false
		for _, item := range items {
			itemType := strings.TrimSpace(item.Get("type").String())
			encrypted := strings.TrimSpace(item.Get("encrypted_content").String())
			if strings.EqualFold(itemType, "reasoning") && encrypted != "" {
				removedReasoning = true
				continue
			}
			filtered = append(filtered, item.Raw)
		}
		if removedReasoning {
			rawArray := "[]"
			if len(filtered) > 0 {
				rawArray = "[" + strings.Join(filtered, ",") + "]"
			}
			if result, err := sjson.SetRawBytes(updated, "input", []byte(rawArray)); err == nil {
				updated = result
				changed = true
			}
		}
	}

	// Strip previous_response_id: it references server-side conversation state
	// tied to the original auth/org. Keeping it causes OpenAI to reload stored
	// reasoning with encrypted content from the original org → same error.
	if gjson.GetBytes(updated, "previous_response_id").Exists() {
		if stripped, err := sjson.DeleteBytes(updated, "previous_response_id"); err == nil {
			updated = stripped
			changed = true
		}
	}

	return updated, changed
}

// hasEncryptedContentContext returns true when the request carries context
// tied to a specific auth/org: either inline encrypted reasoning in the input
// array or a previous_response_id referencing server-side conversation state.
func hasEncryptedContentContext(rawJSON []byte) bool {
	if len(extractReasoningEncryptedFromInput(rawJSON)) > 0 {
		return true
	}
	return strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String()) != ""
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
	s.persistLocked(now)
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
	s.persistLocked(now)
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
	// Pressure relief: drop expired first (done above), then randomly evict 25%
	// of entries to stay within bounds. Random eviction is simple and avoids the
	// thundering-herd effect of clearing the entire map at once.
	if len(s.byResponseID) > s.maxEntries {
		targetSize := s.maxEntries * 3 / 4
		for k := range s.byResponseID {
			if len(s.byResponseID) <= targetSize {
				break
			}
			delete(s.byResponseID, k)
		}
	}
	if len(s.byEncryptedHash) > s.maxEntries {
		targetSize := s.maxEntries * 3 / 4
		for k := range s.byEncryptedHash {
			if len(s.byEncryptedHash) <= targetSize {
				break
			}
			delete(s.byEncryptedHash, k)
		}
	}
}

func (s *responsesAuthAffinityStore) loadFromDisk() {
	if s == nil || strings.TrimSpace(s.persistPath) == "" {
		return
	}
	data, err := os.ReadFile(s.persistPath)
	if err != nil || len(data) == 0 {
		return
	}
	var snap responsesAuthAffinitySnapshot
	if err = json.Unmarshal(data, &snap); err != nil {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, entry := range snap.ByResponseID {
		if strings.TrimSpace(entry.AuthID) == "" || now.After(entry.ExpiresAt) {
			continue
		}
		s.byResponseID[key] = responsesAuthAffinityEntry{
			authID:    strings.TrimSpace(entry.AuthID),
			expiresAt: entry.ExpiresAt,
		}
	}
	for key, entry := range snap.ByEncryptedHash {
		if strings.TrimSpace(entry.AuthID) == "" || now.After(entry.ExpiresAt) {
			continue
		}
		s.byEncryptedHash[key] = responsesAuthAffinityEntry{
			authID:    strings.TrimSpace(entry.AuthID),
			expiresAt: entry.ExpiresAt,
		}
	}
}

func (s *responsesAuthAffinityStore) persistLocked(now time.Time) {
	if s == nil || strings.TrimSpace(s.persistPath) == "" {
		return
	}
	snap := responsesAuthAffinitySnapshot{
		Version:         1,
		SavedAt:         now,
		ByResponseID:    make(map[string]responsesAuthAffinityPersistEntry, len(s.byResponseID)),
		ByEncryptedHash: make(map[string]responsesAuthAffinityPersistEntry, len(s.byEncryptedHash)),
	}
	for key, entry := range s.byResponseID {
		snap.ByResponseID[key] = responsesAuthAffinityPersistEntry{
			AuthID:    entry.authID,
			ExpiresAt: entry.expiresAt,
		}
	}
	for key, entry := range s.byEncryptedHash {
		snap.ByEncryptedHash[key] = responsesAuthAffinityPersistEntry{
			AuthID:    entry.authID,
			ExpiresAt: entry.expiresAt,
		}
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return
	}
	if err = os.MkdirAll(filepath.Dir(s.persistPath), 0o700); err != nil {
		return
	}
	tmpPath := s.persistPath + ".tmp"
	if err = os.WriteFile(tmpPath, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmpPath, s.persistPath)
}
