package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// TestSharedOAuthAliasFallsBackAcrossProvidersOnQuota documents the existing
// behavior of a single OAuth alias that is shared by two provider channels.
// Every codex credential (higher priority) is quota-exhausted, so the request
// must be served by the lower-priority claude credential with its own upstream model.
func TestSharedOAuthAliasFallsBackAcrossProvidersOnQuota(t *testing.T) {
	withQuotaCooldownEnabled(t)
	const alias, codexModel, claudeModel = "coder-shared-alias", "gpt-shared-upstream", "claude-shared-upstream"
	for _, path := range []string{"execute", "stream"} {
		t.Run(path, func(t *testing.T) {
			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			manager.SetRetryConfig(0, 30*time.Second, 0)
			manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
				"codex":  {{Name: codexModel, Alias: alias}},
				"claude": {{Name: claudeModel, Alias: alias}},
			})
			var mu sync.Mutex
			var attempts []string
			record := func(id, model string) {
				mu.Lock()
				defer mu.Unlock()
				attempts = append(attempts, id+"="+model)
			}
			retryAfter := time.Hour
			newExecutor := func(provider string, fn func(*Auth, cliproxyexecutor.Request) (cliproxyexecutor.Response, error)) *customStreamMockExecutor {
				execute := func(_ context.Context, selected *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					record(selected.ID, req.Model)
					return fn(selected, req)
				}
				return &customStreamMockExecutor{
					identifier:              provider,
					mockCustomErrorExecutor: mockCustomErrorExecutor{identifier: provider, executeFn: execute},
					streamFn: func(ctx context.Context, selected *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
						response, errExecute := execute(ctx, selected, req, opts)
						if errExecute != nil {
							return nil, errExecute
						}
						chunks := make(chan cliproxyexecutor.StreamChunk, 1)
						chunks <- cliproxyexecutor.StreamChunk{Payload: response.Payload}
						close(chunks)
						return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
					},
				}
			}
			manager.RegisterExecutor(newExecutor("codex", func(*Auth, cliproxyexecutor.Request) (cliproxyexecutor.Response, error) {
				return cliproxyexecutor.Response{}, streamQuotaError{
					customStatusError: customStatusError{code: http.StatusTooManyRequests, msg: "usage limit reached", retryAfter: &retryAfter},
					credentialScoped:  true,
				}
			}))
			manager.RegisterExecutor(newExecutor("claude", func(_ *Auth, req cliproxyexecutor.Request) (cliproxyexecutor.Response, error) {
				return cliproxyexecutor.Response{Payload: []byte("claude:" + req.Model)}, nil
			}))
			for _, candidate := range []*Auth{
				{ID: "shared-codex-a-" + path, Provider: "codex", Status: StatusActive, Attributes: map[string]string{"priority": "10"}},
				{ID: "shared-codex-b-" + path, Provider: "codex", Status: StatusActive, Attributes: map[string]string{"priority": "10"}},
				{ID: "shared-claude-" + path, Provider: "claude", Status: StatusActive, Attributes: map[string]string{"priority": "1"}},
			} {
				registry.GetGlobalRegistry().RegisterClient(candidate.ID, candidate.Provider, []*registry.ModelInfo{{ID: alias}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(candidate.ID) })
				if _, errRegister := manager.Register(context.Background(), candidate); errRegister != nil {
					t.Fatal(errRegister)
				}
			}
			providers := []string{"codex", "claude"}
			var payload string
			switch path {
			case "execute":
				response, errExecute := manager.Execute(context.Background(), providers, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
				if errExecute != nil {
					t.Fatalf("Execute() error = %v, want claude fallback", errExecute)
				}
				payload = string(response.Payload)
			case "stream":
				result, errStream := manager.ExecuteStream(context.Background(), providers, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{Stream: true})
				if errStream != nil {
					t.Fatalf("ExecuteStream() error = %v, want claude fallback", errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error = %v", chunk.Err)
					}
					payload += string(chunk.Payload)
				}
			}
			if payload != "claude:"+claudeModel {
				t.Fatalf("payload = %q, want claude upstream model %q", payload, claudeModel)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(attempts) != 3 {
				t.Fatalf("attempts = %v, want both codex credentials then claude", attempts)
			}
			for _, attempt := range attempts[:2] {
				if attempt != "shared-codex-a-"+path+"="+codexModel && attempt != "shared-codex-b-"+path+"="+codexModel {
					t.Fatalf("attempts = %v, want codex credentials first", attempts)
				}
			}
			t.Logf("attempt order: %v", attempts)
		})
	}
}
