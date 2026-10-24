package cliproxy

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func captureSupersededRegistrationLogs(t *testing.T) *logtest.Hook {
	t.Helper()
	_, hook := logtest.NewNullLogger()
	oldLevel := log.GetLevel()
	log.SetLevel(log.WarnLevel)
	savedHooks := make(log.LevelHooks)
	for level, hooks := range log.StandardLogger().Hooks {
		savedHooks[level] = append([]log.Hook(nil), hooks...)
	}
	log.AddHook(hook)
	t.Cleanup(func() {
		log.SetLevel(oldLevel)
		log.StandardLogger().ReplaceHooks(savedHooks)
	})
	return hook
}

// TestModelRegistrationSupersededDuringCompletionReconcilesLatest_Issue6718
// reproduces issue #6718: a registration that finishes after its auth inputs
// were committed again mid-flight is fenced out. The triggering commit (a
// plugin writing its model snapshot back through host.auth.save while
// ModelsForAuth is still running) registers no models itself, so without a
// post-publication reconciliation the client is left with no models in
// /v1/models.
func TestModelRegistrationSupersededDuringCompletionReconcilesLatest_Issue6718(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ForceModelPrefix: true}, OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat-6718", Models: []config.OpenAICompatibilityModel{{Name: "upstream-model", Alias: "alias"}}}}}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	service := &Service{cfg: cfg, coreManager: manager}
	reg := GlobalModelRegistry()
	id := t.Name()
	reg.UnregisterClient(id)
	t.Cleanup(func() { reg.UnregisterClient(id) })

	interleaved := false
	runtime := &registrationInterleaveRuntime{onRead: func() {
		if interleaved {
			return
		}
		interleaved = true
		current, ok := manager.GetByID(id)
		if !ok || current == nil {
			t.Fatal("registered auth missing")
		}
		current = current.Clone()
		current.Runtime = nil
		if current.Attributes == nil {
			current.Attributes = make(map[string]string)
		}
		// Mirror the trae snapshot writeback: the auth record is committed
		// again (new Metadata/Attributes) while model resolution is still
		// in flight, and that commit publishes no models on its own.
		current.Metadata = map[string]any{"model_cache": map[string]any{"realm": "cn"}}
		current.Attributes["note"] = "models snapshot written"
		if _, errUpdate := manager.Update(t.Context(), current); errUpdate != nil {
			t.Fatal(errUpdate)
		}
	}}
	auth := &coreauth.Auth{ID: id, Provider: "compat-6718", Prefix: "prefix-6718", Status: coreauth.StatusActive, Runtime: runtime, Attributes: map[string]string{"auth_kind": "apikey", "compat_name": "compat-6718", "provider_key": "compat-6718"}}
	registered, errRegister := manager.Register(t.Context(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}

	service.completeModelRegistrationForAuth(t.Context(), registered)

	if !interleaved {
		t.Fatal("existing Runtime.GetClientID seam was not reached")
	}
	if !reg.ClientSupportsModel(id, "prefix-6718/alias") {
		t.Fatal("models vanished: superseded registration was discarded without reconciling the latest auth (issue #6718)")
	}
}

// TestModelRegistrationSupersededPublicationLogsWarning_Issue6718 covers the
// observability half of issue #6718: when the publication fence discards a
// registration, a warning must identify the discard instead of failing
// silently.
func TestModelRegistrationSupersededPublicationLogsWarning_Issue6718(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ForceModelPrefix: true}, OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat-6718-warn", Models: []config.OpenAICompatibilityModel{{Name: "upstream-model", Alias: "alias"}}}}}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	service := &Service{cfg: cfg, coreManager: manager}
	reg := GlobalModelRegistry()
	id := t.Name()
	reg.UnregisterClient(id)
	t.Cleanup(func() { reg.UnregisterClient(id) })
	hook := captureSupersededRegistrationLogs(t)

	interleaved := false
	runtime := &registrationInterleaveRuntime{onRead: func() {
		if interleaved {
			return
		}
		interleaved = true
		current, ok := manager.GetByID(id)
		if !ok || current == nil {
			t.Fatal("registered auth missing")
		}
		current = current.Clone()
		current.Prefix = "prefix-new"
		current.Runtime = nil
		updated, errUpdate := manager.Update(t.Context(), current)
		if errUpdate != nil {
			t.Fatal(errUpdate)
		}
		service.registerModelsForAuth(t.Context(), updated)
	}}
	auth := &coreauth.Auth{ID: id, Provider: "compat-6718-warn", Prefix: "prefix-old", Status: coreauth.StatusActive, Runtime: runtime, Attributes: map[string]string{"auth_kind": "apikey", "compat_name": "compat-6718-warn", "provider_key": "compat-6718-warn"}}
	registered, errRegister := manager.Register(t.Context(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}

	service.registerModelsForAuth(t.Context(), registered)

	if !interleaved {
		t.Fatal("existing Runtime.GetClientID seam was not reached")
	}
	if !reg.ClientSupportsModel(id, "prefix-new/alias") {
		t.Fatal("newer registration missing; cannot evaluate the discard warning")
	}
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "superseded") && strings.Contains(entry.Message, id) {
			return
		}
	}
	t.Fatalf("superseded publication was discarded silently, no warning logged; entries=%#v", hook.AllEntries())
}

