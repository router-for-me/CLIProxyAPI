package auth

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
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
	Scope        string            `json:"scope"`
	Registration string            `json:"registration"`
	Models       map[string]string `json:"models"`
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
	return StoreAntigravityModelEnums(ctx, current, models)
}

// StoreAntigravityModelEnums replaces the local snapshot after a successful
// catalog probe. A nil map only renews an existing matching KV snapshot. A
// non-nil map is immutable after publication. False indicates a write failure.
func StoreAntigravityModelEnums(ctx context.Context, auth *Auth, models map[string]string) bool {
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
		written, errSet := client.KVSet(ctx, key, doc.Bytes(), kvstore.KVSetOptions{PX: antigravityModelEnumTTL})
		return written && errSet == nil
	}
	if models != nil {
		entry := antigravityModelEnums.GetOrAdd(key, func() *atomic.Pointer[antigravityModelEnumSnapshot] {
			return new(atomic.Pointer[antigravityModelEnumSnapshot])
		})
		entry.Store(&antigravityModelEnumSnapshot{Scope: scope, Registration: auth.antigravityEnumRegistration, Models: models})
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
	if !validAntigravityModelEnumAuth(auth) || upstreamModelID == "" || len(upstreamModelID) > antigravityModelEnumMaxBytes {
		return "", false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var key, scope, registration string
	id := kvstore.HashKeyPart(auth.ID)
	homeScope := auth.Attributes[AntigravityModelEnumHomeScopeAttribute]
	if homeScope != "" || home.Current() != nil {
		if len(homeScope) != 64 {
			return "", false
		}
		key, scope = antigravityModelEnumHomeKey+id+":"+homeScope, homeScope
	} else {
		key, scope = antigravityModelEnumLocalKey+id, antigravityLocalModelEnumScope(auth)
		registration = auth.antigravityEnumRegistration
	}
	var value string
	client, active, errClient := kvstore.CurrentKVClient()
	if active {
		if errClient != nil {
			return "", false
		}
		snapshot, errRead := readAntigravityModelEnumDoc(ctx, client, key)
		if errRead != nil || !antigravityModelEnumDocMatches(snapshot, scope, registration) {
			return "", false
		}
		item := snapshot.Get("models").Get(ojson.Escape(upstreamModelID))
		if item.Type != ojson.String {
			return "", false
		}
		value = strings.Clone(item.Str)
	} else if homeScope == "" {
		if entry, found := antigravityModelEnums.Get(key); found {
			if snapshot := entry.Load(); snapshot.matches(scope, registration) {
				value = snapshot.Models[upstreamModelID]
			}
		}
	}
	if len(value) > antigravityModelEnumMaxBytes || strings.TrimSpace(value) == "" {
		return "", false
	}
	return value, true
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
