package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestExtractClaudeQuota_NilMetadata(t *testing.T) {
	result := extractClaudeQuota(nil)
	if result != nil {
		t.Fatal("expected nil for nil metadata")
	}
}

func TestExtractClaudeQuota_NoQuotaKey(t *testing.T) {
	metadata := map[string]any{
		"email": "test@example.com",
	}
	result := extractClaudeQuota(metadata)
	if result != nil {
		t.Fatal("expected nil when quota key doesn't exist")
	}
}

func TestExtractClaudeQuota_DirectPointer(t *testing.T) {
	quota := &executor.ClaudeCodeQuotaInfo{
		UnifiedStatus:       "ok",
		FiveHourUtilization: 0.5,
		LastUpdated:         time.Now(),
	}
	metadata := map[string]any{
		"claude_code_quota": quota,
	}

	result := extractClaudeQuota(metadata)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.UnifiedStatus != "ok" {
		t.Fatalf("expected UnifiedStatus 'ok', got %s", result.UnifiedStatus)
	}
	if result.FiveHourUtilization != 0.5 {
		t.Fatalf("expected FiveHourUtilization 0.5, got %f", result.FiveHourUtilization)
	}
}

func TestExtractClaudeQuota_JSONDeserialized(t *testing.T) {
	// Simulate JSON deserialization from disk
	metadata := map[string]any{
		"claude_code_quota": map[string]any{
			"unified_status":        "ok",
			"five_hour_utilization": 0.75,
			"seven_day_reset":       int64(1234567890),
		},
	}

	result := extractClaudeQuota(metadata)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.UnifiedStatus != "ok" {
		t.Fatalf("expected UnifiedStatus 'ok', got %s", result.UnifiedStatus)
	}
	if result.FiveHourUtilization != 0.75 {
		t.Fatalf("expected FiveHourUtilization 0.75, got %f", result.FiveHourUtilization)
	}
	if result.SevenDayReset != 1234567890 {
		t.Fatalf("expected SevenDayReset 1234567890, got %d", result.SevenDayReset)
	}
}

func TestExtractClaudeQuota_InvalidData(t *testing.T) {
	metadata := map[string]any{
		"claude_code_quota": "invalid string data",
	}

	result := extractClaudeQuota(metadata)
	if result != nil {
		t.Fatal("expected nil for invalid data")
	}
}

func TestGetClaudeCodeQuotas_NilHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	var h *Handler
	h.GetClaudeCodeQuotas(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	quotas, ok := response["quotas"].([]any)
	if !ok {
		t.Fatal("expected quotas array in response")
	}
	if len(quotas) != 0 {
		t.Fatalf("expected empty quotas array, got %d items", len(quotas))
	}
}

func TestGetClaudeCodeQuotas_NilAuthManager(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	h := &Handler{
		cfg:         &config.Config{},
		authManager: nil,
	}
	h.GetClaudeCodeQuotas(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	quotas, ok := response["quotas"].([]any)
	if !ok {
		t.Fatal("expected quotas array in response")
	}
	if len(quotas) != 0 {
		t.Fatalf("expected empty quotas array, got %d items", len(quotas))
	}
}

func TestGetClaudeCodeQuotas_EmptyList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	manager := auth.NewManager(nil, nil, nil)
	h := &Handler{
		cfg:         &config.Config{},
		authManager: manager,
	}
	h.GetClaudeCodeQuotas(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	quotas, ok := response["quotas"].([]any)
	if !ok {
		t.Fatal("expected quotas array in response")
	}
	if len(quotas) != 0 {
		t.Fatalf("expected empty quotas array, got %d items", len(quotas))
	}

	count, ok := response["count"].(float64)
	if !ok {
		t.Fatal("expected count in response")
	}
	if count != 0 {
		t.Fatalf("expected count 0, got %f", count)
	}
}

func TestGetClaudeCodeQuotas_WithClaudeOAuthAccounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	manager := auth.NewManager(nil, nil, nil)

	// Add Claude OAuth account with quota
	quota := &executor.ClaudeCodeQuotaInfo{
		UnifiedStatus:       "ok",
		FiveHourUtilization: 0.3,
		SevenDayUtilization: 0.6,
		LastUpdated:         time.Now(),
	}
	claudeAuth := &auth.Auth{
		ID:       "claude-1",
		Provider: "claude",
		Label:    "Test Claude",
		Metadata: map[string]any{
			"access_token":      "test-token",
			"email":             "test@example.com",
			"claude_code_quota": quota,
		},
	}
	_, _ = manager.Register(context.Background(), claudeAuth)

	// Add non-Claude account (should be filtered out)
	geminiAuth := &auth.Auth{
		ID:       "gemini-1",
		Provider: "gemini",
		Metadata: map[string]any{
			"email": "gemini@example.com",
		},
	}
	_, _ = manager.Register(context.Background(), geminiAuth)

	// Add Claude account without OAuth (should be filtered out)
	claudeAPIKey := &auth.Auth{
		ID:       "claude-2",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key": "sk-test",
		},
	}
	_, _ = manager.Register(context.Background(), claudeAPIKey)

	h := &Handler{
		cfg:         &config.Config{},
		authManager: manager,
	}
	h.GetClaudeCodeQuotas(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	quotas, ok := response["quotas"].([]any)
	if !ok {
		t.Fatal("expected quotas array in response")
	}
	if len(quotas) != 1 {
		t.Fatalf("expected 1 quota, got %d", len(quotas))
	}

	count, ok := response["count"].(float64)
	if !ok {
		t.Fatal("expected count in response")
	}
	if count != 1 {
		t.Fatalf("expected count 1, got %f", count)
	}

	quotaEntry := quotas[0].(map[string]any)
	if quotaEntry["auth_id"] != "claude-1" {
		t.Fatalf("expected auth_id 'claude-1', got %v", quotaEntry["auth_id"])
	}
	if quotaEntry["email"] != "test@example.com" {
		t.Fatalf("expected email 'test@example.com', got %v", quotaEntry["email"])
	}
	if quotaEntry["label"] != "Test Claude" {
		t.Fatalf("expected label 'Test Claude', got %v", quotaEntry["label"])
	}

	quotaData, ok := quotaEntry["quota"].(map[string]any)
	if !ok {
		t.Fatal("expected quota object in entry")
	}
	if quotaData["unified_status"] != "ok" {
		t.Fatalf("expected unified_status 'ok', got %v", quotaData["unified_status"])
	}
}

