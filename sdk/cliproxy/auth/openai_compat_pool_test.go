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
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const openAICompatPoolProviderKey = "openai-compatible-pool"

type openAICompatPoolExecutor struct {
	id string

	mu                sync.Mutex
	executeModels     []string
	countModels       []string
	streamModels      []string
	executePayloads   map[string][]byte
	executeErrors     map[string]error
	countErrors       map[string]error
	streamFirstErrors map[string]error
	streamPayloads    map[string][]cliproxyexecutor.StreamChunk
}

func (e *openAICompatPoolExecutor) Identifier() string { return e.id }

func (e *openAICompatPoolExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	_ = ctx
	_ = auth
	_ = opts
	e.mu.Lock()
	e.executeModels = append(e.executeModels, req.Model)
	payload := append([]byte(nil), e.executePayloads[req.Model]...)
	err := e.executeErrors[req.Model]
	e.mu.Unlock()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	if len(payload) > 0 {
		return cliproxyexecutor.Response{Payload: payload}, nil
	}
	return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
}

func (e *openAICompatPoolExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	_ = ctx
	_ = auth
	_ = opts
	e.mu.Lock()
	e.streamModels = append(e.streamModels, req.Model)
	err := e.streamFirstErrors[req.Model]
	payloadChunks, hasCustomChunks := e.streamPayloads[req.Model]
	chunks := append([]cliproxyexecutor.StreamChunk(nil), payloadChunks...)
	e.mu.Unlock()
	ch := make(chan cliproxyexecutor.StreamChunk, max(1, len(chunks)))
	if err != nil {
		ch <- cliproxyexecutor.StreamChunk{Err: err}
		close(ch)
		return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Model": {req.Model}}, Chunks: ch}, nil
	}
	if !hasCustomChunks {
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte(req.Model)}
	} else {
		for _, chunk := range chunks {
			ch <- chunk
		}
	}
	close(ch)
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"X-Model": {req.Model}}, Chunks: ch}, nil
}

func (e *openAICompatPoolExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *openAICompatPoolExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	_ = ctx
	_ = auth
	_ = opts
	e.mu.Lock()
	e.countModels = append(e.countModels, req.Model)
	err := e.countErrors[req.Model]
	e.mu.Unlock()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
}

func (e *openAICompatPoolExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	_ = ctx
	_ = auth
	_ = req
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *openAICompatPoolExecutor) ExecuteModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeModels))
	copy(out, e.executeModels)
	return out
}

func (e *openAICompatPoolExecutor) CountModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.countModels))
	copy(out, e.countModels)
	return out
}

func (e *openAICompatPoolExecutor) StreamModels() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.streamModels))
	copy(out, e.streamModels)
	return out
}

type authScopedOpenAICompatPoolExecutor struct {
	id string

	mu           sync.Mutex
	executeCalls []string
}

func (e *authScopedOpenAICompatPoolExecutor) Identifier() string { return e.id }

func (e *authScopedOpenAICompatPoolExecutor) Execute(_ context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	call := auth.ID + "|" + req.Model
	e.mu.Lock()
	e.executeCalls = append(e.executeCalls, call)
	e.mu.Unlock()
	return cliproxyexecutor.Response{Payload: []byte(call)}, nil
}

func (e *authScopedOpenAICompatPoolExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "ExecuteStream not implemented"}
}

func (e *authScopedOpenAICompatPoolExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *authScopedOpenAICompatPoolExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *authScopedOpenAICompatPoolExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *authScopedOpenAICompatPoolExecutor) ExecuteCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.executeCalls))
	copy(out, e.executeCalls)
	return out
}

func newOpenAICompatPoolTestManager(t *testing.T, alias string, models []internalconfig.OpenAICompatibilityModel, executor *openAICompatPoolExecutor) *Manager {
	t.Helper()
	cfg := &internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name:   "pool",
			Models: models,
		}},
	}
	m := NewManager(nil, nil, nil)
	m.SetConfig(cfg)
	if executor == nil {
		executor = &openAICompatPoolExecutor{id: openAICompatPoolProviderKey}
	}
	m.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "pool-auth-" + t.Name(),
		Provider: openAICompatPoolProviderKey,
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key":       "test-key",
			"compat_name":   "pool",
			"provider_key":  openAICompatPoolProviderKey,
			AttributeSource: "config:pool[test]",
		},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	supportsImage := false
	hasChatModel := false
	for i := range models {
		if models[i].Image {
			supportsImage = true
		} else {
			hasChatModel = true
		}
	}
	modelType := "openai-compatibility"
	if supportsImage && !hasChatModel {
		modelType = registry.OpenAIImageModelType
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, openAICompatPoolProviderKey, []*registry.ModelInfo{{
		ID:               alias,
		Type:             modelType,
		SupportsImageAPI: supportsImage,
	}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})
	return m
}

