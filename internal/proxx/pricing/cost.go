package pricing

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

const usdTicksPerUSD = 1e10

// ApplyCost resolves the record's final cost and source and stores them on a fresh copy
// of Detail.CostDetails. It is a no-op when pricing is disabled or not configured.
func ApplyCost(_ context.Context, record *usage.Record) {
	if record == nil {
		return
	}
	st := currentState()
	if st == nil || st.disabled {
		return
	}
	cost, source := st.resolveCost(record, currentCatalog())
	details := record.Detail.CostDetails.Clone()
	if details == nil {
		details = &usage.CostDetails{}
	}
	details.FinalCost = cost
	details.CostSource = source
	record.Detail.CostDetails = details
}

// resolveCost applies the ADR 0004 priority order:
// override price → upstream body cost → cost header → config price → catalog price → unknown.
func (st *state) resolveCost(record *usage.Record, c *Catalog) (*float64, string) {
	match, found := st.lookupRecord(record, c)
	if found && (match.Override || st.overrideUpstream) {
		if cost, ok := tokenCost(record, match.Price); ok {
			return &cost, match.Source
		}
	}
	if cost, ok := upstreamBodyCost(record.Detail.CostDetails); ok {
		return &cost, SourceUpstream
	}
	if cost, ok := headerCost(record.ResponseHeaders, st.costHeaders); ok {
		return &cost, SourceHeader
	}
	if found {
		if cost, ok := tokenCost(record, match.Price); ok {
			return &cost, match.Source
		}
	}
	return nil, SourceUnknown
}

// lookupRecord prices the model that actually served the request: record.Model first,
// then the upstream-reported response model. Alias only matches config entries.
func (st *state) lookupRecord(record *usage.Record, c *Catalog) (Match, bool) {
	if match, ok := lookup(st.entries, c, record.Provider, record.Model, record.Alias); ok {
		return match, true
	}
	if rm := strings.TrimSpace(record.ResponseModel); rm != "" && !strings.EqualFold(rm, record.Model) {
		return lookup(st.entries, c, record.Provider, rm, "")
	}
	return Match{}, false
}

func upstreamBodyCost(cd *usage.CostDetails) (float64, bool) {
	if cd == nil {
		return 0, false
	}
	if cd.Cost != nil && *cd.Cost >= 0 {
		// OpenRouter BYOK reports cost 0 and the provider charge separately.
		if *cd.Cost == 0 && cd.IsBYOK != nil && *cd.IsBYOK && cd.UpstreamInferenceCost != nil && *cd.UpstreamInferenceCost > 0 {
			return *cd.UpstreamInferenceCost, true
		}
		return *cd.Cost, true
	}
	if cd.CostInUSDTicks != nil && *cd.CostInUSDTicks >= 0 {
		return float64(*cd.CostInUSDTicks) / usdTicksPerUSD, true
	}
	return 0, false
}

func headerCost(headers http.Header, names []string) (float64, bool) {
	if len(headers) == 0 || len(names) == 0 {
		return 0, false
	}
	for _, name := range names {
		raw := strings.TrimSpace(headers.Get(name))
		raw = strings.TrimPrefix(raw, "$")
		if raw == "" {
			continue
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err == nil && v >= 0 {
			return v, true
		}
	}
	return 0, false
}

// tokenCost prices the canonical, non-overlapping token buckets of a record:
//
//	uncached input × input + cache read × cache_read + cache write × cache_write
//	+ non-reasoning output × output + reasoning × (reasoning price, else output)
//
// The buckets come from usage.TokenBreakdown, which already normalizes provider semantics
// (OpenAI prompt_tokens include cached tokens; Anthropic input_tokens exclude cache
// reads/writes; OpenAI completion tokens include reasoning). Missing cache prices fall back
// to the input price. It returns false when a needed price is unknown or the breakdown
// cannot be classified.
func tokenCost(record *usage.Record, price Price) (float64, bool) {
	detail := record.Detail
	if !detail.TokenBreakdown.Valid() {
		detail = usage.EnsureTokenBreakdownForProvider(detail, record.Provider, record.ExecutorType)
	}
	b := detail.TokenBreakdown
	if b.TotalTokens == 0 {
		return 0, true
	}
	if b.Quality != usage.TokenAccountingQualityComplete || b.UnclassifiedTokens > 0 {
		return 0, false
	}
	var total float64
	add := func(tokens int64, primary, fallback *float64) bool {
		if tokens == 0 {
			return true
		}
		p := primary
		if p == nil {
			p = fallback
		}
		if p == nil {
			return false
		}
		total += float64(tokens) * *p / 1e6
		return true
	}
	if !add(b.Input.UncachedTokens, price.Input, nil) ||
		!add(b.Input.CacheReadTokens, price.CacheRead, price.Input) ||
		!add(b.Input.CacheWriteTokens, price.CacheWrite, price.Input) ||
		!add(b.Output.NonReasoningTokens, price.Output, nil) ||
		!add(b.Output.ReasoningTokens, price.Reasoning, price.Output) {
		return 0, false
	}
	return total, true
}
