package errorclass

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"body type rate limit wins over status", 400, `{"error":{"type":"rate_limit_error"}}`, ClassRateLimit},
		{"body code insufficient quota", 429, `{"error":{"code":"insufficient_quota"}}`, ClassQuota},
		{"body auth error", 200, `{"error":{"type":"authentication_error"}}`, ClassAuth},
		{"status 429", 429, ``, ClassRateLimit},
		{"status 401", 401, `not json`, ClassAuth},
		{"status 403", 403, ``, ClassPermission},
		{"status 404", 404, ``, ClassNotFound},
		{"status 400", 400, ``, ClassInvalidRequest},
		{"status 408", 408, ``, ClassTimeout},
		{"status 504", 504, ``, ClassTimeout},
		{"status 503", 503, ``, ClassServerError},
		{"keyword timeout", 500, `upstream connection timeout`, ClassTimeout},
		{"keyword overloaded", 529, `server overloaded`, ClassOverloaded},
		{"keyword content filter", 400, `blocked by content filter policy`, ClassContentFilter},
		{"keyword context length", 400, `This model's maximum context length is 8192 tokens`, ClassInvalidRequest},
		{"keyword connection refused", 500, `dial tcp: connection refused`, ClassConnection},
		{"keyword unauthorized", 400, `request unauthorized by upstream`, ClassAuth},
		{"body permission error", 200, `{"error":{"type":"permission_error"}}`, ClassPermission},
		{"body not found error", 200, `{"error":{"type":"not_found_error"}}`, ClassNotFound},
		{"body overloaded error", 200, `{"error":{"type":"overloaded_error"}}`, ClassOverloaded},
		{"body content filter code", 200, `{"error":{"code":"content_filter"}}`, ClassContentFilter},
		{"body server error type", 200, `{"error":{"type":"api_error"}}`, ClassServerError},
		{"unknown falls back to other", 418, `teapot`, ClassOther},
		{"empty body empty status", 0, ``, ClassOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.status, tc.body); got != tc.want {
				t.Fatalf("Classify(%d, %q) = %q, want %q", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

func TestClassifyNeverPanics(t *testing.T) {
	for _, body := range []string{"", "{", "[]", "{not json", `{"error":null}`} {
		_ = Classify(500, body) // must not panic
	}
}
