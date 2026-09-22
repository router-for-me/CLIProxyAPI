package kimi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestKimiDomainResolution(t *testing.T) {
	tests := []struct {
		input      string
		isAI       bool
		normalized string
		oauthHost  string
		apiBaseURL string
	}{
		{
			input:      "kimi.com",
			isAI:       false,
			normalized: KimiDefaultDomain,
			oauthHost:  KimiOAuthHost,
			apiBaseURL: KimiAPIBaseURL,
		},
		{
			input:      "kimi",
			isAI:       false,
			normalized: KimiDefaultDomain,
			oauthHost:  KimiOAuthHost,
			apiBaseURL: KimiAPIBaseURL,
		},
		{
			input:      "kimi.ai",
			isAI:       true,
			normalized: KimiAIDomain,
			oauthHost:  KimiAIOAuthHost,
			apiBaseURL: KimiAIAPIBaseURL,
		},
		{
			input:      "kimi-ai",
			isAI:       true,
			normalized: KimiAIDomain,
			oauthHost:  KimiAIOAuthHost,
			apiBaseURL: KimiAIAPIBaseURL,
		},
		{
			input:      "ai",
			isAI:       true,
			normalized: KimiAIDomain,
			oauthHost:  KimiAIOAuthHost,
			apiBaseURL: KimiAIAPIBaseURL,
		},
		{
			input:      "auth.kimi.ai",
			isAI:       true,
			normalized: KimiAIDomain,
			oauthHost:  KimiAIOAuthHost,
			apiBaseURL: KimiAIAPIBaseURL,
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := IsKimiAIDomain(tt.input); got != tt.isAI {
				t.Fatalf("IsKimiAIDomain(%q) = %v, want %v", tt.input, got, tt.isAI)
			}
			if got := NormalizeKimiDomain(tt.input); got != tt.normalized {
				t.Fatalf("NormalizeKimiDomain(%q) = %q, want %q", tt.input, got, tt.normalized)
			}
			if got := ResolveKimiOAuthHost(tt.input); got != tt.oauthHost {
				t.Fatalf("ResolveKimiOAuthHost(%q) = %q, want %q", tt.input, got, tt.oauthHost)
			}
			if got := ResolveKimiAPIBaseURL(tt.input); got != tt.apiBaseURL {
				t.Fatalf("ResolveKimiAPIBaseURL(%q) = %q, want %q", tt.input, got, tt.apiBaseURL)
			}
		})
	}
}

