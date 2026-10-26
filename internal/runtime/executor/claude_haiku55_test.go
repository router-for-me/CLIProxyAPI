package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeHaiku55Capabilities(t *testing.T) {
	for _, model := range []string{"claude-haiku-5-5", "CLAUDE-HAIKU-5-5", "anthropic/claude-haiku-5-5", "claude-haiku-5-5[1m]", "claude-haiku-5-5-20261007"} {
		t.Run(model, func(t *testing.T) {
			body := []byte(`{"model":"` + model + `","thinking":{"type":"adaptive"},"output_config":{"effort":"medium","timing":"fast"}}`)
			if !claudeRequestSupportsEffort(body, nil) {
				t.Fatal("Haiku 5.5 must support effort")
			}
			betas := claudeCodeCLIBetas(body, nil, true)
			for _, beta := range []string{claudePerTurnControlBeta, claudeMidConvToolChangesBeta, claudeEffortBeta} {
				if !strings.Contains(betas, beta) {
					t.Errorf("missing beta %s: %s", beta, betas)
				}
			}
			for _, beta := range []string{claudePerTurnTimingBeta, claudeThinkingDisplayUpdatesBeta, claudeServerSideFallbackBeta} {
				if strings.Contains(betas, beta) {
					t.Errorf("unsupported beta %s: %s", beta, betas)
				}
			}
			if gjson.GetBytes(applyClaudeCloakThinkingDisplay(body, false), "thinking.display").Exists() {
				t.Fatal("unexpected progress display")
			}
		})
	}
	body := []byte(`{"model":"claude-haiku-4-5","thinking":{"type":"enabled"}}`)
	if claudeRequestSupportsEffort(body, nil) || claudeModelHasPerTurnEffort("claude-haiku-4-5") {
		t.Fatal("older Haiku capabilities changed")
	}
}

func TestClaudeHaiku55Sampling(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, thinking := range []string{"adaptive", "disabled"} {
			body := []byte(`{"model":"claude-haiku-5-5","temperature":1,"top_p":0.99,"top_k":10,"thinking":{"type":"` + thinking + `"}}`)
			out := normalizeClaudeSamplingForUpstream(body, native)
			for _, field := range []string{"temperature", "top_p", "top_k"} {
				if gjson.GetBytes(out, field).Exists() {
					t.Errorf("native=%v thinking=%s retained %s", native, thinking, field)
				}
			}
		}
	}
}

func TestClaudeHaiku55SamplingPayloadBarrier(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, override := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/override=%v", stream, override), func(t *testing.T) {
				var captured []byte
				transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					var errRead error
					captured, errRead = io.ReadAll(req.Body)
					if errRead != nil {
						return nil, errRead
					}
					contentType := "application/json"
					response := `{"id":"msg_test","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[{"type":"text","text":"ready"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
					if stream {
						contentType = "text/event-stream"
						response = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
				})
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
				cfg := &config.Config{}
				if override {
					cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "claude-haiku-5-5", Protocol: "claude"}}, Params: map[string]any{"temperature": 0.5, "top_p": 0.8, "top_k": 7}}}
				}
				executor := NewClaudeExecutor(cfg)
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key"}}
				req := cliproxyexecutor.Request{Model: "claude-haiku-5-5", Payload: []byte(`{"model":"claude-haiku-5-5","max_tokens":128,"temperature":1,"top_p":0.99,"top_k":10,"messages":[{"role":"user","content":"hi"}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
				if stream {
					result, errStream := executor.ExecuteStream(ctx, auth, req, opts)
					if errStream != nil {
						t.Fatal(errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else {
					if _, errExecute := executor.Execute(ctx, auth, req, opts); errExecute != nil {
						t.Fatal(errExecute)
					}
				}
				for field, value := range map[string]float64{"temperature": 0.5, "top_p": 0.8, "top_k": 7} {
					got := gjson.GetBytes(captured, field)
					if override {
						if !got.Exists() || got.Float() != value {
							t.Errorf("payload override lost for %s: %s", field, captured)
						}
					} else if got.Exists() {
						t.Errorf("sampling normalization missed %s: %s", field, captured)
					}
				}
			})
		}
	}
}

func TestClaudeHaiku55ThinkingNormalization(t *testing.T) {
	for _, tc := range []struct{ input, wantType string }{
		{`{"thinking":{"type":"enabled","budget_tokens":4096}}`, "adaptive"},
		{`{"thinking":{"type":"disabled"},"output_config":{"effort":"max"}}`, "disabled"},
	} {
		out, errThinking := thinking.ApplyThinking([]byte(tc.input), "claude-haiku-5-5", "claude", "claude", "claude")
		if errThinking != nil {
			t.Fatal(errThinking)
		}
		if gjson.GetBytes(out, "thinking.type").String() != tc.wantType || gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
			t.Fatalf("invalid normalized thinking: %s", out)
		}
		if tc.wantType == "disabled" && gjson.GetBytes(out, "output_config.effort").Exists() {
			t.Fatalf("disabled thinking retained incompatible effort: %s", out)
		}
	}
}
