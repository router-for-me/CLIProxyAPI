package oidc

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

type PKCECodes struct {
	CodeVerifier  string
	CodeChallenge string
}

func GeneratePKCECodes() (*PKCECodes, error) {
	verifier, err := generateCodeVerifier()
	if err != nil {
		return nil, err
	}
	return &PKCECodes{
		CodeVerifier:  verifier,
		CodeChallenge: generateCodeChallenge(verifier),
	}, nil
}

func generateCodeVerifier() (string, error) {
	buf := make([]byte, 96)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate code verifier failed: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func generateCodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
