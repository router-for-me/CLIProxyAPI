package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestGeminiExecutorAltEscape(t *testing.T) {
	const okBody = `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`

	altCases := []struct {
		name string
		alt  string
	}{
		{name: "query injection ampersand equals", alt: "json&injected=1"},
		{name: "fragment and space", alt: "json#frag value"},
	}

	for _, tc := range altCases {
		t.Run(tc.name, func(t *testing.T) {
			wantQuery := "$alt=" + url.QueryEscape(tc.alt)

			t.Run("Execute", func(t *testing.T) {
				var mu sync.Mutex
				var gotRawQuery string
				var gotQuery url.Values
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					gotRawQuery = r.URL.RawQuery
					gotQuery = r.URL.Query()
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, okBody)
				}))
				defer server.Close()

				exec := NewGeminiExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{
					ID: "gemini-alt-exec",
					Attributes: map[string]string{
						"api_key":  "test-key",
						"base_url": server.URL,
					},
				}
				req := cliproxyexecutor.Request{
					Model:   "gemini-2.5-flash",
					Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
				}
				opts := cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FormatGemini,
					Alt:          tc.alt,
				}
				if _, errExecute := exec.Execute(context.Background(), auth, req, opts); errExecute != nil {
					t.Fatalf("Execute() error = %v", errExecute)
				}

				mu.Lock()
				rawQuery := gotRawQuery
				query := gotQuery
				mu.Unlock()
				if rawQuery != wantQuery {
					t.Fatalf("RawQuery = %q, want %q", rawQuery, wantQuery)
				}
				if query.Has("injected") {
					t.Fatalf("unexpected injected query param in %q", rawQuery)
				}
				if gotAlt := query.Get("$alt"); gotAlt != tc.alt {
					t.Fatalf("decoded $alt = %q, want %q", gotAlt, tc.alt)
				}
			})

			t.Run("ExecuteStream", func(t *testing.T) {
				var mu sync.Mutex
				var gotRawQuery string
				var gotQuery url.Values
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					gotRawQuery = r.URL.RawQuery
					gotQuery = r.URL.Query()
					mu.Unlock()
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: "+okBody+"\n\n")
				}))
				defer server.Close()

				exec := NewGeminiExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{
					ID: "gemini-alt-stream",
					Attributes: map[string]string{
						"api_key":  "test-key",
						"base_url": server.URL,
					},
				}
				req := cliproxyexecutor.Request{
					Model:   "gemini-2.5-flash",
					Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
				}
				opts := cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FormatGemini,
					Stream:       true,
					Alt:          tc.alt,
				}
				res, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
				if errStream != nil {
					t.Fatalf("ExecuteStream() error = %v", errStream)
				}
				for chunk := range res.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error = %v", chunk.Err)
					}
				}

				mu.Lock()
				rawQuery := gotRawQuery
				query := gotQuery
				mu.Unlock()
				if rawQuery != wantQuery {
					t.Fatalf("RawQuery = %q, want %q", rawQuery, wantQuery)
				}
				if query.Has("injected") {
					t.Fatalf("unexpected injected query param in %q", rawQuery)
				}
				if gotAlt := query.Get("$alt"); gotAlt != tc.alt {
					t.Fatalf("decoded $alt = %q, want %q", gotAlt, tc.alt)
				}
			})
		})
	}
}

