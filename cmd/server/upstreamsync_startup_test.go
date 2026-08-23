package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

type startupUpstreamProviderStore struct {
	providers []store.UpstreamProvider
}

func (s startupUpstreamProviderStore) List(context.Context) ([]store.UpstreamProvider, error) {
	return s.providers, nil
}
func (s startupUpstreamProviderStore) Get(context.Context, int64) (*store.UpstreamProvider, error) {
	return nil, store.ErrUpstreamProviderNotFound
}
func (s startupUpstreamProviderStore) Create(context.Context, store.UpstreamProvider) (*store.UpstreamProvider, error) {
	return nil, nil
}
func (s startupUpstreamProviderStore) Update(context.Context, store.UpstreamProvider) (*store.UpstreamProvider, error) {
	return nil, nil
}
func (s startupUpstreamProviderStore) Delete(context.Context, int64) error { return nil }
func (s startupUpstreamProviderStore) Count(context.Context) (int64, error) {
	return int64(len(s.providers)), nil
}

func TestApplyPersistedUpstreamProvidersRendersClaudeRowIdentity(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("{}\n"), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}

	cfg := &config.Config{}
	providers := startupUpstreamProviderStore{providers: []store.UpstreamProvider{{
		ID:           94,
		ProviderType: "claude-api-key",
		APIKey:       "test-key",
		Models:       []store.UpstreamProviderModel{{Name: "minimax-m3"}},
	}}}
	got, errApply := applyPersistedUpstreamProviders(context.Background(), providers, cfg, configPath, "")
	if errApply != nil {
		t.Fatalf("applyPersistedUpstreamProviders() error = %v", errApply)
	}
	if got == cfg {
		t.Fatal("applyPersistedUpstreamProviders() returned original config; want rendered snapshot")
	}
	if len(got.ClaudeKey) != 1 || got.ClaudeKey[0].UpstreamProviderID != 94 {
		t.Fatalf("rendered Claude keys = %+v, want row identity 94", got.ClaudeKey)
	}
}
