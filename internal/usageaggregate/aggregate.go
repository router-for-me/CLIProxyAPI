// Package usageaggregate keeps a bounded, in-memory aggregation of the token
// usage records emitted by the proxy runtime. The management API uses it to
// expose the input/cache/output/reasoning token distribution, which is not
// derivable from the request counters returned by the existing usage endpoints.
package usageaggregate

import (
	"context"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// maxTrackedCredentials bounds the number of distinct credential buckets kept
// in memory. Additional credentials observed beyond the limit are folded into
// the running totals so the aggregate never grows without bound.
const maxTrackedCredentials = 4096

func init() {
	coreusage.RegisterPlugin(defaultAggregator)
}

// CredentialUsage reports the token distribution attributed to one credential.
type CredentialUsage struct {
	Provider  string                   `json:"provider,omitempty"`
	AuthID    string                   `json:"auth_id,omitempty"`
	AuthIndex string                   `json:"auth_index,omitempty"`
	Requests  int64                    `json:"requests"`
	Breakdown coreusage.TokenBreakdown `json:"breakdown"`
}

// ProviderUsage reports the token distribution attributed to one provider.
type ProviderUsage struct {
	Provider  string                   `json:"provider"`
	Requests  int64                    `json:"requests"`
	Breakdown coreusage.TokenBreakdown `json:"breakdown"`
}

// UsageSnapshot is the immutable view returned by Snapshot.
type UsageSnapshot struct {
	Requests    int64                    `json:"requests"`
	Breakdown   coreusage.TokenBreakdown `json:"breakdown"`
	Providers   []ProviderUsage          `json:"providers"`
	Credentials []CredentialUsage        `json:"credentials"`
}

type entry struct {
	breakdown coreusage.TokenBreakdown
	requests  int64
	// quality tracks the worst TokenAccountingQuality seen for this bucket.
	quality coreusage.TokenAccountingQuality
}

type credentialEntry struct {
	entry
	provider  string
	authID    string
	authIndex string
}

type aggregator struct {
	mu       sync.Mutex
	overall  entry
	byCred   map[string]*credentialEntry
	byProv   map[string]*entry
	revision atomic.Uint64
}

var defaultAggregator = &aggregator{
	byCred: make(map[string]*credentialEntry),
	byProv: make(map[string]*entry),
}

// HandleUsage implements coreusage.Plugin.
func (a *aggregator) HandleUsage(_ context.Context, record coreusage.Record) {
	if a == nil || record.Failed {
		return
	}
	breakdown := breakdownFor(record.Detail)
	if !hasTokens(breakdown) {
		return
	}

	// Advance the revision before mutating so a polling persistence loop always
	// observes a change for every recorded request.
	a.revision.Add(1)

	provider := normalizeProvider(record.Provider)
	authID := strings.TrimSpace(record.AuthID)
	authIndex := strings.TrimSpace(record.AuthIndex)

	a.mu.Lock()
	defer a.mu.Unlock()

	addEntry(&a.overall, breakdown)

	provKey := provider
	if provKey == "" {
		provKey = "unknown"
	}
	addEntry(a.providerEntry(provKey), breakdown)

	credKey := provKey + "\x00" + authID + "\x00" + authIndex
	if cred, ok := a.byCred[credKey]; ok {
		addEntry(&cred.entry, breakdown)
		return
	}
	if len(a.byCred) >= maxTrackedCredentials {
		return
	}
	cred := &credentialEntry{
		provider:  provider,
		authID:    authID,
		authIndex: authIndex,
		entry:     entry{quality: coreusage.TokenAccountingQualityComplete},
	}
	addEntry(&cred.entry, breakdown)
	a.byCred[credKey] = cred
}

func (a *aggregator) providerEntry(key string) *entry {
	if prov, ok := a.byProv[key]; ok {
		return prov
	}
	prov := &entry{quality: coreusage.TokenAccountingQualityComplete}
	a.byProv[key] = prov
	return prov
}

// Snapshot returns a copy of the current aggregation.
func Snapshot() UsageSnapshot {
	return defaultAggregator.snapshot()
}

// Revision returns a monotonic counter that advances whenever the aggregate
// changes. It is a cheap atomic read suitable for polling by a background
// persistence loop.
func Revision() uint64 {
	return defaultAggregator.revision.Load()
}

// Restore replaces the aggregate with a previously persisted snapshot. Sections
// absent from the snapshot are cleared rather than merged, so a restore is
// idempotent.
func Restore(snapshot UsageSnapshot) {
	defaultAggregator.restore(snapshot)
}

func (a *aggregator) restore(snapshot UsageSnapshot) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.overall = entry{
		breakdown: snapshot.Breakdown,
		requests:  snapshot.Requests,
		quality:   snapshot.Breakdown.Quality,
	}

	byProv := make(map[string]*entry, len(snapshot.Providers))
	for _, provider := range snapshot.Providers {
		byProv[provider.Provider] = &entry{
			breakdown: provider.Breakdown,
			requests:  provider.Requests,
			quality:   provider.Breakdown.Quality,
		}
	}
	a.byProv = byProv

	byCred := make(map[string]*credentialEntry, len(snapshot.Credentials))
	for _, cred := range snapshot.Credentials {
		provKey := normalizeProvider(cred.Provider)
		if provKey == "" {
			provKey = "unknown"
		}
		entryCred := &credentialEntry{
			provider:  cred.Provider,
			authID:    cred.AuthID,
			authIndex: cred.AuthIndex,
			entry: entry{
				breakdown: cred.Breakdown,
				requests:  cred.Requests,
				quality:   cred.Breakdown.Quality,
			},
		}
		byCred[provKey+"\x00"+cred.AuthID+"\x00"+cred.AuthIndex] = entryCred
	}
	a.byCred = byCred

	a.revision.Add(1)
}

