package helps_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	helpers "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type capabilityTestExecutor struct {
	seenModel string
	resolved  bool
}

func (e *capabilityTestExecutor) Identifier() string { return "claude" }

func (e *capabilityTestExecutor) Execute(_ context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.seenModel = req.Model
	_, e.resolved = helpers.ResolveAPIKeyModelInfo(auth, req)
	out, err := applyCapabilityTestThinking(auth, req, opts)
	return cliproxyexecutor.Response{Payload: out}, err
}

func (e *capabilityTestExecutor) ExecuteStream(_ context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.seenModel = req.Model
	_, e.resolved = helpers.ResolveAPIKeyModelInfo(auth, req)
	out, err := applyCapabilityTestThinking(auth, req, opts)
	if err != nil {
		return nil, err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: out}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*capabilityTestExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

func (e *capabilityTestExecutor) CountTokens(_ context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.seenModel = req.Model
	_, e.resolved = helpers.ResolveAPIKeyModelInfo(auth, req)
	out, err := applyCapabilityTestThinking(auth, req, opts)
	return cliproxyexecutor.Response{Payload: out}, err
}

func (*capabilityTestExecutor) HttpRequest(context.Context, *cliproxyauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func applyCapabilityTestThinking(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) ([]byte, error) {
	translated := []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`)
	return helpers.ApplyRequestThinking(translated, auth, req, opts, opts.SourceFormat.String(), "claude", "claude")
}

func TestApplyRequestThinkingUsesSelectedPrefixedAPIKeyModel(t *testing.T) {
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{ForceModelPrefix: true},
		ClaudeKey: []internalconfig.ClaudeKey{{
			APIKey: "test-key",
			Prefix: "tenant",
			Models: []internalconfig.ClaudeModel{{
				Name: "claude-upstream", Alias: "claude-public",
				Thinking: &registry.ThinkingSupport{Levels: []string{"high"}},
			}},
		}},
	})
	executor := &capabilityTestExecutor{}
	manager.RegisterExecutor(executor)
	auth := &cliproxyauth.Auth{
		ID:       "prefixed-claude-key",
		Provider: "claude",
		Prefix:   "tenant",
		Attributes: map[string]string{
			cliproxyauth.AttributeAuthKind: cliproxyauth.AuthKindAPIKey,
			cliproxyauth.AttributeAPIKey:   "test-key",
			cliproxyauth.AttributeSource:   "config:claude[0]",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{
		ID: "tenant/claude-public", Type: "claude",
	}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })

	source := []byte(`{"model":"tenant/claude-public","reasoning_effort":"xhigh"}`)
	req := cliproxyexecutor.Request{Model: "tenant/claude-public", Payload: source}
	opts := cliproxyexecutor.Options{OriginalRequest: source, SourceFormat: sdktranslator.FormatOpenAI}

	resp, err := manager.Execute(context.Background(), []string{"claude"}, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	assertCapabilityExecution(t, executor, resp.Payload)

	countResp, err := manager.ExecuteCount(context.Background(), []string{"claude"}, req, opts)
	if err != nil {
		t.Fatalf("ExecuteCount() error = %v", err)
	}
	assertCapabilityExecution(t, executor, countResp.Payload)

	stream, err := manager.ExecuteStream(context.Background(), []string{"claude"}, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var streamPayload []byte
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		streamPayload = append(streamPayload, chunk.Payload...)
	}
	assertCapabilityExecution(t, executor, streamPayload)
}

func assertCapabilityExecution(t *testing.T, executor *capabilityTestExecutor, payload []byte) {
	t.Helper()
	if executor.seenModel != "claude-upstream" {
		t.Fatalf("executor model = %q, want claude-upstream", executor.seenModel)
	}
	if !executor.resolved {
		t.Fatal("executor request did not receive the selected model capability")
	}
	if got := gjson.GetBytes(payload, "output_config.effort").String(); got != "high" {
		t.Fatalf("output effort = %q, want high; body=%s", got, payload)
	}
}

func TestApplyRequestThinkingKeepsLegacyPathWithoutBinding(t *testing.T) {
	req := cliproxyexecutor.Request{Model: "claude-upstream", Payload: []byte(`{"reasoning_effort":"xhigh"}`)}
	opts := cliproxyexecutor.Options{OriginalRequest: req.Payload}
	translated := []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`)
	auth := &cliproxyauth.Auth{Provider: "claude", Attributes: map[string]string{
		cliproxyauth.AttributeAuthKind: cliproxyauth.AuthKindOAuth,
	}}

	out, err := helpers.ApplyRequestThinking(translated, auth, req, opts, "openai", "claude", "claude")
	if err != nil {
		t.Fatalf("ApplyRequestThinking() error = %v", err)
	}
	want, wantErr := thinking.ApplyThinking(translated, req.Model, "openai", "claude", "claude")
	if wantErr != nil {
		t.Fatalf("ApplyThinking() error = %v", wantErr)
	}
	if !bytes.Equal(out, want) {
		t.Fatalf("helper output = %s, want legacy output %s", out, want)
	}
}

func TestAuthSelectionOnlyModelDoesNotBindConfiguredCapabilities(t *testing.T) {
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{ClaudeKey: []internalconfig.ClaudeKey{{
		APIKey: "test-key",
		Prefix: "tenant",
		Models: []internalconfig.ClaudeModel{{
			Name: "claude-upstream", Alias: "claude-public",
			Thinking: &registry.ThinkingSupport{Levels: []string{"high"}},
		}},
	}}})
	executor := &capabilityTestExecutor{}
	manager.RegisterExecutor(executor)
	auth := &cliproxyauth.Auth{
		ID: "selection-only-auth", Provider: "claude", Prefix: "tenant",
		Attributes: map[string]string{
			cliproxyauth.AttributeAPIKey: "test-key",
			cliproxyauth.AttributeSource: "config:claude[test]",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "tenant/claude-public"}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })

	req := cliproxyexecutor.Request{Model: "plugin-execution-model", Payload: []byte(`{"reasoning_effort":"xhigh"}`)}
	opts := cliproxyexecutor.Options{
		OriginalRequest: req.Payload,
		SourceFormat:    sdktranslator.FormatOpenAI,
		Metadata: map[string]any{
			cliproxyexecutor.AuthSelectionModelMetadataKey: "tenant/claude-public",
		},
	}
	if _, err := manager.Execute(context.Background(), []string{"claude"}, req, opts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if executor.seenModel != "plugin-execution-model" {
		t.Fatalf("executor model = %q, want plugin-execution-model", executor.seenModel)
	}
	if executor.resolved {
		t.Fatal("auth-selection-only model unexpectedly bound configured capabilities")
	}
}