func readOpenAICompatStreamPayload(t *testing.T, streamResult *cliproxyexecutor.StreamResult) string {
	t.Helper()
	if streamResult == nil {
		t.Fatal("expected stream result")
	}
	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	return string(payload)
}

func TestManagerExecutePrefixedOpenAICompatImageModel(t *testing.T) {
	const alias = "tenant/image-public"
	executor := &openAICompatPoolExecutor{id: openAICompatPoolProviderKey}
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{ForceModelPrefix: true},
		OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name:   "pool",
			Prefix: "tenant",
			Models: []internalconfig.OpenAICompatibilityModel{{
				Name: "upstream-image", Alias: "image-public", Image: true,
			}},
		}},
	})
	m.RegisterExecutor(executor)
	auth := &Auth{
		ID:       "prefixed-image-auth",
		Provider: openAICompatPoolProviderKey,
		Prefix:   "tenant",
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key":       "test-key",
			"compat_name":   "pool",
			"provider_key":  openAICompatPoolProviderKey,
			AttributeSource: "config:pool[test]",
		},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, openAICompatPoolProviderKey, []*registry.ModelInfo{{
		ID: alias, Type: registry.OpenAIImageModelType,
	}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	resp, err := m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.ImageExecutionMetadataKey: true},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(resp.Payload); got != "upstream-image" {
		t.Fatalf("upstream model = %q, want upstream-image", got)
	}
}

func TestManagerFiltersPrefixedOpenAICompatAliasPoolByEndpoint(t *testing.T) {
	const alias = "tenant/public"
	executor := &openAICompatPoolExecutor{id: openAICompatPoolProviderKey}
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{OpenAICompatibility: []internalconfig.OpenAICompatibility{{
		Name:   "pool",
		Prefix: "tenant",
		Models: []internalconfig.OpenAICompatibilityModel{
			{Name: "chat-upstream", Alias: "public"},
			{Name: "image-upstream", Alias: "public", Image: true},
		},
	}}})
	m.RegisterExecutor(executor)
	auth := &Auth{
		ID:       "prefixed-mixed-image-auth",
		Provider: openAICompatPoolProviderKey,
		Prefix:   "tenant",
		Status:   StatusActive,
		Attributes: map[string]string{
			AttributeAPIKey: "test-key",
			"compat_name":   "pool",
			"provider_key":  openAICompatPoolProviderKey,
			AttributeSource: "config:pool[test]",
		},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, openAICompatPoolProviderKey, []*registry.ModelInfo{{ID: alias, Type: registry.OpenAIImageModelType}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	chatCandidates, chatPooled, _, _ := m.preparedExecutionModelsWithAlias(auth, alias, false)
	if len(chatCandidates) != 1 || chatCandidates[0] != "chat-upstream" || chatPooled {
		t.Fatalf("chat candidates = %v, pooled = %t; want one non-pooled chat candidate", chatCandidates, chatPooled)
	}
	imageCandidates, imagePooled, _, _ := m.preparedExecutionModelsWithAlias(auth, alias, true)
	if len(imageCandidates) != 1 || imageCandidates[0] != "image-upstream" || imagePooled {
		t.Fatalf("image candidates = %v, pooled = %t; want one non-pooled image candidate", imageCandidates, imagePooled)
	}

	chatResp, err := m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("chat Execute() error = %v", err)
	}
	if got := string(chatResp.Payload); got != "chat-upstream" {
		t.Fatalf("chat upstream model = %q, want chat-upstream", got)
	}

	imageResp, err := m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.ImageExecutionMetadataKey: true},
	})
	if err != nil {
		t.Fatalf("image Execute() error = %v", err)
	}
	if got := string(imageResp.Payload); got != "image-upstream" {
		t.Fatalf("image upstream model = %q, want image-upstream", got)
	}

	countResp, err := m.ExecuteCount(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("chat ExecuteCount() error = %v", err)
	}
	if got := string(countResp.Payload); got != "chat-upstream" {
		t.Fatalf("count upstream model = %q, want chat-upstream", got)
	}

	stream, err := m.ExecuteStream(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.ImageExecutionMetadataKey: true},
	})
	if err != nil {
		t.Fatalf("image ExecuteStream() error = %v", err)
	}
	if got := readOpenAICompatStreamPayload(t, stream); got != "image-upstream" {
		t.Fatalf("stream upstream model = %q, want image-upstream", got)
	}
}

