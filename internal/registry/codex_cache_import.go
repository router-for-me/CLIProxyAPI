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

type codexCacheReasoningLevel struct {
	Effort string `json:"effort"`
}

type codexCacheEntry struct {
	Slug                     string                     `json:"slug"`
	DisplayName              string                     `json:"display_name"`
	Description              string                     `json:"description"`
	Visibility               string                     `json:"visibility"`
	SupportedInAPI           bool                       `json:"supported_in_api"`
	SupportedReasoningLevels []codexCacheReasoningLevel `json:"supported_reasoning_levels"`
}

type codexCacheFile struct {
	Models []codexCacheEntry `json:"models"`
}

// importCodexCacheSlugs overlays cache-only slugs onto one unpublished
// catalog. Entries are synthesized from the first existing codex template
// (gpt-5.6-luna preferred) so context length and modalities carry over.
// Returns the newly registered slugs.
func importCodexCacheSlugs(data *staticModelsJSON, cachePath string) []string {
	if data == nil {
		return nil
	}
	info, err := os.Stat(cachePath)
	if err != nil {
		return nil // cache is optional; silent when absent
	}
	if info.Size() > maxCodexClientModelsSize {
		log.Warnf("codex-cache import: %s exceeds the size limit, skipping", cachePath)
		return nil
	}
	raw, err := os.ReadFile(cachePath)
	if err != nil {
		return nil
	}
	var cache codexCacheFile
	if err := json.Unmarshal(raw, &cache); err != nil {
		log.Warnf("codex-cache import: failed to parse %s: %v", cachePath, err)
		return nil
	}

	known := make(map[string]bool)
	for _, bucket := range [][]*ModelInfo{data.Claude, data.Gemini, data.Vertex, data.AIStudio, data.CodexFree, data.CodexTeam, data.CodexPlus, data.CodexPro, data.Kimi, data.Antigravity, data.XAI} {
		for _, m := range bucket {
			if m != nil {
				known[m.ID] = true
			}
		}
	}
	template := codexTemplateEntry(data)
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
		base := cloneModelInfo(template)
		base.ID = entry.Slug
		base.Object = "model"
		base.OwnedBy = "openai"
		base.Type = "openai"
		base.Created = time.Now().Unix()
		if entry.DisplayName != "" {
			base.DisplayName = entry.DisplayName
		}
		if entry.Description != "" {
			base.Description = entry.Description
		}
		if levels := cacheReasoningLevels(entry.SupportedReasoningLevels); len(levels) > 0 {
			base.Thinking = &ThinkingSupport{Levels: levels}
		}
		// Register in every codex plan bucket: the catalog is a superset and
		// upstream enforces per-account entitlements at request time.
		data.CodexFree = append(data.CodexFree, cloneModelInfo(base))
		data.CodexTeam = append(data.CodexTeam, cloneModelInfo(base))
		data.CodexPlus = append(data.CodexPlus, cloneModelInfo(base))
		data.CodexPro = append(data.CodexPro, cloneModelInfo(base))
		known[entry.Slug] = true
		added = append(added, entry.Slug)
	}
	return added
}

var codexRequestReasoningLevels = map[string]bool{
	"minimal": true,
	"low":     true,
	"medium":  true,
	"high":    true,
	"xhigh":   true,
	"max":     true,
}

func cacheReasoningLevels(raw []codexCacheReasoningLevel) []string {
	seen := make(map[string]bool)
	levels := make([]string, 0, len(raw))
	for _, level := range raw {
		effort := strings.ToLower(strings.TrimSpace(level.Effort))
		// Cache metadata may lead the request API (for example `ultra`).
		// Publish only levels accepted by internal/thinking's canonical parser.
		if !codexRequestReasoningLevels[effort] || seen[effort] {
			continue
		}
		seen[effort] = true
		levels = append(levels, effort)
	}
	return levels
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

func cloneStaticModelsJSON(data *staticModelsJSON) *staticModelsJSON {
	if data == nil {
		return nil
	}
	return &staticModelsJSON{
		Claude: cloneModelInfos(data.Claude), Gemini: cloneModelInfos(data.Gemini),
		Vertex: cloneModelInfos(data.Vertex), AIStudio: cloneModelInfos(data.AIStudio),
		CodexFree: cloneModelInfos(data.CodexFree), CodexTeam: cloneModelInfos(data.CodexTeam),
		CodexPlus: cloneModelInfos(data.CodexPlus), CodexPro: cloneModelInfos(data.CodexPro),
		Kimi: cloneModelInfos(data.Kimi), Antigravity: cloneModelInfos(data.Antigravity),
		XAI: cloneModelInfos(data.XAI),
	}
}

func overlayCodexCache(data *staticModelsJSON) []string {
	if !codexCacheImportEnabled() {
		return nil
	}
	path := codexCachePath()
	if path == "" {
		return nil
	}
	return importCodexCacheSlugs(data, path)
}

// RefreshCodexCacheOverlay applies the cache to the currently published
// catalog after process-level environment loading (including .env).
func RefreshCodexCacheOverlay() []string {
	if !codexCacheImportEnabled() {
		return nil
	}
	modelsCatalogStore.mu.RLock()
	next := cloneStaticModelsJSON(modelsCatalogStore.data)
	modelsCatalogStore.mu.RUnlock()
	added := overlayCodexCache(next)
	if len(added) == 0 {
		return nil
	}
	modelsCatalogStore.mu.Lock()
	modelsCatalogStore.data = next
	modelsCatalogStore.mu.Unlock()
	return added
}
