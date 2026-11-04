package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/gin-gonic/gin"
	kiroauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestFetchCredentialQuota_Kiro(t *testing.T) {
	gin.SetMode(gin.TestMode)

	resetTime := 1800000000.0
	limit := 200.0
	used := 50.0

	mockKiroServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/getUsageLimits" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/cbor")
		resp := kiroauth.UsageQuotaResponse{
			NextDateReset: &resetTime,
			SubscriptionInfo: &kiroauth.SubscriptionInfo{
				SubscriptionTitle: "Kiro Builder",
			},
			UsageBreakdownList: []kiroauth.UsageBreakdown{
				{
					DisplayName:               "Agentic Requests",
					ResourceType:              "AGENTIC_REQUEST",
					UsageLimitWithPrecision:   &limit,
					CurrentUsageWithPrecision: &used,
					NextDateReset:             &resetTime,
				},
			},
		}
		_ = cbor.NewEncoder(w).Encode(resp)
	}))
	defer mockKiroServer.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "kiro-test-credential.json",
		FileName: "kiro-test-credential.json",
		Provider: "kiro",
		Attributes: map[string]string{
			"base_url": mockKiroServer.URL,
		},
		Metadata: map[string]any{
			"type":          "kiro",
			"access_token":  "mock-access-token",
			"refresh_token": "mock-refresh-token",
			"client_id":     "mock-client-id",
			"client_secret": "mock-client-secret",
			"auth_method":   "builder-id",
			"base_url":      mockKiroServer.URL,
		},
	}
	authIndex := auth.EnsureIndex()
	_, _ = manager.Register(context.Background(), auth)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.SetPluginHost(pluginhost.New())

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/quota/fetch", strings.NewReader(`{"auth_index":"`+authIndex+`"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.FetchCredentialQuota(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var quotaResp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &quotaResp); err != nil {
		t.Fatalf("failed to decode quota response: %v", err)
	}

	subscription, ok := quotaResp["subscription"].(map[string]any)
	if !ok || subscription["plan"] != "Kiro Builder" {
		t.Errorf("plan = %v, want Kiro Builder", quotaResp["subscription"])
	}

	groups, ok := quotaResp["groups"].([]any)
	if !ok || len(groups) != 1 {
		t.Fatalf("groups = %v, want 1 group", quotaResp["groups"])
	}
}

func TestFetchPluginQuota_KiroEnterprise(t *testing.T) {
	gin.SetMode(gin.TestMode)

	resetTime := 1800000000.0
	limit := 300.0
	used := 120.0

	mockKiroServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/getUsageLimits" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/cbor")
		resp := kiroauth.UsageQuotaResponse{
			NextDateReset: &resetTime,
			SubscriptionInfo: &kiroauth.SubscriptionInfo{
				SubscriptionTitle: "Enterprise",
			},
			UsageBreakdownList: []kiroauth.UsageBreakdown{
				{
					DisplayName:               "Agentic Requests",
					ResourceType:              "AGENTIC_REQUEST",
					UsageLimitWithPrecision:   &limit,
					CurrentUsageWithPrecision: &used,
					NextDateReset:             &resetTime,
				},
			},
		}
		_ = cbor.NewEncoder(w).Encode(resp)
	}))
	defer mockKiroServer.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "kiro-enterprise-Url2Sju6N23x0xF0KWvaFXVzLWVhc3QtMQ.json",
		FileName: "kiro-enterprise-Url2Sju6N23x0xF0KWvaFXVzLWVhc3QtMQ.json",
		Provider: "kiro",
		Attributes: map[string]string{
			"base_url": mockKiroServer.URL,
		},
		Metadata: map[string]any{
			"type":          "kiro",
			"access_token":  "enterprise-access-token",
			"refresh_token": "enterprise-refresh-token",
			"client_id":     "enterprise-client-id",
			"client_secret": "enterprise-client-secret",
			"auth_method":   "idc",
			"base_url":      mockKiroServer.URL,
		},
	}
	authIndex := auth.EnsureIndex()
	_, _ = manager.Register(context.Background(), auth)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	h.SetPluginHost(pluginhost.New())

	recPost := httptest.NewRecorder()
	cPost, _ := gin.CreateTestContext(recPost)
	cPost.Params = gin.Params{{Key: "id", Value: "kiro"}}
	cPost.Request = httptest.NewRequest(http.MethodPost, "/plugins/kiro/quota", strings.NewReader(`{"auth_index":"`+authIndex+`"}`))
	cPost.Request.Header.Set("Content-Type", "application/json")

	h.FetchPluginQuota(cPost)

	if recPost.Code != http.StatusOK {
		t.Fatalf("FetchPluginQuota expected status 200, got %d: %s", recPost.Code, recPost.Body.String())
	}

	var quotaResp map[string]any
	if err := json.Unmarshal(recPost.Body.Bytes(), &quotaResp); err != nil {
		t.Fatalf("failed to decode quota response: %v", err)
	}

	subscription, ok := quotaResp["subscription"].(map[string]any)
	if !ok || subscription["plan"] != "Enterprise" {
		t.Errorf("plan = %v, want Enterprise", quotaResp["subscription"])
	}

	recGet := httptest.NewRecorder()
	cGet, _ := gin.CreateTestContext(recGet)
	cGet.Params = gin.Params{{Key: "id", Value: "kiro"}}
	cGet.Request = httptest.NewRequest(http.MethodGet, "/plugins/kiro/quota?auth_index="+authIndex, nil)

	h.GetPluginQuota(cGet)
	if recGet.Code != http.StatusOK {
		t.Fatalf("GetPluginQuota expected status 200, got %d: %s", recGet.Code, recGet.Body.String())
	}

	recReset := httptest.NewRecorder()
	cReset, _ := gin.CreateTestContext(recReset)
	cReset.Params = gin.Params{{Key: "id", Value: "kiro"}}
	cReset.Request = httptest.NewRequest(http.MethodPost, "/plugins/kiro/quota/reset", strings.NewReader(`{"auth_index":"`+authIndex+`"}`))
	cReset.Request.Header.Set("Content-Type", "application/json")

	h.ResetPluginQuota(cReset)
	if recReset.Code != http.StatusOK {
		t.Fatalf("ResetPluginQuota expected status 200, got %d: %s", recReset.Code, recReset.Body.String())
	}
}