// TestModelRegistrationSupersededByEpochAdvanceReconcilesLatest_Issue6718
// covers the registry-epoch arm of the fence: the auth record is re-committed
// mid-computation with identical registration inputs (only Metadata changes,
// like the trae plugin stamping its model_cache snapshot), so the identity
// gate still passes, but a concurrent publication advances the client epoch
// and the epoch CAS discards this computation. The completion pass must
// reconcile the latest auth so its models survive.
func TestModelRegistrationSupersededByEpochAdvanceReconcilesLatest_Issue6718(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ForceModelPrefix: true}, OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat-6718-epoch", Models: []config.OpenAICompatibilityModel{{Name: "upstream-model", Alias: "alias"}}}}}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	service := &Service{cfg: cfg, coreManager: manager}
	reg := GlobalModelRegistry()
	id := t.Name()
	reg.UnregisterClient(id)
	t.Cleanup(func() { reg.UnregisterClient(id) })

	interleaved := false
	runtime := &registrationInterleaveRuntime{onRead: func() {
		if interleaved {
			return
		}
		interleaved = true
		current, ok := manager.GetByID(id)
		if !ok || current == nil {
			t.Fatal("registered auth missing")
		}
		current = current.Clone()
		current.Runtime = nil
		// Metadata-only writeback: registration inputs are unchanged.
		current.Metadata = map[string]any{"model_cache": map[string]any{"realm": "cn"}}
		if _, errUpdate := manager.Update(t.Context(), current); errUpdate != nil {
			t.Fatal(errUpdate)
		}
		// A concurrent publication (the watcher round racing this pass)
		// advances the client epoch without leaving this client's models.
		reg.RegisterClient(id, "other-provider", []*ModelInfo{{ID: "unrelated-model"}})
	}}
	auth := &coreauth.Auth{ID: id, Provider: "compat-6718-epoch", Prefix: "prefix-6718", Status: coreauth.StatusActive, Runtime: runtime, Attributes: map[string]string{"auth_kind": "apikey", "compat_name": "compat-6718-epoch", "provider_key": "compat-6718-epoch"}}
	registered, errRegister := manager.Register(t.Context(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}

	service.completeModelRegistrationForAuth(t.Context(), registered)

	if !interleaved {
		t.Fatal("existing Runtime.GetClientID seam was not reached")
	}
	if !reg.ClientSupportsModel(id, "prefix-6718/alias") {
		t.Fatal("models vanished after an epoch-only supersede without reconciling the latest auth (issue #6718)")
	}
}

