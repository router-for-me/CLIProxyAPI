package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type geminiAppTransport func(*http.Request) (*http.Response, error)

func (f geminiAppTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func geminiAppTestAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{ID: "gemini-app-test", Provider: "gemini-app", Metadata: map[string]any{"cookies": map[string]any{"__Secure-1PSID": "test-session"}}}
}

func geminiAppTestFrame(text string, final bool) string {
	candidate := make([]any, 9)
	candidate[0], candidate[1], candidate[8] = "candidate", []any{text}, []any{1}
	if final {
		candidate[8] = []any{2}
	}
	inner, _ := json.Marshal([]any{nil, nil, nil, nil, []any{candidate}})
	outer, _ := json.Marshal([]any{[]any{"wrb.fr", nil, string(inner)}})
	return fmt.Sprintf("%d\n%s\n", len(outer), outer)
}

func geminiAppTestContext(t *testing.T, wire string, inspect func(*http.Request)) context.Context {
	t.Helper()
	transport := geminiAppTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "gemini.google.com" {
			t.Fatalf("unexpected host: %s", req.URL.Host)
		}
		header := make(http.Header)
		if req.URL.Path == "/app" {
			header.Add("Set-Cookie", "__Secure-1PSIDTS=rotated; Path=/; Secure; HttpOnly")
			return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(`{"SNlM0e":"csrf+token","cfb2h":"build","FdrFJe":"sid"}`))}, nil
		}
		if req.URL.Path != "/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate" {
			t.Fatalf("unexpected path: %s", req.URL.Path)
		}
		if req.Method != http.MethodPost {
			t.Fatal("expected POST")
		}
		if req.URL.Query().Get("bl") != "build" || req.URL.Query().Get("f.sid") != "sid" {
			t.Fatal("missing session routing tokens")
		}
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if req.Form.Get("at") != "csrf+token" {
			t.Fatal("missing form CSRF token")
		}
		if cookie, err := req.Cookie("__Secure-1PSIDTS"); err != nil || cookie.Value != "rotated" {
			t.Fatal("session cookie update was lost")
		}
		if inspect != nil {
			inspect(req)
		}
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(wire))}, nil
	})
	return context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
}

func TestGeminiAppExecutorProtocolResponses(t *testing.T) {
	requests := []struct {
		format sdktranslator.Format
		body   string
	}{
		{sdktranslator.FormatOpenAI, `{"model":"gemini-app","messages":[{"role":"user","content":"Hello"}]}`},
		{sdktranslator.FormatOpenAIResponse, `{"model":"gemini-app","input":[{"role":"user","content":[{"type":"input_text","text":"Hello"}]}]}`},
		{sdktranslator.FormatClaude, `{"model":"gemini-app","messages":[{"role":"user","content":"Hello"}],"max_tokens":100}`},
		{sdktranslator.FormatGemini, `{"contents":[{"role":"user","parts":[{"text":"Hello"}]}]}`},
	}
	for _, test := range requests {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", test.format, stream), func(t *testing.T) {
				ctx := geminiAppTestContext(t, ")]}'\n\n"+geminiAppTestFrame("Hel", false)+geminiAppTestFrame("Hello 🌏", true), nil)
				e := NewGeminiAppExecutor(&config.Config{})
				req := cliproxyexecutor.Request{Model: "gemini-app", Payload: []byte(test.body)}
				opts := cliproxyexecutor.Options{SourceFormat: test.format, OriginalRequest: req.Payload}
				var output string
				var responseID string
				var created int64
				if stream {
					result, err := e.ExecuteStream(ctx, geminiAppTestAuth(), req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						output += string(chunk.Payload)
						if test.format == sdktranslator.FormatOpenAI {
							metadata := gjson.ParseBytes(chunk.Payload)
							if metadata.Get("id").String() == "" || metadata.Get("model").String() != "gemini-app" || metadata.Get("created").Int() <= 0 {
								t.Fatalf("missing response metadata: %s", chunk.Payload)
							}
							if responseID != "" && (responseID != metadata.Get("id").String() || created != metadata.Get("created").Int()) {
								t.Fatal("response metadata changed between chunks")
							}
							responseID, created = metadata.Get("id").String(), metadata.Get("created").Int()
						}
					}
					if !strings.Contains(output, "Hel") || !strings.Contains(output, "lo 🌏") {
						t.Fatalf("missing text deltas: %s", output)
					}
				} else {
					result, err := e.Execute(ctx, geminiAppTestAuth(), req, opts)
					if err != nil {
						t.Fatal(err)
					}
					output = string(result.Payload)
					if !gjson.Valid(output) || !strings.Contains(output, "Hello 🌏") {
						t.Fatalf("invalid text response: %s", output)
					}
					if test.format == sdktranslator.FormatOpenAI || test.format == sdktranslator.FormatClaude {
						metadata := gjson.Parse(output)
						if metadata.Get("id").String() == "" || metadata.Get("model").String() != "gemini-app" {
							t.Fatalf("missing response metadata: %s", output)
						}
						if test.format == sdktranslator.FormatOpenAI && metadata.Get("created").Int() <= 0 {
							t.Fatalf("missing response timestamp: %s", output)
						}
					}
				}
				if strings.Contains(output, "csrf+token") || strings.Contains(output, "test-session") {
					t.Fatal("session material leaked downstream")
				}
				if stream && test.format == sdktranslator.FormatClaude && (!strings.Contains(output, `"model":"gemini-app"`) || !strings.Contains(output, `"id":"gemini-app-`)) {
					t.Fatalf("missing Claude message metadata: %s", output)
				}
			})
		}
	}
}

