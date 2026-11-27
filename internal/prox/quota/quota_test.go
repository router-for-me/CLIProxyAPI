package quota

import (
	"context"
	"errors"
	"github.com/MoeForge/moego/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

const validClaude = `{"seven_day":{"utilization":20,"resets_at":"2026-09-25T00:00:00Z"},"five_hour":{"utilization":0,"resets_at":null},"seven_day_sonnet":null,"seven_day_opus":{"utilization":55,"resets_at":"2026-09-23T00:00:00Z"},"seven_day_oauth_apps":{"utilization":"junk"},"unrecognized_field":{"value":1}}`

func TestParseClaudeUsage(t *testing.T) {
	cases := []struct {
		name, raw string
		valid     bool
	}{
		{"normal", validClaude, true},
		{"minimal", `{"seven_day":{"utilization":0,"resets_at":"2026-09-25T00:00:00+02:00"}}`, true},
		{"exhausted", `{"seven_day":{"utilization":100,"resets_at":"2026-09-25T00:00:00Z"}}`, true},
		{"unstarted weekly", `{"seven_day":{"utilization":0,"resets_at":null}}`, true},
		{"unknown weekly", `{"seven_day":null}`, false},
		{"used weekly without reset", `{"seven_day":{"utilization":3,"resets_at":null}}`, false},
		{"missing utilization", `{"seven_day":{"resets_at":"2026-09-25T00:00:00Z"}}`, false},
		{"null utilization", `{"seven_day":{"utilization":null,"resets_at":"2026-09-25T00:00:00Z"}}`, false},
		{"negative", `{"seven_day":{"utilization":-1,"resets_at":"2026-09-25T00:00:00Z"}}`, false},
		{"above 100", `{"seven_day":{"utilization":101,"resets_at":"2026-09-25T00:00:00Z"}}`, false},
		{"wrong units type", `{"seven_day":{"utilization":"20","resets_at":"2026-09-25T00:00:00Z"}}`, false},
		{"bad timestamp", `{"seven_day":{"utilization":20,"resets_at":"tomorrow"}}`, false},
		{"zero timestamp", `{"seven_day":{"utilization":20,"resets_at":"0001-01-01T00:00:00Z"}}`, false},
		{"short malformed", `{"seven_day":{"utilization":20,"resets_at":"2026-09-25T00:00:00Z"},"five_hour":{"utilization":100,"resets_at":null}}`, false},
		{"model malformed", `{"seven_day":{"utilization":20,"resets_at":"2026-09-25T00:00:00Z"},"seven_day_fable":{"utilization":120,"resets_at":"2026-09-25T00:00:00Z"}}`, false},
		{"invalid JSON", `{"seven_day":`, false},
		{"null", `null`, false},
		{"empty", `{}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseClaudeUsage([]byte(tc.raw), testNow)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
	snap, err := ParseClaudeUsage([]byte(validClaude), testNow)
	if err != nil {
		t.Fatal(err)
	}
	weekly, _ := snap.Window("weekly")
	opus, okOpus := snap.Window("model_weekly:opus")
	_, okApps := snap.Window("model_weekly:oauth_apps")
	if weekly.UsedPercent != 20 || weekly.RemainingPercent() != 80 || !okOpus || opus.UsedPercent != 55 || okApps {
		t.Fatalf("windows: %+v", snap.Windows)
	}
}

func TestParseCodexUsageWindows(t *testing.T) {
	raw := []byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":60,"limit_window_seconds":18000,"reset_at":1790172000,"reset_after_seconds":7200},"secondary_window":{"used_percent":30,"limit_window_seconds":604800,"reset_at":1790683200,"reset_after_seconds":518400}}}`)
	snap, err := ParseCodexUsage(raw, testNow)
	if err != nil {
		t.Fatal(err)
	}
	five, ok5 := snap.Window("five_hour")
	week, ok7 := snap.Window("weekly")
	if snap.PlanType != "plus" || !ok5 || !ok7 || five.UsedPercent != 60 || week.UsedPercent != 30 {
		t.Fatalf("codex windows: %+v", snap)
	}
	monthly, err := ParseCodexUsage([]byte(`{"rate_limit":{"primary_window":{"used_percent":40,"limit_window_seconds":2592000,"reset_after_seconds":360000}}}`), testNow)
	if _, ok := monthly.Window("monthly"); err != nil || !ok {
		t.Fatalf("monthly plan not parsed: %+v %v", monthly, err)
	}
	reached, err := ParseCodexUsage([]byte(`{"rate_limit":{"secondary_window":{"used_percent":80,"limit_window_seconds":604800,"reset_after_seconds":60,"limit_reached":true}}}`), testNow)
	if w, _ := reached.Window("weekly"); err != nil || w.UsedPercent != 100 || !w.Rejected {
		t.Fatalf("limit_reached ignored: %+v %v", reached, err)
	}
}

func TestParseCodexRejectsAmbiguousOrInvalidWindows(t *testing.T) {
	for _, raw := range []string{
		`{"rate_limit":{}}`,
		`{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000,"reset_at":1}}}`,
		`{"rate_limit":{"primary_window":{"used_percent":101,"limit_window_seconds":18000,"reset_at":1}}}`,
		`{"rate_limit":{"secondary_window":{"used_percent":10,"limit_window_seconds":604800}}}`,
		`{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":604800,"reset_at":5},"secondary_window":{"used_percent":10,"limit_window_seconds":604800,"reset_at":1}}}`,
	} {
		if _, err := ParseCodexUsage([]byte(raw), testNow); err == nil {
			t.Fatalf("accepted invalid Codex payload: %s", raw)
		}
	}
}

func TestPassiveHeadersNormalizeUnits(t *testing.T) {
	reset := testNow.Add(3 * time.Hour).Unix()
	claude := http.Header{}
	claude.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.42")
	claude.Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(reset, 10))
	claude.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.9")
	claude.Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
	claude.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(reset, 10))
	ws := ParseClaudeHeaders(claude, testNow)
	if len(ws) != 2 || ws[0].Kind != FiveHour || ws[0].UsedPercent != 42 || ws[0].RemainingPercent() != 58 {
		t.Fatalf("claude fraction must scale to percent: %+v", ws)
	}
	if ws[1].Kind != Weekly || ws[1].UsedPercent != 100 || !ws[1].Rejected {
		t.Fatalf("rejected status must mark the window full: %+v", ws[1])
	}
	codex := http.Header{}
	codex.Set("X-Codex-Primary-Used-Percent", "42")
	codex.Set("X-Codex-Primary-Window-Minutes", "300")
	codex.Set("X-Codex-Primary-Reset-After-Seconds", "600")
	codex.Set("X-Codex-Secondary-Used-Percent", "7.5")
	codex.Set("X-Codex-Secondary-Window-Minutes", "10080")
	codex.Set("X-Codex-Secondary-Reset-At", strconv.FormatInt(reset, 10))
	ws = ParseCodexHeaders(codex, testNow)
	if len(ws) != 2 || ws[0].UsedPercent != 42 || ws[1].UsedPercent != 7.5 || ws[1].Kind != Weekly {
		t.Fatalf("codex percent must stay percent: %+v", ws)
	}
	for _, bad := range []http.Header{
		{"Anthropic-Ratelimit-Unified-5h-Utilization": {"42"}, "Anthropic-Ratelimit-Unified-5h-Reset": {"1"}},
		{"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.4"}},
	} {
		if got := ParseClaudeHeaders(bad, testNow); len(got) != 0 {
			t.Fatalf("accepted incomplete or out-of-range claude headers %v: %+v", bad, got)
		}
	}
	if got := ParseCodexHeaders(http.Header{"X-Codex-Primary-Used-Percent": {"5"}, "X-Codex-Primary-Window-Minutes": {"300"}}, testNow); len(got) != 0 {
		t.Fatalf("codex window without reset accepted: %+v", got)
	}
}

