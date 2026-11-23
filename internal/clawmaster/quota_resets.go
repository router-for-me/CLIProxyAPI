package clawmaster

import (
	"bytes"
	"encoding/json"
	"slices"
	"time"
)

// ClaudeResetGrants counts the limit resets a subscription has banked: grants Anthropic gives an account
// that its owner may claim to clear a limit early. Each count is the resets left on grants that are not
// paused and have not ended, and that clear that limit; a grant that clears several counts for each.
type ClaudeResetGrants struct {
	Weekly   int // grants that clear the week (seven_day, or that give the week back)
	Fable    int // grants that clear Fable's weekly allowance (seven_day_overage_included)
	FiveHour int // grants that clear the five-hour window (five_hour)
}

// claudeResetGrant is one grant of the usage endpoint's cedar_ember block, as Claude Code reads it.
type claudeResetGrant struct {
	ResetsLeft      *int     `json:"resets_left"`
	Paused          bool     `json:"paused"`
	EndsAt          *string  `json:"ends_at"`
	Clears          []string `json:"clears"`
	GivesBackWeekly bool     `json:"gives_back_weekly"`
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
		if grant.GivesBackWeekly || slices.Contains(grant.Clears, "seven_day") {
			grants.Weekly += left
		}
		if slices.Contains(grant.Clears, "seven_day_overage_included") {
			grants.Fable += left
		}
		if slices.Contains(grant.Clears, "five_hour") {
			grants.FiveHour += left
		}
	}
	return grants, true, nil
}
