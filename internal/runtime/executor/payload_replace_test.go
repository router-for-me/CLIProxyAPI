package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestPayloadReplaceCodexTransports(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		for _, stream := range []bool{false, true} {
			t.Run(transport+map[bool]string{false: "/execute", true: "/stream"}[stream], func(t *testing.T) {
				captured := make(chan []byte, 1)
				completed := []byte(`{"type":"response.completed","response":{"id":"resp_replace","status":"completed","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if transport == "websocket" {
						conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
						if errUpgrade != nil {
							t.Error(errUpgrade)
							return
						}
						defer func() {
							if errClose := conn.Close(); errClose != nil {
								t.Error(errClose)
							}
						}()
						_, body, errRead := conn.ReadMessage()
						if errRead != nil {
							t.Error(errRead)
							return
						}
						captured <- body
						if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
							t.Error(errWrite)
						}
						return
					}
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
						return
					}
					captured <- body
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write(append(append([]byte("data: "), completed...), '\n', '\n'))
				}))
				defer server.Close()
				cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{
					Models: []config.PayloadModelRule{{Name: "*", Protocol: "codex", FromProtocol: "responses"}},
					Target: config.PayloadReplaceTarget{Role: "user", Positions: []int{1, 2}}, Replacements: []config.PayloadReplacement{{"hero": "hero!"}},
				}}}}
				var executor cliproxyauth.ProviderExecutor = NewCodexExecutor(cfg)
				if transport == "websocket" {
					executor = NewCodexWebsocketsExecutor(cfg)
				}
				auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "codex", Attributes: map[string]string{"api_key": "test", "base_url": server.URL, "plan_type": "pro"}}
				req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"hero"}]}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
				if stream {
					result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
					if errStream != nil {
						t.Fatal(errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
					t.Fatal(errExecute)
				}
				body := <-captured
				if got := ojson.GetBytes(body, "input.0.content.0.text").String(); got != "hero!" {
					t.Fatalf("replacement missing or repeated: %s", body)
				}
				if len(ojson.GetBytes(body, "input").Array()) != 1 {
					t.Fatalf("created nonexistent second user: %s", body)
				}
			})
		}
	}
}

func TestPayloadReplaceClaudeFinalBarrier(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{Models: []config.PayloadModelRule{{Name: "*", Protocol: "claude"}}, Target: config.PayloadReplaceTarget{Role: "user", Positions: []int{1, 2}}, Replacements: []config.PayloadReplacement{{"hero": "hero!"}}}}}}
	for _, stream := range []bool{false, true} {
		body := executeClaudeContextManagementRequest(t, cfg, []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hero"},{"role":"assistant","content":"hero"},{"role":"user","content":"hero"}]}`), stream)
		for _, index := range []string{"0", "2"} {
			content := ojson.GetBytes(body, "messages."+index+".content")
			matches := 0
			if content.Type == ojson.String && content.String() == "hero!" {
				matches++
			}
			if content.IsArray() {
				for _, block := range content.Array() {
					if block.Get("text").String() == "hero!" {
						matches++
					}
				}
			}
			if matches != 1 {
				t.Fatalf("user %s not replaced exactly once: %s", index, body)
			}
		}
		if got := ojson.GetBytes(body, "messages.1.content").String(); got != "hero" {
			t.Fatalf("assistant changed: %s", body)
		}
	}
}

func TestPayloadReplaceGeminiMergedUsers(t *testing.T) {
	for _, position := range []int{1, 2} {
		cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{
			Models: []config.PayloadModelRule{{Name: "*", Protocol: "gemini", FromProtocol: "claude"}},
			Target: config.PayloadReplaceTarget{Role: "user", Positions: []int{position}}, Replacements: []config.PayloadReplacement{{"hero": "hero!"}},
		}}}}
		executor := NewAIStudioExecutor(cfg, "aistudio", nil)
		req := cliproxyexecutor.Request{Model: "gemini-3.1-pro-preview", Payload: []byte(`{"messages":[{"role":"user","content":"hero"},{"role":"user","content":"hero"}]}`)}
		for _, stream := range []bool{false, true} {
			body, _, errTranslate := executor.translateRequest(context.Background(), req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}, stream)
			if errTranslate != nil {
				t.Fatal(errTranslate)
			}
			contents := ojson.GetBytes(body, "contents").Array()
			if len(contents) != 1 {
				t.Fatalf("expected adjacent users to merge: %s", body)
			}
			want := "hero"
			if position == 1 {
				want = "hero!"
			}
			parts := contents[0].Get("parts").Array()
			if len(parts) != 2 || parts[0].Get("text").String() != want || parts[1].Get("text").String() != want {
				t.Fatalf("selection must use final user positions (%d): %s", position, body)
			}
		}
	}
}

func TestPayloadReplaceCodexCountTokens(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Target: config.PayloadReplaceTarget{Role: "user", All: true}, Replacements: []config.PayloadReplacement{{"hero": strings.Repeat("replacement text ", 100)}}}}}}
	req := cliproxyexecutor.Request{Model: "gpt-5", Payload: []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hero"}]}]}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
	configured, errConfigured := NewCodexExecutor(cfg).CountTokens(context.Background(), nil, req, opts)
	if errConfigured != nil {
		t.Fatal(errConfigured)
	}
	baseline, errBaseline := NewCodexExecutor(&config.Config{}).CountTokens(context.Background(), nil, req, opts)
	if errBaseline != nil {
		t.Fatal(errBaseline)
	}
	if ojson.GetBytes(configured.Payload, "response.usage.input_tokens").Int() <= ojson.GetBytes(baseline.Payload, "response.usage.input_tokens").Int() {
		t.Fatalf("token count did not include replacement: %s vs %s", configured.Payload, baseline.Payload)
	}
}

func TestPayloadReplaceAntigravityRebuiltAttempts(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{Replace: []config.PayloadReplaceRule{{Models: []config.PayloadModelRule{{Name: "*", Protocol: "antigravity"}}, Target: config.PayloadReplaceTarget{Role: "user", Positions: []int{1, 2}}, Replacements: []config.PayloadReplacement{{"hero": "hero!"}}}}}}
	raw := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hero"}]}]}}`)
	req := cliproxyexecutor.Request{Model: "gemini-3.1-pro-preview", Payload: raw}
	ctx := helps.WithPayloadFinalizer(context.Background(), helps.NewPayloadFinalizer(cfg, "antigravity", req.Model, "antigravity", "request", raw, req, cliproxyexecutor.Options{}))
	executor := NewAntigravityExecutor(cfg)
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "test"}}
	for attempt := 0; attempt < 2; attempt++ {
		request, errBuild := executor.buildRequest(ctx, auth, "token", req.Model, raw, attempt == 1, "", "https://example.invalid", "session")
		if errBuild != nil {
			t.Fatal(errBuild)
		}
		body, errRead := io.ReadAll(request.Body)
		if errClose := request.Body.Close(); errClose != nil {
			t.Error(errClose)
		}
		if errRead != nil {
			t.Fatal(errRead)
		}
		if got := ojson.GetBytes(body, "request.contents.0.parts.0.text").String(); got != "hero!" {
			t.Fatalf("attempt %d replacement missing or accumulated: %s", attempt, body)
		}
	}
}
