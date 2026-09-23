package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestKimiAndKimiAIOAuthRoutes(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	server := newTestServer(t)

	wKimi := httptest.NewRecorder()
	server.engine.ServeHTTP(wKimi, httptest.NewRequest(http.MethodGet, "/v0/management/kimi-auth-url", nil))
	if wKimi.Code != http.StatusUnauthorized {
		t.Fatalf("unprotected kimi login route: %d", wKimi.Code)
	}

	wKimiAI := httptest.NewRecorder()
	server.engine.ServeHTTP(wKimiAI, httptest.NewRequest(http.MethodGet, "/v0/management/kimi-ai-auth-url", nil))
	if wKimiAI.Code != http.StatusUnauthorized {
		t.Fatalf("unprotected kimi-ai login route: %d", wKimiAI.Code)
	}

	registeredKimi := false
	registeredKimiAI := false
	for _, route := range server.engine.Routes() {
		if route.Method == http.MethodGet && route.Path == "/v0/management/kimi-auth-url" {
			registeredKimi = true
		}
		if route.Method == http.MethodGet && route.Path == "/v0/management/kimi-ai-auth-url" {
			registeredKimiAI = true
		}
	}
	if !registeredKimi {
		t.Fatal("Kimi login route not registered")
	}
	if !registeredKimiAI {
		t.Fatal("Kimi.ai login route not registered")
	}
}
