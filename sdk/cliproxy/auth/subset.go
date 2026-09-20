// Package auth: optional credential subset routing via the X-Auth-Subset header.
//
// When enabled through SetSubsetRouting, a request may carry a comma separated
// list of auth indexes (the same stable identifiers surfaced as "auth_index" by
// the management API, derived in Auth.EnsureIndex from the credential identity,
// not from load order). Selection for that request is then narrowed to the
// listed credentials before the configured selector runs; rotation, cooldown
// handling and failover are delegated unchanged to the existing selection
// logic through authSelectionEligibility.
//
// Behavior contract:
//   - feature disabled (default) or header absent/empty: current behavior, full pool;
//   - header present with at least one entry matching a known credential:
//     the pool is narrowed to the matching credentials;
//   - every entry unknown: "fallback" policy uses the full pool, "reject"
//     policy fails the request with HTTP 429;
//   - malformed entries are ignored; if nothing valid remains the header is
//     treated as empty (full pool).
package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// SubsetHeaderName is the request header carrying the allowed auth index list.
const SubsetHeaderName = "X-Auth-Subset"

// Empty-subset policies applied when every subset entry is unknown to the pool.
const (
	SubsetEmptyPolicyFallback = "fallback"
	SubsetEmptyPolicyReject   = "reject"
)

// subsetMaxEntryLength bounds a single subset entry; auth indexes are 16 hex
// characters today, the bound only guards against abusive header values.
const subsetMaxEntryLength = 64

type subsetRoutingSettings struct {
	enabled     bool
	emptyPolicy string
	// requireSignature and signatureKey reserve configuration for signing the
	// X-Auth-Subset header. Verification is intentionally not implemented yet.
	// TODO: enforce HMAC verification of the subset header when requireSignature is set.
	requireSignature bool
	signatureKey     string
}

var subsetRouting atomic.Pointer[subsetRoutingSettings]

// SetSubsetRouting configures X-Auth-Subset credential subset routing globally.
// The feature is disabled unless enabled is true. emptyPolicy accepts
// "fallback" (default) or "reject"; any other value falls back to "fallback".
func SetSubsetRouting(enabled bool, emptyPolicy string, requireSignature bool, signatureKey string) {
	policy := strings.ToLower(strings.TrimSpace(emptyPolicy))
	if policy != SubsetEmptyPolicyReject {
		policy = SubsetEmptyPolicyFallback
	}
	subsetRouting.Store(&subsetRoutingSettings{
		enabled:          enabled,
		emptyPolicy:      policy,
		requireSignature: requireSignature,
		signatureKey:     strings.TrimSpace(signatureKey),
	})
}

func subsetRoutingSnapshot() subsetRoutingSettings {
	if settings := subsetRouting.Load(); settings != nil {
		return *settings
	}
	return subsetRoutingSettings{emptyPolicy: SubsetEmptyPolicyFallback}
}

type subsetAllowedAuthIDsContextKey struct{}

func withSubsetAllowedAuthIDs(ctx context.Context, ids map[string]struct{}) context.Context {
	if len(ids) == 0 {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, subsetAllowedAuthIDsContextKey{}, ids)
}

func subsetAllowedAuthIDsFromContext(ctx context.Context) map[string]struct{} {
	if ctx == nil {
		return nil
	}
	ids, _ := ctx.Value(subsetAllowedAuthIDsContextKey{}).(map[string]struct{})
	return ids
}

// subsetEntryValid accepts lowercase alphanumerics plus '.', '_' and '-'.
func subsetEntryValid(entry string) bool {
	if entry == "" || len(entry) > subsetMaxEntryLength {
		return false
	}
	for _, r := range entry {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// parseSubsetHeader splits a comma separated auth index list, trimming
// whitespace, lowercasing entries, dropping malformed items and de-duplicating
// while preserving first-seen order.
func parseSubsetHeader(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		entry := strings.ToLower(strings.TrimSpace(part))
		if !subsetEntryValid(entry) {
			continue
		}
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		out = append(out, entry)
	}
	return out
}

// subsetHeaderValue joins every occurrence of the subset header so repeated
// header lines behave like one comma separated list.
func subsetHeaderValue(headers http.Header) string {
	if len(headers) == 0 {
		return ""
	}
	return strings.Join(headers.Values(SubsetHeaderName), ",")
}

// authIndexNoMutate mirrors Auth.EnsureIndex without caching the result on the
// Auth value, so it is safe on shared entries under the manager read lock.
func authIndexNoMutate(a *Auth) string {
	if a == nil {
		return ""
	}
	if idx := strings.TrimSpace(a.Index); idx != "" {
		return idx
	}
	return stableAuthIndex(a.indexSeed())
}

// resolveSubsetAuthIDs maps requested auth indexes to the IDs of currently
// registered credentials. Unknown entries are ignored.
func (m *Manager) resolveSubsetAuthIDs(entries []string) map[string]struct{} {
	if m == nil || len(entries) == 0 {
		return nil
	}
	requested := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		requested[entry] = struct{}{}
	}
	ids := make(map[string]struct{})
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, auth := range m.auths {
		if auth == nil || strings.TrimSpace(auth.ID) == "" {
			continue
		}
		idx := strings.ToLower(strings.TrimSpace(authIndexNoMutate(auth)))
		if idx == "" {
			continue
		}
		if _, ok := requested[idx]; ok {
			ids[auth.ID] = struct{}{}
		}
	}
	return ids
}

// applySubsetRouting narrows subsequent auth selection for one request to the
// credentials named by the X-Auth-Subset header. It returns a context consumed
// by authSelectionEligibilityForRequest, so every local selection path
// (legacy pick, scheduler fast path, plugin scheduler candidates, retry
// rounds) observes the same subset while rotation and failover stay with the
// existing logic. With the feature disabled or no usable header the context is
// returned unchanged and behavior is identical to an unpatched build.
func (m *Manager) applySubsetRouting(ctx context.Context, opts cliproxyexecutor.Options) (context.Context, error) {
	settings := subsetRoutingSnapshot()
	if !settings.enabled || m == nil {
		return ctx, nil
	}
	raw := subsetHeaderValue(opts.Headers)
	if strings.TrimSpace(raw) == "" {
		return ctx, nil
	}
	entries := parseSubsetHeader(raw)
	if len(entries) == 0 {
		return ctx, nil
	}
	ids := m.resolveSubsetAuthIDs(entries)
	if len(ids) == 0 {
		if settings.emptyPolicy == SubsetEmptyPolicyReject {
			return ctx, &Error{
				Code:       "auth_subset_unavailable",
				Message:    fmt.Sprintf("no registered credential matches the %s header (%d entries)", SubsetHeaderName, len(entries)),
				HTTPStatus: http.StatusTooManyRequests,
			}
		}
		return ctx, nil
	}
	return withSubsetAllowedAuthIDs(ctx, ids), nil
}
