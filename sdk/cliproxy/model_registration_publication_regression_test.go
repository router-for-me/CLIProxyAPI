package cliproxy

import (
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"testing"
)

type registrationInterleaveRuntime struct{ onRead func() }

func (r *registrationInterleaveRuntime) GetClientID() string { r.onRead(); return "" }

func TestModelRegistrationRejectsSupersededPrefix(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ForceModelPrefix: true}, OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat-synthetic", Models: []config.OpenAICompatibilityModel{{Name: "upstream-model", Alias: "alias"}}}}}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	service := &Service{cfg: cfg, coreManager: manager}
	reg := GlobalModelRegistry()
	id := t.Name()
	reg.UnregisterClient(id)
	t.Cleanup(func() { reg.UnregisterClient(id) })
	interleaved := false
	runtime := &registrationInterleaveRuntime{onRead: func() {
		if interleaved {
			t.Fatal("unexpected repeated old runtime read")
		}
		interleaved = true
		current, ok := manager.GetByID(id)
		if !ok {
			t.Fatal("registered auth missing")
		}
		current = current.Clone()
		current.Prefix = "prefix-new"
		current.Runtime = nil
		updated, err := manager.Update(t.Context(), current)
		if err != nil {
			t.Fatal(err)
		}
		service.registerModelsForAuth(t.Context(), updated)
		if !reg.ClientSupportsModel(id, "prefix-new/alias") {
			t.Fatal("new prefix was not registered at the interleave point")
		}
	}}
	auth := &coreauth.Auth{ID: id, Provider: "compat-synthetic", Prefix: "prefix-old", Status: coreauth.StatusActive, Runtime: runtime, Attributes: map[string]string{"auth_kind": "apikey", "compat_name": "compat-synthetic", "provider_key": "compat-synthetic"}}
	registered, err := manager.Register(t.Context(), auth)
	if err != nil {
		t.Fatal(err)
	}
	service.registerModelsForAuth(t.Context(), registered)
	if !interleaved {
		t.Fatal("existing Runtime.GetClientID seam was not reached")
	}
	if !reg.ClientSupportsModel(id, "prefix-new/alias") || reg.ClientSupportsModel(id, "prefix-old/alias") {
		t.Fatalf("superseded auth published after current registration: new=%v old=%v", reg.ClientSupportsModel(id, "prefix-new/alias"), reg.ClientSupportsModel(id, "prefix-old/alias"))
	}
}

// TestModelRegistrationIgnoresExecutionOnlyGenerationChange verifies that a
// request result mutating Generation without changing model-registration
// inputs does not invalidate an in-flight registration for the same inputs.
func TestModelRegistrationIgnoresExecutionOnlyGenerationChange(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ForceModelPrefix: true}, OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat-synthetic", Models: []config.OpenAICompatibilityModel{{Name: "upstream-model", Alias: "alias"}}}}}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	service := &Service{cfg: cfg, coreManager: manager}
	reg := GlobalModelRegistry()
	id := t.Name()
	reg.UnregisterClient(id)
	t.Cleanup(func() { reg.UnregisterClient(id) })
	interleaved := false
	runtime := &registrationInterleaveRuntime{onRead: func() {
		if interleaved {
			t.Fatal("unexpected repeated old runtime read")
		}
		interleaved = true
		manager.MarkResult(t.Context(), coreauth.Result{AuthID: id, Provider: "compat-synthetic", Model: "alias", Success: true})
	}}
	auth := &coreauth.Auth{ID: id, Provider: "compat-synthetic", Prefix: "prefix-current", Status: coreauth.StatusActive, Runtime: runtime, Attributes: map[string]string{"auth_kind": "apikey", "compat_name": "compat-synthetic", "provider_key": "compat-synthetic"}}
	registered, err := manager.Register(t.Context(), auth)
	if err != nil {
		t.Fatal(err)
	}
	service.registerModelsForAuth(t.Context(), registered)
	if !interleaved {
		t.Fatal("existing Runtime.GetClientID seam was not reached")
	}
	if !reg.ClientSupportsModel(id, "prefix-current/alias") {
		t.Fatal("execution-only generation change rejected a current model registration")
	}
}

