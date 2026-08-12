package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

const (
	maxClientAPIKeyIDLength    = 64
	maxClientAPIKeyAliasLength = 128
)

var clientAPIKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type apiKeyProfileResponse struct {
	Index     int    `json:"index"`
	ID        string `json:"id"`
	Revision  string `json:"key_revision"`
	Alias     string `json:"alias"`
	Disabled  bool   `json:"disabled"`
	MaskedKey string `json:"masked_key"`
	Effective bool   `json:"effective"`
	Issue     string `json:"issue,omitempty"`
}

type apiKeyProfileInput struct {
	Index      *int    `json:"index"`
	ExpectedID *string `json:"expected_id"`
	ID         *string `json:"id"`
	Alias      *string `json:"alias"`
	Disabled   *bool   `json:"disabled"`
}

// GetAPIKeyProfiles returns client API key metadata without exposing raw keys.
func (h *Handler) GetAPIKeyProfiles(c *gin.Context) {
	h.mu.Lock()
	profiles := apiKeyProfilesLocked(h.cfg)
	h.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"api-key-profiles": profiles})
}

func apiKeyProfilesLocked(cfg *config.Config) []apiKeyProfileResponse {
	if cfg == nil {
		return []apiKeyProfileResponse{}
	}
	profiles := make([]apiKeyProfileResponse, 0, len(cfg.APIKeys))
	seenKeys := make(map[string]struct{}, len(cfg.APIKeys))
	for index, rawKey := range cfg.APIKeys {
		key := clientAPIKeyMetadataKey(rawKey)
		metadata := cfg.APIKeyMetadata[key]
		effective := key != ""
		issue := ""
		if key == "" {
			issue = "empty"
		} else if _, duplicate := seenKeys[key]; duplicate {
			effective = false
			issue = "duplicate"
		} else {
			seenKeys[key] = struct{}{}
		}
		profiles = append(profiles, apiKeyProfileResponse{
			Index:     index,
			ID:        resolvedClientAPIKeyID(key, metadata),
			Revision:  sdkaccess.FallbackClientKeyID(key),
			Alias:     metadata.Alias,
			Disabled:  metadata.Disabled,
			MaskedKey: maskClientAPIKey(key),
			Effective: effective,
			Issue:     issue,
		})
	}
	return profiles
}

// PutAPIKeyProfiles replaces metadata for all configured client API keys.
func (h *Handler) PutAPIKeyProfiles(c *gin.Context) {
	inputs, err := readAPIKeyProfileInputs(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	next := make(map[string]config.ClientAPIKeyMetadata, len(inputs))
	seenIndexes := make(map[int]struct{}, len(inputs))
	for _, input := range inputs {
		if input.Index == nil || *input.Index < 0 || *input.Index >= len(h.cfg.APIKeys) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid index"})
			return
		}
		index := *input.Index
		if _, exists := seenIndexes[index]; exists {
			c.JSON(http.StatusBadRequest, gin.H{"error": "duplicate index"})
			return
		}
		seenIndexes[index] = struct{}{}
		currentKey := clientAPIKeyMetadataKey(h.cfg.APIKeys[index])
		if input.ExpectedID != nil && strings.TrimSpace(*input.ExpectedID) != resolvedClientAPIKeyID(currentKey, h.cfg.APIKeyMetadata[currentKey]) {
			c.JSON(http.StatusConflict, gin.H{"error": "API key profile changed; refresh and retry"})
			return
		}

		metadata := config.ClientAPIKeyMetadata{}
		if input.ID != nil {
			metadata.ID = strings.TrimSpace(*input.ID)
		}
		if input.Alias != nil {
			metadata.Alias = strings.TrimSpace(*input.Alias)
		}
		if input.Disabled != nil {
			metadata.Disabled = *input.Disabled
		}
		key := currentKey
		if existing, exists := next[key]; exists && existing != metadata {
			c.JSON(http.StatusBadRequest, gin.H{"error": "conflicting profiles for duplicate API key"})
			return
		}
		if !clientAPIKeyMetadataEmpty(metadata) {
			next[key] = metadata
		}
	}

	if err = validateClientAPIKeyMetadata(h.cfg.APIKeys, next); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.cfg.APIKeyMetadata = next
	h.persistLocked(c)
}

// PatchAPIKeyProfile updates selected metadata fields for one client API key.
func (h *Handler) PatchAPIKeyProfile(c *gin.Context) {
	var input apiKeyProfileInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if input.Index == nil || (input.ID == nil && input.Alias == nil && input.Disabled == nil) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing fields"})
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if *input.Index < 0 || *input.Index >= len(h.cfg.APIKeys) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid index"})
		return
	}
	key := clientAPIKeyMetadataKey(h.cfg.APIKeys[*input.Index])
	if input.ExpectedID != nil && strings.TrimSpace(*input.ExpectedID) != resolvedClientAPIKeyID(key, h.cfg.APIKeyMetadata[key]) {
		c.JSON(http.StatusConflict, gin.H{"error": "API key profile changed; refresh and retry"})
		return
	}

	next := cloneClientAPIKeyMetadata(h.cfg.APIKeyMetadata)
	metadata := next[key]
	if input.ID != nil {
		metadata.ID = strings.TrimSpace(*input.ID)
	}
	if input.Alias != nil {
		metadata.Alias = strings.TrimSpace(*input.Alias)
	}
	if input.Disabled != nil {
		metadata.Disabled = *input.Disabled
	}
	if clientAPIKeyMetadataEmpty(metadata) {
		delete(next, key)
	} else {
		next[key] = metadata
	}
	if err := validateClientAPIKeyMetadata(h.cfg.APIKeys, next); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.cfg.APIKeyMetadata = next
	h.persistLocked(c)
}

