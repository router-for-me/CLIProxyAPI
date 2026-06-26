package helps

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func testClaudeOAuthFingerprintConfig(mode string) *config.Config {
	return &config.Config{
		ClaudeOAuthFingerprint: config.ClaudeOAuthFingerprintConfig{
			Enabled:        true,
			Mode:           mode,
			MaxSessions:    4,
			SessionTTL:     "1h",
			LogFingerprint: false,
		},
	}
}

func testClaudeOAuthAuth(id string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       id,
		Provider: "claude",
		Metadata: map[string]any{"email": "john.doe@example.com"},
	}
}

func jsonUserPayload(deviceID, accountUUID, sessionID string) []byte {
	return []byte(`{"model":"claude-sonnet-4-5","metadata":{"user_id":"{\"device_id\":\"` + deviceID + `\",\"account_uuid\":\"` + accountUUID + `\",\"session_id\":\"` + sessionID + `\"}"},"messages":[{"role":"user","content":"hi"}]}`)
}

func TestClaudeOAuthFingerprintGate_PinsAccountIdentity(t *testing.T) {
	ResetClaudeOAuthFingerprintRegistry()
	cfg := testClaudeOAuthFingerprintConfig("monitor")
	auth := testClaudeOAuthAuth("auth-1")
	apiKey := "sk-ant-oat-test"

	body1 := jsonUserPayload("device-a", "account-a", "session-1")
	out1, res1, err1 := ClaudeOAuthFingerprintGate(context.Background(), cfg, auth, nil, body1, "claude-sonnet-4-5")
	if err1 != nil {
		t.Fatalf("first gate error = %v", err1)
	}
	if res1 == nil || res1.Slot != 1 {
		t.Fatalf("first slot = %v, want 1", res1)
	}

	body2 := jsonUserPayload("device-a", "account-a", "session-2")
	out2, res2, err2 := ClaudeOAuthFingerprintGate(context.Background(), cfg, auth, nil, body2, "claude-sonnet-4-5")
	if err2 != nil {
		t.Fatalf("second gate error = %v", err2)
	}
	if !strings.Contains(string(out1), "device-a") || !strings.Contains(string(out2), "device-a") {
		t.Fatalf("expected pinned device-a in outbound bodies: %s | %s", out1, out2)
	}
	if res2 == nil || res2.Slot != 2 {
		t.Fatalf("second slot = %v, want 2", res2)
	}
	if !ClaudeOAuthFingerprintEnabled(cfg, apiKey) {
		t.Fatal("expected fingerprint enabled for oauth token")
	}
}

func TestClaudeOAuthFingerprintGate_SessionLimitEnforce(t *testing.T) {
	ResetClaudeOAuthFingerprintRegistry()
	cfg := testClaudeOAuthFingerprintConfig("enforce")
	auth := testClaudeOAuthAuth("auth-limit")

	for i := 1; i <= 4; i++ {
		body := jsonUserPayload("device-a", "account-a", "session-"+itoa(i))
		_, _, err := ClaudeOAuthFingerprintGate(context.Background(), cfg, auth, nil, body, "claude-sonnet-4-5")
		if err != nil {
			t.Fatalf("session %d error = %v", i, err)
		}
	}

	body5 := jsonUserPayload("device-a", "account-a", "session-5")
	_, res5, err5 := ClaudeOAuthFingerprintGate(context.Background(), cfg, auth, nil, body5, "claude-sonnet-4-5")
	if err5 == nil {
		t.Fatal("expected enforce error for 5th session")
	}
	if res5 == nil || res5.Violation != claudeOAuthViolationSessionLimit {
		t.Fatalf("violation = %q, want %q", res5.Violation, claudeOAuthViolationSessionLimit)
	}
}

func TestClaudeOAuthFingerprintGate_SessionLimitMonitorAllows(t *testing.T) {
	ResetClaudeOAuthFingerprintRegistry()
	cfg := testClaudeOAuthFingerprintConfig("monitor")
	auth := testClaudeOAuthAuth("auth-monitor")

	for i := 1; i <= 4; i++ {
		body := jsonUserPayload("device-a", "account-a", "session-"+itoa(i))
		if _, _, err := ClaudeOAuthFingerprintGate(context.Background(), cfg, auth, nil, body, "claude-sonnet-4-5"); err != nil {
			t.Fatalf("session %d error = %v", i, err)
		}
	}

	body5 := jsonUserPayload("device-a", "account-a", "session-5")
	_, res5, err5 := ClaudeOAuthFingerprintGate(context.Background(), cfg, auth, nil, body5, "claude-sonnet-4-5")
	if err5 != nil {
		t.Fatalf("monitor mode should allow 5th session, got %v", err5)
	}
	if res5 == nil || res5.Violation != claudeOAuthViolationSessionLimit {
		t.Fatalf("violation = %q, want %q", res5.Violation, claudeOAuthViolationSessionLimit)
	}
}

