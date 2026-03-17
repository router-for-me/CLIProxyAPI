package diff

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

type OAuthModelAliasSummary struct {
	hash  string
	count int
}

// SummarizeOAuthModelAlias summarizes OAuth model alias per channel.
func SummarizeOAuthModelAlias(entries map[string][]config.OAuthModelAlias) map[string]OAuthModelAliasSummary {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]OAuthModelAliasSummary, len(entries))
	for k, v := range entries {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "" {
			continue
		}
		out[key] = summarizeOAuthModelAliasList(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// DiffOAuthModelAliasChanges compares OAuth model alias maps.
func DiffOAuthModelAliasChanges(oldMap, newMap map[string][]config.OAuthModelAlias) ([]string, []string) {
	oldSummary := SummarizeOAuthModelAlias(oldMap)
	newSummary := SummarizeOAuthModelAlias(newMap)
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
			changes = append(changes, fmt.Sprintf("oauth-model-alias[%s]: removed", key))
			affected = append(affected, key)
		case !okOld && okNew:
			changes = append(changes, fmt.Sprintf("oauth-model-alias[%s]: added (%d entries)", key, newInfo.count))
			affected = append(affected, key)
		case okOld && okNew && oldInfo.hash != newInfo.hash:
			changes = append(changes, fmt.Sprintf("oauth-model-alias[%s]: updated (%d -> %d entries)", key, oldInfo.count, newInfo.count))
			affected = append(affected, key)
		}
	}
	sort.Strings(changes)
	sort.Strings(affected)
	return changes, affected
}

func summarizeOAuthModelAliasList(list []config.OAuthModelAlias) OAuthModelAliasSummary {
	if len(list) == 0 {
		return OAuthModelAliasSummary{}
	}
	seen := make(map[string]struct{}, len(list))
	normalized := make([]string, 0, len(list))
	for _, alias := range list {
		name := strings.ToLower(strings.TrimSpace(alias.Name))
		aliasVal := strings.ToLower(strings.TrimSpace(alias.Alias))
		if name == "" || aliasVal == "" {
			continue
		}
		key := name + "->" + aliasVal
		if alias.Fork {
			key += "|fork"
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}
	if len(normalized) == 0 {
		return OAuthModelAliasSummary{}
	}
	sort.Strings(normalized)
	sum := sha256.Sum256([]byte(strings.Join(normalized, "|")))
	return OAuthModelAliasSummary{
		hash:  hex.EncodeToString(sum[:]),
		count: len(normalized),
	}
}
