package diff

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

type OAuthModelMappingsSummary struct {
	hash  string
	count int
}

// SummarizeOAuthModelMappings summarizes OAuth model mappings per channel.
func SummarizeOAuthModelMappings(entries map[string][]config.ModelNameMapping) map[string]OAuthModelMappingsSummary {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]OAuthModelMappingsSummary, len(entries))
	for k, v := range entries {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "" {
			continue
		}
		out[key] = summarizeOAuthModelMappingList(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// DiffOAuthModelMappingChanges compares OAuth model mappings maps.
func DiffOAuthModelMappingChanges(oldMap, newMap map[string][]config.ModelNameMapping) ([]string, []string) {
	oldSummary := SummarizeOAuthModelMappings(oldMap)
	newSummary := SummarizeOAuthModelMappings(newMap)
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
			changes = append(changes, fmt.Sprintf("oauth-model-mappings[%s]: removed", key))
			affected = append(affected, key)
		case !okOld && okNew:
			changes = append(changes, fmt.Sprintf("oauth-model-mappings[%s]: added (%d entries)", key, newInfo.count))
			affected = append(affected, key)
		case okOld && okNew && oldInfo.hash != newInfo.hash:
			changes = append(changes, fmt.Sprintf("oauth-model-mappings[%s]: updated (%d -> %d entries)", key, oldInfo.count, newInfo.count))
			affected = append(affected, key)
		}
	}
	sort.Strings(changes)
	sort.Strings(affected)
	return changes, affected
}

func summarizeOAuthModelMappingList(list []config.ModelNameMapping) OAuthModelMappingsSummary {
	if len(list) == 0 {
		return OAuthModelMappingsSummary{}
	}
	seen := make(map[string]struct{}, len(list))
	normalized := make([]string, 0, len(list))
	for _, mapping := range list {
		name := strings.ToLower(strings.TrimSpace(mapping.Name))
		alias := strings.ToLower(strings.TrimSpace(mapping.Alias))
		if name == "" || alias == "" {
			continue
		}
		key := name + "->" + alias
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}
	if len(normalized) == 0 {
		return OAuthModelMappingsSummary{}
	}
	sort.Strings(normalized)
	sum := sha256.Sum256([]byte(strings.Join(normalized, "|")))
	return OAuthModelMappingsSummary{
		hash:  hex.EncodeToString(sum[:]),
		count: len(normalized),
	}
}
