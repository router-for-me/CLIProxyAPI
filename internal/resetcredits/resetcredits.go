// Package resetcredits keeps an account's rate-limit reset credits in its auth metadata, for every
// provider that grants them (Codex, Claude), and names the redeem outcomes the desktop API reports.
package resetcredits

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Auth metadata keys holding the last lookup, so it survives restarts.
const (
	availableKey  = "reset_credits_available"
	applicableKey = "reset_credits_applicable"
	checkedAtKey  = "reset_credits_checked_at"
	expiriesKey   = "reset_credits_expiries"
	grantsKey     = "reset_credits_grants"
)

// Redeem outcome codes, as POST /usage/reset-credit reports them.
const (
	// Reset means a credit was spent; what it cleared shows in the next usage lookup.
	Reset = "reset"
	// NothingToReset means no limit needed clearing, so no credit was spent.
	NothingToReset = "nothing_to_reset"
	// NoCredit means the account holds no usable credit.
	NoCredit = "no_credit"
	// AlreadyRedeemed means this redeem request was used before.
	AlreadyRedeemed = "already_redeemed"
)

// Credits are an account's rate-limit reset credits as of its last lookup.
type Credits struct {
	// Available is how many credits the account holds.
	Available int
	// Applicable is how many can be used now.
	Applicable int
	CheckedAt  time.Time
	// Expiries are the held credits' expiry times, soonest first. A credit that never expires, or one
	// whose expiry lookup failed, has none, so there may be fewer than Available.
	Expiries []time.Time
	// Grants is nil for providers or saved lookups without grant details.
	Grants []Grant
}

// Grant preserves the scope and availability of one independently redeemable batch.
type Grant struct {
	ID                string     `json:"id"`
	Label             string     `json:"label"`
	Scope             string     `json:"scope"`
	Available         int        `json:"available"`
	Applicable        int        `json:"applicable"`
	ExpiresAt         *time.Time `json:"expires_at"`
	Clears            []string   `json:"clears"`
	GivesBackWeekly   *bool      `json:"gives_back_weekly"`
	UnavailableReason *string    `json:"unavailable_reason"`
}

// Same reports equal counts and expiries, ignoring when they were checked.
func (c Credits) Same(other Credits) bool {
	return c.Available == other.Available && c.Applicable == other.Applicable &&
		slices.EqualFunc(c.Expiries, other.Expiries, time.Time.Equal) && reflect.DeepEqual(c.Grants, other.Grants)
}

// Of returns the credential's last looked-up reset credits; false when never reported.
func Of(auth *coreauth.Auth) (Credits, bool) {
	if auth == nil || auth.Metadata == nil {
		return Credits{}, false
	}
	available, okAvailable := metadataInt(auth.Metadata[availableKey])
	applicable, okApplicable := metadataInt(auth.Metadata[applicableKey])
	checkedText, _ := auth.Metadata[checkedAtKey].(string)
	checkedAt, errParse := time.Parse(time.RFC3339, strings.TrimSpace(checkedText))
	if !okAvailable || !okApplicable || errParse != nil {
		return Credits{}, false
	}
	return Credits{Available: available, Applicable: applicable, CheckedAt: checkedAt, Expiries: metadataTimes(auth.Metadata[expiriesKey]), Grants: metadataGrants(auth.Metadata[grantsKey])}, true
}

// Store records a lookup made at checkedAt into metadata. Nil credits mean the account reports none,
// which clears the counts; credits.CheckedAt is ignored.
func Store(metadata map[string]any, checkedAt time.Time, credits *Credits) {
	metadata[checkedAtKey] = checkedAt.UTC().Format(time.RFC3339)
	if credits == nil {
		delete(metadata, availableKey)
		delete(metadata, applicableKey)
		delete(metadata, expiriesKey)
		delete(metadata, grantsKey)
		return
	}
	metadata[availableKey] = credits.Available
	metadata[applicableKey] = credits.Applicable
	if credits.Grants == nil {
		delete(metadata, grantsKey)
	} else {
		grants := make([]any, 0, len(credits.Grants))
		for _, grant := range credits.Grants {
			item := map[string]any{"id": grant.ID, "label": grant.Label, "scope": grant.Scope,
				"available": grant.Available, "applicable": grant.Applicable, "clears": slices.Clone(grant.Clears)}
			if grant.ExpiresAt != nil {
				item["expires_at"] = grant.ExpiresAt.UTC().Format(time.RFC3339Nano)
			}
			if grant.GivesBackWeekly != nil {
				item["gives_back_weekly"] = *grant.GivesBackWeekly
			}
			if grant.UnavailableReason != nil {
				item["unavailable_reason"] = *grant.UnavailableReason
			}
			grants = append(grants, item)
		}
		metadata[grantsKey] = grants
	}
	if len(credits.Expiries) == 0 {
		delete(metadata, expiriesKey)
		return
	}
	expiries := make([]string, len(credits.Expiries))
	for i, at := range credits.Expiries {
		expiries[i] = at.UTC().Format(time.RFC3339)
	}
	metadata[expiriesKey] = expiries
}

func metadataGrants(value any) []Grant {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	grants := make([]Grant, 0, len(raw))
	for _, value := range raw {
		item, okItem := value.(map[string]any)
		if !okItem {
			continue
		}
		id, _ := item["id"].(string)
		label, _ := item["label"].(string)
		scope, _ := item["scope"].(string)
		available, okAvailable := metadataInt(item["available"])
		applicable, okApplicable := metadataInt(item["applicable"])
		if id == "" || !okAvailable || !okApplicable {
			continue
		}
		grant := Grant{ID: id, Label: label, Scope: scope, Available: available, Applicable: applicable, Clears: []string{}}
		switch clears := item["clears"].(type) {
		case []string:
			grant.Clears = slices.Clone(clears)
		case []any:
			for _, clear := range clears {
				if text, okText := clear.(string); okText {
					grant.Clears = append(grant.Clears, text)
				}
			}
		}
		if text, okText := item["expires_at"].(string); okText {
			if at, errParse := time.Parse(time.RFC3339Nano, text); errParse == nil {
				at = at.UTC()
				grant.ExpiresAt = &at
			}
		}
		if givesBack, okBool := item["gives_back_weekly"].(bool); okBool {
			grant.GivesBackWeekly = &givesBack
		}
		if reason, okText := item["unavailable_reason"].(string); okText {
			grant.UnavailableReason = &reason
		}
		grants = append(grants, grant)
	}
	return grants
}

// metadataTimes reads RFC 3339 times stored as []string, or as []any after a JSON round trip.
func metadataTimes(value any) []time.Time {
	var raw []string
	switch v := value.(type) {
	case []string:
		raw = v
	case []any:
		for _, item := range v {
			if text, ok := item.(string); ok {
				raw = append(raw, text)
			}
		}
	}
	var out []time.Time
	for _, text := range raw {
		if at, errParse := time.Parse(time.RFC3339, text); errParse == nil {
			out = append(out, at)
		}
	}
	return out
}

// metadataInt reads a count stored as an int, or as a float64 after a JSON round trip.
func metadataInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	case json.Number:
		n, errInt := v.Int64()
		return int(n), errInt == nil
	}
	return 0, false
}
