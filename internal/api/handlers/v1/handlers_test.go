package v1

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/routing"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func setupTestRouter(h *Handler, authSecret string) *gin.Engine {
	r := gin.New()

	v1 := r.Group("/v1")
	{
		v1.GET("/profiles", h.ListProfiles)
		v1.POST("/profiles", AuthRequired(authSecret), h.CreateProfile)
		v1.PUT("/profiles/:id", AuthRequired(authSecret), h.UpdateProfile)
		v1.DELETE("/profiles/:id", AuthRequired(authSecret), h.DeleteProfile)

		v1.GET("/routing/rules", h.ListRoutingRules)
		v1.POST("/routing/rules", AuthRequired(authSecret), h.CreateRoutingRule)
		v1.PUT("/routing/rules/:id", AuthRequired(authSecret), h.UpdateRoutingRule)
		v1.DELETE("/routing/rules/:id", AuthRequired(authSecret), h.DeleteRoutingRule)

		v1.GET("/diagnostics/bundle", h.GetDiagnosticsBundle)
		v1.GET("/diagnostics/health", h.GetHealth)
	}

	return r
}

func TestListProfiles_ReturnsProfiles(t *testing.T) {
	profileID := "test-profile-1"
	cfg := &routing.RoutingConfig{
		Version:         1,
		ActiveProfileID: &profileID,
		Profiles: []routing.Profile{
			{
				ID:        profileID,
				Name:      "Test Profile",
				Color:     "#FF5733",
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			},
		},
	}

	h := NewHandler(cfg)
	r := setupTestRouter(h, "test-secret")

	req := httptest.NewRequest(http.MethodGet, "/v1/profiles", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /v1/profiles status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp ProfilesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if len(resp.Profiles) != 1 {
		t.Errorf("len(profiles) = %d, want 1", len(resp.Profiles))
	}

	if resp.Profiles[0].ID != profileID {
		t.Errorf("profile.ID = %q, want %q", resp.Profiles[0].ID, profileID)
	}
}

func TestListProfiles_WorksWithoutAuth(t *testing.T) {
	cfg := routing.DefaultRoutingConfig()
	h := NewHandler(cfg)
	r := setupTestRouter(h, "test-secret")

	req := httptest.NewRequest(http.MethodGet, "/v1/profiles", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /v1/profiles without auth status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestCreateProfile_RequiresAuth(t *testing.T) {
	cfg := routing.DefaultRoutingConfig()
	h := NewHandler(cfg)
	r := setupTestRouter(h, "test-secret")

	body := `{"name":"New Profile","color":"#00FF00"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/profiles", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/profiles without auth status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestCreateProfile_WithBearerToken(t *testing.T) {
	cfg := routing.DefaultRoutingConfig()
	h := NewHandler(cfg)
	r := setupTestRouter(h, "test-secret")

	body := `{"name":"New Profile","color":"#00FF00"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/profiles", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("POST /v1/profiles with bearer token status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}

	var resp ProfileResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if resp.Profile.Name != "New Profile" {
		t.Errorf("profile.Name = %q, want %q", resp.Profile.Name, "New Profile")
	}
}

func TestCreateProfile_WithManagementKey(t *testing.T) {
	cfg := routing.DefaultRoutingConfig()
	h := NewHandler(cfg)
	r := setupTestRouter(h, "test-secret")

	body := `{"name":"New Profile","color":"#00FF00"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/profiles", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Management-Key", "test-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("POST /v1/profiles with X-Management-Key status = %d, want %d", w.Code, http.StatusCreated)
	}
}

func TestUpdateProfile_RequiresAuth(t *testing.T) {
	profileID := "test-profile-1"
	cfg := &routing.RoutingConfig{
		Version: 1,
		Profiles: []routing.Profile{
			{ID: profileID, Name: "Original", Color: "#FF0000"},
		},
	}

	h := NewHandler(cfg)
	r := setupTestRouter(h, "test-secret")

	body := `{"name":"Updated Profile","color":"#0000FF"}`
	req := httptest.NewRequest(http.MethodPut, "/v1/profiles/"+profileID, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("PUT /v1/profiles/:id without auth status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestUpdateProfile_WithAuth(t *testing.T) {
	profileID := "test-profile-1"
	cfg := &routing.RoutingConfig{
		Version: 1,
		Profiles: []routing.Profile{
			{ID: profileID, Name: "Original", Color: "#FF0000", CreatedAt: time.Now(), UpdatedAt: time.Now()},
		},
	}

	h := NewHandler(cfg)
	r := setupTestRouter(h, "test-secret")

	body := `{"name":"Updated Profile","color":"#0000FF"}`
	req := httptest.NewRequest(http.MethodPut, "/v1/profiles/"+profileID, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("PUT /v1/profiles/:id with auth status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}

	var resp ProfileResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if resp.Profile.Name != "Updated Profile" {
		t.Errorf("profile.Name = %q, want %q", resp.Profile.Name, "Updated Profile")
	}
}

func TestDeleteProfile_RequiresAuth(t *testing.T) {
	profileID := "test-profile-1"
	cfg := &routing.RoutingConfig{
		Version: 1,
		Profiles: []routing.Profile{
			{ID: profileID, Name: "Test", Color: "#FF0000"},
		},
	}

	h := NewHandler(cfg)
	r := setupTestRouter(h, "test-secret")

	req := httptest.NewRequest(http.MethodDelete, "/v1/profiles/"+profileID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("DELETE /v1/profiles/:id without auth status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestDeleteProfile_WithAuth(t *testing.T) {
	profileID := "test-profile-1"
	cfg := &routing.RoutingConfig{
		Version: 1,
		Profiles: []routing.Profile{
			{ID: profileID, Name: "Test", Color: "#FF0000"},
		},
	}

	h := NewHandler(cfg)
	r := setupTestRouter(h, "test-secret")

	req := httptest.NewRequest(http.MethodDelete, "/v1/profiles/"+profileID, nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("DELETE /v1/profiles/:id with auth status = %d, want %d", w.Code, http.StatusNoContent)
	}

	if len(h.config.Profiles) != 0 {
		t.Errorf("profiles after delete = %d, want 0", len(h.config.Profiles))
	}
}

func TestDeleteProfile_NotFound(t *testing.T) {
	cfg := routing.DefaultRoutingConfig()
	h := NewHandler(cfg)
	r := setupTestRouter(h, "test-secret")

	req := httptest.NewRequest(http.MethodDelete, "/v1/profiles/nonexistent", nil)
	req.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("DELETE nonexistent profile status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestGetDiagnosticsBundle_ReturnsBundle(t *testing.T) {
	cfg := routing.DefaultRoutingConfig()
	h := NewHandler(cfg)
	r := setupTestRouter(h, "test-secret")

	req := httptest.NewRequest(http.MethodGet, "/v1/diagnostics/bundle", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /v1/diagnostics/bundle status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp DiagnosticsBundle
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if resp.Version == "" {
		t.Error("bundle.Version should not be empty")
	}

	if resp.Timestamp == "" {
		t.Error("bundle.Timestamp should not be empty")
	}
}

func TestGetHealth_ReturnsHealthStatus(t *testing.T) {
	cfg := routing.DefaultRoutingConfig()
	h := NewHandler(cfg)
	r := setupTestRouter(h, "test-secret")

	req := httptest.NewRequest(http.MethodGet, "/v1/diagnostics/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /v1/diagnostics/health status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp HealthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if resp.Status != "healthy" {
		t.Errorf("health.Status = %q, want %q", resp.Status, "healthy")
	}
}

func TestDiagnostics_WorksWithoutAuth(t *testing.T) {
	cfg := routing.DefaultRoutingConfig()
	h := NewHandler(cfg)
	r := setupTestRouter(h, "test-secret")

	req := httptest.NewRequest(http.MethodGet, "/v1/diagnostics/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /v1/diagnostics/health without auth status = %d, want %d", w.Code, http.StatusOK)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/diagnostics/bundle", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /v1/diagnostics/bundle without auth status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestListRoutingRules_ReturnsRules(t *testing.T) {
	groupID := "test-group"
	cfg := &routing.RoutingConfig{
		Version: 1,
		ProviderGroups: []routing.ProviderGroup{
			{
				ID:                groupID,
				Name:              "Test Group",
				AccountIDs:        []string{"account-1"},
				SelectionStrategy: routing.SelectionRoundRobin,
			},
		},
	}

	h := NewHandler(cfg)
	r := setupTestRouter(h, "test-secret")

	req := httptest.NewRequest(http.MethodGet, "/v1/routing/rules", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /v1/routing/rules status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp RoutingRulesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if len(resp.ProviderGroups) != 1 {
		t.Errorf("len(providerGroups) = %d, want 1", len(resp.ProviderGroups))
	}
}

func TestCreateRoutingRule_RequiresAuth(t *testing.T) {
	cfg := routing.DefaultRoutingConfig()
	h := NewHandler(cfg)
	r := setupTestRouter(h, "test-secret")

	body := `{"name":"New Group","accountIds":["acc-1"],"selectionStrategy":"round-robin"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/routing/rules", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/routing/rules without auth status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestCreateRoutingRule_WithAuth(t *testing.T) {
	cfg := routing.DefaultRoutingConfig()
	h := NewHandler(cfg)
	r := setupTestRouter(h, "test-secret")

	body := `{"name":"New Group","accountIds":["acc-1"],"selectionStrategy":"round-robin"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/routing/rules", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("POST /v1/routing/rules with auth status = %d, want %d; body: %s", w.Code, http.StatusCreated, w.Body.String())
	}
}

func TestUpdateRoutingRule_RequiresAuth(t *testing.T) {
	groupID := "test-group"
	cfg := &routing.RoutingConfig{
		Version: 1,
		ProviderGroups: []routing.ProviderGroup{
			{ID: groupID, Name: "Test Group", SelectionStrategy: routing.SelectionRoundRobin},
		},
	}

	h := NewHandler(cfg)
	r := setupTestRouter(h, "test-secret")

	body := `{"name":"Updated Group","selectionStrategy":"random"}`
	req := httptest.NewRequest(http.MethodPut, "/v1/routing/rules/"+groupID, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("PUT /v1/routing/rules/:id without auth status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestDeleteRoutingRule_RequiresAuth(t *testing.T) {
	groupID := "test-group"
	cfg := &routing.RoutingConfig{
		Version: 1,
		ProviderGroups: []routing.ProviderGroup{
			{ID: groupID, Name: "Test Group"},
		},
	}

	h := NewHandler(cfg)
	r := setupTestRouter(h, "test-secret")

	req := httptest.NewRequest(http.MethodDelete, "/v1/routing/rules/"+groupID, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("DELETE /v1/routing/rules/:id without auth status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAuthRequired_InvalidToken(t *testing.T) {
	cfg := routing.DefaultRoutingConfig()
	h := NewHandler(cfg)
	r := setupTestRouter(h, "test-secret")

	body := `{"name":"New Profile","color":"#00FF00"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/profiles", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/profiles with invalid token status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAuthRequired_MalformedAuthHeader(t *testing.T) {
	cfg := routing.DefaultRoutingConfig()
	h := NewHandler(cfg)
	r := setupTestRouter(h, "test-secret")

	body := `{"name":"New Profile","color":"#00FF00"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/profiles", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "NotBearer test-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/profiles with malformed auth status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}
