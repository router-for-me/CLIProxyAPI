package misc

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

// GenerateRandomState generates a cryptographically secure random state parameter
// for OAuth2 flows to prevent CSRF attacks.
//
// Returns:
//   - string: A hexadecimal encoded random state string
//   - error: An error if the random generation fails, nil otherwise
func GenerateRandomState() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

// OAuthCallback captures the parsed OAuth callback parameters.
type OAuthCallback struct {
	Code             string
	State            string
	Error            string
	ErrorDescription string
}

// ParseOAuthCallback extracts OAuth parameters from a callback URL.
// It returns nil when the input is empty.
func ParseOAuthCallback(input string) (*OAuthCallback, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, nil
	}

	candidate := trimmed
	if !strings.Contains(candidate, "://") {
		if strings.HasPrefix(candidate, "?") {
			candidate = "http://localhost" + candidate
		} else if strings.ContainsAny(candidate, "/?#") || strings.Contains(candidate, ":") {
			candidate = "http://" + candidate
		} else if strings.Contains(candidate, "=") {
			candidate = "http://localhost/?" + candidate
		} else {
			return nil, fmt.Errorf("invalid callback URL")
		}
	}

	parsedURL, err := url.Parse(candidate)
	if err != nil {
		return nil, err
	}

	query := parsedURL.Query()
	code := strings.TrimSpace(query.Get("code"))
	state := strings.TrimSpace(query.Get("state"))
	errCode := strings.TrimSpace(query.Get("error"))
	errDesc := strings.TrimSpace(query.Get("error_description"))

	if parsedURL.Fragment != "" {
		if fragQuery, errFrag := url.ParseQuery(parsedURL.Fragment); errFrag == nil {
			if code == "" {
				code = strings.TrimSpace(fragQuery.Get("code"))
			}
			if state == "" {
				state = strings.TrimSpace(fragQuery.Get("state"))
			}
			if errCode == "" {
				errCode = strings.TrimSpace(fragQuery.Get("error"))
			}
			if errDesc == "" {
				errDesc = strings.TrimSpace(fragQuery.Get("error_description"))
			}
		}
	}

	if code != "" && state == "" && strings.Contains(code, "#") {
		parts := strings.SplitN(code, "#", 2)
		code = parts[0]
		state = parts[1]
	}

	if errCode == "" && errDesc != "" {
		errCode = errDesc
		errDesc = ""
	}

	if code == "" && errCode == "" {
		return nil, fmt.Errorf("callback URL missing code")
	}

	return &OAuthCallback{
		Code:             code,
		State:            state,
		Error:            errCode,
		ErrorDescription: errDesc,
	}, nil
}
