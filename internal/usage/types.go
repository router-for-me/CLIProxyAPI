// Package usage provides bounded, persistent aggregation of final client
// outcomes and completed upstream attempts. It intentionally stores aggregates
// only, never raw client keys or per-request payloads.
package usage

import (
	"math"
	"sort"
	"strings"
	"time"
)

const (
	SnapshotVersion      = 1
	DefaultMaxBuckets    = 50000
	DefaultMaxClientKeys = 10000
	maxPricingVersions   = 64
	maxCostCurrencies    = 32
	defaultFlushInterval = 30 * time.Second
	recentBucketDuration = 10 * time.Minute
	recentBucketCount    = 20
	recentWindowMinutes  = int(recentBucketDuration/time.Minute) * recentBucketCount
	unknownDimension     = "unknown"
	anonymousClientKeyID = "anonymous"
)

// TokenTotals contains both the provider-reported token total and its available
// category breakdown. Category fields may overlap according to provider protocol;
// callers must not derive cost by summing them directly.
type TokenTotals struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	CachedTokens        int64 `json:"cached_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
}

// Metrics is the additive aggregate stored for a usage dimension.
type Metrics struct {
	// Attempts, Success, and Failed describe completed external client
	// requests. They are the stable counters used for user statistics and
	// billing.
	Attempts int64 `json:"attempts"`
	Success  int64 `json:"success"`
	Failed   int64 `json:"failed"`
	// UpstreamAttempts and UpstreamFailedAttempts describe individual
	// credential/model/provider attempts made while serving those requests.
	// They are operational diagnostics and must not be used for billing.
	UpstreamAttempts       int64       `json:"upstream_attempts"`
	UpstreamFailedAttempts int64       `json:"upstream_failed_attempts"`
	Tokens                 TokenTotals `json:"tokens"`
	LatencyMsTotal         int64       `json:"latency_ms_total"`
	LatencySamples         int64       `json:"latency_samples"`
	TTFTMsTotal            int64       `json:"ttft_ms_total"`
	TTFTSamples            int64       `json:"ttft_samples"`
	// EstimatedCostMicros is populated only when the aggregate contains a
	// single currency. The per-currency map remains authoritative.
	EstimatedCostMicros           int64            `json:"estimated_cost_micros"`
	EstimatedCostMicrosByCurrency map[string]int64 `json:"estimated_cost_micros_by_currency,omitempty"`
	UnpricedTokens                int64            `json:"unpriced_tokens"`
	UnpricedAttempts              int64            `json:"unpriced_attempts"`
	OverflowAttempts              int64            `json:"overflow_attempts,omitempty"`
	PricingVersions               map[string]int64 `json:"pricing_versions,omitempty"`
}

// AverageLatencyMs returns the integer average over latency-bearing attempts.
func (m Metrics) AverageLatencyMs() int64 {
	if m.LatencySamples <= 0 {
		return 0
	}
	return m.LatencyMsTotal / m.LatencySamples
}

// AverageTTFTMs returns the integer average over TTFT-bearing attempts.
func (m Metrics) AverageTTFTMs() int64 {
	if m.TTFTSamples <= 0 {
		return 0
	}
	return m.TTFTMsTotal / m.TTFTSamples
}

// Bucket stores one server-local calendar day and one bounded set of dimensions.
type Bucket struct {
	Day         string    `json:"day"`
	ClientKeyID string    `json:"client_key_id"`
	Provider    string    `json:"provider"`
	Model       string    `json:"model"`
	ServiceTier string    `json:"service_tier"`
	FirstUsedAt time.Time `json:"first_used_at"`
	LastUsedAt  time.Time `json:"last_used_at"`
	Metrics     Metrics   `json:"metrics"`
}

// ClientKeyInfo is safe display metadata. MaskedKey never contains the complete
// credential and Raw API keys are not represented in this package's snapshots.
type ClientKeyInfo struct {
	ID          string    `json:"id"`
	Alias       string    `json:"alias,omitempty"`
	MaskedKey   string    `json:"masked_key,omitempty"`
	FirstUsedAt time.Time `json:"first_used_at"`
	LastUsedAt  time.Time `json:"last_used_at"`
}

// OverflowBucket retains attempts whose high-cardinality dimension would exceed
// the configured bucket cap. It remains day-bounded so retention still applies.
type OverflowBucket struct {
	Day     string  `json:"day"`
	Metrics Metrics `json:"metrics"`
}

// Snapshot is the versioned on-disk and JSON export format.
type Snapshot struct {
	Version       int              `json:"version"`
	SavedAt       time.Time        `json:"saved_at"`
	RetentionDays int              `json:"retention_days"`
	Buckets       []Bucket         `json:"buckets"`
	ClientKeys    []ClientKeyInfo  `json:"client_keys"`
	Overflow      []OverflowBucket `json:"overflow,omitempty"`
}

type bucketKey struct {
	day         string
	clientKeyID string
	provider    string
	model       string
	serviceTier string
}

type recentBucketKey struct {
	startUnix   int64
	clientKeyID string
	provider    string
	model       string
	serviceTier string
}

type recentBucket struct {
	StartAt     time.Time
	ClientKeyID string
	Provider    string
	Model       string
	ServiceTier string
	Metrics     Metrics
}

func keyForBucket(bucket Bucket) bucketKey {
	return bucketKey{
		day:         bucket.Day,
		clientKeyID: bucket.ClientKeyID,
		provider:    bucket.Provider,
		model:       bucket.Model,
		serviceTier: bucket.ServiceTier,
	}
}

func cloneMetrics(in Metrics) Metrics {
	out := in
	if len(in.PricingVersions) > 0 {
		out.PricingVersions = make(map[string]int64, len(in.PricingVersions))
		for version, count := range in.PricingVersions {
			out.PricingVersions[version] = count
		}
	}
	if len(in.EstimatedCostMicrosByCurrency) > 0 {
		out.EstimatedCostMicrosByCurrency = make(map[string]int64, len(in.EstimatedCostMicrosByCurrency))
		for currency, amount := range in.EstimatedCostMicrosByCurrency {
			out.EstimatedCostMicrosByCurrency[currency] = amount
		}
	}
	return out
}

func mergeMetrics(dst *Metrics, src Metrics) {
	if dst == nil {
		return
	}
	dst.Attempts = saturatingAdd(dst.Attempts, src.Attempts)
	dst.Success = saturatingAdd(dst.Success, src.Success)
	dst.Failed = saturatingAdd(dst.Failed, src.Failed)
	dst.UpstreamAttempts = saturatingAdd(dst.UpstreamAttempts, src.UpstreamAttempts)
	dst.UpstreamFailedAttempts = saturatingAdd(dst.UpstreamFailedAttempts, src.UpstreamFailedAttempts)
	dst.Tokens.InputTokens = saturatingAdd(dst.Tokens.InputTokens, src.Tokens.InputTokens)
	dst.Tokens.OutputTokens = saturatingAdd(dst.Tokens.OutputTokens, src.Tokens.OutputTokens)
	dst.Tokens.ReasoningTokens = saturatingAdd(dst.Tokens.ReasoningTokens, src.Tokens.ReasoningTokens)
	dst.Tokens.CachedTokens = saturatingAdd(dst.Tokens.CachedTokens, src.Tokens.CachedTokens)
	dst.Tokens.CacheReadTokens = saturatingAdd(dst.Tokens.CacheReadTokens, src.Tokens.CacheReadTokens)
	dst.Tokens.CacheCreationTokens = saturatingAdd(dst.Tokens.CacheCreationTokens, src.Tokens.CacheCreationTokens)
	dst.Tokens.TotalTokens = saturatingAdd(dst.Tokens.TotalTokens, src.Tokens.TotalTokens)
	dst.LatencyMsTotal = saturatingAdd(dst.LatencyMsTotal, src.LatencyMsTotal)
	dst.LatencySamples = saturatingAdd(dst.LatencySamples, src.LatencySamples)
	dst.TTFTMsTotal = saturatingAdd(dst.TTFTMsTotal, src.TTFTMsTotal)
	dst.TTFTSamples = saturatingAdd(dst.TTFTSamples, src.TTFTSamples)
	mergeCurrencyCosts(dst, src)
	dst.UnpricedTokens = saturatingAdd(dst.UnpricedTokens, src.UnpricedTokens)
	dst.UnpricedAttempts = saturatingAdd(dst.UnpricedAttempts, src.UnpricedAttempts)
	dst.OverflowAttempts = saturatingAdd(dst.OverflowAttempts, src.OverflowAttempts)
	mergeVersionCounts(&dst.PricingVersions, src.PricingVersions)
}

func mergeCurrencyCosts(dst *Metrics, src Metrics) {
	if dst == nil {
		return
	}
	if len(dst.EstimatedCostMicrosByCurrency) == 0 && dst.EstimatedCostMicros != 0 {
		dst.EstimatedCostMicrosByCurrency = map[string]int64{"UNKNOWN": nonNegative(dst.EstimatedCostMicros)}
	}
	if dst.EstimatedCostMicrosByCurrency == nil {
		dst.EstimatedCostMicrosByCurrency = make(map[string]int64)
	}
	if len(src.EstimatedCostMicrosByCurrency) == 0 {
		if src.EstimatedCostMicros != 0 {
			addCurrencyCost(dst.EstimatedCostMicrosByCurrency, "UNKNOWN", src.EstimatedCostMicros)
		}
	} else {
		for _, currency := range sortedCurrencyKeys(src.EstimatedCostMicrosByCurrency) {
			amount := src.EstimatedCostMicrosByCurrency[currency]
			addCurrencyCost(dst.EstimatedCostMicrosByCurrency, currency, amount)
		}
	}
	dst.EstimatedCostMicros = singleCurrencyCost(dst.EstimatedCostMicrosByCurrency)
}

func addCurrencyCost(costs map[string]int64, currency string, amount int64) {
	if costs == nil {
		return
	}
	currency = normalizedCurrency(currency)
	if _, exists := costs[currency]; !exists && len(costs) >= maxCostCurrencies-1 {
		currency = "OTHER"
	}
	costs[currency] = saturatingAdd(costs[currency], nonNegative(amount))
}

func sortedCurrencyKeys(costs map[string]int64) []string {
	keys := make([]string, 0, len(costs))
	for currency := range costs {
		keys = append(keys, currency)
	}
	sort.Strings(keys)
	return keys
}

func normalizedCurrency(value string) string {
	value = strings.ToUpper(stripControlRunes(strings.TrimSpace(value)))
	if value == "" {
		return "UNKNOWN"
	}
	runes := []rune(value)
	if len(runes) > 16 {
		return string(runes[:16])
	}
	return value
}

func singleCurrencyCost(costs map[string]int64) int64 {
	if len(costs) != 1 {
		return 0
	}
	for _, amount := range costs {
		return amount
	}
	return 0
}

func normalizeCurrencyCosts(metrics *Metrics) {
	if metrics == nil {
		return
	}
	original := cloneMetrics(*metrics)
	metrics.EstimatedCostMicros = 0
	metrics.EstimatedCostMicrosByCurrency = nil
	mergeCurrencyCosts(metrics, original)
	originalVersions := metrics.PricingVersions
	metrics.PricingVersions = nil
	mergeVersionCounts(&metrics.PricingVersions, originalVersions)
}

func mergeVersionCounts(dst *map[string]int64, src map[string]int64) {
	if len(src) == 0 || dst == nil {
		return
	}
	if *dst == nil {
		*dst = make(map[string]int64)
	}
	versions := *dst
	for _, version := range sortedPricingVersions(src) {
		count := src[version]
		version = sanitizeDisplayValue(strings.TrimSpace(version), 128)
		if version == "" {
			version = "default"
		}
		if _, ok := versions[version]; !ok && len(versions) >= maxPricingVersions-1 {
			version = "other"
		}
		versions[version] = saturatingAdd(versions[version], count)
	}
}

func saturatingAdd(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	if b < 0 && a < math.MinInt64-b {
		return math.MinInt64
	}
	return a + b
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func sortedPricingVersions(values map[string]int64) []string {
	versions := make([]string, 0, len(values))
	for version := range values {
		versions = append(versions, version)
	}
	sort.Strings(versions)
	return versions
}
