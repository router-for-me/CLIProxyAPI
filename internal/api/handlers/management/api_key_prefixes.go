package management

import (
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

func (h *Handler) pruneAPIKeyPrefixes() {
	keys := make(map[string]struct{}, len(h.cfg.APIKeys))
	for _, key := range h.cfg.APIKeys {
		keys[strings.TrimSpace(key)] = struct{}{}
	}
	for key := range h.cfg.APIKeyPrefixes {
		if _, exists := keys[key]; !exists {
			delete(h.cfg.APIKeyPrefixes, key)
		}
	}
}

// GetAPIKeyPrefixOptions lists configured upstream namespaces, including offline credentials.
func (h *Handler) GetAPIKeyPrefixOptions(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := make(map[string]struct{})
	add := func(prefix string) {
		if prefix != "" {
			seen[prefix] = struct{}{}
		}
	}
	for _, entry := range h.cfg.GeminiKey {
		add(entry.Prefix)
	}
	for _, entry := range h.cfg.InteractionsKey {
		add(entry.Prefix)
	}
	for _, entry := range h.cfg.CodexKey {
		add(entry.Prefix)
	}
	for _, entry := range h.cfg.XAIKey {
		add(entry.Prefix)
	}
	for _, entry := range h.cfg.ClaudeKey {
		add(entry.Prefix)
	}
	for _, entry := range h.cfg.VertexCompatAPIKey {
		add(entry.Prefix)
	}
	for _, entry := range h.cfg.OpenAICompatibility {
		add(entry.Prefix)
	}
	if h.authManager != nil {
		for _, entry := range h.authManager.List() {
			if entry != nil {
				add(entry.Prefix)
			}
		}
	}
	prefixes := make([]string, 0, len(seen))
	for prefix := range seen {
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)
	c.JSON(200, gin.H{"prefixes": prefixes})
}
