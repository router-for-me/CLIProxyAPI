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

func TestSaveTokenToFile_PreservesCustomMetadata(t *testing.T) {
	tempDir := t.TempDir()
	authFilePath := filepath.Join(tempDir, "meta-test.json")

	storage := &MetaTokenStorage{
		Type:        "meta",
		AuthKind:    "oauth",
		AccessToken: "test-access-token",
		APIKey:      "test-api-key",
		DCAToken:    "dca:token",
		Email:       "user@example.com",
	}
	storage.SetMetadata(map[string]any{
		"priority":        float64(10),
		"disable_cooling": true,
		"proxy_url":       "http://proxy:8080",
	})

	if errSave := storage.SaveTokenToFile(authFilePath); errSave != nil {
		t.Fatalf("SaveTokenToFile() error = %v", errSave)
	}

	savedRaw, errRead := os.ReadFile(authFilePath)
	if errRead != nil {
		t.Fatalf("os.ReadFile error = %v", errRead)
	}

	var saved map[string]any
	if errUnmarshal := json.Unmarshal(savedRaw, &saved); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal error = %v", errUnmarshal)
	}

	if saved["api_key"] != "test-api-key" {
		t.Errorf("api_key = %v, want test-api-key", saved["api_key"])
	}
	if saved["priority"] != float64(10) {
		t.Errorf("priority = %v, want 10", saved["priority"])
	}
	if saved["disable_cooling"] != true {
		t.Errorf("disable_cooling = %v, want true", saved["disable_cooling"])
	}
	if saved["proxy_url"] != "http://proxy:8080" {
		t.Errorf("proxy_url = %v, want http://proxy:8080", saved["proxy_url"])
	}
}

func TestSaveTokenToFile_DoesNotRestoreClearedCredentialFields(t *testing.T) {
	tempDir := t.TempDir()
	authFilePath := filepath.Join(tempDir, "meta-test.json")

	// Pre-populate an auth file that had expired DCA credentials and custom configuration
	initial := map[string]any{
		"type":            "meta",
		"auth_kind":       "oauth",
		"access_token":    "old-dca-token",
		"dca_token":       "old-dca-token",
		"expired":         "2026-01-01T00:00:00Z",
		"dca_expired":     "2026-01-01T00:00:00Z",
		"dca_expires_at":  float64(1767225600),
		"priority":        float64(42),
		"models":          []any{"muse-latest"},
		"disable-cooling": true,
	}
	raw, err := json.Marshal(initial)
	if err != nil {
		t.Fatalf("json.Marshal error = %v", err)
	}
	if err := os.WriteFile(authFilePath, raw, 0600); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	// Token storage after minting an API key: Expired is explicitly empty, new APIKey set
	storage := &MetaTokenStorage{
		Type:        "meta",
		AuthKind:    "oauth",
		AccessToken: "new-minted-key",
		APIKey:      "new-minted-key",
		DCAToken:    "new-dca-token",
		Expired:     "", // Deliberately cleared on refresh
		Email:       "user@example.com",
	}

	if errSave := storage.SaveTokenToFile(authFilePath); errSave != nil {
		t.Fatalf("SaveTokenToFile error = %v", errSave)
	}

	savedRaw, errRead := os.ReadFile(authFilePath)
	if errRead != nil {
		t.Fatalf("os.ReadFile error = %v", errRead)
	}

	var saved map[string]any
	if errUnmarshal := json.Unmarshal(savedRaw, &saved); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal error = %v", errUnmarshal)
	}

	// Verify cleared credential fields were NOT restored from disk
	if val, exists := saved["expired"]; exists && val != "" {
		t.Errorf("SaveTokenToFile restored expired timestamp: %v", val)
	}
	if val, exists := saved["dca_expired"]; exists && val != "" {
		t.Errorf("SaveTokenToFile restored dca_expired timestamp: %v", val)
	}
	if _, exists := saved["dca_expires_at"]; exists {
		t.Errorf("SaveTokenToFile restored dca_expires_at timestamp")
	}

	// Verify new credentials were written
	if saved["api_key"] != "new-minted-key" {
		t.Errorf("api_key = %v, want new-minted-key", saved["api_key"])
	}
	if saved["access_token"] != "new-minted-key" {
		t.Errorf("access_token = %v, want new-minted-key", saved["access_token"])
	}

	// Verify non-credential configuration from disk was preserved
	if saved["priority"] != float64(42) {
		t.Errorf("priority = %v, want 42", saved["priority"])
	}
	if saved["disable-cooling"] != true {
		t.Errorf("disable-cooling = %v, want true", saved["disable-cooling"])
	}
	if models, ok := saved["models"].([]any); !ok || len(models) != 1 || models[0] != "muse-latest" {
		t.Errorf("models = %v, want [muse-latest]", saved["models"])
	}
}