func TestManagerImageExecutionExcludesChatOnlyAuthRegistrations(t *testing.T) {
	const (
		model         = "shared-image-model"
		chatProvider  = "claude"
		imageProvider = "openai-compatible-image"
	)
	selector := &trackingSelector{}
	chatExecutor := &openAICompatPoolExecutor{id: chatProvider}
	imageExecutor := &openAICompatPoolExecutor{id: imageProvider}
	m := NewManager(nil, selector, nil)
	m.SetConfig(&internalconfig.Config{
		ClaudeKey: []internalconfig.ClaudeKey{{
			APIKey: "chat-key",
			Models: []internalconfig.ClaudeModel{{Name: model}},
		}},
		OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name: "image",
			Models: []internalconfig.OpenAICompatibilityModel{{
				Name: "image-upstream", Alias: model, Image: true,
			}},
		}},
	})
	m.RegisterExecutor(chatExecutor)
	m.RegisterExecutor(imageExecutor)

	chatAuth := &Auth{
		ID:       "chat-only-auth",
		Provider: chatProvider,
		Status:   StatusActive,
		Attributes: map[string]string{
			AttributeAPIKey: "chat-key",
			AttributeSource: "config:claude[0]",
		},
	}
	imageAuth := &Auth{
		ID:       "image-auth",
		Provider: imageProvider,
		Status:   StatusActive,
		Attributes: map[string]string{
			AttributeAPIKey: "image-key",
			AttributeSource: "config:openai-compatibility[image]",
			"compat_name":   "image",
			"provider_key":  imageProvider,
		},
	}
	for _, auth := range []*Auth{chatAuth, imageAuth} {
		if _, err := m.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, err)
		}
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(chatAuth.ID, chatProvider, []*registry.ModelInfo{{ID: model, Type: "openai-compatibility"}})
	reg.RegisterClient(imageAuth.ID, imageProvider, []*registry.ModelInfo{{ID: model, Type: registry.OpenAIImageModelType, SupportsImageAPI: true}})
	t.Cleanup(func() {
		reg.UnregisterClient(chatAuth.ID)
		reg.UnregisterClient(imageAuth.ID)
	})

	resp, err := m.Execute(context.Background(), []string{chatProvider, imageProvider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.ImageExecutionMetadataKey: true},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := string(resp.Payload); got != "image-upstream" {
		t.Fatalf("upstream model = %q, want image-upstream", got)
	}
	if got := selector.lastAuthID; len(got) != 1 || got[0] != imageAuth.ID {
		t.Fatalf("selector candidates = %v, want only %s", got, imageAuth.ID)
	}
	if got := chatExecutor.ExecuteModels(); len(got) != 0 {
		t.Fatalf("chat-only executor calls = %v, want none", got)
	}

	pluginScheduler := &fakePluginScheduler{
		resp:    pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinFillFirst},
		handled: true,
	}
	m.SetPluginScheduler(pluginScheduler)
	resp, err = m.Execute(context.Background(), []string{chatProvider, imageProvider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{
		Metadata: map[string]any{cliproxyexecutor.ImageExecutionMetadataKey: true},
	})
	if err != nil {
		t.Fatalf("Execute() with delegated scheduler error = %v", err)
	}
	if got := string(resp.Payload); got != "image-upstream" {
		t.Fatalf("delegated scheduler upstream model = %q, want image-upstream", got)
	}
	if got := pluginScheduler.requests; len(got) != 1 || len(got[0].Candidates) != 1 || got[0].Candidates[0].ID != imageAuth.ID {
		t.Fatalf("delegated scheduler candidates = %+v, want only %s", got, imageAuth.ID)
	}
}

