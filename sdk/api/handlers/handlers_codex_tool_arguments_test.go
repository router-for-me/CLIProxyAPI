package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestGetContextWithCancelCodexToolArgumentScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"/v1/responses", "/backend-api/codex/responses"} {
		for _, websocket := range []bool{false, true} {
			for _, ua := range []string{"codex-tui/0.159.0", "Codex-Desktop/0.159.0", "curl/8.7.1", ""} {
				t.Run(path+"/"+ua+map[bool]string{false: "/http", true: "/websocket"}[websocket], func(t *testing.T) {
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, path, nil)
					c.Request.Header.Set("User-Agent", ua)
					if websocket {
						c.Request.Header.Set("Connection", "Upgrade")
						c.Request.Header.Set("Upgrade", "websocket")
					}
					h := &BaseAPIHandler{Cfg: &config.SDKConfig{}}
					ctx, cancel := h.GetContextWithCancel(nil, c, context.Background())
					defer cancel()
					nonStream := []byte(`{"output":[{"type":"function_call","arguments":"{\"count\":2500.0}"}]}`)
					stream := []byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","arguments":"{\"count\":2500.0}"}}`)
					r := sdktranslator.NewRegistry()
					r.Register(sdktranslator.FormatOpenAIResponse, sdktranslator.FormatCodex, nil, sdktranslator.ResponseTransform{
						NonStream: func(context.Context, string, []byte, []byte, []byte, *any) []byte { return nonStream },
						Stream:    func(context.Context, string, []byte, []byte, []byte, *any) [][]byte { return [][]byte{stream} },
					})
					wantNonStream, wantStream := string(nonStream), string(stream)
					if strings.Contains(strings.ToLower(ua), "codex") {
						wantNonStream = strings.Replace(wantNonStream, "2500.0", "2500", 1)
						wantStream = strings.Replace(wantStream, "2500.0", "2500", 1)
					}
					if got := r.TranslateNonStream(ctx, sdktranslator.FormatCodex, sdktranslator.FormatOpenAIResponse, "model", nil, nil, nil, nil); string(got) != wantNonStream {
						t.Errorf("non-stream got %s; want %s", got, wantNonStream)
					}
					if got := r.TranslateStream(ctx, sdktranslator.FormatCodex, sdktranslator.FormatOpenAIResponse, "model", nil, nil, nil, nil); len(got) != 1 || string(got[0]) != wantStream {
						t.Errorf("stream got %q; want %s", got, wantStream)
					}
				})
			}
		}
	}
}
