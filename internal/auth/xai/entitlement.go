package xai

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
)

const (
	// FreeOAuthModel is the model exposed by the Grok CLI free entitlement.
	FreeOAuthModel = "grok-4.5"
	// ComposerModelPrefix identifies models served only by the Grok CLI proxy.
	ComposerModelPrefix = "grok-composer-"
)

// AccessTokenHasStandardAPITier reports whether an xAI OAuth access token has
// a positive API tier. The claim is used only to select an upstream transport;
// the upstream still authenticates and authorizes every request.
func AccessTokenHasStandardAPITier(accessToken string) bool {
	parts := strings.Split(strings.TrimSpace(accessToken), ".")
	if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
		return false
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Tier json.RawMessage `json:"tier"`
	}
	if err = json.Unmarshal(payload, &claims); err != nil {
		return false
	}
	return positiveTierClaim(claims.Tier)
}

// OAuthModelUsesGrokCLI reports whether an OAuth request belongs on the Grok
// CLI proxy. Free OAuth accounts use the CLI transport, while paid accounts use
// the standard API except for CLI-only composer models.
func OAuthModelUsesGrokCLI(authKind, accessToken, model string) bool {
	if !strings.EqualFold(strings.TrimSpace(authKind), "oauth") {
		return false
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), ComposerModelPrefix) {
		return true
	}
	return !AccessTokenHasStandardAPITier(accessToken)
}

func positiveTierClaim(raw json.RawMessage) bool {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}

	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		value, errParse := strconv.ParseFloat(number.String(), 64)
		return errParse == nil && value > 0
	}

	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return false
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	if value, errParse := strconv.ParseFloat(text, 64); errParse == nil {
		return value > 0
	}
	return false
}
