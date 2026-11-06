package management

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const recoveredCodexUsage = `{"account_id":"quota-account","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":0}},"model_usage":{"gpt-6-astra":{"available":true}}}`

func newQuotaRecoveryHandler(t *testing.T, provider string) (*Handler, *coreauth.Auth, coreauth.CooldownStateStore) {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	authDir := t.TempDir()
	store := coreauth.NewFileCooldownStateStoreWithAuthDir(authDir, authDir)
	manager.SetCooldownStateStore(store)
	auth := &coreauth.Auth{
		ID: t.Name(), FileName: "codex-test.json", Provider: provider, Status: coreauth.StatusActive,
		Metadata: map[string]any{"access_token": "quota-token", "account_id": "quota-account"},
	}
	auth.EnsureIndex()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{
		{ID: "gpt-6-astra"}, {ID: "gpt-6-luna"}, {ID: "gpt-6-sol"},
	})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(t.Context(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	manager.MarkResult(t.Context(), coreauth.Result{AuthID: auth.ID, Provider: provider, Model: "gpt-6-luna", Success: true})
	recordUsageLimit(t, manager, auth.ID, provider, "gpt-6-astra", true, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	return h, auth, store
}

func recordUsageLimit(t *testing.T, manager *coreauth.Manager, authID, provider, model string, credentialScope bool, headers http.Header) {
	t.Helper()
	week := 7 * 24 * time.Hour
	ctx := internallogging.WithResponseHeadersHolder(t.Context())
	internallogging.SetResponseHeaders(ctx, headers)
	manager.MarkResult(ctx, coreauth.Result{
		AuthID: authID, Provider: provider, Model: model, CredentialScope: credentialScope, RetryAfter: &week,
		Error: &coreauth.Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`},
	})
}

func useQuotaTestUpstream(t *testing.T, serve http.HandlerFunc) {
	t.Helper()
	server := httptest.NewTLSServer(serve)
	t.Cleanup(server.Close)
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = server.Certificate().DNSNames[0]
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() {
		http.DefaultTransport = previous
		transport.CloseIdleConnections()
	})
}

func callQuotaTestAPI(t *testing.T, h *Handler, auth *coreauth.Auth, upstreamURL string) *httptest.ResponseRecorder {
	t.Helper()
	recorder, ctx := newQuotaTestAPICall(t, auth, upstreamURL)
	h.APICall(ctx)
	return recorder
}

func newQuotaTestAPICall(t *testing.T, auth *coreauth.Auth, upstreamURL string) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	body, errMarshal := json.Marshal(apiCallRequest{
		AuthIndexSnake: &auth.Index, Method: http.MethodGet, URL: upstreamURL,
		Header: map[string]string{"Authorization": "Bearer $TOKEN$", "ChatGPT-Account-ID": "quota-account"},
	})
	if errMarshal != nil {
		t.Fatalf("marshal API call: %v", errMarshal)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v8/management/requests/api-call", strings.NewReader(string(body)))
	ctx.Request.Header.Set("Content-Type", "application/json")
	return recorder, ctx
}
