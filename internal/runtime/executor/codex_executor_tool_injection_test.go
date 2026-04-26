package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorImageGenerationToolInjectionAcrossRequestPaths(t *testing.T) {
	paths := []struct {
		name string
		run  codexCaptureRunner
	}{
		{name: "execute", run: runCodexExecuteAndCaptureBody},
		{name: "stream", run: runCodexStreamAndCaptureBody},
		{name: "compact", run: runCodexCompactAndCaptureBody},
	}

	cases := []struct {
		name      string
		model     string
		payload   []byte
		authAttrs map[string]string
		wantImage bool
		wantTools int
	}{
		{
			name:      "generic web search client does not inject",
			model:     "gpt-5.4",
			payload:   []byte(`{"model":"gpt-5.4","input":"Say ok","tools":[{"type":"web_search_preview"}]}`),
			wantImage: false,
			wantTools: 1,
		},
		{
			name:      "plain generic request does not inject",
			model:     "gpt-5.4",
			payload:   []byte(`{"model":"gpt-5.4","input":"Say ok"}`),
			wantImage: false,
			wantTools: 0,
		},
		{
			name:      "explicit image generation tool choice injects",
			model:     "gpt-5.4",
			payload:   []byte(`{"model":"gpt-5.4","input":"Draw a cat","tool_choice":{"type":"image_generation"}}`),
			wantImage: true,
			wantTools: 1,
		},
		{
			name:      "string image generation tool choice injects",
			model:     "gpt-5.4",
			payload:   []byte(`{"model":"gpt-5.4","input":"Draw a cat","tool_choice":"image_generation"}`),
			wantImage: true,
			wantTools: 1,
		},
		{
			name:      "allowed tools image generation tool choice injects",
			model:     "gpt-5.4",
			payload:   []byte(`{"model":"gpt-5.4","input":"Draw a cat","tool_choice":{"type":"allowed_tools","tools":[{"type":"image_generation"}]}}`),
			wantImage: true,
			wantTools: 1,
		},
		{
			name:      "existing image generation tool is preserved",
			model:     "gpt-5.4",
			payload:   []byte(`{"model":"gpt-5.4","input":"Draw a cat","tools":[{"type":"image_generation","output_format":"webp"}],"tool_choice":{"type":"image_generation"}}`),
			wantImage: true,
			wantTools: 1,
		},
		{
			name:      "free codex auth does not inject even with explicit tool choice",
			model:     "gpt-5.4",
			payload:   []byte(`{"model":"gpt-5.4","input":"Draw a cat","tool_choice":{"type":"image_generation"}}`),
			authAttrs: map[string]string{"plan_type": "free"},
			wantImage: false,
			wantTools: 0,
		},
		{
			name:      "spark model does not inject even with explicit tool choice",
			model:     "gpt-5.3-codex-spark",
			payload:   []byte(`{"model":"gpt-5.3-codex-spark","input":"Draw a cat","tool_choice":{"type":"image_generation"}}`),
			wantImage: false,
			wantTools: 0,
		},
	}

	for _, path := range paths {
		for _, tc := range cases {
			t.Run(path.name+"/"+tc.name, func(t *testing.T) {
				capturedBody := path.run(t, tc.model, tc.payload, tc.authAttrs)

				if gotImage := hasImageGenerationTool(capturedBody); gotImage != tc.wantImage {
					t.Fatalf("hasImageGenerationTool = %v, want %v; upstream body=%s", gotImage, tc.wantImage, string(capturedBody))
				}
				tools := gjson.GetBytes(capturedBody, "tools")
				if gotTools := len(tools.Array()); gotTools != tc.wantTools {
					t.Fatalf("tools len = %d, want %d; upstream body=%s", gotTools, tc.wantTools, string(capturedBody))
				}
				if strings.Contains(tc.name, "existing image generation") {
					if got := tools.Array()[0].Get("output_format").String(); got != "webp" {
						t.Fatalf("existing image_generation output_format = %q, want webp; upstream body=%s", got, string(capturedBody))
					}
				}
			})
		}
	}
}

type codexCaptureRunner func(t *testing.T, model string, payload []byte, authAttrs map[string]string) []byte

func runCodexExecuteAndCaptureBody(t *testing.T, model string, payload []byte, authAttrs map[string]string) []byte {
	t.Helper()
	server, bodyCh, errCh := newCodexCaptureServer(t, "/responses")
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := codexCaptureAuth(server.URL, authAttrs)

	_, errExec := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if errExec != nil {
		t.Fatalf("Execute error: %v", errExec)
	}
	return receiveCapturedCodexBody(t, bodyCh, errCh)
}

func runCodexStreamAndCaptureBody(t *testing.T, model string, payload []byte, authAttrs map[string]string) []byte {
	t.Helper()
	server, bodyCh, errCh := newCodexCaptureServer(t, "/responses")
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := codexCaptureAuth(server.URL, authAttrs)

	result, errExec := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream error: %v", errExec)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}
	return receiveCapturedCodexBody(t, bodyCh, errCh)
}

func runCodexCompactAndCaptureBody(t *testing.T, model string, payload []byte, authAttrs map[string]string) []byte {
	t.Helper()
	server, bodyCh, errCh := newCodexCaptureServer(t, "/responses/compact")
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := codexCaptureAuth(server.URL, authAttrs)

	_, errExec := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
		Stream:       false,
	})
	if errExec != nil {
		t.Fatalf("Execute compact error: %v", errExec)
	}
	return receiveCapturedCodexBody(t, bodyCh, errCh)
}

func newCodexCaptureServer(t *testing.T, wantPath string) (*httptest.Server, <-chan []byte, <-chan error) {
	t.Helper()
	bodyCh := make(chan []byte, 1)
	errCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPath {
			errCh <- fmt.Errorf("path = %q, want %q", r.URL.Path, wantPath)
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			errCh <- errRead
			http.Error(w, errRead.Error(), http.StatusInternalServerError)
			return
		}
		bodyCh <- append([]byte(nil), body...)

		switch wantPath {
		case "/responses/compact":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1775555723,\"status\":\"completed\",\"model\":\"gpt-5.4\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
		}
	}))
	return server, bodyCh, errCh
}

func codexCaptureAuth(baseURL string, attrs map[string]string) *cliproxyauth.Auth {
	authAttrs := map[string]string{
		"base_url": baseURL,
		"api_key":  "test",
	}
	for key, value := range attrs {
		authAttrs[key] = value
	}
	return &cliproxyauth.Auth{Provider: "codex", Attributes: authAttrs}
}

func receiveCapturedCodexBody(t *testing.T, bodyCh <-chan []byte, errCh <-chan error) []byte {
	t.Helper()
	select {
	case errRead := <-errCh:
		t.Fatalf("capture request body: %v", errRead)
	case capturedBody := <-bodyCh:
		if len(capturedBody) == 0 {
			t.Fatal("missing captured upstream request body")
		}
		return capturedBody
	default:
		t.Fatal("missing captured upstream request body")
	}
	return nil
}
