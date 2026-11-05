// Package ollama implements a read-only client for Ollama Cloud account
// endpoints (usage and balance) backing the v8 management quota API.
//
// The upstream contract is defined by Ollama's official OpenAPI document
// (https://github.com/ollama/ollama/blob/main/docs/openapi.yaml). All money
// fields are USD numbers. The client never accepts a configurable origin: the
// fixed https://ollama.com host and its /api/usage and /api/balance paths are
// the only request targets, which keeps the management endpoint free of SSRF
// and credential-exfiltration surface.
package ollama

import (
	"encoding/json"
	"time"
)

// UsageRange is an allowed /api/usage range value.
type UsageRange string

const (
	UsageRange24h UsageRange = "24h"
	UsageRange7d  UsageRange = "7d"
	UsageRange30d UsageRange = "30d"
)

// Valid reports whether the range is one of the official values.
func (r UsageRange) Valid() bool {
	switch r {
	case UsageRange24h, UsageRange7d, UsageRange30d:
		return true
	}
	return false
}

// UsageScope is an allowed /api/usage scope value.
type UsageScope string

const (
	UsageScopeSelf UsageScope = "self"
)

// Valid reports whether the scope is accepted by the management endpoint.
// Team scope requires team-admin eligibility on the Ollama side and is not
// exposed by the management API.
func (s UsageScope) Valid() bool {
	return s == UsageScopeSelf
}

// UsageMetrics carries the counters reported by /api/usage. Only
// RequestCount is guaranteed present; the remaining fields are optional in
// the upstream schema and stay nullable here.
type UsageMetrics struct {
	RequestCount      int64    `json:"request_count"`
	UsageUSD          *float64 `json:"usage_usd,omitempty"`
	InputTokens       *int64   `json:"input_tokens,omitempty"`
	CachedInputTokens *int64   `json:"cached_input_tokens,omitempty"`
	OutputTokens      *int64   `json:"output_tokens,omitempty"`
}

// UsageGroup is a per-model usage breakdown (present when group_by=model).
type UsageGroup struct {
	UsageMetrics

	Model string `json:"model"`
}

// UsageSummary is the metrics for a range or bucket, with optional grouping.
type UsageSummary struct {
	UsageMetrics

	Groups    []UsageGroup  `json:"groups,omitempty"`
	Remainder *UsageMetrics `json:"remainder,omitempty"`
}

// UsageBucket is one hour or day of usage, in chronological order. Buckets
// without recorded requests are included by the upstream with zero counters.
type UsageBucket struct {
	UsageSummary

	From    string `json:"from"`
	Until   string `json:"until"`
	Partial bool   `json:"partial,omitempty"`
}

// UsageResponse is the /api/usage response for a selected range and scope.
type UsageResponse struct {
	Range       string        `json:"range"`
	Scope       string        `json:"scope"`
	Granularity string        `json:"granularity"`
	GroupBy     *string       `json:"group_by,omitempty"`
	From        string        `json:"from"`
	Until       string        `json:"until"`
	Totals      UsageSummary  `json:"totals"`
	Buckets     []UsageBucket `json:"buckets"`
}

// BalancePeriod is the included-credit billing period in UTC.
type BalancePeriod struct {
	From  string `json:"from"`
	Until string `json:"until"`
}

// IncludedBalance is the modern included-credit shape: remaining and total
// allowance in USD for the current period.
type IncludedBalance struct {
	BalanceUSD   *float64       `json:"balance_usd"`
	AllowanceUSD *float64       `json:"allowance_usd"`
	Period       *BalancePeriod `json:"period,omitempty"`
}

// LegacyBalanceLimit is one legacy plan limit expressed as a percentage.
type LegacyBalanceLimit struct {
	RemainingPercent *float64 `json:"remaining_percent"`
	ResetsAt         string   `json:"resets_at,omitempty"`
}

// LegacyIncludedBalance is the legacy plan-limit shape (session and weekly
// percentages) returned for older accounts.
type LegacyIncludedBalance struct {
	Session *LegacyBalanceLimit `json:"session,omitempty"`
	Weekly  *LegacyBalanceLimit `json:"weekly,omitempty"`
}

// PurchasedBalance is the remaining unexpired purchased credits in USD.
type PurchasedBalance struct {
	BalanceUSD *float64 `json:"balance_usd"`
}

// BalanceResponse is the /api/balance response. The upstream schema declares
// included as a oneOf: exactly one of Included (modern credits) or Legacy
// (legacy plan limits) is populated. Both are serialized back under the
// upstream's single included field to keep the wire format faithful.
type BalanceResponse struct {
	Included  *IncludedBalance       `json:"-"`
	Legacy    *LegacyIncludedBalance `json:"-"`
	Purchased *PurchasedBalance      `json:"purchased,omitempty"`
}

// UnmarshalJSON resolves the upstream oneOf on included: a payload carrying
// session or weekly limits is legacy, anything else is the modern shape.
func (b *BalanceResponse) UnmarshalJSON(data []byte) error {
	var aux struct {
		Included  json.RawMessage   `json:"included"`
		Purchased *PurchasedBalance `json:"purchased"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	b.Included = nil
	b.Legacy = nil
	b.Purchased = aux.Purchased
	if len(aux.Included) == 0 {
		return nil
	}

	var probe struct {
		Session *LegacyBalanceLimit `json:"session"`
		Weekly  *LegacyBalanceLimit `json:"weekly"`
	}
	if err := json.Unmarshal(aux.Included, &probe); err != nil {
		return err
	}
	if probe.Session != nil || probe.Weekly != nil {
		var legacy LegacyIncludedBalance
		if err := json.Unmarshal(aux.Included, &legacy); err != nil {
			return err
		}
		b.Legacy = &legacy
		return nil
	}

	var included IncludedBalance
	if err := json.Unmarshal(aux.Included, &included); err != nil {
		return err
	}
	b.Included = &included
	return nil
}

// MarshalJSON writes the populated included shape back under the upstream's
// single included field.
func (b *BalanceResponse) MarshalJSON() ([]byte, error) {
	out := struct {
		Included  json.RawMessage   `json:"included,omitempty"`
		Purchased *PurchasedBalance `json:"purchased,omitempty"`
	}{Purchased: b.Purchased}
	if b.Included != nil {
		raw, err := json.Marshal(b.Included)
		if err != nil {
			return nil, err
		}
		out.Included = raw
	} else if b.Legacy != nil {
		raw, err := json.Marshal(b.Legacy)
		if err != nil {
			return nil, err
		}
		out.Included = raw
	}
	return json.Marshal(out)
}

// QuotaResponse is the management API response for one Ollama Cloud
// connection. Usage and Balance are nullable to express partial success: a
// section is omitted (with its error message set) when only that upstream
// call failed. No credential material is ever included. FetchedAt is the UTC
// instant the upstream data was read, serialized as ISO-8601.
type QuotaResponse struct {
	Provider     string           `json:"provider"`
	ConnectionID string           `json:"connection_id"`
	DisplayName  string           `json:"display_name"`
	Scope        UsageScope       `json:"scope"`
	Range        UsageRange       `json:"range"`
	FetchedAt    time.Time        `json:"fetched_at"`
	Usage        *UsageResponse   `json:"usage,omitempty"`
	Balance      *BalanceResponse `json:"balance,omitempty"`
	UsageError   string           `json:"usage_error,omitempty"`
	BalanceError string           `json:"balance_error,omitempty"`
	Source       string           `json:"source"`
}
