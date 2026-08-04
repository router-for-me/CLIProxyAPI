package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestRPCRegistrationRouteReconfigureAndShutdown(t *testing.T) {
	previous := runtimePlugin
	runtimePlugin = newRuntimeState(nil)
	t.Cleanup(func() {
		runtimePlugin.shutdown()
		runtimePlugin = previous
	})
	config := []byte("aliases:\n- alias: routed\n  random_start: false\n  targets: [{provider: codex, model: terra}, {provider: codex, model: second}]")
	lifecycleRaw, _ := json.Marshal(lifecycleRequest{ConfigYAML: config})
	if _, errRegister := handleMethod(pluginabi.MethodPluginRegister, lifecycleRaw); errRegister != nil {
		t.Fatalf("register error = %v", errRegister)
	}
	routeRaw, _ := json.Marshal(pluginapi.ModelRouteRequest{
		RequestedModel:     "routed",
		AvailableProviders: []string{"codex"},
		Headers:            map[string][]string{"X-Session-ID": {"rpc-session"}},
	})
	raw, errRoute := handleMethod(pluginabi.MethodModelRoute, routeRaw)
	if errRoute != nil {
		t.Fatalf("route error = %v", errRoute)
	}
	var wrapped envelope
	if errUnmarshal := json.Unmarshal(raw, &wrapped); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	var route pluginapi.ModelRouteResponse
	if errUnmarshal := json.Unmarshal(wrapped.Result, &route); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !route.Handled || route.ResponseModel != "routed" {
		t.Fatalf("route = %#v", route)
	}
	before := runtimePlugin.loadedConfig()
	badRaw, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte("aliases: []")})
	if _, errReconfigure := handleMethod(pluginabi.MethodPluginReconfigure, badRaw); errReconfigure == nil {
		t.Fatal("invalid reconfigure succeeded")
	}
	if runtimePlugin.loadedConfig() != before {
		t.Fatal("failed reconfiguration replaced active config")
	}
	raw, errRoute = handleMethod(pluginabi.MethodModelRoute, routeRaw)
	if errRoute != nil {
		t.Fatalf("route after failed reconfiguration error = %v", errRoute)
	}
	if errUnmarshal := json.Unmarshal(raw, &wrapped); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if errUnmarshal := json.Unmarshal(wrapped.Result, &route); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if route.TargetModel != "second" {
		t.Fatalf("failed reconfiguration reset cursor: route = %#v", route)
	}
	if _, errShutdown := handleMethod(pluginabi.MethodPluginShutdown, nil); errShutdown != nil {
		t.Fatalf("shutdown error = %v", errShutdown)
	}
	if _, errShutdown := handleMethod(pluginabi.MethodPluginShutdown, nil); errShutdown != nil {
		t.Fatalf("second shutdown error = %v", errShutdown)
	}
}

func TestSuccessfulReconfigureResetsStateAndModels(t *testing.T) {
	runtime := newRuntimeState(nil)
	firstRaw, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte("aliases:\n- alias: first\n  random_start: false\n  targets: [{provider: codex, model: one}, {provider: codex, model: two}]")})
	if errConfigure := runtime.configure(firstRaw); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	req := pluginapi.ModelRouteRequest{
		RequestedModel:     "first",
		AvailableProviders: []string{"codex"},
		Headers:            map[string][]string{"X-Session-ID": {"session"}},
	}
	_ = runtime.route(req)
	if got := runtime.route(req); got.TargetModel != "two" {
		t.Fatalf("pre-reconfigure route = %#v", got)
	}
	secondRaw, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte("aliases:\n- alias: first\n  random_start: false\n  targets: [{provider: codex, model: one}, {provider: codex, model: two}]\n- alias: second\n  random_start: false\n  targets: [{provider: claude, model: opus}]")})
	if errConfigure := runtime.configure(secondRaw); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	if got := runtime.route(req); got.TargetModel != "one" {
		t.Fatalf("successful reconfiguration did not reset cursor: %#v", got)
	}
	models := runtime.registeredModels()
	if len(models.Models) != 2 || models.Models[0].ID != "first" || models.Models[1].ID != "second" {
		t.Fatalf("reconfigured models = %#v", models)
	}
	runtime.shutdown()
	runtime.shutdown()
}
