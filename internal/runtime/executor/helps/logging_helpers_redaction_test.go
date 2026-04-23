package helps

import (
	"strings"
	"testing"
)

func TestSanitizeLoggedPayloadRedactsImageAndSessionFields(t *testing.T) {
	in := []byte(`{"b64_json":"ABCDEF","result":"IMAGEBASE64","partial_image_b64":"PARTIAL","image_url":"data:image/png;base64,AAAABBBB","session_id":"session-secret","x-codex-installation-id":"install-secret","ChatGPT-Account-ID":"acct-secret"}`)
	got := string(SanitizeLoggedPayload(in))
	for _, secret := range []string{"ABCDEF", "IMAGEBASE64", "PARTIAL", "AAAABBBB", "session-secret", "install-secret", "acct-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sanitized payload still contains %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("expected redaction marker, got %s", got)
	}
}
