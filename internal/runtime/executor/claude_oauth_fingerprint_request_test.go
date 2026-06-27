package executor

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeOAuthFingerprintFinalizeRemovesCCHBeforeLegacySigning(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.195.abc; cc_entrypoint=cli; cch=12345;"}],"messages":[]}`)
	out := removeClaudeBillingHeaderCCH(body)
	header := gjson.GetBytes(out, "system.0.text").String()
	if strings.Contains(header, "cch=") {
		t.Fatalf("cch should be removed before legacy signing, got %q", header)
	}
	signed := signAnthropicMessagesBody(out)
	if gjson.GetBytes(signed, "system.0.text").String() != header {
		t.Fatalf("legacy signing should leave cch-free header unchanged")
	}
}

func TestNormalizeClaudeOAuthStableBillingHeaderText(t *testing.T) {
	header := "x-anthropic-billing-header: cc_version=2.1.63.abc; cc_entrypoint=vscode; cch=12345;"
	out, changed := normalizeClaudeOAuthStableBillingHeaderText(header)
	if !changed {
		t.Fatal("expected billing header change")
	}
	if !strings.Contains(out, "cc_version=2.1.195.abc") {
		t.Fatalf("cc_version not normalized: %q", out)
	}
	if !strings.Contains(out, "cc_entrypoint=cli") {
		t.Fatalf("entrypoint not normalized: %q", out)
	}
}
