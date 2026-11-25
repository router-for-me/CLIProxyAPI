package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestGeminiVertexStream_ImagenDoesNotPanicOnNilScanner(t *testing.T) {
	imagenResp := `{"predictions":[{"bytesBase64Encoded":"iVBORw0KGgo=","mimeType":"image/png"}]}`
	for _, provider := range []string{"api_key", "service_account"} {
		t.Run(provider, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/token" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, imagenResp)
			}))
			defer server.Close()

			ctx := context.Background()
			auth := &cliproxyauth.Auth{ID: t.Name()}
			if provider == "api_key" {
				auth.Attributes = map[string]string{"api_key": "test-key", "base_url": server.URL}
			} else {
				var serviceAccount map[string]any
				if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &serviceAccount); errUnmarshal != nil {
					t.Fatal(errUnmarshal)
				}
				auth.Metadata = map[string]any{
					"project_id":      "proxy-test",
					"location":        "global",
					"service_account": serviceAccount,
				}
				target, errParse := parseTestURL(t, server.URL)
				if errParse != nil {
					t.Fatal(errParse)
				}
				transport := http.DefaultTransport.(*http.Transport).Clone()
				transport.Proxy = nil
				t.Cleanup(transport.CloseIdleConnections)
				ctx = context.WithValue(ctx, "cliproxy.roundtripper", issue6258RedirectTransport{target: target, base: transport})
			}

			exec := NewGeminiVertexExecutor(&config.Config{})
			req := cliproxyexecutor.Request{
				Model:   "imagen-3.0-generate-002",
				Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"draw a cat"}]}]}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatGemini,
				Stream:       true,
			}

			streamResult, errStream := exec.ExecuteStream(ctx, auth, req, opts)
			if errStream != nil {
				t.Fatalf("ExecuteStream returned error: %v", errStream)
			}

			var payloads []string
			for chunk := range streamResult.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream chunk error: %v", chunk.Err)
				}
				if len(chunk.Payload) > 0 {
					payloads = append(payloads, string(chunk.Payload))
				}
			}

			combined := strings.Join(payloads, "\n")
			if !strings.Contains(combined, "iVBORw0KGgo=") {
				t.Fatalf("expected converted Imagen base64 payload, got: %s", combined)
			}
		})
	}
}