func TestGetClaudeCodeQuota_MissingAuthID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{}

	h := &Handler{
		cfg:         &config.Config{},
		authManager: auth.NewManager(nil, nil, nil),
	}
	h.GetClaudeCodeQuota(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if response["error"] != "auth ID required" {
		t.Fatalf("expected error 'auth ID required', got %v", response["error"])
	}
}

func TestGetClaudeCodeQuota_NilAuthManager(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{gin.Param{Key: "authId", Value: "test-id"}}

	h := &Handler{
		cfg:         &config.Config{},
		authManager: nil,
	}
	h.GetClaudeCodeQuota(c)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", w.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if response["error"] != "auth manager not available" {
		t.Fatalf("expected error 'auth manager not available', got %v", response["error"])
	}
}

func TestGetClaudeCodeQuota_AuthNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{gin.Param{Key: "authId", Value: "nonexistent"}}

	h := &Handler{
		cfg:         &config.Config{},
		authManager: auth.NewManager(nil, nil, nil),
	}
	h.GetClaudeCodeQuota(c)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", w.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if response["error"] != "auth not found" {
		t.Fatalf("expected error 'auth not found', got %v", response["error"])
	}
}

func TestGetClaudeCodeQuota_NotClaudeProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{gin.Param{Key: "authId", Value: "gemini-1"}}

	manager := auth.NewManager(nil, nil, nil)
	geminiAuth := &auth.Auth{
		ID:       "gemini-1",
		Provider: "gemini",
		Metadata: map[string]any{
			"email": "test@example.com",
		},
	}
	_, _ = manager.Register(context.Background(), geminiAuth)

	h := &Handler{
		cfg:         &config.Config{},
		authManager: manager,
	}
	h.GetClaudeCodeQuota(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if response["error"] != "auth is not a Claude account" {
		t.Fatalf("expected error 'auth is not a Claude account', got %v", response["error"])
	}
}

func TestGetClaudeCodeQuota_NotOAuthAccount_NilMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{gin.Param{Key: "authId", Value: "claude-1"}}

	manager := auth.NewManager(nil, nil, nil)
	claudeAuth := &auth.Auth{
		ID:       "claude-1",
		Provider: "claude",
		Metadata: nil,
	}
	_, _ = manager.Register(context.Background(), claudeAuth)

	h := &Handler{
		cfg:         &config.Config{},
		authManager: manager,
	}
	h.GetClaudeCodeQuota(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if response["error"] != "not an OAuth account" {
		t.Fatalf("expected error 'not an OAuth account', got %v", response["error"])
	}
}

func TestGetClaudeCodeQuota_NotOAuthAccount_NoAccessToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{gin.Param{Key: "authId", Value: "claude-1"}}

	manager := auth.NewManager(nil, nil, nil)
	claudeAuth := &auth.Auth{
		ID:       "claude-1",
		Provider: "claude",
		Metadata: map[string]any{
			"email": "test@example.com",
		},
	}
	_, _ = manager.Register(context.Background(), claudeAuth)

	h := &Handler{
		cfg:         &config.Config{},
		authManager: manager,
	}
	h.GetClaudeCodeQuota(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if response["error"] != "not an OAuth account" {
		t.Fatalf("expected error 'not an OAuth account', got %v", response["error"])
	}
}

