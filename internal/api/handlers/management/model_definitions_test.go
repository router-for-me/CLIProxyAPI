package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type staticPluginModelProvider struct {
	provider string
	models   []pluginapi.ModelInfo
}

func (p staticPluginModelProvider) StaticModels(context.Context, pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
	return pluginapi.ModelResponse{
		Provider: p.provider,
		Models:   p.models,
	}, nil
}

func (p staticPluginModelProvider) ModelsForAuth(context.Context, pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	return pluginapi.ModelResponse{}, nil
}

func invokeGetStaticModelDefinitions(t *testing.T, h *Handler, channel string) (*httptest.ResponseRecorder, struct {
	Channel string               `json:"channel"`
	Error   string               `json:"error"`
	Models  []registry.ModelInfo `json:"models"`
}) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{{Key: "channel", Value: channel}}
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/routing/model-definitions/"+channel, nil)
	h.GetStaticModelDefinitions(ctx)

	var body struct {
		Channel string               `json:"channel"`
		Error   string               `json:"error"`
		Models  []registry.ModelInfo `json:"models"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &body); errUnmarshal != nil {
		t.Fatalf("unmarshal response body %q: %v", rec.Body.String(), errUnmarshal)
	}
	return rec, body
}

func TestGetStaticModelDefinitions_BuiltInChannel(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)

	rec, body := invokeGetStaticModelDefinitions(t, h, "claude")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if body.Channel != "claude" {
		t.Fatalf("channel = %q, want %q", body.Channel, "claude")
	}
	if len(body.Models) == 0 {
		t.Fatal("expected non-empty models for built-in channel claude")
	}
}

func TestGetStaticModelDefinitions_FallsBackToPluginStaticModels(t *testing.T) {
	const provider = "workbuddy"
	reg := registry.GetGlobalRegistry()
	clientID := "workbuddy-cred-1.json"
	reg.RegisterClient(clientID, provider, []*registry.ModelInfo{
		{ID: "workbuddy-from-registry", DisplayName: "WorkBuddy Registry"},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	host := pluginhost.New()
	pluginID := "workbuddy-plugin"
	host.RegisterPluginForTest(pluginID, pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			ModelProvider: staticPluginModelProvider{
				provider: provider,
				models: []pluginapi.ModelInfo{
					{ID: "workbuddy-pro", DisplayName: "WorkBuddy Pro"},
					{ID: "workbuddy-lite", DisplayName: "WorkBuddy Lite"},
				},
			},
		},
	})
	host.RegisterModels(t.Context(), reg)
	host.RegisterExecutors(coreauth.NewManager(nil, nil, nil), reg)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	h.SetPluginHost(host)

	rec, body := invokeGetStaticModelDefinitions(t, h, "WorkBuddy")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if body.Channel != provider {
		t.Fatalf("channel = %q, want %q", body.Channel, provider)
	}
	if len(body.Models) != 2 || body.Models[0].ID != "workbuddy-pro" || body.Models[1].ID != "workbuddy-lite" {
		t.Fatalf("models = %#v, want [workbuddy-pro workbuddy-lite]", body.Models)
	}
}

func TestGetStaticModelDefinitions_FallsBackToRegistryModelsForPerAuthPlugin(t *testing.T) {
	const provider = "qoderwork"
	reg := registry.GetGlobalRegistry()
	clientID := "qoderwork-cred-1.json"
	reg.RegisterClient(clientID, provider, []*registry.ModelInfo{
		{ID: "qwen3.8-plus-QoderWork", DisplayName: "Qwen 3.8 Plus"},
		{ID: "qwen3.8-flash-QoderWork", DisplayName: "Qwen 3.8 Flash"},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	h.SetPluginHost(pluginhost.New())

	rec, body := invokeGetStaticModelDefinitions(t, h, "QoderWork")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if body.Channel != provider {
		t.Fatalf("channel = %q, want %q", body.Channel, provider)
	}
	if len(body.Models) != 2 || body.Models[0].ID != "qwen3.8-flash-QoderWork" || body.Models[1].ID != "qwen3.8-plus-QoderWork" {
		t.Fatalf("models = %#v, want [qwen3.8-flash-QoderWork qwen3.8-plus-QoderWork]", body.Models)
	}
}

func TestGetStaticModelDefinitions_RejectsUnknownChannel(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	h.SetPluginHost(pluginhost.New())

	rec, body := invokeGetStaticModelDefinitions(t, h, "nonexistent-provider")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if body.Error != "unknown channel" || body.Channel != "nonexistent-provider" {
		t.Fatalf("body = %#v, want unknown channel error for nonexistent-provider", body)
	}
}
