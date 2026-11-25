package clawmaster

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const testOrg = "00000000-1111-2222-3333-444444444444"

var resetNow = time.Date(2026, 11, 25, 6, 0, 0, 0, time.UTC)

// usageWithGrants is a usage answer shaped as Anthropic sends it: the week at 100%, Fable at 90%, and a
// Fable reset and a weekly one banked.
func usageWithGrants(grants string, extra string) string {
	return `{"seven_day":{"utilization":100,"resets_at":"2026-11-29T10:00:00Z"},` +
		`"limits":[{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable"}},"percent":90,"resets_at":"2026-11-29T10:00:00Z"}],` +
		`"cedar_ember":{"eligible":true,"exhausted":["seven_day"],"cooldown_until":null` + extra + `,"grants":[` + grants + `]}}`
}

const (
	fableGrant  = `{"id":"fable-a","type":"full","clears":["seven_day_overage_included"],"resets_left":1,"resets_total":1,"usable_now":true,"gives_back_weekly":true,"paused":false,"ends_at":"2027-03-09T07:59:00+00:00","unavailable_reason":null}`
	weeklyGrant = `{"id":"weekly-a","type":"weekly","clears":["seven_day","seven_day_overage_included"],"resets_left":1,"resets_total":1,"usable_now":true,"paused":false,"ends_at":"2026-12-10T22:00:00+00:00","unavailable_reason":null}`
)

type resetCall struct {
	method, url, body string
	header            http.Header
}

// fakeAnthropic answers usage reads from usage (the first, then after, once a claim is made), the OAuth
// profile, and claims with claim.
type fakeAnthropic struct {
	usage, after, profile string
	claimStatus           int
	claim                 string
	calls                 []resetCall
}

func (f *fakeAnthropic) do(_ context.Context, _ *coreauth.Auth, r *http.Request) (*http.Response, error) {
	var body string
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
	}
	f.calls = append(f.calls, resetCall{method: r.Method, url: r.URL.String(), body: body, header: r.Header.Clone()})
	status, answer := http.StatusOK, ""
	switch {
	case r.URL.Path == "/api/oauth/usage":
		answer = f.usage
		if f.after != "" && f.claimed() {
			answer = f.after
		}
	case r.URL.Path == "/api/oauth/profile":
		answer = f.profile
	case strings.HasSuffix(r.URL.Path, "/reset_rate_limits"):
		status, answer = f.claimStatus, f.claim
		if status == 0 {
			status = http.StatusOK
		}
	default:
		status = http.StatusNotFound
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(answer)), Header: http.Header{}}, nil
}

func (f *fakeAnthropic) claimed() bool {
	for _, c := range f.calls {
		if c.method == http.MethodPost {
			return true
		}
	}
	return false
}

func (f *fakeAnthropic) posts() []resetCall {
	var out []resetCall
	for _, c := range f.calls {
		if c.method == http.MethodPost {
			out = append(out, c)
		}
	}
	return out
}

func resetAuth(org string) *coreauth.Auth {
	meta := map[string]any{"access_token": "opaque", "expired": "2099-01-01T00:00:00Z"}
	if org != "" {
		meta["organization_uuid"] = org
	}
	return &coreauth.Auth{ID: "p", Provider: "claude", Metadata: meta, Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth}}
}

func runReset(t *testing.T, f *fakeAnthropic, org, kind string, claim bool) (ClaudeResetReport, error) {
	t.Helper()
	return claudeResetFlow(t.Context(), resetAuth(org), f.do, kind, claim, func() time.Time { return resetNow })
}

// Without --claim the command only reads: it names the grant a claim would spend and sends nothing else.
func TestAResetWithoutClaimOnlyReads(t *testing.T) {
	f := &fakeAnthropic{usage: usageWithGrants(weeklyGrant+","+fableGrant, "")}
	report, err := runReset(t, f, testOrg, ResetKindFable, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0].method != http.MethodGet || f.calls[0].url != ClaudeOAuthUsageEndpoint {
		t.Fatalf("calls %+v, want one usage read", f.calls)
	}
	if report.Claimed || report.Grant == nil || report.Grant.id != "fable-a" || !report.Grant.GivesBackWeekly || !report.Grant.UsableNow {
		t.Fatalf("report %+v grant %+v", report, report.Grant)
	}
	if report.Before.Week == nil || *report.Before.Week != 1 || report.Before.Fable == nil || *report.Before.Fable != 0.9 {
		t.Fatalf("usage %+v", report.Before)
	}
	if report.Before.Weekly != 1 || report.Before.FableResets != 1 {
		t.Fatalf("banked %+v", report.Before)
	}
}

