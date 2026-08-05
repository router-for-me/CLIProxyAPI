package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestRefreshAntigravityOAuthAccessTokenPreservesConcurrentPaymentDisable(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"refreshed-access-token","expires_in":3600,"token_type":"Bearer"}`))
	}))
	defer server.Close()

	previousTokenURL := antigravityOAuthTokenURL
	antigravityOAuthTokenURL = server.URL
	defer func() { antigravityOAuthTokenURL = previousTokenURL }()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{
		QuotaExceeded: config.QuotaExceeded{OnPaymentRequired: "disable"},
	})
	auth := &coreauth.Auth{
		ID:       "antigravity-management-refresh.json",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":          "antigravity",
			"access_token":  "expired-access-token",
			"refresh_token": "refresh-token",
			"expired":       time.Now().Add(-time.Minute).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register: %v", errRegister)
	}
	staleRefreshAuth, okAuth := manager.GetByID(auth.ID)
	if !okAuth || staleRefreshAuth == nil {
		t.Fatal("registered auth missing")
	}

	handler := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	refreshDone := make(chan error, 1)
	go func() {
		_, errRefresh := handler.refreshAntigravityOAuthAccessToken(context.Background(), staleRefreshAuth)
		refreshDone <- errRefresh
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("management token refresh did not start")
	}

	manager.MarkResult(context.Background(), coreauth.Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "gemini-test",
		Error: &coreauth.Error{
			HTTPStatus: http.StatusPaymentRequired,
			Message:    "insufficient balance",
		},
	})
	current, okCurrent := manager.GetByID(auth.ID)
	if !okCurrent || current == nil || !current.Disabled || current.Status != coreauth.StatusDisabled {
		t.Fatalf("402 did not disable auth before refresh completion: %#v", current)
	}

	close(release)
	select {
	case errRefresh := <-refreshDone:
		if errRefresh != nil {
			t.Fatalf("refreshAntigravityOAuthAccessToken: %v", errRefresh)
		}
	case <-time.After(time.Second):
		t.Fatal("management token refresh did not finish")
	}

	current, okCurrent = manager.GetByID(auth.ID)
	if !okCurrent || current == nil {
		t.Fatal("auth missing after refresh")
	}
	if !current.Disabled || current.Status != coreauth.StatusDisabled {
		t.Fatalf("stale management refresh re-enabled payment-disabled auth: %#v", current)
	}
	if reason, _ := current.Metadata["disabled_reason"].(string); reason != "payment_required" {
		t.Fatalf("disabled_reason = %q, want payment_required", reason)
	}
	if got, _ := current.Metadata["access_token"].(string); got != "refreshed-access-token" {
		t.Fatalf("refreshed access token = %q, want refreshed-access-token", got)
	}
}
