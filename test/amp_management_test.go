package test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// newAmpTestHandler creates a test handler with default ampcode configuration.
func newAmpTestHandler(t *testing.T) (*management.Handler, string) {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := &config.Config{
		AmpCode: config.AmpCode{
			UpstreamURL:                   "https://example.com",
			UpstreamAPIKey:                "test-api-key-12345",
			RestrictManagementToLocalhost: true,
			ForceModelMappings:            false,
			ModelMappings: []config.AmpModelMapping{
				{From: "gpt-4", To: "gemini-pro"},
			},
		},
	}

	if err := os.WriteFile(configPath, []byte("port: 8080\n"), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	h := management.NewHandler(cfg, configPath, nil)
	return h, configPath
}

// setupAmpRouter creates a test router with all ampcode management endpoints.
func setupAmpRouter(h *management.Handler) *gin.Engine {
	r := gin.New()
	mgmt := r.Group("/v0/management")
	{
		mgmt.GET("/ampcode", h.GetAmpCode)
		mgmt.GET("/ampcode/upstream-url", h.GetAmpUpstreamURL)
		mgmt.PUT("/ampcode/upstream-url", h.PutAmpUpstreamURL)
		mgmt.DELETE("/ampcode/upstream-url", h.DeleteAmpUpstreamURL)
		mgmt.GET("/ampcode/upstream-api-key", h.GetAmpUpstreamAPIKey)
		mgmt.PUT("/ampcode/upstream-api-key", h.PutAmpUpstreamAPIKey)
		mgmt.DELETE("/ampcode/upstream-api-key", h.DeleteAmpUpstreamAPIKey)
		mgmt.GET("/ampcode/restrict-management-to-localhost", h.GetAmpRestrictManagementToLocalhost)
		mgmt.PUT("/ampcode/restrict-management-to-localhost", h.PutAmpRestrictManagementToLocalhost)
		mgmt.GET("/ampcode/model-mappings", h.GetAmpModelMappings)
		mgmt.PUT("/ampcode/model-mappings", h.PutAmpModelMappings)
		mgmt.PATCH("/ampcode/model-mappings", h.PatchAmpModelMappings)
		mgmt.DELETE("/ampcode/model-mappings", h.DeleteAmpModelMappings)
		mgmt.GET("/ampcode/force-model-mappings", h.GetAmpForceModelMappings)
		mgmt.PUT("/ampcode/force-model-mappings", h.PutAmpForceModelMappings)
	}
	return r
}

// TestGetAmpCode verifies GET /v0/management/ampcode returns full ampcode config.
func TestGetAmpCode(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/ampcode", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var resp map[string]config.AmpCode
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	ampcode := resp["ampcode"]
	if ampcode.UpstreamURL != "https://example.com" {
		t.Errorf("expected upstream-url %q, got %q", "https://example.com", ampcode.UpstreamURL)
	}
	if len(ampcode.ModelMappings) != 1 {
		t.Errorf("expected 1 model mapping, got %d", len(ampcode.ModelMappings))
	}
}

// TestGetAmpUpstreamURL verifies GET /v0/management/ampcode/upstream-url returns the upstream URL.
func TestGetAmpUpstreamURL(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/ampcode/upstream-url", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp["upstream-url"] != "https://example.com" {
		t.Errorf("expected %q, got %q", "https://example.com", resp["upstream-url"])
	}
}

// TestPutAmpUpstreamURL verifies PUT /v0/management/ampcode/upstream-url updates the upstream URL.
func TestPutAmpUpstreamURL(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	body := `{"value": "https://new-upstream.com"}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/ampcode/upstream-url", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
}

// TestDeleteAmpUpstreamURL verifies DELETE /v0/management/ampcode/upstream-url clears the upstream URL.
func TestDeleteAmpUpstreamURL(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	req := httptest.NewRequest(http.MethodDelete, "/v0/management/ampcode/upstream-url", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}
}

// TestGetAmpUpstreamAPIKey verifies GET /v0/management/ampcode/upstream-api-key returns the API key.
func TestGetAmpUpstreamAPIKey(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/ampcode/upstream-api-key", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	key := resp["upstream-api-key"].(string)
	if key != "test-api-key-12345" {
		t.Errorf("expected key %q, got %q", "test-api-key-12345", key)
	}
}

// TestPutAmpUpstreamAPIKey verifies PUT /v0/management/ampcode/upstream-api-key updates the API key.
func TestPutAmpUpstreamAPIKey(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	body := `{"value": "new-secret-key"}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/ampcode/upstream-api-key", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}
}

