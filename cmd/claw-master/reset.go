package main

import (
	"errors"
	"fmt"
	"io"
	"regexp"
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
		fmt.Fprintf(&b, "This account cannot claim limit resets (Anthropic says: %s).\n", upstreamWord(r.Before.IneligibleReason))
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
		why := "not usable now"
		if g.Unavailable != "" {
			why = upstreamWord(g.Unavailable)
		}
		if r.Before.CooldownUntil != "" {
			why += ", cooldown until " + upstreamWord(r.Before.CooldownUntil)
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
	if r.ClaimUnknown {
		b.WriteString("The claim was sent, but its answer could not be read: the reset may be spent.\n")
		if r.After != nil {
			fmt.Fprintf(&b, "After: %s\n", formatResetUsage(*r.After, now))
		}
		return b.String()
	}
	if !r.Claimed {
		return b.String()
	}
	c := r.Claim
	fmt.Fprintf(&b, "Anthropic answered: %s", upstreamWord(c.Result))
	if c.Reason != "" {
		fmt.Fprintf(&b, " (%s)", upstreamWord(c.Reason))
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
	if u.WeekResetsAt.After(now) {
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
			names = append(names, upstreamWord(c))
		}
	}
	return strings.Join(names, " and ")
}

// finishReset prints what a reset command found and gives its exit: a failure before any claim left prints
// nothing more than the error; a claim whose answer was lost prints the report, then fails.
func finishReset(w io.Writer, r clawmaster.ClaudeResetReport, err error, claim bool, now time.Time) (int, error) {
	if err != nil && !r.ClaimUnknown {
		return 1, err
	}
	fmt.Fprint(w, formatResetReport(r, claim, now))
	if err != nil {
		return 1, err
	}
	if err := resetOutcome(r, claim); err != nil {
		return 1, err
	}
	return 0, nil
}

// resetOutcome is the command's verdict: with --claim it fails unless Anthropic answered that it reset.
func resetOutcome(r clawmaster.ClaudeResetReport, claim bool) error {
	if !claim {
		return nil
	}
	if !r.Claimed || r.Claim.Result != "reset" {
		return errors.New("no reset was claimed")
	}
	return nil
}

// upstreamPlain is what an Anthropic enum or timestamp looks like; anything else is not printed, since a
// free-form string could carry account text or terminal control characters.
var upstreamPlain = regexp.MustCompile(`^[A-Za-z0-9_:.+-]{1,64}$`)

func upstreamWord(s string) string {
	if s == "" {
		return "unknown"
	}
	if upstreamPlain.MatchString(s) {
		return s
	}
	return "unrecognised"
}