func TestClaudeOAuthFingerprintGate_SameSessionDifferentModel(t *testing.T) {
	ResetClaudeOAuthFingerprintRegistry()
	cfg := testClaudeOAuthFingerprintConfig("monitor")
	auth := testClaudeOAuthAuth("auth-model")

	body := jsonUserPayload("device-a", "account-a", "session-shared")
	_, res1, err1 := ClaudeOAuthFingerprintGate(context.Background(), cfg, auth, nil, body, "claude-sonnet-4-5")
	if err1 != nil {
		t.Fatalf("first gate error = %v", err1)
	}
	_, res2, err2 := ClaudeOAuthFingerprintGate(context.Background(), cfg, auth, nil, body, "claude-opus-4-5")
	if err2 != nil {
		t.Fatalf("second gate error = %v", err2)
	}
	if res1.Slot != 1 || res2.Slot != 1 {
		t.Fatalf("slots = %d and %d, want both 1", res1.Slot, res2.Slot)
	}
}

func TestClaudeOAuthFingerprintGate_IdentityMismatchEnforce(t *testing.T) {
	ResetClaudeOAuthFingerprintRegistry()
	cfg := testClaudeOAuthFingerprintConfig("enforce")
	auth := testClaudeOAuthAuth("auth-identity")

	body1 := jsonUserPayload("device-a", "account-a", "session-1")
	if _, _, err := ClaudeOAuthFingerprintGate(context.Background(), cfg, auth, nil, body1, "m"); err != nil {
		t.Fatalf("first gate error = %v", err)
	}

	body2 := jsonUserPayload("device-b", "account-a", "session-2")
	_, res2, err2 := ClaudeOAuthFingerprintGate(context.Background(), cfg, auth, nil, body2, "m")
	if err2 == nil {
		t.Fatal("expected identity mismatch enforce error")
	}
	if res2 == nil || res2.Violation != claudeOAuthViolationIdentityMismatch {
		t.Fatalf("violation = %q, want %q", res2.Violation, claudeOAuthViolationIdentityMismatch)
	}
}

func TestClaudeOAuthFingerprintGate_SessionTTLRelease(t *testing.T) {
	ResetClaudeOAuthFingerprintRegistry()
	cfg := testClaudeOAuthFingerprintConfig("enforce")
	cfg.ClaudeOAuthFingerprint.SessionTTL = "50ms"
	auth := testClaudeOAuthAuth("auth-ttl")

	for i := 1; i <= 4; i++ {
		body := jsonUserPayload("device-a", "account-a", "session-"+itoa(i))
		if _, _, err := ClaudeOAuthFingerprintGate(context.Background(), cfg, auth, nil, body, "m"); err != nil {
			t.Fatalf("session %d error = %v", i, err)
		}
	}

	time.Sleep(60 * time.Millisecond)

	body5 := jsonUserPayload("device-a", "account-a", "session-5")
	_, res5, err5 := ClaudeOAuthFingerprintGate(context.Background(), cfg, auth, nil, body5, "m")
	if err5 != nil {
		t.Fatalf("expected slot after ttl expiry, got %v", err5)
	}
	if res5 == nil || res5.Slot == 0 {
		t.Fatalf("slot after expiry = %v, want >= 1", res5)
	}
}

func TestClaudeOAuthFingerprintGate_SessionHeaderMismatchFlag(t *testing.T) {
	ResetClaudeOAuthFingerprintRegistry()
	cfg := testClaudeOAuthFingerprintConfig("monitor")
	auth := testClaudeOAuthAuth("auth-mismatch")
	headers := http.Header{}
	headers.Set(ClaudeCodeSessionHeader, "header-session-id")
	body := jsonUserPayload("device-a", "account-a", "body-session-id")

	_, res, err := ClaudeOAuthFingerprintGate(context.Background(), cfg, auth, headers, body, "m")
	if err != nil {
		t.Fatalf("gate error = %v", err)
	}
	if res == nil || !res.SessionMismatch {
		t.Fatal("expected session mismatch flag")
	}
	if res.SessionID != "body-session-id" {
		t.Fatalf("session = %q, want body-session-id", res.SessionID)
	}
}

