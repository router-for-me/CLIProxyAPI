package codex

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestSanitizedOAuthErrorAcceptsOnlyExactKnownCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		code string
	}{
		{"flat", `{"error":"invalid_grant"}`, "invalid_grant"},
		{"top-level", `{"code":"invalid_client","error":null}`, "invalid_client"},
		{"nested", `{"error":{"code":"invalid_grant","message":"SECRET"}}`, "invalid_grant"},
		{"terminal-priority", `{"code":"refresh_token_reused","error":{"code":"invalid_grant"}}`, "refresh_token_reused"},
		{"nested-terminal-priority", `{"code":"invalid_grant","error":{"code":"refresh_token_reused"}}`, "refresh_token_reused"},
		{"unknown-flat", `{"error":"SECRET"}`, "oauth_error"},
		{"unknown-nested", `{"error":{"code":"SECRET","message":"refresh_token_reused"}}`, "oauth_error"},
		{"substring", `{"error":{"code":"refresh_token_reused_SECRET"}}`, "oauth_error"},
		{"case", `{"error":{"code":"REFRESH_TOKEN_REUSED"}}`, "oauth_error"},
		{"whitespace", `{"error":{"code":" refresh_token_reused"}}`, "oauth_error"},
		{"wrong-type", `{"error":{"code":["refresh_token_reused"]}}`, "oauth_error"},
		{"array", `{"error":[{"code":"refresh_token_reused"}]}`, "oauth_error"},
		{"malformed", `{"error":{"code":"refresh_token_reused"}`, "oauth_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizedOAuthError("token refresh", 400, []byte(tc.body))
			want := "token refresh failed with status 400: " + tc.code
			if got.Error() != want {
				t.Fatalf("got %q, want %q", got.Error(), want)
			}
		})
	}
}

func TestSanitizedOAuthIOErrorPreservesWrappedCancellationWithoutSecrets(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		err := sanitizedOAuthIOError("safe operation failure", fmt.Errorf("SECRET: %w", cause))
		if !errors.Is(err, cause) || err.Error() != cause.Error() {
			t.Fatal("wrapped cancellation must retain its class without its untrusted message")
		}
	}
	err := sanitizedOAuthIOError("safe operation failure", errors.New("SECRET"))
	if err.Error() != "safe operation failure" {
		t.Fatal("arbitrary I/O errors must be replaced")
	}
}