func TestClassifyClaude429(t *testing.T) {
	shared := http.Header{"Anthropic-Ratelimit-Unified-5h-Status": {"rejected"}}
	if r := ClassifyClaude429(shared, testNow); !r.Shared || r.ModelOnly {
		t.Fatalf("shared rejection: %+v", r)
	}
	model := http.Header{"Anthropic-Ratelimit-Unified-Status": {"rejected"}, "Anthropic-Ratelimit-Unified-5h-Status": {"allowed"}, "Anthropic-Ratelimit-Unified-Reset": {strconv.FormatInt(testNow.Add(time.Hour).Unix(), 10)}}
	if r := ClassifyClaude429(model, testNow); r.Shared || !r.ModelOnly || r.Until == nil {
		t.Fatalf("model-only rejection: %+v", r)
	}
}

func snapshotOf(ws ...Window) Snapshot { return Snapshot{Windows: ws} }

func win(kind Kind, family string, used float64, reset time.Duration, observed time.Time) Window {
	t := testNow.Add(reset)
	return Window{Kind: kind, Family: family, UsedPercent: used, ResetsAt: &t, Source: SourcePoll, ObservedAt: observed}
}

func TestAssess(t *testing.T) {
	maxAge := 10 * time.Minute
	fresh := testNow.Add(-time.Minute)
	cases := []struct {
		name   string
		snap   Snapshot
		model  string
		state  State
		reason string
	}{
		{"ready", snapshotOf(win(Weekly, "", 20, 48*time.Hour, fresh)), "claude-sonnet-4", Ready, "fresh"},
		{"no long window", snapshotOf(win(FiveHour, "", 20, time.Hour, fresh)), "x", Unknown, "no_long_window"},
		{"five hour exhausted", snapshotOf(win(Weekly, "", 20, 48*time.Hour, fresh), win(FiveHour, "", 100, time.Hour, fresh)), "x", Exhausted, "five_hour_exhausted"},
		{"exhausted survives staleness", snapshotOf(win(Weekly, "", 100, 48*time.Hour, testNow.Add(-5*time.Hour))), "x", Exhausted, "weekly_exhausted"},
		{"model window only for its family", snapshotOf(win(Weekly, "", 20, 48*time.Hour, fresh), win(ModelWeekly, "opus", 100, 24*time.Hour, fresh)), "claude-sonnet-4", Ready, "fresh"},
		{"model window exhausts family", snapshotOf(win(Weekly, "", 20, 48*time.Hour, fresh), win(ModelWeekly, "opus", 100, 24*time.Hour, fresh)), "claude-opus-4-6", Exhausted, "model_weekly:opus_exhausted"},
		{"passed reset is not a reset", snapshotOf(win(Weekly, "", 100, -time.Minute, fresh)), "x", ResetPending, "weekly_awaiting_reset_evidence"},
		{"passed reset on healthy window", snapshotOf(win(Weekly, "", 30, -time.Minute, fresh)), "x", ResetPending, "weekly_reset_passed"},
		{"stale", snapshotOf(win(Weekly, "", 30, 48*time.Hour, testNow.Add(-maxAge))), "x", Stale, "weekly_stale"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := Assess(tc.snap, tc.model, testNow, maxAge)
			if a.State != tc.state || a.Reason != tc.reason {
				t.Fatalf("got %s/%s, want %s/%s", a.State, a.Reason, tc.state, tc.reason)
			}
		})
	}
	a := Assess(snapshotOf(win(Weekly, "", 20, 72*time.Hour, fresh), win(ModelWeekly, "opus", 60, 24*time.Hour, fresh), win(FiveHour, "", 35, time.Hour, fresh)), "claude-opus-4-6", testNow, maxAge)
	if a.Applicable == nil || a.Applicable.Key() != "model_weekly:opus" || a.Remaining != 40 || a.FiveHourUsed != 35 {
		t.Fatalf("applicable window must be the earliest relevant long reset with the tightest remaining: %+v", a)
	}
}

