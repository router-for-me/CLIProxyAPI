package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxCarriedAnthropicWindows bounds how many windows one merge carries forward,
// so a hint holds at most its own capture's windows plus this many. The carried
// side is the one needing a bound: a capture is sized by one response, while
// merging accumulates across responses over slugs and resets the upstream
// chooses. The capture's own count is not deducted, so a capture that fills its
// own budget still carries.
//
// On the executor path the capture side is bounded by maxAnthropicWindows in
// internal/runtime/executor/helps/claude_rate_limit_record.go. That value is
// restated here rather than imported: sdk does not depend on that package, and
// this entry point is exported to callers that bypass it.
const maxCarriedAnthropicWindows = 64

// maxAnthropicCarryHorizon bounds both how far ahead a carried window's reset
// may be and how old the reading itself may be. Seven days is the longest
// window the unified family exposes, and Reset comes from the response, so a
// distant reset is one the age-out gate never fires on. Bounding the reading's
// age as well keeps a beyond-horizon window from re-entering the carry set once
// a gap in traffic moves the capture instant close enough to that reset.
//
// Only carrying is gated: such a window is still stored and served while the
// capture that reported it is the current one.
const maxAnthropicCarryHorizon = 8 * 24 * time.Hour

// AnthropicRateLimitHint records the most-recent Anthropic
// `anthropic-ratelimit-unified-*` response-header state observed for one auth.
//
// This is passive observability state, populated by the Claude executor on every
// upstream response. It is NOT consulted by the conductor or selector for routing
// decisions — those continue to use Auth.Quota / Auth.NextRetryAfter. Operators
// (management API consumers, dashboards) read this hint to surface utilization
// and reset times before a credential is actually rate-limited.
//
// All fields except Known and ObservedAt are optional. Anthropic's
// `unified-*` family is undocumented and field set has been observed to grow
// over time (e.g. tier-specific 7d windows like `7d_opus`); the Windows map is
// keyed by header window slug rather than predeclared fields so new windows
// surface without code change.
type AnthropicRateLimitHint struct {
	// Known is true once any unified-* header has been observed for this auth.
	Known bool
	// ObservedAt is when the most recent capture happened (server clock).
	ObservedAt time.Time
	// Status mirrors `anthropic-ratelimit-unified-status`: e.g. "allowed",
	// "allowed_warning", "rejected". Pass-through string; do not enum-tighten.
	Status string
	// RepresentativeClaim names the binding window
	// (`anthropic-ratelimit-unified-representative-claim`); e.g. "five_hour",
	// "seven_day", "seven_day_opus". Pass-through string.
	RepresentativeClaim string
	// Reset is the reset moment of the representative window
	// (`anthropic-ratelimit-unified-reset`, epoch seconds → time.Time).
	Reset time.Time
	// Windows maps window slug → per-window state. Slugs are extracted from
	// header names of the form `anthropic-ratelimit-unified-{slug}-{field}`,
	// e.g. "5h", "7d", "7d_opus". The map is generative; consumers should
	// iterate rather than assume a fixed key set.
	//
	// Not necessarily one response's worth: SetAnthropicRateLimitHint carries
	// live windows forward, so this can hold windows RawHeaders never mentions.
	Windows map[string]AnthropicQuotaWindow
	// FallbackPercentage mirrors `anthropic-ratelimit-unified-fallback-percentage`.
	// Optional; 0 when absent.
	FallbackPercentage float64
	// HasFallbackPercentage is true iff the header was present AND parsed to a
	// usable number. It separates an explicit 0 from both "absent" and
	// "malformed", so a consumer never renders garbage as a real reading.
	HasFallbackPercentage bool
	// OverageStatus mirrors `anthropic-ratelimit-unified-overage-status`.
	// Optional; empty when absent.
	OverageStatus string
	// OverageDisabledReason mirrors
	// `anthropic-ratelimit-unified-overage-disabled-reason`. Optional.
	OverageDisabledReason string
	// UpgradePaths mirrors `anthropic-ratelimit-unified-upgrade-paths`. Optional.
	UpgradePaths string
	// RawHeaders preserves every observed `anthropic-ratelimit-unified-*` header
	// as a lower-cased name → first value map. Forward-compat safety net for
	// undocumented schema drift; may be nil when no headers were captured.
	RawHeaders map[string]string
	// AccountFingerprint identifies the underlying account this capture came
	// from (see AnthropicAccountFingerprint). The hint store is keyed by auth
	// ID, but an ID can be reused across credentials — a rotation in place, or
	// a delete-then-recreate. Tagging the capture lets readers reject a hint
	// that demonstrably belongs to a different account instead of reporting the
	// previous credential's quota.
	//
	// Empty means "account unknown" (e.g. an OAuth credential with no email in
	// metadata) and never causes rejection; see AnthropicRateLimitHintFor.
	AccountFingerprint string
}

