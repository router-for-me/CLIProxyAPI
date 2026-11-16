package management

import (
	"os"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestQuotaUsageHasCapacity(t *testing.T) {
	const claude = `"five_hour":{"utilization":10},"seven_day":{"utilization":20}`
	const windows = `"primary_window":{"used_percent":10},"secondary_window":{"used_percent":20}`
	tests := []struct {
		name, provider, body string
		want                 bool
		model                string
	}{
		{"claude_restored", "claude", `{` + claude + `}`, true, "claude-sonnet-4-5"},
		{"claude_normal_limits", "claude", `{` + claude + `,"limits":[{"kind":"session","percent":10},{"kind":"weekly_all","percent":20},{"kind":"weekly_scoped","percent":30,"scope":{"model":{"id":null,"display_name":"Fable 5"}}}]}`, true, "claude-fable-5-1"},
		{"claude_session_exhausted", "claude", `{` + claude + `,"limits":[{"kind":"session","percent":100}]}`, false, ""},
		{"claude_weekly_all_exhausted", "claude", `{` + claude + `,"limits":[{"kind":"weekly_all","percent":100}]}`, false, ""},
		{"claude_missing_base", "claude", `{"five_hour":{"utilization":1}}`, false, ""},
		{"claude_model_exhausted", "claude", `{` + claude + `,"seven_day_opus":{"utilization":100}}`, false, ""},
		{"claude_extra_cannot_override", "claude", `{"five_hour":{"utilization":100},"seven_day":{"utilization":20},"extra_usage":{"is_enabled":true,"monthly_limit":10000,"used_credits":0}}`, false, ""},
		{"claude_fable_display_name", "claude", `{` + claude + `,"limits":[{"kind":"weekly_scoped","percent":20,"scope":{"model":{"id":null,"display_name":"Fable 5"}}}]}`, true, "claude-fable-5-1"},
		{"claude_scoped_exhausted", "claude", `{` + claude + `,"limits":[{"kind":"weekly_scoped","percent":100,"scope":{"model":{"display_name":"Fable 5"}}}]}`, false, ""},
		{"claude_unknown_scope", "claude", `{` + claude + `,"limits":[{"kind":"weekly_scoped","percent":20,"scope":{"model":{"display_name":"Unknown"}}}]}`, false, ""},
		{"claude_unknown_blocked_model", "claude", `{` + claude + `}`, false, "unknown-model"},
		{"claude_null_percent", "claude", `{"five_hour":{"utilization":null},"seven_day":{"utilization":10}}`, false, ""},
		{"claude_negative_percent", "claude", `{"five_hour":{"utilization":-1},"seven_day":{"utilization":10}}`, false, ""},
		{"codex_restored", "codex", `{"rate_limit":{` + windows + `,"allowed":true,"limit_reached":false}}`, true, ""},
		{"codex_disallowed", "codex", `{"rate_limit":{` + windows + `,"allowed":false}}`, false, ""},
		{"codex_reached", "codex", `{"rate_limit":{` + windows + `,"limit_reached":true}}`, false, ""},
		{"codex_missing_window", "codex", `{"rate_limit":{"primary_window":{"used_percent":0}}}`, false, ""},
		{"codex_additional_exhausted", "codex", `{"rate_limit":{` + windows + `},"additional_rate_limits":[{"metered_feature":"model","rate_limit":{"primary_window":{"used_percent":100},"secondary_window":{"used_percent":1}}}]}`, false, ""},
		{"codex_review_exhausted", "codex", `{"rate_limit":{` + windows + `},"code_review_rate_limit":{"primary_window":{"used_percent":1},"secondary_window":{"used_percent":100}}}`, false, ""},
		{"codex_aliases", "codex", `{"rateLimit":{"primaryWindow":{"usedPercent":0},"secondaryWindow":{"usedPercent":1},"limitReached":false},"additionalRateLimits":[]}`, true, ""},
		{"codex_conflicting_alias", "codex", `{"rate_limit":{"primary_window":{"used_percent":20,"usedPercent":100},"secondary_window":{"used_percent":1}}}`, false, ""},
		{"codex_conflicting_alias_reverse", "codex", `{"rate_limit":{"primary_window":{"used_percent":100,"usedPercent":20},"secondary_window":{"used_percent":1}}}`, false, ""},
		{"codex_conflicting_flag", "codex", `{"rate_limit":{` + windows + `,"limit_reached":false,"limitReached":true}}`, false, ""},
		{"claude_unknown_limit_kind", "claude", `{` + claude + `,"limits":[{"kind":"unknown","percent":20}]}`, false, ""},
		{"codex_single_window", "codex", `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":7,"limit_window_seconds":604800},"secondary_window":null}}`, true, ""},
		{"codex_single_exhausted", "codex", `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":100},"secondary_window":null}}`, false, ""},
		{"codex_single_disallowed", "codex", `{"rate_limit":{"allowed":false,"primary_window":{"used_percent":7},"secondary_window":null}}`, false, ""},
		{"codex_single_reached", "codex", `{"rate_limit":{"limit_reached":true,"primary_window":{"used_percent":7},"secondary_window":null}}`, false, ""},
		{"codex_secondary_malformed", "codex", `{"rate_limit":{"primary_window":{"used_percent":7},"secondary_window":{}}}`, false, ""},
		{"codex_secondary_exhausted", "codex", `{"rate_limit":{"primary_window":{"used_percent":7},"secondary_window":{"used_percent":100}}}`, false, ""},
		{"invalid_json", "claude", `{`, false, ""},
		{"unknown_provider", "other", `{` + claude + `}`, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var auth *coreauth.Auth
			if tt.model != "" {
				auth = &coreauth.Auth{ModelStates: map[string]*coreauth.ModelState{tt.model: {Unavailable: true}}}
			}
			if got := quotaUsageHasCapacity(tt.provider, []byte(tt.body), auth); got != tt.want {
				t.Fatalf("capacity=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestQuotaVersionFirstClaudeModels(t *testing.T) {
	for _, tt := range []struct {
		name, model string
		want        bool
	}{
		{"sonnet", "claude-3-7-sonnet-20250219", true},
		{"haiku", "claude-3-5-haiku-20241022", true},
		{"unknown_prefix", "other-3-7-sonnet-20250219", false},
		{"prefixed", "fake-claude-3-7-sonnet-20250219", false},
		{"nonnumeric_version", "claude-three-7-sonnet-20250219", false},
		{"empty_version", "claude-3--sonnet-20250219", false},
		{"unknown_family", "claude-3-7-unknown-20250219", false},
		{"trailing_separator", "claude-3-7-sonnet-", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			auth := &coreauth.Auth{ModelStates: map[string]*coreauth.ModelState{tt.model: {Unavailable: true}}}
			body := []byte(`{"five_hour":{"utilization":1},"seven_day":{"utilization":2},"limits":[{"kind":"weekly_scoped","percent":3,"scope":{"model":{"id":"` + tt.model + `"}}}]}`)
			if got := quotaUsageHasCapacity("claude", body, auth); got != tt.want {
				t.Fatalf("capacity=%v want %v", got, tt.want)
			}
		})
	}
}

func TestQuotaClaudeWeeklyOnlyCapacity(t *testing.T) {
	fixture, err := os.ReadFile("testdata/claude-weekly-only-usage.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, old, replacement string
		want                   bool
	}{
		{name: "provider weekly-only response", want: true},
		{name: "whitespace around null", old: `"five_hour": null`, replacement: `"five_hour":  null `, want: true},
		{name: "missing session window", old: `"five_hour": null,`, replacement: ``},
		{name: "malformed session window", old: `"five_hour": null`, replacement: `"five_hour": {}`},
		{name: "session percent null", old: `"five_hour": null`, replacement: `"five_hour": {"utilization":null}`},
		{name: "session exhausted", old: `"five_hour": null`, replacement: `"five_hour": {"utilization":100}`},
		{name: "session negative", old: `"five_hour": null`, replacement: `"five_hour": {"utilization":-1}`},
		{name: "session string", old: `"five_hour": null`, replacement: `"five_hour": "null"`},
		{name: "weekly window null", old: `"seven_day": {`, replacement: `"seven_day": null, "ignored_weekly": {`},
		{name: "weekly window missing", old: `"seven_day": {`, replacement: `"ignored_weekly": {`},
		{name: "weekly null", old: `"utilization": 0.0`, replacement: `"utilization": null`},
		{name: "weekly exhausted", old: `"utilization": 0.0`, replacement: `"utilization": 100`},
		{name: "weekly missing percent", old: `"utilization": 0.0,`, replacement: ``},
		{name: "weekly negative", old: `"utilization": 0.0`, replacement: `"utilization": -1`},
		{name: "scoped legacy exhausted", old: `"seven_day_opus": null`, replacement: `"seven_day_opus": {"utilization":100}`},
		{name: "weekly limit exhausted", old: `"percent": 0`, replacement: `"percent": 100`},
		{name: "unknown scoped model", old: `"display_name": "Fable"`, replacement: `"display_name": "Unknown"`},
		{name: "session limit exhausted", old: `"limits": [`, replacement: `"limits": [{"kind":"session","percent":100},`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := string(fixture)
			if tc.old != "" {
				body = strings.Replace(body, tc.old, tc.replacement, 1)
				if body == string(fixture) {
					t.Fatal("fixture mutation did not match")
				}
			}
			auth := &coreauth.Auth{ModelStates: map[string]*coreauth.ModelState{"claude-opus-5-5": {Unavailable: true}}}
			if got := quotaUsageHasCapacity("claude", []byte(body), auth); got != tc.want {
				t.Fatalf("capacity=%v, want %v", got, tc.want)
			}
		})
	}
}
