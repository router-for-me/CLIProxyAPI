package main

import (
	"bytes"
	"errors"
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
	if !strings.Contains(done, "Anthropic answered: reset; cleared Fable; 0 left.\nAfter: week 55% used, Fable 0% used, the week resets in 100h\n") {
		t.Fatalf("claimed %q", done)
	}
	if out := formatResetReport(clawmaster.ClaudeResetReport{Kind: "fable", Before: clawmaster.ClaudeResetUsage{IneligibleReason: "surface"}}, false, now); !strings.Contains(out, "cannot claim limit resets (Anthropic says: surface)") {
		t.Fatalf("ineligible %q", out)
	}
	if out := formatResetReport(clawmaster.ClaudeResetReport{Kind: "weekly", Before: before}, false, now); !strings.Contains(out, "No weekly reset is banked.") {
		t.Fatalf("none %q", out)
	}
}

func TestTheResetReportPrintsOnlyPlainWordsFromAnthropic(t *testing.T) {
	now := time.Date(2026, 11, 25, 6, 0, 0, 0, time.UTC)
	grant := &clawmaster.ClaudeResetGrant{Clears: []string{"seven_day_overage_included", "someone@example.com"}, ResetsLeft: 1, UsableNow: true}
	before := clawmaster.ClaudeResetUsage{Eligible: true, FableResets: 1, WeekResetsAt: now.Add(-time.Hour)}
	out := formatResetReport(clawmaster.ClaudeResetReport{Kind: "fable", Before: before, Grant: grant, Claimed: true,
		Claim: clawmaster.ClaudeResetClaim{Result: "already_used", Reason: "\x1b[2Jaccount of someone"}}, true, now)
	if strings.Contains(out, "example.com") || strings.Contains(out, "\x1b") || strings.Contains(out, "resets in") {
		t.Fatalf("report %q", out)
	}
	if !strings.Contains(out, "clears Fable and unrecognised") || !strings.Contains(out, "Anthropic answered: already_used (unrecognised).") {
		t.Fatalf("report %q", out)
	}
	unknown := formatResetReport(clawmaster.ClaudeResetReport{Kind: "fable", Before: before, Grant: grant, ClaimUnknown: true}, true, now)
	if !strings.Contains(unknown, "The claim was sent, but its answer could not be read: the reset may be spent.") {
		t.Fatalf("unknown %q", unknown)
	}
}

// With --claim the command succeeds only when Anthropic answered that it reset.
func TestAResetClaimSucceedsOnlyOnAReset(t *testing.T) {
	for _, tc := range []struct {
		report clawmaster.ClaudeResetReport
		claim  bool
		ok     bool
	}{
		{clawmaster.ClaudeResetReport{}, false, true},
		{clawmaster.ClaudeResetReport{Claimed: true, Claim: clawmaster.ClaudeResetClaim{Result: "reset"}}, true, true},
		{clawmaster.ClaudeResetReport{Claimed: true, Claim: clawmaster.ClaudeResetClaim{Result: "already_used"}}, true, false},
		{clawmaster.ClaudeResetReport{Claimed: true, Claim: clawmaster.ClaudeResetClaim{Result: "rate_limited"}}, true, false},
		{clawmaster.ClaudeResetReport{}, true, false},
	} {
		if err := resetOutcome(tc.report, tc.claim); (err == nil) != tc.ok {
			t.Fatalf("%+v claim %v: %v", tc.report, tc.claim, err)
		}
	}
}

func TestResetTakesOnlyItsFlags(t *testing.T) {
	for _, kind := range []string{"monthly", ""} {
		if code, err := run([]string{"reset", "a", "--kind", kind}); code != 2 || err == nil {
			t.Fatalf("--kind %q: code %d err %v", kind, code, err)
		}
	}
	if code, err := run([]string{"reset", "a", "--", "extra"}); code != 2 || err == nil {
		t.Fatalf("code %d err %v", code, err)
	}
	if code, err := run([]string{"reset", "a", "--bogus"}); code != 2 || err == nil {
		t.Fatalf("code %d err %v", code, err)
	}
}

// A claim whose answer was lost prints the report, saying the reset may be spent, and fails with the cause;
// a failure before any claim left prints nothing.
func TestALostClaimIsPrintedThenFails(t *testing.T) {
	now := time.Date(2026, 11, 25, 6, 0, 0, 0, time.UTC)
	grant := &clawmaster.ClaudeResetGrant{Clears: []string{"seven_day_overage_included"}, ResetsLeft: 1, UsableNow: true}
	lost := errors.New("the claim was sent but its answer could not be read")
	var out bytes.Buffer
	code, err := finishReset(&out, clawmaster.ClaudeResetReport{Kind: "fable", Before: clawmaster.ClaudeResetUsage{Eligible: true}, Grant: grant, ClaimUnknown: true}, lost, true, now)
	if code != 1 || err != lost || !strings.Contains(out.String(), "the reset may be spent") {
		t.Fatalf("code %d err %v out %q", code, err, out.String())
	}
	out.Reset()
	early := errors.New("cannot load the profile's login")
	if code, err := finishReset(&out, clawmaster.ClaudeResetReport{}, early, true, now); code != 1 || err != early || out.Len() != 0 {
		t.Fatalf("code %d err %v out %q", code, err, out.String())
	}
	out.Reset()
	if code, err := finishReset(&out, clawmaster.ClaudeResetReport{Kind: "fable", Before: clawmaster.ClaudeResetUsage{Eligible: true}, Grant: grant, Claimed: true, Claim: clawmaster.ClaudeResetClaim{Result: "reset"}}, nil, true, now); code != 0 || err != nil || !strings.Contains(out.String(), "Anthropic answered: reset") {
		t.Fatalf("code %d err %v out %q", code, err, out.String())
	}
}
