package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestResolveOpenAIModelsURL(t *testing.T) {
	testCases := []struct {
		name    string
		baseURL string
		attrs   map[string]string
		want    string
	}{
		{
			name:    "RootBaseURLUsesV1Models",
			baseURL: "https://api.openai.com",
			want:    "https://api.openai.com/v1/models",
		},
		{
			name:    "VersionedBaseURLUsesModels",
			baseURL: "https://api.z.ai/api/coding/paas/v4",
			want:    "https://api.z.ai/api/coding/paas/v4/models",
		},
		{
			name:    "ModelsURLOverrideWins",
			baseURL: "https://api.z.ai/api/coding/paas/v4",
			attrs: map[string]string{
				"models_url": "https://custom.example.com/models",
			},
			want: "https://custom.example.com/models",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveOpenAIModelsURL(tc.baseURL, tc.attrs)
			if got != tc.want {
				t.Fatalf("resolveOpenAIModelsURL(%q) = %q, want %q", tc.baseURL, got, tc.want)
			}
		})
	}
}

func TestFetchOpenAIModels_UsesVersionedPath(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"data":[{"id":"z-ai-model"}]}`))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"base_url": server.URL + "/api/coding/paas/v4",
			"api_key":  "test-key",
		},
	}

	models := FetchOpenAIModels(context.Background(), auth, &config.Config{}, "openai-compatibility")
	if len(models) != 1 {
		t.Fatalf("expected one model, got %d", len(models))
	}
	if gotPath != "/api/coding/paas/v4/models" {
		t.Fatalf("got path %q, want %q", gotPath, "/api/coding/paas/v4/models")
	}
}
