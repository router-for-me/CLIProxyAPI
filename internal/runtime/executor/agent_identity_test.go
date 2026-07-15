package executor

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func agentIdentityTestAuth(t *testing.T, keyName string) (*cliproxyauth.Auth, ed25519.PublicKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	metadata := map[string]any{
		"type":             "agent_identity",
		"agent_runtime_id": "agent-test",
		"task_id":          "task-test",
		"account_id":       "acct-test",
		keyName:            base64.StdEncoding.EncodeToString(der),
	}
	return &cliproxyauth.Auth{Provider: "codex", Metadata: metadata}, publicKey
}

func parseAgentAssertionForTest(t *testing.T, header string) agentAssertion {
	t.Helper()
	if !strings.HasPrefix(header, agentAssertionScheme+" ") {
		t.Fatalf("Authorization = %q, want %s scheme", header, agentAssertionScheme)
	}
	encoded := strings.TrimPrefix(header, agentAssertionScheme+" ")
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode assertion envelope: %v", err)
	}
	var assertion agentAssertion
	if err = json.Unmarshal(raw, &assertion); err != nil {
		t.Fatalf("unmarshal assertion envelope: %v", err)
	}
	return assertion
}

func TestBuildAgentAssertionSignsCanonicalPayload(t *testing.T) {
	auth, publicKey := agentIdentityTestAuth(t, "agent_private_key")
	now := time.Date(2026, time.July, 15, 12, 34, 56, 0, time.UTC)

	header, err := buildAgentAssertion(agentIdentityCredsFromAuth(auth), now)
	if err != nil {
		t.Fatalf("buildAgentAssertion() error = %v", err)
	}
	assertion := parseAgentAssertionForTest(t, header)
	if assertion.AgentRuntimeID != "agent-test" || assertion.TaskID != "task-test" {
		t.Fatalf("assertion identity = %#v", assertion)
	}
	if assertion.Timestamp != "2026-07-15T12:34:56Z" {
		t.Fatalf("timestamp = %q", assertion.Timestamp)
	}
	signature, err := base64.StdEncoding.DecodeString(assertion.Signature)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	payload := assertion.AgentRuntimeID + ":" + assertion.TaskID + ":" + assertion.Timestamp
	if !ed25519.Verify(publicKey, []byte(payload), signature) {
		t.Fatal("signature does not verify canonical payload")
	}
}

func TestAgentIdentityAcceptsLegacyPrivateKeyAliases(t *testing.T) {
	for _, keyName := range []string{"private_key_pkcs8_base64", "private_key"} {
		t.Run(keyName, func(t *testing.T) {
			auth, _ := agentIdentityTestAuth(t, keyName)
			if _, err := buildAgentAssertion(agentIdentityCredsFromAuth(auth), time.Unix(0, 0)); err != nil {
				t.Fatalf("legacy key %s rejected: %v", keyName, err)
			}
		})
	}
}

func TestCodexPrepareRequestUsesAgentAssertion(t *testing.T) {
	auth, _ := agentIdentityTestAuth(t, "agent_private_key")
	req, err := http.NewRequest(http.MethodPost, "https://example.test/responses", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	if err = NewCodexExecutor(nil).PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest() error = %v", err)
	}
	parseAgentAssertionForTest(t, req.Header.Get("Authorization"))
	if got := req.Header.Get("Chatgpt-Account-Id"); got != "acct-test" {
		t.Fatalf("Chatgpt-Account-Id = %q, want acct-test", got)
	}
}