func TestMergeObservedKeepsNewest(t *testing.T) {
	s := snapshotOf(win(Weekly, "", 20, 48*time.Hour, testNow), win(ModelWeekly, "opus", 10, 48*time.Hour, testNow))
	older := win(Weekly, "", 90, 48*time.Hour, testNow.Add(-time.Minute))
	if s.MergeObserved([]Window{older}) {
		t.Fatal("older observation replaced newer window")
	}
	newer := win(Weekly, "", 25, 48*time.Hour, testNow.Add(time.Minute))
	newer.Source = SourcePassive
	if !s.MergeObserved([]Window{newer}) {
		t.Fatal("newer observation ignored")
	}
	w, _ := s.Window("weekly")
	if _, ok := s.Window("model_weekly:opus"); w.UsedPercent != 25 || !ok {
		t.Fatalf("merge lost or kept wrong windows: %+v", s.Windows)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testCredential() Credential {
	return Credential{Type: "claude", AccessToken: "SECRET_TOKEN", AccountUUID: "a"}
}

func fetcherWith(rt roundTripFunc) *HTTPFetcher {
	f := NewHTTPFetcher()
	f.Client.Transport = rt
	f.Now = func() time.Time { return testNow }
	return f
}

func TestHTTPQuotaRequestAndIdentity(t *testing.T) {
	f := fetcherWith(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != ClaudeUsageURL || r.Method != http.MethodGet || r.Body != nil {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer SECRET_TOKEN" || r.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
			t.Fatal("missing OAuth headers")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(validClaude))}, nil
	})
	snap, err := f.Fetch(context.Background(), testCredential())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Identity != testCredential().Identity() || snap.PolledAt.IsZero() {
		t.Fatal("missing identity or freshness")
	}
	encoded, _ := json.Marshal(snap)
	if strings.Contains(string(encoded), "SECRET_TOKEN") {
		t.Fatal("token leaked")
	}
}

