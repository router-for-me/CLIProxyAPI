package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type capturedPluginLog struct {
	level          string
	message        string
	fields         map[string]any
	hostCallbackID string
}

func newTestRuntime(t *testing.T) *runtimeState {
	t.Helper()
	cfg, errCompile := decodeAndCompileConfig([]byte(`
aliases:
  - alias: Iterative-Model
    display_name: Iterative Model
    targets:
      - provider: codex
        model: terra
        repeat: 3
        efforts:
          low: {model: luna}
          medium: xhigh
          high: {model: luna, effort: xhigh}
          xhigh: {model: "luna(high)", effort: medium}
      - {provider: claude, model: "opus(high)"}
`), 1)
	if errCompile != nil {
		t.Fatal(errCompile)
	}
	runtime := newRuntimeState(func() time.Time { return time.Unix(100, 0) })
	runtime.cursors.random = func(int) int { return 0 }
	runtime.config.Store(cfg)
	return runtime
}

func TestRouterMatchingSuffixAndSequence(t *testing.T) {
	runtime := newTestRuntime(t)
	unmatched := runtime.route(pluginapi.ModelRouteRequest{RequestedModel: "other", AvailableProviders: []string{"codex"}})
	if unmatched.Handled {
		t.Fatalf("unmatched response = %#v", unmatched)
	}
	base := pluginapi.ModelRouteRequest{
		RequestedModel:     "ITERATIVE-MODEL(max)",
		AvailableProviders: []string{"codex", "claude"},
		Headers:            http.Header{"Session-Id": {"session-a"}},
	}
	first := runtime.route(base)
	if !first.Handled || first.Target != "codex" || first.TargetModel != "terra(max)" || first.Reason != routeReason {
		t.Fatalf("first response = %#v", first)
	}
	second := runtime.route(base)
	third := runtime.route(base)
	fourth := runtime.route(base)
	if second.TargetModel != "terra(max)" || third.TargetModel != "terra(max)" {
		t.Fatalf("second/third = %#v / %#v", second, third)
	}
	if fourth.Target != "claude" || fourth.TargetModel != "opus(high)" {
		t.Fatalf("configured suffix did not win: %#v", fourth)
	}
}

func TestRouterAppliesSlotEffortTiers(t *testing.T) {
	runtime := newTestRuntime(t)
	cases := map[string]string{
		"iterative-model(medium)":  "terra(xhigh)",
		"iterative-model(low)":     "luna(low)",
		"iterative-model(high)":    "luna(xhigh)",
		"iterative-model(xhigh)":   "luna(high)",
		"iterative-model(minimal)": "terra(minimal)",
		"iterative-model(max)":     "terra(max)",
		"iterative-model(8000)":    "terra(8000)",
		"iterative-model(auto)":    "terra(auto)",
		"iterative-model(none)":    "terra(none)",
		"iterative-model":          "terra",
	}
	for requested, want := range cases {
		t.Run(requested, func(t *testing.T) {
			got := runtime.route(pluginapi.ModelRouteRequest{
				RequestedModel:     requested,
				AvailableProviders: []string{"codex", "claude"},
			})
			if !got.Handled || got.Target != "codex" || got.TargetModel != want {
				t.Fatalf("route(%q) = %#v, want codex/%s", requested, got, want)
			}
		})
	}
}

func TestRouterUsesConfiguredRandomStart(t *testing.T) {
	runtime := newTestRuntime(t)
	randomCalls := 0
	runtime.cursors.random = func(limit int) int {
		randomCalls++
		if limit != 4 {
			t.Fatalf("random limit = %d, want 4", limit)
		}
		return 3
	}
	req := pluginapi.ModelRouteRequest{
		RequestedModel:     "iterative-model",
		AvailableProviders: []string{"codex", "claude"},
		Headers:            http.Header{"X-Session-ID": {"random-session"}},
	}
	first := runtime.route(req)
	second := runtime.route(req)
	if first.Target != "claude" || first.TargetModel != "opus(high)" {
		t.Fatalf("random first route = %#v", first)
	}
	if second.Target != "codex" || second.TargetModel != "terra" {
		t.Fatalf("route after random start = %#v", second)
	}
	if randomCalls != 1 {
		t.Fatalf("random calls = %d, want 1", randomCalls)
	}
}

