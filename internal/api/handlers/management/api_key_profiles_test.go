package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/apikeyusage"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestAPIKeyProfileCRUDRevealsSecretOnlyOnCreate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	handler := &Handler{cfg: &config.Config{}, configFilePath: configPath}
	engine := gin.New()
	engine.GET("/api-key-profiles", handler.GetAPIKeyProfiles)
	engine.POST("/api-key-profiles", handler.PostAPIKeyProfile)
	engine.PUT("/api-key-profiles/:id", handler.PutAPIKeyProfile)
	engine.DELETE("/api-key-profiles/:id", handler.DeleteAPIKeyProfile)

	createRecorder := performJSONRequest(t, engine, http.MethodPost, "/api-key-profiles", `{"name":"Alice","weekly":{"requests":25}}`)
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createRecorder.Code, createRecorder.Body.String())
	}
	var created struct {
		Profile apiKeyProfileResponse `json:"profile"`
	}
	decodeJSONResponse(t, createRecorder, &created)
	if len(created.Profile.APIKey) < minimumManagedAPIKeyLength {
		t.Fatalf("generated API key length = %d", len(created.Profile.APIKey))
	}
	if created.Profile.KeyFingerprint != apikeyusage.Fingerprint(created.Profile.APIKey) {
		t.Fatalf("fingerprint = %q, want %q", created.Profile.KeyFingerprint, apikeyusage.Fingerprint(created.Profile.APIKey))
	}
	secret := created.Profile.APIKey

	listRecorder := performJSONRequest(t, engine, http.MethodGet, "/api-key-profiles", "")
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRecorder.Code, listRecorder.Body.String())
	}
	if strings.Contains(listRecorder.Body.String(), secret) {
		t.Fatal("list response leaked the API key secret")
	}
	var listed struct {
		Profiles []apiKeyProfileResponse `json:"api-key-profiles"`
	}
	decodeJSONResponse(t, listRecorder, &listed)
	if len(listed.Profiles) != 1 || listed.Profiles[0].APIKey != "" || listed.Profiles[0].KeyFingerprint == "" {
		t.Fatalf("listed profiles = %#v", listed.Profiles)
	}

	updatePath := "/api-key-profiles/" + created.Profile.ID
	updateRecorder := performJSONRequest(t, engine, http.MethodPut, updatePath, `{"name":"Alice Updated","monthly":{"tokens":1000}}`)
	if updateRecorder.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updateRecorder.Code, updateRecorder.Body.String())
	}
	if strings.Contains(updateRecorder.Body.String(), secret) {
		t.Fatal("update response leaked the API key secret")
	}
	var updated struct {
		Profile apiKeyProfileResponse `json:"profile"`
	}
	decodeJSONResponse(t, updateRecorder, &updated)
	if updated.Profile.Name != "Alice Updated" || updated.Profile.Monthly.Tokens != 1000 || updated.Profile.KeyFingerprint != created.Profile.KeyFingerprint {
		t.Fatalf("updated profile = %#v", updated.Profile)
	}
	if len(handler.cfg.APIKeyProfiles) != 1 || handler.cfg.APIKeyProfiles[0].APIKey != secret {
		t.Fatal("update did not preserve the existing API key")
	}

	deleteRecorder := performJSONRequest(t, engine, http.MethodDelete, updatePath, "")
	if deleteRecorder.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", deleteRecorder.Code, deleteRecorder.Body.String())
	}
	if len(handler.cfg.APIKeyProfiles) != 0 {
		t.Fatalf("profiles after delete = %#v", handler.cfg.APIKeyProfiles)
	}
}

func performJSONRequest(t *testing.T, engine http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	engine.ServeHTTP(recorder, request)
	return recorder
}

func decodeJSONResponse(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
}
