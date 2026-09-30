package test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/responsestools"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	executor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	translator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestResponsesToolsNativeDiscoveryCustomLoop(t *testing.T) {
	alias := responsestools.CustomAliasFor("", "apply_patch")
	server, received := newResponsesToolsProviderServer(t, `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"fc_patch","name":"`+alias+`","call_id":"p1","arguments":"{\"input\":\"patch text\"}"}]}}`+"\n\n")
	cfg := &config.Config{}
	cfg.NormalizeResponsesToolsConfig()
	mgr := auth.NewManager(nil, nil, nil)
	mgr.RegisterExecutor(runtimeexecutor.NewMetaExecutor(cfg))
	mgr.SetConfig(cfg)
	mgr.SetRetryConfig(0, 0, 0)
	registerResponsesToolsProvider(t, mgr, "native-meta-loop", "meta", "review-model", server.URL)
	body := []byte(`{"model":"review-model","tools":[{"type":"tool_search","execution":"client"}],"input":[{"type":"tool_search_call","id":"fc_search","call_id":"s1","arguments":{"query":"patch"}},{"type":"tool_search_output","id":"fco_search","call_id":"s1","tools":[{"type":"custom","name":"apply_patch","format":{"type":"text"}}]}]}`)
	response, err := mgr.Execute(context.Background(), []string{"meta"}, executor.Request{Model: "review-model", Payload: body},
		executor.Options{SourceFormat: translator.FormatOpenAIResponse, OriginalRequest: body})
	if err != nil {
		t.Fatal(err)
	}
	wire := received.get()
	if gjson.GetBytes(wire, "input.1.tools.0.name").String() != alias ||
		gjson.GetBytes(wire, "input.1.tools.0.type").String() != "function" {
		t.Fatalf("native discovery did not send a portable custom declaration: %s", wire)
	}
	if gjson.GetBytes(wire, "input.0.id").String() != "tsc_search" ||
		gjson.GetBytes(wire, "input.1.id").String() != "tso_search" {
		t.Fatalf("native search history was not repaired before adaptation: %s", wire)
	}
	call := gjson.GetBytes(response.Payload, "output.0")
	if call.Get("type").String() != "custom_tool_call" || call.Get("id").String() != "ctc_patch" ||
		call.Get("name").String() != "apply_patch" || call.Get("input").String() != "patch text" ||
		call.Get("call_id").String() != "p1" {
		t.Fatalf("native discovery response lost custom identity: %s", response.Payload)
	}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatal(err)
	}
	var replayCall map[string]any
	if err := json.Unmarshal([]byte(call.Raw), &replayCall); err != nil {
		t.Fatal(err)
	}
	root["input"] = append(root["input"].([]any), replayCall, map[string]any{
		"type": "custom_tool_call_output", "id": "ctco_result", "call_id": "p1", "output": "applied",
	})
	followup, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Execute(context.Background(), []string{"meta"}, executor.Request{Model: "review-model", Payload: followup},
		executor.Options{SourceFormat: translator.FormatOpenAIResponse, OriginalRequest: followup}); err != nil {
		t.Fatal(err)
	}
	wire = received.get()
	if gjson.GetBytes(wire, "input.2.name").String() != alias ||
		gjson.GetBytes(wire, "input.2.id").String() != "fc_patch" ||
		gjson.GetBytes(wire, "input.3.id").String() != "fco_result" ||
		gjson.GetBytes(wire, "input.3.call_id").String() != "p1" {
		t.Fatalf("custom result replay lost the native discovery contract: %s", wire)
	}
}