func TestRouterSessionsAndProviderSkipping(t *testing.T) {
	runtime := newTestRuntime(t)
	reqA := pluginapi.ModelRouteRequest{
		RequestedModel:     "iterative-model",
		AvailableProviders: []string{"codex", "claude"},
		Body:               []byte(`{"metadata":{"user_id":"user_x_account__session_aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}}`),
	}
	reqB := reqA
	reqB.Body = []byte(`{"metadata":{"user_id":"user_x_account__session_ffffffff-bbbb-cccc-dddd-eeeeeeeeeeee"}}`)
	for range 2 {
		if got := runtime.route(reqA); got.Target != "codex" {
			t.Fatalf("session A route = %#v", got)
		}
	}
	if got := runtime.route(reqB); got.TargetModel != "terra" {
		t.Fatalf("session B did not start independently: %#v", got)
	}
	skip := pluginapi.ModelRouteRequest{
		RequestedModel:     "iterative-model",
		AvailableProviders: []string{"claude"},
		Body:               []byte(`{"conversation_id":"skip-conversation"}`),
	}
	if got := runtime.route(skip); got.Target != "claude" {
		t.Fatalf("provider skip route = %#v", got)
	}
	if got := runtime.route(pluginapi.ModelRouteRequest{
		RequestedModel:     "iterative-model",
		AvailableProviders: []string{"codex", "claude"},
		Body:               []byte(`{"conversation_id":"skip-conversation"}`),
	}); got.Target != "codex" {
		t.Fatalf("skip cursor did not wrap after selected target: %#v", got)
	}
}

func TestRouterRecognizesPromptCacheAndMessageHash(t *testing.T) {
	runtime := newTestRuntime(t)
	for _, body := range [][]byte{
		[]byte(`{"prompt_cache_key":"cache-a"}`),
		[]byte(`{"messages":[{"role":"user","content":"stable opening"}]}`),
	} {
		req := pluginapi.ModelRouteRequest{
			RequestedModel:     "iterative-model",
			AvailableProviders: []string{"codex", "claude"},
			Body:               body,
		}
		first := runtime.route(req)
		second := runtime.route(req)
		if first.TargetModel != "terra" || second.TargetModel != "terra" {
			t.Fatalf("stable session routes = %#v / %#v", first, second)
		}
	}
}

func TestRouterStatelessAndUnavailable(t *testing.T) {
	runtime := newTestRuntime(t)
	req := pluginapi.ModelRouteRequest{RequestedModel: "iterative-model", AvailableProviders: []string{"codex"}}
	for range 3 {
		if got := runtime.route(req); got.TargetModel != "terra" {
			t.Fatalf("stateless response = %#v", got)
		}
	}
	req.AvailableProviders = nil
	if got := runtime.route(req); got.Handled {
		t.Fatalf("unavailable response = %#v", got)
	}
}

func TestRouterLogsSelectionThroughHostWithoutSensitiveIdentifiers(t *testing.T) {
	runtime := newTestRuntime(t)
	logs := make([]capturedPluginLog, 0, 1)
	runtime.logger = func(level, message string, fields map[string]any, hostCallbackID string) {
		logs = append(logs, capturedPluginLog{
			level: level, message: message, fields: fields, hostCallbackID: hostCallbackID,
		})
	}
	const sessionID = "private-session-identifier"
	resp := runtime.routeWithCallback(pluginapi.ModelRouteRequest{
		RequestedModel:     "iterative-model(max)",
		AvailableProviders: []string{"codex", "claude"},
		Headers: http.Header{
			"X-Session-ID":  {sessionID},
			"Authorization": {"Bearer private-token"},
		},
		Body: []byte(`{"messages":[{"role":"user","content":"private prompt"}]}`),
	}, "callback-123")
	if !resp.Handled || len(logs) != 1 {
		t.Fatalf("response/logs = %#v / %#v", resp, logs)
	}
	got := logs[0]
	wantMessage := "model-sequence-router: selected target alias=Iterative-Model position=0 requested=max"
	if got.level != "debug" || got.message != wantMessage || got.hostCallbackID != "callback-123" {
		t.Fatalf("log identity = %#v", got)
	}
	if got.fields["alias"] != "Iterative-Model" || got.fields["provider"] != "codex" ||
		got.fields["model"] != "terra(max)" ||
		got.fields["advanced"] != true || got.fields["random_start"] != true || got.fields["sequence_index"] != 0 ||
		got.fields["requested_effort"] != "max" {
		t.Fatalf("log fields = %#v", got.fields)
	}
	if got.fields["session_hash"] != shortSessionHash("header:"+sessionID) {
		t.Fatalf("session hash = %#v", got.fields["session_hash"])
	}
	raw, errMarshal := json.Marshal(map[string]any{
		"level": got.level, "message": got.message, "fields": got.fields, "host_callback_id": got.hostCallbackID,
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	for _, secret := range []string{sessionID, "private-token", "private prompt"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("log contains sensitive value %q: %s", secret, raw)
		}
	}
}

func TestRegisteredModels(t *testing.T) {
	runtime := newTestRuntime(t)
	models := runtime.registeredModels()
	if models.Provider != pluginIdentifier || len(models.Models) != 1 {
		t.Fatalf("registered models = %#v", models)
	}
	model := models.Models[0]
	if model.ID != "Iterative-Model" || model.DisplayName != "Iterative Model" ||
		model.OwnedBy != pluginIdentifier || model.Type != "model-sequence" || !model.UserDefined {
		t.Fatalf("model = %#v", model)
	}
	if model.Thinking != nil || len(model.SupportedParameters) != 0 || model.ContextLength != 0 {
		t.Fatalf("heterogeneous capabilities advertised: %#v", model)
	}
}
