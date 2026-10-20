package helps

import (
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestIsVertexOpenAPIModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"deepseek-ai/deepseek-v4.1-flash-maas", true},
		{"deepseek-ai/deepseek-v4.1-flash-maas(high)", true},
		{"qwen/qwen3-coder-480b-a35b-instruct-maas", true},
		{"xai/grok-4-maas", true},
		{"publishers/deepseek-ai/models/deepseek-v4.1-flash-maas", true},
		{"publishers/google/models/gemini-2.5-pro", false},
		{"publishers/deepseek-ai/deepseek-v4.1-flash-maas", false},
		{"deepseek-ai/extra/segment", false},
		{"google/gemini-2.5-pro", false},
		{"models/gemini-2.5-pro", false},
		{"gemini-2.5-pro", false},
		{"imagen-3.0-generate-002", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := IsVertexOpenAPIModel(tc.model); got != tc.want {
			t.Errorf("IsVertexOpenAPIModel(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestNormalizeVertexOpenAPIModel(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"deepseek-ai/deepseek-v4.1-flash-maas", "deepseek-ai/deepseek-v4.1-flash-maas"},
		{"publishers/deepseek-ai/models/deepseek-v4.1-flash-maas", "deepseek-ai/deepseek-v4.1-flash-maas"},
		{"publishers/deepseek-ai/models/deepseek-v4.1-flash-maas(high)", "deepseek-ai/deepseek-v4.1-flash-maas(high)"},
		{"gemini-2.5-pro", "google/gemini-2.5-pro"},
		{"gemini-2.5-pro(8192)", "google/gemini-2.5-pro(8192)"},
		{"models/gemini-2.5-pro", "google/gemini-2.5-pro"},
		{"publishers/google/models/gemini-2.5-pro", "google/gemini-2.5-pro"},
	}
	for _, tc := range tests {
		if got := NormalizeVertexOpenAPIModel(tc.in); got != tc.want {
			t.Errorf("NormalizeVertexOpenAPIModel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestShouldExecuteVertexOpenAPI(t *testing.T) {
	if !ShouldExecuteVertexOpenAPI(nil, "deepseek-ai/deepseek-v4.1-flash-maas") {
		t.Fatal("expected automatic OpenAPI selection for deepseek-ai/deepseek-v4.1-flash-maas")
	}
	disabledAuth := &cliproxyauth.Auth{
		Metadata: map[string]any{"openapi": false},
	}
	if ShouldExecuteVertexOpenAPI(disabledAuth, "deepseek-ai/deepseek-v4.1-flash-maas") {
		t.Fatal("expected explicit openapi=false to disable OpenAPI routing")
	}
	enabledAuth := &cliproxyauth.Auth{
		Attributes: map[string]string{"openapi": "true"},
	}
	if !ShouldExecuteVertexOpenAPI(enabledAuth, "gemini-2.5-pro") {
		t.Fatal("expected explicit openapi=true to enable OpenAPI routing")
	}
}

func TestVertexOpenAPIBaseURLBuilders(t *testing.T) {
	if got := VertexServiceAccountOpenAPIBaseURL("", "v1", "my-proj", "global"); got != "https://aiplatform.googleapis.com/v1/projects/my-proj/locations/global/endpoints/openapi" {
		t.Fatalf("VertexServiceAccountOpenAPIBaseURL(global) = %q", got)
	}
	if got := VertexServiceAccountOpenAPIBaseURL("", "v1", "my-proj", "us-central1"); got != "https://us-central1-aiplatform.googleapis.com/v1/projects/my-proj/locations/us-central1/endpoints/openapi" {
		t.Fatalf("VertexServiceAccountOpenAPIBaseURL(us-central1) = %q", got)
	}
	if got := VertexAPIKeyOpenAPIBaseURL("https://example.com/v1/projects/my-proj/locations/global", "v1", "", ""); got != "https://example.com/v1/projects/my-proj/locations/global/endpoints/openapi" {
		t.Fatalf("VertexAPIKeyOpenAPIBaseURL(project/location URL) = %q", got)
	}
	if got := VertexAPIKeyOpenAPIBaseURL("https://example.com/api", "v1", "", ""); got != "https://example.com/api/v1/endpoints/openapi" {
		t.Fatalf("VertexAPIKeyOpenAPIBaseURL(relay URL) = %q", got)
	}
	if !IsGoogleVertexHost("") || !IsGoogleVertexHost("https://aiplatform.googleapis.com/v1") || !IsGoogleVertexHost("https://us-central1-aiplatform.googleapis.com") {
		t.Fatal("expected Google Vertex hosts to be detected")
	}
	if IsGoogleVertexHost("https://zenmux.ai/api") {
		t.Fatal("expected third-party host not to be detected as Google Vertex host")
	}
}
