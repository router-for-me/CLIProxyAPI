package api

import (
	"strings"
	"testing"
)

func TestAccountLoginBridgePrecedesUpstreamBundle(t *testing.T) {
	original := `<html><head><script type="module">oldLogin()</script></head><body></body></html>`
	output := string(injectManagementBillingNav([]byte(original)))
	bridge := strings.Index(output, `id="cpa-account-bridge"`)
	bundle := strings.Index(output, `type="module"`)
	if bridge < 0 || bridge > bundle {
		t.Fatal("account redirect must run before legacy login bundle")
	}
	repeated := string(injectManagementBillingNav([]byte(output)))
	if strings.Count(repeated, `id="cpa-account-bridge"`) != 1 {
		t.Fatal("duplicate bridge")
	}
}
