package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func registerVertexClaudeTestAuth(t *testing.T, cfg *config.Config, auth *coreauth.Auth) {
	t.Helper()
	service := &Service{cfg: cfg, coreManager: coreauth.NewManager(nil, nil, nil)}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	registered, err := service.coreManager.Register(context.Background(), auth)
	if err != nil {
		t.Fatalf("Register auth error: %v", err)
	}
	service.registerModelsForAuth(context.Background(), registered)
}

func vertexSAAuth(id string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:         id,
		Provider:   "vertex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth"},
		Metadata: map[string]any{
			"type": "vertex", "project_id": "p", "location": "global",
			"service_account": map[string]any{"type": "service_account", "client_email": "sa@p.iam.gserviceaccount.com"},
		},
	}
}

func TestVertexClaudeModels_ServiceAccountGetsModels(t *testing.T) {
	cfg := &config.Config{VertexClaudeModels: []string{" claude-sonnet-5-5 ", "", "claude-sonnet-5-5", "CLAUDE-SONNET-5-5", "claude-opus-4-5"}}
	auth := vertexSAAuth("vertex-sa-claude-test")
	registerVertexClaudeTestAuth(t, cfg, auth)
	for _, id := range []string{"claude-sonnet-5-5", "claude-opus-4-5"} {
		if !GlobalModelRegistry().ClientSupportsModel(auth.ID, id) {
			t.Fatalf("expected SA vertex auth to support %q", id)
		}
	}
	infos := GlobalModelRegistry().GetModelsForClient(auth.ID)
	count := 0
	for _, m := range infos {
		if m.ID == "claude-sonnet-5-5" {
			count++
			if m.OwnedBy != "anthropic" || m.Type != "vertex" {
				t.Fatalf("unexpected model info: %+v", m)
			}
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one claude-sonnet-5-5, got %d", count)
	}
}

func TestVertexClaudeModels_APIKeyAuthSkipped(t *testing.T) {
	cfg := &config.Config{VertexClaudeModels: []string{"claude-sonnet-5-5"}}
	auth := vertexSAAuth("vertex-apikey-claude-test")
	auth.Attributes = map[string]string{"auth_kind": "apikey", "api_key": "k"}
	registerVertexClaudeTestAuth(t, cfg, auth)
	if GlobalModelRegistry().ClientSupportsModel(auth.ID, "claude-sonnet-5-5") {
		t.Fatalf("api-key vertex auth must not get claude models")
	}
}

// Vertex auths that cannot use the rawPredict path must not advertise Claude models,
// otherwise requests would fall through to a Gemini endpoint and 404 upstream.
func TestVertexClaudeModels_NonServiceAccountAuthSkipped(t *testing.T) {
	cfg := &config.Config{VertexClaudeModels: []string{"claude-sonnet-5-5"}}
	for _, tc := range []struct {
		name   string
		mutate func(*coreauth.Auth)
	}{
		{name: "access token", mutate: func(a *coreauth.Auth) { a.Metadata["access_token"] = "ya29.test" }},
		{name: "missing service account", mutate: func(a *coreauth.Auth) { delete(a.Metadata, "service_account") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := vertexSAAuth("vertex-non-sa-claude-test")
			tc.mutate(auth)
			registerVertexClaudeTestAuth(t, cfg, auth)
			if GlobalModelRegistry().ClientSupportsModel(auth.ID, "claude-sonnet-5-5") {
				t.Fatal("vertex auth without usable service account must not get claude models")
			}
		})
	}
}

func TestVertexClaudeModels_EmptyConfigNoChange(t *testing.T) {
	base := []*ModelInfo{{ID: "gemini-x"}}
	for _, cfg := range []*config.Config{nil, {}, {VertexClaudeModels: []string{" ", ""}}} {
		if got := appendVertexClaudeModels(cfg, base); len(got) != 1 {
			t.Fatalf("expected no change, got %d models", len(got))
		}
	}
}
