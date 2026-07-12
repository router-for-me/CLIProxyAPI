package executor

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// windsurfModelConfig holds the parsed backend model UIDs from the Devin CLI cache.
type windsurfModelConfig struct {
	ModelUIDs      []string
	DefaultModelID string
}

// modelConfigCache caches the model config file with its mtime.
var (
	modelConfigCache      *windsurfModelConfig
	modelConfigCacheMtime int64
	modelConfigCacheMu    sync.RWMutex
)

// defaultWindsurfModels is the hard-coded fallback list used when the Devin CLI
// model_configs_v4.bin is not available.
var defaultWindsurfModels = []string{
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
	"MODEL_PRIVATE_2",
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

// parseWindsurfModelConfig reads the Devin CLI model config cache or returns defaults.
func parseWindsurfModelConfig() *windsurfModelConfig {
	home, err := os.UserHomeDir()
	if err != nil {
		return defaultWindsurfModelConfig()
	}
	path := filepath.Join(home, ".cache", "devin", "cli", "model_configs_v4.bin")

	stat, err := os.Stat(path)
	if err != nil {
		return defaultWindsurfModelConfig()
	}

	modelConfigCacheMu.RLock()
	if modelConfigCache != nil && modelConfigCacheMtime == stat.ModTime().UnixMilli() {
		cached := modelConfigCache
		modelConfigCacheMu.RUnlock()
		return cached
	}
	modelConfigCacheMu.RUnlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return defaultWindsurfModelConfig()
	}

	uids := extractModelUIDs(data)
	if len(uids) == 0 {
		return defaultWindsurfModelConfig()
	}

	defaultModelID := "glm-5-2"
	if !contains(uids, defaultModelID) {
		defaultModelID = uids[0]
	}

	cfg := &windsurfModelConfig{
		ModelUIDs:      uids,
		DefaultModelID: defaultModelID,
	}

	modelConfigCacheMu.Lock()
	modelConfigCache = cfg
	modelConfigCacheMtime = stat.ModTime().UnixMilli()
	modelConfigCacheMu.Unlock()

	return cfg
}

func defaultWindsurfModelConfig() *windsurfModelConfig {
	return &windsurfModelConfig{
		ModelUIDs:      defaultWindsurfModels,
		DefaultModelID: "glm-5-2",
	}
}

// extractModelUIDs extracts readable strings that look like backend UIDs.
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

// mapOpenAIModelToWindsurf maps an OpenAI-style model ID (e.g. "devin/glm-5.2")
// to the backend Windsurf model UID.
func mapOpenAIModelToWindsurf(model string, cfg *windsurfModelConfig) string {
	base := baseModelUID(model, cfg.DefaultModelID)
	return withReasoningVariant(base, model, cfg)
}

func baseModelUID(model, defaultModelID string) string {
	model = strings.TrimSpace(model)
	switch model {
	case "devin/glm-5.2":
		return "glm-5-2"
	case "devin/glm-5.1":
		return "glm-5-1"
	case "devin/default":
		return defaultModelID
	case "devin/gpt-5.5", "gpt-5.5":
		return "gpt-5-5"
	case "devin/gpt-5.4", "gpt-5.4":
		return "gpt-5-4"
	case "devin/gpt-5.4-mini", "gpt-5.4-mini":
		return "gpt-5-4-mini"
	case "devin/gpt-5.3-codex", "gpt-5.3-codex-spark":
		return "gpt-5-3-codex"
	case "devin/gpt-5.2":
		return "gpt-5-2"
	case "devin/claude-opus-4.8":
		return "claude-opus-4-8"
	case "devin/claude-fable-5":
		return "claude-5-fable"
	case "devin/claude-sonnet-5":
		return "claude-sonnet-5"
	case "devin/claude-opus-4.7":
		return "claude-opus-4-7"
	case "devin/claude-opus-4.6":
		return "claude-opus-4-6"
	case "devin/claude-opus-4.5":
		return "claude-opus-4-5"
	case "devin/claude-sonnet-4.6":
		return "claude-sonnet-4-6"
	case "devin/claude-sonnet-4.5":
		return "claude-sonnet-4-5"
	case "devin/claude-haiku-4.5":
		return "MODEL_PRIVATE_11"
	case "devin/gemini-3.5-flash":
		return "gemini-3-5-flash"
	case "devin/gemini-3-pro":
		return "gemini-3.1-pro"
	case "devin/gemini-3-flash":
		return "gemini-3.0-flash"
	case "devin/swe-1.6":
		return "swe-1-6"
	case "devin/swe-1.5":
		return "MODEL_SWE_1_5_SLOW"
	case "devin/kimi-k2.7":
		return "kimi-k2-7"
	case "devin/kimi-k2.6":
		return "kimi-k2-6"
	case "devin/deepseek-v4":
		return "deepseek-v4"
	}
	if strings.HasPrefix(model, "devin/") {
		return strings.TrimPrefix(model, "devin/")
	}
	return defaultModelID
}

// withReasoningVariant applies reasoning/service-tier variants. For the minimal
// implementation we keep the base UID. Full variant logic can be layered later.
func withReasoningVariant(base, model string, cfg *windsurfModelConfig) string {
	if contains(cfg.ModelUIDs, base) {
		return base
	}
	// Try a few normalizations.
	candidates := []string{
		base,
		strings.ReplaceAll(base, ".", "-"),
		strings.Replace(base, "-(", "(", 1),
	}
	for _, c := range candidates {
		if contains(cfg.ModelUIDs, c) {
			return c
		}
	}
	return base
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// readStringFields extracts UTF-8 strings from arbitrary bytes. This is a simple
// heuristic matching the TypeScript implementation used to parse model_configs_v4.bin.
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

// WindsurfModelIDs returns the OpenAI-facing model IDs exposed by the Windsurf executor.
func WindsurfModelIDs() []string {
	return []string{
		"devin/glm-5.2",
		"devin/glm-5.1",
		"devin/gpt-5.5",
		"devin/gpt-5.4",
		"devin/gpt-5.4-mini",
		"devin/gpt-5.3-codex",
		"devin/gpt-5.2",
		"devin/claude-opus-4.8",
		"devin/claude-fable-5",
		"devin/claude-sonnet-5",
		"devin/claude-opus-4.7",
		"devin/claude-opus-4.6",
		"devin/claude-opus-4.5",
		"devin/claude-sonnet-4.6",
		"devin/claude-sonnet-4.5",
		"devin/claude-haiku-4.5",
		"devin/gemini-3.5-flash",
		"devin/gemini-3-pro",
		"devin/gemini-3-flash",
		"devin/swe-1.6",
		"devin/swe-1.5",
		"devin/kimi-k2.7",
		"devin/kimi-k2.6",
		"devin/deepseek-v4",
		"devin/default",
	}
}
