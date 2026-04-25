// Package vertex provides Google Vertex AI authentication utilities.
package vertex

import (
	"context"
	"encoding/json"
	"fmt"
)

// Credential represents a Vertex AI service account credential.
type Credential struct {
	Type                    string `json:"type"`
	ProjectID               string `json:"project_id"`
	PrivateKeyID            string `json:"private_key_id"`
	PrivateKey              string `json:"private_key"`
	ClientEmail             string `json:"client_email"`
	ClientID                string `json:"client_id"`
	AuthURI                 string `json:"auth_uri"`
	TokenURI                string `json:"token_uri"`
	AuthProviderX509CertURL string `json:"auth_provider_x509_cert_url"`
	ClientX509CertURL       string `json:"client_x509_cert_url"`
}

// ValidateCredential validates a Vertex service account JSON.
func ValidateCredential(data []byte) (*Credential, error) {
	var cred Credential
	if err := json.Unmarshal(data, &cred); err != nil {
		return nil, fmt.Errorf("invalid credential JSON: %w", err)
	}
	if cred.Type != "service_account" {
		return nil, fmt.Errorf("credential type must be 'service_account', got %q", cred.Type)
	}
	if cred.ClientEmail == "" {
		return nil, fmt.Errorf("client_email is required")
	}
	return &cred, nil
}

// GetTokenSource returns a token source for the credential (stub).
func (c *Credential) GetTokenSource(ctx context.Context) (string, error) {
	// Stub implementation - would normally use oauth2/google
	return "", fmt.Errorf("token source not implemented in stub")
}
