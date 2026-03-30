package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/geminicli"
)

var compactMetadataScalarKeys = map[string]struct{}{
	"type":                     {},
	"email":                    {},
	"project_id":               {},
	"access_token":             {},
	"refresh_token":            {},
	"accessToken":              {},
	"refreshToken":             {},
	"token_type":               {},
	"tokenType":                {},
	"id_token":                 {},
	"cookie":                   {},
	"disabled":                 {},
	"prefix":                   {},
	"proxy_url":                {},
	"priority":                 {},
	"note":                     {},
	"request_retry":            {},
	"request-retry":            {},
	"disable_cooling":          {},
	"disable-cooling":          {},
	"last_refresh":             {},
	"lastRefresh":              {},
	"last_refreshed_at":        {},
	"lastRefreshedAt":          {},
	"refresh_interval":         {},
	"refreshInterval":          {},
	"refresh_interval_seconds": {},
	"refreshIntervalSeconds":   {},
	"expired":                  {},
	"expire":                   {},
	"expires_at":               {},
	"expiresAt":                {},
	"expiry":                   {},
	"expires":                  {},
	"device_id":                {},
	"account_id":               {},
	"api_key":                  {},
	"tool_prefix_disabled":     {},
	"tool-prefix-disabled":     {},
	"websockets":               {},
	"virtual":                  {},
	"virtual_parent_id":        {},
}

var compactMetadataObjectKeys = map[string]struct{}{
	"token": {},
}

var compactMetadataCollectionKeys = map[string]struct{}{
	"excluded_models": {},
	"excluded-models": {},
}

var compactMetadataClearableKeys = map[string]struct{}{
	"note":            {},
	"priority":        {},
	"request_retry":   {},
	"request-retry":   {},
	"disable_cooling": {},
	"disable-cooling": {},
	"prefix":          {},
	"proxy_url":       {},
	"websockets":      {},
	"excluded_models": {},
	"excluded-models": {},
}

// CompactMetadataForMemory keeps only lightweight routing and scheduling fields in memory.
func CompactMetadataForMemory(meta map[string]any) map[string]any {
	if len(meta) == 0 {
		return nil
	}

	compact := make(map[string]any)
	for key, value := range meta {
		if _, ok := compactMetadataScalarKeys[key]; ok {
			switch typed := value.(type) {
			case string:
				compact[key] = strings.TrimSpace(typed)
			case bool, float64, int, int32, int64, json.Number:
				compact[key] = typed
			}
			continue
		}
		if _, ok := compactMetadataObjectKeys[key]; ok {
			if copied := cloneCompactObject(value); copied != nil {
				compact[key] = copied
			}
			continue
		}
		if _, ok := compactMetadataCollectionKeys[key]; ok {
			if copied := cloneCompactCollection(value); copied != nil {
				compact[key] = copied
			}
		}
	}

	if _, ok := compact["type"]; !ok {
		if provider, okType := meta["type"].(string); okType {
			compact["type"] = strings.TrimSpace(provider)
		}
	}

	if !hasCompactExpiry(compact) {
		if expiry, okExpiry := expirationFromMap(meta); okExpiry && !expiry.IsZero() {
			compact["expiry"] = expiry.UTC().Format(time.RFC3339)
		}
	}

	if len(compact) == 0 {
		return nil
	}
	return compact
}

// PrepareFileBackedAuthForMemory strips heavyweight file-backed state from the in-memory snapshot.
func PrepareFileBackedAuthForMemory(auth *Auth) *Auth {
	if auth == nil {
		return nil
	}
	clone := auth.Clone()
	if !clone.canDeferFileHydration() {
		return clone
	}
	clone.Metadata = CompactMetadataForMemory(clone.Metadata)
	if !clone.shouldRetainRuntimeOnDeferredSnapshot() {
		clone.Runtime = nil
	}
	clone.Storage = nil
	clone.deferredFileHydration = true
	return clone
}

func (a *Auth) shouldRetainRuntimeOnDeferredSnapshot() bool {
	if a == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(a.Provider), "gemini-cli") {
		return false
	}
	if len(a.Attributes) == 0 {
		return false
	}
	if parentID := strings.TrimSpace(a.Attributes["gemini_virtual_parent"]); parentID != "" {
		return true
	}
	if marker := strings.TrimSpace(a.Attributes["gemini_virtual_primary"]); marker != "" {
		enabled, err := strconv.ParseBool(marker)
		return err == nil && enabled
	}
	return false
}

