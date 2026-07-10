package executor

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/sirupsen/logrus"
)

func TestFingerprintObserveDisabledIsNoop(t *testing.T) {
	cfg := &config.Config{} // FingerprintObserve.Enabled defaults false
	if fingerprintObserveEnabled(cfg) {
		t.Fatal("expected observe disabled by default")
	}
	req, _ := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	req.Header.Set("User-Agent", "codex_cli_rs/0.144.1 (Mac OS 26.2.0; arm64) iTerm.app/3.6.10 (codex_cli_rs; 0.144.1)")
	// Must be a safe no-op (no panic) even with nil auth when disabled.
	observeCodexFingerprint(cfg, nil, req)
	observeClaudeFingerprint(cfg, nil, req)
}

func TestFingerprintObserveShapeHelpers(t *testing.T) {
	// session-id header shape via RAW keys (codex sets them bypassing canonicalization).
	hyphen := http.Header{}
	hyphen["session-id"] = []string{"uuid"}
	if got := fpSessionShape(hyphen); got != "session-id" {
		t.Fatalf("hyphen session shape = %q, want session-id", got)
	}
	under := http.Header{}
	under["session_id"] = []string{"uuid"}
	if got := fpSessionShape(under); got != "session_id" {
		t.Fatalf("underscore session shape = %q, want session_id", got)
	}
	if got := fpSessionShape(http.Header{}); got != "none" {
		t.Fatalf("empty session shape = %q, want none", got)
	}
	// fpHeaderRaw must find a raw-keyed header that http.Header.Get would miss.
	if got := fpHeaderRaw(hyphen, "Session-Id"); got != "uuid" {
		t.Fatalf("fpHeaderRaw = %q, want uuid", got)
	}
	// TLS profile host mapping.
	cfg := &config.Config{}
	if got := fpTLSProfile(cfg, "api.anthropic.com"); got != "node-h1" {
		t.Fatalf("anthropic profile = %q, want node-h1", got)
	}
	if got := fpTLSProfile(cfg, "chatgpt.com"); got != "chrome-h2" {
		t.Fatalf("chatgpt profile = %q, want chrome-h2", got)
	}
	cfg.DisableNodeTLSFingerprint = true
	if got := fpTLSProfile(cfg, "api.anthropic.com"); got != "chrome-h2(node-disabled)" {
		t.Fatalf("anthropic (node disabled) profile = %q", got)
	}
	// Account tag is stable + non-PII (never the raw email).
	auth := &cliproxyauth.Auth{ID: "acc-xyz", Provider: "codex", Metadata: map[string]any{"email": "user@example.com"}}
	tag1 := fpAccountTag(auth)
	tag2 := fpAccountTag(auth)
	if tag1 != tag2 || !strings.HasPrefix(tag1, "acct-") {
		t.Fatalf("account tag unstable/invalid: %q vs %q", tag1, tag2)
	}
	if strings.Contains(tag1, "example.com") {
		t.Fatalf("account tag leaks PII: %q", tag1)
	}
}

func TestFingerprintObserveEnabledEmitsThrottledLog(t *testing.T) {
	cfg := &config.Config{}
	cfg.FingerprintObserve.Enabled = true
	cfg.FingerprintObserve.MinIntervalSeconds = 1

	var buf bytes.Buffer
	oldOut := logrus.StandardLogger().Out
	oldLevel := logrus.GetLevel()
	logrus.SetOutput(&buf)
	logrus.SetLevel(logrus.InfoLevel)
	defer func() { logrus.SetOutput(oldOut); logrus.SetLevel(oldLevel) }()

	req, _ := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	req.Header.Set("User-Agent", "codex_cli_rs/0.144.1 (Mac OS 26.2.0; arm64) iTerm.app/3.6.10 (codex_cli_rs; 0.144.1)")
	req.Header["session-id"] = []string{"sid"}
	auth := &cliproxyauth.Auth{ID: "acc-fp-observe-test", Provider: "codex", Metadata: map[string]any{"account_id": "x"}}

	observeCodexFingerprint(cfg, auth, req)
	out := buf.String()
	if !strings.Contains(out, "FP-OBSERVE") || !strings.Contains(out, "codex_cli_rs/0.144.1") {
		t.Fatalf("expected FP-OBSERVE codex log with UA, got: %s", out)
	}
	if !strings.Contains(out, "session-id") {
		t.Fatalf("expected session_hdr=session-id in log, got: %s", out)
	}

	// Second call for the same account within the interval must be throttled (no new line).
	buf.Reset()
	observeCodexFingerprint(cfg, auth, req)
	if strings.Contains(buf.String(), "FP-OBSERVE") {
		t.Fatalf("expected throttled (no second log), got: %s", buf.String())
	}
}
