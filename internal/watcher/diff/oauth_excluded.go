package diff

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

type ExcludedModelsSummary struct {
	hash  string
	count int
}

// SummarizeExcludedModels normalizes and hashes an excluded-model list.
func SummarizeExcludedModels(list []string) ExcludedModelsSummary {
	if len(list) == 0 {
		return ExcludedModelsSummary{}
	}
	seen := make(map[string]struct{}, len(list))
	normalized := make([]string, 0, len(list))
	for _, entry := range list {
		if trimmed := strings.ToLower(strings.TrimSpace(entry)); trimmed != "" {
			if _, exists := seen[trimmed]; exists {
				continue
			}
			seen[trimmed] = struct{}{}
			normalized = append(normalized, trimmed)
		}
	}
	sort.Strings(normalized)
	return ExcludedModelsSummary{
		hash:  ComputeExcludedModelsHash(normalized),
		count: len(normalized),
	}
}

// SummarizeOAuthExcludedModels summarizes OAuth excluded models per provider.
func SummarizeOAuthExcludedModels(entries map[string][]string) map[string]ExcludedModelsSummary {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]ExcludedModelsSummary, len(entries))
	for k, v := range entries {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "" {
			continue
		}
		out[key] = SummarizeExcludedModels(v)
	}
	return out
}

// DiffOAuthExcludedModelChanges compares OAuth excluded models maps.
func DiffOAuthExcludedModelChanges(oldMap, newMap map[string][]string) ([]string, []string) {
	oldSummary := SummarizeOAuthExcludedModels(oldMap)
	newSummary := SummarizeOAuthExcludedModels(newMap)
	keys := make(map[string]struct{}, len(oldSummary)+len(newSummary))
	for k := range oldSummary {
		keys[k] = struct{}{}
	}
	for k := range newSummary {
		keys[k] = struct{}{}
	}
	changes := make([]string, 0, len(keys))
	affected := make([]string, 0, len(keys))
	for key := range keys {
		oldInfo, okOld := oldSummary[key]
		newInfo, okNew := newSummary[key]
		switch {
		case okOld && !okNew:
			changes = append(changes, fmt.Sprintf("oauth-excluded-models[%s]: removed", key))
			affected = append(affected, key)
		case !okOld && okNew:
			changes = append(changes, fmt.Sprintf("oauth-excluded-models[%s]: added (%d entries)", key, newInfo.count))
			affected = append(affected, key)
		case okOld && okNew && oldInfo.hash != newInfo.hash:
			changes = append(changes, fmt.Sprintf("oauth-excluded-models[%s]: updated (%d -> %d entries)", key, oldInfo.count, newInfo.count))
			affected = append(affected, key)
		}
	}
	sort.Strings(changes)
	sort.Strings(affected)
	return changes, affected
}

type AmpModelMappingsSummary struct {
	hash  string
	count int
}

// SummarizeAmpModelMappings hashes Amp model mappings for change detection.
func SummarizeAmpModelMappings(mappings []config.AmpModelMapping) AmpModelMappingsSummary {
	if len(mappings) == 0 {
		return AmpModelMappingsSummary{}
	}
	entries := make([]string, 0, len(mappings))
	for _, mapping := range mappings {
		from := strings.TrimSpace(mapping.From)
		to := strings.TrimSpace(mapping.To)
		if from == "" && to == "" {
			continue
		}
		entries = append(entries, from+"->"+to)
	}
	if len(entries) == 0 {
		return AmpModelMappingsSummary{}
	}
	sort.Strings(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "|")))
	return AmpModelMappingsSummary{
		hash:  hex.EncodeToString(sum[:]),
		count: len(entries),
	}
}
