package xai

import (
	"bytes"
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
	// UsingAPIKey is the optional credential field that explicitly selects the
	// official API (true) or Grok CLI transport (false).
	UsingAPIKey = "using_api"
)

// StandardAPIHint describes the unverified routing hint carried by an xAI
// OAuth access token. It is not an authorization result; xAI still validates
// the token and decides whether the requested endpoint is allowed.
type StandardAPIHint uint8

const (
	StandardAPIHintUnknown StandardAPIHint = iota
	StandardAPIHintNo
	StandardAPIHintYes
)

// AccessTokenStandardAPIHint extracts xAI's undocumented tier claim as a
// transport hint. Missing, malformed, and unknown claims remain unknown so
// callers can fail closed to the Grok CLI transport.
func AccessTokenStandardAPIHint(accessToken string) StandardAPIHint {
	parts := strings.SplitN(strings.TrimSpace(accessToken), ".", 3)
	if len(parts) < 2 || strings.TrimSpace(parts[1]) == "" {
		return StandardAPIHintUnknown
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return StandardAPIHintUnknown
	}
	var claims struct {
		Tier json.RawMessage `json:"tier"`
	}
	if err = json.Unmarshal(payload, &claims); err != nil {
		return StandardAPIHintUnknown
	}
	return standardAPIHintFromTierClaim(claims.Tier)
}

// AccessTokenSuggestsStandardAPI reports whether the unverified token hint
// prefers the standard API. Unknown claims deliberately return false.
func AccessTokenSuggestsStandardAPI(accessToken string) bool {
	return AccessTokenStandardAPIHint(accessToken) == StandardAPIHintYes
}

// ExplicitUsingAPI parses a credential's explicit route override. Attributes
// take precedence over metadata, and invalid values are treated as unset.
func ExplicitUsingAPI(attributes map[string]string, metadata map[string]any) (value, ok bool) {
	if len(attributes) > 0 {
		if raw := strings.TrimSpace(attributes[UsingAPIKey]); raw != "" {
			if parsed, errParse := strconv.ParseBool(raw); errParse == nil {
				return parsed, true
			}
		}
	}
	if len(metadata) == 0 {
		return false, false
	}
	raw, exists := metadata[UsingAPIKey]
	if !exists || raw == nil {
		return false, false
	}
	switch typed := raw.(type) {
	case bool:
		return typed, true
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(typed))
		if errParse == nil {
			return parsed, true
		}
	}
	return false, false
}

func standardAPIHintFromTierClaim(raw json.RawMessage) StandardAPIHint {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return StandardAPIHintUnknown
	}

	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		value, errParse := number.Int64()
		if errParse != nil {
			return StandardAPIHintUnknown
		}
		if value > 0 {
			return StandardAPIHintYes
		}
		return StandardAPIHintNo
	}

	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return StandardAPIHintUnknown
	}
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return StandardAPIHintUnknown
	}
	if value, errParse := strconv.ParseInt(text, 10, 64); errParse == nil {
		if value > 0 {
			return StandardAPIHintYes
		}
		return StandardAPIHintNo
	}
	if text == "free" {
		return StandardAPIHintNo
	}
	return StandardAPIHintUnknown
}
