// Package cursor implements Cursor OAuth PKCE authentication and token refresh.
package cursor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

const (
	CursorLoginURL   = "https://cursor.com/loginDeepControl"
	CursorPollURL    = "https://api2.cursor.sh/auth/poll"
	CursorRefreshURL = "https://api2.cursor.sh/auth/exchange_user_api_key"

	pollMaxAttempts      = 150
	pollBaseDelay        = 1 * time.Second
	pollMaxDelay         = 10 * time.Second
	pollBackoffMultiply  = 1.2
	maxConsecutiveErrors = 10
)

// AuthParams holds the PKCE parameters for Cursor login.
type AuthParams struct {
	Verifier  string
	Challenge string
	UUID      string
	LoginURL  string
}

// TokenPair holds the access and refresh tokens from Cursor.
type TokenPair struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

// GeneratePKCE creates a PKCE verifier and challenge pair.
func GeneratePKCE() (verifier, challenge string, err error) {
	verifierBytes := make([]byte, 96)
	if _, err = rand.Read(verifierBytes); err != nil {
		return "", "", fmt.Errorf("cursor: failed to generate PKCE verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(verifierBytes)

	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

// GenerateAuthParams creates the full set of auth params for Cursor login.
func GenerateAuthParams() (*AuthParams, error) {
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}

	uuidBytes := make([]byte, 16)
	if _, err = rand.Read(uuidBytes); err != nil {
		return nil, fmt.Errorf("cursor: failed to generate UUID: %w", err)
	}
	uuid := fmt.Sprintf("%x-%x-%x-%x-%x",
		uuidBytes[0:4], uuidBytes[4:6], uuidBytes[6:8], uuidBytes[8:10], uuidBytes[10:16])

	loginURL := fmt.Sprintf("%s?challenge=%s&uuid=%s&mode=login&redirectTarget=cli",
		CursorLoginURL, challenge, uuid)

	return &AuthParams{
		Verifier:  verifier,
		Challenge: challenge,
		UUID:      uuid,
		LoginURL:  loginURL,
	}, nil
}

// PollForAuth polls the Cursor auth endpoint until the user completes login.
func PollForAuth(ctx context.Context, uuid, verifier string) (*TokenPair, error) {
	delay := pollBaseDelay
	consecutiveErrors := 0

	client := &http.Client{Timeout: 10 * time.Second}

	for attempt := 0; attempt < pollMaxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}

		url := fmt.Sprintf("%s?uuid=%s&verifier=%s", CursorPollURL, uuid, verifier)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("cursor: failed to create poll request: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			consecutiveErrors++
			if consecutiveErrors >= maxConsecutiveErrors {
				return nil, fmt.Errorf("cursor: too many consecutive poll errors (last: %v)", err)
			}
			delay = minDuration(time.Duration(float64(delay)*pollBackoffMultiply), pollMaxDelay)
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			// Still waiting for user to authorize
			consecutiveErrors = 0
			delay = minDuration(time.Duration(float64(delay)*pollBackoffMultiply), pollMaxDelay)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var tokens TokenPair
			if err := json.Unmarshal(body, &tokens); err != nil {
				return nil, fmt.Errorf("cursor: failed to parse auth response: %w", err)
			}
			return &tokens, nil
		}

		return nil, fmt.Errorf("cursor: poll failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil, fmt.Errorf("cursor: authentication polling timeout (waited ~%.0f seconds)",
		float64(pollMaxAttempts)*pollMaxDelay.Seconds()/2)
}

// RefreshToken refreshes a Cursor access token using the refresh token.
func RefreshToken(ctx context.Context, refreshToken string) (*TokenPair, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CursorRefreshURL,
		strings.NewReader("{}"))
	if err != nil {
		return nil, fmt.Errorf("cursor: failed to create refresh request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+refreshToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cursor: token refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cursor: token refresh failed (status %d): %s", resp.StatusCode, string(body))
	}

	var tokens TokenPair
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("cursor: failed to parse refresh response: %w", err)
	}

	// Keep original refresh token if not returned
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = refreshToken
	}

	return &tokens, nil
}

// ParseJWTSub extracts the "sub" claim from a Cursor JWT access token.
// Cursor JWTs contain "sub" like "auth0|user_XXXX" which uniquely identifies
// the account. Returns empty string if parsing fails.
func ParseJWTSub(token string) string {
	decoded := decodeJWTPayload(token)
	if decoded == nil {
		return ""
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return ""
	}
	return claims.Sub
}

// SubToShortHash converts a JWT sub claim to a short hex hash for use in filenames.
// e.g. "auth0|user_2x..." → "a3f8b2c1"
func SubToShortHash(sub string) string {
	if sub == "" {
		return ""
	}
	h := sha256.Sum256([]byte(sub))
	return fmt.Sprintf("%x", h[:4]) // 8 hex chars
}

// decodeJWTPayload decodes the payload (middle) part of a JWT.
func decodeJWTPayload(token string) []byte {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload := parts[1]
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	payload = strings.ReplaceAll(payload, "-", "+")
	payload = strings.ReplaceAll(payload, "_", "/")
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil
	}
	return decoded
}

// GetTokenExpiry extracts the JWT expiry from an access token with a 5-minute safety margin.
// Falls back to 1 hour from now if the token can't be parsed.
func GetTokenExpiry(token string) time.Time {
	decoded := decodeJWTPayload(token)
	if decoded == nil {
		return time.Now().Add(1 * time.Hour)
	}

	var claims struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil || claims.Exp == 0 {
		return time.Now().Add(1 * time.Hour)
	}

	sec, frac := math.Modf(claims.Exp)
	expiry := time.Unix(int64(sec), int64(frac*1e9))
	// Subtract 5-minute safety margin
	return expiry.Add(-5 * time.Minute)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
