package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestSynthesizeAuthFile_MapsModelWeightsAttribute(t *testing.T) {
	ctx := &SynthesisContext{Config: &config.Config{}, AuthDir: "/auth", Now: time.Now(), IDGenerator: NewStableIDGenerator()}
	data := []byte(`{"type":"claude","email":"a@example.com","weight":5,"model_weights":{"Claude-Fable-5-1":1,"claude-opus-5":0}}`)
	auths, err := SynthesizeAuthFile(ctx, "/auth/claude-a.json", data)
	if err != nil {
		t.Fatalf("SynthesizeAuthFile() error = %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("SynthesizeAuthFile() auths = %d, want 1", len(auths))
	}
	if got := auths[0].Attributes[coreauth.AttributeWeight]; got != "5" {
		t.Fatalf("weight attribute = %q, want 5", got)
	}
	if got := auths[0].Attributes[coreauth.AttributeModelWeights]; got != `{"claude-fable-5-1":1,"claude-opus-5":0}` {
		t.Fatalf("model_weights attribute = %q", got)
	}

	legacy := []byte(`{"type":"claude","email":"b@example.com","model-weights":{"claude-opus-5":2}}`)
	auths, err = SynthesizeAuthFile(ctx, "/auth/claude-b.json", legacy)
	if err != nil || len(auths) != 1 {
		t.Fatalf("SynthesizeAuthFile(legacy key) = %v, %v", auths, err)
	}
	if got := auths[0].Attributes[coreauth.AttributeModelWeights]; got != `{"claude-opus-5":2}` {
		t.Fatalf("legacy model-weights attribute = %q", got)
	}

	invalid := []byte(`{"type":"claude","email":"c@example.com","model_weights":{"claude-opus-5":1.5}}`)
	if _, err = SynthesizeAuthFile(ctx, "/auth/claude-c.json", invalid); err == nil {
		t.Fatal("SynthesizeAuthFile(invalid model_weights) expected error")
	}

	plain := []byte(`{"type":"claude","email":"d@example.com","weight":2}`)
	auths, err = SynthesizeAuthFile(ctx, "/auth/claude-d.json", plain)
	if err != nil || len(auths) != 1 {
		t.Fatalf("SynthesizeAuthFile(plain) = %v, %v", auths, err)
	}
	if _, ok := auths[0].Attributes[coreauth.AttributeModelWeights]; ok {
		t.Fatalf("plain file should not carry a model_weights attribute, got %q", auths[0].Attributes[coreauth.AttributeModelWeights])
	}
}