// A claim posts what Claude Code posts, for the grant of the kind asked, then reads the usage again.
func TestAClaimSpendsTheGrantOfItsKind(t *testing.T) {
	for _, tc := range []struct {
		kind, grant string
	}{{ResetKindFable, "fable-a"}, {ResetKindWeekly, "weekly-a"}} {
		t.Run(tc.kind, func(t *testing.T) {
			f := &fakeAnthropic{
				usage: usageWithGrants(weeklyGrant+","+fableGrant, ""),
				after: `{"seven_day":{"utilization":55,"resets_at":"2026-11-29T10:00:00Z"},"cedar_ember":{"eligible":true,"grants":[]}}`,
				claim: `{"result":"reset","resets_left":0,"cleared":["seven_day_overage_included"],"weekly_resets_at":"2026-11-29T10:00:00Z"}`,
			}
			report, err := runReset(t, f, testOrg, tc.kind, true)
			if err != nil {
				t.Fatal(err)
			}
			posts := f.posts()
			if len(posts) != 1 {
				t.Fatalf("posts %+v", posts)
			}
			if want := "https://api.anthropic.com/api/organizations/" + testOrg + "/reset_rate_limits"; posts[0].url != want {
				t.Fatalf("claim url %s, want %s", posts[0].url, want)
			}
			var sent map[string]string
			if err := json.Unmarshal([]byte(posts[0].body), &sent); err != nil {
				t.Fatal(err)
			}
			if sent["program"] != "cedar_ember" || sent["grant_id"] != tc.grant || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(sent["request_id"]) || len(sent) != 3 {
				t.Fatalf("claim body %v", sent)
			}
			if posts[0].header.Get("Content-Type") != "application/json" || !strings.HasPrefix(posts[0].header.Get("User-Agent"), "claude-cli/") {
				t.Fatalf("claim headers %v", posts[0].header)
			}
			if !report.Claimed || report.Claim.Result != "reset" || report.Claim.ResetsLeft == nil || *report.Claim.ResetsLeft != 0 {
				t.Fatalf("claim %+v", report.Claim)
			}
			if report.After == nil || report.After.Week == nil || *report.After.Week != 0.55 {
				t.Fatalf("after %+v", report.After)
			}
		})
	}
}

// A grant Anthropic says cannot be used now, an ineligible account, or no grant of the kind: nothing is
// posted, even with --claim.
func TestNoClaimIsSentForAGrantThatCannotBeUsed(t *testing.T) {
	blocked := strings.NewReplacer(`"usable_now":true`, `"usable_now":false`, `"unavailable_reason":null`, `"unavailable_reason":"blocked_seven_day"`).Replace(fableGrant)
	for _, tc := range []struct {
		name, usage string
		grant       bool
	}{
		{"blocked", usageWithGrants(blocked, ""), true},
		{"ineligible", `{"cedar_ember":{"eligible":false,"ineligible_reason":"surface","grants":[]}}`, false},
		{"none of the kind", usageWithGrants(weeklyGrant, ""), false},
		{"used", usageWithGrants(strings.Replace(fableGrant, `"resets_left":1`, `"resets_left":0`, 1), ""), false},
		{"ended", usageWithGrants(strings.Replace(fableGrant, "2027-03-09T07:59:00+00:00", "2026-11-25T05:00:00Z", 1), ""), false},
		{"paused", usageWithGrants(strings.Replace(fableGrant, `"paused":false`, `"paused":true`, 1), ""), false},
		{"a malformed id", usageWithGrants(strings.Replace(fableGrant, `"fable-a"`, `"Fable A"`, 1), ""), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAnthropic{usage: tc.usage, claim: `{"result":"reset"}`}
			report, err := runReset(t, f, testOrg, ResetKindFable, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(f.posts()) != 0 || report.Claimed {
				t.Fatalf("posted %+v", f.posts())
			}
			if (report.Grant != nil) != tc.grant {
				t.Fatalf("grant %+v, want one: %v", report.Grant, tc.grant)
			}
		})
	}
	// A usage answer with no reset block at all is an error: the account's resets are unknown.
	f := &fakeAnthropic{usage: `{"seven_day":{"utilization":10}}`}
	if _, err := runReset(t, f, testOrg, ResetKindFable, true); err == nil || len(f.posts()) != 0 {
		t.Fatalf("err %v posts %+v", err, f.posts())
	}
	if _, err := runReset(t, f, testOrg, "monthly", true); err == nil {
		t.Fatal("an unknown kind was accepted")
	}
}

