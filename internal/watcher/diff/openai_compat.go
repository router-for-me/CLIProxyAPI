package diff

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// DiffOpenAICompatibility produces human-readable change descriptions.
func DiffOpenAICompatibility(oldList, newList []config.OpenAICompatibility) []string {
	changes := make([]string, 0)
	oldMap := make(map[string]config.OpenAICompatibility, len(oldList))
	oldLabels := make(map[string]string, len(oldList))
	for idx, entry := range oldList {
		key, label := openAICompatKey(entry, idx)
		oldMap[key] = entry
		oldLabels[key] = label
	}
	newMap := make(map[string]config.OpenAICompatibility, len(newList))
	newLabels := make(map[string]string, len(newList))
	for idx, entry := range newList {
		key, label := openAICompatKey(entry, idx)
		newMap[key] = entry
		newLabels[key] = label
	}
	keySet := make(map[string]struct{}, len(oldMap)+len(newMap))
	for key := range oldMap {
		keySet[key] = struct{}{}
	}
	for key := range newMap {
		keySet[key] = struct{}{}
	}
	orderedKeys := make([]string, 0, len(keySet))
	for key := range keySet {
		orderedKeys = append(orderedKeys, key)
	}
	sort.Strings(orderedKeys)
	for _, key := range orderedKeys {
		oldEntry, oldOk := oldMap[key]
		newEntry, newOk := newMap[key]
		label := oldLabels[key]
		if label == "" {
			label = newLabels[key]
		}
		switch {
		case !oldOk:
			changes = append(changes, fmt.Sprintf("provider added: %s (api-keys=%d, models=%d)", label, countAPIKeys(newEntry), countOpenAIModels(newEntry.Models)))
		case !newOk:
			changes = append(changes, fmt.Sprintf("provider removed: %s (api-keys=%d, models=%d)", label, countAPIKeys(oldEntry), countOpenAIModels(oldEntry.Models)))
		default:
			if detail := describeOpenAICompatibilityUpdate(oldEntry, newEntry); detail != "" {
				changes = append(changes, fmt.Sprintf("provider updated: %s %s", label, detail))
			}
		}
	}
	return changes
}

func describeOpenAICompatibilityUpdate(oldEntry, newEntry config.OpenAICompatibility) string {
	oldKeyCount := countAPIKeys(oldEntry)
	newKeyCount := countAPIKeys(newEntry)
	oldModelCount := countOpenAIModels(oldEntry.Models)
	newModelCount := countOpenAIModels(newEntry.Models)
	details := make([]string, 0, 3)
	if oldKeyCount != newKeyCount {
		details = append(details, fmt.Sprintf("api-keys %d -> %d", oldKeyCount, newKeyCount))
	}
	if oldModelCount != newModelCount {
		details = append(details, fmt.Sprintf("models %d -> %d", oldModelCount, newModelCount))
	}
	if !equalStringMap(oldEntry.Headers, newEntry.Headers) {
		details = append(details, "headers updated")
	}
	if len(details) == 0 {
		return ""
	}
	return "(" + strings.Join(details, ", ") + ")"
}

func countAPIKeys(entry config.OpenAICompatibility) int {
	count := 0
	for _, keyEntry := range entry.APIKeyEntries {
		if strings.TrimSpace(keyEntry.APIKey) != "" {
			count++
		}
	}
	return count
}

func countOpenAIModels(models []config.OpenAICompatibilityModel) int {
	count := 0
	for _, model := range models {
		name := strings.TrimSpace(model.Name)
		alias := strings.TrimSpace(model.Alias)
		if name == "" && alias == "" {
			continue
		}
		count++
	}
	return count
}

func openAICompatKey(entry config.OpenAICompatibility, index int) (string, string) {
	name := strings.TrimSpace(entry.Name)
	if name != "" {
		return "name:" + name, name
	}
	base := strings.TrimSpace(entry.BaseURL)
	if base != "" {
		return "base:" + base, base
	}
	for _, model := range entry.Models {
		alias := strings.TrimSpace(model.Alias)
		if alias == "" {
			alias = strings.TrimSpace(model.Name)
		}
		if alias != "" {
			return "alias:" + alias, alias
		}
	}
	sig := openAICompatSignature(entry)
	if sig == "" {
		return fmt.Sprintf("index:%d", index), fmt.Sprintf("entry-%d", index+1)
	}
	short := sig
	if len(short) > 8 {
		short = short[:8]
	}
	return "sig:" + sig, "compat-" + short
}

func openAICompatSignature(entry config.OpenAICompatibility) string {
	var parts []string

	if v := strings.TrimSpace(entry.Name); v != "" {
		parts = append(parts, "name="+strings.ToLower(v))
	}
	if v := strings.TrimSpace(entry.BaseURL); v != "" {
		parts = append(parts, "base="+v)
	}

	models := make([]string, 0, len(entry.Models))
	for _, model := range entry.Models {
		name := strings.TrimSpace(model.Name)
		alias := strings.TrimSpace(model.Alias)
		if name == "" && alias == "" {
			continue
		}
		models = append(models, strings.ToLower(name)+"|"+strings.ToLower(alias))
	}
	if len(models) > 0 {
		sort.Strings(models)
		parts = append(parts, "models="+strings.Join(models, ","))
	}

	if len(entry.Headers) > 0 {
		keys := make([]string, 0, len(entry.Headers))
		for k := range entry.Headers {
			if trimmed := strings.TrimSpace(k); trimmed != "" {
				keys = append(keys, strings.ToLower(trimmed))
			}
		}
		if len(keys) > 0 {
			sort.Strings(keys)
			parts = append(parts, "headers="+strings.Join(keys, ","))
		}
	}

	// Intentionally exclude API key material; only count non-empty entries.
	if count := countAPIKeys(entry); count > 0 {
		parts = append(parts, fmt.Sprintf("api_keys=%d", count))
	}

	if len(parts) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}