func TestGeminiAppPayloadRulesAreFinal(t *testing.T) {
	models := []config.PayloadModelRule{{Name: "gemini-app", Protocol: "gemini-app"}}
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{Models: models, Params: map[string]any{"0.0": "configured prompt", "45": 0, "7": 0}}},
		Filter:   []config.PayloadFilterRule{{Models: models, Params: []string{"80"}}},
	}}
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var sent bool
			ctx := geminiAppTestContext(t, geminiAppTestFrame("answer", true), func(req *http.Request) {
				sent = true
				outer := gjson.Parse(req.Form.Get("f.req"))
				body := gjson.Parse(outer.Get("1").String())
				if body.Get("0.0").String() != "configured prompt" || body.Get("45").Int() != 0 || body.Get("7").Int() != 0 || len(body.Array()) != 80 {
					t.Fatalf("payload rules overwritten by framing: %s", body.Raw)
				}
			})
			req := cliproxyexecutor.Request{Model: "gemini-app", Payload: []byte(`{"contents":[{"parts":[{"text":"original"}]}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}
			e := NewGeminiAppExecutor(cfg)
			if stream {
				result, err := e.ExecuteStream(ctx, geminiAppTestAuth(), req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else if _, err := e.Execute(ctx, geminiAppTestAuth(), req, opts); err != nil {
				t.Fatal(err)
			}
			if !sent {
				t.Fatal("upstream request was not sent")
			}
		})
	}
}

func TestGeminiAppDefaultPayloadRulesOverrideBuiltins(t *testing.T) {
	models := []config.PayloadModelRule{{Name: "gemini-app", Protocol: "gemini-app"}}
	cfg := &config.Config{Payload: config.PayloadConfig{
		Default:    []config.PayloadRule{{Models: models, Params: map[string]any{"0.0": "default prompt", "45": 0}}},
		DefaultRaw: []config.PayloadRule{{Models: models, Params: map[string]any{"7": "0"}}},
	}}
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			ctx := geminiAppTestContext(t, geminiAppTestFrame("answer", true), func(req *http.Request) {
				body := gjson.Parse(gjson.Get(req.Form.Get("f.req"), "1").String())
				if body.Get("0.0").String() != "default prompt" || body.Get("45").Int() != 0 || body.Get("7").Int() != 0 {
					t.Fatalf("built-in values blocked caller defaults: %s", body.Raw)
				}
			})
			req := cliproxyexecutor.Request{Model: "gemini-app", Payload: []byte(`{"contents":[{"parts":[{"text":"original"}]}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini, OriginalRequest: req.Payload}
			e := NewGeminiAppExecutor(cfg)
			if stream {
				result, err := e.ExecuteStream(ctx, geminiAppTestAuth(), req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else if _, err := e.Execute(ctx, geminiAppTestAuth(), req, opts); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGeminiAppTruncatedStreamFails(t *testing.T) {
	ctx := geminiAppTestContext(t, geminiAppTestFrame("partial", false), nil)
	e := NewGeminiAppExecutor(&config.Config{})
	req := cliproxyexecutor.Request{Model: "gemini-app", Payload: []byte(`{"contents":[{"parts":[{"text":"Hello"}]}]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}
	if _, err := e.Execute(ctx, geminiAppTestAuth(), req, opts); err == nil {
		t.Fatal("returned partial answer as success")
	}
	result, err := e.ExecuteStream(ctx, geminiAppTestAuth(), req, opts)
	if err != nil {
		t.Fatal(err)
	}
	var failed bool
	for chunk := range result.Chunks {
		failed = failed || chunk.Err != nil
	}
	if !failed {
		t.Fatal("truncated stream did not report an error")
	}
}

func TestGeminiAppStreamCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(geminiAppTestContext(t, geminiAppTestFrame("first", false)+geminiAppTestFrame("first second", true), nil))
	defer cancel()
	e := NewGeminiAppExecutor(&config.Config{})
	req := cliproxyexecutor.Request{Model: "gemini-app", Payload: []byte(`{"contents":[{"parts":[{"text":"Hello"}]}]}`)}
	result, err := e.ExecuteStream(ctx, geminiAppTestAuth(), req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini})
	if err != nil {
		t.Fatal(err)
	}
	<-result.Chunks
	cancel()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-result.Chunks:
			if !ok {
				return
			}
		case <-timer.C:
			t.Fatal("stream goroutine did not stop after cancellation")
		}
	}
}

func TestGeminiAppRejectsUnsupportedRequestsBeforeUpstream(t *testing.T) {
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(geminiAppTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("unsupported input reached upstream")
		return nil, nil
	})))
	e := NewGeminiAppExecutor(&config.Config{})
	for _, input := range []string{
		`{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"AA=="}}]}]}`,
		`{"tools":[{"functionDeclarations":[{"name":"tool"}]}],"contents":[{"parts":[{"text":"Hello"}]}]}`,
	} {
		_, err := e.Execute(ctx, geminiAppTestAuth(), cliproxyexecutor.Request{Model: "gemini-app", Payload: []byte(input)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini})
		if err == nil {
			t.Fatal("unsupported request accepted")
		}
		var scoped cliproxyexecutor.RequestScopedError
		if !errors.As(err, &scoped) || !scoped.IsRequestScoped() {
			t.Fatalf("unsupported request affects credential state: %v", err)
		}
	}
	request, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	if err := e.PrepareRequest(request, geminiAppTestAuth()); err == nil || request.Header.Get("Cookie") != "" {
		t.Fatal("cookies attached to unrelated host")
	}
}

func TestGeminiAppRejectsStructuredOutputBeforeUpstream(t *testing.T) {
	tests := []struct {
		name   string
		format sdktranslator.Format
		body   string
	}{
		{"OpenAI schema", sdktranslator.FormatOpenAI, `{"messages":[{"role":"user","content":"Hello"}],"response_format":{"type":"json_schema","json_schema":{"name":"result","schema":{"type":"object"}}}}`},
		{"OpenAI JSON", sdktranslator.FormatOpenAI, `{"messages":[{"role":"user","content":"Hello"}],"response_format":{"type":"json_object"}}`},
		{"Responses schema", sdktranslator.FormatOpenAIResponse, `{"input":"Hello","text":{"format":{"type":"json_schema","name":"result","schema":{"type":"object"}}}}`},
		{"Gemini MIME", sdktranslator.FormatGemini, `{"contents":[{"parts":[{"text":"Hello"}]}],"generationConfig":{"responseMimeType":"application/json"}}`},
		{"Gemini schema", sdktranslator.FormatGemini, `{"contents":[{"parts":[{"text":"Hello"}]}],"generationConfig":{"responseSchema":{"type":"OBJECT"}}}`},
		{"Gemini JSON schema", sdktranslator.FormatGemini, `{"contents":[{"parts":[{"text":"Hello"}]}],"generationConfig":{"responseJsonSchema":{"type":"object"}}}`},
	}
	for _, test := range tests {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", test.name, stream), func(t *testing.T) {
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(geminiAppTransport(func(*http.Request) (*http.Response, error) {
					t.Fatal("structured-output request reached upstream")
					return nil, nil
				})))
				ctx = cliproxyexecutor.WithUpstreamAttemptTracker(ctx)
				e := NewGeminiAppExecutor(&config.Config{})
				req := cliproxyexecutor.Request{Model: "gemini-app", Payload: []byte(test.body)}
				opts := cliproxyexecutor.Options{SourceFormat: test.format}
				var err error
				if stream {
					_, err = e.ExecuteStream(ctx, geminiAppTestAuth(), req, opts)
				} else {
					_, err = e.Execute(ctx, geminiAppTestAuth(), req, opts)
				}
				var scoped cliproxyexecutor.RequestScopedError
				var status interface{ StatusCode() int }
				if !errors.As(err, &scoped) || !scoped.IsRequestScoped() || !errors.As(err, &status) || status.StatusCode() != http.StatusBadRequest {
					t.Fatalf("expected request-scoped 400, got %v", err)
				}
				if cliproxyexecutor.UpstreamAttempted(ctx) {
					t.Fatal("local validation marked an upstream attempt")
				}
			})
		}
	}
}

func TestGeminiAppMarksFailedUpstreamAttempts(t *testing.T) {
	for _, failSession := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("session=%v/stream=%v", failSession, stream), func(t *testing.T) {
				failure := errors.New("upstream transport failed")
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(geminiAppTransport(func(req *http.Request) (*http.Response, error) {
					if !cliproxyexecutor.UpstreamAttempted(req.Context()) {
						t.Fatal("upstream attempt was not marked at transport boundary")
					}
					if failSession || req.URL.Path != "/app" {
						return nil, failure
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"SNlM0e":"token"}`))}, nil
				})))
				ctx = cliproxyexecutor.WithUpstreamAttemptTracker(ctx)
				e := NewGeminiAppExecutor(&config.Config{})
				req := cliproxyexecutor.Request{Model: "gemini-app", Payload: []byte(`{"contents":[{"parts":[{"text":"Hello"}]}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}
				var err error
				if stream {
					_, err = e.ExecuteStream(ctx, geminiAppTestAuth(), req, opts)
				} else {
					_, err = e.Execute(ctx, geminiAppTestAuth(), req, opts)
				}
				if !errors.Is(err, failure) || !cliproxyexecutor.UpstreamAttempted(ctx) {
					t.Fatalf("upstream failure lost: attempted=%v, err=%v", cliproxyexecutor.UpstreamAttempted(ctx), err)
				}
			})
		}
	}
}

func TestGeminiAppTTFTExcludesSessionFetch(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			capture := &multiProviderUsageCapture{alias: t.Name(), records: make(chan coreusage.Record, 4)}
			coreusage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{}) })
			coreusage.StartDefault(context.Background())
			synctest.Test(t, func(t *testing.T) {
				transport := geminiAppTransport(func(req *http.Request) (*http.Response, error) {
					body := geminiAppTestFrame("answer", true)
					if req.URL.Path == "/app" {
						time.Sleep(10 * time.Second) // Virtual time inside synctest.
						body = `{"SNlM0e":"token"}`
					} else {
						time.Sleep(250 * time.Millisecond)
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
				})
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
				ctx = coreusage.WithRequestedModelAlias(ctx, capture.alias)
				e := NewGeminiAppExecutor(&config.Config{})
				req := cliproxyexecutor.Request{Model: "gemini-app", Payload: []byte(`{"contents":[{"parts":[{"text":"Hello"}]}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}
				if stream {
					result, err := e.ExecuteStream(ctx, geminiAppTestAuth(), req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, err := e.Execute(ctx, geminiAppTestAuth(), req, opts); err != nil {
					t.Fatal(err)
				}
			})
			record := capture.await(t)
			if record.TTFT != 250*time.Millisecond || record.Latency != 10*time.Second+250*time.Millisecond || record.Failed {
				t.Fatalf("incorrect generation timing: TTFT=%v, latency=%v, failed=%v", record.TTFT, record.Latency, record.Failed)
			}
		})
	}
}
