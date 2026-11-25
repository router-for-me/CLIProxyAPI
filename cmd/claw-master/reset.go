package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clawmaster"
)

// formatResetReport says what a reset command found and did: the week's and Fable's use, the grant a claim
// spends, and Anthropic's answer. It names no account, organization or grant id.
func formatResetReport(r clawmaster.ClaudeResetReport, claim bool, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Now: %s\n", formatResetUsage(r.Before, now))
	if !r.Before.Eligible {
		reason := r.Before.IneligibleReason
		if reason == "" {
			reason = "unknown"
		}
		fmt.Fprintf(&b, "This account cannot claim limit resets (Anthropic says: %s).\n", reason)
		return b.String()
	}
	fmt.Fprintf(&b, "Banked: %d weekly, %d Fable", r.Before.Weekly, r.Before.FableResets)
	if r.Before.FiveHourOnly > 0 {
		fmt.Fprintf(&b, ", %d five-hour", r.Before.FiveHourOnly)
	}
	b.WriteString("\n")
	if r.Grant == nil {
		fmt.Fprintf(&b, "No %s reset is banked.\n", r.Kind)
		return b.String()
	}
	g := r.Grant
	fmt.Fprintf(&b, "The %s reset clears %s", r.Kind, describeClears(g.Clears))
	if g.GivesBackWeekly {
		b.WriteString(" and gives the week back what Fable used")
	}
	fmt.Fprintf(&b, "; %d left", g.ResetsLeft)
	if !g.EndsAt.IsZero() {
		fmt.Fprintf(&b, ", until %s", g.EndsAt.UTC().Format("2006-01-02 15:04 MST"))
	}
	b.WriteString(".\n")
	if !g.UsableNow {
		why := g.Unavailable
		if why == "" {
			why = "not usable now"
		}
		if r.Before.CooldownUntil != "" {
			why += ", cooldown until " + r.Before.CooldownUntil
		}
		fmt.Fprintf(&b, "It cannot be claimed now (Anthropic says: %s).\n", why)
		return b.String()
	}
	if !claim {
		if r.Before.Week != nil && r.Before.Fable != nil && g.GivesBackWeekly {
			back := *r.Before.Fable * 0.5
			fmt.Fprintf(&b, "Claiming it should leave the week about %s used and Fable at 0%%.\n", percent(max(*r.Before.Week-back, 0)))
		}
		b.WriteString("Nothing was claimed. Run again with --claim to spend it.\n")
		return b.String()
	}
	if !r.Claimed {
		return b.String()
	}
	c := r.Claim
	fmt.Fprintf(&b, "Claimed: %s", c.Result)
	if c.Reason != "" {
		fmt.Fprintf(&b, " (%s)", c.Reason)
	}
	if len(c.Cleared) > 0 {
		fmt.Fprintf(&b, "; cleared %s", describeClears(c.Cleared))
	}
	if c.ResetsLeft != nil {
		fmt.Fprintf(&b, "; %d left", *c.ResetsLeft)
	}
	b.WriteString(".\n")
	if r.After != nil {
		fmt.Fprintf(&b, "After: %s\n", formatResetUsage(*r.After, now))
	}
	return b.String()
}

func formatResetUsage(u clawmaster.ClaudeResetUsage, now time.Time) string {
	parts := []string{"week " + optionalPercent(u.Week) + " used", "Fable " + optionalPercent(u.Fable) + " used"}
	if !u.WeekResetsAt.IsZero() {
		parts = append(parts, fmt.Sprintf("the week resets in %.0fh", u.WeekResetsAt.Sub(now).Hours()))
	}
	return strings.Join(parts, ", ")
}

func optionalPercent(f *float64) string {
	if f == nil {
		return "unknown"
	}
	return percent(*f)
}

func percent(f float64) string { return fmt.Sprintf("%.0f%%", f*100) }

func describeClears(clears []string) string {
	names := make([]string, 0, len(clears))
	for _, c := range clears {
		switch c {
		case "seven_day":
			names = append(names, "the week")
		case "seven_day_overage_included":
			names = append(names, "Fable")
		case "five_hour":
			names = append(names, "the five-hour window")
		default:
			names = append(names, c)
		}
	}
	return strings.Join(names, " and ")
}
