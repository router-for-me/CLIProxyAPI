package executor

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestProviderRetryAfterIsForwardedVerbatim(t *testing.T) {
	date := "Wed, 21 Oct 2026 07:28:00 GMT"
	for _, tc := range []struct {
		name    string
		status  int
		header  string
		want    string
		message string
	}{
		{name: "seconds", status: 429, header: "120", want: "120"},
		{name: "http date", status: 429, header: date, want: date},
		{name: "not a delay", status: 429, header: "soon"},
		{name: "negative", status: 429, header: "-5"},
		{name: "absent", status: 429},
		{name: "other status", status: 503, header: "30"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := http.Header{}
			if tc.header != "" {
				headers.Set("Retry-After", tc.header)
			}
			body := []byte(`{"error":{"message":"limited"}}`)
			for name, err := range map[string]error{
				"openai-compat": newOpenAICompatStatusError(tc.status, headers, body),
				"claude":        classifyClaudeUpstreamError(tc.status, headers, body),
			} {
				got := auth.SafeResponseHeaders(err).Get("Retry-After")
				if got != tc.want {
					t.Errorf("%s: Retry-After = %q, want %q", name, got, tc.want)
				}
			}
		})
	}
}
