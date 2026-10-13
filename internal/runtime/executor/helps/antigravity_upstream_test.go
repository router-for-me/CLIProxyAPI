package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAntigravityUpstreamModelIsAccountScoped(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	low := &cliproxyauth.Auth{ID: "upstream-variant-low"}
	high := &cliproxyauth.Auth{ID: "upstream-variant-high"}
	for _, tc := range []struct {
		auth    *cliproxyauth.Auth
		variant string
	}{{low, "gemini-3.8-flash-low"}, {high, "gemini-3.8-flash-high"}} {
		reg.RegisterClient(tc.auth.ID, "antigravity", []*registry.ModelInfo{{
			ID: "account/flash-alias", MetadataModelID: "gemini-3.8-flash", UpstreamModelID: tc.variant,
		}})
		t.Cleanup(func() { reg.UnregisterClient(tc.auth.ID) })
	}
	for _, tc := range []struct {
		auth *cliproxyauth.Auth
		want string
	}{{low, "gemini-3.8-flash-low"}, {high, "gemini-3.8-flash-high"}} {
		for _, model := range []string{"account/flash-alias", "gemini-3.8-flash"} {
			if got := AntigravityUpstreamModel(tc.auth, model); got != tc.want {
				t.Fatalf("auth=%s model=%s: upstream=%s, want %s", tc.auth.ID, model, got, tc.want)
			}
		}
	}
	if got := AntigravityUpstreamModel(nil, "gemini-3.8-flash"); got != "gemini-3.8-flash-high" {
		t.Fatalf("unknown account fallback=%q", got)
	}
}
