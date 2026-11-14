package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// codexQuotaWindowPrefixes lists the Codex base quota windows, in tie-break order.
var codexQuotaWindowPrefixes = []string{"X-Codex-Primary-", "X-Codex-Secondary-"}

const (
	maxQuotaSignalHeaders = 64
	maxQuotaSignalValue   = 512
)

// ProviderSupportsQuotaObservation reports whether the named provider emits a
// passive credential-level quota snapshot understood by collectQuotaSignals.
func ProviderSupportsQuotaObservation(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude", "codex", "devin":
		return true
	default:
		return false
	}
}

// ObserveResponseHeadersForProvider replaces the passive quota snapshot with the
// signals carried by the current upstream response.
//
// The snapshot is replaced rather than merged: a watermark such as Retry-After
// or a "limit reached" flag only appears on the response that produced it, so
// accumulating signals across responses would leave an expired value visible
// indefinitely. Responses that carry no quota signal at all (transport
// failures, 5xx, unrelated endpoints) leave the previous snapshot untouched.
//
// This function only ever touches ObservedAt and Signals. Cooldown and
// scheduling fields are never read or written here.
func (q *QuotaState) ObserveResponseHeadersForProvider(provider string, headers http.Header, observedAt time.Time) bool {
	if q == nil {
		return false
	}
	if !ProviderSupportsQuotaObservation(provider) {
		return q.ClearObservationSignals()
	}
	next := collectQuotaSignals(provider, headers)
	if len(next) == 0 {
		return false
	}
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	q.Signals = next
	q.ObservedAt = observedAt
	return true
}

// quotaAccountIdentity identifies whose quota a passive snapshot describes:
// a digest of the provider plus the account email and, when present, the
// upstream account and organization IDs. API-key credentials identify by their key, so only the
// digest is returned and the secret is never retained. It is empty when the
// auth carries no account identity.
func quotaAccountIdentity(auth *Auth) string {
	if auth == nil {
		return ""
	}
	_, account := auth.AccountInfo()
	for _, key := range []string{"account_id", "account_uuid", "organization_uuid"} {
		if value, ok := auth.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			account += "|" + key + "=" + strings.TrimSpace(value)
		}
	}
	if account == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(auth.Provider)) + "|" + account))
	return hex.EncodeToString(sum[:16])
}

// quotaSnapshotIsForAnotherAccount reports whether a snapshot kept from existing
// may describe a different account than auth. Without an identity on both
// sides, a credential replacement cannot be shown to be the same account, and
// wrongly clearing a snapshot costs only one probe request.
func quotaSnapshotIsForAnotherAccount(existing, auth *Auth, credentialsReplaced bool) bool {
	before, after := quotaAccountIdentity(existing), quotaAccountIdentity(auth)
	if before != "" && after != "" {
		return before != after
	}
	return credentialsReplaced
}

// observeResponseHeadersIfNotOlder applies an observation unless the snapshot
// already holds a newer one. Observations are stamped at header receipt, and
// concurrent requests on one credential can finish out of order, so an older
// stream that drains last must not replace a newer snapshot.
func (q *QuotaState) observeResponseHeadersIfNotOlder(provider string, headers http.Header, observedAt time.Time) bool {
	if q == nil || (!q.ObservedAt.IsZero() && observedAt.Before(q.ObservedAt)) {
		return false
	}
	return q.ObserveResponseHeadersForProvider(provider, headers, observedAt)
}

// dropSupersededResetHeaders removes the older of a window's absolute
// (-Reset-At) and relative (-Reset-After-Seconds) reset values when they were
// received at different times. A websocket frame can update only one of them
// while the holder keeps the other from an earlier frame, which may describe
// the previous window. A newer value that cannot be read supersedes nothing.
// headers is not modified.
func dropSupersededResetHeaders(headers http.Header, receivedAt func(string) time.Time) http.Header {
	if len(headers) == 0 || receivedAt == nil {
		return headers
	}
	const relativeSuffix = "-reset-after-seconds"
	result := headers
	cloned := false
	for key := range headers {
		lower := strings.ToLower(key)
		if !strings.HasSuffix(lower, relativeSuffix) {
			continue
		}
		absoluteKey := http.CanonicalHeaderKey(key[:len(key)-len(relativeSuffix)] + "-Reset-At")
		if _, ok := headers[absoluteKey]; !ok {
			continue
		}
		relativeAt, absoluteAt := receivedAt(key), receivedAt(absoluteKey)
		if relativeAt.Equal(absoluteAt) {
			continue
		}
		// Only a readable newer value can supersede the older one.
		superseded := key
		if relativeAt.After(absoluteAt) {
			superseded = absoluteKey
			if _, ok := parseResetAfterSeconds(headers.Get(key)); !ok {
				continue
			}
		} else if _, ok := parseResetTimestamp(headers.Get(absoluteKey)); !ok {
			continue
		}
		if !cloned {
			result = headers.Clone()
			cloned = true
		}
		delete(result, superseded)
	}
	return result
}

