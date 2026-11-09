package payloadcompress

import (
	"strings"
	"testing"
)

func readLifecycleConfig() Config {
	on := true
	return testConfigWith(StepOptions{ReadLifecycleEnabled: &on}, EngineReadLifecycle)
}

func testConfigWith(opts StepOptions, engines ...string) Config {
	steps := make([]Step, 0, len(engines))
	for _, name := range engines {
		steps = append(steps, Step{Engine: name, Options: opts})
	}
	return Config{
		Enabled:      true,
		MinBytes:     1,
		DefaultCombo: "t",
		Combos:       []Combo{{Name: "t", Models: []string{"*"}, Pipeline: steps}},
	}
}

func TestReadLifecycleStubsSupersededRead(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"file_path\":\"a.go\"}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"OLD A"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c2","type":"function","function":{"name":"read_file","arguments":"{\"file_path\":\"a.go\"}"}}]},` +
		`{"role":"tool","tool_call_id":"c2","content":"NEW A"}]}`)
	out := applyReadLifecycle(body)
	if !strings.Contains(string(out), "[read superseded") {
		t.Fatalf("stale read not stubbed: %s", out)
	}
	if !strings.Contains(string(out), "NEW A") {
		t.Fatalf("latest read lost: %s", out)
	}
	if strings.Contains(string(out), "OLD A") {
		t.Fatalf("stale content survived: %s", out)
	}
}

func TestReadLifecycleWriteInvalidatesEarlierRead(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","function":{"name":"read_file","arguments":"{\"file_path\":\"a.go\"}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"OLD A"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c2","function":{"name":"edit_file","arguments":"{\"file_path\":\"a.go\"}"}}]},` +
		`{"role":"tool","tool_call_id":"c2","content":"ok"}]}`)
	out := applyReadLifecycle(body)
	if strings.Contains(string(out), "OLD A") || !strings.Contains(string(out), "[read superseded") {
		t.Fatalf("edit did not invalidate read: %s", out)
	}
}

func TestReadLifecycleKeepsUnrelatedReads(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","function":{"name":"read_file","arguments":"{\"file_path\":\"a.go\"}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"A"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c2","function":{"name":"read_file","arguments":"{\"file_path\":\"b.go\"}"}}]},` +
		`{"role":"tool","tool_call_id":"c2","content":"B"}]}`)
	if got := applyReadLifecycle(body); string(got) != string(body) {
		t.Fatalf("unrelated read rewritten: %s", got)
	}
}

func TestReadLifecycleAnthropicShape(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a.go"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"OLD A"}]}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"a.go"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"NEW A"}]}]}`)
	out := applyReadLifecycle(body)
	if strings.Contains(string(out), "OLD A") || !strings.Contains(string(out), "[read superseded") {
		t.Fatalf("anthropic stale read not stubbed: %s", out)
	}
	if !strings.Contains(string(out), "NEW A") {
		t.Fatalf("latest read lost: %s", out)
	}
}

func TestReadLifecycleIdempotentAndNonChatUntouched(t *testing.T) {
	body := []byte(`{"messages":[` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","function":{"name":"read_file","arguments":"{\"file_path\":\"a.go\"}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"OLD"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c2","function":{"name":"read_file","arguments":"{\"file_path\":\"a.go\"}"}}]}]}`)
	once := applyReadLifecycle(body)
	twice := applyReadLifecycle(once)
	if string(once) != string(twice) {
		t.Fatalf("not idempotent:\n%s\n%s", once, twice)
	}
	other := []byte(`{"input":[{"role":"user","content":"hi"}]}`)
	if got := applyReadLifecycle(other); string(got) != string(other) {
		t.Fatalf("non-chat body rewritten: %s", got)
	}
}

func TestReadLifecycleDisabledByDefault(t *testing.T) {
	cfg := testConfigWith(StepOptions{}, EngineReadLifecycle)
	body := []byte(`{"messages":[` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","function":{"name":"read_file","arguments":"{\"file_path\":\"a.go\"}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"OLD"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c2","function":{"name":"read_file","arguments":"{\"file_path\":\"a.go\"}"}}]}]}`)
	out, stats := Apply(cfg, "gpt-4o", body)
	if string(out) != string(body) || stats.Applied {
		t.Fatalf("opt-in engine ran by default: %s %+v", out, stats)
	}
}

func TestReadLifecycleAppliedThroughApply(t *testing.T) {
	cfg := readLifecycleConfig()
	long := strings.Repeat("old file content ", 40)
	body := []byte(`{"messages":[` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","function":{"name":"read_file","arguments":"{\"file_path\":\"a.go\"}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"` + long + `"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c2","function":{"name":"read_file","arguments":"{\"file_path\":\"a.go\"}"}}]},` +
		`{"role":"tool","tool_call_id":"c2","content":"NEW"}]}`)
	out, stats := Apply(cfg, "gpt-4o", body)
	if !stats.Applied || !strings.Contains(string(out), "[read superseded") {
		t.Fatalf("prepass not applied: %+v %s", stats, out)
	}
	if stats.Saved[EngineReadLifecycle] <= 0 {
		t.Fatalf("saving not reported: %+v", stats.Saved)
	}
}