// AnthropicQuotaWindow records per-window state captured from
// `anthropic-ratelimit-unified-{slug}-{field}` headers.
type AnthropicQuotaWindow struct {
	// ObservedAt is when this window's state was captured, which lags the
	// hint-level ObservedAt for a window carried forward from an earlier
	// capture (see SetAnthropicRateLimitHint). Age a reading out against this
	// field, not the hint-level one. The store sets it, overwriting whatever
	// the caller supplied, and it changes on every capture — exclude it when
	// comparing windows to detect a change in quota state.
	ObservedAt time.Time
	// Status mirrors `unified-{slug}-status`: "allowed", "allowed_warning",
	// "rejected". Pass-through string.
	Status string
	// Reset mirrors `unified-{slug}-reset` (epoch seconds → time.Time).
	Reset time.Time
	// Utilization mirrors `unified-{slug}-utilization` as a fraction. Can
	// exceed 1.0 when overage is in effect. Consult HasUtilization to
	// distinguish a real 0.0 reading from "header not present".
	Utilization float64
	// HasUtilization is true iff a `unified-{slug}-utilization` header was
	// observed for this window. Without this flag, a 0.0 utilization is
	// indistinguishable from an absent header — both cases would mislead
	// downstream alerts into treating unknown utilization as healthy usage.
	HasUtilization bool
	// SurpassedThreshold mirrors `unified-{slug}-surpassed-threshold`. Optional;
	// 0 when absent. Typically populated only when Status is at warning or above.
	SurpassedThreshold float64
	// HasSurpassedThreshold is true iff the header was present AND parsed.
	// Same contract as HasUtilization.
	HasSurpassedThreshold bool
}

var anthropicRateLimitHintByAuth sync.Map

// anthropicRateLimitWriteMu serializes the read-compare-write in
// SetAnthropicRateLimitHint. sync.Map offers no compare-and-swap usable here --
// AnthropicRateLimitHint holds map fields and is uncomparable, so CompareAndSwap
// would panic -- and a bare Load-then-Store lets two concurrent captures for one
// auth interleave, which is the proxy's normal load rather than an edge case.
// Readers stay lock-free: this guards writers against each other only.
var anthropicRateLimitWriteMu sync.Mutex