// quotaResetReceivedAt returns when the reset value the selector will score
// arrived, or the zero time when headers carry none it can read. It chooses
// the window and validates values the same way quotaWindowReset does. Snapshots are ordered by this time: an
// attempt can merge other headers late, even another window's reset, while
// still holding an older value for the scored window, and must not be taken
// for a newer observation of it.
func quotaResetReceivedAt(headers http.Header, receivedAt func(string) time.Time) time.Time {
	var scored time.Time
	if receivedAt == nil {
		return scored
	}
	longest := int64(-1)
	for _, prefix := range codexQuotaWindowPrefixes {
		var windowReceived time.Time
		if _, ok := parseResetTimestamp(headers.Get(prefix + "Reset-At")); ok {
			windowReceived = receivedAt(prefix + "Reset-At")
		}
		if _, ok := parseResetAfterSeconds(headers.Get(prefix + "Reset-After-Seconds")); ok {
			if received := receivedAt(prefix + "Reset-After-Seconds"); received.After(windowReceived) {
				windowReceived = received
			}
		}
		if windowReceived.IsZero() {
			continue
		}
		minutes, _ := strconv.ParseInt(strings.TrimSpace(headers.Get(prefix+"Window-Minutes")), 10, 64)
		if minutes > longest {
			longest = minutes
			scored = windowReceived
		}
	}
	if longest >= 0 {
		return scored
	}
	if _, ok := parseResetTimestamp(headers.Get("Anthropic-Ratelimit-Unified-7d-Reset")); ok {
		return receivedAt("Anthropic-Ratelimit-Unified-7d-Reset")
	}
	const unifiedReset = "Anthropic-Ratelimit-Unified-Reset"
	if parsed, ok := parseResetTimestamp(headers.Get(unifiedReset)); ok {
		claims := map[string]string{
			"Anthropic-Ratelimit-Unified-Representative-Claim": headers.Get("Anthropic-Ratelimit-Unified-Representative-Claim"),
			"Anthropic-Ratelimit-Unified-Overage-Reset":        headers.Get("Anthropic-Ratelimit-Unified-Overage-Reset"),
		}
		if received := receivedAt(unifiedReset); !claudeResetIsBillingBoundary(claims, parsed, received) {
			return received
		}
	}
	return scored
}

// rebaseRelativeQuotaHeaders rewrites relative reset values so they count from
// observedAt. A websocket quota frame can merge only some headers, so a
// Reset-After-Seconds value received at another time than observedAt would
// otherwise be anchored at the wrong moment and its reset would drift.
// receivedAt reports when a header was received; headers is not modified.
func rebaseRelativeQuotaHeaders(headers http.Header, observedAt time.Time, receivedAt func(string) time.Time) http.Header {
	if len(headers) == 0 || receivedAt == nil {
		return headers
	}
	rebased := headers
	cloned := false
	for key, values := range headers {
		if len(values) == 0 || !strings.HasSuffix(strings.ToLower(key), "-reset-after-seconds") {
			continue
		}
		received := receivedAt(key)
		if received.IsZero() || received.Equal(observedAt) {
			continue
		}
		seconds, ok := parseResetAfterSeconds(values[len(values)-1])
		if !ok {
			continue
		}
		remaining := math.Max(0, seconds-observedAt.Sub(received).Seconds())
		if !cloned {
			rebased = headers.Clone()
			cloned = true
		}
		rebased[key] = []string{strconv.FormatInt(int64(math.Round(remaining)), 10)}
	}
	return rebased
}

// ClearObservationSignals removes only passive observation data. It leaves
// cooldown and scheduler state untouched.
func (q *QuotaState) ClearObservationSignals() bool {
	if q == nil || (len(q.Signals) == 0 && q.ObservedAt.IsZero()) {
		return false
	}
	q.Signals = nil
	q.ObservedAt = time.Time{}
	return true
}

// cooldownFieldsOf copies only scheduler cooldown fields. Observation data is
// omitted so cooldown persistence and restore cannot replace a live snapshot.
func cooldownFieldsOf(q QuotaState) QuotaState {
	return QuotaState{
		Exceeded:      q.Exceeded,
		Reason:        q.Reason,
		NextRecoverAt: q.NextRecoverAt,
		BackoffLevel:  q.BackoffLevel,
	}
}

// applyCooldownFields writes only scheduler cooldown fields. ObservedAt and
// Signals are left untouched so a cooldown transition cannot erase the last
// upstream watermark.
func applyCooldownFields(dst *QuotaState, cooldown QuotaState) {
	if dst == nil {
		return
	}
	dst.Exceeded = cooldown.Exceeded
	dst.Reason = cooldown.Reason
	dst.NextRecoverAt = cooldown.NextRecoverAt
	dst.BackoffLevel = cooldown.BackoffLevel
	dst.cooldownSignals = maps.Clone(cooldown.cooldownSignals)
	dst.cooldownEvaluated = cooldown.Exceeded || cooldown.cooldownEvaluated
}

