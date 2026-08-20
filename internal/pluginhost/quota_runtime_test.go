package pluginhost

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestHostAuthGetRuntimeIncludesPerModelQuotaResetState(t *testing.T) {
	next := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	auth := &coreauth.Auth{
		ID:       "quota.json",
		Provider: "demo",
		FileName: "quota.json",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"runtime_only": "true",
		},
		ModelStates: map[string]*coreauth.ModelState{
			"demo-model": {
				Unavailable:    true,
				NextRetryAfter: next,
				Quota: coreauth.QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: next,
				},
				UpdatedAt: next.Add(-time.Minute),
			},
		},
	}
	auth.EnsureIndex()

	host := New()
	host.SetAuthManager(coreauth.NewManager(nil, nil, nil))
	if _, errRegister := host.currentAuthManager().Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	request, errMarshal := json.Marshal(pluginapi.HostAuthGetRequest{AuthIndex: auth.Index})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	raw, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthGetRuntime, request)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	response, errDecode := decodeRPCEnvelope[pluginapi.HostAuthGetRuntimeResponse](raw)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if len(response.Auth.ModelStates) != 1 {
		t.Fatalf("model states = %#v, want one model", response.Auth.ModelStates)
	}
	state, ok := response.Auth.ModelStates["demo-model"]
	if !ok || !state.NextReset.Equal(next) {
		t.Fatalf("demo-model state = %#v, want reset %v", state, next)
	}
}
