package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestCodexUltrafastPerAccount(t *testing.T) {
	for _, transport := range []string{"http", "http-stream", "compact", "websocket", "websocket-stream"} {
		for _, tc := range []struct {
			name, plan, requested, want, override string
			apiKey, filter                        bool
		}{
			{name: "promax", plan: "promax", requested: "ultrafast", want: "ultrafast"},
			{name: "pro fallback", plan: "pro", requested: "ultrafast", want: "priority"},
			{name: "plus fallback", plan: "plus", requested: "ultrafast", want: "priority"},
			{name: "free fallback", plan: "free", requested: "ultrafast", want: "priority"},
			{name: "unknown fallback", requested: "ultrafast", want: "priority"},
			{name: "fast unchanged", plan: "pro", requested: "priority", want: "priority"},
			{name: "standard unchanged", plan: "promax"},
			{name: "api key passthrough", apiKey: true, requested: "ultrafast", want: "ultrafast"},
			{name: "payload override wins", plan: "pro", requested: "ultrafast", override: "ultrafast", want: "ultrafast"},
			{name: "payload filter wins", plan: "pro", requested: "ultrafast", filter: true},
		} {
			t.Run(transport+"/"+tc.name, func(t *testing.T) {
				var captured capturedCodexRequest
				var rec codexWebsocketHintRecorder
				websocket := transport == "websocket" || transport == "websocket-stream"
				var baseURL string
				if websocket {
					server := newCodexWebsocketHintServer(t, &rec)
					t.Cleanup(server.Close)
					baseURL = server.URL
				} else {
					server := newCodexRoutingHintServer(t, &captured)
					t.Cleanup(server.Close)
					baseURL = server.URL
				}
				auth := codexOAuthTestAuth(baseURL)
				auth.ID = "ultrafast-test"
				auth.Attributes["plan_type"] = tc.plan
				if tc.apiKey {
					auth = codexAPIKeyTestAuth(baseURL)
				}
				cfg := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}}
				if tc.override != "" {
					cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"service_tier": tc.override}}}
				}
				if tc.filter {
					cfg.Payload.Filter = []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"service_tier"}}}
				}
				var exec cliproxyauth.ProviderExecutor = NewCodexExecutor(cfg)
				if websocket {
					exec = NewCodexWebsocketsExecutor(cfg)
				}
				payload := []byte(`{"model":"gpt-6.1-sol","input":[{"role":"user","content":"hi"}]}`)
				if tc.requested != "" {
					payload, _ = sjson.SetBytes(payload, "service_tier", tc.requested)
				}
				req := cliproxyexecutor.Request{Model: "gpt-6.1-sol", Payload: payload}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
				if transport == "compact" {
					opts.Alt = "responses/compact"
				}
				if transport == "http-stream" || transport == "websocket-stream" {
					opts.Stream = true
					result, errExecute := exec.ExecuteStream(t.Context(), auth, req, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, errExecute := exec.Execute(t.Context(), auth, req, opts); errExecute != nil {
					t.Fatal(errExecute)
				}
				gotTier, gotHint := captured.serviceTier.String(), captured.routingHint
				if websocket {
					rec.mu.Lock()
					defer rec.mu.Unlock()
					if len(rec.frames) != 1 || len(rec.handshakes) != 1 {
						t.Fatalf("frames = %d, handshakes = %d, want one of each", len(rec.frames), len(rec.handshakes))
					}
					gotTier, gotHint = rec.frames[0].tier, rec.handshakes[0]
				}
				if gotTier != tc.want {
					t.Errorf("service_tier = %q, want %q", gotTier, tc.want)
				}
				wantHint := "model=gpt-6.1-sol"
				if tc.want != "" {
					wantHint += ";tier=" + tc.want
				}
				if tc.apiKey {
					wantHint = ""
				}
				if gotHint != wantHint {
					t.Errorf("routing hint = %q, want %q", gotHint, wantHint)
				}
				if got := gjson.GetBytes(payload, "service_tier").String(); got != tc.requested {
					t.Errorf("original request changed to %q; credential retries must retain %q", got, tc.requested)
				}
			})
		}
	}
}

func TestCodexUltrafastTranslations(t *testing.T) {
	for _, tc := range []struct {
		format sdktranslator.Format
		body   string
	}{
		{sdktranslator.FormatOpenAIResponse, `{"input":"hi"}`},
		{sdktranslator.FormatOpenAI, `{"messages":[{"role":"user","content":"hi"}]}`},
	} {
		t.Run(tc.format.String(), func(t *testing.T) {
			input, _ := sjson.Set(tc.body, "service_tier", "  ULTRAFAST  ")
			body := sdktranslator.TranslateRequest(tc.format, sdktranslator.FormatCodex, "gpt-6.1-sol", []byte(input), true)
			for _, plan := range []string{"promax", "pro"} {
				auth := codexOAuthTestAuth("")
				auth.Attributes["plan_type"] = plan
				got := gjson.GetBytes(helps.NormalizeCodexServiceTier(body, auth), "service_tier").String()
				want := "ultrafast"
				if plan == "pro" {
					want = "priority"
				}
				if got != want {
					t.Errorf("%s service_tier = %q, want %q", plan, got, want)
				}
			}
		})
	}
}