// TestModelRegistrationSupersededByDisableUnregisters_Issue6718 covers the
// reconciliation edge where the mid-computation commit disables the auth:
// the superseded publication is discarded, and the completion pass must
// remove the previously registered models instead of leaving them behind.
func TestModelRegistrationSupersededByDisableUnregisters_Issue6718(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ForceModelPrefix: true}, OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat-6718-off", Models: []config.OpenAICompatibilityModel{{Name: "upstream-model", Alias: "alias"}}}}}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	service := &Service{cfg: cfg, coreManager: manager}
	reg := GlobalModelRegistry()
	id := t.Name()
	reg.UnregisterClient(id)
	t.Cleanup(func() { reg.UnregisterClient(id) })

	interleaved := false
	runtime := &registrationInterleaveRuntime{onRead: func() {
		if interleaved {
			return
		}
		interleaved = true
		current, ok := manager.GetByID(id)
		if !ok || current == nil {
			t.Fatal("registered auth missing")
		}
		current = current.Clone()
		current.Runtime = nil
		current.Disabled = true
		current.Status = coreauth.StatusDisabled
		if _, errUpdate := manager.Update(t.Context(), current); errUpdate != nil {
			t.Fatal(errUpdate)
		}
	}}
	auth := &coreauth.Auth{ID: id, Provider: "compat-6718-off", Prefix: "prefix-6718", Status: coreauth.StatusActive, Runtime: runtime, Attributes: map[string]string{"auth_kind": "apikey", "compat_name": "compat-6718-off", "provider_key": "compat-6718-off"}}
	registered, errRegister := manager.Register(t.Context(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	// Models from an earlier successful registration are already visible.
	reg.RegisterClient(id, "compat-6718-off", []*ModelInfo{{ID: "prefix-6718/alias"}})

	service.completeModelRegistrationForAuth(t.Context(), registered)

	if !interleaved {
		t.Fatal("existing Runtime.GetClientID seam was not reached")
	}
	if reg.ClientSupportsModel(id, "prefix-6718/alias") {
		t.Fatal("models of an auth disabled mid-registration were left registered (issue #6718)")
	}
}

type issue6718ModelProvider struct {
	provider string
	handle   func(call int, req pluginapi.AuthModelRequest) pluginapi.ModelResponse
	calls    int
}

func (p *issue6718ModelProvider) Identifier() string { return p.provider }

func (p *issue6718ModelProvider) ParseAuth(context.Context, pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	return pluginapi.AuthParseResponse{}, nil
}

func (p *issue6718ModelProvider) StartLogin(context.Context, pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	return pluginapi.AuthLoginStartResponse{}, nil
}

func (p *issue6718ModelProvider) PollLogin(context.Context, pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	return pluginapi.AuthLoginPollResponse{}, nil
}

func (p *issue6718ModelProvider) RefreshAuth(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	return pluginapi.AuthRefreshResponse{}, nil
}

func (p *issue6718ModelProvider) Execute(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	return pluginapi.ExecutorResponse{}, nil
}

func (p *issue6718ModelProvider) ExecuteStream(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
	return pluginapi.ExecutorStreamResponse{}, nil
}

func (p *issue6718ModelProvider) CountTokens(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	return pluginapi.ExecutorResponse{}, nil
}

func (p *issue6718ModelProvider) HttpRequest(context.Context, pluginapi.ExecutorHTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
	return pluginapi.ExecutorHTTPResponse{}, nil
}

func (p *issue6718ModelProvider) StaticModels(context.Context, pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
	return pluginapi.ModelResponse{Provider: p.provider, Models: []pluginapi.ModelInfo{{ID: "static-model"}}}, nil
}

func (p *issue6718ModelProvider) ModelsForAuth(_ context.Context, req pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	p.calls++
	if p.handle != nil {
		return p.handle(p.calls, req), nil
	}
	return pluginapi.ModelResponse{Provider: p.provider, Models: []pluginapi.ModelInfo{{ID: "plugin-model"}}}, nil
}

func newIssue6718PluginService(t *testing.T, provider string, handle func(call int, req pluginapi.AuthModelRequest) pluginapi.ModelResponse) (*Service, *coreauth.Manager, *issue6718ModelProvider, *coreauth.Auth) {
	t.Helper()
	plugin := &issue6718ModelProvider{provider: provider, handle: handle}
	host := pluginhost.New()
	host.RegisterPluginForTest(provider, pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
		AuthProvider:       plugin,
		ModelProvider:      plugin,
		Executor:           plugin,
		ExecutorModelScope: pluginapi.ExecutorModelScopeOAuth,
	}})
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	service := &Service{cfg: &config.Config{}, coreManager: manager, pluginHost: host}
	id := t.Name()
	reg := GlobalModelRegistry()
	reg.UnregisterClient(id)
	t.Cleanup(func() { reg.UnregisterClient(id) })
	auth := &coreauth.Auth{ID: id, Provider: provider, Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth"}}
	registered, errRegister := manager.Register(t.Context(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	return service, manager, plugin, registered
}

// TestPluginModelRegistrationWritebackRecomputesLatestOnce_Issue6718 drives
// the real plugin ModelsForAuth path: the first discovery commits a newer
// auth snapshot while it is still running (the host.auth.save writeback),
// which fences out its AuthUpdate and models. The completion pass must
// recompute exactly once from the latest auth and publish the models.
func TestPluginModelRegistrationWritebackRecomputesLatestOnce_Issue6718(t *testing.T) {
	const provider = "issue6718-plugin"
	var service *Service
	var manager *coreauth.Manager
	handle := func(call int, req pluginapi.AuthModelRequest) pluginapi.ModelResponse {
		if call == 1 {
			current, ok := manager.GetByID(req.AuthID)
			if !ok || current == nil {
				t.Fatal("registered auth missing")
			}
			current = current.Clone()
			if current.Attributes == nil {
				current.Attributes = make(map[string]string)
			}
			current.Attributes["note"] = "models snapshot written"
			if _, errUpdate := manager.Update(t.Context(), current); errUpdate != nil {
				t.Fatal(errUpdate)
			}
		}
		return pluginapi.ModelResponse{
			Provider:   provider,
			Models:     []pluginapi.ModelInfo{{ID: "plugin-model"}},
			AuthUpdate: pluginapi.AuthData{Provider: provider, Metadata: map[string]any{"model_cache": map[string]any{"realm": "cn"}}},
		}
	}
	var plugin *issue6718ModelProvider
	var registered *coreauth.Auth
	service, manager, plugin, registered = newIssue6718PluginService(t, provider, handle)

	service.completeModelRegistrationForAuth(t.Context(), registered)

	if plugin.calls != 2 {
		t.Fatalf("ModelsForAuth calls = %d, want exactly 2 (initial + one reconciliation)", plugin.calls)
	}
	if !GlobalModelRegistry().ClientSupportsModel(registered.ID, "plugin-model") {
		t.Fatal("plugin models vanished after the writeback fenced out the first discovery (issue #6718)")
	}
}

// TestPluginModelRegistrationInvalidAuthUpdateDoesNotRetry_Issue6718 verifies
// that a real commit error (invalid credential weight) is not mistaken for a
// fencing race: the plugin must not be re-invoked and the true error must be
// logged.
func TestPluginModelRegistrationInvalidAuthUpdateDoesNotRetry_Issue6718(t *testing.T) {
	const provider = "issue6718-invalid"
	hook := captureSupersededRegistrationLogs(t)
	handle := func(_ int, _ pluginapi.AuthModelRequest) pluginapi.ModelResponse {
		return pluginapi.ModelResponse{
			Provider:   provider,
			Models:     []pluginapi.ModelInfo{{ID: "plugin-model"}},
			AuthUpdate: pluginapi.AuthData{Provider: provider, Metadata: map[string]any{"weight": "nonnumeric"}},
		}
	}
	service, _, plugin, registered := newIssue6718PluginService(t, provider, handle)

	service.completeModelRegistrationForAuth(t.Context(), registered)

	if plugin.calls != 1 {
		t.Fatalf("ModelsForAuth calls = %d, want 1 (real commit errors must not trigger reconciliation)", plugin.calls)
	}
	for _, entry := range hook.AllEntries() {
		if entry.Level == log.WarnLevel && strings.Contains(entry.Message, "failed to commit") && strings.Contains(entry.Message, "weight") {
			return
		}
	}
	t.Fatalf("real commit error was not logged with its cause; entries=%#v", hook.AllEntries())
}

// TestPluginModelRegistrationCanceledContextDoesNotRetry_Issue6718 verifies
// that canceling the context during discovery stops the completion pass
// instead of triggering a reconciliation call.
func TestPluginModelRegistrationCanceledContextDoesNotRetry_Issue6718(t *testing.T) {
	const provider = "issue6718-cancel"
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var manager *coreauth.Manager
	handle := func(call int, req pluginapi.AuthModelRequest) pluginapi.ModelResponse {
		if call == 1 {
			current, ok := manager.GetByID(req.AuthID)
			if !ok || current == nil {
				t.Fatal("registered auth missing")
			}
			current = current.Clone()
			if current.Attributes == nil {
				current.Attributes = make(map[string]string)
			}
			current.Attributes["note"] = "models snapshot written"
			if _, errUpdate := manager.Update(t.Context(), current); errUpdate != nil {
				t.Fatal(errUpdate)
			}
			cancel()
		}
		return pluginapi.ModelResponse{
			Provider:   provider,
			Models:     []pluginapi.ModelInfo{{ID: "plugin-model"}},
			AuthUpdate: pluginapi.AuthData{Provider: provider, Metadata: map[string]any{"model_cache": map[string]any{"realm": "cn"}}},
		}
	}
	var service *Service
	var plugin *issue6718ModelProvider
	var registered *coreauth.Auth
	service, manager, plugin, registered = newIssue6718PluginService(t, provider, handle)

	service.completeModelRegistrationForAuth(ctx, registered)

	if plugin.calls != 1 {
		t.Fatalf("ModelsForAuth calls = %d, want 1 (canceled context must not trigger reconciliation)", plugin.calls)
	}
}
