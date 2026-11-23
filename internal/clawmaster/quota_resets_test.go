package clawmaster

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// The usage endpoint, asked with cedar_ember=1, lists the limit resets an account has banked. Each count is
// the resets left on grants that are neither paused nor ended; a grant counts once, under the largest limit
// it clears, as claude.ai's usage page lists one "Weekly reset" and one "Fable reset".
func TestTheBankedResetsAreCountedPerLimit(t *testing.T) {
	now := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		body      string
		want      ClaudeResetGrants
		wantKnown bool
	}{
		{name: "no block", body: `{"limits":[]}`},
		{name: "a null block", body: `{"cedar_ember":null}`},
		{name: "a block of another shape", body: `{"cedar_ember":"x"}`},
		{name: "ineligible", body: `{"cedar_ember":{"eligible":false,"grants":[{"id":"g","resets_left":1,"clears":["seven_day"]}]}}`, wantKnown: true},
		{name: "one weekly reset", body: `{"cedar_ember":{"eligible":true,"grants":[{"id":"g","resets_total":1,"resets_left":1,"clears":["seven_day"]}]}}`, want: ClaudeResetGrants{Weekly: 1}, wantKnown: true},
		{name: "used", body: `{"cedar_ember":{"eligible":true,"grants":[{"id":"g","resets_total":1,"resets_left":0,"clears":["seven_day"]}]}}`, wantKnown: true},
		{name: "a Fable reset that gives the week back", body: `{"cedar_ember":{"eligible":true,"grants":[{"id":"g","resets_left":1,"clears":["seven_day_overage_included"],"gives_back_weekly":true}]}}`, want: ClaudeResetGrants{Fable: 1}, wantKnown: true},
		{name: "a Fable reset and a weekly one", body: `{"cedar_ember":{"eligible":true,"grants":[{"id":"f","resets_total":1,"resets_left":1,"clears":["seven_day_overage_included"],"gives_back_weekly":true,"ends_at":"2027-03-08T00:00:00Z"},{"id":"w","resets_total":1,"resets_left":1,"clears":["seven_day"],"ends_at":"2026-12-10T00:00:00Z"}]}}`, want: ClaudeResetGrants{Weekly: 1, Fable: 1}, wantKnown: true},
		{name: "a full reset and a session one", body: `{"cedar_ember":{"eligible":true,"grants":[{"id":"a","resets_left":2,"clears":["five_hour","seven_day","seven_day_overage_included"]},{"id":"b","resets_left":1,"clears":["five_hour"]}]}}`, want: ClaudeResetGrants{Weekly: 2, FiveHour: 1}, wantKnown: true},
		{name: "paused, ended and malformed grants", body: `{"cedar_ember":{"eligible":true,"grants":[{"id":"p","resets_left":1,"paused":true,"clears":["seven_day"]},{"id":"e","resets_left":1,"ends_at":"2026-11-23T08:59:00Z","clears":["seven_day"]},{"id":"m","resets_left":"one","clears":["seven_day"]},{"id":"n","clears":["seven_day"]},{"id":"f","resets_left":1,"ends_at":"2026-11-30T00:00:00Z","clears":["seven_day"]}]}}`, want: ClaudeResetGrants{Weekly: 1}, wantKnown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, known, err := ParseClaudeResetGrants([]byte(tc.body), now)
			if err != nil || known != tc.wantKnown || got != tc.want {
				t.Fatalf("got %+v known %v err %v, want %+v known %v", got, known, err, tc.want, tc.wantKnown)
			}
		})
	}
	if _, _, err := ParseClaudeResetGrants([]byte(`{`), now); err == nil {
		t.Fatal("a truncated body parsed")
	}
}