func TestResolveKimiDomainFromAuth(t *testing.T) {
	tests := []struct {
		name string
		auth *cliproxyauth.Auth
		want string
	}{
		{
			name: "nil auth",
			auth: nil,
			want: KimiDefaultDomain,
		},
		{
			name: "default kimi provider",
			auth: &cliproxyauth.Auth{Provider: "kimi"},
			want: KimiDefaultDomain,
		},
		{
			name: "kimi-ai provider",
			auth: &cliproxyauth.Auth{Provider: "kimi-ai"},
			want: KimiAIDomain,
		},
		{
			name: "kimi.ai provider",
			auth: &cliproxyauth.Auth{Provider: "kimi.ai"},
			want: KimiAIDomain,
		},
		{
			name: "attributes domain kimi.ai",
			auth: &cliproxyauth.Auth{
				Provider:   "kimi",
				Attributes: map[string]string{"domain": "kimi.ai"},
			},
			want: KimiAIDomain,
		},
		{
			name: "attributes base_url api.kimi.ai",
			auth: &cliproxyauth.Auth{
				Provider:   "kimi",
				Attributes: map[string]string{"base_url": "https://api.kimi.ai/coding"},
			},
			want: KimiAIDomain,
		},
		{
			name: "metadata domain kimi.ai",
			auth: &cliproxyauth.Auth{
				Provider: "kimi",
				Metadata: map[string]any{"domain": "kimi.ai"},
			},
			want: KimiAIDomain,
		},
		{
			name: "metadata type kimi-ai",
			auth: &cliproxyauth.Auth{
				Provider: "kimi",
				Metadata: map[string]any{"type": "kimi-ai"},
			},
			want: KimiAIDomain,
		},
		{
			name: "storage with domain kimi.ai",
			auth: &cliproxyauth.Auth{
				Provider: "kimi",
				Storage:  &KimiTokenStorage{Domain: "kimi.ai"},
			},
			want: KimiAIDomain,
		},
		{
			name: "explicit com domain in attributes overrides kimi-ai in filename",
			auth: &cliproxyauth.Auth{
				ID:         "kimi-ai-backup.json",
				FileName:   "kimi-ai-backup.json",
				Attributes: map[string]string{"domain": "kimi.com"},
			},
			want: KimiDefaultDomain,
		},
		{
			name: "explicit com base_url in attributes overrides kimi-ai in filename",
			auth: &cliproxyauth.Auth{
				ID:         "kimi-ai-backup.json",
				FileName:   "kimi-ai-backup.json",
				Attributes: map[string]string{"base_url": "https://api.kimi.com/coding"},
			},
			want: KimiDefaultDomain,
		},
		{
			name: "explicit com in storage domain overrides kimi-ai in filename",
			auth: &cliproxyauth.Auth{
				ID:       "kimi-ai-backup.json",
				FileName: "kimi-ai-backup.json",
				Storage:  &KimiTokenStorage{Domain: "kimi.com"},
			},
			want: KimiDefaultDomain,
		},
		{
			name: "unrelated hostname does not trigger kimi.ai",
			auth: &cliproxyauth.Auth{
				Attributes: map[string]string{"base_url": "https://example.com/kimi.ai"},
			},
			want: KimiDefaultDomain,
		},
		{
			name: "file name containing kimi-ai",
			auth: &cliproxyauth.Auth{
				ID:       "kimi-ai-12345.json",
				FileName: "kimi-ai-12345.json",
			},
			want: KimiAIDomain,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveKimiDomainFromAuth(tt.auth)
			if got != tt.want {
				t.Fatalf("ResolveKimiDomainFromAuth() = %q, want %q", got, tt.want)
			}
			isAI := IsKimiAIAuth(tt.auth)
			wantAI := tt.want == KimiAIDomain
			if isAI != wantAI {
				t.Fatalf("IsKimiAIAuth() = %v, want %v", isAI, wantAI)
			}
		})
	}
}

func TestKimiAuthCreationAndEndpoints(t *testing.T) {
	authCom := NewKimiAuth(nil)
	if authCom.domain != KimiDefaultDomain {
		t.Fatalf("authCom.domain = %q, want %q", authCom.domain, KimiDefaultDomain)
	}
	if authCom.deviceClient.deviceCodeURL() != "https://auth.kimi.com/api/oauth/device_authorization" {
		t.Fatalf("deviceCodeURL = %q, want %q", authCom.deviceClient.deviceCodeURL(), "https://auth.kimi.com/api/oauth/device_authorization")
	}
	if authCom.deviceClient.tokenURL() != "https://auth.kimi.com/api/oauth/token" {
		t.Fatalf("tokenURL = %q, want %q", authCom.deviceClient.tokenURL(), "https://auth.kimi.com/api/oauth/token")
	}

	authAI := NewKimiAIAuth(nil)
	if authAI.domain != KimiAIDomain {
		t.Fatalf("authAI.domain = %q, want %q", authAI.domain, KimiAIDomain)
	}
	if authAI.deviceClient.deviceCodeURL() != "https://auth.kimi.ai/api/oauth/device_authorization" {
		t.Fatalf("deviceCodeURL = %q, want %q", authAI.deviceClient.deviceCodeURL(), "https://auth.kimi.ai/api/oauth/device_authorization")
	}
	if authAI.deviceClient.tokenURL() != "https://auth.kimi.ai/api/oauth/token" {
		t.Fatalf("tokenURL = %q, want %q", authAI.deviceClient.tokenURL(), "https://auth.kimi.ai/api/oauth/token")
	}
}

