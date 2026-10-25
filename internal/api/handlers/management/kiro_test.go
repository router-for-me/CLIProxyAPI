package management

import (
	"net/url"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestKiroQuotaResolvesAccountRegionAndProfile(t *testing.T) {
	auth := &coreauth.Auth{Provider: "kiro", Metadata: map[string]any{"region": "us-east-1", "profile_arn": "arn:aws:codewhisperer:eu-central-1:123:profile/test"}}
	target, _ := url.Parse("https://management.us-east-1.kiro.dev/getUsageLimits?profileArn=caller-profile")
	if err := prepareCredentialQuotaRequest(auth, target, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if target.Host != "management.eu-central-1.kiro.dev" || target.Query().Get("profileArn") != auth.Metadata["profile_arn"] || target.Query().Get("resourceType") != "AGENTIC_REQUEST" || target.Query().Get("isEmailRequired") != "true" {
		t.Fatalf("quota target: %s", target)
	}
	for _, test := range []struct {
		host, path string
		want       bool
	}{
		{"q.us-east-1.amazonaws.com", "/getUsageLimits", false},
		{"q.eu-central-1.amazonaws.com", "/getUsageLimits", false},
		{"management.us-east-1.kiro.dev", "/getUsageLimits", true},
		{"management.eu-central-1.kiro.dev", "/getUsageLimits", true},
		{"management.us-west-2.kiro.dev", "/getUsageLimits", false},
		{"q.us-east-1.amazonaws.com", "/getUsageLimits/other", false},
		{"q.us-east-1.amazonaws.com.attacker.test", "/getUsageLimits", false},
		{"example.com", "/getUsageLimits", false},
	} {
		if got := hubQuotaRouteAllowed("kiro", test.host, test.path); got != test.want {
			t.Errorf("allow %s%s = %v", test.host, test.path, got)
		}
	}
}
