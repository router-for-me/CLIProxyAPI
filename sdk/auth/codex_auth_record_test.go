package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

func makeTestCodexJWT(planType, accountID string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	authInfo := map[string]any{
		"chatgpt_account_id": accountID,
	}
	if planType != "" {
		authInfo["chatgpt_plan_type"] = planType
	}
	claimsMap := map[string]any{
		"email":                       "user@example.com",
		"https://api.openai.com/auth": authInfo,
	}
	payloadBytes, _ := json.Marshal(claimsMap)
	claims := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return header + "." + claims + "."
}

func TestBuildAuthRecord_PlanTypeDefaultsToFreeWhenMissing(t *testing.T) {
	authenticator := NewCodexAuthenticator()
	authSvc := codex.NewCodexAuth(nil)

	idTokenWithoutPlan := makeTestCodexJWT("", "acc-12345")
	bundle := &codex.CodexAuthBundle{
		TokenData: codex.CodexTokenData{
			IDToken:      idTokenWithoutPlan,
			AccessToken:  "mock-access-token",
			RefreshToken: "mock-refresh-token",
			Email:        "user@example.com",
		},
	}

	auth, errBuild := authenticator.buildAuthRecord(authSvc, bundle)
	if errBuild != nil {
		t.Fatalf("buildAuthRecord error: %v", errBuild)
	}

	if got := auth.Attributes["plan_type"]; got != "free" {
		t.Errorf("auth.Attributes[plan_type] = %q, want free", got)
	}
	if got := auth.Metadata["plan_type"]; got != "free" {
		t.Errorf("auth.Metadata[plan_type] = %q, want free", got)
	}
	storage, ok := auth.Storage.(*codex.CodexTokenStorage)
	if !ok || storage == nil {
		t.Fatalf("auth.Storage is not *codex.CodexTokenStorage: %T", auth.Storage)
	}
	if storage.PlanType != "free" {
		t.Errorf("storage.PlanType = %q, want free", storage.PlanType)
	}
	if !strings.HasSuffix(auth.FileName, "-free.json") {
		t.Errorf("auth.FileName = %q, want suffix -free.json", auth.FileName)
	}
}

func TestBuildAuthRecord_PlanTypeExtractedWhenPresent(t *testing.T) {
	authenticator := NewCodexAuthenticator()
	authSvc := codex.NewCodexAuth(nil)

	idTokenWithPlan := makeTestCodexJWT("plus", "acc-12345")
	bundle := &codex.CodexAuthBundle{
		TokenData: codex.CodexTokenData{
			IDToken:      idTokenWithPlan,
			AccessToken:  "mock-access-token",
			RefreshToken: "mock-refresh-token",
			Email:        "user@example.com",
			PlanType:     "plus",
		},
	}

	auth, errBuild := authenticator.buildAuthRecord(authSvc, bundle)
	if errBuild != nil {
		t.Fatalf("buildAuthRecord error: %v", errBuild)
	}

	if got := auth.Attributes["plan_type"]; got != "plus" {
		t.Errorf("auth.Attributes[plan_type] = %q, want plus", got)
	}
	if got := auth.Metadata["plan_type"]; got != "plus" {
		t.Errorf("auth.Metadata[plan_type] = %q, want plus", got)
	}
	storage, ok := auth.Storage.(*codex.CodexTokenStorage)
	if !ok || storage == nil {
		t.Fatalf("auth.Storage is not *codex.CodexTokenStorage: %T", auth.Storage)
	}
	if storage.PlanType != "plus" {
		t.Errorf("storage.PlanType = %q, want plus", storage.PlanType)
	}
	if !strings.HasSuffix(auth.FileName, "-plus.json") {
		t.Errorf("auth.FileName = %q, want suffix -plus.json", auth.FileName)
	}
}
