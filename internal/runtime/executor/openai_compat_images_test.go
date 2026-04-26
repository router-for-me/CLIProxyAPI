package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestOpenAICompatExecutorSupportsNativeImagesEndpoints(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})

	for _, endpoint := range []string{"images/generations", "/images/generations"} {
		if !executor.SupportsNativeImagesEndpoint(endpoint) {
			t.Fatalf("SupportsNativeImagesEndpoint(%q) = false, want true", endpoint)
		}
	}
	for _, endpoint := range []string{"responses", "images/edits"} {
		if executor.SupportsNativeImagesEndpoint(endpoint) {
			t.Fatalf("SupportsNativeImagesEndpoint(%q) = true, want false", endpoint)
		}
	}
}

func TestOpenAICompatExecutorNativeImagesGenerationsUsesImagesEndpoint(t *testing.T) {
	bodyCh := make(chan []byte, 1)
	pathCh := make(chan string, 1)
	userAgentCh := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pathCh <- r.URL.Path
		userAgentCh <- r.Header.Get("User-Agent")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		bodyCh <- append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"ok"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test-key",
	}}
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "upstream-image-model",
		Payload: []byte(`{"model":"alias-image","prompt":"draw"}`),
	}, cliproxyexecutor.Options{
		Alt:             "images/generations",
		OriginalRequest: []byte(`{"model":"alias-image","prompt":"draw"}`),
		SourceFormat:    sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if string(resp.Payload) != `{"created":1,"data":[{"b64_json":"ok"}]}` {
		t.Fatalf("payload = %s", string(resp.Payload))
	}
	if path := <-pathCh; path != "/images/generations" {
		t.Fatalf("path = %q, want /images/generations", path)
	}
	if userAgent := <-userAgentCh; userAgent != openAICompatUserAgent {
		t.Fatalf("User-Agent = %q, want %q", userAgent, openAICompatUserAgent)
	}
	var captured map[string]any
	if err := json.Unmarshal(<-bodyCh, &captured); err != nil {
		t.Fatalf("unmarshal captured body: %v", err)
	}
	if got := captured["model"]; got != "upstream-image-model" {
		t.Fatalf("model = %v, want upstream-image-model", got)
	}
	if got := captured["prompt"]; got != "draw" {
		t.Fatalf("prompt = %v, want draw", got)
	}
}

func TestOpenAICompatExecutorNativeImagesGenerationsAppliesPayloadRules(t *testing.T) {
	bodyCh := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		bodyCh <- append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"ok"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		Payload: config.PayloadConfig{
			Default: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "upstream-image-model", Protocol: "openai"}},
				Params: map[string]any{"background": "transparent"},
			}},
			Override: []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "alias-image", Protocol: "openai"}},
				Params: map[string]any{"quality": "high"},
			}},
			Filter: []config.PayloadFilterRule{{
				Models: []config.PayloadModelRule{{Name: "upstream-image-model", Protocol: "openai"}},
				Params: []string{"temporary"},
			}},
		},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test-key",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "upstream-image-model",
		Payload: []byte(`{"model":"alias-image","prompt":"draw","quality":"low","temporary":true}`),
	}, cliproxyexecutor.Options{
		Alt:             "images/generations",
		OriginalRequest: []byte(`{"model":"alias-image","prompt":"draw","quality":"low","temporary":true}`),
		SourceFormat:    sdktranslator.FromString("openai"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "alias-image",
		},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	var captured map[string]any
	if err := json.Unmarshal(<-bodyCh, &captured); err != nil {
		t.Fatalf("unmarshal captured body: %v", err)
	}
	if got := captured["model"]; got != "upstream-image-model" {
		t.Fatalf("model = %v, want upstream-image-model", got)
	}
	if got := captured["background"]; got != "transparent" {
		t.Fatalf("background = %v, want transparent", got)
	}
	if got := captured["quality"]; got != "high" {
		t.Fatalf("quality = %v, want high", got)
	}
	if _, ok := captured["temporary"]; ok {
		t.Fatalf("temporary field should be filtered, got %v", captured["temporary"])
	}
}
