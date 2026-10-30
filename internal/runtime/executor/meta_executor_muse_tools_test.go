package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// Model the tool-bearing rejection observed on the live Meta endpoint. Testing
// both public formats catches Hermes chat translation as well as Responses.
func TestMetaExecutor_MuseToolsReachUpstream(t *testing.T) {
	for _, format := range []string{"openai", "codex"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", format, stream), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					tools := gjson.GetBytes(body, "tools").Array()
					if len(tools) != 2 || tools[1].Get("type").String() != "namespace" || tools[1].Get("name").String() != "muse" || tools[1].Get("tools.0.name").String() != "submit_reminder_decision" {
						w.WriteHeader(http.StatusForbidden)
						_, _ = io.WriteString(w, `{"error":{"code":"client_attestation_denied"}}`)
						return
					}
					if r.URL.Path != "/responses" || tools[0].Get("name").String() != "client_lookup" || tools[0].Get("parameters.properties.query.type").String() != "string" {
						t.Errorf("client tool or upstream path changed: %s %s", r.URL.Path, body)
					}
					writeMetaResponsesOK(w, "ok")
				}))
				defer server.Close()
				payload := []byte(`{"model":"muse-wonder-full-1.0-contributor","input":"hello","tools":[{"type":"function","name":"client_lookup","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}]}`)
				if format == "openai" {
					payload = []byte(`{"model":"muse-wonder-full-1.0-contributor","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"client_lookup","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}}]}`)
				}
				exec := NewMetaExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{Provider: "meta", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
				req := cliproxyexecutor.Request{Model: "muse-wonder-full-1.0-contributor", Payload: payload}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(format), Stream: stream}
				if stream {
					result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestMetaExecutor_MuseToolsPayloadRulesRemainFinal(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, remove := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/remove=%t", stream, remove), func(t *testing.T) {
				cfg := &config.Config{Payload: config.PayloadConfig{
					Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "muse-*"}}, Params: map[string]any{"tools.0.description": "configured caller", "tools.1.description": "configured namespace"}}},
				}}
				if remove {
					cfg.Payload.Filter = []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "muse-*"}}, Params: []string{"tools"}}}
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					if remove {
						if gjson.GetBytes(body, "tools").Exists() {
							t.Errorf("filtered tools were restored: %s", body)
						}
					} else if gjson.GetBytes(body, "tools.1.name").String() != "muse" || gjson.GetBytes(body, "tools.1.description").String() != "configured namespace" || gjson.GetBytes(body, "tools.0.description").String() != "configured caller" {
						t.Errorf("namespace did not honor final payload override: %s", body)
					}
					writeMetaResponsesOK(w, "ok")
				}))
				defer server.Close()
				exec := NewMetaExecutor(cfg)
				auth := &cliproxyauth.Auth{Provider: "meta", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
				req := cliproxyexecutor.Request{Model: "muse-wonder-full-1.0", Payload: []byte(`{"input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex"), Stream: stream}
				if stream {
					result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
