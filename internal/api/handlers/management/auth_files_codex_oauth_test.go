package management

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestRequestCodexToken_WebUIExternalCallbackSkipsLocalForwarder(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	h := NewHandlerWithoutConfigFilePath(&config.Config{
		AuthDir: t.TempDir(),
		Port:    49794,
		RemoteManagement: config.RemoteManagement{
			ExternalBaseURL: "https://cpa.example.com",
		},
	}, nil)
	h.tokenStore = &memoryAuthStore{}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v0/management/codex-auth-url?is_webui=true", nil)
	req.Host = "cpa.example.com"
	ctx.Request = req

	h.RequestCodexToken(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var resp struct {
		Status string `json:"status"`
		URL    string `json:"url"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("status = %q, want %q", resp.Status, "ok")
	}
	if resp.State == "" {
		t.Fatalf("expected non-empty oauth state")
	}

	parsed, err := url.Parse(resp.URL)
	if err != nil {
		t.Fatalf("failed to parse auth url: %v", err)
	}
	if got := parsed.Query().Get("redirect_uri"); got != "https://cpa.example.com/codex/callback" {
		t.Fatalf("redirect_uri = %q, want %q", got, "https://cpa.example.com/codex/callback")
	}

	callbackForwardersMu.Lock()
	_, exists := callbackForwarders[codexCallbackPort]
	callbackForwardersMu.Unlock()
	if exists {
		t.Fatalf("expected no local codex callback forwarder for external webui requests")
	}

	CompleteOAuthSession(resp.State)
	time.Sleep(600 * time.Millisecond)
}

func TestRequestCodexToken_WebUILocalhostKeepsLocalForwarder(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	if callbackForwarderPortUnavailable(codexCallbackPort) {
		t.Skipf("skipping localhost forwarder test because 127.0.0.1:%d is unavailable in this environment", codexCallbackPort)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir(), Port: 49794}, nil)
	h.tokenStore = &memoryAuthStore{}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v0/management/codex-auth-url?is_webui=true", nil)
	req.Host = "127.0.0.1:49794"
	ctx.Request = req

	h.RequestCodexToken(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var resp struct {
		Status string `json:"status"`
		URL    string `json:"url"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	parsed, err := url.Parse(resp.URL)
	if err != nil {
		t.Fatalf("failed to parse auth url: %v", err)
	}
	if got := parsed.Query().Get("redirect_uri"); got != "http://localhost:1455/auth/callback" {
		t.Fatalf("redirect_uri = %q, want %q", got, "http://localhost:1455/auth/callback")
	}

	callbackForwardersMu.Lock()
	_, exists := callbackForwarders[codexCallbackPort]
	callbackForwardersMu.Unlock()
	if !exists {
		t.Fatalf("expected local codex callback forwarder for localhost webui requests")
	}

	CompleteOAuthSession(resp.State)
	time.Sleep(600 * time.Millisecond)

	callbackForwardersMu.Lock()
	_, exists = callbackForwarders[codexCallbackPort]
	callbackForwardersMu.Unlock()
	if exists {
		t.Fatalf("expected local codex callback forwarder to stop after session completion")
	}
}

func callbackForwarderPortUnavailable(port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return true
	}
	_ = ln.Close()
	return false
}