func TestHTTPCodexQuotaRequestAndIdentity(t *testing.T) {
	f := fetcherWith(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != CodexUsageURL || r.Header.Get("Authorization") != "Bearer CODEX_TOKEN" || r.Header.Get("Chatgpt-Account-Id") != "account-id" || r.Header.Get("User-Agent") == "" {
			t.Fatalf("unexpected Codex request: %s %+v", r.URL, r.Header)
		}
		body := `{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":45,"limit_window_seconds":18000,"reset_after_seconds":3600},"secondary_window":{"used_percent":10,"limit_window_seconds":604800,"reset_after_seconds":3600}}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	cred := Credential{Type: "codex", AccessToken: "CODEX_TOKEN", AccountID: "account-id"}
	snap, err := f.Fetch(context.Background(), cred)
	if err != nil || snap.Provider != "codex" || snap.Identity != cred.Identity() {
		t.Fatalf("bad Codex fetch: %+v, %v", snap, err)
	}
}

func TestHTTPFailureHandling(t *testing.T) {
	for _, status := range []int{301, 401, 403, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := fetcherWith(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {"720"}}, Body: io.NopCloser(strings.NewReader("SECRET_TOKEN"))}, nil
			})
			_, err := f.Fetch(context.Background(), testCredential())
			qerr, ok := IsError(err)
			if !ok || strings.Contains(err.Error(), "SECRET_TOKEN") || qerr.Status != status {
				t.Fatalf("bad error: %v", err)
			}
			if status == 429 && qerr.RetryAfter != 12*time.Minute {
				t.Fatalf("lost Retry-After: %+v", qerr)
			}
			if (status == 401 || status == 403) != qerr.AuthRejected() {
				t.Fatalf("auth classification wrong for %d", status)
			}
		})
	}
	for _, body := range []string{"SECRET_TOKEN", strings.Repeat("a", maxUsageBytes+1)} {
		f := fetcherWith(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		if _, err := f.Fetch(context.Background(), testCredential()); err == nil || strings.Contains(err.Error(), "SECRET_TOKEN") {
			t.Fatalf("unsanitized body failure: %v", err)
		}
	}
}

func TestCredentialsDoNotEscapeThroughRedirects(t *testing.T) {
	targetCalled := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalled = true }))
	defer target.Close()
	f := fetcherWith(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != ClaudeUsageURL {
			return http.DefaultTransport.RoundTrip(r)
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {target.URL}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	if _, err := f.Fetch(context.Background(), testCredential()); err == nil {
		t.Fatal("redirect accepted")
	}
	if targetCalled {
		t.Fatal("redirect destination was contacted")
	}
}

func TestCredentialEligibilityAndCancellation(t *testing.T) {
	for _, change := range []func(*Credential){
		func(c *Credential) { c.Disabled = true },
		func(c *Credential) { c.Type = "unsupported" },
		func(c *Credential) { c.AccessToken = "" },
		func(c *Credential) { c.BaseURL = "https://custom.test" },
	} {
		c := testCredential()
		change(&c)
		f := fetcherWith(func(*http.Request) (*http.Response, error) {
			t.Fatal("ineligible credential reached network")
			return nil, nil
		})
		if _, err := f.Fetch(context.Background(), c); err == nil {
			t.Fatal("ineligible credential accepted")
		}
	}
	f := fetcherWith(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, errors.New("SECRET_TOKEN")
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Fetch(ctx, testCredential()); err == nil || strings.Contains(err.Error(), "SECRET_TOKEN") {
		t.Fatalf("bad cancellation error: %v", err)
	}
}

func TestRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"120", 2 * time.Minute}, {"-5", 0}, {"9999999999", time.Hour}, {"not a date", 0},
		{testNow.Add(3 * time.Minute).Format(http.TimeFormat), 3 * time.Minute},
		{testNow.Add(-time.Minute).Format(http.TimeFormat), 0},
	} {
		if got := RetryAfter(tc.raw, testNow); got != tc.want {
			t.Fatalf("%s: got %s, want %s", tc.raw, got, tc.want)
		}
	}
}

func FuzzParseClaudeUsage(f *testing.F) {
	f.Add([]byte(validClaude))
	f.Add([]byte(`{"seven_day":null}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		s, err := ParseClaudeUsage(raw, testNow)
		if err != nil {
			return
		}
		w, ok := s.Window("weekly")
		if !ok || !validPercent(w.UsedPercent) || (w.UsedPercent != 0 && w.ResetsAt == nil) {
			t.Fatalf("accepted invalid weekly quota: %+v", s)
		}
	})
}

func FuzzParseCodexUsage(f *testing.F) {
	f.Add([]byte(`{"rate_limit":{"primary_window":{"used_percent":45,"limit_window_seconds":18000,"reset_after_seconds":3600},"secondary_window":{"used_percent":10,"limit_window_seconds":604800,"reset_after_seconds":3600}}}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		s, err := ParseCodexUsage(raw, testNow)
		if err != nil {
			return
		}
		long := false
		for _, w := range s.Windows {
			long = long || w.Long()
			if !validPercent(w.UsedPercent) {
				t.Fatalf("invalid percent: %+v", w)
			}
		}
		if !long {
			t.Fatal("accepted snapshot without long window")
		}
	})
}