// Of several grants of the kind, a claim spends one usable now, then the one Anthropic names next, then the
// one that ends soonest.
func TestAClaimPicksTheGrantAnthropicWouldTake(t *testing.T) {
	grant := func(id, ends string, usable bool) string {
		g := strings.Replace(fableGrant, `"fable-a"`, `"`+id+`"`, 1)
		g = strings.Replace(g, "2027-03-09T07:59:00+00:00", ends, 1)
		if !usable {
			g = strings.Replace(g, `"usable_now":true`, `"usable_now":false`, 1)
		}
		return g
	}
	for _, tc := range []struct {
		name, grants, extra, want string
	}{
		{"usable first", grant("late", "2027-03-09T00:00:00Z", true) + "," + grant("soon", "2026-12-01T00:00:00Z", false), "", "late"},
		{"next named", grant("x", "2026-12-01T00:00:00Z", true) + "," + grant("y", "2027-03-09T00:00:00Z", true), `,"next_grant_id":"y"`, "y"},
		{"soonest end", grant("x", "2027-03-09T00:00:00Z", true) + "," + grant("y", "2026-12-01T00:00:00Z", true), "", "y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAnthropic{usage: usageWithGrants(tc.grants, tc.extra)}
			report, err := runReset(t, f, testOrg, ResetKindFable, false)
			if err != nil || report.Grant == nil || report.Grant.id != tc.want {
				t.Fatalf("grant %+v err %v, want %s", report.Grant, err, tc.want)
			}
		})
	}
}

// A login saved before the organization was kept reads it from the OAuth profile; a profile without one
// stops the claim unsent.
func TestAClaimReadsTheOrganizationWhenTheLoginLacksIt(t *testing.T) {
	f := &fakeAnthropic{usage: usageWithGrants(fableGrant, ""), profile: `{"organization":{"uuid":"` + testOrg + `"}}`, claim: `{"result":"reset"}`}
	if _, err := runReset(t, f, "", ResetKindFable, true); err != nil {
		t.Fatal(err)
	}
	if posts := f.posts(); len(posts) != 1 || !strings.Contains(posts[0].url, testOrg) {
		t.Fatalf("posts %+v", posts)
	}
	f = &fakeAnthropic{usage: usageWithGrants(fableGrant, ""), profile: `{"organization":{}}`}
	if _, err := runReset(t, f, "", ResetKindFable, true); err == nil || len(f.posts()) != 0 {
		t.Fatalf("err %v posts %+v", err, f.posts())
	}
}

// Anthropic's refusals come back as its result; an error status is an error that does not repeat the body.
func TestAClaimReportsAnthropicsAnswer(t *testing.T) {
	f := &fakeAnthropic{usage: usageWithGrants(fableGrant, ""), claim: `{"result":"cooldown","reason":"cooldown","cooldown_until":"2026-11-25T06:02:00Z"}`}
	report, err := runReset(t, f, testOrg, ResetKindFable, true)
	if err != nil || !report.Claimed || report.Claim.Result != "cooldown" || report.Claim.Reason != "cooldown" {
		t.Fatalf("report %+v err %v", report.Claim, err)
	}
	f = &fakeAnthropic{usage: usageWithGrants(fableGrant, ""), claimStatus: http.StatusForbidden, claim: `{"error":"someone@example.com"}`}
	if report, err := runReset(t, f, testOrg, ResetKindFable, true); err == nil || strings.Contains(err.Error(), "example.com") || !report.ClaimUnknown || report.Claimed {
		t.Fatalf("report %+v err %v", report, err)
	}
	f = &fakeAnthropic{usage: usageWithGrants(fableGrant, ""), claimStatus: http.StatusTooManyRequests}
	if report, err := runReset(t, f, testOrg, ResetKindFable, true); err != nil || report.Claim.Result != "rate_limited" {
		t.Fatalf("report %+v err %v", report.Claim, err)
	}
}

