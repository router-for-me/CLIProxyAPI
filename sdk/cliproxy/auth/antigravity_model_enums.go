package auth

import (
	"context"
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/antigravitycatalog"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelconfig"
)

const (
	// AntigravityModelEnumHomeScopeAttribute identifies a credential registration across Home dispatches.
	AntigravityModelEnumHomeScopeAttribute = "antigravity_model_enum_scope"
	antigravityModelEnumHomeKey            = "antigravity:model-enums:v1:"
	antigravityModelEnumLocalKey           = "antigravity:model-enums:local:v1:"
	antigravityModelEnumTTL                = 7 * 24 * time.Hour
	antigravityModelEnumMaxBytes           = 256
)

type antigravityModelEnumSnapshot struct {
	Scope             string                    `json:"scope"`
	Registration      string                    `json:"registration"`
	Models            map[string]string         `json:"models"`
	ThinkingGroups    map[string]map[string]int `json:"thinkingGroups,omitempty"`
	ThinkingWebSearch map[string]bool           `json:"thinkingWebSearch,omitempty"`
}

var antigravityModelEnums = cache.NewBoundedLRU[string, *atomic.Pointer[antigravityModelEnumSnapshot]](1024, nil)

func newAntigravityEnumRegistration() string { return rand.Text() }

func antigravityLocalModelEnumScope(auth *Auth) string {
	grant := authRefreshToken(auth)
	if grant == "" {
		grant = authOuterAccessToken(auth)
	}
	// File Synthesizer and FileTokenStore can assign different CreatedAt values
	// to the same OAuth grant. Local epochs also have no meaning after restart.
	return kvstore.HashKeyPart(fmt.Sprintf("%q/%q/%q", grant, authMetadataString(auth, "project_id"), AntigravityModelProbeBaseURLs(auth)[0]))
}

func invalidateLocalAntigravityModelEnums(ctx context.Context, auths ...*Auth) error {
	if len(auths) == 0 || home.Current() != nil {
		return nil
	}
	var keys []string
	for _, auth := range auths {
		if auth.Attributes[AntigravityModelEnumHomeScopeAttribute] == "" {
			keys = append(keys, antigravityModelEnumLocalKey+kvstore.HashKeyPart(auth.ID))
		}
	}
	if len(keys) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, _, errDelete := kvstore.KVDelRequired(ctx, keys...)
	return errDelete
}

// StoreAntigravityModelEnums publishes a managed credential's local snapshot
// only while its registration and probe scope are current.
func (m *Manager) StoreAntigravityModelEnums(ctx context.Context, auth *Auth, models map[string]string) bool {
	return m.StoreAntigravityModelCatalog(ctx, auth, models, nil, nil)
}

// StoreAntigravityModelCatalog publishes enums and thinking groups for the same managed registration.
func (m *Manager) StoreAntigravityModelCatalog(ctx context.Context, auth *Auth, models map[string]string, groups map[string]map[string]int, webSearch map[string]bool) bool {
	if !validAntigravityModelEnumAuth(auth) || home.Current() != nil || auth.Attributes[AntigravityModelEnumHomeScopeAttribute] != "" {
		return false
	}
	releaseMutation, errMutation := m.lockAuthMutationContext(ctx, auth.ID)
	if errMutation != nil {
		return false
	}
	defer releaseMutation()
	current, exists := m.GetByID(auth.ID)
	if !exists || !validAntigravityModelEnumAuth(current) || current.RegistrationEpoch != auth.RegistrationEpoch ||
		antigravityLocalModelEnumScope(current) != antigravityLocalModelEnumScope(auth) {
		return false
	}
	return StoreAntigravityModelCatalog(ctx, current, models, groups, webSearch)
}

// StoreAntigravityModelEnums replaces the local snapshot after a successful
// catalog probe. A nil map only renews an existing matching KV snapshot. A
// non-nil map is immutable after publication. False indicates a write failure.
func StoreAntigravityModelEnums(ctx context.Context, auth *Auth, models map[string]string) bool {
	return StoreAntigravityModelCatalog(ctx, auth, models, nil, nil)
}