// DeleteAPIKeyProfile removes metadata without deleting the underlying API key.
func (h *Handler) DeleteAPIKeyProfile(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()

	index, ok := apiKeyProfileDeleteIndex(c, h.cfg.APIKeys, h.cfg.APIKeyMetadata)
	if !ok || index < 0 || index >= len(h.cfg.APIKeys) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing or invalid index or id"})
		return
	}
	key := clientAPIKeyMetadataKey(h.cfg.APIKeys[index])
	if expectedID := strings.TrimSpace(c.Query("expected_id")); expectedID != "" && expectedID != resolvedClientAPIKeyID(key, h.cfg.APIKeyMetadata[key]) {
		c.JSON(http.StatusConflict, gin.H{"error": "API key profile changed; refresh and retry"})
		return
	}
	delete(h.cfg.APIKeyMetadata, key)
	h.persistLocked(c)
}

func readAPIKeyProfileInputs(c *gin.Context) ([]apiKeyProfileInput, error) {
	data, err := c.GetRawData()
	if err != nil {
		return nil, fmt.Errorf("failed to read body")
	}
	var inputs []apiKeyProfileInput
	if err = json.Unmarshal(data, &inputs); err == nil {
		return inputs, nil
	}
	var wrapper struct {
		Profiles []apiKeyProfileInput `json:"api-key-profiles"`
		Items    []apiKeyProfileInput `json:"items"`
	}
	if err = json.Unmarshal(data, &wrapper); err != nil {
		return nil, fmt.Errorf("invalid body")
	}
	if wrapper.Profiles != nil {
		return wrapper.Profiles, nil
	}
	if wrapper.Items != nil {
		return wrapper.Items, nil
	}
	return nil, fmt.Errorf("invalid body")
}

func apiKeyProfileDeleteIndex(c *gin.Context, keys []string, metadata map[string]config.ClientAPIKeyMetadata) (int, bool) {
	if rawIndex := strings.TrimSpace(c.Query("index")); rawIndex != "" {
		index, err := strconv.Atoi(rawIndex)
		return index, err == nil
	}
	if id := strings.TrimSpace(c.Query("id")); id != "" {
		for index, rawKey := range keys {
			key := clientAPIKeyMetadataKey(rawKey)
			if resolvedClientAPIKeyID(key, metadata[key]) == id {
				return index, true
			}
		}
	}
	return 0, false
}

func validateClientAPIKeyMetadata(keys []string, metadata map[string]config.ClientAPIKeyMetadata) error {
	replacements := make([]string, 0, len(keys)*2)
	seenRawKeys := make(map[string]struct{}, len(keys))
	for _, rawKey := range keys {
		key := clientAPIKeyMetadataKey(rawKey)
		if key == "" {
			continue
		}
		if _, exists := seenRawKeys[key]; exists {
			continue
		}
		seenRawKeys[key] = struct{}{}
		replacements = append(replacements, key, "")
	}
	credentialRedactor := strings.NewReplacer(replacements...)
	seenIDs := make(map[string]string, len(keys))
	for _, rawKey := range keys {
		key := clientAPIKeyMetadataKey(rawKey)
		entry := metadata[key]
		explicitID := strings.TrimSpace(entry.ID)
		alias := strings.TrimSpace(entry.Alias)
		if strings.IndexFunc(alias, unicode.IsControl) >= 0 {
			return fmt.Errorf("alias must not contain control characters")
		}
		if (explicitID != "" && credentialRedactor.Replace(explicitID) != explicitID) ||
			(alias != "" && credentialRedactor.Replace(alias) != alias) {
			return fmt.Errorf("profile id and alias must not contain API key material")
		}
		id := resolvedClientAPIKeyID(key, entry)
		if utf8.RuneCountInString(strings.TrimSpace(entry.Alias)) > maxClientAPIKeyAliasLength {
			return fmt.Errorf("alias exceeds %d characters", maxClientAPIKeyAliasLength)
		}
		if key == "" && id == "" {
			continue
		}
		if len(id) > maxClientAPIKeyIDLength || !clientAPIKeyIDPattern.MatchString(id) {
			return fmt.Errorf("invalid id: use 1-%d letters, numbers, dots, underscores, or hyphens", maxClientAPIKeyIDLength)
		}
		if previousKey, exists := seenIDs[id]; exists && previousKey != key {
			return fmt.Errorf("duplicate id %q", id)
		}
		seenIDs[id] = key
	}
	return nil
}

