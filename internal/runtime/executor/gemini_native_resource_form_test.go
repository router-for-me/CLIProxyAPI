package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// TestGeminiExecutorEmbeddingNormalizesResourceFormModel guards alias/router targets that
// resolve to a resource-form model: the upstream URL must not become models/models/<id>.
func TestGeminiExecutorEmbeddingNormalizesResourceFormModel(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embedding":{"values":[0.1]}}`))
	}))
	defer server.Close()

	cfg := &config.Config{}
	exec := NewGeminiExecutor(cfg)
	auth := &cliproxyauth.Auth{ID: "gemini-resource-form", Provider: "gemini", Attributes: map[string]string{"base_url": server.URL, "api_key": "test-key"}}
	if _, errExecute := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:    "models/gemini-embedding-001",
		Payload:  []byte(`{"content":{"parts":[{"text":"hi"}]}}`),
		Metadata: map[string]any{cliproxyexecutor.RequestActionMetadataKey: helps.GeminiEmbedContentAction},
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if strings.Contains(gotPath, "models/models/") || !strings.Contains(gotPath, "/models/gemini-embedding-001:embedContent") {
		t.Fatalf("upstream path = %q, want a single models/ segment", gotPath)
	}
}
