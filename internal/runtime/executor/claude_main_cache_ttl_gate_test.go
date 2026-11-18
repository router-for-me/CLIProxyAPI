package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
)

// The 1h upgrade applies only to native main conversations. A helper profile
// (for example a Haiku 5.5 Agent SDK web-search helper) is excluded even when
// the body-only probe check does not recognize it. A request that only has the
// Claude Code shape (which cloak_mode never treats as confirmed) is excluded
// too: the gate reads the detection, not the policy-promoted confirmation.
func TestClaudeNativeMainCacheTTL1hGate(t *testing.T) {
	cfg := &config.Config{}
	cfg.ClaudeCode.MainCacheTTL1h = true
	oauth := claudeFingerprintPolicy{AuthIsOAuthToken: true}

	native := helps.ClaudeCodeRequestDetection{Confirmed: true, NativeClient: true}
	helper := native
	helper.HelperProfile = true
	shapeOnly := helps.ClaudeCodeRequestDetection{ConfirmedShape: true}
	nonNative := helps.ClaudeCodeRequestDetection{Confirmed: true}

	tests := []struct {
		name            string
		cfg             *config.Config
		fp              claudeFingerprintPolicy
		detection       helps.ClaudeCodeRequestDetection
		subagent, probe bool
		want            bool
	}{
		{name: "main conversation", cfg: cfg, fp: oauth, detection: native, want: true},
		{name: "helper profile", cfg: cfg, fp: oauth, detection: helper},
		{name: "shape only", cfg: cfg, fp: oauth, detection: shapeOnly},
		{name: "not a native client", cfg: cfg, fp: oauth, detection: nonNative},
		{name: "probe", cfg: cfg, fp: oauth, detection: native, probe: true},
		{name: "subagent", cfg: cfg, fp: oauth, detection: native, subagent: true},
		{name: "api key credential", cfg: cfg, detection: native},
		{name: "flag off", cfg: &config.Config{}, fp: oauth, detection: native},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := claudeNativeMainCacheTTL1h(tt.cfg, tt.fp, tt.detection, tt.subagent, tt.probe); got != tt.want {
				t.Fatalf("claudeNativeMainCacheTTL1h() = %v, want %v", got, tt.want)
			}
		})
	}
}