func TestApplyCodexHeadersFromSourcesUsesAgentAssertion(t *testing.T) {
	auth, _ := agentIdentityTestAuth(t, "agent_private_key")
	req, err := http.NewRequest(http.MethodPost, "https://example.test/responses", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	if err := applyCodexHeadersFromSources(req, auth, "ignored-bearer", true, nil, nil); err != nil {
		t.Fatalf("applyCodexHeadersFromSources() error = %v", err)
	}
	parseAgentAssertionForTest(t, req.Header.Get("Authorization"))
	if got := req.Header.Get("Chatgpt-Account-Id"); got != "acct-test" {
		t.Fatalf("Chatgpt-Account-Id = %q, want acct-test", got)
	}
}

func TestAgentIdentityPrivateKeyAcceptsWhitespaceAndRawBase64(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	std := base64.StdEncoding.EncodeToString(der)
	raw := base64.RawStdEncoding.EncodeToString(der)
	wrapped := std[:20] + "\n " + std[20:]

	for _, keyB64 := range []string{wrapped, raw} {
		got, err := agentIdentityPrivateKey(keyB64)
		if err != nil {
			t.Fatalf("agentIdentityPrivateKey(%q) error = %v", keyB64[:24], err)
		}
		if len(got) != ed25519.PrivateKeySize {
			t.Fatalf("private key size = %d", len(got))
		}
	}
}

func TestApplyCodexHeadersFromSourcesPrefersNonEmptyAccountID(t *testing.T) {
	auth, _ := agentIdentityTestAuth(t, "agent_private_key")
	// Empty account_id must not clobber chatgpt_account_id fallback.
	auth.Metadata["account_id"] = "   "
	auth.Metadata["chatgpt_account_id"] = "acct-fallback"
	req, err := http.NewRequest(http.MethodPost, "https://example.test/responses", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	if err := applyCodexHeadersFromSources(req, auth, "ignored-bearer", true, nil, nil); err != nil {
		t.Fatalf("applyCodexHeadersFromSources() error = %v", err)
	}
	if got := req.Header.Get("Chatgpt-Account-Id"); got != "acct-fallback" {
		t.Fatalf("Chatgpt-Account-Id = %q, want acct-fallback", got)
	}
}

func TestApplyCodexHeadersFromSourcesOAuthKeepsBearer(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"type": "codex", "access_token": "tok", "account_id": "acct-oauth"},
	}
	req, err := http.NewRequest(http.MethodPost, "https://example.test/responses", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	if err := applyCodexHeadersFromSources(req, auth, "tok", true, nil, nil); err != nil {
		t.Fatalf("applyCodexHeadersFromSources() error = %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer tok" {
		t.Fatalf("Authorization = %q, want Bearer tok", got)
	}
	if got := req.Header.Get("Chatgpt-Account-Id"); got != "acct-oauth" {
		t.Fatalf("Chatgpt-Account-Id = %q, want acct-oauth", got)
	}
}

func TestCodexPrepareRequestOAuthKeepsBearer(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"type": "codex", "access_token": "tok"},
	}
	req, err := http.NewRequest(http.MethodPost, "https://example.test/responses", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	if err = NewCodexExecutor(nil).PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest() error = %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer tok" {
		t.Fatalf("Authorization = %q, want Bearer tok", got)
	}
}

func TestApplyCodexWebsocketHeadersFreshAssertionPerDial(t *testing.T) {
	auth, publicKey := agentIdentityTestAuth(t, "agent_private_key")

	first, err := applyCodexWebsocketHeaders(context.Background(), nil, auth, "", nil)
	if err != nil {
		t.Fatalf("applyCodexWebsocketHeaders() error = %v", err)
	}
	firstAssertion := parseAgentAssertionForTest(t, first.Get("Authorization"))
	if firstAssertion.TaskID != "task-test" {
		t.Fatalf("first dial task_id = %q, want task-test", firstAssertion.TaskID)
	}
	if got := headerValueCaseInsensitive(first, "ChatGPT-Account-ID"); got != "acct-test" {
		t.Fatalf("ChatGPT-Account-ID = %q, want acct-test", got)
	}

	// A later dial must sign the current metadata, not reuse a cached assertion.
	auth.Metadata["task_id"] = "task-rotated"
	second, err := applyCodexWebsocketHeaders(context.Background(), nil, auth, "", nil)
	if err != nil {
		t.Fatalf("second applyCodexWebsocketHeaders() error = %v", err)
	}
	secondAssertion := parseAgentAssertionForTest(t, second.Get("Authorization"))
	if secondAssertion.TaskID != "task-rotated" {
		t.Fatalf("second dial task_id = %q, want task-rotated", secondAssertion.TaskID)
	}
	signature, err := base64.StdEncoding.DecodeString(secondAssertion.Signature)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	payload := secondAssertion.AgentRuntimeID + ":" + secondAssertion.TaskID + ":" + secondAssertion.Timestamp
	if !ed25519.Verify(publicKey, []byte(payload), signature) {
		t.Fatal("second dial signature does not verify")
	}
}

func TestApplyCodexWebsocketHeadersOAuthKeepsBearer(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"type": "codex", "access_token": "tok", "account_id": "acct-oauth"},
	}
	headers, err := applyCodexWebsocketHeaders(context.Background(), nil, auth, "tok", nil)
	if err != nil {
		t.Fatalf("applyCodexWebsocketHeaders() error = %v", err)
	}
	if got := headers.Get("Authorization"); got != "Bearer tok" {
		t.Fatalf("Authorization = %q, want Bearer tok", got)
	}
	if got := headerValueCaseInsensitive(headers, "ChatGPT-Account-ID"); got != "acct-oauth" {
		t.Fatalf("ChatGPT-Account-ID = %q, want acct-oauth", got)
	}
}

func TestApplyCodexHeadersFromSourcesReturnsAssertionError(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{
			"type":             "agent_identity",
			"agent_runtime_id": "agent-test",
			// missing task_id and private key
		},
	}
	req, err := http.NewRequest(http.MethodPost, "https://example.test/responses", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if err := applyCodexHeadersFromSources(req, auth, "", true, nil, nil); err == nil {
		t.Fatal("expected assertion generation error")
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want empty on failure", got)
	}
}

func TestApplyCodexWebsocketHeadersReturnsAssertionError(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{
			"type":              "agent_identity",
			"agent_runtime_id":  "agent-test",
			"agent_private_key": "not-valid-base64!!!",
			"task_id":           "task-test",
		},
	}
	headers, err := applyCodexWebsocketHeaders(context.Background(), nil, auth, "", nil)
	if err == nil {
		t.Fatal("expected assertion generation error")
	}
	if headers != nil {
		t.Fatalf("headers = %#v, want nil on failure", headers)
	}
}
