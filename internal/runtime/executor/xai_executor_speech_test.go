package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func newXAISpeechAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"base_url":  baseURL,
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{"access_token": "xai-token"},
	}
}

func TestXAIExecutorExecuteSpeechUsesTTSEndpoint(t *testing.T) {
	audio := []byte{0xFF, 0xFB, 0x90, 0x00, 0x01, 0x02}

	var gotPath, gotMethod, gotAuth, gotTokenAuth, gotClientVersion, gotAccept string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotTokenAuth = r.Header.Get(xaiTokenAuthHeader)
		gotClientVersion = r.Header.Get(xaiClientVersionHeader)
		gotAccept = r.Header.Get("Accept")
		var errRead error
		gotBody, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(audio)
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	payload := []byte(`{"text":"hello","voice_id":"eve","language":"en"}`)

	resp, err := exec.Execute(context.Background(), newXAISpeechAuth(server.URL), cliproxyexecutor.Request{
		Model:   "grok-tts",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-audio"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/tts",
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if gotPath != "/tts" {
		t.Fatalf("path = %q, want /tts", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotAuth != "Bearer xai-token" {
		t.Fatalf("Authorization = %q, want Bearer xai-token", gotAuth)
	}
	// Speech must use the official API headers. The Grok CLI chat headers would
	// mean the request went through the chat path, which has no /tts endpoint.
	if gotTokenAuth != "" {
		t.Fatalf("%s = %q, want empty on speech path", xaiTokenAuthHeader, gotTokenAuth)
	}
	if gotClientVersion != "" {
		t.Fatalf("%s = %q, want empty on speech path", xaiClientVersionHeader, gotClientVersion)
	}
	// Synthesis returns binary audio, so a JSON-only Accept would misdescribe it.
	if gotAccept != "*/*" {
		t.Fatalf("Accept = %q, want */*", gotAccept)
	}
	if !bytes.Equal(gotBody, payload) {
		t.Fatalf("body = %s, want %s", string(gotBody), string(payload))
	}
	if !bytes.Equal(resp.Payload, audio) {
		t.Fatalf("payload = %v, want %v", resp.Payload, audio)
	}
	if got := resp.Headers.Get("Content-Type"); got != "audio/mpeg" {
		t.Fatalf("Content-Type = %q, want audio/mpeg", got)
	}
}

// TestXAIExecutorExecuteSpeechUsesOfficialAPIBaseForOAuth guards the trap where an
// OAuth credential is routed through the chat helpers and pinned to the Grok CLI
// chat proxy, which has no /tts and would cool down the whole xAI auth pool.
func TestXAIExecutorExecuteSpeechUsesOfficialAPIBaseForOAuth(t *testing.T) {
	var gotHost string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte{0x00})
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	auth := newXAISpeechAuth(server.URL)

	if _, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-tts",
		Payload: []byte(`{"text":"hi","voice_id":"eve","language":"auto"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-audio"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/audio/speech",
		},
	}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if gotHost == "" {
		t.Fatal("speech request did not reach the configured base URL")
	}
}

func TestXAIExecutorExecuteSpeechVoicesUsesGet(t *testing.T) {
	var gotPath, gotMethod string
	var bodyLen int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		body, _ := io.ReadAll(r.Body)
		bodyLen = len(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"voices":[{"voice_id":"eve"}]}`))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	resp, err := exec.Execute(context.Background(), newXAISpeechAuth(server.URL), cliproxyexecutor.Request{
		Model: "grok-tts",
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-audio"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/tts/voices",
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if gotPath != "/tts/voices" {
		t.Fatalf("path = %q, want /tts/voices", gotPath)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("method = %q, want GET", gotMethod)
	}
	if bodyLen != 0 {
		t.Fatalf("voices request body length = %d, want 0", bodyLen)
	}
	if string(resp.Payload) != `{"voices":[{"voice_id":"eve"}]}` {
		t.Fatalf("payload = %s", string(resp.Payload))
	}
}

func TestXAIExecutorExecuteSpeechSurfacesUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer server.Close()

	exec := NewXAIExecutor(&config.Config{})
	_, err := exec.Execute(context.Background(), newXAISpeechAuth(server.URL), cliproxyexecutor.Request{
		Model:   "grok-tts",
		Payload: []byte(`{"text":"hi","voice_id":"eve","language":"auto"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-audio"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/tts",
		},
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want a 429 status error")
	}
	statusErrValue, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error %T does not expose a status code", err)
	}
	if statusErrValue.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", statusErrValue.StatusCode())
	}
}

func TestXAISpeechEndpointPath(t *testing.T) {
	cases := []struct {
		name        string
		sourceForma string
		requestPath string
		want        string
	}{
		{name: "audio speech", sourceForma: "openai-audio", requestPath: "/v1/audio/speech", want: xaiTTSPath},
		{name: "native tts", sourceForma: "openai-audio", requestPath: "/v1/tts", want: xaiTTSPath},
		{name: "voices", sourceForma: "openai-audio", requestPath: "/v1/tts/voices", want: xaiTTSVoicesPath},
		{name: "missing path defaults to tts", sourceForma: "openai-audio", requestPath: "", want: xaiTTSPath},
		{name: "image format ignored", sourceForma: "openai-image", requestPath: "/v1/tts", want: ""},
		{name: "chat format ignored", sourceForma: "openai", requestPath: "/v1/tts", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString(tc.sourceForma),
				Metadata: map[string]any{
					cliproxyexecutor.RequestPathMetadataKey: tc.requestPath,
				},
			}
			if got := xaiSpeechEndpointPath(opts); got != tc.want {
				t.Fatalf("xaiSpeechEndpointPath() = %q, want %q", got, tc.want)
			}
		})
	}
}