func TestManagerUsesEndpointSpecificForceMappingForSharedOpenAICompatAlias(t *testing.T) {
	const alias = "shared"
	executor := &openAICompatPoolExecutor{
		id: openAICompatPoolProviderKey,
		executePayloads: map[string][]byte{
			"chat-upstream":  []byte(`{"model":"chat-upstream"}`),
			"image-upstream": []byte(`{"model":"image-upstream"}`),
		},
		streamPayloads: map[string][]cliproxyexecutor.StreamChunk{
			"chat-upstream":  {{Payload: []byte(`{"model":"chat-upstream"}`)}},
			"image-upstream": {{Payload: []byte(`{"model":"image-upstream"}`)}},
		},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "chat-upstream", Alias: alias, ForceMapping: true},
		{Name: "image-upstream", Alias: alias, Image: true},
	}, executor)

	chatResp, err := m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("chat Execute() error = %v", err)
	}
	if got := string(chatResp.Payload); got != `{"model":"shared"}` {
		t.Fatalf("chat payload = %s, want force-mapped alias", got)
	}

	imageOpts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.ImageExecutionMetadataKey: true}}
	imageResp, err := m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, imageOpts)
	if err != nil {
		t.Fatalf("image Execute() error = %v", err)
	}
	if got := string(imageResp.Payload); got != `{"model":"image-upstream"}` {
		t.Fatalf("image payload = %s, want upstream model without force mapping", got)
	}

	chatStream, err := m.ExecuteStream(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("chat ExecuteStream() error = %v", err)
	}
	if got := readOpenAICompatStreamPayload(t, chatStream); got != `{"model":"shared"}` {
		t.Fatalf("chat stream payload = %s, want force-mapped alias", got)
	}

	imageStream, err := m.ExecuteStream(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, imageOpts)
	if err != nil {
		t.Fatalf("image ExecuteStream() error = %v", err)
	}
	if got := readOpenAICompatStreamPayload(t, imageStream); got != `{"model":"image-upstream"}` {
		t.Fatalf("image stream payload = %s, want upstream model without force mapping", got)
	}
}

func TestManagerMatchesDuplicateOpenAICompatUpstreamByEndpointKind(t *testing.T) {
	orders := []struct {
		name   string
		models []internalconfig.OpenAICompatibilityModel
	}{
		{name: "chat first", models: []internalconfig.OpenAICompatibilityModel{
			{Name: "shared-upstream", Alias: "shared"},
			{Name: "shared-upstream", Alias: "shared", Image: true},
		}},
		{name: "image first", models: []internalconfig.OpenAICompatibilityModel{
			{Name: "shared-upstream", Alias: "shared", Image: true},
			{Name: "shared-upstream", Alias: "shared"},
		}},
	}

	for _, tc := range orders {
		t.Run(tc.name, func(t *testing.T) {
			executor := &openAICompatPoolExecutor{id: openAICompatPoolProviderKey}
			m := newOpenAICompatPoolTestManager(t, "shared", tc.models, executor)

			for _, test := range []struct {
				name     string
				metadata map[string]any
			}{
				{name: "chat"},
				{name: "image", metadata: map[string]any{cliproxyexecutor.ImageExecutionMetadataKey: true}},
			} {
				t.Run(test.name, func(t *testing.T) {
					resp, err := m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: "shared"}, cliproxyexecutor.Options{Metadata: test.metadata})
					if err != nil {
						t.Fatalf("Execute() error = %v", err)
					}
					if got := string(resp.Payload); got != "shared-upstream" {
						t.Fatalf("upstream model = %q, want shared-upstream", got)
					}
				})
			}
		})
	}
}