// SetAnthropicRateLimitHint updates the latest known Anthropic rate-limit state
// for an auth. ObservedAt is defaulted to time.Now() if zero. Empty authID is
// silently ignored. Concurrent-safe.
//
// Windows are merged with the stored hint; everything else — Status, Reset,
// RepresentativeClaim, RawHeaders and the rest — describes this capture alone
// and replaces what was stored. The `unified-*` family covers only the windows
// belonging to the responding model's tier, so an ordinary response omits a
// premium window such as `7d_oi` whose quota is still live, and replacing
// wholesale drops it hours before its reset.
//
// A window the stored hint knows and this capture omits — or names without
// reporting a single field for — is carried over when all of:
//
//   - this capture has Known set. One with no unified-* content inherits
//     nothing.
//   - the stored capture's AccountFingerprint equals this one's. Any
//     difference, including one side empty, leaves identity unestablished.
//   - the window's Reset is after this capture's ObservedAt. A reset exactly at
//     it has arrived; a zero Reset can never age out.
//   - that Reset is at most maxAnthropicCarryHorizon ahead, and the reading is
//     itself no older than that.
//
// A window this capture did report replaces the stored one whole, never field
// by field — including one reported without a parseable reset, which is honest
// new data that no later capture can carry.
//
// At most maxCarriedAnthropicWindows are carried per merge. When that binds the
// candidates resetting soonest are dropped, since ordinary captures re-report a
// near-reset window anyway while the long-horizon ones are what the carry
// exists to preserve; ties break on slug.
//
// Carried windows keep the per-window ObservedAt of the capture that observed
// them, and the rest are stamped with this capture's, overwriting the caller's
// value. A hint from GetAnthropicRateLimitHint must therefore not be written
// back through this function: the round trip re-stamps carried windows as
// freshly observed, so it is not an identity. To assert that the window set is
// exactly X, call DeleteAnthropicRateLimitHint first — a sequence that is not
// atomic against a concurrent capture, since Delete does not take the writer
// mutex.
//
// One limitation: a capture older than the stored one is dropped whole, windows
// included, so a live window only it observed is lost rather than merged. Call
// sites stamp ObservedAt and then parse the response before calling, so the
// window for that reorder is the parse plus the wait on the writer mutex.
func SetAnthropicRateLimitHint(authID string, hint AnthropicRateLimitHint) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	if hint.ObservedAt.IsZero() {
		hint.ObservedAt = time.Now()
	}
	// Clone map fields so the stored hint owns its own state. Without this,
	// a caller that mutates the maps post-Set would corrupt the shared
	// store and risk "concurrent map iteration and map write" panics under
	// concurrent Get traffic. Pairs with the Get-side defensive copy: store
	// owns its inner maps, callers see independent copies on both sides.
	if hint.Windows != nil {
		cloned := make(map[string]AnthropicQuotaWindow, len(hint.Windows))
		for k, v := range hint.Windows {
			// The store owns per-window ObservedAt; carried windows keep the
			// stamp of the capture that saw them.
			v.ObservedAt = hint.ObservedAt
			cloned[k] = v
		}
		hint.Windows = cloned
	}
	if hint.RawHeaders != nil {
		cloned := make(map[string]string, len(hint.RawHeaders))
		for k, v := range hint.RawHeaders {
			cloned[k] = v
		}
		hint.RawHeaders = cloned
	}
	// Drop a capture older than what is already stored; the godoc above covers
	// how two concurrent captures on one credential arrive out of order.
	//
	// This orders same-credential captures only. A response already in flight
	// when the credential rotated carries a *newer* ObservedAt than the
	// replacement's capture, so no ordering rule can reject it; that case is
	// handled on read instead, by the account fingerprint (see
	// AnthropicRateLimitHintFor). The two are complementary: ordering settles
	// which snapshot of one account wins, identity settles which account the
	// stored snapshot belongs to.
	//
	// Serialized against other writers so the compare and the store cannot be
	// split by a concurrent capture; see anthropicRateLimitWriteMu.
	anthropicRateLimitWriteMu.Lock()
	defer anthropicRateLimitWriteMu.Unlock()
	if prev, ok := anthropicRateLimitHintByAuth.Load(authID); ok {
		if prevHint, okPrev := prev.(AnthropicRateLimitHint); okPrev {
			if prevHint.ObservedAt.After(hint.ObservedAt) {
				return
			}
			carryLiveWindows(&hint, prevHint)
		}
	}
	anthropicRateLimitHintByAuth.Store(authID, hint)
}