// The poll asks for the grants and the pool exports them per profile; once no poll has reported them for
// a while the gauges go, rather than repeating an old count.
func TestThePollExportsEachAccountsBankedResets(t *testing.T) {
	reader := startMetrics(t, nil)
	selector, _, _ := namedSelector(t)
	manager := coreauth.NewManager(nil, selector, nil)
	if _, err := manager.Register(t.Context(), &coreauth.Auth{
		ID: "profile-a", Provider: "claude", Status: coreauth.StatusActive,
		Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth},
		Metadata:   map[string]any{"access_token": "opaque-a", "expired": "2099-01-01T00:00:00Z"},
	}); err != nil {
		t.Fatal(err)
	}
	body := `{"limits":[{"kind":"weekly_all","percent":40,"resets_at":"2099-01-01T00:00:00Z"}],"cedar_ember":{"eligible":true,"grants":[{"id":"g","resets_left":1,"clears":["seven_day"]}]}}`
	var asked string
	poll := func() {
		loadBackendWeeklyQuotas(t.Context(), manager, selector, []string{"profile-a"}, func(_ context.Context, _ *coreauth.Auth, req *http.Request) (*http.Response, error) {
			asked = req.URL.String()
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
	}
	gauges := func() map[string]float64 {
		src := &fixedSource{snap: selector.metricsSnapshot(nil)}
		registerStateSource(src)
		defer unregisterStateSource(src)
		rm := collect(t, reader)
		out := map[string]float64{}
		for _, name := range []string{"claw_master.quota.weekly.resets_left", "claw_master.quota.fable.resets_left", "claw_master.quota.five_hour.resets_left"} {
			if v, ok := gaugeOf(rm, name, map[string]string{"profile": "claude-primary"}); ok {
				out[name] = v
			}
		}
		return out
	}

	poll()
	if !strings.Contains(asked, "cedar_ember=1") {
		t.Fatalf("the poll asked %q, without cedar_ember=1", asked)
	}
	if got := gauges(); got["claw_master.quota.weekly.resets_left"] != 1 || got["claw_master.quota.fable.resets_left"] != 0 || len(got) != 3 {
		t.Fatalf("after a poll with one weekly reset: %v", got)
	}
	// A poll answered without the grants (an old cached body, say) leaves the count while it is recent...
	body = `{"limits":[{"kind":"weekly_all","percent":40,"resets_at":"2099-01-01T00:00:00Z"}]}`
	poll()
	if got := gauges(); got["claw_master.quota.weekly.resets_left"] != 1 {
		t.Fatalf("right after a poll without grants: %v, want the last count", got)
	}
	// ...and drops it once no poll has reported grants for longer than resetGrantsMaxAge.
	later := time.Now().Add(resetGrantsMaxAge + time.Minute)
	selector.now = func() time.Time { return later }
	if got := gauges(); len(got) != 0 {
		t.Fatalf("%s after the last grants: %v, want no gauges", resetGrantsMaxAge+time.Minute, got)
	}
}

// The poll asks as Claude Code does, and logs what Anthropic said of the account's resets once per change,
// without grant ids or labels, so an exported count can be checked against its source.
func TestThePollAsksAsClaudeCodeAndLogsTheGrantBlock(t *testing.T) {
	logs := captureLogs(t, "info")
	selector, _, _ := namedSelector(t)
	manager := coreauth.NewManager(nil, selector, nil)
	if _, err := manager.Register(t.Context(), &coreauth.Auth{
		ID: "profile-a", Provider: "claude", Status: coreauth.StatusActive,
		Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth},
		Metadata:   map[string]any{"access_token": "opaque-a", "expired": "2099-01-01T00:00:00Z"},
	}); err != nil {
		t.Fatal(err)
	}
	body := `{"limits":[],"cedar_ember":{"eligible":false,"ineligible_reason":"surface","grants":[{"id":"grant-secret-id","label":"Weekly reset","resets_left":1,"clears":["seven_day"],"paused":false}]}}`
	var agent string
	poll := func() {
		loadBackendWeeklyQuotas(t.Context(), manager, selector, []string{"profile-a"}, func(_ context.Context, _ *coreauth.Auth, req *http.Request) (*http.Response, error) {
			agent = req.Header.Get("User-Agent")
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
	}
	poll()
	poll()
	if !strings.HasPrefix(agent, "claude-cli/") || !strings.HasSuffix(agent, " (external, cli)") || agent != claudeCodeUserAgent() {
		t.Fatalf("User-Agent %q, want Claude Code's form", agent)
	}
	text := logs.String()
	if n := strings.Count(text, "reset grants"); n != 1 {
		t.Fatalf("logged the block %d times over two identical polls, want once:\n%s", n, text)
	}
	for _, want := range []string{"claude-primary", `\"ineligible_reason\":\"surface\"`, `\"clears\":[\"seven_day\"]`} {
		if !strings.Contains(text, want) {
			t.Errorf("the log lacks %s:\n%s", want, text)
		}
	}
	if strings.Contains(text, "grant-secret-id") || strings.Contains(text, "Weekly reset") {
		t.Errorf("the log carries a grant id or label:\n%s", text)
	}
}
