package pluginhost

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type schemaVersionTrackingClient struct {
	pluginClient
	schemaVersion uint32
}

func (c *schemaVersionTrackingClient) setSchemaVersion(schemaVersion uint32) {
	c.schemaVersion = schemaVersion
}

func TestRegisterRPCPluginKeepsSDKDefaultAtSchema1(t *testing.T) {
	lookup := newTestSymbolLookup(&testPlugin{
		registerResult: validTestPlugin("default-schema"),
	})
	client := &schemaVersionTrackingClient{pluginClient: lookup}

	if _, errRegister := registerRPCPlugin(context.Background(), nil, "default-schema", client, pluginabi.MethodPluginRegister, nil); errRegister != nil {
		t.Fatalf("registerRPCPlugin() error = %v", errRegister)
	}
	if client.schemaVersion != pluginabi.SchemaVersionV1 {
		t.Fatalf("negotiated schema version = %d, want compatibility default %d", client.schemaVersion, pluginabi.SchemaVersionV1)
	}
}

func TestRegisterRPCPluginPropagatesSchemaVersionToCallbackContexts(t *testing.T) {
	plugin := validTestPlugin("callback-schema")
	plugin.Capabilities.ModelRouter = modelRouterFunc(func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
		return pluginapi.ModelRouteResponse{}, nil
	})
	lookup := newTestSymbolLookup(&testPlugin{registerResult: plugin})
	lookup.schemaVersion = pluginabi.SchemaVersionV1
	host := New()

	registered, errRegister := registerRPCPlugin(context.Background(), host, "callback-schema", lookup, pluginabi.MethodPluginRegister, nil)
	if errRegister != nil {
		t.Fatalf("registerRPCPlugin() error = %v", errRegister)
	}
	adapter, okAdapter := registered.Capabilities.ModelRouter.(*rpcPluginAdapter)
	if !okAdapter {
		t.Fatalf("ModelRouter type = %T, want *rpcPluginAdapter", registered.Capabilities.ModelRouter)
	}

	callbackID, cleanup := adapter.openHostCallbackContext(context.Background())
	defer cleanup()
	if got := host.callbackContexts.schemaVersion(callbackID); got != pluginabi.SchemaVersionV1 {
		t.Fatalf("callback schema version = %d, want %d", got, pluginabi.SchemaVersionV1)
	}
}