// TestDeleteAmpUpstreamAPIKey verifies DELETE /v0/management/ampcode/upstream-api-key clears the API key.
func TestDeleteAmpUpstreamAPIKey(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	req := httptest.NewRequest(http.MethodDelete, "/v0/management/ampcode/upstream-api-key", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}
}

// TestGetAmpRestrictManagementToLocalhost verifies GET returns the localhost restriction setting.
func TestGetAmpRestrictManagementToLocalhost(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/ampcode/restrict-management-to-localhost", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var resp map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp["restrict-management-to-localhost"] != true {
		t.Error("expected restrict-management-to-localhost to be true")
	}
}

// TestPutAmpRestrictManagementToLocalhost verifies PUT updates the localhost restriction setting.
func TestPutAmpRestrictManagementToLocalhost(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	body := `{"value": false}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/ampcode/restrict-management-to-localhost", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}
}

// TestGetAmpModelMappings verifies GET /v0/management/ampcode/model-mappings returns all mappings.
func TestGetAmpModelMappings(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/ampcode/model-mappings", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var resp map[string][]config.AmpModelMapping
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	mappings := resp["model-mappings"]
	if len(mappings) != 1 {
		t.Fatalf("expected 1 mapping, got %d", len(mappings))
	}
	if mappings[0].From != "gpt-4" || mappings[0].To != "gemini-pro" {
		t.Errorf("unexpected mapping: %+v", mappings[0])
	}
}

// TestPutAmpModelMappings verifies PUT /v0/management/ampcode/model-mappings replaces all mappings.
func TestPutAmpModelMappings(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	body := `{"value": [{"from": "claude-3", "to": "gpt-4o"}, {"from": "gemini", "to": "claude"}]}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/ampcode/model-mappings", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
}

// TestPatchAmpModelMappings verifies PATCH updates existing mappings and adds new ones.
func TestPatchAmpModelMappings(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	body := `{"value": [{"from": "gpt-4", "to": "updated-model"}, {"from": "new-model", "to": "target"}]}`
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/ampcode/model-mappings", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}
}

// TestDeleteAmpModelMappings_Specific verifies DELETE removes specified mappings by "from" field.
func TestDeleteAmpModelMappings_Specific(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	body := `{"value": ["gpt-4"]}`
	req := httptest.NewRequest(http.MethodDelete, "/v0/management/ampcode/model-mappings", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}
}

// TestDeleteAmpModelMappings_All verifies DELETE with empty body removes all mappings.
func TestDeleteAmpModelMappings_All(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	req := httptest.NewRequest(http.MethodDelete, "/v0/management/ampcode/model-mappings", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}
}

// TestGetAmpForceModelMappings verifies GET returns the force-model-mappings setting.
func TestGetAmpForceModelMappings(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/ampcode/force-model-mappings", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var resp map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp["force-model-mappings"] != false {
		t.Error("expected force-model-mappings to be false")
	}
}

// TestPutAmpForceModelMappings verifies PUT updates the force-model-mappings setting.
func TestPutAmpForceModelMappings(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	body := `{"value": true}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/ampcode/force-model-mappings", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}
}

// TestPutAmpModelMappings_VerifyState verifies PUT replaces mappings and state is persisted.
func TestPutAmpModelMappings_VerifyState(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	body := `{"value": [{"from": "model-a", "to": "model-b"}, {"from": "model-c", "to": "model-d"}, {"from": "model-e", "to": "model-f"}]}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/ampcode/model-mappings", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT failed: status %d, body: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/ampcode/model-mappings", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp map[string][]config.AmpModelMapping
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	mappings := resp["model-mappings"]
	if len(mappings) != 3 {
		t.Fatalf("expected 3 mappings, got %d", len(mappings))
	}

	expected := map[string]string{"model-a": "model-b", "model-c": "model-d", "model-e": "model-f"}
	for _, m := range mappings {
		if expected[m.From] != m.To {
			t.Errorf("mapping %q -> expected %q, got %q", m.From, expected[m.From], m.To)
		}
	}
}

