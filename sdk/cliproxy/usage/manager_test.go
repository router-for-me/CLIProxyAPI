package usage_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// TestContextKeyConstants guards against accidental renames of the SDK
// constants that plugin authors reference. The string values must stay
// stable because they are the contract between the runtime and plugins.
func TestContextKeyConstants(t *testing.T) {
	cases := []struct {
		name  string
		got   string
		want  string
	}{
		{"CtxUpstreamURL", usage.CtxUpstreamURL, "upstream_url"},
		{"CtxFirstUserMsg", usage.CtxFirstUserMsg, "upstream_first_user_msg"},
		{"CtxResponseText", usage.CtxResponseText, "upstream_response_text"},
		{"CtxRawUsage", usage.CtxRawUsage, "upstream_raw_usage"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}
