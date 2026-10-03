package refreshdiagnostic

import (
	"fmt"
	"strings"
	"testing"
)

func TestSafeRefreshDiagnostics(t *testing.T) {
	for _, test := range []struct {
		status       int
		body, reason string
	}{
		{401, `{"error":{"code":"refresh_token_expired","message":"secret@example.com token-secret"}}`, "refresh_token_expired"},
		{400, `{"error":"invalid_grant","code":"refresh_token_reused"}`, "refresh_token_reused"},
		{401, `{"error":{"code":"access_token_expired"}}`, ""},
		{503, `{"code":"refresh_token_expired"}`, ""},
		{401, `token-secret`, ""},
	} {
		err := FromResponse(test.status, []byte(test.body))
		if got := Reason(fmt.Errorf("wrapped: %w", err)); got != test.reason {
			t.Fatalf("reason %q, want %q", got, test.reason)
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "@") {
			t.Fatal("upstream details leaked")
		}
	}
}

func TestInvalidGrantRemainsClassifiableWithoutLeakingDetails(t *testing.T) {
	for _, body := range []string{`{"error":"invalid_grant","error_description":"secret"}`, `{"error":{"code":"invalid_grant","message":"secret"}}`} {
		err := FromResponse(400, []byte(body))
		if !strings.Contains(err.Error(), "invalid_grant") || strings.Contains(err.Error(), "secret") || Reason(err) != "" {
			t.Fatalf("unexpected diagnostic: %v", err)
		}
	}
}
