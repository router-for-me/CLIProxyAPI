package codex

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRefreshTokensWithRetry_NonRetryableOnlyAttemptsOnce(t *testing.T) {
	var calls int32
	auth := &CodexAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				atomic.AddInt32(&calls, 1)
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_grant","code":"refresh_token_reused"}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			}),
		},
	}

	_, err := auth.RefreshTokensWithRetry(context.Background(), "dummy_refresh_token", 3)
	if err == nil {
		t.Fatalf("expected error for non-retryable refresh failure")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "refresh_token_reused") {
		t.Fatalf("expected refresh_token_reused in error, got: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 refresh attempt, got %d", got)
	}
}

func TestNewCodexAuthWithProxyURL_OverrideDirectDisablesProxy(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "http://proxy.example.com:8080"}}
	auth := NewCodexAuthWithProxyURL(cfg, "direct")

	transport, ok := auth.httpClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("expected http.Transport, got %T", auth.httpClient.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestNewCodexAuthWithProxyURL_OverrideProxyTakesPrecedence(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "http://global.example.com:8080"}}
	auth := NewCodexAuthWithProxyURL(cfg, "http://override.example.com:8081")

	transport, ok := auth.httpClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("expected http.Transport, got %T", auth.httpClient.Transport)
	}
	req, errReq := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errReq != nil {
		t.Fatalf("new request: %v", errReq)
	}
	proxyURL, errProxy := transport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("proxy func: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://override.example.com:8081" {
		t.Fatalf("proxy URL = %v, want http://override.example.com:8081", proxyURL)
	}
}

func TestNewCodexAuthWithProxyURL_CustomCAAppliesWithoutProxy(t *testing.T) {
	t.Setenv("CODEX_CA_CERTIFICATE", mustCreateTestCertificatePEM(t))
	t.Setenv("SSL_CERT_FILE", "")

	auth := NewCodexAuthWithProxyURL(&config.Config{}, "")

	transport, ok := auth.httpClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("expected http.Transport, got %T", auth.httpClient.Transport)
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.RootCAs == nil {
		t.Fatal("expected custom RootCAs to be configured")
	}
	if len(transport.TLSClientConfig.RootCAs.Subjects()) == 0 {
		t.Fatal("expected custom RootCAs to contain certificates")
	}
}

func TestGenerateAuthURLMatchesLatestCodexDefaults(t *testing.T) {
	auth := &CodexAuth{}
	pkceCodes := &PKCECodes{
		CodeVerifier:  "verifier",
		CodeChallenge: "challenge",
	}

	rawURL, err := auth.GenerateAuthURL("state-123", pkceCodes)
	if err != nil {
		t.Fatalf("GenerateAuthURL() error = %v", err)
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	if parsed.Scheme != "https" || parsed.Host != "auth.openai.com" || parsed.Path != "/oauth/authorize" {
		t.Fatalf("auth URL = %s, want https://auth.openai.com/oauth/authorize", parsed.String())
	}

	query := parsed.Query()
	if got := query.Get("client_id"); got != ClientID {
		t.Fatalf("client_id = %q, want %q", got, ClientID)
	}
	if got := query.Get("redirect_uri"); got != RedirectURI {
		t.Fatalf("redirect_uri = %q, want %q", got, RedirectURI)
	}
	if got := query.Get("scope"); got != DefaultAuthScope {
		t.Fatalf("scope = %q, want %q", got, DefaultAuthScope)
	}
	if got := query.Get("originator"); got != misc.CodexCLIOriginator {
		t.Fatalf("originator = %q, want %q", got, misc.CodexCLIOriginator)
	}
	if got := query.Get("prompt"); got != "" {
		t.Fatalf("prompt = %q, want empty", got)
	}
}

func TestGenerateAuthURLWithOptionsIncludesOriginatorAndWorkspace(t *testing.T) {
	auth := &CodexAuth{}
	pkceCodes := &PKCECodes{
		CodeVerifier:  "verifier",
		CodeChallenge: "challenge",
	}

	rawURL, err := auth.GenerateAuthURLWithOptions("state-123", pkceCodes, "codex_vscode", "ws_123")
	if err != nil {
		t.Fatalf("GenerateAuthURLWithOptions() error = %v", err)
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	query := parsed.Query()
	if got := query.Get("originator"); got != "codex_vscode" {
		t.Fatalf("originator = %q, want %q", got, "codex_vscode")
	}
	if got := query.Get("allowed_workspace_id"); got != "ws_123" {
		t.Fatalf("allowed_workspace_id = %q, want %q", got, "ws_123")
	}
}

func mustCreateTestCertificatePEM(t *testing.T) string {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey() error = %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "CLIProxyAPI Test CA",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("x509.CreateCertificate() error = %v", err)
	}

	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: der,
	}))
}
