package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestGeminiVertexOpenAPI_APIKeyRequestSettings(t *testing.T) {
	for _, method := range []string{"execute", "stream", "count"} {
		for _, perModel := range []bool{true, false} {
			for _, enabled := range []bool{true, false} {
				t.Run(method+"/model="+strconv.FormatBool(perModel)+"/openapi="+strconv.FormatBool(enabled), func(t *testing.T) {
					paths := make(chan string, 1)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						paths <- r.URL.Path
						_, _ = io.Copy(io.Discard, r.Body)
						if strings.HasSuffix(r.URL.Path, ":countTokens") {
							w.Header().Set("Content-Type", "application/json")
							_, _ = w.Write([]byte(`{"totalTokens":3}`))
							return
						}
						body := `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`
						if enabled {
							body = `{"id":"test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
						}
						if method == "stream" {
							w.Header().Set("Content-Type", "text/event-stream")
							if enabled {
								body = `{"id":"test","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`
							}
							_, _ = w.Write([]byte("data: " + body + "\n\ndata: [DONE]\n\n"))
							return
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(body))
					}))
					defer server.Close()
					model := "custom-model"
					if !enabled {
						model = "partner/custom-model"
					}
					entry := config.VertexCompatKey{APIKey: "test-key", BaseURL: server.URL, OpenAPI: &enabled}
					if perModel {
						fallback := !enabled
						entry.OpenAPI = &fallback
						entry.Models = []config.VertexCompatModel{{Name: model, Alias: "public-model", OpenAPI: &enabled}}
					}
					exec := NewGeminiVertexExecutor(&config.Config{VertexCompatAPIKey: []config.VertexCompatKey{entry}})
					auth := &cliproxyauth.Auth{ID: "vertex-key", Provider: "vertex", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
					req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: []byte(`{"model":"public-model","messages":[{"role":"user","content":"hello"}]}`)}
					switch method {
					case "execute":
						if _, errExecute := exec.Execute(context.Background(), auth, req, opts); errExecute != nil {
							t.Fatal(errExecute)
						}
					case "stream":
						result, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
						if errStream != nil {
							t.Fatal(errStream)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					case "count":
						if _, errCount := exec.CountTokens(context.Background(), auth, req, opts); errCount != nil {
							t.Fatal(errCount)
						}
					}
					var gotPath string
					select {
					case gotPath = <-paths:
					default:
					}
					if enabled && method == "count" {
						if gotPath != "" {
							t.Fatalf("OpenAPI token counting unexpectedly called %q", gotPath)
						}
						return
					}
					if enabled {
						if gotPath != "/v1/endpoints/openapi/chat/completions" {
							t.Fatalf("OpenAPI path = %q", gotPath)
						}
					} else if !strings.Contains(gotPath, "/publishers/google/models/"+model+":") {
						t.Fatalf("native path = %q", gotPath)
					}
				})
			}
		}
	}
}

func TestGeminiVertexOpenAPI_ConfigLocationFallback(t *testing.T) {
	exec := NewGeminiVertexExecutor(&config.Config{VertexCompatAPIKey: []config.VertexCompatKey{{APIKey: "test-key", OpenAPILocation: " global "}}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key", "location": "us-central1"}}
	if got := exec.vertexOpenAPILocation(auth, ""); got != "global" {
		t.Fatalf("config location = %q, want global", got)
	}
	auth.Metadata = map[string]any{"openapi-location": " europe-west4 "}
	if got := exec.vertexOpenAPILocation(auth, ""); got != "europe-west4" {
		t.Fatalf("metadata location = %q, want europe-west4", got)
	}
	auth.Attributes["openapi_location"] = " us-east5 "
	if got := exec.vertexOpenAPILocation(auth, ""); got != "us-east5" {
		t.Fatalf("attribute location = %q, want us-east5", got)
	}

	// Verify full resource base-url with /locations/.../publishers/google/models honors openapi-location in actual HTTP path
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-loc","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	fullBase := server.URL + "/v1/projects/p/locations/us-central1/publishers/google/models"
	execWithFullBase := NewGeminiVertexExecutor(&config.Config{
		VertexCompatAPIKey: []config.VertexCompatKey{
			{
				APIKey:          "test-key",
				BaseURL:         fullBase,
				OpenAPILocation: "global",
			},
		},
	})
	authFullBase := &cliproxyauth.Auth{
		ID:       "vertex-key-full-base",
		Provider: "vertex",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": fullBase,
		},
	}
	if _, errExec := execWithFullBase.Execute(context.Background(), authFullBase, cliproxyexecutor.Request{
		Model:   "deepseek-ai/deepseek-v4.1-flash-maas",
		Payload: []byte(`{"model":"deepseek-ai/deepseek-v4.1-flash-maas","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	}); errExec != nil {
		t.Fatalf("Execute(fullBase with openapi-location=global) error = %v", errExec)
	}
	wantPath := "/v1/projects/p/locations/global/endpoints/openapi/chat/completions"
	if gotPath != wantPath {
		t.Fatalf("Execute(fullBase) path = %q, want %q", gotPath, wantPath)
	}

	// Verify full global base_url + regional location without openapi_location preserves /locations/global for Execute & ExecuteStream
	var saPaths []string
	saServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"mock-vertex-sa-token","token_type":"Bearer","expires_in":3600}`))
			return
		}
		saPaths = append(saPaths, r.URL.Path)
		if r.Header.Get("Accept") == "text/event-stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer saServer.Close()

	var sa map[string]any
	if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, saServer.URL+"/token"), &sa); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	saGlobalBaseAuth := &cliproxyauth.Auth{
		ID:       "vertex-sa-global-base",
		Provider: "vertex",
		Attributes: map[string]string{
			"base_url": saServer.URL + "/v1/projects/p/locations/global/endpoints/openapi",
		},
		Metadata: map[string]any{
			"project_id":      "p",
			"location":        "us-central1",
			"service_account": sa,
		},
	}
	saExec := NewGeminiVertexExecutor(&config.Config{})
	saReq := cliproxyexecutor.Request{
		Model:   "deepseek-ai/deepseek-v4.1-flash-maas",
		Payload: []byte(`{"model":"deepseek-ai/deepseek-v4.1-flash-maas","messages":[{"role":"user","content":"hi"}]}`),
	}
	if _, errExec := saExec.Execute(context.Background(), saGlobalBaseAuth, saReq, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	}); errExec != nil {
		t.Fatalf("SA Execute(global base_url) error = %v", errExec)
	}
	streamRes, errStream := saExec.ExecuteStream(context.Background(), saGlobalBaseAuth, saReq, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
		Stream:         true,
	})
	if errStream != nil {
		t.Fatalf("SA ExecuteStream(global base_url) error = %v", errStream)
	}
	for chunk := range streamRes.Chunks {
		if chunk.Err != nil {
			t.Fatalf("SA stream chunk error: %v", chunk.Err)
		}
	}
	if len(saPaths) != 2 || saPaths[0] != wantPath || saPaths[1] != wantPath {
		t.Fatalf("SA global base_url paths = %v, want [%q, %q]", saPaths, wantPath, wantPath)
	}
}