func TestResponsesToolsPostRuleGuardCoverage(t *testing.T) {
	for _, provider := range []string{"xai", "meta", "codex"} {
		for _, stream := range []bool{false, true} {
			name := provider + "/execute"
			if stream {
				name = provider + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				server, received := newResponsesToolsProviderServer(t, responsesToolsCodexStream)
				cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "review-model"}}, Params: map[string]any{"tools": []any{}}}}}}
				if provider == "codex" {
					cfg.ResponsesTools.Routes = []config.ResponsesToolsRoute{{Match: config.ResponsesToolsMatch{Provider: "codex", AuthKind: "api-key", UpstreamModel: "review-model", UpstreamFormat: "codex", BaseURL: server.URL}, ClientSearch: "bridge", CustomTools: "function"}}
				}
				cfg.NormalizeResponsesToolsConfig()
				if err := cfg.ValidateResponsesToolsConfig(); err != nil {
					t.Fatal(err)
				}
				mgr := auth.NewManager(nil, nil, nil)
				if provider == "xai" {
					mgr.RegisterExecutor(runtimeexecutor.NewXAIExecutor(cfg))
				} else if provider == "meta" {
					mgr.RegisterExecutor(runtimeexecutor.NewMetaExecutor(cfg))
				} else {
					mgr.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
				}
				mgr.SetRetryConfig(0, 0, 0)
				mgr.SetConfig(cfg)
				registerResponsesToolsProvider(t, mgr, "strict-review-"+provider, provider, "review-model", server.URL)
				body := []byte(`{"model":"review-model","tools":[{"type":"tool_search","execution":"client"},{"type":"custom","name":"apply_patch"}],"input":[]}`)
				req := executor.Request{Model: "review-model", Payload: body}
				opts := executor.Options{SourceFormat: translator.FormatOpenAIResponse, OriginalRequest: body, Stream: stream}
				var err error
				if stream {
					var result *executor.StreamResult
					result, err = mgr.ExecuteStream(context.Background(), []string{provider}, req, opts)
					if result != nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								err = chunk.Err
							}
						}
					}
				} else {
					_, err = mgr.Execute(context.Background(), []string{provider}, req, opts)
				}
				if len(received.get()) > 0 {
					t.Fatalf("post rule removed negotiated tools but request was sent: err=%v body=%s", err, received.get())
				}
				var localError interface{ StatusCode() int }
				if !errors.As(err, &localError) || localError.StatusCode() != 422 {
					t.Fatalf("expected local 422 outbound contract error: %v", err)
				}
			})
		}
	}
}

func TestResponsesToolsNativeDiscoveryPostRuleGuard(t *testing.T) {
	for _, discovered := range []struct {
		name string
		tool string
	}{
		{"function", `{"type":"function","name":"read","parameters":{"type":"object"}}`},
		{"namespace", `{"type":"namespace","name":"fs","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}`},
	} {
		for _, stream := range []bool{false, true} {
			name := discovered.name + "/execute"
			if stream {
				name = discovered.name + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				server, received := newResponsesToolsProviderServer(t, responsesToolsCodexStream)
				alias := responsestools.CustomAliasFor("", "apply_patch")
				cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
					Models: []config.PayloadModelRule{{Name: "review-model"}},
					Params: map[string]any{"input.1.tools": []any{map[string]any{
						"type": "function", "name": alias,
						"parameters": map[string]any{"type": "object"},
					}}},
				}}}}
				cfg.NormalizeResponsesToolsConfig()
				if err := cfg.ValidateResponsesToolsConfig(); err != nil {
					t.Fatal(err)
				}
				mgr := auth.NewManager(nil, nil, nil)
				mgr.RegisterExecutor(runtimeexecutor.NewMetaExecutor(cfg))
				mgr.SetConfig(cfg)
				mgr.SetRetryConfig(0, 0, 0)
				registerResponsesToolsProvider(t, mgr, "native-discovery-guard", "meta", "review-model", server.URL)
				body := []byte(`{"model":"review-model","tools":[{"type":"tool_search","execution":"client"}],"input":[{"type":"tool_search_call","id":"tsc_s","call_id":"s1","arguments":{"query":"tools"}},{"type":"tool_search_output","id":"tso_s","call_id":"s1","tools":[` +
					discovered.tool + `,{"type":"custom","name":"apply_patch","format":{"type":"text"}}]}]}`)
				req := executor.Request{Model: "review-model", Payload: body}
				opts := executor.Options{SourceFormat: translator.FormatOpenAIResponse, OriginalRequest: body, Stream: stream}
				var err error
				if stream {
					var result *executor.StreamResult
					result, err = mgr.ExecuteStream(context.Background(), []string{"meta"}, req, opts)
					if result != nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								err = chunk.Err
							}
						}
					}
				} else {
					_, err = mgr.Execute(context.Background(), []string{"meta"}, req, opts)
				}
				if len(received.get()) > 0 {
					t.Fatalf("post rule deleted a discovered function but request was sent: err=%v body=%s", err, received.get())
				}
				var compat interface {
					error
					StatusCode() int
					IsRequestScoped() bool
				}
				if !errors.As(err, &compat) || compat.StatusCode() != 422 ||
					!compat.IsRequestScoped() ||
					!strings.Contains(compat.Error(), "removed required function tool") {
					t.Fatalf("expected local 422 for a missing discovered function: %v", err)
				}
			})
		}
	}
}
