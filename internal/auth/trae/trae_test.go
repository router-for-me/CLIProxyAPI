package trae

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCredentials(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "auth.json")
	raw := []byte(`{"auth_mode":"trae","last_refresh":"2026-09-08T06:13:21Z","trae":{"access_token":"secret-token","user_id":"user@example.com","expires_at":"2026-09-22T06:13:21Z","region":"cn","login_method":"device","credential_kind":"cloud_cli_jwt"}}`)
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	credentials, errLoad := LoadCredentials(path)
	if errLoad != nil {
		t.Fatalf("LoadCredentials() error = %v", errLoad)
	}
	if credentials.AccessToken != "secret-token" || credentials.UserID != "user@example.com" || credentials.Region != "CN" {
		t.Fatalf("LoadCredentials() = %+v", credentials)
	}
}

func TestLoadCredentialsRejectsDifferentAuthMode(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "auth.json")
	if errWrite := os.WriteFile(path, []byte(`{"auth_mode":"chatgpt","trae":{"access_token":"secret-token"}}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if _, errLoad := LoadCredentials(path); errLoad == nil {
		t.Fatal("LoadCredentials() error = nil, want auth mode error")
	}
}

func TestModelsFromMetadataAndRegistryModels(t *testing.T) {
	t.Parallel()
	models := []Model{{
		Name:               "GPT-5.6-Sol",
		ConfigName:         "gpt-5.6-sol",
		BackendModel:       "gpt-5.6-sol__dev",
		Provider:           Provider,
		ContextWindow:      272000,
		SupportedMIMETypes: []string{"image/*"},
	}}
	metadata := map[string]any{"models": models}
	resolved, ok := ModelFor(metadata, "gpt-5.6-sol")
	if !ok || resolved.Name != "GPT-5.6-Sol" || resolved.BackendModel != "gpt-5.6-sol__dev" {
		t.Fatalf("ModelFor() = (%+v, %t)", resolved, ok)
	}
	registryModels := RegistryModels(metadata)
	if len(registryModels) != 1 {
		t.Fatalf("RegistryModels() len = %d, want 1", len(registryModels))
	}
	if registryModels[0].ID != "GPT-5.6-Sol" || registryModels[0].ContextLength != 272000 {
		t.Fatalf("RegistryModels()[0] = %+v", registryModels[0])
	}
	if len(registryModels[0].SupportedInputModalities) != 2 || registryModels[0].SupportedInputModalities[1] != "IMAGE" {
		t.Fatalf("SupportedInputModalities = %v", registryModels[0].SupportedInputModalities)
	}
	levels := registryModels[0].Thinking.Levels
	if len(levels) != 4 || levels[3] != "xhigh" {
		t.Fatalf("Thinking levels = %v, want xhigh capability", levels)
	}
}

func TestLoadCachedModels(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "models_cache.json")
	raw := []byte(`{"client_version":"0.204.1","models":[{"slug":"GPT-5.6-Sol","config_name":"gpt-5.6-sol","prompt_model_id":"gpt-5.6-sol","context_window":272000,"input_modalities":["text","image"]}]}`)
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	models, version, errLoad := LoadCachedModels(path)
	if errLoad != nil {
		t.Fatalf("LoadCachedModels() error = %v", errLoad)
	}
	if version != "0.204.1" || len(models) != 1 || models[0].BackendModel != "gpt-5.6-sol__dev" {
		t.Fatalf("LoadCachedModels() = (%+v, %q)", models, version)
	}
}
