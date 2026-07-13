package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestFetchModelsUsesConfiguredBaseURL(t *testing.T) {
	var gotPath string
	var gotClientVersion string
	var gotEdgeKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotClientVersion = r.URL.Query().Get("client_version")
		gotEdgeKey = r.Header.Get("X-Edgee-Api-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()

	_, count, errFetch := fetchModels(
		context.Background(),
		&coreauth.Auth{},
		"oauth-token",
		server.URL+"/codex/",
		"test-version",
		map[string]string{"x-edgee-api-key": "edge-key"},
	)
	if errFetch != nil {
		t.Fatalf("fetchModels() error = %v", errFetch)
	}
	if count != 0 {
		t.Fatalf("model count = %d, want 0", count)
	}
	if gotPath != "/codex/models" {
		t.Fatalf("request path = %q, want /codex/models", gotPath)
	}
	if gotClientVersion != "test-version" {
		t.Fatalf("client_version = %q, want test-version", gotClientVersion)
	}
	if gotEdgeKey != "edge-key" {
		t.Fatalf("X-Edgee-Api-Key = %q, want configured header", gotEdgeKey)
	}
}
