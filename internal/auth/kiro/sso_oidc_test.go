package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type recordingRoundTripper struct {
	lastReq *http.Request
}

func (rt *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.lastReq = req
	body := `{"nextToken":null,"profiles":[{"arn":"arn:aws:codewhisperer:us-east-1:123456789012:profile/ABC","profileName":"test"}]}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

func TestTryListAvailableProfiles_UsesClientIDForAccountKey(t *testing.T) {
	rt := &recordingRoundTripper{}
	client := &SSOOIDCClient{
		httpClient: &http.Client{Transport: rt},
	}

	profileArn := client.tryListAvailableProfiles(context.Background(), "access-token", "client-id-123", "refresh-token-456")
	if profileArn == "" {
		t.Fatal("expected profileArn, got empty result")
	}

	accountKey := GetAccountKey("client-id-123", "refresh-token-456")
	fp := GlobalFingerprintManager().GetFingerprint(accountKey)
	expected := fmt.Sprintf("aws-sdk-js/%s KiroIDE-%s-%s", fp.RuntimeSDKVersion, fp.KiroVersion, fp.KiroHash)
	got := rt.lastReq.Header.Get("X-Amz-User-Agent")
	if got != expected {
		t.Errorf("X-Amz-User-Agent = %q, want %q", got, expected)
	}
}

func TestTryListAvailableProfiles_UsesRefreshTokenWhenClientIDMissing(t *testing.T) {
	rt := &recordingRoundTripper{}
	client := &SSOOIDCClient{
		httpClient: &http.Client{Transport: rt},
	}

	profileArn := client.tryListAvailableProfiles(context.Background(), "access-token", "", "refresh-token-789")
	if profileArn == "" {
		t.Fatal("expected profileArn, got empty result")
	}

	accountKey := GetAccountKey("", "refresh-token-789")
	fp := GlobalFingerprintManager().GetFingerprint(accountKey)
	expected := fmt.Sprintf("aws-sdk-js/%s KiroIDE-%s-%s", fp.RuntimeSDKVersion, fp.KiroVersion, fp.KiroHash)
	got := rt.lastReq.Header.Get("X-Amz-User-Agent")
	if got != expected {
		t.Errorf("X-Amz-User-Agent = %q, want %q", got, expected)
	}
}

func TestRegisterClientForAuthCodeWithIDC(t *testing.T) {
	var capturedReq struct {
		Method  string
		Path    string
		Headers http.Header
		Body    map[string]interface{}
	}

	mockResp := RegisterClientResponse{
		ClientID:              "test-client-id",
		ClientSecret:          "test-client-secret",
		ClientIDIssuedAt:      1700000000,
		ClientSecretExpiresAt: 1700086400,
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedReq.Method = r.Method
		capturedReq.Path = r.URL.Path
		capturedReq.Headers = r.Header.Clone()

		bodyBytes, _ := io.ReadAll(r.Body)
		json.Unmarshal(bodyBytes, &capturedReq.Body)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockResp)
	}))
	defer ts.Close()

	// Extract host to build a region that resolves to our test server.
	// Override getOIDCEndpoint by passing region="" and patching the endpoint.
	// Since getOIDCEndpoint builds "https://oidc.{region}.amazonaws.com", we
	// instead inject the test server URL directly via a custom HTTP client transport.
	client := &SSOOIDCClient{
		httpClient: ts.Client(),
	}

	// We need to route the request to our test server. Use a transport that rewrites the URL.
	client.httpClient.Transport = &rewriteTransport{
		base:      ts.Client().Transport,
		targetURL: ts.URL,
	}

	resp, err := client.RegisterClientForAuthCodeWithIDC(
		context.Background(),
		"http://127.0.0.1:19877/oauth/callback",
		"https://my-idc-instance.awsapps.com/start",
		"us-east-1",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify request method and path
	if capturedReq.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", capturedReq.Method)
	}
	if capturedReq.Path != "/client/register" {
		t.Errorf("path = %q, want /client/register", capturedReq.Path)
	}

	// Verify headers
	if ct := capturedReq.Headers.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	ua := capturedReq.Headers.Get("User-Agent")
	if !strings.Contains(ua, "KiroIDE") {
		t.Errorf("User-Agent %q does not contain KiroIDE", ua)
	}
	if !strings.Contains(ua, "sso-oidc") {
		t.Errorf("User-Agent %q does not contain sso-oidc", ua)
	}
	xua := capturedReq.Headers.Get("X-Amz-User-Agent")
	if !strings.Contains(xua, "KiroIDE") {
		t.Errorf("x-amz-user-agent %q does not contain KiroIDE", xua)
	}

	// Verify body fields
	if v, _ := capturedReq.Body["clientName"].(string); v != "Kiro IDE" {
		t.Errorf("clientName = %q, want %q", v, "Kiro IDE")
	}
	if v, _ := capturedReq.Body["clientType"].(string); v != "public" {
		t.Errorf("clientType = %q, want %q", v, "public")
	}
	if v, _ := capturedReq.Body["issuerUrl"].(string); v != "https://my-idc-instance.awsapps.com/start" {
		t.Errorf("issuerUrl = %q, want %q", v, "https://my-idc-instance.awsapps.com/start")
	}

	// Verify scopes array
	scopesRaw, ok := capturedReq.Body["scopes"].([]interface{})
	if !ok || len(scopesRaw) != 5 {
		t.Fatalf("scopes: got %v, want 5-element array", capturedReq.Body["scopes"])
	}
	expectedScopes := []string{
		"codewhisperer:completions", "codewhisperer:analysis",
		"codewhisperer:conversations", "codewhisperer:transformations",
		"codewhisperer:taskassist",
	}
	for i, s := range expectedScopes {
		if scopesRaw[i].(string) != s {
			t.Errorf("scopes[%d] = %q, want %q", i, scopesRaw[i], s)
		}
	}

	// Verify grantTypes
	grantTypesRaw, ok := capturedReq.Body["grantTypes"].([]interface{})
	if !ok || len(grantTypesRaw) != 2 {
		t.Fatalf("grantTypes: got %v, want 2-element array", capturedReq.Body["grantTypes"])
	}
	if grantTypesRaw[0].(string) != "authorization_code" || grantTypesRaw[1].(string) != "refresh_token" {
		t.Errorf("grantTypes = %v, want [authorization_code, refresh_token]", grantTypesRaw)
	}

	// Verify redirectUris
	redirectRaw, ok := capturedReq.Body["redirectUris"].([]interface{})
	if !ok || len(redirectRaw) != 1 {
		t.Fatalf("redirectUris: got %v, want 1-element array", capturedReq.Body["redirectUris"])
	}
	if redirectRaw[0].(string) != "http://127.0.0.1:19877/oauth/callback" {
		t.Errorf("redirectUris[0] = %q, want %q", redirectRaw[0], "http://127.0.0.1:19877/oauth/callback")
	}

	// Verify response parsing
	if resp.ClientID != "test-client-id" {
		t.Errorf("ClientID = %q, want %q", resp.ClientID, "test-client-id")
	}
	if resp.ClientSecret != "test-client-secret" {
		t.Errorf("ClientSecret = %q, want %q", resp.ClientSecret, "test-client-secret")
	}
	if resp.ClientIDIssuedAt != 1700000000 {
		t.Errorf("ClientIDIssuedAt = %d, want %d", resp.ClientIDIssuedAt, 1700000000)
	}
	if resp.ClientSecretExpiresAt != 1700086400 {
		t.Errorf("ClientSecretExpiresAt = %d, want %d", resp.ClientSecretExpiresAt, 1700086400)
	}
}

// rewriteTransport redirects all requests to the test server URL.
type rewriteTransport struct {
	base      http.RoundTripper
	targetURL string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, _ := url.Parse(t.targetURL)
	req.URL.Scheme = target.Scheme
	req.URL.Host = target.Host
	if t.base != nil {
		return t.base.RoundTrip(req)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func TestBuildAuthorizationURL(t *testing.T) {
	scopes := "codewhisperer:completions,codewhisperer:analysis,codewhisperer:conversations,codewhisperer:transformations,codewhisperer:taskassist"
	endpoint := "https://oidc.us-east-1.amazonaws.com"
	redirectURI := "http://127.0.0.1:19877/oauth/callback"

	authURL := buildAuthorizationURL(endpoint, "test-client-id", redirectURI, scopes, "random-state", "test-challenge")

	// Verify colons and commas in scopes are percent-encoded
	if !strings.Contains(authURL, "codewhisperer%3Acompletions") {
		t.Errorf("expected colons in scopes to be percent-encoded, got: %s", authURL)
	}
	if !strings.Contains(authURL, "completions%2Ccodewhisperer") {
		t.Errorf("expected commas in scopes to be percent-encoded, got: %s", authURL)
	}

	// Parse back and verify all parameters round-trip correctly
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("failed to parse auth URL: %v", err)
	}

	if !strings.HasPrefix(authURL, endpoint+"/authorize?") {
		t.Errorf("expected URL to start with %s/authorize?, got: %s", endpoint, authURL)
	}

	q := parsed.Query()
	checks := map[string]string{
		"response_type":         "code",
		"client_id":             "test-client-id",
		"redirect_uri":          redirectURI,
		"scopes":                scopes,
		"state":                 "random-state",
		"code_challenge":        "test-challenge",
		"code_challenge_method": "S256",
	}
	for key, want := range checks {
		if got := q.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}
