package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestFetchOpenAIModels_UsesDefaultModelsPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer srv.Close()

	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"base_url": srv.URL,
			"api_key":  "k",
		},
	}

	models := FetchOpenAIModels(context.Background(), auth, &config.Config{}, "openai-compatibility")
	if len(models) != 1 || models[0].ID != "m1" {
		t.Fatalf("unexpected models result: %#v", models)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/models")
	}
}

func TestFetchOpenAIModels_UsesCustomModelsPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"data":[{"id":"glm-4.5"}]}`))
	}))
	defer srv.Close()

	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"base_url":        srv.URL,
			"api_key":         "k",
			"models_endpoint": "/api/coding/paas/v4/models",
		},
	}

	models := FetchOpenAIModels(context.Background(), auth, &config.Config{}, "openai-compatibility")
	if len(models) != 1 || models[0].ID != "glm-4.5" {
		t.Fatalf("unexpected models result: %#v", models)
	}
	if gotPath != "/api/coding/paas/v4/models" {
		t.Fatalf("path = %q, want %q", gotPath, "/api/coding/paas/v4/models")
	}
}
