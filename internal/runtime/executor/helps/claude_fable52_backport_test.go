package helps

import "testing"

func TestClaudeFable52BackportBaseline(t *testing.T) {
	profile := defaultClaudeDeviceProfile(nil)
	if got := DefaultClaudeVersion(nil); got != "2.1.321" {
		t.Fatalf("DefaultClaudeVersion() = %q, want 2.1.321", got)
	}
	if profile.UserAgent != "claude-cli/2.1.321 (external, cli)" {
		t.Fatalf("UserAgent = %q", profile.UserAgent)
	}
	if profile.PackageVersion != "0.128.0" || profile.RuntimeVersion != "v26.12.1" {
		t.Fatalf("fingerprint package/runtime = %q/%q", profile.PackageVersion, profile.RuntimeVersion)
	}
}