func rejectedClaudeSignalsForCooldown(provider string, headers http.Header) map[string]string {
	if !strings.EqualFold(strings.TrimSpace(provider), "claude") {
		return nil
	}
	signals := collectQuotaSignals("claude", headers)
	if !hasRejectedClaudeUnifiedSignals(signals) {
		return nil
	}
	return signals
}

func sanitizePersistedClaudeCooldownSignals(raw map[string]string) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	headers := make(http.Header, len(raw))
	for key, val := range raw {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), "anthropic-ratelimit-unified-") {
			headers.Set(key, val)
		}
	}
	return rejectedClaudeSignalsForCooldown("claude", headers)
}

// collectQuotaSignals builds the bounded snapshot for a single response.
func collectQuotaSignals(provider string, headers http.Header) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	names := make([]string, 0, len(headers))
	values := make(map[string]string, len(headers))
	for key, headerValues := range headers {
		canonicalKey := http.CanonicalHeaderKey(strings.TrimSpace(key))
		if !isQuotaSignalHeaderForProvider(provider, canonicalKey) || len(headerValues) == 0 {
			continue
		}
		value := strings.TrimSpace(headerValues[len(headerValues)-1])
		if !validQuotaSignalValue(value) {
			continue
		}
		if _, exists := values[canonicalKey]; !exists {
			names = append(names, canonicalKey)
		}
		values[canonicalKey] = value
	}
	if len(names) == 0 {
		return nil
	}
	// Rank then sort so truncation is deterministic and keeps credential-level
	// watermarks (plan, credits, primary) ahead of additional-limit namespaces.
	sort.Slice(names, func(i, j int) bool {
		ri, rj := quotaSignalRetentionRank(names[i]), quotaSignalRetentionRank(names[j])
		if ri != rj {
			return ri < rj
		}
		return names[i] < names[j]
	})
	if len(names) > maxQuotaSignalHeaders {
		names = names[:maxQuotaSignalHeaders]
	}
	signals := make(map[string]string, len(names))
	for _, name := range names {
		signals[name] = values[name]
	}
	return signals
}

// validQuotaSignalValue rejects empty, oversized, and control-character values.
// Observed values reach the plain-text upstream request log, so a value
// containing CR/LF could otherwise forge a header line there.
func validQuotaSignalValue(value string) bool {
	if value == "" || len(value) > maxQuotaSignalValue {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func quotaSignalRetentionRank(name string) int {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch {
	case lower == "retry-after", strings.HasPrefix(lower, "anthropic-ratelimit-unified-"):
		return 0
	case lower == "x-codex-plan-type", lower == "x-codex-active-limit", strings.HasPrefix(lower, "x-codex-credits-"):
		return 1
	case lower == "x-codex-allowed", lower == "x-codex-limit-reached",
		strings.HasPrefix(lower, "x-codex-primary-"), strings.HasPrefix(lower, "x-codex-secondary-"):
		return 2
	case strings.HasPrefix(lower, "x-codex-code-review-"):
		return 3
	case strings.HasPrefix(lower, "x-codex-additional-"):
		return 5
	case strings.HasPrefix(lower, "x-codex-"):
		return 4
	default:
		return 6
	}
}

func isQuotaSignalHeaderForProvider(provider, name string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "retry-after" {
		return provider == "claude" || provider == "codex"
	}
	if strings.HasPrefix(name, "anthropic-ratelimit-unified-") {
		return provider == "claude"
	}
	if strings.HasPrefix(name, "x-ratelimit-") {
		// Observed Codex responses do not carry x-ratelimit-* headers; the only
		// upstream seen emitting them is Grok, which is excluded from quota
		// observation. The rule is kept so a future Codex rollout is captured
		// without another change, but it is expected to be inert today.
		return provider == "codex"
	}
	if !strings.HasPrefix(name, "x-codex-") {
		return false
	}
	if provider != "codex" {
		return false
	}
	if name == "x-codex-active-limit" || name == "x-codex-plan-type" ||
		strings.HasPrefix(name, "x-codex-credits-") {
		return true
	}
	// Codex namespaces each additional limit by a short name on the HTTP path
	// (x-codex-bengalfox-primary-used-percent) and by limit name on the
	// websocket path (x-codex-additional-<limit>-primary-used-percent), so the
	// suffix is matched instead of an exhaustive header list.
	for _, marker := range []string{
		"-allowed",
		"-limit-reached",
		"-limit-name",
		"-used-percent",
		"-window-minutes",
		"-reset-after-seconds",
		"-reset-at",
		"-over-secondary-limit-percent",
	} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

// mergeQuotaObservation keeps the newest observation snapshot instead of
// unioning signals captured at different times, so merging an older snapshot
// can never resurrect a stale watermark.
func mergeQuotaObservation(target, source QuotaState) QuotaState {
	if source.ObservedAt.IsZero() || source.ObservedAt.Before(target.ObservedAt) {
		return target
	}
	target.ObservedAt = source.ObservedAt
	target.Signals = source.Clone().Signals
	return target
}