// The command loads the profile's saved login and claims with it: the organization saved with the login
// names the claim's URL, and the login's token rides on each request.
func TestAResetLoadsTheProfilesLogin(t *testing.T) {
	saved := writeSyntheticBackendCredential(t, t.TempDir(), "login.json", map[string]any{"type": "claude", "access_token": integrationClaudeToken, "account_uuid": integrationClaudeAccount, "organization_uuid": testOrg, "claude_device_ids": []string{integrationClaudeDevice}})
	profile := Profile{Name: "a", Provider: "claude", AuthID: saved.AuthID, AuthDir: saved.AuthDir}
	f := &fakeAnthropic{usage: usageWithGrants(fableGrant, ""), claim: `{"result":"reset"}`}
	var tokens []string
	do := func(ctx context.Context, auth *coreauth.Auth, r *http.Request) (*http.Response, error) {
		tokens = append(tokens, credentialString(auth.Metadata, "access_token"))
		return f.do(ctx, auth, r)
	}
	report, err := resetClaudeLimit(t.Context(), profile, " Fable ", true, do)
	if err != nil || !report.Claimed {
		t.Fatalf("report %+v err %v", report, err)
	}
	if posts := f.posts(); len(posts) != 1 || !strings.Contains(posts[0].url, testOrg) {
		t.Fatalf("posts %+v", posts)
	}
	for _, token := range tokens {
		if token != integrationClaudeToken {
			t.Fatalf("a request carried %q", token)
		}
	}
	if _, err := resetClaudeLimit(t.Context(), Profile{Provider: "codex"}, ResetKindFable, true, do); err == nil {
		t.Fatal("a Codex profile was reset")
	}
}

// A claim that left but whose answer is lost (a dropped connection, an error status, an unreadable body) is
// reported as one that may have spent the reset, with the usage read again; a transport error does not
// carry the claim's URL, which names the organization.
func TestAClaimWithALostAnswerSaysTheResetMayBeSpent(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(f *fakeAnthropic) ClaudeQuotaRequestFunc
	}{
		{"dropped", func(f *fakeAnthropic) ClaudeQuotaRequestFunc {
			return func(ctx context.Context, a *coreauth.Auth, r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodPost {
					_, _ = f.do(ctx, a, r)
					return nil, &url.Error{Op: "Post", URL: r.URL.String(), Err: io.ErrUnexpectedEOF}
				}
				return f.do(ctx, a, r)
			}
		}},
		{"unreadable", func(f *fakeAnthropic) ClaudeQuotaRequestFunc { f.claim = `{"result":`; return f.do }},
		{"error status", func(f *fakeAnthropic) ClaudeQuotaRequestFunc { f.claimStatus = http.StatusBadGateway; return f.do }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAnthropic{usage: usageWithGrants(fableGrant, ""), after: `{"seven_day":{"utilization":55,"resets_at":"2026-11-29T10:00:00Z"},"cedar_ember":{"eligible":true,"grants":[]}}`}
			report, err := claudeResetFlow(t.Context(), resetAuth(testOrg), tc.fail(f), ResetKindFable, true, func() time.Time { return resetNow })
			if err == nil || strings.Contains(err.Error(), testOrg) || !strings.Contains(err.Error(), "may be spent") {
				t.Fatalf("err %v", err)
			}
			if !report.ClaimUnknown || report.Claimed || report.After == nil || report.After.Week == nil || *report.After.Week != 0.55 || len(f.posts()) != 1 {
				t.Fatalf("report %+v posts %d", report, len(f.posts()))
			}
		})
	}
	// A failure before the claim leaves (reading the organization) is a plain error: nothing was sent.
	f := &fakeAnthropic{usage: usageWithGrants(fableGrant, "")}
	do := func(ctx context.Context, a *coreauth.Auth, r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/oauth/profile" {
			return nil, &url.Error{Op: "Get", URL: r.URL.String(), Err: io.ErrUnexpectedEOF}
		}
		return f.do(ctx, a, r)
	}
	report, err := claudeResetFlow(t.Context(), resetAuth(""), do, ResetKindFable, true, func() time.Time { return resetNow })
	if err == nil || report.ClaimUnknown || len(f.posts()) != 0 {
		t.Fatalf("report %+v err %v", report, err)
	}
}