// TestModelRegistrationDoesNotResurrectRemovedAuth verifies the removal arm
// of the same publication invariant: a registration computed before its auth
// was removed must not republish the client when it finishes afterwards.
func TestModelRegistrationDoesNotResurrectRemovedAuth(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ForceModelPrefix: true}, OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat-synthetic", Models: []config.OpenAICompatibilityModel{{Name: "upstream-model", Alias: "alias"}}}}}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	service := &Service{cfg: cfg, coreManager: manager}
	reg := GlobalModelRegistry()
	id := t.Name()
	reg.UnregisterClient(id)
	t.Cleanup(func() { reg.UnregisterClient(id) })
	interleaved := false
	runtime := &registrationInterleaveRuntime{onRead: func() {
		if interleaved {
			t.Fatal("unexpected repeated old runtime read")
		}
		interleaved = true
		service.applyCoreAuthRemoval(t.Context(), id)
		if reg.ClientSupportsModel(id, "prefix-old/alias") {
			t.Fatal("removed auth still has registered models at the interleave point")
		}
	}}
	auth := &coreauth.Auth{ID: id, Provider: "compat-synthetic", Prefix: "prefix-old", Status: coreauth.StatusActive, Runtime: runtime, Attributes: map[string]string{"auth_kind": "apikey", "compat_name": "compat-synthetic", "provider_key": "compat-synthetic"}}
	registered, err := manager.Register(t.Context(), auth)
	if err != nil {
		t.Fatal(err)
	}
	service.registerModelsForAuth(t.Context(), registered)
	if !interleaved {
		t.Fatal("existing Runtime.GetClientID seam was not reached")
	}
	if reg.ClientSupportsModel(id, "prefix-old/alias") {
		t.Fatalf("superseded registration resurrected removed auth: old=%v", reg.ClientSupportsModel(id, "prefix-old/alias"))
	}
}

// TestModelRegistrationRejectsPreReregisterSnapshot verifies the
// unregister/re-register identity arm of the same publication invariant: a
// registration computed from the pre-removal identity must not overwrite the
// models of the re-registered identity.
func TestModelRegistrationRejectsPreReregisterSnapshot(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{ForceModelPrefix: true}, OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat-synthetic", Models: []config.OpenAICompatibilityModel{{Name: "upstream-model", Alias: "alias"}}}}}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	service := &Service{cfg: cfg, coreManager: manager}
	reg := GlobalModelRegistry()
	id := t.Name()
	reg.UnregisterClient(id)
	t.Cleanup(func() { reg.UnregisterClient(id) })
	interleaved := false
	runtime := &registrationInterleaveRuntime{onRead: func() {
		if interleaved {
			t.Fatal("unexpected repeated old runtime read")
		}
		interleaved = true
		service.applyCoreAuthRemoval(t.Context(), id)
		fresh := &coreauth.Auth{ID: id, Provider: "compat-synthetic", Prefix: "prefix-fresh", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "apikey", "compat_name": "compat-synthetic", "provider_key": "compat-synthetic"}}
		registeredFresh, err := manager.Register(t.Context(), fresh)
		if err != nil {
			t.Fatal(err)
		}
		service.registerModelsForAuth(t.Context(), registeredFresh)
		if !reg.ClientSupportsModel(id, "prefix-fresh/alias") {
			t.Fatal("fresh prefix was not registered at the interleave point")
		}
	}}
	auth := &coreauth.Auth{ID: id, Provider: "compat-synthetic", Prefix: "prefix-old", Status: coreauth.StatusActive, Runtime: runtime, Attributes: map[string]string{"auth_kind": "apikey", "compat_name": "compat-synthetic", "provider_key": "compat-synthetic"}}
	registered, err := manager.Register(t.Context(), auth)
	if err != nil {
		t.Fatal(err)
	}
	service.registerModelsForAuth(t.Context(), registered)
	if !interleaved {
		t.Fatal("existing Runtime.GetClientID seam was not reached")
	}
	if !reg.ClientSupportsModel(id, "prefix-fresh/alias") || reg.ClientSupportsModel(id, "prefix-old/alias") {
		t.Fatalf("pre-reregister registration overwrote fresh identity: fresh=%v old=%v", reg.ClientSupportsModel(id, "prefix-fresh/alias"), reg.ClientSupportsModel(id, "prefix-old/alias"))
	}
}
