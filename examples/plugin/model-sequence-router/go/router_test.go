package main

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// capturedPluginLog records one log entry the plugin emitted through its host.
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
	sequenceTurn := func(turns int) pluginapi.ModelRouteRequest {
		return pluginapi.ModelRouteRequest{
			RequestedModel:     "ITERATIVE-MODEL(max)",
			AvailableProviders: []string{"codex", "claude"},
			Body:               messagesTurn(t, "sequence", turns),
		}
	}
	first := runtime.route(sequenceTurn(1))
	if !first.Handled || first.Target != "codex" || first.TargetModel != "terra(max)" || first.Reason != routeReason {
		t.Fatalf("first response = %#v", first)
	}
	second := runtime.route(sequenceTurn(2))
	third := runtime.route(sequenceTurn(3))
	fourth := runtime.route(sequenceTurn(4))
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
		Body:               messagesTurn(t, "random", 1),
	}
	first := runtime.route(req)
	req.Body = messagesTurn(t, "random", 2)
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
		Body:               messagesTurn(t, "conversation-a", 1),
	}
	reqB := reqA
	reqB.Body = messagesTurn(t, "conversation-b", 1)
	for range 2 {
		if got := runtime.route(reqA); got.Target != "codex" {
			t.Fatalf("conversation A route = %#v", got)
		}
	}
	if got := runtime.route(reqB); got.TargetModel != "terra" {
		t.Fatalf("conversation B did not start independently: %#v", got)
	}
	skip := pluginapi.ModelRouteRequest{
		RequestedModel:     "iterative-model",
		AvailableProviders: []string{"claude"},
		Body:               messagesTurn(t, "skip-conversation", 1),
	}
	if got := runtime.route(skip); got.Target != "claude" {
		t.Fatalf("provider skip route = %#v", got)
	}
	if got := runtime.route(pluginapi.ModelRouteRequest{
		RequestedModel:     "iterative-model",
		AvailableProviders: []string{"codex", "claude"},
		Body:               messagesTurn(t, "skip-conversation", 2),
	}); got.Target != "codex" {
		t.Fatalf("skip cursor did not wrap after selected target: %#v", got)
	}
}

// TestRouterHoldsPositionForUnchangedRequests proves a repeated conversation state
// keeps its position, while a request carrying no conversation content routes without
// a cursor.
func TestRouterHoldsPositionForUnchangedRequests(t *testing.T) {
	runtime := newTestRuntime(t)
	for _, body := range [][]byte{
		[]byte(`{"prompt_cache_key":"cache-a"}`),
		messagesTurn(t, "stable", 1),
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