// TestPatchAmpModelMappings_VerifyState verifies PATCH merges mappings correctly.
func TestPatchAmpModelMappings_VerifyState(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	body := `{"value": [{"from": "gpt-4", "to": "updated-target"}, {"from": "new-model", "to": "new-target"}]}`
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/ampcode/model-mappings", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PATCH failed: status %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/ampcode/model-mappings", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp map[string][]config.AmpModelMapping
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	mappings := resp["model-mappings"]
	if len(mappings) != 2 {
		t.Fatalf("expected 2 mappings (1 updated + 1 new), got %d", len(mappings))
	}

	found := make(map[string]string)
	for _, m := range mappings {
		found[m.From] = m.To
	}

	if found["gpt-4"] != "updated-target" {
		t.Errorf("gpt-4 should map to updated-target, got %q", found["gpt-4"])
	}
	if found["new-model"] != "new-target" {
		t.Errorf("new-model should map to new-target, got %q", found["new-model"])
	}
}

// TestDeleteAmpModelMappings_VerifyState verifies DELETE removes specific mappings and keeps others.
func TestDeleteAmpModelMappings_VerifyState(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	putBody := `{"value": [{"from": "a", "to": "1"}, {"from": "b", "to": "2"}, {"from": "c", "to": "3"}]}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/ampcode/model-mappings", bytes.NewBufferString(putBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	delBody := `{"value": ["a", "c"]}`
	req = httptest.NewRequest(http.MethodDelete, "/v0/management/ampcode/model-mappings", bytes.NewBufferString(delBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("DELETE failed: status %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/ampcode/model-mappings", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp map[string][]config.AmpModelMapping
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	mappings := resp["model-mappings"]
	if len(mappings) != 1 {
		t.Fatalf("expected 1 mapping remaining, got %d", len(mappings))
	}
	if mappings[0].From != "b" || mappings[0].To != "2" {
		t.Errorf("expected b->2, got %s->%s", mappings[0].From, mappings[0].To)
	}
}

// TestDeleteAmpModelMappings_NonExistent verifies DELETE with non-existent mapping doesn't affect existing ones.
func TestDeleteAmpModelMappings_NonExistent(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	delBody := `{"value": ["non-existent-model"]}`
	req := httptest.NewRequest(http.MethodDelete, "/v0/management/ampcode/model-mappings", bytes.NewBufferString(delBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/ampcode/model-mappings", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp map[string][]config.AmpModelMapping
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(resp["model-mappings"]) != 1 {
		t.Errorf("original mapping should remain, got %d mappings", len(resp["model-mappings"]))
	}
}

// TestPutAmpModelMappings_Empty verifies PUT with empty array clears all mappings.
func TestPutAmpModelMappings_Empty(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	body := `{"value": []}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/ampcode/model-mappings", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/ampcode/model-mappings", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp map[string][]config.AmpModelMapping
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(resp["model-mappings"]) != 0 {
		t.Errorf("expected 0 mappings, got %d", len(resp["model-mappings"]))
	}
}

// TestPutAmpUpstreamURL_VerifyState verifies PUT updates upstream URL and persists state.
func TestPutAmpUpstreamURL_VerifyState(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	body := `{"value": "https://new-api.example.com"}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/ampcode/upstream-url", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT failed: status %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/ampcode/upstream-url", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if resp["upstream-url"] != "https://new-api.example.com" {
		t.Errorf("expected %q, got %q", "https://new-api.example.com", resp["upstream-url"])
	}
}

// TestDeleteAmpUpstreamURL_VerifyState verifies DELETE clears upstream URL.
func TestDeleteAmpUpstreamURL_VerifyState(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	req := httptest.NewRequest(http.MethodDelete, "/v0/management/ampcode/upstream-url", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("DELETE failed: status %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/ampcode/upstream-url", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if resp["upstream-url"] != "" {
		t.Errorf("expected empty string, got %q", resp["upstream-url"])
	}
}

// TestPutAmpUpstreamAPIKey_VerifyState verifies PUT updates API key and persists state.
func TestPutAmpUpstreamAPIKey_VerifyState(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	body := `{"value": "new-secret-api-key-xyz"}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/ampcode/upstream-api-key", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT failed: status %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/ampcode/upstream-api-key", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if resp["upstream-api-key"] != "new-secret-api-key-xyz" {
		t.Errorf("expected %q, got %q", "new-secret-api-key-xyz", resp["upstream-api-key"])
	}
}

