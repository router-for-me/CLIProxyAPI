package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// codexCacheImportEnv enables importing model slugs from the official Codex
// CLI cache into the routing catalog. Opt-in so default behavior stays
// unchanged.
const codexCacheImportEnv = "CPA_IMPORT_CODEX_CACHE_SLUGS"

// codexCacheImportEnabled reports whether the opt-in import is turned on.
func codexCacheImportEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(codexCacheImportEnv))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// codexCachePath returns the official Codex CLI cache location or "" when
// the home directory cannot be resolved.
func codexCachePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".codex", "models_cache.json")
}

type codexCacheEntry struct {
	Slug           string `json:"slug"`
	DisplayName    string `json:"display_name"`
	Description    string `json:"description"`
	Visibility     string `json:"visibility"`
	SupportedInAPI bool   `json:"supported_in_api"`
}

type codexCacheFile struct {
	Models []codexCacheEntry `json:"models"`
}

// importCodexCacheSlugs appends every slug from the cache that is missing
// from all known model buckets. Entries are synthesized from the first
// existing codex template (gpt-5.6-luna preferred) so capabilities like
// context length, thinking levels and modalities carry over. Returns the
// newly registered slugs.
func importCodexCacheSlugs(s *modelStore, cachePath string) []string {
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil // cache is optional; silent when absent
	}
	if len(data) > maxCodexClientModelsSize {
		log.Warnf("codex-cache import: %s exceeds the size limit, skipping", cachePath)
		return nil
	}
	var cache codexCacheFile
	if err := json.Unmarshal(data, &cache); err != nil {
		log.Warnf("codex-cache import: failed to parse %s: %v", cachePath, err)
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		return nil
	}
	known := make(map[string]bool)
	for _, bucket := range [][]*ModelInfo{s.data.Claude, s.data.Gemini, s.data.Vertex, s.data.AIStudio, s.data.CodexFree, s.data.CodexTeam, s.data.CodexPlus, s.data.CodexPro, s.data.Kimi, s.data.Antigravity, s.data.XAI} {
		for _, m := range bucket {
			if m != nil {
				known[m.ID] = true
			}
		}
	}
	template := codexTemplateEntry(s.data)
	if template == nil {
		return nil
	}
	var added []string
	for _, entry := range cache.Models {
		if entry.Slug == "" || known[entry.Slug] {
			continue
		}
		if entry.Visibility == "hide" || !entry.SupportedInAPI {
			continue
		}
		mi := *template
		mi.ID = entry.Slug
		mi.Object = "model"
		mi.OwnedBy = "openai"
		mi.Type = "openai"
		mi.Created = time.Now().Unix()
		if entry.DisplayName != "" {
			mi.DisplayName = entry.DisplayName
		}
		if entry.Description != "" {
			mi.Description = entry.Description
		}
		// Register in every codex plan bucket: the catalog is a superset and
		// upstream enforces per-account entitlements at request time.
		s.data.CodexFree = append(s.data.CodexFree, &mi)
		s.data.CodexTeam = append(s.data.CodexTeam, &mi)
		s.data.CodexPlus = append(s.data.CodexPlus, &mi)
		s.data.CodexPro = append(s.data.CodexPro, &mi)
		known[entry.Slug] = true
		added = append(added, entry.Slug)
	}
	if len(added) > 0 {
		log.Infof("codex-cache import: registered %d new model slug(s): %v", len(added), added)
	}
	return added
}

// codexTemplateEntry picks the catalog entry cloned for synthesized slugs:
// prefer gpt-5.6-luna, then the first openai-typed codex entry.
func codexTemplateEntry(data *staticModelsJSON) *ModelInfo {
	for _, m := range data.CodexPro {
		if m != nil && m.ID == "gpt-5.6-luna" {
			return m
		}
	}
	for _, m := range data.CodexPro {
		if m != nil && m.Type == "openai" {
			return m
		}
	}
	return nil
}

// maybeImportCodexCache runs the opt-in import when enabled.
func maybeImportCodexCache() {
	if !codexCacheImportEnabled() {
		return
	}
	if path := codexCachePath(); path != "" {
		importCodexCacheSlugs(modelsCatalogStore, path)
	}
}