func (a *aggregator) snapshot() UsageSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := UsageSnapshot{
		Requests:    a.overall.requests,
		Breakdown:   finalize(a.overall),
		Providers:   make([]ProviderUsage, 0, len(a.byProv)),
		Credentials: make([]CredentialUsage, 0, len(a.byCred)),
	}

	for provider, entry := range a.byProv {
		out.Providers = append(out.Providers, ProviderUsage{
			Provider:  provider,
			Requests:  entry.requests,
			Breakdown: finalize(*entry),
		})
	}
	sort.Slice(out.Providers, func(i, j int) bool {
		if out.Providers[i].Breakdown.TotalTokens != out.Providers[j].Breakdown.TotalTokens {
			return out.Providers[i].Breakdown.TotalTokens > out.Providers[j].Breakdown.TotalTokens
		}
		return out.Providers[i].Provider < out.Providers[j].Provider
	})

	for _, cred := range a.byCred {
		out.Credentials = append(out.Credentials, CredentialUsage{
			Provider:  cred.provider,
			AuthID:    cred.authID,
			AuthIndex: cred.authIndex,
			Requests:  cred.requests,
			Breakdown: finalize(cred.entry),
		})
	}
	sort.Slice(out.Credentials, func(i, j int) bool {
		if out.Credentials[i].Breakdown.TotalTokens != out.Credentials[j].Breakdown.TotalTokens {
			return out.Credentials[i].Breakdown.TotalTokens > out.Credentials[j].Breakdown.TotalTokens
		}
		if out.Credentials[i].AuthID != out.Credentials[j].AuthID {
			return out.Credentials[i].AuthID < out.Credentials[j].AuthID
		}
		return out.Credentials[i].AuthIndex < out.Credentials[j].AuthIndex
	})

	return out
}

func addEntry(dst *entry, src coreusage.TokenBreakdown) {
	addBreakdown(&dst.breakdown, src)
	dst.requests++
	if qualityRank(src.Quality) > qualityRank(dst.quality) {
		dst.quality = src.Quality
	}
}

func addBreakdown(dst *coreusage.TokenBreakdown, src coreusage.TokenBreakdown) {
	dst.SchemaVersion = coreusage.TokenAccountingSchemaVersion
	dst.TotalTokens += src.TotalTokens
	dst.Input.TotalTokens += src.Input.TotalTokens
	dst.Input.UncachedTokens += src.Input.UncachedTokens
	dst.Input.CacheReadTokens += src.Input.CacheReadTokens
	dst.Input.CacheWriteTokens += src.Input.CacheWriteTokens
	dst.Output.TotalTokens += src.Output.TotalTokens
	dst.Output.NonReasoningTokens += src.Output.NonReasoningTokens
	dst.Output.ReasoningTokens += src.Output.ReasoningTokens
	dst.UnclassifiedTokens += src.UnclassifiedTokens
}

// finalize stamps the aggregate with the accounting quality of its inputs. The
// numeric buckets stay additive, so a sum of complete records is complete.
func finalize(e entry) coreusage.TokenBreakdown {
	breakdown := e.breakdown
	breakdown.SchemaVersion = coreusage.TokenAccountingSchemaVersion
	if e.quality == "" {
		breakdown.Quality = coreusage.TokenAccountingQualityComplete
		return breakdown
	}
	breakdown.Quality = e.quality
	return breakdown
}

func breakdownFor(detail coreusage.Detail) coreusage.TokenBreakdown {
	if detail.TokenBreakdown.SchemaVersion == coreusage.TokenAccountingSchemaVersion {
		return detail.TokenBreakdown
	}
	return coreusage.NewSubsetTokenBreakdown(
		detail.InputTokens,
		detail.CacheReadTokens,
		detail.CacheCreationTokens,
		detail.OutputTokens,
		detail.ReasoningTokens,
		detail.TotalTokens,
	)
}

func hasTokens(breakdown coreusage.TokenBreakdown) bool {
	return breakdown.TotalTokens > 0 ||
		breakdown.Input.TotalTokens > 0 ||
		breakdown.Output.TotalTokens > 0 ||
		breakdown.UnclassifiedTokens > 0
}

func qualityRank(quality coreusage.TokenAccountingQuality) int {
	switch quality {
	case coreusage.TokenAccountingQualityInconsistent:
		return 2
	case coreusage.TokenAccountingQualityUnclassified:
		return 1
	default:
		return 0
	}
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}
