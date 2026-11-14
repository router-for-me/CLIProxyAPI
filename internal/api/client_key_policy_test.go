package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestClientKeyPoliciesRejectHTTPAndWebsocket(t *testing.T) {
	server := newTestServerWithConfig(t, &config.Config{SDKConfig: config.SDKConfig{
		APIKeys:        []string{"restricted", "legacy"},
		APIKeyPolicies: []config.APIKeyPolicy{{APIKey: "restricted", Name: "test-client", BlockedModels: []string{"paid-*"}}},
	}})
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens", "/v1/chat/completions", "/v1/responses", "/v1/images/generations", "/backend-api/codex/alpha/search", "/v1beta/models/paid-model:generateContent"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"PAID-MODEL","stream":true}`))
		request.Header.Set("Authorization", "Bearer restricted")
		recorder := httptest.NewRecorder()
		server.engine.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s: status %d, want 403", path, recorder.Code)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"paid-model","messages":[]}`))
	request.Header.Set("Authorization", "Bearer legacy")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, request)
	if recorder.Code == http.StatusForbidden {
		t.Fatal("legacy key became restricted")
	}
	httpServer := httptest.NewServer(server.engine)
	defer httpServer.Close()
	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/v1/responses", http.Header{"Authorization": []string{"Bearer restricted"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	if err = connection.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"paid-model","input":[]}`)); err != nil {
		t.Fatal(err)
	}
	_, response, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(response), "not allowed") {
		t.Fatalf("websocket turn was not denied: %s", response)
	}
}
