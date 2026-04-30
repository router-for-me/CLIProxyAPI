package temporal

import (
	"bytes"
	"testing"
)

func TestInjectIntoPayload_OpenAIMessages(t *testing.T) {
	payload := []byte(`{"model":"glm-4.6","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	out := InjectIntoPayload(payload, "glm-4.6")
	if !bytes.Contains(out, []byte(`\u003ctemporal `)) {
		t.Fatalf("expected escaped temporal tag in output, got: %s", string(out))
	}
	if !bytes.Contains(out, []byte(`"role":"system"`)) {
		t.Fatalf("expected system role injected, got: %s", string(out))
	}
}

func TestInjectIntoPayload_ClaudeSystemString(t *testing.T) {
	payload := []byte(`{"model":"claude-opus-4","system":"you are helpful","messages":[{"role":"user","content":"hi"}]}`)
	out := InjectIntoPayload(payload, "claude-opus-4")
	if !bytes.Contains(out, []byte(`\u003ctemporal `)) {
		t.Fatalf("expected escaped temporal tag, got: %s", string(out))
	}
}

func TestInjectIntoPayload_DedupSkipsExisting(t *testing.T) {
	payload := []byte(`{"model":"glm-4.6","messages":[{"role":"user","content":"<temporal day=\"Monday\"/>"}]}`)
	out := InjectIntoPayload(payload, "glm-4.6")
	if !bytes.Equal(out, payload) {
		t.Fatalf("expected payload unchanged when temporal already present, got:\n%s", string(out))
	}
}

func TestInjectIntoPayload_ImageModelSkipped(t *testing.T) {
	payload := []byte(`{"model":"imagen-3","messages":[{"role":"user","content":"hi"}]}`)
	out := InjectIntoPayload(payload, "imagen-3")
	if !bytes.Equal(out, payload) {
		t.Fatalf("expected payload unchanged for image model, got:\n%s", string(out))
	}
}

func TestShouldInject_DefaultIsOptIn(t *testing.T) {
	// Injection is opt-in: DefaultConfig() must return Enabled=false so
	// community users are never surprised by hidden request mutation.
	if ShouldInject(DefaultConfig()) {
		t.Fatal("DefaultConfig should NOT inject (opt-in)")
	}
}

func TestShouldInject_ExplicitlyEnabled(t *testing.T) {
	if !ShouldInject(Config{Enabled: true}) {
		t.Fatal("explicitly enabled config should inject")
	}
}

func TestShouldInject_Disabled(t *testing.T) {
	if ShouldInject(Config{Enabled: false}) {
		t.Fatal("disabled config should not inject")
	}
}
