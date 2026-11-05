package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/ollama"
)

func TestLiveOllamaManagementRoundTrip(t *testing.T) {
	if os.Getenv("OLLAMA_LIVE_TEST") != "1" {
		t.Skip("live management check disabled")
	}
	if os.Getenv("OLLAMA_API_KEY") == "" {
		_ = godotenv.Load("../../../../../.env", "../../../../.env")
	}
	key := os.Getenv("OLLAMA_API_KEY")
	if key == "" {
		t.Fatal("OLLAMA_API_KEY unavailable")
	}
	h := &Handler{cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "Live acceptance", BaseURL: "https://ollama.com/v1", APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: key}}}}}, ollamaQuota: ollama.NewQuotaService(), envSecret: "local-test-management", failedAttempts: make(map[string]*attemptInfo)}
	router := gin.New()
	group := router.Group("/v8/management", h.Middleware())
	group.GET("/quotas/ollama/connections", h.ListOllamaQuotaConnections)
	group.GET("/quotas/ollama", h.GetOllamaQuota)
	do := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Authorization", "Bearer local-test-management")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), key) {
			t.Fatal("credential exposed in management response")
		}
		if w.Code != 200 {
			t.Fatalf("management status %d", w.Code)
		}
		return w
	}
	var discovery struct {
		Connections []ollama.ConnectionInfo `json:"connections"`
	}
	if err := json.Unmarshal(do("/v8/management/quotas/ollama/connections").Body.Bytes(), &discovery); err != nil || len(discovery.Connections) != 1 {
		t.Fatal("discovery failed")
	}
	for _, rng := range []string{"24h", "7d", "30d"} {
		w := do("/v8/management/quotas/ollama?provider_id=" + discovery.Connections[0].ID + "&range=" + rng)
		var response ollama.QuotaResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Usage == nil || response.Balance == nil {
			t.Fatal("live management response incomplete")
		}
	}
	t.Log("authenticated management discovery and all ranges passed; no account data retained")
}

// newOllamaQuotaHandler wires a handler whose quota service points at the test
// upstream.
func newOllamaQuotaHandler(usageStatus int, usageBody string, balanceStatus int, balanceBody string) (*Handler, *httptest.Server) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/usage":
			if usageStatus == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "30")
			}
			w.WriteHeader(usageStatus)
			fmt.Fprint(w, usageBody)
		case "/api/balance":
			w.WriteHeader(balanceStatus)
			fmt.Fprint(w, balanceBody)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	client := ollama.NewClient()
	client.EndpointBase = server.URL

	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "ollama-cloud",
			BaseURL: "https://ollama.com/v1",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
				APIKey: "ollama-secret-key",
			}},
		}},
	}

	return &Handler{cfg: cfg, ollamaQuota: ollama.NewQuotaServiceWithClient(client, nil)}, server
}

func serveOllamaQuota(t *testing.T, h *Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	router.GET("/v8/management/quotas/ollama", h.GetOllamaQuota)
	recorder := httptest.NewRecorder()
	if strings.Contains(target, "provider_id=ollama-cloud") {
		target = strings.ReplaceAll(target, "provider_id=ollama-cloud", "provider_id="+h.ollamaQuota.Connections(h.cfg)[0].ID)
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	router.ServeHTTP(recorder, req)
	return recorder
}

func TestGetOllamaQuotaSuccess(t *testing.T) {
	t.Parallel()

	h, server := newOllamaQuotaHandler(
		http.StatusOK,
		`{"range":"7d","scope":"self","granularity":"day","from":"2026-10-29T00:00:00Z","until":"2026-11-05T00:00:00Z","totals":{"request_count":3,"usage_usd":1.5},"buckets":[{"from":"2026-11-04T00:00:00Z","until":"2026-11-05T00:00:00Z","partial":true,"request_count":1}]}`,
		http.StatusOK,
		`{"included":{"balance_usd":12.5,"allowance_usd":20,"period":{"from":"2026-11-01T00:00:00Z","until":"2026-12-01T00:00:00Z"}},"purchased":{"balance_usd":3.25}}`,
	)
	defer server.Close()

	recorder := serveOllamaQuota(t, h, "/v8/management/quotas/ollama?provider_id=ollama-cloud&range=7d&scope=self")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}

	var resp ollama.QuotaResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Provider != "ollama-cloud" || !strings.HasPrefix(resp.ConnectionID, "ollama-") {
		t.Fatalf("identity = %s/%s, want ollama-cloud", resp.Provider, resp.ConnectionID)
	}
	if resp.Usage == nil || resp.Balance == nil {
		t.Fatalf("sections = usage:%v balance:%v, want both", resp.Usage, resp.Balance)
	}
	if *resp.Balance.Included.BalanceUSD != 12.5 || *resp.Balance.Purchased.BalanceUSD != 3.25 {
		t.Fatalf("balance = %+v, want $12.50 included / $3.25 purchased", resp.Balance)
	}

	body := recorder.Body.String()
	if strings.Contains(body, "ollama-secret-key") {
		t.Fatal("response leaked the API key")
	}
	if strings.Contains(body, "https://ollama.com/v1") {
		t.Fatal("response leaked the configured base URL")
	}
}

