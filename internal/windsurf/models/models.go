// Package models reads the Devin CLI model config cache and exposes the
// OpenAI-facing model IDs for the Windsurf / Devin provider.
package models

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Config holds the parsed backend model UIDs from the Devin CLI cache.
type Config struct {
	ModelUIDs      []string
	DefaultModelID string
}

var (
	configCache      *Config
	configCacheMtime int64
	configCacheMu    sync.RWMutex
)

// defaultModelUIDs is the hard-coded fallback list used when the Devin CLI
// model_configs_v4.bin is not available.
var defaultModelUIDs = []string{
	"glm-5-2",
	"glm-5-1",
	"gpt-5-5",
	"gpt-5-4",
	"gpt-5-4-mini",
	"gpt-5-3-codex",
	"gpt-5-2",
	"claude-opus-4-8",
	"claude-5-fable",
	"claude-sonnet-5",
	"claude-opus-4-7",
	"claude-opus-4-6",
	"claude-opus-4-5",
	"claude-sonnet-4-6",
	"claude-sonnet-4-5",
	"MODEL_PRIVATE_11",
	"gemini-3-5-flash",
	"gemini-3.1-pro",
	"gemini-3.0-flash",
	"swe-1-6",
	"MODEL_SWE_1_5_SLOW",
	"kimi-k2-7",
	"kimi-k2-6",
	"deepseek-v4",
}

// modelUIDRegex is used to detect valid backend UIDs inside the binary config.
var modelUIDRegex = regexp.MustCompile(`^(?:glm|gpt|claude|swe|gemini|o[0-9]|codex|llama|mistral|kimi|deepseek|adaptive|subagent|opus|MODEL_)[a-zA-Z0-9_.-]*$`)

// ParseConfig reads the Devin CLI model config cache or returns defaults.
func ParseConfig(path string) *Config {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return defaultConfig()
		}
		path = filepath.Join(home, ".cache", "devin", "cli", "model_configs_v4.bin")
	}

	stat, err := os.Stat(path)
	if err != nil {
		return defaultConfig()
	}

	configCacheMu.RLock()
	if configCache != nil && configCacheMtime == stat.ModTime().UnixMilli() {
		cached := configCache
		configCacheMu.RUnlock()
		return cached
	}
	configCacheMu.RUnlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return defaultConfig()
	}

	uids := extractModelUIDs(data)
	if len(uids) == 0 {
		return defaultConfig()
	}

	defaultModelID := "glm-5-2"
	if !contains(uids, defaultModelID) {
		defaultModelID = uids[0]
	}

	cfg := &Config{
		ModelUIDs:      uids,
		DefaultModelID: defaultModelID,
	}

	configCacheMu.Lock()
	configCache = cfg
	configCacheMtime = stat.ModTime().UnixMilli()
	configCacheMu.Unlock()

	return cfg
}

func defaultConfig() *Config {
	return &Config{
		ModelUIDs:      defaultModelUIDs,
		DefaultModelID: "glm-5-2",
	}
}

// WindsurfModelIDs returns the OpenAI-facing model IDs exposed by the Windsurf executor.
// It first tries to read the Devin CLI model config cache, falling back to a hard-coded list.
func WindsurfModelIDs() []string {
	cfg := ParseConfig("")
	if len(cfg.ModelUIDs) == 0 {
		return defaultModelIDs()
	}

	seen := make(map[string]struct{})
	var ids []string
	for _, uid := range cfg.ModelUIDs {
		id := "devin/" + uid
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	// Always expose the default alias.
	if _, ok := seen["devin/default"]; !ok {
		ids = append(ids, "devin/default")
	}
	return ids
}

func defaultModelIDs() []string {
	ids := make([]string, 0, len(defaultModelUIDs))
	for _, uid := range defaultModelUIDs {
		ids = append(ids, "devin/"+uid)
	}
	ids = append(ids, "devin/default")
	return ids
}

func extractModelUIDs(data []byte) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, s := range readStringFields(data) {
		if isModelUID(s) {
			lower := s
			if _, ok := seen[lower]; !ok {
				seen[lower] = struct{}{}
				out = append(out, lower)
			}
		}
	}
	return out
}

func isModelUID(s string) bool {
	if len(s) < 2 {
		return false
	}
	if !strings.Contains(s, "-") && !strings.HasPrefix(s, "MODEL_") {
		return false
	}
	return modelUIDRegex.MatchString(s)
}

func readStringFields(data []byte) []string {
	var out []string
	var current []byte
	for _, b := range data {
		if b == 0x09 || b == 0x0a || b == 0x0d || (b >= 0x20 && b <= 0x7e) {
			current = append(current, b)
			continue
		}
		if len(current) >= 2 {
			s := string(current)
			if isReadableString(s) {
				out = append(out, s)
			}
		}
		current = current[:0]
	}
	if len(current) >= 2 {
		s := string(current)
		if isReadableString(s) {
			out = append(out, s)
		}
	}
	return out
}

func isReadableString(s string) bool {
	return regexp.MustCompile(`^[\x09\x0a\x0d\x20-\x7e]{2,}$`).MatchString(s)
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
