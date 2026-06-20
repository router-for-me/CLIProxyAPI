// Package codex provides authentication and token management functionality
// for OpenAI's Codex AI services. It handles OAuth2 PKCE (Proof Key for Code Exchange)
// code generation for secure authentication flows.
package codex

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// GeneratePKCECodes generates a new pair of PKCE (Proof Key for Code Exchange) codes.
// It creates a cryptographically random code verifier and its corresponding
// SHA256 code challenge, as specified in RFC 7636. This is a critical security
// feature for the OAuth 2.0 authorization code flow.
func GeneratePKCECodes() (*PKCECodes, error) {
	// Generate code verifier: 43-128 characters, URL-safe
	codeVerifier, err := generateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("failed to generate code verifier: %w", err)
	}

	// Generate code challenge using S256 method
	codeChallenge := generateCodeChallenge(codeVerifier)

	return &PKCECodes{
		CodeVerifier:  codeVerifier,
		CodeChallenge: codeChallenge,
	}, nil
}

// generateCodeVerifier creates a cryptographically secure random string to be used
// as the code verifier in the PKCE flow. The verifier is a high-entropy string
// that is later used to prove possession of the client that initiated the
// authorization request.
func generateCodeVerifier() (string, error) {
	// Generate 96 random bytes (will result in 128 base64 characters)
	bytes := make([]byte, 96)
	_, err := rand.Read(bytes)
	if err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Encode to URL-safe base64 without padding
	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(bytes), nil
}

// generateCodeChallenge creates a code challenge from a given code verifier.
// The challenge is derived by taking the SHA256 hash of the verifier and then
// Base64 URL-encoding the result. This is sent in the initial authorization
// request and later verified against the verifier.
func generateCodeChallenge(codeVerifier string) string {
	hash := sha256.Sum256([]byte(codeVerifier))
	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(hash[:])
}