// TestDeleteAmpUpstreamAPIKey_VerifyState verifies DELETE clears API key.
func TestDeleteAmpUpstreamAPIKey_VerifyState(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	req := httptest.NewRequest(http.MethodDelete, "/v0/management/ampcode/upstream-api-key", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("DELETE failed: status %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/ampcode/upstream-api-key", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if resp["upstream-api-key"] != "" {
		t.Errorf("expected empty string, got %q", resp["upstream-api-key"])
	}
}

// TestPutAmpRestrictManagementToLocalhost_VerifyState verifies PUT updates localhost restriction.
func TestPutAmpRestrictManagementToLocalhost_VerifyState(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	body := `{"value": false}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/ampcode/restrict-management-to-localhost", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT failed: status %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/ampcode/restrict-management-to-localhost", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if resp["restrict-management-to-localhost"] != false {
		t.Error("expected false after update")
	}
}

// TestPutAmpForceModelMappings_VerifyState verifies PUT updates force-model-mappings setting.
func TestPutAmpForceModelMappings_VerifyState(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	body := `{"value": true}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/ampcode/force-model-mappings", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT failed: status %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/ampcode/force-model-mappings", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if resp["force-model-mappings"] != true {
		t.Error("expected true after update")
	}
}

// TestPutBoolField_EmptyObject verifies PUT with empty object returns 400.
func TestPutBoolField_EmptyObject(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	body := `{}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/ampcode/force-model-mappings", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d for empty object, got %d", http.StatusBadRequest, w.Code)
	}
}

// TestComplexMappingsWorkflow tests a full workflow: PUT, PATCH, DELETE, and GET.
func TestComplexMappingsWorkflow(t *testing.T) {
	h, _ := newAmpTestHandler(t)
	r := setupAmpRouter(h)

	putBody := `{"value": [{"from": "m1", "to": "t1"}, {"from": "m2", "to": "t2"}, {"from": "m3", "to": "t3"}, {"from": "m4", "to": "t4"}]}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/ampcode/model-mappings", bytes.NewBufferString(putBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	patchBody := `{"value": [{"from": "m2", "to": "t2-updated"}, {"from": "m5", "to": "t5"}]}`
	req = httptest.NewRequest(http.MethodPatch, "/v0/management/ampcode/model-mappings", bytes.NewBufferString(patchBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	delBody := `{"value": ["m1", "m3"]}`
	req = httptest.NewRequest(http.MethodDelete, "/v0/management/ampcode/model-mappings", bytes.NewBufferString(delBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	req = httptest.NewRequest(http.MethodGet, "/v0/management/ampcode/model-mappings", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp map[string][]config.AmpModelMapping
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	mappings := resp["model-mappings"]
	if len(mappings) != 3 {
		t.Fatalf("expected 3 mappings (m2, m4, m5), got %d", len(mappings))
	}

	expected := map[string]string{"m2": "t2-updated", "m4": "t4", "m5": "t5"}
	found := make(map[string]string)
	for _, m := range mappings {
		found[m.From] = m.To
	}

	for from, to := range expected {
		if found[from] != to {
			t.Errorf("mapping %s: expected %q, got %q", from, to, found[from])
		}
	}
}

// TestNilHandlerGetAmpCode verifies handler works with empty config.
func TestNilHandlerGetAmpCode(t *testing.T) {
	cfg := &config.Config{}
	h := management.NewHandler(cfg, "", nil)
	r := setupAmpRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/ampcode", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}
}

// TestEmptyConfigGetAmpModelMappings verifies GET returns empty array for fresh config.
func TestEmptyConfigGetAmpModelMappings(t *testing.T) {
	cfg := &config.Config{}
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8080\n"), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	h := management.NewHandler(cfg, configPath, nil)
	r := setupAmpRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/ampcode/model-mappings", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var resp map[string][]config.AmpModelMapping
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(resp["model-mappings"]) != 0 {
		t.Errorf("expected 0 mappings, got %d", len(resp["model-mappings"]))
	}
}
