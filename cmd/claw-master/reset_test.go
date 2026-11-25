package main

import (
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clawmaster"
)

func TestTheResetReportSaysWhatAClaimDoes(t *testing.T) {
	now := time.Date(2026, 11, 25, 6, 0, 0, 0, time.UTC)
	week, fable, after := 1.0, 0.9, 0.55
	left := 0
	grant := &clawmaster.ClaudeResetGrant{Clears: []string{"seven_day_overage_included"}, ResetsLeft: 1, UsableNow: true, GivesBackWeekly: true, EndsAt: time.Date(2027, 3, 9, 7, 59, 0, 0, time.UTC)}
	before := clawmaster.ClaudeResetUsage{Week: &week, Fable: &fable, WeekResetsAt: now.Add(100 * time.Hour), Eligible: true, Weekly: 0, FableResets: 1}
	dry := formatResetReport(clawmaster.ClaudeResetReport{Kind: "fable", Before: before, Grant: grant}, false, now)
	for _, want := range []string{
		"Now: week 100% used, Fable 90% used, the week resets in 100h\n",
		"Banked: 0 weekly, 1 Fable\n",
		"The fable reset clears Fable and gives the week back what Fable used; 1 left, until 2027-03-09 07:59 UTC.\n",
		"Claiming it should leave the week about 55% used and Fable at 0%.\n",
		"Nothing was claimed. Run again with --claim to spend it.\n",
	} {
		if !strings.Contains(dry, want) {
			t.Fatalf("dry run %q lacks %q", dry, want)
		}
	}
	blocked := *grant
	blocked.UsableNow, blocked.Unavailable = false, "blocked_seven_day"
	if out := formatResetReport(clawmaster.ClaudeResetReport{Kind: "fable", Before: before, Grant: &blocked}, true, now); !strings.Contains(out, "It cannot be claimed now (Anthropic says: blocked_seven_day).") {
		t.Fatalf("blocked %q", out)
	}
	done := formatResetReport(clawmaster.ClaudeResetReport{Kind: "fable", Before: before, Grant: grant, Claimed: true,
		Claim: clawmaster.ClaudeResetClaim{Result: "reset", Cleared: []string{"seven_day_overage_included"}, ResetsLeft: &left},
		After: &clawmaster.ClaudeResetUsage{Week: &after, Fable: new(float64), WeekResetsAt: now.Add(100 * time.Hour)}}, true, now)
	if !strings.Contains(done, "Claimed: reset; cleared Fable; 0 left.\nAfter: week 55% used, Fable 0% used, the week resets in 100h\n") {
		t.Fatalf("claimed %q", done)
	}
	if out := formatResetReport(clawmaster.ClaudeResetReport{Kind: "fable", Before: clawmaster.ClaudeResetUsage{IneligibleReason: "surface"}}, false, now); !strings.Contains(out, "cannot claim limit resets (Anthropic says: surface)") {
		t.Fatalf("ineligible %q", out)
	}
	if out := formatResetReport(clawmaster.ClaudeResetReport{Kind: "weekly", Before: before}, false, now); !strings.Contains(out, "No weekly reset is banked.") {
		t.Fatalf("none %q", out)
	}
}

func TestResetTakesOnlyItsFlags(t *testing.T) {
	if code, err := run([]string{"reset", "a", "--", "extra"}); code != 2 || err == nil {
		t.Fatalf("code %d err %v", code, err)
	}
	if code, err := run([]string{"reset", "a", "--bogus"}); code != 2 || err == nil {
		t.Fatalf("code %d err %v", code, err)
	}
}
