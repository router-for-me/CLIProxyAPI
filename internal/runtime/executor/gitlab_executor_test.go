package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestGitLabExecutorRefresh_WithPATStoresGatewayMetadata(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/code_suggestions/direct_access" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer pat-123" {
			t.Fatalf("unexpected Authorization header %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"base_url":"` + server.URL + `",
			"token":"gateway-token",
			"expires_at":2000000000,
			"headers":{"X-Gitlab-Realm":"saas"},
			"model_details":{"model_provider":"mistral","model_name":"codestral-2501"}
		}`))
	}))
	defer server.Close()

	exec := NewGitLabExecutor(nil)
	auth := &cliproxyauth.Auth{
		ID:       "gitlab-pat.json",
		Provider: "gitlab",
		Metadata: map[string]any{
			"type":                  "gitlab",
			"auth_method":           "pat",
			"base_url":              server.URL,
			"personal_access_token": "pat-123",
		},
	}

	updated, err := exec.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}
	if got := metadataString(updated.Metadata, "duo_gateway_token"); got != "gateway-token" {
		t.Fatalf("unexpected gateway token %q", got)
	}
	if got := gitLabModelName(updated); got != "codestral-2501" {
		t.Fatalf("unexpected model name %q", got)
	}
	headers := gitLabHeaders(updated)
	if headers["X-Gitlab-Realm"] != "saas" {
		t.Fatalf("unexpected gateway headers %+v", headers)
	}
}

func TestGitLabExecutorExecute_UsesGatewayHeadersAndResolvedModel(t *testing.T) {
	var receivedAuth string
	var receivedRealm string
	var receivedModel string

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedAuth = r.Header.Get("Authorization")
		receivedRealm = r.Header.Get("X-Gitlab-Realm")
		receivedModel = findJSONField(string(body), `"model":"`, `"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer gateway.Close()

	exec := NewGitLabExecutor(nil)
	auth := &cliproxyauth.Auth{
		ID:       "gitlab-oauth.json",
		Provider: "gitlab",
		Metadata: map[string]any{
			"type":                 "gitlab",
			"auth_method":          "oauth",
			"duo_gateway_base_url": gateway.URL,
			"duo_gateway_token":    "gateway-token",
			"duo_gateway_headers":  map[string]any{"X-Gitlab-Realm": "saas"},
			"model_details":        map[string]any{"model_name": "codestral-2501", "model_provider": "mistral"},
		},
	}

	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gitlab-duo",
		Payload: []byte(`{"model":"gitlab-duo","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("expected non-empty payload")
	}
	if receivedAuth != "Bearer gateway-token" {
		t.Fatalf("unexpected Authorization header %q", receivedAuth)
	}
	if receivedRealm != "saas" {
		t.Fatalf("unexpected X-Gitlab-Realm header %q", receivedRealm)
	}
	if receivedModel != "codestral-2501" {
		t.Fatalf("unexpected resolved model %q", receivedModel)
	}
}

func findJSONField(body, prefix, suffix string) string {
	start := strings.Index(body, prefix)
	if start < 0 {
		return ""
	}
	start += len(prefix)
	end := strings.Index(body[start:], suffix)
	if end < 0 {
		return ""
	}
	return body[start : start+end]
}