func TestGetOllamaQuotaDefaults(t *testing.T) {
	t.Parallel()

	h, server := newOllamaQuotaHandler(
		http.StatusOK,
		`{"range":"7d","scope":"self","granularity":"day","from":"2026-11-01T00:00:00Z","until":"2026-11-05T00:00:00Z","totals":{"request_count":1},"buckets":[]}`,
		http.StatusOK,
		`{"included":{"balance_usd":1,"allowance_usd":2,"period":{"from":"2026-11-01T00:00:00Z","until":"2026-12-01T00:00:00Z"}},"purchased":{"balance_usd":0}}`,
	)
	defer server.Close()

	recorder := serveOllamaQuota(t, h, "/v8/management/quotas/ollama?provider_id=ollama-cloud")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}

	var resp ollama.QuotaResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Range != ollama.UsageRange7d || resp.Scope != ollama.UsageScopeSelf {
		t.Fatalf("defaults = %s/%s, want 7d/self", resp.Range, resp.Scope)
	}
}

func TestGetOllamaQuotaParameterValidation(t *testing.T) {
	t.Parallel()

	h, server := newOllamaQuotaHandler(http.StatusOK, `{}`, http.StatusOK, `{}`)
	defer server.Close()

	cases := []struct {
		name   string
		target string
	}{
		{"missing provider id", "/v8/management/quotas/ollama"},
		{"empty provider id", "/v8/management/quotas/ollama?provider_id=%20%20"},
		{"invalid range", "/v8/management/quotas/ollama?provider_id=ollama-cloud&range=1h"},
		{"invalid scope", "/v8/management/quotas/ollama?provider_id=ollama-cloud&scope=team"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recorder := serveOllamaQuota(t, h, tc.target)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestGetOllamaQuotaUnknownConnection(t *testing.T) {
	t.Parallel()

	h, server := newOllamaQuotaHandler(http.StatusOK, `{}`, http.StatusOK, `{}`)
	defer server.Close()

	recorder := serveOllamaQuota(t, h, "/v8/management/quotas/ollama?provider_id=does-not-exist")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "no Ollama Cloud connection") {
		t.Fatalf("body = %s, want not-found message", recorder.Body.String())
	}
}

func TestGetOllamaQuotaUpstreamUnauthorizedMapsTo502(t *testing.T) {
	t.Parallel()

	h, server := newOllamaQuotaHandler(
		http.StatusUnauthorized, `{"error":"Invalid credentials"}`,
		http.StatusUnauthorized, `{"error":"Invalid credentials"}`,
	)
	defer server.Close()

	recorder := serveOllamaQuota(t, h, "/v8/management/quotas/ollama?provider_id=ollama-cloud")
	// A 401 would trigger client logout handling; the condition must surface
	// as a gateway-side failure instead.
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "Invalid credentials") {
		t.Fatalf("body = %s, want sanitized upstream message", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "ollama-secret-key") {
		t.Fatal("error response leaked the API key")
	}
}

func TestGetOllamaQuotaUpstreamThrottledMapsTo503(t *testing.T) {
	t.Parallel()

	h, server := newOllamaQuotaHandler(
		http.StatusTooManyRequests, `{"error":"Too many requests"}`,
		http.StatusTooManyRequests, `{"error":"Too many requests"}`,
	)
	defer server.Close()

	recorder := serveOllamaQuota(t, h, "/v8/management/quotas/ollama?provider_id=ollama-cloud")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "retry after") {
		t.Fatalf("body = %s, want retry-after hint", recorder.Body.String())
	}
}

func TestGetOllamaQuotaPartialSuccess(t *testing.T) {
	t.Parallel()

	h, server := newOllamaQuotaHandler(
		http.StatusOK, `{"range":"7d","scope":"self","granularity":"day","from":"2026-11-01T00:00:00Z","until":"2026-11-05T00:00:00Z","totals":{"request_count":2},"buckets":[]}`,
		http.StatusForbidden, `{"error":"Account suspended"}`,
	)
	defer server.Close()

	recorder := serveOllamaQuota(t, h, "/v8/management/quotas/ollama?provider_id=ollama-cloud")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for partial success; body = %s", recorder.Code, recorder.Body.String())
	}

	var resp ollama.QuotaResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Usage == nil {
		t.Fatal("usage = nil, want populated")
	}
	if resp.Balance != nil {
		t.Fatal("balance = non-nil, want omitted")
	}
	if !strings.Contains(resp.BalanceError, "Account suspended") {
		t.Fatalf("balance_error = %q, want sanitized upstream message", resp.BalanceError)
	}
}

func TestOllamaQuotaDiscoveryRequiresManagementAuthAndOmitsSecrets(t *testing.T) {
	h, server := newOllamaQuotaHandler(http.StatusOK, `{}`, http.StatusOK, `{}`)
	defer server.Close()
	h.envSecret = "test-management-secret"
	h.failedAttempts = make(map[string]*attemptInfo)
	router := gin.New()
	router.GET("/connections", h.Middleware(), h.ListOllamaQuotaConnections)
	for _, authorized := range []bool{false, true} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/connections", nil)
		request.RemoteAddr = "127.0.0.1:12345"
		if authorized {
			request.Header.Set("Authorization", "Bearer test-management-secret")
		}
		router.ServeHTTP(recorder, request)
		if !authorized {
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated status = %d", recorder.Code)
			}
			continue
		}
		if recorder.Code != http.StatusOK {
			t.Fatalf("authenticated status = %d", recorder.Code)
		}
		if strings.Contains(recorder.Body.String(), "ollama-secret-key") || strings.Contains(recorder.Body.String(), "api_key") {
			t.Fatal("discovery leaked credential material")
		}
		if !strings.Contains(recorder.Body.String(), `"connection_id":"ollama-`) {
			t.Fatal("missing connection")
		}
	}
}