func TestManagerExecuteCount_OpenAICompatAliasPoolStopsOnInvalidRequest(t *testing.T) {
	alias := "claude-opus-4.66"
	invalidErr := &Error{HTTPStatus: http.StatusUnprocessableEntity, Message: "unprocessable entity"}
	executor := &openAICompatPoolExecutor{
		id:          openAICompatPoolProviderKey,
		countErrors: map[string]error{"deepseek-v3.1": invalidErr},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "deepseek-v3.1", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	_, err := m.ExecuteCount(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err == nil || err.Error() != invalidErr.Error() {
		t.Fatalf("execute count error = %v, want %v", err, invalidErr)
	}
	got := executor.CountModels()
	if len(got) != 1 || got[0] != "deepseek-v3.1" {
		t.Fatalf("count calls = %v, want only first invalid model", got)
	}
}
func TestResolveModelAliasPoolFromConfigModels(t *testing.T) {
	models := []modelAliasEntry{
		internalconfig.OpenAICompatibilityModel{Name: "deepseek-v3.1", Alias: "claude-opus-4.66"},
		internalconfig.OpenAICompatibilityModel{Name: "glm-5", Alias: "claude-opus-4.66"},
		internalconfig.OpenAICompatibilityModel{Name: "kimi-k2.5", Alias: "claude-opus-4.66"},
	}
	got := resolveModelAliasPoolFromConfigModels("claude-opus-4.66(8192)", models)
	want := []string{"deepseek-v3.1(8192)", "glm-5(8192)", "kimi-k2.5(8192)"}
	if len(got) != len(want) {
		t.Fatalf("pool len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pool[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManagerExecute_OpenAICompatAliasPoolRotatesWithinAuth(t *testing.T) {
	alias := "claude-opus-4.66"
	executor := &openAICompatPoolExecutor{id: openAICompatPoolProviderKey}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "deepseek-v3.1", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	for i := 0; i < 3; i++ {
		resp, err := m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
		if err != nil {
			t.Fatalf("execute %d: %v", i, err)
		}
		if len(resp.Payload) == 0 {
			t.Fatalf("execute %d returned empty payload", i)
		}
	}

	got := executor.ExecuteModels()
	want := []string{"deepseek-v3.1", "glm-5", "deepseek-v3.1"}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d model = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManagerExecute_OpenAICompatAliasPoolForceMappingRotatesAndRewritesResponse(t *testing.T) {
	alias := "claude-opus-4.66"
	executor := &openAICompatPoolExecutor{
		id: openAICompatPoolProviderKey,
		executePayloads: map[string][]byte{
			"deepseek-v3.1": []byte(`{"model":"deepseek-v3.1"}`),
			"glm-5":         []byte(`{"model":"glm-5"}`),
		},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "deepseek-v3.1", Alias: alias, ForceMapping: true},
		{Name: "glm-5", Alias: alias, ForceMapping: true},
	}, executor)

	var payloads []string
	for i := 0; i < 2; i++ {
		resp, err := m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
		if err != nil {
			t.Fatalf("execute %d: %v", i, err)
		}
		payloads = append(payloads, string(resp.Payload))
	}

	got := executor.ExecuteModels()
	wantModels := []string{"deepseek-v3.1", "glm-5"}
	for i := range wantModels {
		if got[i] != wantModels[i] {
			t.Fatalf("execute call %d model = %q, want %q", i, got[i], wantModels[i])
		}
	}
	wantPayloads := []string{`{"model":"claude-opus-4.66"}`, `{"model":"claude-opus-4.66"}`}
	for i := range wantPayloads {
		if payloads[i] != wantPayloads[i] {
			t.Fatalf("payload %d = %s, want %s", i, payloads[i], wantPayloads[i])
		}
	}
}

func TestManagerExecute_OpenAICompatAliasPoolStopsOnBadRequest(t *testing.T) {
	alias := "claude-opus-4.66"
	invalidErr := &Error{HTTPStatus: http.StatusBadRequest, Message: "invalid_request_error: malformed payload"}
	executor := &openAICompatPoolExecutor{
		id:            openAICompatPoolProviderKey,
		executeErrors: map[string]error{"deepseek-v3.1": invalidErr},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "deepseek-v3.1", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	_, err := m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err == nil || err.Error() != invalidErr.Error() {
		t.Fatalf("execute error = %v, want %v", err, invalidErr)
	}
	got := executor.ExecuteModels()
	if len(got) != 1 || got[0] != "deepseek-v3.1" {
		t.Fatalf("execute calls = %v, want only first invalid model", got)
	}
}

func TestManagerExecute_OpenAICompatAliasPoolFallsBackOnModelSupportBadRequest(t *testing.T) {
	alias := "claude-opus-4.66"
	modelSupportErr := &Error{
		HTTPStatus: http.StatusBadRequest,
		Message:    "invalid_request_error: The requested model is not supported.",
	}
	executor := &openAICompatPoolExecutor{
		id:            openAICompatPoolProviderKey,
		executeErrors: map[string]error{"deepseek-v3.1": modelSupportErr},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "deepseek-v3.1", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	resp, err := m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute error = %v, want fallback success", err)
	}
	if string(resp.Payload) != "glm-5" {
		t.Fatalf("payload = %q, want %q", string(resp.Payload), "glm-5")
	}
	got := executor.ExecuteModels()
	want := []string{"deepseek-v3.1", "glm-5"}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d model = %q, want %q", i, got[i], want[i])
		}
	}

	updated, ok := m.GetByID("pool-auth-" + t.Name())
	if !ok || updated == nil {
		t.Fatalf("expected auth to remain registered")
	}
	state := updated.ModelStates["deepseek-v3.1"]
	if state == nil {
		t.Fatalf("expected suspended upstream model state")
	}
	if !state.Unavailable || state.NextRetryAfter.IsZero() {
		t.Fatalf("expected upstream model suspension, got %+v", state)
	}
}

func TestManagerExecute_OpenAICompatAliasPoolFallsBackOnModelSupportUnprocessableEntity(t *testing.T) {
	alias := "claude-opus-4.66"
	modelSupportErr := &Error{
		HTTPStatus: http.StatusUnprocessableEntity,
		Message:    "The requested model is not supported.",
	}
	executor := &openAICompatPoolExecutor{
		id:            openAICompatPoolProviderKey,
		executeErrors: map[string]error{"deepseek-v3.1": modelSupportErr},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "deepseek-v3.1", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	resp, err := m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute error = %v, want fallback success", err)
	}
	if string(resp.Payload) != "glm-5" {
		t.Fatalf("payload = %q, want %q", string(resp.Payload), "glm-5")
	}
	got := executor.ExecuteModels()
	want := []string{"deepseek-v3.1", "glm-5"}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d model = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManagerExecute_OpenAICompatAliasPoolFallsBackWithinSameAuth(t *testing.T) {
	alias := "claude-opus-4.66"
	executor := &openAICompatPoolExecutor{
		id:            openAICompatPoolProviderKey,
		executeErrors: map[string]error{"deepseek-v3.1": &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "deepseek-v3.1", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	resp, err := m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if string(resp.Payload) != "glm-5" {
		t.Fatalf("payload = %q, want %q", string(resp.Payload), "glm-5")
	}
	got := executor.ExecuteModels()
	want := []string{"deepseek-v3.1", "glm-5"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d model = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManagerExecuteStream_OpenAICompatAliasPoolRetriesOnEmptyBootstrap(t *testing.T) {
	alias := "claude-opus-4.66"
	executor := &openAICompatPoolExecutor{
		id: openAICompatPoolProviderKey,
		streamPayloads: map[string][]cliproxyexecutor.StreamChunk{
			"deepseek-v3.1": {},
		},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "deepseek-v3.1", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	streamResult, err := m.ExecuteStream(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute stream: %v", err)
	}
	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != "glm-5" {
		t.Fatalf("payload = %q, want %q", string(payload), "glm-5")
	}
	got := executor.StreamModels()
	want := []string{"deepseek-v3.1", "glm-5"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stream call %d model = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManagerExecuteStream_OpenAICompatAliasPoolFallsBackBeforeFirstByte(t *testing.T) {
	alias := "claude-opus-4.66"
	executor := &openAICompatPoolExecutor{
		id:                openAICompatPoolProviderKey,
		streamFirstErrors: map[string]error{"deepseek-v3.1": &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "deepseek-v3.1", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	streamResult, err := m.ExecuteStream(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute stream: %v", err)
	}
	var payload []byte
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		payload = append(payload, chunk.Payload...)
	}
	if string(payload) != "glm-5" {
		t.Fatalf("payload = %q, want %q", string(payload), "glm-5")
	}
	got := executor.StreamModels()
	want := []string{"deepseek-v3.1", "glm-5"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stream call %d model = %q, want %q", i, got[i], want[i])
		}
	}
	if gotHeader := streamResult.Headers.Get("X-Model"); gotHeader != "glm-5" {
		t.Fatalf("header X-Model = %q, want %q", gotHeader, "glm-5")
	}
}

func TestManagerExecuteStream_OpenAICompatAliasPoolStopsOnInvalidRequest(t *testing.T) {
	alias := "claude-opus-4.66"
	invalidErr := &Error{HTTPStatus: http.StatusUnprocessableEntity, Message: "unprocessable entity"}
	executor := &openAICompatPoolExecutor{
		id:                openAICompatPoolProviderKey,
		streamFirstErrors: map[string]error{"deepseek-v3.1": invalidErr},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "deepseek-v3.1", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	_, err := m.ExecuteStream(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err == nil || err.Error() != invalidErr.Error() {
		t.Fatalf("execute stream error = %v, want %v", err, invalidErr)
	}
	got := executor.StreamModels()
	if len(got) != 1 || got[0] != "deepseek-v3.1" {
		t.Fatalf("stream calls = %v, want only first invalid model", got)
	}
}

func TestManagerExecute_OpenAICompatAliasPoolSkipsSuspendedUpstreamOnLaterRequests(t *testing.T) {
	alias := "claude-opus-4.66"
	modelSupportErr := &Error{
		HTTPStatus: http.StatusBadRequest,
		Message:    "invalid_request_error: The requested model is not supported.",
	}
	executor := &openAICompatPoolExecutor{
		id:            openAICompatPoolProviderKey,
		executeErrors: map[string]error{"deepseek-v3.1": modelSupportErr},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "deepseek-v3.1", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	for i := 0; i < 3; i++ {
		resp, err := m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
		if err != nil {
			t.Fatalf("execute %d: %v", i, err)
		}
		if string(resp.Payload) != "glm-5" {
			t.Fatalf("execute %d payload = %q, want %q", i, string(resp.Payload), "glm-5")
		}
	}

	got := executor.ExecuteModels()
	want := []string{"deepseek-v3.1", "glm-5", "glm-5", "glm-5"}
	if len(got) != len(want) {
		t.Fatalf("execute calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execute call %d model = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManagerExecuteStream_OpenAICompatAliasPoolSkipsSuspendedUpstreamOnLaterRequests(t *testing.T) {
	alias := "claude-opus-4.66"
	modelSupportErr := &Error{
		HTTPStatus: http.StatusUnprocessableEntity,
		Message:    "The requested model is not supported.",
	}
	executor := &openAICompatPoolExecutor{
		id:                openAICompatPoolProviderKey,
		streamFirstErrors: map[string]error{"deepseek-v3.1": modelSupportErr},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "deepseek-v3.1", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	for i := 0; i < 3; i++ {
		streamResult, err := m.ExecuteStream(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
		if err != nil {
			t.Fatalf("execute stream %d: %v", i, err)
		}
		if payload := readOpenAICompatStreamPayload(t, streamResult); payload != "glm-5" {
			t.Fatalf("execute stream %d payload = %q, want %q", i, payload, "glm-5")
		}
		if gotHeader := streamResult.Headers.Get("X-Model"); gotHeader != "glm-5" {
			t.Fatalf("execute stream %d header X-Model = %q, want %q", i, gotHeader, "glm-5")
		}
	}

	got := executor.StreamModels()
	want := []string{"deepseek-v3.1", "glm-5", "glm-5", "glm-5"}
	if len(got) != len(want) {
		t.Fatalf("stream calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stream call %d model = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManagerExecuteCount_OpenAICompatAliasPoolRotatesWithinAuth(t *testing.T) {
	alias := "claude-opus-4.66"
	executor := &openAICompatPoolExecutor{id: openAICompatPoolProviderKey}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "deepseek-v3.1", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	for i := 0; i < 2; i++ {
		resp, err := m.ExecuteCount(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
		if err != nil {
			t.Fatalf("execute count %d: %v", i, err)
		}
		if len(resp.Payload) == 0 {
			t.Fatalf("execute count %d returned empty payload", i)
		}
	}

	got := executor.CountModels()
	want := []string{"deepseek-v3.1", "glm-5"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("count call %d model = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManagerExecuteCount_OpenAICompatAliasPoolSkipsSuspendedUpstreamOnLaterRequests(t *testing.T) {
	alias := "claude-opus-4.66"
	modelSupportErr := &Error{
		HTTPStatus: http.StatusBadRequest,
		Message:    "invalid_request_error: The requested model is unsupported.",
	}
	executor := &openAICompatPoolExecutor{
		id:          openAICompatPoolProviderKey,
		countErrors: map[string]error{"deepseek-v3.1": modelSupportErr},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "deepseek-v3.1", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	for i := 0; i < 3; i++ {
		resp, err := m.ExecuteCount(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
		if err != nil {
			t.Fatalf("execute count %d: %v", i, err)
		}
		if string(resp.Payload) != "glm-5" {
			t.Fatalf("execute count %d payload = %q, want %q", i, string(resp.Payload), "glm-5")
		}
	}

	got := executor.CountModels()
	want := []string{"deepseek-v3.1", "glm-5", "glm-5", "glm-5"}
	if len(got) != len(want) {
		t.Fatalf("count calls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("count call %d model = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestManagerExecute_OpenAICompatAliasPoolBlockedAuthDoesNotConsumeRetryBudget(t *testing.T) {
	alias := "claude-opus-4.66"
	cfg := &internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name: "pool",
			Models: []internalconfig.OpenAICompatibilityModel{
				{Name: "deepseek-v3.1", Alias: alias},
				{Name: "glm-5", Alias: alias},
			},
		}},
	}
	m := NewManager(nil, nil, nil)
	m.SetConfig(cfg)
	m.SetRetryConfig(0, 0, 1)

	executor := &authScopedOpenAICompatPoolExecutor{id: openAICompatPoolProviderKey}
	m.RegisterExecutor(executor)

	badAuth := &Auth{
		ID:       "aa-blocked-auth",
		Provider: openAICompatPoolProviderKey,
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key":      "bad-key",
			"compat_name":  "pool",
			"provider_key": openAICompatPoolProviderKey,
		},
	}
	goodAuth := &Auth{
		ID:       "bb-good-auth",
		Provider: openAICompatPoolProviderKey,
		Status:   StatusActive,
		Attributes: map[string]string{
			"api_key":      "good-key",
			"compat_name":  "pool",
			"provider_key": openAICompatPoolProviderKey,
		},
	}
	if _, err := m.Register(context.Background(), badAuth); err != nil {
		t.Fatalf("register bad auth: %v", err)
	}
	if _, err := m.Register(context.Background(), goodAuth); err != nil {
		t.Fatalf("register good auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(badAuth.ID, openAICompatPoolProviderKey, []*registry.ModelInfo{{ID: alias}})
	reg.RegisterClient(goodAuth.ID, openAICompatPoolProviderKey, []*registry.ModelInfo{{ID: alias}})
	t.Cleanup(func() {
		reg.UnregisterClient(badAuth.ID)
		reg.UnregisterClient(goodAuth.ID)
	})

	modelSupportErr := &Error{
		HTTPStatus: http.StatusBadRequest,
		Message:    "invalid_request_error: The requested model is not supported.",
	}
	for _, upstreamModel := range []string{"deepseek-v3.1", "glm-5"} {
		m.MarkResult(context.Background(), Result{
			AuthID:   badAuth.ID,
			Provider: openAICompatPoolProviderKey,
			Model:    upstreamModel,
			Success:  false,
			Error:    modelSupportErr,
		})
	}

	resp, err := m.Execute(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("execute error = %v, want success via fallback auth", err)
	}
	if !strings.HasPrefix(string(resp.Payload), goodAuth.ID+"|") {
		t.Fatalf("payload = %q, want auth %q", string(resp.Payload), goodAuth.ID)
	}

	got := executor.ExecuteCalls()
	if len(got) != 1 {
		t.Fatalf("execute calls = %v, want only one real execution on fallback auth", got)
	}
	if !strings.HasPrefix(got[0], goodAuth.ID+"|") {
		t.Fatalf("execute call = %q, want fallback auth %q", got[0], goodAuth.ID)
	}
}

func TestManagerExecuteStream_OpenAICompatAliasPoolStopsOnInvalidBootstrap(t *testing.T) {
	alias := "claude-opus-4.66"
	invalidErr := &Error{HTTPStatus: http.StatusBadRequest, Message: "invalid_request_error: malformed payload"}
	executor := &openAICompatPoolExecutor{
		id:                openAICompatPoolProviderKey,
		streamFirstErrors: map[string]error{"deepseek-v3.1": invalidErr},
	}
	m := newOpenAICompatPoolTestManager(t, alias, []internalconfig.OpenAICompatibilityModel{
		{Name: "deepseek-v3.1", Alias: alias},
		{Name: "glm-5", Alias: alias},
	}, executor)

	streamResult, err := m.ExecuteStream(context.Background(), []string{openAICompatPoolProviderKey}, cliproxyexecutor.Request{Model: alias}, cliproxyexecutor.Options{})
	if err == nil {
		t.Fatal("expected invalid request error")
	}
	if err != invalidErr {
		t.Fatalf("error = %v, want %v", err, invalidErr)
	}
	if streamResult != nil {
		t.Fatalf("streamResult = %#v, want nil on invalid bootstrap", streamResult)
	}
	if got := executor.StreamModels(); len(got) != 1 || got[0] != "deepseek-v3.1" {
		t.Fatalf("stream calls = %v, want only first upstream model", got)
	}
}
