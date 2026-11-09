package payloadcompress

import (
	"strings"
	"testing"
)

// capturePayloadEngine records each invocation and can shrink the body.
type capturePayloadEngine struct {
	label  string
	calls  *[]string
	shrink bool
}

func (c capturePayloadEngine) name() string { return c.label }

func (c capturePayloadEngine) applyBody(body []byte) []byte {
	*c.calls = append(*c.calls, c.label)
	if c.shrink {
		return []byte("{}")
	}
	return body
}

// withPayloadFactories swaps payload engine factories for the test and restores
// them afterwards.
func withPayloadFactories(t *testing.T, factories map[string]func() payloadEngine) {
	t.Helper()
	orig := make(map[string]func() payloadEngine, len(factories))
	for name := range factories {
		orig[name] = payloadEngineFactories[name]
		payloadEngineFactories[name] = factories[name]
	}
	t.Cleanup(func() {
		for name, fn := range orig {
			payloadEngineFactories[name] = fn
		}
	})
}

func TestPayloadEnginesRegistered(t *testing.T) {
	names := []string{
		EngineAggressive, EngineUltra, EngineCCR,
		EngineCodexResponses, EngineIonizer, EngineLLM,
	}
	for _, name := range names {
		factory, ok := payloadEngineFactories[name]
		if !ok {
			t.Fatalf("%s has no payload engine factory", name)
		}
		if got := factory().name(); got != name {
			t.Fatalf("factory for %s reports name %q", name, got)
		}
		if !knownEngine(name) {
			t.Fatalf("%s rejected by knownEngine", name)
		}
	}
}

func TestPayloadEnginesReachableThroughApply(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	names := []string{
		EngineAggressive, EngineUltra, EngineCCR,
		EngineCodexResponses, EngineIonizer, EngineLLM,
	}
	for _, name := range names {
		var calls []string
		withPayloadFactories(t, map[string]func() payloadEngine{
			name: func() payloadEngine {
				return capturePayloadEngine{label: name, calls: &calls, shrink: true}
			},
		})
		_, stats := Apply(testConfig(name), "m", body)
		if len(calls) != 1 || calls[0] != name {
			t.Fatalf("%s not reached through Apply: calls=%v", name, calls)
		}
		if got := stats.Saved[name]; got != len(body)-2 {
			t.Fatalf("%s savings not attributed: got %d", name, got)
		}
	}
}

func TestPayloadEnginesRunInPipelineOrder(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	pipeline := []string{EngineLLM, EngineAggressive, EngineUltra, EngineIonizer}
	var calls []string
	factories := make(map[string]func() payloadEngine, len(pipeline))
	for _, name := range pipeline {
		name := name
		factories[name] = func() payloadEngine {
			return capturePayloadEngine{label: name, calls: &calls}
		}
	}
	withPayloadFactories(t, factories)

	Apply(testConfig(pipeline...), "m", body)
	if got, want := strings.Join(calls, ","), strings.Join(pipeline, ","); got != want {
		t.Fatalf("pipeline order: got %s want %s", got, want)
	}
}
