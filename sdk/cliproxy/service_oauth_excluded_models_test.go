package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestOAuthExcludedModels_KimiOAuth(t *testing.T) {
	t.Parallel()

	svc := &Service{
		cfg: &config.Config{
			OAuthExcludedModels: map[string][]string{
				"kimi": {"kimi-k2-thinking", "kimi-k2.5"},
			},
		},
	}

	got := svc.oauthExcludedModels("kimi", "oauth")
	if len(got) != 2 {
		t.Fatalf("expected 2 excluded models, got %d", len(got))
	}
	if got[0] != "kimi-k2-thinking" || got[1] != "kimi-k2.5" {
		t.Fatalf("unexpected excluded models: %#v", got)
	}
}

func TestOAuthExcludedModels_KimiAPIKeyReturnsNil(t *testing.T) {
	t.Parallel()

	svc := &Service{
		cfg: &config.Config{
			OAuthExcludedModels: map[string][]string{
				"kimi": {"kimi-k2-thinking"},
			},
		},
	}

	got := svc.oauthExcludedModels("kimi", "apikey")
	if got != nil {
		t.Fatalf("expected nil for apikey auth kind, got %#v", got)
	}
}

