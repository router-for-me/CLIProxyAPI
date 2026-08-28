package diff

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type OAuthRequestScopedErrorsSummary struct {
	hash  string
	count int
}

// SummarizeOAuthRequestScopedErrors summarizes OAuth request-scoped errors per channel.
func SummarizeOAuthRequestScopedErrors(entries map[string][]config.RequestScopedErrorRule) map[string]OAuthRequestScopedErrorsSummary {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]OAuthRequestScopedErrorsSummary, len(entries))
	for k, v := range entries {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "" {
			continue
		}
		out[key] = summarizeOAuthRequestScopedErrorsList(v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// DiffOAuthRequestScopedErrorsChanges compares OAuth request-scoped error maps.
func DiffOAuthRequestScopedErrorsChanges(oldMap, newMap map[string][]config.RequestScopedErrorRule) ([]string, []string) {
	oldSummary := SummarizeOAuthRequestScopedErrors(oldMap)
	newSummary := SummarizeOAuthRequestScopedErrors(newMap)
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
			changes = append(changes, fmt.Sprintf("oauth-request-scoped-errors[%s]: removed", key))
			affected = append(affected, key)
		case !okOld && okNew:
			changes = append(changes, fmt.Sprintf("oauth-request-scoped-errors[%s]: added (%d entries)", key, newInfo.count))
			affected = append(affected, key)
		case okOld && okNew && oldInfo.hash != newInfo.hash:
			changes = append(changes, fmt.Sprintf("oauth-request-scoped-errors[%s]: updated (%d -> %d entries)", key, oldInfo.count, newInfo.count))
			affected = append(affected, key)
		}
	}
	sort.Strings(changes)
	sort.Strings(affected)
	return changes, affected
}

func summarizeOAuthRequestScopedErrorsList(list []config.RequestScopedErrorRule) OAuthRequestScopedErrorsSummary {
	if len(list) == 0 {
		return OAuthRequestScopedErrorsSummary{}
	}
	var b strings.Builder
	valid := 0
	for _, entry := range list {
		if entry.Status <= 0 || (len(entry.Match) == 0 && len(entry.MatchRegexr) == 0) || entry.Action == "" {
			continue
		}
		valid++
		b.WriteString(fmt.Sprintf("%d|%s|%s|%s\n", entry.Status, strings.Join(entry.Match, ","), strings.Join(entry.MatchRegexr, ","), entry.Action))
	}
	if valid == 0 {
		return OAuthRequestScopedErrorsSummary{}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return OAuthRequestScopedErrorsSummary{
		hash:  hex.EncodeToString(sum[:]),
		count: valid,
	}
}