func TestKiroAuthFiles_SupportsQuota(t *testing.T) {
	auth := &coreauth.Auth{
		ID:         "kiro-enterprise-Url2Sju6N23x0xF0KWvaFXVzLWVhc3QtMQ.json",
		FileName:   "kiro-enterprise-Url2Sju6N23x0xF0KWvaFXVzLWVhc3QtMQ.json",
		Provider:   "kiro",
		Attributes: map[string]string{"runtime_only": "true"},
		Metadata: map[string]any{
			"type":         "kiro",
			"access_token": "token-1",
		},
	}

	h := &Handler{}
	entryV0 := h.buildAuthFileEntryLocked(auth, authFileQuotaContext{v8: false})
	if supports, ok := entryV0["supports_quota"].(bool); !ok || !supports {
		t.Errorf("v0 supports_quota = %v, want true", entryV0["supports_quota"])
	}
	if qp, ok := entryV0["quota_provider"].(string); !ok || qp != "kiro" {
		t.Errorf("v0 quota_provider = %v, want kiro", entryV0["quota_provider"])
	}

	entryV8 := h.buildAuthFileEntryLocked(auth, authFileQuotaContext{v8: true})
	if supports, ok := entryV8["supports_quota"].(bool); !ok || !supports {
		t.Errorf("v8 supports_quota = %v, want true", entryV8["supports_quota"])
	}
	if pluginID, ok := entryV8["quota_plugin_id"].(string); !ok || pluginID != "kiro" {
		t.Errorf("v8 quota_plugin_id = %v, want kiro", entryV8["quota_plugin_id"])
	}
}

func TestAPICall_CBOR_Decoding(t *testing.T) {
	gin.SetMode(gin.TestMode)

	type testPayload struct {
		Message string `json:"message"`
		Count   int    `json:"count"`
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/cbor")
		payload := testPayload{Message: "hello cbor", Count: 42}
		_ = cbor.NewEncoder(w).Encode(payload)
	}))
	defer upstream.Close()

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir(), Port: 8317}, nil)

	reqData := apiCallRequest{
		Method: "GET",
		URL:    upstream.URL,
		Header: map[string]string{
			"Accept": "application/cbor",
		},
	}
	reqJSON, _ := json.Marshal(reqData)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(string(reqJSON)))
	c.Request.Header.Set("Content-Type", "application/json")

	h.APICall(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("APICall failed with status %d: %s", rec.Code, rec.Body.String())
	}

	var resp apiCallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode APICall response: %v", err)
	}

	var decoded testPayload
	if err := json.Unmarshal([]byte(resp.Body), &decoded); err != nil {
		t.Fatalf("expected resp.Body to be decoded JSON string, got error: %v, body: %q", err, resp.Body)
	}

	if decoded.Message != "hello cbor" || decoded.Count != 42 {
		t.Errorf("decoded = %+v, want Message='hello cbor', Count=42", decoded)
	}
}
