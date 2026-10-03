package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexExecutorExecutePublishesMainUsageBeforeImageUsage(t *testing.T) {
	for _, mainUsage := range []bool{true, false} {
		for _, imageUsage := range []struct {
			name   string
			fields string
			want   coreusage.Detail
		}{
			{name: "zero", fields: `,"tool_usage":{"image_gen":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`},
			{
				name:   "nonzero",
				fields: `,"tool_usage":{"image_gen":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"input_tokens_details":{"cached_tokens":3}}}`,
				want:   coreusage.Detail{InputTokens: 10, OutputTokens: 20, TotalTokens: 30, CachedTokens: 3, CacheReadTokens: 3},
			},
			{name: "missing"},
		} {
			if !mainUsage && imageUsage.fields == "" {
				continue
			}
			t.Run(fmt.Sprintf("main_%t/image_%s", mainUsage, imageUsage.name), func(t *testing.T) {
				var mainFields string
				var wantMain coreusage.Detail
				if mainUsage {
					mainFields = `,"usage":{"input_tokens":100,"output_tokens":40,"total_tokens":140,"input_tokens_details":{"cached_tokens":25}}`
					wantMain = coreusage.Detail{InputTokens: 100, OutputTokens: 40, TotalTokens: 140, CachedTokens: 25, CacheReadTokens: 25}
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPost || r.URL.Path != "/responses" {
						t.Errorf("upstream request = %s %s, want POST /responses", r.Method, r.URL.Path)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, errWrite := fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_usage\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-5.5\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]%s%s}}\n\n", mainFields, imageUsage.fields)
					if errWrite != nil {
						t.Errorf("write upstream response: %v", errWrite)
					}
				}))
				t.Cleanup(server.Close)

				alias := t.Name()
				capture := &codexResponseModelUsageCapture{alias: alias, records: make(chan coreusage.Record, 4)}
				coreusage.RegisterNamedPlugin(t.Name(), capture)
				t.Cleanup(func() {
					coreusage.RegisterNamedPlugin(t.Name(), codexResponseModelNoopUsagePlugin{})
				})
				ctx := coreusage.WithRequestedModelAlias(context.Background(), alias)
				executor := NewCodexExecutor(&config.Config{})
				resp, errExecute := executor.Execute(ctx, codexOAuthTestAuth(server.URL), cliproxyexecutor.Request{
					Model:   "gpt-5.5",
					Payload: []byte(`{"model":"gpt-5.5","input":"hi"}`),
				}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
				if errExecute != nil {
					t.Fatalf("Execute: %v", errExecute)
				}
				if len(resp.Payload) == 0 {
					t.Fatal("Execute returned an empty response")
				}

				mainRecord := capture.await(t)
				if mainRecord.Model != "gpt-5.5" || mainRecord.Failed {
					t.Errorf("main record model = %q, failed = %t", mainRecord.Model, mainRecord.Failed)
				}
				if got := mainRecord.Detail; got.InputTokens != wantMain.InputTokens || got.OutputTokens != wantMain.OutputTokens || got.TotalTokens != wantMain.TotalTokens || got.CachedTokens != wantMain.CachedTokens || got.CacheReadTokens != wantMain.CacheReadTokens {
					t.Errorf("main usage = %+v, want %+v", mainRecord.Detail, wantMain)
				}
				if imageUsage.name == "nonzero" {
					imageRecord := capture.await(t)
					if imageRecord.Model != codexDefaultImageToolModel || imageRecord.Failed {
						t.Errorf("image record model = %q, failed = %t", imageRecord.Model, imageRecord.Failed)
					}
					if got, want := imageRecord.Detail, imageUsage.want; got.InputTokens != want.InputTokens || got.OutputTokens != want.OutputTokens || got.TotalTokens != want.TotalTokens || got.CachedTokens != want.CachedTokens || got.CacheReadTokens != want.CacheReadTokens {
						t.Errorf("image usage = %+v, want %+v", imageRecord.Detail, imageUsage.want)
					}
				}
			})
		}
	}
}
