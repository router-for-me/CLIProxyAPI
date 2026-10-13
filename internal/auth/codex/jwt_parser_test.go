package codex

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func makeTestJWT(payload map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payloadBytes, _ := json.Marshal(payload)
	claims := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return header + "." + claims + "."
}

func TestGetPlanType(t *testing.T) {
	var nilClaims *JWTClaims
	if got := nilClaims.GetPlanType(); got != DefaultPlanType {
		t.Fatalf("nilClaims.GetPlanType() = %q, want %q", got, DefaultPlanType)
	}

	emptyClaims := &JWTClaims{}
	if got := emptyClaims.GetPlanType(); got != DefaultPlanType {
		t.Fatalf("emptyClaims.GetPlanType() = %q, want %q", got, DefaultPlanType)
	}

	whitespaceClaims := &JWTClaims{
		CodexAuthInfo: CodexAuthInfo{
			ChatgptPlanType: "   ",
		},
	}
	if got := whitespaceClaims.GetPlanType(); got != DefaultPlanType {
		t.Fatalf("whitespaceClaims.GetPlanType() = %q, want %q", got, DefaultPlanType)
	}

	proClaims := &JWTClaims{
		CodexAuthInfo: CodexAuthInfo{
			ChatgptPlanType: "pro",
		},
	}
	if got := proClaims.GetPlanType(); got != "pro" {
		t.Fatalf("proClaims.GetPlanType() = %q, want %q", got, "pro")
	}
}

func TestParseJWTToken_MissingPlanTypeDefaultsToFree(t *testing.T) {
	jwtWithoutPlan := makeTestJWT(map[string]any{
		"email": "user@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acc-12345",
		},
	})

	claims, errParse := ParseJWTToken(jwtWithoutPlan)
	if errParse != nil {
		t.Fatalf("ParseJWTToken failed: %v", errParse)
	}
	if got := claims.GetPlanType(); got != "free" {
		t.Fatalf("claims.GetPlanType() = %q, want %q", got, "free")
	}

	jwtWithPlan := makeTestJWT(map[string]any{
		"email": "user@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acc-12345",
			"chatgpt_plan_type":  "team",
		},
	})

	claimsTeam, errParseTeam := ParseJWTToken(jwtWithPlan)
	if errParseTeam != nil {
		t.Fatalf("ParseJWTToken failed: %v", errParseTeam)
	}
	if got := claimsTeam.GetPlanType(); got != "team" {
		t.Fatalf("claimsTeam.GetPlanType() = %q, want %q", got, "team")
	}
}