func TestFormatClaudeOAuthFingerprintLine_IncludesWarn(t *testing.T) {
	headers := http.Header{}
	headers.Set("User-Agent", "claude-cli/2.1.63 (external, cli)")
	headers.Set("Anthropic-Beta", "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14")
	headers.Set("X-Stainless-Package-Version", "0.74.0")
	headers.Set("X-Stainless-Os", "MacOS")
	headers.Set("X-Stainless-Arch", "arm64")
	headers.Set("X-Stainless-Runtime-Version", "v24.3.0")
	body := jsonUserPayload("device-a", "account-a", "body-session-id")
	result := &ClaudeOAuthFingerprintGateResult{
		SessionID:       "body-session-id",
		Slot:            2,
		DeviceID:        "device-a",
		AccountID:       "account-a",
		Format:          "json",
		SessionMismatch: true,
		HeaderSessionID: "header-sess",
		BodySessionID:   "body-session-id",
		Violation:       "-",
	}
	line := formatClaudeOAuthFingerprintLine(nil, headers, body, "claude-sonnet-4-5", result)
	if strings.Contains(line, "acct=") {
		t.Fatalf("line should not include acct: %s", line)
	}
	if !strings.Contains(line, "device=") {
		t.Fatalf("line missing device: %s", line)
	}
	if strings.Contains(line, "fmt=") || strings.Contains(line, "user=") {
		t.Fatalf("json format should use device= not fmt=/user=: %s", line)
	}
	if !strings.Contains(line, "ua=claude-cli/2.1.63") {
		t.Fatalf("line missing ua: %s", line)
	}
	if !strings.Contains(line, "beta=claude-code-20250219") {
		t.Fatalf("line missing beta: %s", line)
	}
	if !strings.Contains(line, "pkg=0.74.0") {
		t.Fatalf("line missing pkg: %s", line)
	}
	if !strings.Contains(line, "rtver=v24.3.0") {
		t.Fatalf("line missing rtver: %s", line)
	}
	if !strings.Contains(line, "os=MacOS") {
		t.Fatalf("line missing os: %s", line)
	}
	if !strings.Contains(line, "arch=arm64") {
		t.Fatalf("line missing arch: %s", line)
	}
	for _, omitted := range []string{"app=", "aver=", "runtime=", "lang=", "retry=", "timeout=", "xccs="} {
		if strings.Contains(line, omitted) {
			t.Fatalf("line should omit %s: %s", omitted, line)
		}
	}
	if !strings.Contains(line, "slot=2") {
		t.Fatalf("line missing slot: %s", line)
	}
	if !strings.Contains(line, "warn=session_mismatch") {
		t.Fatalf("line missing warn: %s", line)
	}
	if !strings.Contains(line, "hdr=header-") || !strings.Contains(line, "body=body-ses") {
		t.Fatalf("line missing hdr/body tokens: %s", line)
	}
}

func TestFormatClaudeOAuthFingerprintLine_LegacyUsesUser(t *testing.T) {
	userHash := strings.Repeat("b", 64)
	accountUUID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	userID := "user_" + userHash + "_account_" + accountUUID + "_session_" + sessionID
	body := []byte(`{"metadata":{"user_id":"` + userID + `"}}`)
	result := &ClaudeOAuthFingerprintGateResult{
		SessionID: sessionID,
		AccountID: accountUUID,
		UserHash:  userHash,
		Format:    "legacy",
	}
	line := formatClaudeOAuthFingerprintLine(nil, nil, body, "", result)
	if !strings.Contains(line, "user=bbbbbbbb") {
		t.Fatalf("line missing user: %s", line)
	}
	if strings.Contains(line, "device=") || strings.Contains(line, "fmt=") {
		t.Fatalf("legacy format should use user= not device=/fmt=: %s", line)
	}
}

func TestClaudeOAuthFingerprintLogEnabled_IgnoresCommercialMode(t *testing.T) {
	cfg := &config.Config{
		CommercialMode: true,
		ClaudeOAuthFingerprint: config.ClaudeOAuthFingerprintConfig{
			Enabled:        true,
			LogFingerprint: true,
		},
	}
	if !ClaudeOAuthFingerprintLogEnabled(cfg) {
		t.Fatal("log-fingerprint should be independent of commercial-mode")
	}
}

func TestClaudeOAuthFingerprintGate_LegacyUserID(t *testing.T) {
	ResetClaudeOAuthFingerprintRegistry()
	cfg := testClaudeOAuthFingerprintConfig("monitor")
	auth := testClaudeOAuthAuth("auth-legacy")
	userHash := strings.Repeat("a", 64)
	accountUUID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	userID := "user_" + userHash + "_account_" + accountUUID + "_session_" + sessionID
	body := []byte(`{"metadata":{"user_id":"` + userID + `"},"messages":[{"role":"user","content":"hi"}]}`)

	out, res, err := ClaudeOAuthFingerprintGate(context.Background(), cfg, auth, nil, body, "m")
	if err != nil {
		t.Fatalf("gate error = %v", err)
	}
	if res.Format != "legacy" {
		t.Fatalf("format = %q, want legacy", res.Format)
	}
	if !strings.Contains(string(out), userHash) {
		t.Fatalf("legacy outbound missing user hash: %s", out)
	}
}

func TestAppendClaudeOAuthFingerprintLogLine_PerAccountFile(t *testing.T) {
	dir := t.TempDir()
	cfg := testClaudeOAuthFingerprintConfig("monitor")
	cfg.ClaudeOAuthFingerprint.LogDir = dir
	auth := testClaudeOAuthAuth("auth-file")

	line := "06-26 13:31:46 session=abc device=dev account=acc violation=-"
	if err := appendClaudeOAuthFingerprintLogLine(cfg, auth, line); err != nil {
		t.Fatalf("appendClaudeOAuthFingerprintLogLine() error = %v", err)
	}

	path := filepath.Join(dir, "john.doe_example.com.log")
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	if !strings.Contains(string(raw), "session=abc") {
		t.Fatalf("unexpected log content: %s", raw)
	}
}