// StoreAntigravityModelCatalog stores immutable enums and grouped capabilities as one local snapshot.
// A nil enum map only renews an existing matching snapshot.
func StoreAntigravityModelCatalog(ctx context.Context, auth *Auth, models map[string]string, groups map[string]map[string]int, webSearch map[string]bool) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validAntigravityModelEnumAuth(auth) || home.Current() != nil || auth.Attributes[AntigravityModelEnumHomeScopeAttribute] != "" || ctx.Err() != nil {
		return false
	}
	key := antigravityModelEnumLocalKey + kvstore.HashKeyPart(auth.ID)
	scope := antigravityLocalModelEnumScope(auth)
	client, active, errClient := kvstore.CurrentKVClient()
	if active {
		if errClient != nil {
			return false
		}
		if models == nil {
			snapshot, errRead := readAntigravityModelEnumDoc(ctx, client, key)
			if errRead != nil || !antigravityModelEnumDocMatches(snapshot, scope, auth.antigravityEnumRegistration) {
				return false
			}
			kept, errExpire := client.KVExpire(ctx, key, antigravityModelEnumTTL)
			return kept && errExpire == nil
		}
		doc := ojson.NewDocString("{}")
		doc.Set("scope", scope)
		doc.Set("registration", auth.antigravityEnumRegistration)
		doc.Set("models", models)
		if len(groups) > 0 {
			doc.Set("thinkingGroups", groups)
			if len(webSearch) > 0 {
				doc.Set("thinkingWebSearch", webSearch)
			}
		}
		written, errSet := client.KVSet(ctx, key, doc.Bytes(), kvstore.KVSetOptions{PX: antigravityModelEnumTTL})
		return written && errSet == nil
	}
	if models != nil {
		entry := antigravityModelEnums.GetOrAdd(key, func() *atomic.Pointer[antigravityModelEnumSnapshot] {
			return new(atomic.Pointer[antigravityModelEnumSnapshot])
		})
		entry.Store(&antigravityModelEnumSnapshot{Scope: scope, Registration: auth.antigravityEnumRegistration, Models: models, ThinkingGroups: groups, ThinkingWebSearch: webSearch})
	}
	return true
}

// AntigravityModelEnumSnapshotMissing reports a confirmed local KV miss. It is
// used when a persisted model catalog would otherwise suppress discovery.
func AntigravityModelEnumSnapshotMissing(ctx context.Context, auth *Auth) (bool, error) {
	if !validAntigravityModelEnumAuth(auth) || home.Current() != nil || auth.Attributes[AntigravityModelEnumHomeScopeAttribute] != "" {
		return false, nil
	}
	client, active, errClient := kvstore.CurrentKVClient()
	if !active || errClient != nil {
		return false, errClient
	}
	snapshot, errRead := readAntigravityModelEnumDoc(ctx, client, antigravityModelEnumLocalKey+kvstore.HashKeyPart(auth.ID))
	return errRead == nil && !antigravityModelEnumDocMatches(snapshot, antigravityLocalModelEnumScope(auth), auth.antigravityEnumRegistration), errRead
}

// LookupAntigravityModelEnum returns a previously discovered enum for the exact
// upstream model ID. It never probes the upstream, and misses or unavailable KV
// are reported as unknown.
func LookupAntigravityModelEnum(ctx context.Context, auth *Auth, upstreamModelID string) (string, bool) {
	modelEnum := SelectAntigravityModel(ctx, auth, upstreamModelID, "", nil).Enum
	return modelEnum, modelEnum != ""
}

// AntigravityModelSelection holds an upstream model and its metadata for one attempt.
// A positive MaxOutputTokens indicates a match in the account's thinking groups.
type AntigravityModelSelection struct {
	ID              string
	Enum            string
	MaxOutputTokens int
	WebSearch       bool
}

