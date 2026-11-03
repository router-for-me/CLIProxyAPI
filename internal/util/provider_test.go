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
