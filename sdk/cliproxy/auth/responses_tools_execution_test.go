package auth

import (
	"context"
	"encoding/json"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"net/http"
	"strings"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// fakeResponsesExecutor captures the wire payload it receives and returns a
// canned upstream response without touching HTTP or plugins.
type fakeResponsesExecutor struct {
	provider   string
	toFormat   sdktranslator.Format
	wire       [][]byte
	response   []byte
	err        error
	countCalls int
}

func (f *fakeResponsesExecutor) Identifier() string { return f.provider }

func (f *fakeResponsesExecutor) Execute(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	f.wire = append(f.wire, req.Payload)
	if f.err != nil {
		return cliproxyexecutor.Response{}, f.err
	}
	return cliproxyexecutor.Response{Payload: f.response}, nil
}

func (f *fakeResponsesExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	resp, err := f.Execute(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: resp.Payload}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (f *fakeResponsesExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (f *fakeResponsesExecutor) CountTokens(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	f.countCalls++
	f.wire = append(f.wire, req.Payload)
	return cliproxyexecutor.Response{Payload: []byte("{\"tokens\":1}")}, nil
}

func (f *fakeResponsesExecutor) HttpRequest(_ context.Context, _ *Auth, _ *http.Request) (*http.Response, error) {
	return nil, nil
}

func (f *fakeResponsesExecutor) RequestToFormat(_ cliproxyexecutor.Request, _ cliproxyexecutor.Options) sdktranslator.Format {
	return f.toFormat
}

func responsesToolsTestManager() *Manager {
	cfg := &internalconfig.Config{}
	cfg.ResponsesTools.Enabled = true
	cfg.ResponsesTools.Routes = []internalconfig.ResponsesToolsRoute{{
		Match: internalconfig.ResponsesToolsMatch{
			Provider: "codex", AuthKind: "oauth",
			UpstreamModel: "gpt-5.6-sol", UpstreamFormat: "codex",
		},
		ClientSearch: "bridge", CustomTools: "inherit",
	}}
	cfg.NormalizeResponsesToolsConfig()
	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(cfg)
	return mgr
}

func responsesToolsTestAuth(t *testing.T, mgr *Manager) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("rt-auth", "codex", []*registry.ModelInfo{{ID: "gpt-5.6-sol"}})
	t.Cleanup(func() { reg.UnregisterClient("rt-auth") })
	_, err := mgr.Register(context.Background(), &Auth{
		ID: "rt-auth", Provider: "codex",
		Attributes: map[string]string{"auth_kind": "oauth"},
		Metadata:   map[string]any{"disable_cooling": true},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
}

func TestResponsesToolsExecuteBridgesSearch(t *testing.T) {
	mgr := responsesToolsTestManager()
	responsesToolsTestAuth(t, mgr)
	exec := &fakeResponsesExecutor{provider: "codex", toFormat: sdktranslator.FormatCodex}
	mgr.RegisterExecutor(exec)
	body := "{\"tools\": [{\"type\": \"tool_search\"}], \"input\": []}"
	req := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: []byte(body)}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatCodex,
		OriginalRequest: []byte(body),
	}
	exec.response = []byte("{\"output\": [{\"type\": \"function_call\", \"name\": \"tool_search\", \"call_id\": \"c1\", \"arguments\": \"{\\\"query\\\":\\\"x\\\"}\"}]}")
	resp, err := mgr.Execute(context.Background(), []string{"codex"}, req, opts)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(exec.wire) != 1 {
		t.Fatalf("expected one upstream call, got %d", len(exec.wire))
	}
	if strings.Contains(string(exec.wire[0]), "tool_search\":") || strings.Contains(string(exec.wire[0]), "\"type\":\"tool_search\"") {
		t.Fatalf("search declaration leaked upstream: %s", exec.wire[0])
	}
	var decoded map[string]any
	if err := json.Unmarshal(resp.Payload, &decoded); err != nil {
		t.Fatalf("response: %v", err)
	}
	output := decoded["output"].([]any)
	item := output[0].(map[string]any)
	if item["type"] != "tool_search_call" {
		t.Fatalf("search call not restored: %v", item)
	}
}

func TestResponsesToolsExecutePassthroughWhenDisabled(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{})
	responsesToolsTestAuth(t, mgr)
	exec := &fakeResponsesExecutor{provider: "codex", toFormat: sdktranslator.FormatCodex}
	mgr.RegisterExecutor(exec)
	body := "{\"tools\": [{\"type\": \"tool_search\"}], \"input\": []}"
	req := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: []byte(body)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatCodex, OriginalRequest: []byte(body)}
	exec.response = []byte("{\"ok\":true}")
	resp, err := mgr.Execute(context.Background(), []string{"codex"}, req, opts)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if string(exec.wire[0]) != body {
		t.Fatalf("disabled route must pass through byte-for-byte")
	}
	if string(resp.Payload) != "{\"ok\":true}" {
		t.Fatalf("response must pass through")
	}
}

func TestResponsesToolsBridgeErrorStopsWithoutRotation(t *testing.T) {
	mgr := responsesToolsTestManager()
	responsesToolsTestAuth(t, mgr)
	exec := &fakeResponsesExecutor{provider: "codex", toFormat: sdktranslator.FormatCodex}
	mgr.RegisterExecutor(exec)
	// disabled client-search rejects explicit search contracts at Prepare.
	cfg := &internalconfig.Config{}
	cfg.ResponsesTools.Enabled = true
	cfg.ResponsesTools.Routes = []internalconfig.ResponsesToolsRoute{{
		Match: internalconfig.ResponsesToolsMatch{
			Provider: "codex", AuthKind: "oauth",
			UpstreamModel: "gpt-5.6-sol", UpstreamFormat: "codex",
		},
		ClientSearch: "disabled", CustomTools: "inherit",
	}}
	cfg.NormalizeResponsesToolsConfig()
	mgr.SetConfig(cfg)
	body := "{\"tools\": [{\"type\": \"tool_search\"}], \"input\": []}"
	req := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: []byte(body)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatCodex, OriginalRequest: []byte(body)}
	_, err := mgr.Execute(context.Background(), []string{"codex"}, req, opts)
	if err == nil {
		t.Fatalf("expected rejection")
	}
	if len(exec.wire) != 0 {
		t.Fatalf("rejected request must never reach upstream")
	}
}