// SelectAntigravityModel resolves a thinking variant and its enum from a single
// credential-scoped catalog snapshot. An empty or unmatched level keeps the original ID.
func SelectAntigravityModel(ctx context.Context, auth *Auth, upstreamModelID, level string, cfg *config.Config) AntigravityModelSelection {
	selection := AntigravityModelSelection{ID: upstreamModelID}
	if !validAntigravityModelEnumAuth(auth) || upstreamModelID == "" || len(upstreamModelID) > antigravityModelEnumMaxBytes {
		return selection
	}
	if ctx == nil {
		ctx = context.Background()
	}
	level = strings.ToLower(level)
	if !antigravitycatalog.ValidThinkingSuffix(level) {
		level = ""
	}
	var key, scope, registration string
	id := kvstore.HashKeyPart(auth.ID)
	homeScope := auth.Attributes[AntigravityModelEnumHomeScopeAttribute]
	if homeScope != "" || home.Current() != nil {
		if len(homeScope) != 64 {
			return selection
		}
		key, scope = antigravityModelEnumHomeKey+id+":"+homeScope, homeScope
	} else {
		key, scope = antigravityModelEnumLocalKey+id, antigravityLocalModelEnumScope(auth)
		registration = auth.antigravityEnumRegistration
	}
	client, active, errClient := kvstore.CurrentKVClient()
	if active {
		if errClient != nil {
			return selection
		}
		snapshot, errRead := readAntigravityModelEnumDoc(ctx, client, key)
		if errRead != nil || !antigravityModelEnumDocMatches(snapshot, scope, registration) {
			return selection
		}
		models := snapshot.Get("models")
		lookupEnum := func(id string) string {
			if item := models.Get(ojson.Escape(id)); item.Type == ojson.String && validAntigravityModelEnum(item.Str) {
				return strings.Clone(item.Str)
			}
			return ""
		}
		selection.Enum = lookupEnum(upstreamModelID)
		if groups := snapshot.Get("thinkingGroups"); level != "" && groups.IsObject() {
			selection.selectThinkingVariant(level, AntigravityExcludedModels(auth, cfg), lookupEnum, func(group, level string) (int, bool) {
				levels := groups.Get(ojson.Escape(group))
				if !levels.IsObject() {
					return -1, false
				}
				value := levels.Get(ojson.Escape(level))
				if value.Type == ojson.Number {
					if limit, errLimit := strconv.Atoi(value.Raw); errLimit == nil && limit >= 0 {
						return limit, true
					}
				}
				return -1, true
			})
			if selection.MaxOutputTokens > 0 {
				selection.WebSearch = snapshot.Get("thinkingWebSearch").Get(ojson.Escape(selection.ID)).Type == ojson.True
			}
		}
	} else if homeScope == "" {
		if entry, found := antigravityModelEnums.Get(key); found {
			if snapshot := entry.Load(); snapshot.matches(scope, registration) {
				lookupEnum := func(id string) string {
					if value := snapshot.Models[id]; validAntigravityModelEnum(value) {
						return value
					}
					return ""
				}
				selection.Enum = lookupEnum(upstreamModelID)
				if level != "" && len(snapshot.ThinkingGroups) > 0 {
					selection.selectThinkingVariant(level, AntigravityExcludedModels(auth, cfg), lookupEnum, func(group, level string) (int, bool) {
						levels, isGroup := snapshot.ThinkingGroups[group]
						if limit, listed := levels[level]; listed && limit >= 0 {
							return limit, true
						}
						return -1, isGroup
					})
					if selection.MaxOutputTokens > 0 {
						selection.WebSearch = snapshot.ThinkingWebSearch[selection.ID]
					}
				}
			}
		}
	}
	return selection
}

func validAntigravityModelEnum(value string) bool {
	return len(value) <= antigravityModelEnumMaxBytes && strings.TrimSpace(value) != ""
}

// AntigravityExcludedModels returns the effective OAuth exclusion patterns.
func AntigravityExcludedModels(auth *Auth, cfg *config.Config) []string {
	if auth == nil {
		return nil
	}
	if value := strings.TrimSpace(auth.Attributes["excluded_models"]); value != "" {
		return strings.Split(value, ",")
	}
	if cfg != nil {
		return cfg.OAuthExcludedModels[OAuthModelAliasChannel(auth.Provider, auth.AuthKind())]
	}
	return nil
}

// SameAntigravityModelCatalogScope checks whether an access-token refresh can
// reuse the selection and exclusion decision from the original credential.
func SameAntigravityModelCatalogScope(before, after *Auth) bool {
	if !validAntigravityModelEnumAuth(before) || !validAntigravityModelEnumAuth(after) || before.ID != after.ID ||
		before.AuthKind() != after.AuthKind() || before.Attributes["excluded_models"] != after.Attributes["excluded_models"] {
		return false
	}
	beforeScope := before.Attributes[AntigravityModelEnumHomeScopeAttribute]
	afterScope := after.Attributes[AntigravityModelEnumHomeScopeAttribute]
	if beforeScope != "" || afterScope != "" || home.Current() != nil {
		return beforeScope == afterScope
	}
	return before.antigravityEnumRegistration == after.antigravityEnumRegistration &&
		antigravityLocalModelEnumScope(before) == antigravityLocalModelEnumScope(after)
}

