package helps

import (
	"net/http"
	"testing"
)

func TestIsAntigravityAccountSuspended(t *testing.T) {
	const errorInfoBody = `{"error":{"code":403,"message":"This service has been disabled in this account for violation of Terms of Service. Please submit an appeal to continue using this product.","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"TOS_VIOLATION","domain":"cloudcode-pa.googleapis.com","metadata":{"appeal_url":"https://forms.gle/hGzM9MEUv2azZsrb9"}}]}}`
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{name: "error info", status: http.StatusForbidden, body: errorInfoBody, want: true},
		{name: "reason without the usual message", status: http.StatusForbidden, body: `{"error":{"code":403,"message":"denied","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"TOS_VIOLATION"}]}}`, want: true},
		{name: "older message only", status: http.StatusForbidden, body: `{"error":{"code":403,"message":"This service has been disabled in this account for violation of Terms of Service. If you believe this is an error, contact gemini-code-assist-user-feedback@google.com.","status":"PERMISSION_DENIED"}}`, want: true},
		{name: "plain 403", status: http.StatusForbidden, body: `{"error":{"code":403,"message":"The caller does not have permission","status":"PERMISSION_DENIED"}}`},
		{name: "other error info reason", status: http.StatusForbidden, body: `{"error":{"code":403,"message":"denied","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"SERVICE_DISABLED"}]}}`},
		{name: "not json", status: http.StatusForbidden, body: `forbidden`},
		{name: "not a 403", status: http.StatusBadRequest, body: errorInfoBody},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAntigravityAccountSuspended(tc.status, []byte(tc.body)); got != tc.want {
				t.Fatalf("IsAntigravityAccountSuspended() = %v, want %v", got, tc.want)
			}
		})
	}
}