func TestKimiCreateTokenStorageAndSave(t *testing.T) {
	bundle := &KimiAuthBundle{
		TokenData: &KimiTokenData{
			AccessToken:  "test-access-token",
			RefreshToken: "test-refresh-token",
			TokenType:    "Bearer",
			ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
			Scope:        "user",
		},
		DeviceID: "device-test-123",
	}

	authAI := NewKimiAIAuth(nil)
	storageAI := authAI.CreateTokenStorage(bundle)
	if storageAI.Type != "kimi-ai" {
		t.Fatalf("storageAI.Type = %q, want kimi-ai", storageAI.Type)
	}
	if storageAI.Domain != KimiAIDomain {
		t.Fatalf("storageAI.Domain = %q, want %q", storageAI.Domain, KimiAIDomain)
	}
	if storageAI.BaseURL != KimiAIAPIBaseURL {
		t.Fatalf("storageAI.BaseURL = %q, want %q", storageAI.BaseURL, KimiAIAPIBaseURL)
	}

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "kimi-ai-token.json")
	if errSave := storageAI.SaveTokenToFile(filePath); errSave != nil {
		t.Fatalf("SaveTokenToFile() error = %v", errSave)
	}

	data, errRead := os.ReadFile(filePath)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	var parsed map[string]any
	if errUnmarshal := json.Unmarshal(data, &parsed); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if parsed["type"] != "kimi-ai" {
		t.Fatalf("saved type = %v, want kimi-ai", parsed["type"])
	}
	if parsed["domain"] != KimiAIDomain {
		t.Fatalf("saved domain = %v, want %q", parsed["domain"], KimiAIDomain)
	}
	if parsed["base_url"] != KimiAIAPIBaseURL {
		t.Fatalf("saved base_url = %v, want %q", parsed["base_url"], KimiAIAPIBaseURL)
	}
}

func TestRefreshToken_KimiAIEndpoint(t *testing.T) {
	resetKimiRefreshGroupForTest()
	t.Cleanup(resetKimiRefreshGroupForTest)

	var targetURL string
	transport := kimiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		targetURL = req.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`{
				"access_token":"new-ai-access",
				"refresh_token":"new-ai-refresh",
				"token_type":"Bearer",
				"expires_in":3600
			}`)),
			Header:  make(http.Header),
			Request: req,
		}, nil
	})

	clientAI := NewDeviceFlowClientWithDomain(nil, KimiAIDomain)
	clientAI.httpClient = &http.Client{Transport: transport}

	td, errRefresh := clientAI.RefreshToken(context.Background(), "ai-refresh-token")
	if errRefresh != nil {
		t.Fatalf("RefreshToken() error = %v", errRefresh)
	}
	if td == nil || td.AccessToken != "new-ai-access" {
		t.Fatalf("unexpected token data: %#v", td)
	}
	if targetURL != "https://auth.kimi.ai/api/oauth/token" {
		t.Fatalf("RefreshToken target URL = %q, want %q", targetURL, "https://auth.kimi.ai/api/oauth/token")
	}
}

func TestRefreshToken_CrossDomainIsolation(t *testing.T) {
	resetKimiRefreshGroupForTest()
	t.Cleanup(resetKimiRefreshGroupForTest)

	var callsCom int32
	var callsAI int32

	transportCom := kimiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&callsCom, 1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`{
				"access_token":"token-com",
				"refresh_token":"refresh-com",
				"token_type":"Bearer",
				"expires_in":3600
			}`)),
			Header:  make(http.Header),
			Request: req,
		}, nil
	})
	transportAI := kimiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&callsAI, 1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`{
				"access_token":"token-ai",
				"refresh_token":"refresh-ai",
				"token_type":"Bearer",
				"expires_in":3600
			}`)),
			Header:  make(http.Header),
			Request: req,
		}, nil
	})

	clientCom := NewDeviceFlowClientWithDomain(nil, KimiDefaultDomain)
	clientCom.httpClient = &http.Client{Transport: transportCom}

	clientAI := NewDeviceFlowClientWithDomain(nil, KimiAIDomain)
	clientAI.httpClient = &http.Client{Transport: transportAI}

	// Same refresh token used across both domains concurrently
	const sharedToken = "shared-test-token"
	resCom, errCom := clientCom.RefreshToken(context.Background(), sharedToken)
	if errCom != nil {
		t.Fatalf("clientCom.RefreshToken error = %v", errCom)
	}
	resAI, errAI := clientAI.RefreshToken(context.Background(), sharedToken)
	if errAI != nil {
		t.Fatalf("clientAI.RefreshToken error = %v", errAI)
	}

	if resCom.AccessToken != "token-com" {
		t.Fatalf("resCom.AccessToken = %q, want token-com", resCom.AccessToken)
	}
	if resAI.AccessToken != "token-ai" {
		t.Fatalf("resAI.AccessToken = %q, want token-ai", resAI.AccessToken)
	}
	if callsCom != 1 || callsAI != 1 {
		t.Fatalf("expected 1 call each, got com=%d, ai=%d", callsCom, callsAI)
	}
}
