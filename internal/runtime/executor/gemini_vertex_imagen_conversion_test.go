package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestGeminiVertexImagenPathsUsePredictProtocol(t *testing.T) {
	const requestBody = `{"contents":[{"role":"user","parts":[{"text":"a cat"}]}]}`
	const imagenResponse = `{"predictions":[{"bytesBase64Encoded":"aGVsbG8=","mimeType":"image/png"}]}`

	tests := []struct {
		name         string
		authMode     string
		stream       bool
		sourceFormat sdktranslator.Format
		requestBody  string
	}{
		{name: "api key non-stream", authMode: "api_key", stream: false, sourceFormat: sdktranslator.FormatGemini, requestBody: requestBody},
		{name: "api key non-stream OpenAI Responses", authMode: "api_key", stream: false, sourceFormat: sdktranslator.FormatOpenAIResponse, requestBody: `{"model":"imagen-3.0-generate-002","input":"a cat"}`},
		{name: "service account stream", authMode: "service_account", stream: true, sourceFormat: sdktranslator.FormatGemini, requestBody: requestBody},
		{name: "api key stream", authMode: "api_key", stream: true, sourceFormat: sdktranslator.FormatGemini, requestBody: requestBody},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			captured := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
					return
				}
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Errorf("read request body: %v", errRead)
					return
				}
				captured <- body
				if r.Method != http.MethodPost {
					t.Errorf("Imagen method = %s, want POST", r.Method)
				}
				if !strings.HasSuffix(r.URL.Path, "/imagen-3.0-generate-002:predict") {
					t.Errorf("Imagen path = %s, want :predict action", r.URL.Path)
				}
				if r.URL.RawQuery != "" {
					t.Errorf("Imagen URL query = %q, want empty", r.URL.RawQuery)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, imagenResponse)
			}))
			defer server.Close()

			ctx := context.Background()
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "vertex"}
			if tt.authMode == "api_key" {
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
			req := cliproxyexecutor.Request{Model: "imagen-3.0-generate-002", Payload: []byte(tt.requestBody)}
			opts := cliproxyexecutor.Options{
				Stream:          tt.stream,
				SourceFormat:    tt.sourceFormat,
				ResponseFormat:  tt.sourceFormat,
				OriginalRequest: []byte(tt.requestBody),
			}

			var output strings.Builder
			if tt.stream {
				result, errStream := exec.ExecuteStream(ctx, auth, req, opts)
				if errStream != nil {
					t.Fatalf("ExecuteStream() error = %v", errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("ExecuteStream() chunk error = %v", chunk.Err)
					}
					output.Write(chunk.Payload)
				}
			} else {
				response, errExecute := exec.Execute(ctx, auth, req, opts)
				if errExecute != nil {
					t.Fatalf("Execute() error = %v", errExecute)
				}
				output.Write(response.Payload)
			}

			sent := <-captured
			if ojson.GetBytesNoCopy(sent, "contents").Exists() {
				t.Fatalf("Imagen request contains Gemini contents: %s", sent)
			}
			if got := ojson.GetBytesNoCopy(sent, "instances.0.prompt").String(); got != "a cat" {
				t.Fatalf("instances[0].prompt = %q, want a cat; body=%s", got, sent)
			}
			if got := ojson.GetBytesNoCopy(sent, "parameters.sampleCount").Int(); got != 1 {
				t.Fatalf("parameters.sampleCount = %d, want 1; body=%s", got, sent)
			}
			if !strings.Contains(output.String(), "aGVsbG8=") {
				t.Fatalf("translated response does not contain Imagen bytes; output=%s", output.String())
			}
			terminalField := `"finishReason":"STOP"`
			if tt.sourceFormat == sdktranslator.FormatOpenAIResponse {
				terminalField = `"status":"completed"`
			}
			if !strings.Contains(output.String(), terminalField) {
				t.Fatalf("translated response does not contain terminal status %s; output=%s", terminalField, output.String())
			}
		})
	}
}

func TestGeminiVertexImagenPayloadRulesRemainFinal(t *testing.T) {
	const requestBody = `{"contents":[{"role":"user","parts":[{"text":"original"}]}]}`
	cfg := &config.Config{Payload: config.PayloadConfig{OverrideRaw: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "imagen-3.0-generate-002"}},
		Params: map[string]any{"parameters": `{"sampleCount":2}`},
	}}}}

	captured := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			return
		}
		captured <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"predictions":[{"bytesBase64Encoded":"aGVsbG8=","mimeType":"image/png"}]}`)
	}))
	defer server.Close()

	exec := NewGeminiVertexExecutor(cfg)
	auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "vertex", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
	req := cliproxyexecutor.Request{Model: "imagen-3.0-generate-002", Payload: []byte(requestBody)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini, ResponseFormat: sdktranslator.FormatGemini, OriginalRequest: []byte(requestBody)}
	if _, errExecute := exec.Execute(context.Background(), auth, req, opts); errExecute != nil {
		t.Fatal(errExecute)
	}

	sent := <-captured
	if got := ojson.GetBytesNoCopy(sent, "parameters.sampleCount").Int(); got != 2 {
		t.Fatalf("parameters.sampleCount = %d, want configured value 2; body=%s", got, sent)
	}
	if ojson.GetBytesNoCopy(sent, "contents").Exists() {
		t.Fatalf("Imagen payload contains contents after payload rules: %s", sent)
	}
}
