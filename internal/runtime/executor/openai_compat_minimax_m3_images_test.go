package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorMiniMaxM3InlinesRemoteImageURL(t *testing.T) {
	oldFetch := fetchMiniMaxM3ImageURL
	var fetched []string
	fetchMiniMaxM3ImageURL = func(_ context.Context, rawURL string) (string, []byte, bool) {
		fetched = append(fetched, rawURL)
		return "image/png", []byte("png-bytes"), true
	}
	t.Cleanup(func() {
		fetchMiniMaxM3ImageURL = oldFetch
	})

	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("minimax", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL + "/v1",
		"api_key":     "test-key",
		"compat_kind": "minimax",
	}}

	payload := []byte(`{"model":"MiniMax-M3","messages":[{"role":"user","content":[{"type":"text","text":"inspect"},{"type":"image_url","image_url":{"url":"https://cdn.example.com/cat.png"}},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,AAAA"}}]}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "MiniMax-M3",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if len(fetched) != 1 || fetched[0] != "https://cdn.example.com/cat.png" {
		t.Fatalf("fetched URLs = %v, want only remote image", fetched)
	}
	if got := gjson.GetBytes(gotBody, "messages.0.content.1.image_url.url").String(); got != "data:image/png;base64,cG5nLWJ5dGVz" {
		t.Fatalf("image_url.url = %q, want inlined data URL; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "messages.0.content.2.image_url.url").String(); got != "data:image/jpeg;base64,AAAA" {
		t.Fatalf("existing data URL changed to %q; body=%s", got, string(gotBody))
	}
}

func TestSanitizeOpenAICompatHTTPRequestBodyMiniMaxM3InlinesRemoteImageURL(t *testing.T) {
	oldFetch := fetchMiniMaxM3ImageURL
	fetchMiniMaxM3ImageURL = func(_ context.Context, rawURL string) (string, []byte, bool) {
		if rawURL != "https://cdn.example.com/cat.png" {
			t.Fatalf("unexpected fetch URL %q", rawURL)
		}
		return "image/webp", []byte("webp-bytes"), true
	}
	t.Cleanup(func() {
		fetchMiniMaxM3ImageURL = oldFetch
	})

	payload := `{"model":"MiniMax-M3","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://cdn.example.com/cat.png"}}]}]}`
	req := httptest.NewRequest(http.MethodPost, "https://api.minimax.io/v1/chat/completions", strings.NewReader(payload))

	if err := sanitizeOpenAICompatHTTPRequestBody(req, openAICompatProfileForKind("minimax"), "https://api.minimax.io/v1"); err != nil {
		t.Fatalf("sanitizeOpenAICompatHTTPRequestBody() error = %v", err)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got := gjson.GetBytes(body, "messages.0.content.0.image_url.url").String(); got != "data:image/webp;base64,d2VicC1ieXRlcw==" {
		t.Fatalf("image_url.url = %q, want inlined data URL; body=%s", got, string(body))
	}
}

func TestOpenAICompatExecutorMiniMaxM3ImageInliningSkipsOtherModels(t *testing.T) {
	oldFetch := fetchMiniMaxM3ImageURL
	fetchCalls := 0
	fetchMiniMaxM3ImageURL = func(_ context.Context, rawURL string) (string, []byte, bool) {
		fetchCalls++
		return "image/png", []byte(rawURL), true
	}
	t.Cleanup(func() {
		fetchMiniMaxM3ImageURL = oldFetch
	})

	var gotBodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBodies = append(gotBodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("minimax", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL + "/v1",
		"api_key":     "test-key",
		"compat_kind": "minimax",
	}}

	for _, model := range []string{"MiniMax-M3-highspeed", "MiniMax-M2.7"} {
		payload := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://cdn.example.com/cat.png"}}]}]}`)
		if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   model,
			Payload: payload,
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")}); err != nil {
			t.Fatalf("Execute(%s) error = %v", model, err)
		}
	}

	if fetchCalls != 0 {
		t.Fatalf("fetch calls = %d, want 0", fetchCalls)
	}
	if len(gotBodies) != 2 {
		t.Fatalf("got %d bodies, want 2", len(gotBodies))
	}
	for i, body := range gotBodies {
		if got := gjson.GetBytes(body, "messages.0.content.0.image_url.url").String(); got != "https://cdn.example.com/cat.png" {
			t.Fatalf("body %d image_url.url = %q, want original URL; body=%s", i, got, string(body))
		}
	}
}

