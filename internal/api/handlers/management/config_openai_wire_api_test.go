package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestOpenAICompatWireAPIUpdates(t *testing.T) {
	for _, mode := range []string{"put array", "put items", "patch"} {
		for _, wire := range []string{"", "responses", "chat-completions", " RESPONSES ", "respones"} {
			t.Run(mode+"/"+wire, func(t *testing.T) {
				t.Setenv("MANAGEMENT_PASSWORD", "")
				path := filepath.Join(t.TempDir(), "config.yaml")
				original := []byte("openai-compatibility:\n  - name: original\n    base-url: https://original.test\n    wire-api: responses\n")
				if err := os.WriteFile(path, original, 0600); err != nil {
					t.Fatal(err)
				}
				cfg, err := config.ParseConfigBytes(original)
				if err != nil {
					t.Fatal(err)
				}
				before, _ := json.Marshal(cfg.OpenAICompatibility)
				h := NewHandler(cfg, path, nil)
				var body any
				method := http.MethodPut
				entry := map[string]any{"name": "updated", "base-url": "https://updated.test", "wire-api": wire}
				switch mode {
				case "put array":
					body = []any{entry}
				case "put items":
					body = map[string]any{"items": []any{entry}}
				case "patch":
					method = http.MethodPatch
					body = map[string]any{"index": 0, "value": entry}
				}
				raw, _ := json.Marshal(body)
				rec := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(rec)
				ctx.Request = httptest.NewRequest(method, "/v0/management/openai-compatibility", bytes.NewReader(raw))
				ctx.Request.Header.Set("Content-Type", "application/json")
				if method == http.MethodPut {
					h.PutOpenAICompat(ctx)
				} else {
					h.PatchOpenAICompat(ctx)
				}
				saved, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if wire == "respones" {
					if rec.Code != 400 || !strings.Contains(rec.Body.String(), "wire-api") {
						t.Errorf("status=%d body=%s, want 400 wire-api error", rec.Code, rec.Body.String())
					}
					after, _ := json.Marshal(cfg.OpenAICompatibility)
					if !bytes.Equal(before, after) || !bytes.Equal(saved, original) {
						t.Fatal("invalid update changed live or persisted config")
					}
					return
				}
				if rec.Code != 200 {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
				loaded, err := config.ParseConfigBytes(saved)
				if err != nil {
					t.Fatal(err)
				}
				want := strings.ToLower(strings.TrimSpace(wire))
				if got := loaded.OpenAICompatibility[0]; got.WireAPI != want || got.Name != "updated" {
					t.Errorf("persisted entry=%+v", got)
				}
				if len(cfg.OpenAICompatibility) != 1 || cfg.OpenAICompatibility[0].WireAPI != want || cfg.OpenAICompatibility[0].Name != "updated" || cfg.OpenAICompatibility[0].BaseURL != "https://updated.test" {
					t.Fatal("unexpected live provider config")
				}
			})
		}
	}
}

func TestOpenAICompatInvalidWireAPIPatchCannotDeleteEntry(t *testing.T) {
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "original", BaseURL: "https://original.test", WireAPI: "responses"}}}
	h := NewHandlerWithoutConfigFilePath(cfg, nil)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/openai-compatibility", strings.NewReader(`{"index":0,"value":{"base-url":"","wire-api":"respones"}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchOpenAICompat(ctx)
	if rec.Code != 400 || len(cfg.OpenAICompatibility) != 1 {
		t.Fatalf("status=%d entries=%d, want rejected patch without deletion", rec.Code, len(cfg.OpenAICompatibility))
	}
}
