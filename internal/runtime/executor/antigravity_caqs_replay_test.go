package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature/signaturetest"
	antigravityclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/antigravity/claude"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestAntigravityExecutorCAQSTwoTurnReplay(t *testing.T) {
	sig := signaturetest.AntigravityCAQS()
	previousCache, previousStrict := cache.SignatureCacheEnabled(), cache.SignatureBypassStrictMode()
	t.Cleanup(func() {
		cache.SetSignatureCacheEnabled(previousCache)
		cache.SetSignatureBypassStrictMode(previousStrict)
	})
	for _, mode := range []struct {
		name           string
		cached, strict bool
	}{{"cache", true, false}, {"bypass", false, false}, {"strict", false, true}} {
		t.Run(mode.name, func(t *testing.T) {
			cache.SetSignatureCacheEnabled(mode.cached)
			cache.SetSignatureBypassStrictMode(mode.strict)
			for _, streaming := range []bool{false, true} {
				t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
					model := "claude-opus-5-5-high"
					response := []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"checked calculation","thoughtSignature":"` + sig + `"},{"text":"34113"}]},"finishReason":"STOP"}],"modelVersion":"claude-opus-5-5-high","usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":15}}}`)
					requests := make(chan []byte, 2)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						b, errBody := io.ReadAll(r.Body)
						if errBody != nil {
							t.Error(errBody)
							w.WriteHeader(500)
							return
						}
						requests <- b
						if strings.Contains(r.URL.Path, "streamGenerateContent") {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprintf(w, "data: %s\n\n", response)
						} else {
							w.Header().Set("Content-Type", "application/json")
							_, _ = w.Write(response)
						}
					}))
					defer server.Close()
					executor := NewAntigravityExecutor(&config.Config{})
					auth := &cliproxyauth.Auth{ID: "caqs-replay-" + t.Name(), Metadata: map[string]any{"access_token": "test-token", "expired": time.Now().Add(time.Hour).Format(time.RFC3339), "project_id": "test-project"}, Attributes: map[string]string{"base_url": server.URL}}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatClaude, Stream: streaming}
					call := func(payload []byte) []byte {
						t.Helper()
						req := cliproxyexecutor.Request{Model: model, Payload: payload}
						if !streaming {
							res, errExecute := executor.Execute(context.Background(), auth, req, opts)
							if errExecute != nil {
								t.Fatal(errExecute)
							}
							return res.Payload
						}
						res, errExecute := executor.ExecuteStream(context.Background(), auth, req, opts)
						if errExecute != nil {
							t.Fatal(errExecute)
						}
						var out []byte
						for chunk := range res.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
							out = append(out, chunk.Payload...)
						}
						return out
					}
					first := []byte(`{"model":"claude-opus-5-5-high","max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"calculate"}]}`)
					downstream := call(first)
					<-requests
					var content []byte
					if streaming {
						var signatureOut, thinkingOut, textOut string
						for _, line := range bytes.Split(downstream, []byte("\n")) {
							if !bytes.HasPrefix(line, []byte("data:")) {
								continue
							}
							event := gjson.ParseBytes(bytes.TrimSpace(line[5:]))
							switch event.Get("delta.type").String() {
							case "signature_delta":
								signatureOut += event.Get("delta.signature").String()
							case "thinking_delta":
								thinkingOut += event.Get("delta.thinking").String()
							case "text_delta":
								textOut += event.Get("delta.text").String()
							}
						}
						if signatureOut != sig {
							t.Fatal("stream changed the original signature")
						}
						content = []byte(`[{"type":"thinking"},{"type":"text"}]`)
						content, _ = sjson.SetBytes(content, "0.thinking", thinkingOut)
						content, _ = sjson.SetBytes(content, "0.signature", signatureOut)
						content, _ = sjson.SetBytes(content, "1.text", textOut)
					} else {
						content = []byte(gjson.GetBytes(downstream, "content").Raw)
						if gjson.GetBytes(content, "0.signature").String() != sig {
							t.Fatal("nonstream changed original signature")
						}
					}
					second := []byte(`{"model":"claude-opus-5-5-high","max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"calculate"},{"role":"assistant","content":[]},{"role":"user","content":"plus 17"}]}`)
					second, _ = sjson.SetRawBytes(second, "messages.1.content", content)
					call(second)
					upstream := <-requests
					if gjson.GetBytes(upstream, "model").String() != model {
						t.Fatal("upstream model changed")
					}
					parts := gjson.GetBytes(upstream, "request.contents.1.parts").Array()
					if len(parts) < 2 || !parts[0].Get("thought").Bool() || parts[0].Get("text").String() != "checked calculation" || parts[0].Get("thoughtSignature").String() != sig {
						t.Fatal("second turn lost thinking or changed the original signature")
					}
					if mode.cached {
						const text = "caqs-cache-recovery-test"
						cache.CacheSignature(model, text, sig)
						t.Cleanup(func() { _ = cache.DeleteCachedSignatureRequired(context.Background(), model, text) })
						recovery := []byte(`{"model":"claude-opus-5-5-high","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"caqs-cache-recovery-test"}]},{"role":"user","content":"continue"}]}`)
						recovered := antigravityclaude.ConvertClaudeRequestToAntigravity(model, recovery, false)
						if gjson.GetBytes(recovered, "request.contents.0.parts.0.thoughtSignature").String() != sig {
							t.Fatal("cache recovery changed original signature")
						}
					}
				})
			}
		})
	}
}