func TestInlineMiniMaxM3RemoteImageURLsLimitsImageCount(t *testing.T) {
	oldFetch := fetchMiniMaxM3ImageURL
	fetchCalls := 0
	fetchMiniMaxM3ImageURL = func(_ context.Context, rawURL string) (string, []byte, bool) {
		fetchCalls++
		return "image/jpeg", []byte(rawURL), true
	}
	t.Cleanup(func() {
		fetchMiniMaxM3ImageURL = oldFetch
	})

	payload := []byte(`{"model":"MiniMax-M3","messages":[{"role":"user","content":[
		{"type":"image_url","image_url":{"url":"https://cdn.example.com/1.jpg"}},
		{"type":"image_url","image_url":{"url":"https://cdn.example.com/2.jpg"}},
		{"type":"image_url","image_url":{"url":"https://cdn.example.com/3.jpg"}},
		{"type":"image_url","image_url":{"url":"https://cdn.example.com/4.jpg"}},
		{"type":"image_url","image_url":{"url":"https://cdn.example.com/5.jpg"}}
	]}]}`)
	out, changed := inlineMiniMaxM3RemoteImageURLs(context.Background(), payload, openAICompatProfileForKind("minimax"), "MiniMax-M3")
	if !changed {
		t.Fatal("expected payload to change")
	}
	if fetchCalls != miniMaxM3ImageInlineMaxImages {
		t.Fatalf("fetch calls = %d, want %d", fetchCalls, miniMaxM3ImageInlineMaxImages)
	}
	for i := 0; i < miniMaxM3ImageInlineMaxImages; i++ {
		path := "messages.0.content." + strconv.Itoa(i) + ".image_url.url"
		if got := gjson.GetBytes(out, path).String(); !strings.HasPrefix(got, "data:image/jpeg;base64,") {
			t.Fatalf("content %d was not inlined: %q; body=%s", i, got, string(out))
		}
	}
	if got := gjson.GetBytes(out, "messages.0.content.4.image_url.url").String(); got != "https://cdn.example.com/5.jpg" {
		t.Fatalf("fifth image = %q, want original URL; body=%s", got, string(out))
	}
}

func TestMiniMaxM3ImageURLAllowedBlocksPrivateTargets(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{raw: "https://example.com/cat.png", want: true},
		{raw: "http://127.0.0.1/cat.png", want: false},
		{raw: "http://10.0.0.1/cat.png", want: false},
		{raw: "http://localhost/cat.png", want: false},
		{raw: "file:///tmp/cat.png", want: false},
		{raw: "https://user:pass@example.com/cat.png", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			parsed, err := url.Parse(tt.raw)
			if err != nil {
				t.Fatalf("parse URL: %v", err)
			}
			if got := miniMaxM3ImageURLAllowed(parsed); got != tt.want {
				t.Fatalf("miniMaxM3ImageURLAllowed(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestFetchMiniMaxM3ImageURLDefaultBlocksLoopbackBeforeRequest(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png"))
	}))
	defer server.Close()

	if mediaType, data, ok := fetchMiniMaxM3ImageURLDefault(context.Background(), server.URL+"/cat.png"); ok {
		t.Fatalf("fetch succeeded unexpectedly: mediaType=%q data=%q", mediaType, string(data))
	}
	if called {
		t.Fatal("loopback image server should not have been requested")
	}
}