func resolvedClientAPIKeyID(key string, metadata config.ClientAPIKeyMetadata) string {
	if id := strings.TrimSpace(metadata.ID); id != "" {
		return id
	}
	return sdkaccess.FallbackClientKeyID(key)
}

func clientAPIKeyMetadataKey(key string) string {
	return strings.TrimSpace(key)
}

func cloneClientAPIKeyMetadata(source map[string]config.ClientAPIKeyMetadata) map[string]config.ClientAPIKeyMetadata {
	cloned := make(map[string]config.ClientAPIKeyMetadata, len(source))
	for key, metadata := range source {
		cloned[key] = metadata
	}
	return cloned
}

func clientAPIKeyMetadataEmpty(metadata config.ClientAPIKeyMetadata) bool {
	return strings.TrimSpace(metadata.ID) == "" && strings.TrimSpace(metadata.Alias) == "" && !metadata.Disabled
}

func pruneClientAPIKeyMetadata(keys []string, metadata map[string]config.ClientAPIKeyMetadata) {
	if len(metadata) == 0 {
		return
	}
	configured := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		configured[clientAPIKeyMetadataKey(key)] = struct{}{}
	}
	for key, entry := range metadata {
		canonicalKey := clientAPIKeyMetadataKey(key)
		if _, exists := configured[canonicalKey]; !exists {
			delete(metadata, key)
			continue
		}
		if canonicalKey != key {
			if _, exists := metadata[canonicalKey]; !exists {
				metadata[canonicalKey] = entry
			}
			delete(metadata, key)
		}
	}
}

func migrateClientAPIKeyMetadata(metadata map[string]config.ClientAPIKeyMetadata, oldKey, newKey string, keys []string) {
	oldKey = clientAPIKeyMetadataKey(oldKey)
	newKey = clientAPIKeyMetadataKey(newKey)
	if oldKey == newKey {
		return
	}
	oldStillConfigured := false
	newKeyCount := 0
	for _, configuredKey := range keys {
		switch clientAPIKeyMetadataKey(configuredKey) {
		case oldKey:
			oldStillConfigured = true
		case newKey:
			newKeyCount++
		}
	}
	// If the replacement key already existed, both entries now represent the
	// same credential and must keep the destination credential's identity.
	if newKeyCount > 1 {
		if !oldStillConfigured {
			delete(metadata, oldKey)
		}
		return
	}
	entry, exists := metadata[oldKey]
	if !exists {
		entry = config.ClientAPIKeyMetadata{}
	}
	if strings.TrimSpace(entry.ID) == "" {
		// A management PATCH is an explicit key rotation. Preserve the old
		// fallback identity so retained usage remains attached to this profile.
		entry.ID = sdkaccess.FallbackClientKeyID(oldKey)
	}
	if _, destinationExists := metadata[newKey]; !destinationExists {
		metadata[newKey] = entry
	}
	if !oldStillConfigured {
		delete(metadata, oldKey)
	}
}

func migrateReplacedClientAPIKeyMetadata(metadata map[string]config.ClientAPIKeyMetadata, oldKeys, newKeys []string) {
	oldSet := make(map[string]struct{}, len(oldKeys))
	newSet := make(map[string]struct{}, len(newKeys))
	for _, rawKey := range oldKeys {
		key := clientAPIKeyMetadataKey(rawKey)
		if key != "" {
			oldSet[key] = struct{}{}
		}
	}
	for _, rawKey := range newKeys {
		key := clientAPIKeyMetadataKey(rawKey)
		if key != "" {
			newSet[key] = struct{}{}
		}
	}

	removed := make([]string, 0)
	seenRemoved := make(map[string]struct{})
	for _, rawKey := range oldKeys {
		key := clientAPIKeyMetadataKey(rawKey)
		if key == "" {
			continue
		}
		if _, remains := newSet[key]; remains {
			continue
		}
		if _, duplicate := seenRemoved[key]; duplicate {
			continue
		}
		seenRemoved[key] = struct{}{}
		removed = append(removed, key)
	}
	added := make([]string, 0)
	seenAdded := make(map[string]struct{})
	for _, rawKey := range newKeys {
		key := clientAPIKeyMetadataKey(rawKey)
		if key == "" {
			continue
		}
		if _, existed := oldSet[key]; existed {
			continue
		}
		if _, duplicate := seenAdded[key]; duplicate {
			continue
		}
		seenAdded[key] = struct{}{}
		added = append(added, key)
	}

	for index := 0; index < len(removed) && index < len(added); index++ {
		migrateClientAPIKeyMetadata(metadata, removed[index], added[index], newKeys)
	}
}

func maskClientAPIKey(key string) string {
	runes := []rune(key)
	if len(runes) <= 8 {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:4]) + strings.Repeat("*", len(runes)-8) + string(runes[len(runes)-4:])
}
