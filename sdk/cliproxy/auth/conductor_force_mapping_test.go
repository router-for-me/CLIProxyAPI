package auth

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type forceMappingExecutor struct {
	id string

	mu            sync.Mutex
	executeModels []string
	streamModels  []string
}

func (e *forceMappingExecutor) Identifier() string { return e.id }

func (e *forceMappingExecutor) Execute(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeModels = append(e.executeModels, req.Model)
	e.mu.Unlock()
	payload := forceMappingNonStreamUpstreamPayload(e.id, req.Model)
	return cliproxyexecutor.Response{Payload: []byte(payload)}, nil
}

func (e *forceMappingExecutor) ExecuteStream(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamModels = append(e.streamModels, req.Model)
	e.mu.Unlock()
	chunks := forceMappingStreamUpstreamChunks(e.id, req.Model)
	ch := make(chan cliproxyexecutor.StreamChunk, len(chunks))
	for _, chunk := range chunks {
		ch <- cliproxyexecutor.StreamChunk{Payload: chunk}
	}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *forceMappingExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *forceMappingExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *forceMappingExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *forceMappingExecutor) ExecuteModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeModels))
	copy(out, e.executeModels)
	return out
}

func (e *forceMappingExecutor) StreamModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.streamModels))
	copy(out, e.streamModels)
	return out
}

type forceMappingCreditsFallbackExecutor struct {
	id string

	mu                      sync.Mutex
	executeModels           []string
	executeCreditsRequested []bool
	streamModels            []string
	streamCreditsRequested  []bool
}

func (e *forceMappingCreditsFallbackExecutor) Identifier() string { return e.id }

func (e *forceMappingCreditsFallbackExecutor) Execute(ctx context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	creditsRequested := AntigravityCreditsRequested(ctx)
	e.mu.Lock()
	e.executeModels = append(e.executeModels, req.Model)
	e.executeCreditsRequested = append(e.executeCreditsRequested, creditsRequested)
	e.mu.Unlock()
	if !creditsRequested {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "MODEL_CAPACITY_EXHAUSTED"}
	}
	payload := `{"model":"` + req.Model + `","message":{"model":"` + req.Model + `"}}`
	return cliproxyexecutor.Response{Payload: []byte(payload)}, nil
}

