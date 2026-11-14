package kiro

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegisterClientWithRegion(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/client/register" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if payload["clientName"] != "Kiro IDE" {
			t.Errorf("clientName = %v, want Kiro IDE", payload["clientName"])
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"clientId": "test-client-id",
			"clientSecret": "test-client-secret",
			"clientIdIssuedAt": 1700000000,
			"clientSecretExpiresAt": 1800000000
		}`))
	}))
	defer mockServer.Close()

	client := &SSOOIDCClient{
		httpClient: mockServer.Client(),
	}

	endpoint := mockServer.URL
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint+"/client/register", strings.NewReader(`{"clientName":"Kiro IDE"}`))
	SetOIDCHeaders(req)
	if req.Header.Get("User-Agent") == "" {
		t.Errorf("User-Agent not set by SetOIDCHeaders")
	}

	res, err := client.httpClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer res.Body.Close()

	var regResp RegisterClientResponse
	if err := json.NewDecoder(res.Body).Decode(&regResp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if regResp.ClientID != "test-client-id" {
		t.Errorf("ClientID = %q, want test-client-id", regResp.ClientID)
	}
}

func TestStartDeviceAuthAndCreateToken(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/device_authorization":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"deviceCode": "d-code-123",
				"userCode": "ABCD-1234",
				"verificationUri": "https://auth.example.com",
				"verificationUriComplete": "https://auth.example.com?code=ABCD-1234",
				"expiresIn": 900,
				"interval": 1
			}`))
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{
				"accessToken": "access-token-xyz",
				"tokenType": "Bearer",
				"expiresIn": 3600,
				"refreshToken": "refresh-token-xyz"
			}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	client := &SSOOIDCClient{
		httpClient: mockServer.Client(),
	}

	tokenResp, err := client.httpClient.Post(mockServer.URL+"/token", "application/json", nil)
	if err != nil {
		t.Fatalf("token request failed: %v", err)
	}
	defer tokenResp.Body.Close()

	var token CreateTokenResponse
	if err := json.NewDecoder(tokenResp.Body).Decode(&token); err != nil {
		t.Fatalf("decode token failed: %v", err)
	}

	if token.AccessToken != "access-token-xyz" {
		t.Errorf("AccessToken = %q, want access-token-xyz", token.AccessToken)
	}
}
