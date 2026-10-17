package pluginhost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestNativeUnsupportedMethodAdvertisesAllAllowedMethods(t *testing.T) {
	plugin := &nativePluginDouble{}
	plugin.routes = []pluginapi.NativeRoute{
		{Method: http.MethodGet, Path: "/v1/plugin/run", Handler: plugin},
		{Method: http.MethodPost, Path: "/v1/plugin/run", Handler: plugin},
	}
	host := newHostWithRecords(capabilityRecord{id: "native", plugin: pluginapi.Plugin{
		SchemaVersion: pluginabi.SchemaVersionNativeAPI,
		Capabilities:  pluginapi.Capabilities{NativeAPI: plugin},
	}})
	host.RegisterNativeRoutes(context.Background(), nil)
	recorder := httptest.NewRecorder()
	if !host.ServeNativeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/v1/plugin/run", nil)) {
		t.Fatal("registered path was not handled")
	}
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, POST" {
		t.Fatalf("unsupported method response: status=%d Allow=%q", recorder.Code, recorder.Header().Get("Allow"))
	}
	if plugin.request.Method != "" {
		t.Fatal("unsupported method reached the plugin")
	}
}