func (e *forceMappingCreditsFallbackExecutor) ExecuteStream(ctx context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	creditsRequested := AntigravityCreditsRequested(ctx)
	e.mu.Lock()
	e.streamModels = append(e.streamModels, req.Model)
	e.streamCreditsRequested = append(e.streamCreditsRequested, creditsRequested)
	e.mu.Unlock()
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	if !creditsRequested {
		ch <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "MODEL_CAPACITY_EXHAUSTED"}}
		close(ch)
		return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
	}
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"message":{"model":"` + req.Model + `"}}` + "\n\n")}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *forceMappingCreditsFallbackExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *forceMappingCreditsFallbackExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *forceMappingCreditsFallbackExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *forceMappingCreditsFallbackExecutor) ExecuteModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeModels))
	copy(out, e.executeModels)
	return out
}

func (e *forceMappingCreditsFallbackExecutor) ExecuteCreditsRequested() []bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]bool, len(e.executeCreditsRequested))
	copy(out, e.executeCreditsRequested)
	return out
}

func (e *forceMappingCreditsFallbackExecutor) StreamModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.streamModels))
	copy(out, e.streamModels)
	return out
}

func (e *forceMappingCreditsFallbackExecutor) StreamCreditsRequested() []bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]bool, len(e.streamCreditsRequested))
	copy(out, e.streamCreditsRequested)
	return out
}

func forceMappingPayloadLeaksUpstream(payload, upstreamModel string) bool {
	if upstreamModel == "" {
		return false
	}
	return strings.Contains(payload, `"model":"`+upstreamModel+`"`) ||
		strings.Contains(payload, `"model": "`+upstreamModel+`"`) ||
		strings.Contains(payload, `"modelVersion":"`+upstreamModel+`"`)
}

func forceMappingNonStreamUpstreamPayload(provider, upstreamModel string) string {
	switch provider {
	case "codex":
		return strings.Replace(liveCodexResponsesNonStreamUpstream, "gpt-5.4", upstreamModel, 1)
	case "kimi":
		return strings.Replace(liveKimiMessagesNonStreamUpstream, "kimi-k2.5", upstreamModel, 1)
	case "xai":
		return `{"type":"message","role":"assistant","model":"` + upstreamModel + `","content":[{"type":"text","text":"hi"}]}`
	case "antigravity":
		return strings.Replace(liveAntigravityMessagesStartUpstream, "gemini-3-flash", upstreamModel, 1)
	default:
		return `{"model":"` + upstreamModel + `","message":{"model":"` + upstreamModel + `"}}`
	}
}

func forceMappingStreamUpstreamChunks(provider, upstreamModel string) [][]byte {
	switch provider {
	case "codex":
		created := strings.Replace(liveCodexResponsesCreatedUpstream, "gpt-5.4", upstreamModel, -1)
		completed := strings.Replace(liveCodexResponsesCompletedUpstream, "gpt-5.4", upstreamModel, -1)
		return [][]byte{
			[]byte("event: response.created\n"),
			[]byte("data: " + created + "\n"),
			[]byte("\n"),
			[]byte("event: response.completed\n"),
			[]byte("data: " + completed + "\n"),
			[]byte("\n"),
		}
	case "kimi":
		msg := strings.Replace(liveKimiMessagesStartUpstream, "kimi-k2.5", upstreamModel, 1)
		chat := strings.Replace(liveKimiChatChunkUpstream, "kimi-k2.5", upstreamModel, 1)
		return [][]byte{
			[]byte("event:message_start\n"),
			[]byte("data:" + msg + "\n\n"),
			[]byte("data: " + chat + "\n\n"),
		}
	case "xai":
		msg := strings.Replace(liveXAIMessagesStartUpstream, "grok-4.3", upstreamModel, 1)
		return [][]byte{
			[]byte("event: message_start\n"),
			[]byte("data: " + msg + "\n\n"),
		}
	case "antigravity":
		msg := strings.Replace(liveAntigravityMessagesStartUpstream, "gemini-3-flash", upstreamModel, 1)
		return [][]byte{
			[]byte("event: message_start\n"),
			[]byte("data: " + msg + "\n\n"),
		}
	default:
		return [][]byte{
			[]byte(`data: {"type":"response.created","response":{"model":"` + upstreamModel + `"}}` + "\n\n"),
		}
	}
}

func setupForceMappingManager(t *testing.T, provider, upstreamModel, aliasModel string) (*Manager, *forceMappingExecutor) {
	t.Helper()
	manager := NewManager(nil, nil, nil)
	executor := &forceMappingExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:         upstreamModel,
			Alias:        aliasModel,
			Fork:         true,
			ForceMapping: true,
		}},
	})

	auth := &Auth{
		ID:       provider + "-force-mapping-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: aliasModel}, {ID: upstreamModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	return manager, executor
}

func setupForceMappingCreditsFallbackManager(t *testing.T, upstreamModel, aliasModel string) (*Manager, *forceMappingCreditsFallbackExecutor) {
	t.Helper()
	const provider = "antigravity"
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
	})
	manager.SetRetryConfig(0, 0, 1)
	executor := &forceMappingCreditsFallbackExecutor{id: provider}
	manager.RegisterExecutor(executor)
	manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		provider: {{
			Name:         upstreamModel,
			Alias:        aliasModel,
			Fork:         true,
			ForceMapping: true,
		}},
	})

	auth := &Auth{
		ID:       provider + "-force-mapping-credits-auth",
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: aliasModel}, {ID: upstreamModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	return manager, executor
}

func TestManagerExecute_OAuthAliasForceMappingRewritesNonStreamResponse(t *testing.T) {
	const (
		provider      = "antigravity"
		upstreamModel = "gemini-3-flash-preview"
		aliasModel    = "claude-haiku-4-5-20251001"
	)

	manager, executor := setupForceMappingManager(t, provider, upstreamModel, aliasModel)
	resp, errExecute := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: aliasModel}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success", errExecute)
	}

	gotModels := executor.ExecuteModels()
	if len(gotModels) != 1 || gotModels[0] != upstreamModel {
		t.Fatalf("execute models = %v, want [%s]", gotModels, upstreamModel)
	}
	if got := string(resp.Payload); !strings.Contains(got, aliasModel) || forceMappingPayloadLeaksUpstream(got, upstreamModel) {
		t.Fatalf("response payload = %s, want alias %q without upstream %q", got, aliasModel, upstreamModel)
	}
}

func TestManagerExecuteStream_OAuthAliasForceMappingRewritesStreamResponse(t *testing.T) {
	const (
		provider      = "antigravity"
		upstreamModel = "gemini-3-flash-preview"
		aliasModel    = "claude-haiku-4-5-20251001"
	)

	manager, executor := setupForceMappingManager(t, provider, upstreamModel, aliasModel)
	streamResult, errExecute := manager.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: aliasModel}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute stream error = %v, want success", errExecute)
	}

	gotModels := executor.StreamModels()
	if len(gotModels) != 1 || gotModels[0] != upstreamModel {
		t.Fatalf("stream models = %v, want [%s]", gotModels, upstreamModel)
	}

	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if got := string(payload); !strings.Contains(got, aliasModel) || forceMappingPayloadLeaksUpstream(got, upstreamModel) {
		t.Fatalf("stream payload = %s, want alias %q without upstream %q", got, aliasModel, upstreamModel)
	}
}

func TestManagerExecuteStream_OAuthAliasForceMappingRewritesCodexStyleLineChunks(t *testing.T) {
	const (
		provider      = "codex"
		upstreamModel = "gpt-5.4"
		aliasModel    = "gpt-5.4-fast"
	)

	manager, _ := setupForceMappingManager(t, provider, upstreamModel, aliasModel)

	streamResult, errExecute := manager.ExecuteStream(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: aliasModel}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute stream error = %v, want success", errExecute)
	}

	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	got := string(payload)
	if !strings.Contains(got, aliasModel) || forceMappingPayloadLeaksUpstream(got, upstreamModel) {
		t.Fatalf("stream payload = %s, want alias %q without upstream %q", got, aliasModel, upstreamModel)
	}
}

func TestManagerExecute_OAuthAliasForceMappingRewritesKimiAndXAIResponses(t *testing.T) {
	cases := []struct {
		provider      string
		upstreamModel string
		aliasModel    string
	}{
		{provider: "kimi", upstreamModel: "kimi-k2.5", aliasModel: "k2.5"},
		{provider: "xai", upstreamModel: "grok-4.3", aliasModel: "grok-latest"},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			manager, executor := setupForceMappingManager(t, tc.provider, tc.upstreamModel, tc.aliasModel)
			resp, errExecute := manager.Execute(context.Background(), []string{tc.provider}, cliproxyexecutor.Request{Model: tc.aliasModel}, cliproxyexecutor.Options{})
			if errExecute != nil {
				t.Fatalf("execute error = %v, want success", errExecute)
			}
			gotModels := executor.ExecuteModels()
			if len(gotModels) != 1 || gotModels[0] != tc.upstreamModel {
				t.Fatalf("execute models = %v, want [%s]", gotModels, tc.upstreamModel)
			}
			if got := string(resp.Payload); !strings.Contains(got, tc.aliasModel) || forceMappingPayloadLeaksUpstream(got, tc.upstreamModel) {
				t.Fatalf("response payload = %s, want alias %q without upstream %q", got, tc.aliasModel, tc.upstreamModel)
			}
		})
	}
}

func TestManagerExecuteStream_OAuthAliasForceMappingRewritesKimiAndXAIResponses(t *testing.T) {
	cases := []struct {
		provider      string
		upstreamModel string
		aliasModel    string
	}{
		{provider: "kimi", upstreamModel: "kimi-k2.5", aliasModel: "k2.5"},
		{provider: "xai", upstreamModel: "grok-4.3", aliasModel: "grok-latest"},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			manager, executor := setupForceMappingManager(t, tc.provider, tc.upstreamModel, tc.aliasModel)
			streamResult, errExecute := manager.ExecuteStream(context.Background(), []string{tc.provider}, cliproxyexecutor.Request{Model: tc.aliasModel}, cliproxyexecutor.Options{})
			if errExecute != nil {
				t.Fatalf("execute stream error = %v, want success", errExecute)
			}
			gotModels := executor.StreamModels()
			if len(gotModels) != 1 || gotModels[0] != tc.upstreamModel {
				t.Fatalf("stream models = %v, want [%s]", gotModels, tc.upstreamModel)
			}
			var payload []byte
			for chunk := range streamResult.Chunks {
				if chunk.Err != nil {
					t.Fatalf("unexpected stream error: %v", chunk.Err)
				}
				payload = append(payload, chunk.Payload...)
			}
			got := string(payload)
			if !strings.Contains(got, tc.aliasModel) || forceMappingPayloadLeaksUpstream(got, tc.upstreamModel) {
				t.Fatalf("stream payload = %s, want alias %q without upstream %q", got, tc.aliasModel, tc.upstreamModel)
			}
		})
	}
}

func TestManagerExecute_LiveDerivedForceMapping_AllProviders(t *testing.T) {
	cases := []struct {
		provider      string
		upstreamModel string
		aliasModel    string
	}{
		{provider: "codex", upstreamModel: "gpt-5.4", aliasModel: "gpt-5.4-fast"},
		{provider: "antigravity", upstreamModel: "gemini-3-flash", aliasModel: "claude-haiku-4-5-20251001"},
		{provider: "kimi", upstreamModel: "kimi-k2.5", aliasModel: "k2.5"},
		{provider: "xai", upstreamModel: "grok-4.3", aliasModel: "grok-latest"},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			manager, executor := setupForceMappingManager(t, tc.provider, tc.upstreamModel, tc.aliasModel)
			resp, errExecute := manager.Execute(context.Background(), []string{tc.provider}, cliproxyexecutor.Request{Model: tc.aliasModel}, cliproxyexecutor.Options{})
			if errExecute != nil {
				t.Fatalf("execute error = %v", errExecute)
			}
			if got := executor.ExecuteModels(); len(got) != 1 || got[0] != tc.upstreamModel {
				t.Fatalf("execute models = %v, want [%s]", got, tc.upstreamModel)
			}
			gotPayload := string(resp.Payload)
			if !strings.Contains(gotPayload, tc.aliasModel) || forceMappingPayloadLeaksUpstream(gotPayload, tc.upstreamModel) {
				t.Fatalf("payload = %s, want alias %q without upstream %q", gotPayload, tc.aliasModel, tc.upstreamModel)
			}
		})
	}
}

func TestManagerExecuteStream_LiveDerivedForceMapping_AllProviders(t *testing.T) {
	cases := []struct {
		provider      string
		upstreamModel string
		aliasModel    string
	}{
		{provider: "codex", upstreamModel: "gpt-5.4", aliasModel: "gpt-5.4-fast"},
		{provider: "antigravity", upstreamModel: "gemini-3-flash", aliasModel: "claude-haiku-4-5-20251001"},
		{provider: "kimi", upstreamModel: "kimi-k2.5", aliasModel: "k2.5"},
		{provider: "xai", upstreamModel: "grok-4.3", aliasModel: "grok-latest"},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			manager, executor := setupForceMappingManager(t, tc.provider, tc.upstreamModel, tc.aliasModel)
			streamResult, errExecute := manager.ExecuteStream(context.Background(), []string{tc.provider}, cliproxyexecutor.Request{Model: tc.aliasModel}, cliproxyexecutor.Options{})
			if errExecute != nil {
				t.Fatalf("execute stream error = %v", errExecute)
			}
			if got := executor.StreamModels(); len(got) != 1 || got[0] != tc.upstreamModel {
				t.Fatalf("stream models = %v, want [%s]", got, tc.upstreamModel)
			}
			var payload []byte
			for chunk := range streamResult.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream error: %v", chunk.Err)
				}
				payload = append(payload, chunk.Payload...)
			}
			got := string(payload)
			if !strings.Contains(got, tc.aliasModel) {
				t.Fatalf("stream payload missing alias %q: %s", tc.aliasModel, got)
			}
			if forceMappingPayloadLeaksUpstream(got, tc.upstreamModel) {
				t.Fatalf("stream payload leaked upstream %q: %s", tc.upstreamModel, got)
			}
		})
	}
}

func TestManagerExecute_AntigravityCreditsFallbackForceMappingRewritesResponse(t *testing.T) {
	const (
		upstreamModel = "gemini-3-flash-preview"
		aliasModel    = "claude-haiku-4-5-20251001"
	)

	manager, executor := setupForceMappingCreditsFallbackManager(t, upstreamModel, aliasModel)
	resp, errExecute := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: aliasModel}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute error = %v, want success", errExecute)
	}

	if got := executor.ExecuteModels(); len(got) != 2 || got[0] != upstreamModel || got[1] != upstreamModel {
		t.Fatalf("execute models = %v, want [%s %s]", got, upstreamModel, upstreamModel)
	}
	if got := executor.ExecuteCreditsRequested(); len(got) != 2 || got[0] || !got[1] {
		t.Fatalf("credits flags = %v, want [false true]", got)
	}
	if got := string(resp.Payload); !strings.Contains(got, aliasModel) || forceMappingPayloadLeaksUpstream(got, upstreamModel) {
		t.Fatalf("response payload = %s, want alias %q without upstream %q", got, aliasModel, upstreamModel)
	}
}

func TestManagerExecuteStream_AntigravityCreditsFallbackForceMappingRewritesResponse(t *testing.T) {
	const (
		upstreamModel = "gemini-3-flash-preview"
		aliasModel    = "claude-haiku-4-5-20251001"
	)

	manager, executor := setupForceMappingCreditsFallbackManager(t, upstreamModel, aliasModel)
	streamResult, errExecute := manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: aliasModel}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute stream error = %v, want success", errExecute)
	}

	if got := executor.StreamModels(); len(got) != 2 || got[0] != upstreamModel || got[1] != upstreamModel {
		t.Fatalf("stream models = %v, want [%s %s]", got, upstreamModel, upstreamModel)
	}
	if got := executor.StreamCreditsRequested(); len(got) != 2 || got[0] || !got[1] {
		t.Fatalf("credits flags = %v, want [false true]", got)
	}
	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if got := string(payload); !strings.Contains(got, aliasModel) || forceMappingPayloadLeaksUpstream(got, upstreamModel) {
		t.Fatalf("stream payload = %s, want alias %q without upstream %q", got, aliasModel, upstreamModel)
	}
}

func setupAPIKeyForceMappingManager(t *testing.T, provider, upstreamModel, aliasModel string) (*Manager, *forceMappingExecutor) {
	t.Helper()
	manager := NewManager(nil, nil, nil)
	executor := &forceMappingExecutor{id: provider}
	manager.RegisterExecutor(executor)

	cfg := &internalconfig.Config{}
	apiKey := provider + "-key"
	switch provider {
	case "claude":
		cfg.ClaudeKey = []internalconfig.ClaudeKey{{
			APIKey: apiKey,
			Models: []internalconfig.ClaudeModel{{
				Name:         upstreamModel,
				Alias:        aliasModel,
				ForceMapping: true,
			}},
		}}
	case "codex":
		cfg.CodexKey = []internalconfig.CodexKey{{
			APIKey: apiKey,
			Models: []internalconfig.CodexModel{{
				Name:         upstreamModel,
				Alias:        aliasModel,
				ForceMapping: true,
			}},
		}}
	case "xai":
		cfg.XAIKey = []internalconfig.XAIKey{{
			APIKey: apiKey,
			Models: []internalconfig.XAIModel{{
				Name:         upstreamModel,
				Alias:        aliasModel,
				ForceMapping: true,
			}},
		}}
	case "vertex":
		cfg.VertexCompatAPIKey = []internalconfig.VertexCompatKey{{
			APIKey: apiKey,
			Models: []internalconfig.VertexCompatModel{{
				Name:         upstreamModel,
				Alias:        aliasModel,
				ForceMapping: true,
			}},
		}}
	case "openai-compatibility":
		cfg.OpenAICompatibility = []internalconfig.OpenAICompatibility{{
			Name: provider,
			Models: []internalconfig.OpenAICompatibilityModel{{
				Name:         upstreamModel,
				Alias:        aliasModel,
				ForceMapping: true,
			}},
		}}
	default:
		t.Fatalf("unsupported provider %q", provider)
	}
	manager.SetConfig(cfg)

	auth := &Auth{
		ID:         provider + "-api-key-force-mapping-auth",
		Provider:   provider,
		Attributes: map[string]string{"api_key": apiKey},
	}
	if provider == "openai-compatibility" {
		auth.Attributes["compat_name"] = provider
		auth.Attributes["provider_key"] = provider
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: aliasModel}, {ID: upstreamModel}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	manager.RefreshSchedulerEntry(auth.ID)

	return manager, executor
}

func TestManagerExecute_APIKeyAliasForceMappingRewritesResponse(t *testing.T) {
	tests := []struct {
		provider      string
		upstreamModel string
		aliasModel    string
	}{
		{provider: "claude", upstreamModel: "glm-5.2", aliasModel: "claude-sonnet-latest"},
		{provider: "codex", upstreamModel: "gpt-5.5", aliasModel: "claude-sonnet-4-5"},
		{provider: "xai", upstreamModel: "grok-4.5", aliasModel: "grok-latest"},
		{provider: "vertex", upstreamModel: "gemini-3-pro", aliasModel: "claude-opus-4-5"},
		{provider: "openai-compatibility", upstreamModel: "deepseek-v3.1", aliasModel: "claude-opus-4.66"},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			manager, executor := setupAPIKeyForceMappingManager(t, tt.provider, tt.upstreamModel, tt.aliasModel)
			resp, errExecute := manager.Execute(context.Background(), []string{tt.provider}, cliproxyexecutor.Request{Model: tt.aliasModel}, cliproxyexecutor.Options{})
			if errExecute != nil {
				t.Fatalf("execute error = %v, want success", errExecute)
			}

			gotModels := executor.ExecuteModels()
			if len(gotModels) != 1 || gotModels[0] != tt.upstreamModel {
				t.Fatalf("execute models = %v, want [%s]", gotModels, tt.upstreamModel)
			}
			if got := string(resp.Payload); !strings.Contains(got, tt.aliasModel) || forceMappingPayloadLeaksUpstream(got, tt.upstreamModel) {
				t.Fatalf("response payload = %s, want alias %q without upstream %q", got, tt.aliasModel, tt.upstreamModel)
			}
		})
	}
}

func TestManagerExecuteStream_APIKeyAliasForceMappingRewritesResponse(t *testing.T) {
	tests := []struct {
		provider      string
		upstreamModel string
		aliasModel    string
	}{
		{provider: "claude", upstreamModel: "glm-5.2", aliasModel: "claude-sonnet-latest"},
		{provider: "codex", upstreamModel: "gpt-5.5", aliasModel: "claude-sonnet-4-5"},
		{provider: "xai", upstreamModel: "grok-4.5", aliasModel: "grok-latest"},
		{provider: "vertex", upstreamModel: "gemini-3-pro", aliasModel: "claude-opus-4-5"},
		{provider: "openai-compatibility", upstreamModel: "deepseek-v3.1", aliasModel: "claude-opus-4.66"},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			manager, executor := setupAPIKeyForceMappingManager(t, tt.provider, tt.upstreamModel, tt.aliasModel)
			streamResult, errExecute := manager.ExecuteStream(context.Background(), []string{tt.provider}, cliproxyexecutor.Request{Model: tt.aliasModel}, cliproxyexecutor.Options{})
			if errExecute != nil {
				t.Fatalf("execute stream error = %v, want success", errExecute)
			}

			gotModels := executor.StreamModels()
			if len(gotModels) != 1 || gotModels[0] != tt.upstreamModel {
				t.Fatalf("stream models = %v, want [%s]", gotModels, tt.upstreamModel)
			}

			var payload []byte
			for chunk := range streamResult.Chunks {
				if chunk.Err != nil {
					t.Fatalf("unexpected stream error: %v", chunk.Err)
				}
				payload = append(payload, chunk.Payload...)
			}
			if got := string(payload); !strings.Contains(got, tt.aliasModel) || forceMappingPayloadLeaksUpstream(got, tt.upstreamModel) {
				t.Fatalf("stream payload = %s, want alias %q without upstream %q", got, tt.aliasModel, tt.upstreamModel)
			}
		})
	}
}