func TestGeminiVertexExecutorAltEscape(t *testing.T) {
	const okBody = `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`

	altCases := []struct {
		name string
		alt  string
	}{
		{name: "query injection ampersand equals", alt: "json&injected=1"},
		{name: "fragment and space", alt: "json#frag value"},
	}

	for _, authMode := range []string{"api_key", "service_account"} {
		t.Run(authMode, func(t *testing.T) {
			for _, tc := range altCases {
				t.Run(tc.name, func(t *testing.T) {
					wantQuery := "$alt=" + url.QueryEscape(tc.alt)

					newServerAndAuth := func(t *testing.T, stream bool) (*httptest.Server, context.Context, *cliproxyauth.Auth, func() (string, url.Values)) {
						t.Helper()
						var mu sync.Mutex
						var gotRawQuery string
						var gotQuery url.Values
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							if r.URL.Path == "/token" {
								w.Header().Set("Content-Type", "application/json")
								_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
								return
							}
							mu.Lock()
							gotRawQuery = r.URL.RawQuery
							gotQuery = r.URL.Query()
							mu.Unlock()
							if stream || strings.Contains(r.URL.Path, "streamGenerateContent") {
								w.Header().Set("Content-Type", "text/event-stream")
								_, _ = io.WriteString(w, "data: "+okBody+"\n\n")
								return
							}
							w.Header().Set("Content-Type", "application/json")
							_, _ = io.WriteString(w, okBody)
						}))

						ctx := context.Background()
						auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "vertex"}
						if authMode == "api_key" {
							auth.Attributes = map[string]string{
								"api_key":  "test-key",
								"base_url": server.URL,
							}
						} else {
							var serviceAccount map[string]any
							if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &serviceAccount); errUnmarshal != nil {
								server.Close()
								t.Fatalf("unmarshal service account: %v", errUnmarshal)
							}
							auth.Metadata = map[string]any{
								"project_id":      "proxy-test",
								"location":        "global",
								"service_account": serviceAccount,
							}
							target, errParse := parseTestURL(t, server.URL)
							if errParse != nil {
								server.Close()
								t.Fatalf("parse server URL: %v", errParse)
							}
							transport := http.DefaultTransport.(*http.Transport).Clone()
							transport.Proxy = nil
							t.Cleanup(transport.CloseIdleConnections)
							ctx = context.WithValue(ctx, "cliproxy.roundtripper", issue6258RedirectTransport{target: target, base: transport})
						}

						getCaptured := func() (string, url.Values) {
							mu.Lock()
							defer mu.Unlock()
							return gotRawQuery, gotQuery
						}
						return server, ctx, auth, getCaptured
					}

					t.Run("Execute", func(t *testing.T) {
						server, ctx, auth, getCaptured := newServerAndAuth(t, false)
						defer server.Close()

						exec := NewGeminiVertexExecutor(&config.Config{})
						req := cliproxyexecutor.Request{
							Model:   "gemini-2.5-flash",
							Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
						}
						opts := cliproxyexecutor.Options{
							SourceFormat: sdktranslator.FormatGemini,
							Alt:          tc.alt,
						}
						if _, errExecute := exec.Execute(ctx, auth, req, opts); errExecute != nil {
							t.Fatalf("Execute() error = %v", errExecute)
						}

						rawQuery, query := getCaptured()
						if rawQuery != wantQuery {
							t.Fatalf("RawQuery = %q, want %q", rawQuery, wantQuery)
						}
						if query.Has("injected") {
							t.Fatalf("unexpected injected query param in %q", rawQuery)
						}
						if gotAlt := query.Get("$alt"); gotAlt != tc.alt {
							t.Fatalf("decoded $alt = %q, want %q", gotAlt, tc.alt)
						}
					})

					t.Run("ExecuteStream", func(t *testing.T) {
						server, ctx, auth, getCaptured := newServerAndAuth(t, true)
						defer server.Close()

						exec := NewGeminiVertexExecutor(&config.Config{})
						req := cliproxyexecutor.Request{
							Model:   "gemini-2.5-flash",
							Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
						}
						opts := cliproxyexecutor.Options{
							SourceFormat: sdktranslator.FormatGemini,
							Stream:       true,
							Alt:          tc.alt,
						}
						res, errStream := exec.ExecuteStream(ctx, auth, req, opts)
						if errStream != nil {
							t.Fatalf("ExecuteStream() error = %v", errStream)
						}
						for chunk := range res.Chunks {
							if chunk.Err != nil {
								t.Fatalf("stream chunk error = %v", chunk.Err)
							}
						}

						rawQuery, query := getCaptured()
						if rawQuery != wantQuery {
							t.Fatalf("RawQuery = %q, want %q", rawQuery, wantQuery)
						}
						if query.Has("injected") {
							t.Fatalf("unexpected injected query param in %q", rawQuery)
						}
						if gotAlt := query.Get("$alt"); gotAlt != tc.alt {
							t.Fatalf("decoded $alt = %q, want %q", gotAlt, tc.alt)
						}
					})
				})
			}
		})
	}
}
