package cliproxy

import (
	"os"
	"path/filepath"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelrouting"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// A persisted projection that cannot be prepared (for one, its credential was
// rotated away) must not stop the proxy: it leaves the config file and the
// running configuration, and the proxy starts with no active generation.
func TestInitializeModelRoutingStartsWithoutAStaleProjection(t *testing.T) {
	modelKey := modelrouting.ModelKey{CatalogProviderID: "catalog-provider", CanonicalModelID: "rotated-model"}
	routeKey := modelrouting.RouteKey{ModelKey: modelKey, RouteChannel: "rotated-channel"}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte("# kept\nport: 8317\nmodel-routing:\n  generation: 11\ndebug: false\n"), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}
	service := &Service{
		coreManager: coreauth.NewManager(nil, nil, coreauth.NoopHook{}),
		configPath:  configPath,
		cfg: &internalconfig.Config{ModelRouting: &modelrouting.Config{
			Generation: 11,
			DirectModels: []modelrouting.DirectModel{{
				ModelKey: modelKey, DisplayName: "rotated-model", Active: true,
				Routes: []modelrouting.DirectRoute{{
					RouteKey: routeKey, CatalogRouteProviderID: "rotated-channel", CatalogRouteModelID: "rotated-model",
					RuntimeModelID: "rotated-model", RouteSelector: modelrouting.SelectorForRoute(routeKey, "rotated-model"),
					QuotaDomains:   []string{"rotated-quota"},
					CredentialRefs: []modelrouting.CredentialRef{{ID: coreauth.CredentialReferenceID("key-before-rotation"), Kind: "api_key"}},
					Protocols:      []string{"openai_chat"},
					Health:         modelrouting.Health{Status: "healthy", Selectable: true},
					Selectable:     true,
				}},
			}},
		}},
	}

	if errInit := service.initializeModelRouting(); errInit != nil {
		t.Fatalf("initializeModelRouting with a stale projection = %v, want a started service", errInit)
	}
	if identity := service.modelRoutingIdentity(); identity != nil {
		t.Fatalf("active model-routing identity = %+v, want none", identity)
	}
	if service.cfg.ModelRouting != nil {
		t.Fatal("stale projection is still part of the running configuration")
	}
	persisted, errRead := os.ReadFile(configPath)
	if errRead != nil {
		t.Fatalf("read config: %v", errRead)
	}
	if got, want := string(persisted), "# kept\nport: 8317\ndebug: false\n"; got != want {
		t.Fatalf("persisted config = %q, want %q", got, want)
	}
}