// lookupLimit returns -1 for a missing or malformed member and zero for a
// declared member that cannot serve as a target.
func (selection *AntigravityModelSelection) selectThinkingVariant(level string, excluded []string, lookupEnum func(string) string, lookupLimit func(group, level string) (int, bool)) {
	group := selection.ID
	limit, isGroup := lookupLimit(group, level)
	if !isGroup {
		// Try the longest group first. A shared prefix is not evidence of
		// membership; the complete suffix must be listed in that group.
		for end := len(selection.ID); end > 0; {
			end = strings.LastIndexByte(selection.ID[:end], '-')
			if end <= 0 {
				break
			}
			group = selection.ID[:end]
			suffix := selection.ID[end+1:]
			if !antigravitycatalog.ValidThinkingSuffix(suffix) {
				continue
			}
			if current, _ := lookupLimit(group, suffix); current >= 0 {
				limit, _ = lookupLimit(group, level)
				break
			}
		}
	}
	if limit <= 0 || len(group)+1+len(level) > antigravityModelEnumMaxBytes {
		return
	}
	target := group + "-" + level
	normalized := strings.ToLower(strings.TrimSpace(target))
	for _, pattern := range excluded {
		if modelconfig.MatchExcludedModelPattern(strings.ToLower(strings.TrimSpace(pattern)), normalized) {
			return
		}
	}
	if enum := lookupEnum(target); enum != "" {
		selection.ID, selection.Enum, selection.MaxOutputTokens = target, enum, limit
	}
}

func readAntigravityModelEnumDoc(ctx context.Context, client kvstore.KVClient, key string) (ojson.Result, error) {
	raw, found, errGet := client.KVGet(ctx, key)
	if errGet != nil || !found || len(raw) > 2<<20 || !ojson.ValidBytes(raw) {
		return ojson.Result{}, errGet
	}
	return ojson.ParseBytes(raw), nil
}

func antigravityModelEnumDocMatches(snapshot ojson.Result, scope, registration string) bool {
	return snapshot.Get("scope").Str == scope && snapshot.Get("models").IsObject() &&
		(registration == "" || snapshot.Get("registration").Str == registration)
}

func (snapshot *antigravityModelEnumSnapshot) matches(scope, registration string) bool {
	return snapshot != nil && snapshot.Models != nil && snapshot.Scope == scope &&
		(registration == "" || registration == snapshot.Registration)
}

func validAntigravityModelEnumAuth(auth *Auth) bool {
	return auth != nil && auth.ID != "" && !auth.Disabled && auth.Status != StatusDisabled &&
		strings.EqualFold(auth.Provider, "antigravity")
}

// AntigravityModelProbeBaseURLs returns discovery endpoints in probe order.
func AntigravityModelProbeBaseURLs(auth *Auth) []string {
	if auth != nil && auth.Attributes != nil {
		if raw := strings.TrimSpace(auth.Attributes["base_urls"]); raw != "" {
			parts := strings.Split(raw, ",")
			urls := make([]string, 0, len(parts))
			for _, p := range parts {
				if trimmed := strings.TrimRight(strings.TrimSpace(p), "/"); trimmed != "" {
					urls = append(urls, trimmed)
				}
			}
			if len(urls) > 0 {
				return urls
			}
		}
	}
	if baseURL := resolveAntigravityModelBaseURL(auth); baseURL != "" {
		return []string{baseURL}
	}
	return []string{"https://daily-cloudcode-pa.googleapis.com"}
}

func resolveAntigravityModelBaseURL(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if value := strings.TrimSpace(auth.Attributes["base_url"]); value != "" {
			return strings.TrimRight(value, "/")
		}
	}
	if auth.Metadata != nil {
		if value, ok := auth.Metadata["base_url"].(string); ok {
			value = strings.TrimSpace(value)
			if value != "" {
				return strings.TrimRight(value, "/")
			}
		}
	}
	return ""
}
