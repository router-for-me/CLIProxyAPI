package clawmaster

import (
	"bytes"
	"encoding/json"
	"slices"
	"time"
)

// ClaudeResetGrants counts the limit resets a subscription has banked: grants Anthropic gives an account
// that its owner may claim to clear a limit early. Each count is the resets left on grants that are not
// paused and have not ended. A grant counts once, under the largest limit it clears, as claude.ai's usage
// page lists it ("Weekly reset", "Fable reset"): a Fable reset that also gives back the week's share
// Fable used is a Fable reset.
type ClaudeResetGrants struct {
	Weekly   int // grants that clear the week (seven_day), a weekly or a full reset
	Fable    int // grants that clear Fable's weekly allowance (seven_day_overage_included) but not the week
	FiveHour int // grants that clear only the five-hour window (five_hour)
}

// claudeResetGrant is one grant of the usage endpoint's cedar_ember block, as Claude Code reads it.
type claudeResetGrant struct {
	ResetsLeft *int     `json:"resets_left"`
	Paused     bool     `json:"paused"`
	EndsAt     *string  `json:"ends_at"`
	Clears     []string `json:"clears"`
}

// ParseClaudeResetGrants reads the reset grants of a usage response fetched with cedar_ember=1: the
// cedar_ember block's grants. A response without the block is unknown, not an error; an account the
// block calls ineligible has none. A malformed grant is skipped, as Claude Code skips it.
func ParseClaudeResetGrants(payload []byte, now time.Time) (ClaudeResetGrants, bool, error) {
	var root struct {
		CedarEmber *struct {
			Eligible bool              `json:"eligible"`
			Grants   []json.RawMessage `json:"grants"`
		} `json:"cedar_ember"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&root); err != nil {
		var probe any
		if json.Unmarshal(payload, &probe) == nil {
			// Valid JSON of another shape (a block that is not an object): unknown.
			return ClaudeResetGrants{}, false, nil
		}
		return ClaudeResetGrants{}, false, err
	}
	if root.CedarEmber == nil {
		return ClaudeResetGrants{}, false, nil
	}
	var grants ClaudeResetGrants
	if !root.CedarEmber.Eligible {
		return grants, true, nil
	}
	for _, raw := range root.CedarEmber.Grants {
		var grant claudeResetGrant
		if json.Unmarshal(raw, &grant) != nil || grant.ResetsLeft == nil || *grant.ResetsLeft <= 0 || grant.Paused {
			continue
		}
		if grant.EndsAt != nil {
			if ends, err := time.Parse(time.RFC3339, *grant.EndsAt); err == nil && !ends.After(now) {
				continue
			}
		}
		left := *grant.ResetsLeft
		switch {
		case slices.Contains(grant.Clears, "seven_day"):
			grants.Weekly += left
		case slices.Contains(grant.Clears, "seven_day_overage_included"):
			grants.Fable += left
		case slices.Contains(grant.Clears, "five_hour"):
			grants.FiveHour += left
		}
	}
	return grants, true, nil
}

// resetGrantFields are the parts of the cedar_ember block worth logging: whether the account may claim a
// reset and why not, and what each grant clears and allows. Grant ids and labels are left out.
var (
	resetBlockFields = []string{"eligible", "ineligible_reason", "at_limit", "exhausted", "cooldown_until"}
	resetGrantFields = []string{"type", "clears", "resets_left", "resets_total", "paused", "usable_now", "use_requires_limit", "gives_back_weekly", "blocking", "starts_at", "ends_at", "unavailable_reason"}
)

// DescribeClaudeResetGrants is the cedar_ember block of a usage response as one compact line for the log,
// or "" when the response has none: what Anthropic said, so a count the pool exports can be checked.
func DescribeClaudeResetGrants(payload []byte) string {
	var root struct {
		CedarEmber map[string]json.RawMessage `json:"cedar_ember"`
	}
	if json.Unmarshal(payload, &root) != nil || root.CedarEmber == nil {
		return ""
	}
	pick := func(from map[string]json.RawMessage, fields []string) map[string]json.RawMessage {
		out := make(map[string]json.RawMessage, len(fields))
		for _, field := range fields {
			if value, ok := from[field]; ok {
				out[field] = value
			}
		}
		return out
	}
	block := pick(root.CedarEmber, resetBlockFields)
	var grants []map[string]json.RawMessage
	if json.Unmarshal(root.CedarEmber["grants"], &grants) == nil {
		picked := make([]map[string]json.RawMessage, 0, len(grants))
		for _, grant := range grants {
			picked = append(picked, pick(grant, resetGrantFields))
		}
		raw, _ := json.Marshal(picked)
		block["grants"] = raw
	}
	line, err := json.Marshal(block)
	if err != nil {
		return ""
	}
	return string(line)
}
