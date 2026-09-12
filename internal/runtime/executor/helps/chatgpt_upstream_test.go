package helps

import (
	"net/url"
	"testing"
)

func TestIsChatGPTUpstreamURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "backend-api", raw: "https://chatgpt.com/backend-api/subscriptions", want: true},
		{name: "mixed case host", raw: "https://ChatGPT.com/backend-api/codex/responses", want: true},
		{name: "explicit 443", raw: "https://chatgpt.com:443/backend-api/subscriptions", want: true},
		{name: "http", raw: "http://chatgpt.com/backend-api/subscriptions", want: false},
		{name: "custom port", raw: "https://chatgpt.com:8443/backend-api/subscriptions", want: false},
		{name: "lookalike host", raw: "https://chatgpt.com.example/backend-api/subscriptions", want: false},
		{name: "userinfo", raw: "https://user:pass@chatgpt.com/backend-api/subscriptions", want: false},
		{name: "other https", raw: "https://api.example.com/v1/ping", want: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parsed, errParse := url.Parse(tc.raw)
			if errParse != nil {
				t.Fatalf("parse url: %v", errParse)
			}
			if got := IsChatGPTUpstreamURL(parsed); got != tc.want {
				t.Fatalf("IsChatGPTUpstreamURL(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
	if IsChatGPTUpstreamURL(nil) {
		t.Fatal("IsChatGPTUpstreamURL(nil) = true")
	}
}