// windowHasContent reports whether a window says anything about quota, matching
// the emptiness rule the management projection applies before emitting one. The
// parser seeds a slug as soon as any header names it, so an entry whose every
// header failed to parse records that a header arrived, not an observation.
func windowHasContent(window AnthropicQuotaWindow) bool {
	return window.Status != "" ||
		window.HasUtilization ||
		window.HasSurpassedThreshold ||
		!window.Reset.IsZero()
}

// carryLiveWindows extends the capture's windows with the still-live windows
// prevHint knows and this capture did not report; SetAnthropicRateLimitHint
// documents the policy.
//
// prevHint is the stored hint, so its map is read-only here; its values are
// flat structs, so copying one out shares nothing. hint.Windows is this call's
// private clone and the only map written.
//
// Must be called with anthropicRateLimitWriteMu held.
func carryLiveWindows(hint *AnthropicRateLimitHint, prevHint AnthropicRateLimitHint) {
	// A capture stating nothing about quota inherits nothing.
	if !hint.Known {
		return
	}
	if prevHint.AccountFingerprint != hint.AccountFingerprint {
		return
	}
	horizon := hint.ObservedAt.Add(maxAnthropicCarryHorizon)

	type carryCandidate struct {
		slug  string
		reset time.Time
	}
	candidates := make([]carryCandidate, 0, len(prevHint.Windows))
	for slug, window := range prevHint.Windows {
		if reported, ok := hint.Windows[slug]; ok && windowHasContent(reported) {
			continue
		}
		// Excludes a zero Reset too: a window with no known reset could never
		// be aged out.
		if !window.Reset.After(hint.ObservedAt) {
			continue
		}
		if window.Reset.After(horizon) {
			continue
		}
		// A close-enough reset is not sufficient on its own. After a gap in
		// traffic a reset that was implausibly distant when observed comes back
		// inside the horizon, so bound the age of the reading as well.
		if hint.ObservedAt.Sub(window.ObservedAt) > maxAnthropicCarryHorizon {
			continue
		}
		candidates = append(candidates, carryCandidate{slug: slug, reset: window.Reset})
	}
	if len(candidates) == 0 {
		return
	}
	if len(candidates) > maxCarriedAnthropicWindows {
		// Keep the furthest resets: those are the long-horizon windows the
		// responding tier stopped reporting, while near-reset candidates get
		// re-reported anyway. Slug breaks ties so eviction never depends on map
		// iteration order.
		sort.Slice(candidates, func(left, right int) bool {
			if !candidates[left].reset.Equal(candidates[right].reset) {
				return candidates[left].reset.After(candidates[right].reset)
			}
			return candidates[left].slug < candidates[right].slug
		})
		candidates = candidates[:maxCarriedAnthropicWindows]
	}

	if hint.Windows == nil {
		hint.Windows = make(map[string]AnthropicQuotaWindow, len(candidates))
	}
	for _, candidate := range candidates {
		hint.Windows[candidate.slug] = prevHint.Windows[candidate.slug]
	}
}