func cloneCompactCollection(value any) any {
	switch typed := value.(type) {
	case []string:
		return compactStringSlice(typed)
	case []interface{}:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			if raw, ok := item.(string); ok {
				items = append(items, raw)
			}
		}
		return compactStringSlice(items)
	default:
		return nil
	}
}

func cloneCompactObject(value any) any {
	switch typed := value.(type) {
	case string:
		if trimmed := strings.TrimSpace(typed); trimmed != "" {
			return trimmed
		}
		return nil
	case map[string]any:
		return cloneStringAnyMap(typed)
	case map[string]string:
		if len(typed) == 0 {
			return nil
		}
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out[key] = trimmed
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

func cloneStringAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		if raw, ok := value.(string); ok {
			if trimmed := strings.TrimSpace(raw); trimmed != "" {
				out[key] = trimmed
			}
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func compactStringSlice(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func hasCompactExpiry(meta map[string]any) bool {
	for _, key := range expireKeys {
		if _, ok := meta[key]; ok {
			return true
		}
	}
	return false
}

func (a *Auth) MarkDeferredFileHydration() {
	if a == nil {
		return
	}
	a.deferredFileHydration = true
}

func (a *Auth) ClearDeferredFileHydration() {
	if a == nil {
		return
	}
	a.deferredFileHydration = false
}

func (a *Auth) DeferredFileHydration() bool {
	if a == nil {
		return false
	}
	return a.deferredFileHydration
}

func (a *Auth) canDeferFileHydration() bool {
	if a == nil {
		return false
	}
	return strings.TrimSpace(a.authFilePath()) != ""
}

func (a *Auth) authFilePath() string {
	if a == nil {
		return ""
	}
	if a.Attributes != nil {
		if path := strings.TrimSpace(a.Attributes["path"]); path != "" {
			return path
		}
	}
	if fileName := strings.TrimSpace(a.FileName); fileName != "" && filepath.IsAbs(fileName) {
		return fileName
	}
	return ""
}

func (a *Auth) hydrateFileBackedState() error {
	if a == nil || !a.deferredFileHydration {
		return nil
	}
	path := a.authFilePath()
	if path == "" {
		return fmt.Errorf("auth file path missing for %s", a.ID)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read auth file: %w", err)
	}
	if len(body) == 0 {
		return fmt.Errorf("auth file is empty: %s", path)
	}

	fullMetadata := make(map[string]any)
	if err = json.Unmarshal(body, &fullMetadata); err != nil {
		return fmt.Errorf("parse auth file: %w", err)
	}

	a.Metadata = fullMetadata
	a.Runtime = rebuildDeferredRuntime(a, fullMetadata)
	a.Storage = nil
	a.deferredFileHydration = false
	return nil
}

func rebuildDeferredRuntime(auth *Auth, metadata map[string]any) any {
	if auth == nil {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "gemini-cli") {
		return nil
	}

	if auth.Attributes != nil {
		if parentID := strings.TrimSpace(auth.Attributes["gemini_virtual_parent"]); parentID != "" {
			projectID := strings.TrimSpace(auth.Attributes["gemini_virtual_project"])
			if projectID == "" {
				projectID, _ = auth.Metadata["project_id"].(string)
				projectID = strings.TrimSpace(projectID)
			}
			shared := geminicli.NewSharedCredential(parentID, metadataString(metadata, "email"), metadata, splitDeferredGeminiProjects(metadata))
			return geminicli.NewVirtualCredential(projectID, shared)
		}
		if strings.EqualFold(strings.TrimSpace(auth.Attributes["gemini_virtual_primary"]), "true") {
			return geminicli.NewSharedCredential(auth.ID, metadataString(metadata, "email"), metadata, splitDeferredGeminiProjects(metadata))
		}
	}

	return nil
}

func metadataString(meta map[string]any, key string) string {
	if meta == nil {
		return ""
	}
	value, _ := meta[key].(string)
	return strings.TrimSpace(value)
}

func splitDeferredGeminiProjects(metadata map[string]any) []string {
	raw, _ := metadata["project_id"].(string)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		projectID := strings.TrimSpace(part)
		if projectID == "" {
			continue
		}
		if _, ok := seen[projectID]; ok {
			continue
		}
		seen[projectID] = struct{}{}
		out = append(out, projectID)
	}
	return out
}
