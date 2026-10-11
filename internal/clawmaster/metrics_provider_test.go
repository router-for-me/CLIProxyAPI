package clawmaster

import "testing"

// Measurements about Claude carry provider=claude; the pool's own (connections, the process) carry
// none, so another provider's pool can be told apart without renaming anything.
func TestProviderNamesWhoseServiceAMeasurementIsAbout(t *testing.T) {
	reader := startMetrics(t, nil)
	observeTokens("claude-test", "box", "acct", "project", tokenCounts{input: 1, output: 2})
	observeConnection("tls", "accepted")
	rm := collect(t, reader)
	want := map[string]string{
		"claw_master.inference.tokens":            "claude",
		"claw_master.inference.tokens.by_project": "claude",
		"claw_master.proxy.connections":           "",
		"claw_master.process.uptime_seconds":      "",
	}
	found := map[string]bool{}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			provider, checked := want[m.Name]
			if !checked {
				continue
			}
			for _, set := range attributeSets(m) {
				found[m.Name] = true
				if got := set["provider"]; got != provider {
					t.Errorf("%s provider = %q, want %q", m.Name, got, provider)
				}
			}
		}
	}
	for name := range want {
		if !found[name] {
			t.Errorf("%s was not recorded", name)
		}
	}
}