// GetAnthropicRateLimitHint returns the latest known Anthropic rate-limit state
// for an auth. The returned bool is true when a hint has been stored for this
// authID at any point; the hint's Known field reflects whether the stored data
// includes any parsed unified-* header content (a non-empty record).
//
// This lookup is by auth ID alone and does NOT check account identity. An auth
// ID reused across a credential rotation can therefore return the previous
// account's quota here. Callers that hold the *Auth — which is every caller
// serving this data outward — should use AnthropicRateLimitHintFor instead,
// which rejects a hint belonging to a different account. This entry point
// remains for lookups where no *Auth is available and the ID-only semantics
// are the intent.
func GetAnthropicRateLimitHint(authID string) (AnthropicRateLimitHint, bool) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return AnthropicRateLimitHint{}, false
	}
	value, ok := anthropicRateLimitHintByAuth.Load(authID)
	if !ok {
		return AnthropicRateLimitHint{}, false
	}
	hint, ok := value.(AnthropicRateLimitHint)
	if !ok {
		anthropicRateLimitHintByAuth.Delete(authID)
		return AnthropicRateLimitHint{}, false
	}
	// Clone map fields so callers cannot mutate internal state. Concurrent
	// readers each get an independent view; the shared store remains stable.
	// Without this, a caller mutating got.Windows or got.RawHeaders (even
	// accidentally while preparing response data) would race against other
	// readers and could trigger a `concurrent map read and map write` panic.
	if hint.Windows != nil {
		cloned := make(map[string]AnthropicQuotaWindow, len(hint.Windows))
		for k, v := range hint.Windows {
			cloned[k] = v
		}
		hint.Windows = cloned
	}
	if hint.RawHeaders != nil {
		cloned := make(map[string]string, len(hint.RawHeaders))
		for k, v := range hint.RawHeaders {
			cloned[k] = v
		}
		hint.RawHeaders = cloned
	}
	return hint, true
}

// HasKnownAnthropicRateLimitHint reports whether a hint with parsed content has
// been captured for this auth. Like GetAnthropicRateLimitHint, this is keyed by
// auth ID alone and does not check account identity.
func HasKnownAnthropicRateLimitHint(authID string) bool {
	hint, ok := GetAnthropicRateLimitHint(authID)
	return ok && hint.Known
}

// AnthropicAccountFingerprint derives a stable, non-secret identifier for the
// account behind an auth, used to detect that a reused auth ID now refers to a
// different credential.
//
// Returns "" when the account cannot be identified (nil auth, or an OAuth
// credential whose metadata carries no email). Callers must treat "" as
// "unknown" rather than as a distinct account.
//
// The underlying AccountInfo value is hashed rather than stored: for API-key
// auths it is the API key itself, which must not sit in a process-wide map or
// reach a management response.
func AnthropicAccountFingerprint(auth *Auth) string {
	if auth == nil {
		return ""
	}
	kind, value := auth.AccountInfo()
	value = strings.TrimSpace(value)
	if kind == "" || value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(kind + "\x00" + value))
	return hex.EncodeToString(sum[:8])
}

// AnthropicRateLimitHintFor returns the stored hint for an auth, rejecting a
// capture that provably belongs to a different account under the same auth ID.
//
// Rejection requires both fingerprints to be non-empty and different. An empty
// fingerprint on either side means "account unknown", which resolves to serving
// the hint: an unknown account is the pre-existing ID-only behaviour, whereas
// rejecting on unknown would discard valid state every time an account became
// unidentifiable — for instance a token refresh that returns no email.
//
// This is what makes the store safe against auth-lifecycle events without
// hooking them. A capture still in flight when a credential rotates lands under
// the old fingerprint and is rejected here rather than resurrecting stale quota.
func AnthropicRateLimitHintFor(auth *Auth) (AnthropicRateLimitHint, bool) {
	if auth == nil {
		return AnthropicRateLimitHint{}, false
	}
	hint, ok := GetAnthropicRateLimitHint(auth.ID)
	if !ok {
		return AnthropicRateLimitHint{}, false
	}
	expected := AnthropicAccountFingerprint(auth)
	if expected != "" && hint.AccountFingerprint != "" && expected != hint.AccountFingerprint {
		return AnthropicRateLimitHint{}, false
	}
	return hint, true
}

// DeleteAnthropicRateLimitHint removes any stored hint for an auth. Empty
// authID is a no-op. Concurrent-safe.
//
// Called from Manager.Remove to release the entry when a credential goes away.
// Correctness against a reused auth ID does not depend on this running:
// AnthropicRateLimitHintFor rejects a capture whose account fingerprint no
// longer matches, which also covers a capture that lands after the delete.
func DeleteAnthropicRateLimitHint(authID string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	anthropicRateLimitHintByAuth.Delete(authID)
}
