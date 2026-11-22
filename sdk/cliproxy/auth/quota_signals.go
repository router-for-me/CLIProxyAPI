package auth

import (
	"maps"
	"net/http"
	"sort"
	"strings"
	"time"
)

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
// A snapshot older than the current one is ignored, so a long stream that
// finishes after a newer response cannot restore its outdated watermark.
//
// Providers without quota headers discard stale header signals.
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
	if observedAt.Before(q.ObservedAt) {
		return false
	}
	q.Signals = next
	q.ObservedAt = observedAt
	return true
}

// ObserveLookupForProvider replaces the usage-lookup snapshot. It leaves the
// response snapshot alone, so traffic never erases windows only a lookup reports.
func (q *QuotaState) ObserveLookupForProvider(provider string, headers http.Header, lookedUpAt time.Time) bool {
	if q == nil || !ProviderSupportsQuotaObservation(provider) {
		return false
	}
	next := collectQuotaSignals(provider, headers)
	if len(next) == 0 {
		return false
	}
	if lookedUpAt.IsZero() {
		lookedUpAt = time.Now()
	}
	q.LookupSignals = next
	q.LookupWindows = nil
	q.LookedUpAt = lookedUpAt
	return true
}

// ClearObservationSignals removes header snapshots and preserves normalized lookup windows.
func (q *QuotaState) ClearObservationSignals() bool {
	if q == nil {
		return false
	}
	clearLookupTime := len(q.LookupWindows) == 0 && !q.LookedUpAt.IsZero()
	if len(q.Signals) == 0 && q.ObservedAt.IsZero() && len(q.LookupSignals) == 0 && !clearLookupTime {
		return false
	}
	q.Signals = nil
	q.ObservedAt = time.Time{}
	q.LookupSignals = nil
	if clearLookupTime {
		q.LookedUpAt = time.Time{}
	}
	return true
}

// applyObservationFields writes only observation fields, leaving cooldown state alone.
func applyObservationFields(dst *QuotaState, observation QuotaState) {
	if dst == nil {
		return
	}
	dst.ObservedAt = observation.ObservedAt
	dst.Signals = observation.Signals
	dst.LookedUpAt = observation.LookedUpAt
	dst.LookupSignals = observation.LookupSignals
	dst.LookupWindows = observation.LookupWindows
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

// mergeQuotaObservation keeps the newest response and lookup snapshots, each
// independently, instead of unioning signals captured at different times, so
// merging an older snapshot can never resurrect a stale watermark.
func mergeQuotaObservation(target, source QuotaState) QuotaState {
	if !source.ObservedAt.IsZero() && !source.ObservedAt.Before(target.ObservedAt) {
		target.ObservedAt = source.ObservedAt
		target.Signals = cloneSignals(source.Signals)
	}
	if !source.LookedUpAt.IsZero() && !source.LookedUpAt.Before(target.LookedUpAt) {
		target.LookedUpAt = source.LookedUpAt
		target.LookupSignals = cloneSignals(source.LookupSignals)
		target.LookupWindows = cloneQuotaWindows(source.LookupWindows)
	}
	return target
}

func rejectedQuotaSignalsForCooldown(provider string, headers http.Header) map[string]string {
	if strings.EqualFold(strings.TrimSpace(provider), "codex") {
		signals := collectQuotaSignals("codex", headers)
		if hasRejectedCodexSignals(signals) {
			return signals
		}
		return nil
	}
	return rejectedClaudeSignalsForCooldown(provider, headers)
}

func sanitizePersistedCooldownSignals(provider string, raw map[string]string) map[string]string {
	if !strings.EqualFold(provider, "codex") {
		return sanitizePersistedClaudeCooldownSignals(raw)
	}
	headers := make(http.Header, len(raw))
	for key, value := range raw {
		headers.Set(key, value)
	}
	return rejectedQuotaSignalsForCooldown("codex", headers)
}
