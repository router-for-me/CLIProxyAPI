package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
)

func TestNewCopilotAuth(t *testing.T) {
	cfg := &config.Config{}
	auth := NewCopilotAuth(cfg, nil)
	if auth.httpClient == nil {
		t.Error("expected default httpClient to be set")
	}
}

func TestCopilotAuth_ValidateToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "goodtoken") {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"Bad credentials"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"login":"testuser"}`)
	}))
	defer server.Close()

	cfg := &config.Config{}
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			mockReq, _ := http.NewRequest(req.Method, server.URL, req.Body)
			mockReq.Header = req.Header
			return http.DefaultClient.Do(mockReq)
		}),
	}
	auth := NewCopilotAuth(cfg, client)
	// Crucially, we need to ensure deviceClient uses our mocked client
	auth.deviceClient.httpClient = client

	ok, username, err := auth.ValidateToken(context.Background(), "goodtoken")
	if err != nil || !ok || username != "testuser" {
		t.Errorf("ValidateToken failed: ok=%v, username=%s, err=%v", ok, username, err)
	}

	ok, _, _ = auth.ValidateToken(context.Background(), "badtoken")
	if ok {
		t.Error("expected invalid token to fail validation")
	}
}

func TestCopilotAuth_CreateTokenStorage(t *testing.T) {
	auth := &CopilotAuth{}
	bundle := &CopilotAuthBundle{
		TokenData: &CopilotTokenData{
			AccessToken: "access",
			TokenType:   "Bearer",
			Scope:       "user",
		},
		Username: "user123",
	}
	storage := auth.CreateTokenStorage(bundle)
	if storage.AccessToken != "access" || storage.Username != "user123" {
		t.Errorf("CreateTokenStorage failed: %+v", storage)
	}
}

func TestCopilotAuth_MakeAuthenticatedRequest(t *testing.T) {
	auth := &CopilotAuth{}
	apiToken := &CopilotAPIToken{Token: "api-token"}
	req, err := auth.MakeAuthenticatedRequest(context.Background(), "GET", "http://api.com", nil, apiToken)
	if err != nil {
		t.Fatalf("MakeAuthenticatedRequest failed: %v", err)
	}
	if req.Header.Get("Authorization") != "Bearer api-token" {
		t.Errorf("wrong auth header: %s", req.Header.Get("Authorization"))
	}
}

func TestDeviceFlowClient_RequestDeviceCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := DeviceCodeResponse{
			DeviceCode:      "device",
			UserCode:        "user",
			VerificationURI: "uri",
			ExpiresIn:       900,
			Interval:        5,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &DeviceFlowClient{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				mockReq, _ := http.NewRequest(req.Method, server.URL, req.Body)
				return http.DefaultClient.Do(mockReq)
			}),
		},
	}

	resp, err := client.RequestDeviceCode(context.Background())
	if err != nil {
		t.Fatalf("RequestDeviceCode failed: %v", err)
	}
	if resp.DeviceCode != "device" {
		t.Errorf("expected device code, got %s", resp.DeviceCode)
	}
}

func TestDeviceFlowClient_PollForToken(t *testing.T) {
	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		w.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			fmt.Fprint(w, `{"error":"authorization_pending"}`)
			return
		}
		fmt.Fprint(w, `{"access_token":"token123"}`)
	}))
	defer server.Close()

	client := &DeviceFlowClient{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				mockReq, _ := http.NewRequest(req.Method, server.URL, req.Body)
				return http.DefaultClient.Do(mockReq)
			}),
		},
	}

	deviceCode := &DeviceCodeResponse{
		DeviceCode: "device",
		Interval:   1,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	token, err := client.PollForToken(ctx, deviceCode)
	if err != nil {
		t.Fatalf("PollForToken failed: %v", err)
	}
	if token.AccessToken != "token123" {
		t.Errorf("expected token123, got %s", token.AccessToken)
	}
}

func TestCopilotAuth_LoadAndValidateToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.Header.Get("Authorization"), "expired") {
			fmt.Fprint(w, `{"token":"new","expires_at":1}`) // expired
			return
		}
		fmt.Fprint(w, `{"token":"new","expires_at":0}`) // never expires
	}))
	defer server.Close()

	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			mockReq, _ := http.NewRequest(req.Method, server.URL, req.Body)
			mockReq.Header = req.Header
			return http.DefaultClient.Do(mockReq)
		}),
	}
	auth := NewCopilotAuth(&config.Config{}, client)

	// Valid case
	ok, err := auth.LoadAndValidateToken(context.Background(), &CopilotTokenStorage{AccessToken: "valid"})
	if !ok || err != nil {
		t.Errorf("LoadAndValidateToken failed: ok=%v, err=%v", ok, err)
	}

	// Expired case
	ok, err = auth.LoadAndValidateToken(context.Background(), &CopilotTokenStorage{AccessToken: "expired"})
	if ok || err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("expected expired error, got ok=%v, err=%v", ok, err)
	}

	// No token case
	ok, err = auth.LoadAndValidateToken(context.Background(), nil)
	if ok || err == nil {
		t.Error("expected error for nil storage")
	}
}

func TestCopilotAuth_GetAPIEndpoint(t *testing.T) {
	auth := &CopilotAuth{}
	if auth.GetAPIEndpoint() != "https://api.api.githubcopilot.com" && auth.GetAPIEndpoint() != "https://api.githubcopilot.com" {
		t.Errorf("unexpected endpoint: %s", auth.GetAPIEndpoint())
	}
}

func TestCopilotAuth_StartDeviceFlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(DeviceCodeResponse{DeviceCode: "dc"})
	}))
	defer server.Close()

	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			mockReq, _ := http.NewRequest(req.Method, server.URL, req.Body)
			return http.DefaultClient.Do(mockReq)
		}),
	}
	auth := NewCopilotAuth(&config.Config{}, client)
	auth.deviceClient.httpClient = client

	resp, err := auth.StartDeviceFlow(context.Background())
	if err != nil || resp.DeviceCode != "dc" {
		t.Errorf("StartDeviceFlow failed: %v", err)
	}
}

func TestCopilotAuth_WaitForAuthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/user" {
			fmt.Fprint(w, `{"login":"testuser"}`)
			return
		}
		fmt.Fprint(w, `{"access_token":"token123"}`)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			mockReq, _ := http.NewRequest(req.Method, server.URL, req.Body)
			return http.DefaultClient.Do(mockReq)
		}),
	}
	// We need to override the hardcoded URLs in DeviceFlowClient for this test to work without rewriteTransport
	// but DeviceFlowClient uses constants. So we MUST use rewriteTransport logic or similar.
	
	mockTransport := &rewriteTransportOverride{
		target: server.URL,
	}
	client.Transport = mockTransport

	auth := NewCopilotAuth(&config.Config{}, client)
	auth.deviceClient.httpClient = client

	bundle, err := auth.WaitForAuthorization(context.Background(), &DeviceCodeResponse{DeviceCode: "dc", Interval: 1})
	if err != nil || bundle.Username != "testuser" {
		t.Errorf("WaitForAuthorization failed: %v", err)
	}
}

type rewriteTransportOverride struct {
	target string
}

func (t *rewriteTransportOverride) RoundTrip(req *http.Request) (*http.Response, error) {
	newReq := req.Clone(req.Context())
	newReq.URL.Scheme = "http"
	newReq.URL.Host = strings.TrimPrefix(t.target, "http://")
	return http.DefaultClient.Do(newReq)
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
