package pluginhost

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestRPCCapabilitiesIncludeFrontendAuthProviderExclusive(t *testing.T) {
	plugin := pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			FrontendAuthProvider:          frontendAuthProviderFunc{identifier: "exclusive-auth"},
			FrontendAuthProviderExclusive: true,
		},
	}

	caps := rpcCapabilitiesFromPlugin(plugin)
	if !caps.FrontendAuthProvider {
		t.Fatal("FrontendAuthProvider = false, want true")
	}
	if !caps.FrontendAuthProviderExclusive {
		t.Fatal("FrontendAuthProviderExclusive = false, want true")
	}

	raw, errMarshal := json.Marshal(caps)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	if !json.Valid(raw) {
		t.Fatalf("marshaled capabilities are invalid JSON: %s", raw)
	}
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(raw, &decoded); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if decoded["frontend_auth_provider_exclusive"] != true {
		t.Fatalf("frontend_auth_provider_exclusive = %#v, want true", decoded["frontend_auth_provider_exclusive"])
	}
}
