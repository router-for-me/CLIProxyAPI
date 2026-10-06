package diff

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

type OAuthSettingsSummary struct {
	hash  string
	count int
}

// SummarizeOAuthSettings summarizes OAuth model settings per channel.
func SummarizeOAuthSettings(entries map[string][]config.OAuthModelSetting) map[string]OAuthSettingsSummary {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]OAuthSettingsSummary, len(entries))
	for k, v := range entries {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "" {
			continue
		}
		out[key] = summarizeOAuthSettingsList(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// DiffOAuthSettingsChanges compares OAuth model settings maps.
func DiffOAuthSettingsChanges(oldMap, newMap map[string][]config.OAuthModelSetting) ([]string, []string) {
	oldSummary := SummarizeOAuthSettings(oldMap)
	newSummary := SummarizeOAuthSettings(newMap)
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
			changes = append(changes, fmt.Sprintf("oauth-settings[%s]: removed", key))
			affected = append(affected, key)
		case !okOld && okNew:
			changes = append(changes, fmt.Sprintf("oauth-settings[%s]: added (%d entries)", key, newInfo.count))
			affected = append(affected, key)
		case okOld && okNew && oldInfo.hash != newInfo.hash:
			changes = append(changes, fmt.Sprintf("oauth-settings[%s]: updated (%d -> %d entries)", key, oldInfo.count, newInfo.count))
			affected = append(affected, key)
		}
	}
	sort.Strings(changes)
	sort.Strings(affected)
	return changes, affected
}

func summarizeOAuthSettingsList(list []config.OAuthModelSetting) OAuthSettingsSummary {
	if len(list) == 0 {
		return OAuthSettingsSummary{}
	}
	seen := make(map[string]struct{}, len(list))
	normalized := make([]string, 0, len(list))
	for _, setting := range list {
		name := strings.ToLower(strings.TrimSpace(setting.Name))
		if name == "" {
			continue
		}
		alias := strings.ToLower(strings.TrimSpace(setting.Alias))
		key := name + "->" + alias
		if setting.MaxContextLength > 0 {
			key += fmt.Sprintf("|max-context-length=%d", setting.MaxContextLength)
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}
	if len(normalized) == 0 {
		return OAuthSettingsSummary{}
	}
	sum := sha256.Sum256([]byte(strings.Join(normalized, "|")))
	return OAuthSettingsSummary{
		hash:  hex.EncodeToString(sum[:]),
		count: len(normalized),
	}
}
