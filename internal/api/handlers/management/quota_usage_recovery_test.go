package management

import (
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
