package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// The 1h upgrade applies only to native main conversations. A helper profile
// (for example a Haiku 5.5 Agent SDK web-search helper) is excluded even when
// the body-only probe check does not recognize it.
func TestClaudeNativeMainCacheTTL1hGate(t *testing.T) {
	cfg := &config.Config{}
	cfg.ClaudeCode.MainCacheTTL1h = true
	oauth := claudeFingerprintPolicy{AuthIsOAuthToken: true}

	tests := []struct {
		name                                     string
		cfg                                      *config.Config
		fp                                       claudeFingerprintPolicy
		confirmed, subagent, probe, helper, want bool
	}{
		{name: "main conversation", cfg: cfg, fp: oauth, confirmed: true, want: true},
		{name: "helper profile", cfg: cfg, fp: oauth, confirmed: true, helper: true},
		{name: "probe", cfg: cfg, fp: oauth, confirmed: true, probe: true},
		{name: "subagent", cfg: cfg, fp: oauth, confirmed: true, subagent: true},
		{name: "not confirmed", cfg: cfg, fp: oauth},
		{name: "api key credential", cfg: cfg, confirmed: true},
		{name: "flag off", cfg: &config.Config{}, fp: oauth, confirmed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := claudeNativeMainCacheTTL1h(tt.cfg, tt.fp, tt.confirmed, tt.subagent, tt.probe, tt.helper); got != tt.want {
				t.Fatalf("claudeNativeMainCacheTTL1h() = %v, want %v", got, tt.want)
			}
		})
	}
}
