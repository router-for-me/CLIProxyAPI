package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
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
	if !route.Handled || route.TargetModel != "terra" {
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
	first := []byte("aliases:\n- alias: first\n  random_start: false\n  targets: [{provider: codex, model: one}, {provider: codex, model: two}]")
	if errConfigure := runtime.configure(first); errConfigure != nil {
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
	second := []byte("aliases:\n- alias: first\n  random_start: false\n  targets: [{provider: codex, model: one}, {provider: codex, model: two}]\n- alias: second\n  random_start: false\n  targets: [{provider: claude, model: opus}]")
	if errConfigure := runtime.configure(second); errConfigure != nil {
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

func TestExecutorMethodsReportRetryableUnavailability(t *testing.T) {
	methods := []string{
		pluginabi.MethodExecutorExecute,
		pluginabi.MethodExecutorExecuteStream,
		pluginabi.MethodExecutorCountTokens,
		pluginabi.MethodExecutorHTTPRequest,
	}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			raw, errHandle := handleMethod(method, nil)
			if errHandle != nil {
				t.Fatalf("%s error = %v", method, errHandle)
			}
			var wrapped envelope
			if errUnmarshal := json.Unmarshal(raw, &wrapped); errUnmarshal != nil {
				t.Fatal(errUnmarshal)
			}
			if wrapped.OK || wrapped.Error == nil {
				t.Fatalf("%s envelope = %#v", method, wrapped)
			}
			if wrapped.Error.Code != unavailableCode || wrapped.Error.HTTPStatus != unavailableStatus {
				t.Fatalf("%s error = %#v", method, wrapped.Error)
			}
		})
	}
}

func TestExecutorIdentifierNamesPlugin(t *testing.T) {
	raw, errHandle := handleMethod(pluginabi.MethodExecutorIdentifier, nil)
	if errHandle != nil {
		t.Fatalf("identifier error = %v", errHandle)
	}
	var wrapped envelope
	if errUnmarshal := json.Unmarshal(raw, &wrapped); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	var identity executorIdentifier
	if errUnmarshal := json.Unmarshal(wrapped.Result, &identity); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !wrapped.OK || identity.Identifier != pluginIdentifier {
		t.Fatalf("identifier envelope = %#v / %#v", wrapped, identity)
	}
}

func TestExecutorCapabilityFollowsUnavailablePolicy(t *testing.T) {
	const targets = "\n  targets: [{provider: codex, model: one}, {provider: claude, model: two}]"
	skipped, errSkip := decodeAndCompileConfig([]byte("aliases:\n- alias: routed"+targets), 1)
	if errSkip != nil {
		t.Fatal(errSkip)
	}
	skipRegistration := pluginRegistration(skipped)
	if skipRegistration.Capabilities.Executor ||
		skipRegistration.Capabilities.ExecutorModelScope != "" ||
		len(skipRegistration.Capabilities.ExecutorInputFormats) != 0 ||
		len(skipRegistration.Capabilities.ExecutorOutputFormats) != 0 {
		t.Fatalf("skip capabilities = %#v", skipRegistration.Capabilities)
	}
	if !skipRegistration.Capabilities.ModelRegistrar || !skipRegistration.Capabilities.ModelRouter || !skipRegistration.Capabilities.UsagePlugin {
		t.Fatalf("skip capabilities dropped routing = %#v", skipRegistration.Capabilities)
	}

	reported, errReported := decodeAndCompileConfig([]byte("unavailable_provider: error\naliases:\n- alias: routed"+targets), 1)
	if errReported != nil {
		t.Fatal(errReported)
	}
	capabilities := pluginRegistration(reported).Capabilities
	if !capabilities.Executor || capabilities.ExecutorModelScope != pluginapi.ExecutorModelScopeStatic {
		t.Fatalf("error capabilities = %#v", capabilities)
	}
	wantFormats := []string{
		string(translator.FormatOpenAI),
		string(translator.FormatOpenAIResponse),
		string(translator.FormatClaude),
		string(translator.FormatGemini),
		string(translator.FormatCodex),
		string(translator.FormatAntigravity),
		string(translator.FormatInteractions),
	}
	for _, formats := range [][]string{capabilities.ExecutorInputFormats, capabilities.ExecutorOutputFormats} {
		if len(formats) != len(wantFormats) {
			t.Fatalf("declared formats = %#v, want %#v", formats, wantFormats)
		}
		for index, want := range wantFormats {
			if formats[index] != want {
				t.Fatalf("format[%d] = %q, want %q", index, formats[index], want)
			}
		}
	}
}