func TestGetClaudeCodeQuota_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{gin.Param{Key: "authId", Value: "claude-1"}}

	manager := auth.NewManager(nil, nil, nil)
	quota := &executor.ClaudeCodeQuotaInfo{
		UnifiedStatus:       "ok",
		FiveHourUtilization: 0.25,
		SevenDayUtilization: 0.5,
		LastUpdated:         time.Now(),
	}
	claudeAuth := &auth.Auth{
		ID:       "claude-1",
		Provider: "claude",
		Label:    "My Claude",
		Metadata: map[string]any{
			"access_token":      "test-token",
			"email":             "user@example.com",
			"claude_code_quota": quota,
		},
	}
	_, _ = manager.Register(context.Background(), claudeAuth)

	h := &Handler{
		cfg:         &config.Config{},
		authManager: manager,
	}
	h.GetClaudeCodeQuota(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if response["auth_id"] != "claude-1" {
		t.Fatalf("expected auth_id 'claude-1', got %v", response["auth_id"])
	}
	if response["email"] != "user@example.com" {
		t.Fatalf("expected email 'user@example.com', got %v", response["email"])
	}
	if response["label"] != "My Claude" {
		t.Fatalf("expected label 'My Claude', got %v", response["label"])
	}

	quotaData, ok := response["quota"].(map[string]any)
	if !ok {
		t.Fatal("expected quota object in response")
	}
	if quotaData["unified_status"] != "ok" {
		t.Fatalf("expected unified_status 'ok', got %v", quotaData["unified_status"])
	}
}

func TestRefreshClaudeCodeQuota_MissingAuthID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{}
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	h := &Handler{
		cfg:         &config.Config{},
		authManager: auth.NewManager(nil, nil, nil),
	}
	h.RefreshClaudeCodeQuota(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if response["error"] != "auth ID required" {
		t.Fatalf("expected error 'auth ID required', got %v", response["error"])
	}
}

func TestRefreshClaudeCodeQuota_NilAuthManager(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{gin.Param{Key: "authId", Value: "test-id"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	h := &Handler{
		cfg:         &config.Config{},
		authManager: nil,
	}
	h.RefreshClaudeCodeQuota(c)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", w.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if response["error"] != "auth manager not available" {
		t.Fatalf("expected error 'auth manager not available', got %v", response["error"])
	}
}

func TestRefreshClaudeCodeQuota_AuthNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{gin.Param{Key: "authId", Value: "nonexistent"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	h := &Handler{
		cfg:         &config.Config{},
		authManager: auth.NewManager(nil, nil, nil),
	}
	h.RefreshClaudeCodeQuota(c)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", w.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if response["error"] != "auth not found" {
		t.Fatalf("expected error 'auth not found', got %v", response["error"])
	}
}

func TestRefreshClaudeCodeQuota_NotClaudeProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{gin.Param{Key: "authId", Value: "gemini-1"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	manager := auth.NewManager(nil, nil, nil)
	geminiAuth := &auth.Auth{
		ID:       "gemini-1",
		Provider: "gemini",
		Metadata: map[string]any{
			"email": "test@example.com",
		},
	}
	_, _ = manager.Register(context.Background(), geminiAuth)

	h := &Handler{
		cfg:         &config.Config{},
		authManager: manager,
	}
	h.RefreshClaudeCodeQuota(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if response["error"] != "auth is not a Claude account" {
		t.Fatalf("expected error 'auth is not a Claude account', got %v", response["error"])
	}
}

func TestRefreshClaudeCodeQuota_NotOAuthAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{gin.Param{Key: "authId", Value: "claude-1"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	manager := auth.NewManager(nil, nil, nil)
	claudeAuth := &auth.Auth{
		ID:       "claude-1",
		Provider: "claude",
		Metadata: map[string]any{
			"email": "test@example.com",
		},
	}
	_, _ = manager.Register(context.Background(), claudeAuth)

	h := &Handler{
		cfg:         &config.Config{},
		authManager: manager,
	}
	h.RefreshClaudeCodeQuota(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if response["error"] != "not an OAuth account" {
		t.Fatalf("expected error 'not an OAuth account', got %v", response["error"])
	}
}

func TestRefreshClaudeCodeQuota_DefensiveMetadataInit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{gin.Param{Key: "authId", Value: "claude-1"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	manager := auth.NewManager(nil, nil, nil)
	// Auth with nil metadata but has access_token check will fail first
	claudeAuth := &auth.Auth{
		ID:       "claude-1",
		Provider: "claude",
		Metadata: nil,
	}
	_, _ = manager.Register(context.Background(), claudeAuth)

	h := &Handler{
		cfg:         &config.Config{},
		authManager: manager,
	}
	h.RefreshClaudeCodeQuota(c)

	// Should fail at OAuth check before defensive init
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}
