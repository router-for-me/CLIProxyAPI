package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

type nativeImagesRequestCapture struct {
	path          string
	authorization string
	userAgent     string
	body          []byte
	readErr       error
}

func TestOpenAICompatExecutorNativeImagesGenerations(t *testing.T) {
	captured := make(chan nativeImagesRequestCapture, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		captured <- nativeImagesRequestCapture{
			path:          r.URL.Path,
			authorization: r.Header.Get("Authorization"),
			userAgent:     r.Header.Get("User-Agent"),
			body:          body,
			readErr:       err,
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream-Test", "native-images")
		_, _ = w.Write([]byte(`{"created":123,"data":[{"b64_json":"img"}],"usage":{"total_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test-key",
	}}
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "upstream-image-model",
		Payload: []byte(`{"model":"client-image-model","prompt":"draw a cat","response_format":"b64_json"}`),
	}, cliproxyexecutor.Options{
		Alt:             "images/generations",
		OriginalRequest: []byte(`{"model":"client-image-model","prompt":"draw a cat","response_format":"b64_json"}`),
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if string(resp.Payload) != `{"created":123,"data":[{"b64_json":"img"}],"usage":{"total_tokens":1}}` {
		t.Fatalf("payload = %s", string(resp.Payload))
	}
	if resp.Headers.Get("X-Upstream-Test") != "native-images" {
		t.Fatalf("missing upstream header")
	}

	var got nativeImagesRequestCapture
	select {
	case got = <-captured:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for upstream request")
	}
	if got.readErr != nil {
		t.Fatalf("read request body: %v", got.readErr)
	}
	if got.path != "/v1/images/generations" {
		t.Fatalf("path = %q, want %q", got.path, "/v1/images/generations")
	}
	if got.authorization != "Bearer test-key" {
		t.Fatalf("authorization = %q", got.authorization)
	}
	if got.userAgent != "cli-proxy-openai-compat" {
		t.Fatalf("user agent = %q", got.userAgent)
	}
	if model := gjson.GetBytes(got.body, "model").String(); model != "upstream-image-model" {
		t.Fatalf("model = %q, want %q; body=%s", model, "upstream-image-model", string(got.body))
	}
	if prompt := gjson.GetBytes(got.body, "prompt").String(); prompt != "draw a cat" {
		t.Fatalf("prompt = %q", prompt)
	}
}
