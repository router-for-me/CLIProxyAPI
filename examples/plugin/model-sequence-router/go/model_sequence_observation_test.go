package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// captureRouteLogs redirects plugin logging into a slice the caller inspects.
func captureRouteLogs(runtime *runtimeState) *[]capturedPluginLog {
	logs := make([]capturedPluginLog, 0, 4)
	runtime.logger = func(level, message string, fields map[string]any, hostCallbackID string) {
		logs = append(logs, capturedPluginLog{
			level: level, message: message, fields: fields, hostCallbackID: hostCallbackID,
		})
	}
	return &logs
}

func TestRouterLogsSelectionThroughHostWithoutSensitiveIdentifiers(t *testing.T) {
	runtime := newTestRuntime(t)
	logs := captureRouteLogs(runtime)
	resp := runtime.routeWithCallback(pluginapi.ModelRouteRequest{
		RequestedModel:     "iterative-model(max)",
		AvailableProviders: []string{"codex", "claude"},
		Headers:            http.Header{"Authorization": {"Bearer private-token"}},
		Body:               []byte(`{"messages":[{"role":"user","content":"private prompt"}]}`),
	}, "callback-123")
	if !resp.Handled || len(*logs) != 1 {
		t.Fatalf("response/logs = %#v / %#v", resp, *logs)
	}
	got := (*logs)[0]
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
	if got.fields["session_hash"] == "" {
		t.Fatalf("route record dropped the conversation hash: %#v", got.fields)
	}
	raw, errMarshal := json.Marshal(map[string]any{
		"level": got.level, "message": got.message, "fields": got.fields, "host_callback_id": got.hostCallbackID,
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	for _, secret := range []string{"private-token", "private prompt"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("log contains sensitive value %q: %s", secret, raw)
		}
	}
}

func TestRouterAdvancesOncePerConversationState(t *testing.T) {
	runtime := newTestRuntime(t)
	logs := captureRouteLogs(runtime)
	countRequest := pluginapi.ModelRouteRequest{
		RequestedModel:     "iterative-model",
		AvailableProviders: []string{"codex", "claude"},
		Body:               []byte(`{"messages":[{"role":"user","content":"first turn"}],"max_tokens":1}`),
	}
	if got := runtime.route(countRequest); got.Target != "codex" {
		t.Fatalf("count-shaped call = %#v", got)
	}
	generationRequest := countRequest
	generationRequest.Body = []byte(`{"messages":[{"role":"user","content":"first turn"}],"max_tokens":512}`)
	generationRequest.Stream = true
	if got := runtime.route(generationRequest); got.Target != "codex" {
		t.Fatalf("generation call = %#v", got)
	}
	changed := generationRequest
	changed.Body = []byte(`{"messages":[{"role":"user","content":"first turn"},{"role":"assistant","content":"reply"},{"role":"user","content":"second turn"}]}`)
	if got := runtime.route(changed); got.Target != "codex" {
		t.Fatalf("changed state call = %#v", got)
	}
	wantOutcomes := []string{"advanced", "replayed", "advanced"}
	wantIndexes := []int{0, 0, 1}
	if len(*logs) != len(wantOutcomes) {
		t.Fatalf("route records = %d, want %d", len(*logs), len(wantOutcomes))
	}
	for index, want := range wantOutcomes {
		record := (*logs)[index]
		if record.fields["outcome"] != want || record.fields["sequence_index"] != wantIndexes[index] {
			t.Fatalf("record %d = %#v, want %s at %d", index, record.fields, want, wantIndexes[index])
		}
		if record.fields["identity_source"] != string(identitySourceDerived) {
			t.Fatalf("record %d identity source = %#v", index, record.fields["identity_source"])
		}
	}
}

func TestRouterEmitsOneNoticePerSkippedPosition(t *testing.T) {
	runtime := newTestRuntime(t)
	logs := captureRouteLogs(runtime)
	got := runtime.route(pluginapi.ModelRouteRequest{
		RequestedModel:     "iterative-model",
		AvailableProviders: []string{"claude"},
		Body:               []byte(`{"messages":[{"role":"user","content":"skip"}]}`),
	})
	if got.Target != "claude" {
		t.Fatalf("skip route = %#v", got)
	}
	notices := make([]capturedPluginLog, 0, 3)
	for _, record := range *logs {
		if record.fields["event"] == "skip" {
			notices = append(notices, record)
		}
	}
	if len(notices) != 3 {
		t.Fatalf("skip notices = %d, want 3", len(notices))
	}
	for index, notice := range notices {
		if notice.level != "warn" || notice.fields["provider"] != "codex" || notice.fields["sequence_index"] != index {
			t.Fatalf("notice %d = %#v", index, notice.fields)
		}
	}
}

func TestErrorPolicyKeepsPositionAndSelfTargets(t *testing.T) {
	cfg, errCompile := decodeAndCompileConfig([]byte(`
unavailable_provider: error
aliases:
  - alias: held
    random_start: false
    targets: [{provider: codex, model: one}, {provider: claude, model: two}]
`), 1)
	if errCompile != nil {
		t.Fatal(errCompile)
	}
	runtime := newRuntimeState(func() time.Time { return time.Unix(100, 0) })
	runtime.config.Store(cfg)
	logs := captureRouteLogs(runtime)
	req := pluginapi.ModelRouteRequest{
		RequestedModel:     "held",
		AvailableProviders: []string{"claude"},
		Body:               []byte(`{"messages":[{"role":"user","content":"held turn"}]}`),
	}
	held := runtime.route(req)
	if !held.Handled || held.TargetKind != pluginapi.ModelRouteTargetSelf || held.Reason != unavailableCode {
		t.Fatalf("held response = %#v", held)
	}
	if len(*logs) != 1 {
		t.Fatalf("held records = %#v", *logs)
	}
	record := (*logs)[0]
	if record.level != "warn" || record.fields["provider"] != "codex" || record.fields["sequence_index"] != 0 {
		t.Fatalf("held record = %#v", record.fields)
	}
	req.AvailableProviders = []string{"codex", "claude"}
	recovered := runtime.route(req)
	if recovered.Target != "codex" || recovered.TargetModel != "one" {
		t.Fatalf("recovered response = %#v, want the kept position", recovered)
	}
}
