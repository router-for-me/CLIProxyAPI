package util_test

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// TestMaskSensitiveQueryMasksValue covers the related half of issue #6666:
// deleting a client API key by value passes the secret as ?value=, so the
// request logger must mask it like other secret-bearing parameters.
func TestMaskSensitiveQueryMasksValue(t *testing.T) {
	secretKey := "sk-secret-client-key"

	t.Run("value parameter is masked", func(t *testing.T) {
		masked := util.MaskSensitiveQuery("value=" + secretKey)
		if strings.Contains(masked, secretKey) {
			t.Fatalf("value parameter leaked in full: %s", masked)
		}
		if masked != "value=sk-s...-key" {
			t.Fatalf("masked=%q want the HideAPIKey form", masked)
		}
	})

	t.Run("index parameter is untouched", func(t *testing.T) {
		masked := util.MaskSensitiveQuery("index=3")
		if masked != "index=3" {
			t.Fatalf("masked=%q want %q", masked, "index=3")
		}
	})

	t.Run("known secret parameters stay masked", func(t *testing.T) {
		masked := util.MaskSensitiveQuery("key=" + secretKey + "&access_token=" + secretKey)
		if strings.Contains(masked, secretKey) {
			t.Fatalf("secret parameter leaked in full: %s", masked)
		}
	})
}

func TestMaskSensitiveHeaderValueMasksCookiesAndSecrets(t *testing.T) {
	for _, tc := range []struct {
		key      string
		value    string
		wantLeak string
	}{
		{key: "Authorization", value: "Bearer super-secret-token-value", wantLeak: "super-secret-token-value"},
		{key: "X-Api-Key", value: "sk-super-secret-api-key", wantLeak: "sk-super-secret-api-key"},
		{key: "Cookie", value: "session=super-secret-cookie", wantLeak: "super-secret-cookie"},
		{key: "Set-Cookie", value: "__cf_bm=super-secret-cf-cookie; Path=/; HttpOnly", wantLeak: "super-secret-cf-cookie"},
	} {
		masked := util.MaskSensitiveHeaderValue(tc.key, tc.value)
		if strings.Contains(masked, tc.wantLeak) {
			t.Fatalf("MaskSensitiveHeaderValue(%q) leaked %q in %q", tc.key, tc.wantLeak, masked)
		}
	}

	if got := util.MaskSensitiveHeaderValue("X-Request-Id", "req-plain-123"); got != "req-plain-123" {
		t.Fatalf("MaskSensitiveHeaderValue(X-Request-Id) = %q, want unchanged", got)
	}
}
