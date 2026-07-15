package executor

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// agentAssertionScheme is the Authorization scheme used by Codex agent identity auths.
const agentAssertionScheme = "AgentAssertion"

// agentAssertion is the signed identity envelope carried in the Authorization header.
type agentAssertion struct {
	AgentRuntimeID string `json:"agent_runtime_id"`
	TaskID         string `json:"task_id"`
	Timestamp      string `json:"timestamp"`
	Signature      string `json:"signature"`
}

// agentIdentityCreds holds agent identity credential material extracted from auth metadata.
type agentIdentityCreds struct {
	runtimeID     string
	privateKeyB64 string
	taskID        string
	accountID     string
}

func agentIdentityMetadataString(auth *cliproxyauth.Auth, keys ...string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	for _, key := range keys {
		if v, ok := auth.Metadata[key].(string); ok {
			if trimmed := strings.TrimSpace(v); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// agentIdentityCredsFromAuth extracts agent identity credentials from auth metadata.
// Canonical keys are agent_runtime_id, agent_private_key and task_id; legacy
// private_key_pkcs8_base64 and private_key spellings are accepted as aliases.
func agentIdentityCredsFromAuth(auth *cliproxyauth.Auth) agentIdentityCreds {
	return agentIdentityCreds{
		runtimeID:     agentIdentityMetadataString(auth, "agent_runtime_id"),
		privateKeyB64: agentIdentityMetadataString(auth, "agent_private_key", "private_key_pkcs8_base64", "private_key"),
		taskID:        agentIdentityMetadataString(auth, "task_id"),
		accountID:     agentIdentityMetadataString(auth, "account_id", "chatgpt_account_id"),
	}
}

// isAgentIdentityAuth reports whether the auth carries agent identity credentials.
func isAgentIdentityAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	if kind := agentIdentityMetadataString(auth, "auth_kind"); strings.EqualFold(kind, "agent_identity") {
		return true
	}
	if t := agentIdentityMetadataString(auth, "type"); strings.EqualFold(t, "agent_identity") {
		return true
	}
	creds := agentIdentityCredsFromAuth(auth)
	return creds.runtimeID != "" && creds.privateKeyB64 != ""
}

// agentIdentityAccountID returns the ChatGPT account id associated with an agent identity auth.
func agentIdentityAccountID(auth *cliproxyauth.Auth) string {
	return agentIdentityCredsFromAuth(auth).accountID
}

// agentIdentityPrivateKey decodes the base64 PKCS#8 DER private key into an Ed25519 key.
// Metadata often contains whitespace/newlines or omits padding, so decoding accepts both
// standard and raw base64 after stripping whitespace.
func agentIdentityPrivateKey(privateKeyB64 string) (ed25519.PrivateKey, error) {
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\r', '	':
			return -1
		default:
			return r
		}
	}, privateKeyB64)
	der, err := base64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		var errRaw error
		der, errRaw = base64.RawStdEncoding.DecodeString(cleaned)
		if errRaw != nil {
			return nil, fmt.Errorf("agent identity auth: decode private key: %w", err)
		}
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("agent identity auth: parse private key: %w", err)
	}
	privateKey, ok := parsedKey.(ed25519.PrivateKey)
	if !ok || len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("agent identity auth: private key is not ed25519")
	}
	return privateKey, nil
}

// buildAgentAssertion signs "<agent_runtime_id>:<task_id>:<timestamp>" with the Ed25519
// private key and returns the Authorization header value
// "AgentAssertion <base64url(JSON envelope)>".
func buildAgentAssertion(creds agentIdentityCreds, now time.Time) (string, error) {
	if creds.runtimeID == "" || creds.privateKeyB64 == "" || creds.taskID == "" {
		return "", fmt.Errorf("agent identity auth: missing agent_runtime_id, agent_private_key or task_id")
	}
	privateKey, err := agentIdentityPrivateKey(creds.privateKeyB64)
	if err != nil {
		return "", err
	}
	timestamp := now.UTC().Format(time.RFC3339)
	payload := creds.runtimeID + ":" + creds.taskID + ":" + timestamp
	signature := ed25519.Sign(privateKey, []byte(payload))
	envelope, err := json.Marshal(agentAssertion{
		AgentRuntimeID: creds.runtimeID,
		TaskID:         creds.taskID,
		Timestamp:      timestamp,
		Signature:      base64.StdEncoding.EncodeToString(signature),
	})
	if err != nil {
		return "", fmt.Errorf("agent identity auth: marshal assertion: %w", err)
	}
	return agentAssertionScheme + " " + base64.RawURLEncoding.EncodeToString(envelope), nil
}

// generateAgentAssertion builds a fresh Authorization header value for the auth.
func generateAgentAssertion(auth *cliproxyauth.Auth) (string, error) {
	return buildAgentAssertion(agentIdentityCredsFromAuth(auth), time.Now())
}
