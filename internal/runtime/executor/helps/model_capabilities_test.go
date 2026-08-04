package helps_test

import (
	"context"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	helps "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type configuredThinkingExecutor struct {
	seenModel        string
	resolved         bool
	translateRequest bool
	translatedBody   []byte
}

func (*configuredThinkingExecutor) Identifier() string { return "claude" }

func (e *configuredThinkingExecutor) Execute(_ context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.seenModel = req.Model
	modelInfo, resolved := cliproxyauth.ResolvedAPIKeyModelInfo(req)
	e.resolved = resolved && modelInfo != nil
	body := []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}}`)
	if e.translateRequest {
		body = sdktranslator.TranslateRequest(opts.SourceFormat, sdktranslator.FormatClaude, req.Model, req.Payload, opts.Stream)
		e.translatedBody = append(e.translatedBody[:0], body...)
	}
	out, err := helps.ApplyRequestThinking(body, req, opts, opts.SourceFormat.String(), "claude", "claude")
	return cliproxyexecutor.Response{Payload: out}, err
}

func (e *configuredThinkingExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	response, err := e.Execute(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: response.Payload}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*configuredThinkingExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

func (e *configuredThinkingExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}

func (*configuredThinkingExecutor) HttpRequest(context.Context, *cliproxyauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestApplyRequestThinkingUsesExactClaudeModeForSummaryOnlyRequest(t *testing.T) {
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{ForceModelPrefix: true},
		ClaudeKey: []internalconfig.ClaudeKey{{
			APIKey: "summary-selected-key",
			Prefix: "summary-tenant",
			Models: []internalconfig.ClaudeModel{{
				Name:  "summary-shared-upstream",
				Alias: "summary-public-model",
				Thinking: &registry.ThinkingSupport{
					Min: 1024,
					Max: 16000,
				},
			}},
		}},
	})
	executor := &configuredThinkingExecutor{translateRequest: true}
	manager.RegisterExecutor(executor)
	auth := &cliproxyauth.Auth{
		ID:       "summary-selected-auth",
		Provider: "claude",
		Prefix:   "summary-tenant",
		Attributes: map[string]string{
			cliproxyauth.AttributeAuthKind: cliproxyauth.AuthKindAPIKey,
			cliproxyauth.AttributeAPIKey:   "summary-selected-key",
			cliproxyauth.AttributeSource:   "config:claude[0]",
		},
	}

	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{
		ID: "summary-tenant/summary-public-model", Type: "claude",
	}})
	modelRegistry.RegisterClient("summary-unrelated-auth", auth.Provider, []*registry.ModelInfo{{
		ID: "summary-shared-upstream", Type: "claude",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high"}},
	}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(auth.ID)
		modelRegistry.UnregisterClient("summary-unrelated-auth")
	})
	if registered, errRegister := manager.Register(t.Context(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	} else if registered == nil {
		t.Fatal("Register() returned nil auth")
	}

	original := []byte(`{"model":"summary-tenant/summary-public-model","reasoning":{"summary":"auto"},"input":"hi"}`)
	response, errExecute := manager.Execute(t.Context(), []string{"claude"}, cliproxyexecutor.Request{
		Model:   "summary-tenant/summary-public-model",
		Payload: original,
		Format:  sdktranslator.FormatOpenAIResponse,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAIResponse,
		OriginalRequest: original,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if got := gjson.GetBytes(executor.translatedBody, "thinking.type").String(); got != "adaptive" {
		t.Fatalf("pre-executor thinking.type = %q, want global adaptive trigger; body=%s", got, executor.translatedBody)
	}
	if got := gjson.GetBytes(response.Payload, "thinking.type").String(); got != "enabled" {
		t.Fatalf("thinking.type = %q, want exact manual mode; body=%s", got, response.Payload)
	}
	if got := gjson.GetBytes(response.Payload, "thinking.budget_tokens").Int(); got != 1024 {
		t.Fatalf("thinking.budget_tokens = %d, want exact minimum 1024; body=%s", got, response.Payload)
	}
	if got := gjson.GetBytes(response.Payload, "thinking.display").String(); got != "summarized" {
		t.Fatalf("thinking.display = %q, want summarized; body=%s", got, response.Payload)
	}
	if gjson.GetBytes(response.Payload, "output_config.effort").Exists() {
		t.Fatalf("manual thinking retained adaptive effort: %s", response.Payload)
	}
}

func TestApplyRequestThinkingUsesSelectedPrefixedAPIKeyModel(t *testing.T) {
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{ForceModelPrefix: true},
		ClaudeKey: []internalconfig.ClaudeKey{{
			APIKey: "selected-key",
			Prefix: "tenant",
			Models: []internalconfig.ClaudeModel{{
				Name: "shared-upstream", Alias: "public-model",
				Thinking: &registry.ThinkingSupport{Levels: []string{"high"}},
			}},
		}},
	})
	executor := &configuredThinkingExecutor{}
	manager.RegisterExecutor(executor)
	auth := &cliproxyauth.Auth{
		ID:       "selected-auth",
		Provider: "claude",
		Prefix:   "tenant",
		Attributes: map[string]string{
			cliproxyauth.AttributeAuthKind: cliproxyauth.AuthKindAPIKey,
			cliproxyauth.AttributeAPIKey:   "selected-key",
			cliproxyauth.AttributeSource:   "config:claude[0]",
		},
	}

	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "tenant/public-model", Type: "claude"}})
	modelRegistry.RegisterClient("unrelated-auth", auth.Provider, []*registry.ModelInfo{{
		ID: "shared-upstream", Type: "claude",
		Thinking: &registry.ThinkingSupport{Levels: []string{"max"}},
	}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(auth.ID)
		modelRegistry.UnregisterClient("unrelated-auth")
	})
	ctx := t.Context()
	registered, errRegister := manager.Register(ctx, auth)
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	if registered == nil {
		t.Fatal("Register() returned nil auth")
	}

	original := []byte(`{"model":"tenant/public-model","reasoning_effort":"max","messages":[{"role":"user","content":"hello"}]}`)
	req := cliproxyexecutor.Request{
		Model:   "tenant/public-model",
		Payload: original,
		Format:  sdktranslator.FormatOpenAI,
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: original,
	}
	assertResponse := func(path string, payload []byte) {
		t.Helper()
		if executor.seenModel != "shared-upstream" {
			t.Fatalf("%s executor model = %q, want shared-upstream", path, executor.seenModel)
		}
		if !executor.resolved {
			t.Fatalf("%s request did not receive selected model capabilities", path)
		}
		if got := gjson.GetBytes(payload, "output_config.effort").String(); got != "high" {
			t.Fatalf("%s output effort = %q, want selected credential capability high; body=%s", path, got, payload)
		}
	}

	response, errExecute := manager.Execute(ctx, []string{"claude"}, req, opts)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	assertResponse("execute", response.Payload)

	countResponse, errCount := manager.ExecuteCount(ctx, []string{"claude"}, req, opts)
	if errCount != nil {
		t.Fatalf("ExecuteCount() error = %v", errCount)
	}
	assertResponse("count", countResponse.Payload)

	streamResult, errStream := manager.ExecuteStream(ctx, []string{"claude"}, req, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	var streamPayload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("ExecuteStream() chunk error = %v", chunk.Err)
		}
		streamPayload = append(streamPayload, chunk.Payload...)
	}
	assertResponse("stream", streamPayload)
}
