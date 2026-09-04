package meta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestMetaAuth_StartDeviceFlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		_ = r.ParseForm()
		if r.Form.Get("client_id") != ClientID {
			t.Errorf("expected client_id %s, got %s", ClientID, r.Form.Get("client_id"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(DeviceCodeResponse{
			DeviceCode:              "test-device-code",
			UserCode:                "TEST-1234",
			VerificationURI:         "https://auth.meta.com/device",
			VerificationURIComplete: "https://auth.meta.com/device?user_code=TEST-1234",
			ExpiresIn:               900,
			Interval:                5,
		})
	}))
	defer server.Close()

	auth := NewMetaAuth(&config.Config{})
	auth.httpClient = server.Client()

	dcr, err := auth.StartDeviceFlowWithEndpoint(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dcr.DeviceCode != "test-device-code" || dcr.UserCode != "TEST-1234" {
		t.Fatalf("unexpected response: %+v", dcr)
	}
}

func TestMetaAuth_WaitForAuthorization(t *testing.T) {
	t.Run("successful authorization and minting", func(t *testing.T) {
		attempts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/key" {
				_ = json.NewEncoder(w).Encode(MintedKeyResponse{
					APIKey:       "LLM|test-minted-key",
					UserEmail:    "user@meta.com",
					UserFullName: "Meta User",
				})
				return
			}
			attempts++
			if attempts < 2 {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": "authorization_pending",
				})
				return
			}
			_ = json.NewEncoder(w).Encode(TokenData{
				AccessToken: "test-access-token-123",
				TokenType:   "Bearer",
				ExpiresIn:   3600,
			})
		}))
		defer server.Close()

		auth := NewMetaAuth(&config.Config{})
		auth.httpClient = server.Client()
		auth.SetMintURL(server.URL + "/key")

		dcr := &DeviceCodeResponse{
			DeviceCode:    "test-device",
			TokenEndpoint: server.URL + "/token",
			Interval:      1,
			ExpiresIn:     10,
		}

		bundle, err := auth.WaitForAuthorization(context.Background(), dcr)
		if err != nil {
			t.Fatalf("WaitForAuthorization error: %v", err)
		}
		if bundle.TokenData == nil || bundle.TokenData.AccessToken != "test-access-token-123" {
			t.Fatalf("expected token test-access-token-123, got %+v", bundle.TokenData)
		}
		if bundle.MintedKey == nil || bundle.MintedKey.APIKey != "LLM|test-minted-key" {
			t.Fatalf("expected minted key LLM|test-minted-key, got %+v", bundle.MintedKey)
		}
		if bundle.Email != "user@meta.com" {
			t.Errorf("expected email user@meta.com, got %s", bundle.Email)
		}
		if bundle.Name != "Meta User" {
			t.Errorf("expected name Meta User, got %s", bundle.Name)
		}
	})

	t.Run("mint failure falls back gracefully to dca token", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/key" {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"mint unavailable"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(TokenData{
				AccessToken: "test-access-token-dca-only",
				TokenType:   "Bearer",
				ExpiresIn:   3600,
			})
		}))
		defer server.Close()

		auth := NewMetaAuth(&config.Config{})
		auth.httpClient = server.Client()
		auth.SetMintURL(server.URL + "/key")

		dcr := &DeviceCodeResponse{
			DeviceCode:    "test-device",
			TokenEndpoint: server.URL + "/token",
			Interval:      1,
			ExpiresIn:     10,
		}

		bundle, err := auth.WaitForAuthorization(context.Background(), dcr)
		if err != nil {
			t.Fatalf("WaitForAuthorization error: %v", err)
		}
		if bundle.TokenData == nil || bundle.TokenData.AccessToken != "test-access-token-dca-only" {
			t.Fatalf("expected token test-access-token-dca-only, got %+v", bundle.TokenData)
		}
		if bundle.MintedKey != nil {
			t.Fatalf("expected nil MintedKey on mint failure, got %+v", bundle.MintedKey)
		}
	})
}

func TestCredentialFileName(t *testing.T) {
	if got := CredentialFileName("user@example.com", ""); got != "meta-user_example.com.json" {
		t.Errorf("unexpected fileName: %s", got)
	}
	if got := CredentialFileName("", "12345"); got == "" || !filepath.IsLocal(got) {
		t.Errorf("unexpected subject-based fileName: %s", got)
	}
	if got := CredentialFileName("", ""); got != "meta-oauth.json" {
		t.Errorf("unexpected fallback fileName: %s", got)
	}
}

func TestReadLocalMuseCLIAuth(t *testing.T) {
	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "auth.json")

	content := `{
		"schema_version": 1,
		"providers": {
			"meta": {
				"access_token": "test-token-xyz",
				"api_key": "test-key-abc",
				"api_base_url": "https://api.meta.ai/v1",
				"user_email": "engineer@meta.com"
			}
		}
	}`
	if err := os.WriteFile(authPath, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write auth.json: %v", err)
	}

	t.Setenv("MUSE_AUTH_PATH", authPath)

	cred, ok := ReadLocalMuseCLIAuth()
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if cred.APIKey != "test-key-abc" {
		t.Errorf("expected api_key test-key-abc, got %s", cred.APIKey)
	}
	if cred.BaseURL != "https://api.meta.ai/v1" {
		t.Errorf("expected base_url https://api.meta.ai/v1, got %s", cred.BaseURL)
	}
	if cred.Email != "engineer@meta.com" {
		t.Errorf("expected email engineer@meta.com, got %s", cred.Email)
	}
}
