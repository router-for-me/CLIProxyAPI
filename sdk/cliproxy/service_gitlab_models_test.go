package cliproxy

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestRegisterModelsForAuth_GitLabUsesDiscoveredModelAndAlias(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "gitlab-auth",
		Provider: "gitlab",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"model_details": map[string]any{
				"model_provider": "mistral",
				"model_name":     "codestral-2501",
			},
		},
	}

	reg := registry.GetGlobalRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	models := reg.GetModelsForClient(auth.ID)
	if len(models) == 0 {
		t.Fatal("expected GitLab models to be registered")
	}

	seenActual := false
	seenAlias := false
	for _, model := range models {
		if model == nil {
			continue
		}
		switch strings.TrimSpace(model.ID) {
		case "codestral-2501":
			seenActual = true
		case "gitlab-duo":
			seenAlias = true
		}
	}

	if !seenActual {
		t.Fatal("expected discovered GitLab model to be registered")
	}
	if !seenAlias {
		t.Fatal("expected stable GitLab Duo alias to be registered")
	}
}
