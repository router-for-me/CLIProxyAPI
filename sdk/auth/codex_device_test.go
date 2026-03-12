package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"

	internalcodex "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
)

func TestBuildAuthRecordNormalizesTeamAliases(t *testing.T) {
	authenticator := NewCodexAuthenticator()
	authSvc := &internalcodex.CodexAuth{}
	accountID := "acct_business_123"
	email := "tester@example.com"
	bundle := &internalcodex.CodexAuthBundle{
		TokenData: internalcodex.CodexTokenData{
			IDToken:      testCodexDeviceJWT(t, email, accountID, "business"),
			AccessToken:  "access",
			RefreshToken: "refresh",
			AccountID:    accountID,
			Email:        email,
		},
		LastRefresh: "2026-03-10T00:00:00Z",
	}

	record, err := authenticator.buildAuthRecord(authSvc, bundle)
	if err != nil {
		t.Fatalf("build auth record: %v", err)
	}

	digest := sha256.Sum256([]byte(accountID))
	expectedName := "codex-" + hex.EncodeToString(digest[:])[:8] + "-" + email + "-team.json"
	if record.FileName != expectedName {
		t.Fatalf("expected file name %q, got %q", expectedName, record.FileName)
	}
	if got, _ := record.Metadata["plan_type"].(string); got != "team" {
		t.Fatalf("expected metadata plan_type team, got %#v", record.Metadata["plan_type"])
	}
	if got := record.Attributes["plan_type"]; got != "team" {
		t.Fatalf("expected attribute plan_type team, got %q", got)
	}
}

func testCodexDeviceJWT(t *testing.T, email, accountID, planType string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payloadRaw, err := json.Marshal(map[string]any{
		"email": email,
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
			"chatgpt_plan_type":  planType,
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadRaw)
	return header + "." + payload + ".signature"
}
