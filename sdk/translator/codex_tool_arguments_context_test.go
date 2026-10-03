package translator

import (
	"bytes"
	"context"
	"testing"
)

func TestRegistryCodexToolArgumentPolicy(t *testing.T) {
	parent := WithCodexToolArgumentNormalization(context.Background(), true)
	for _, tt := range []struct {
		name string
		ctx  context.Context
	}{
		{"default", context.Background()},
		{"nil", nil},
		{"explicit false", WithCodexToolArgumentNormalization(context.Background(), false)},
		{"child override", WithCodexToolArgumentNormalization(parent, false)},
	} {
		for _, format := range []Format{FormatOpenAIResponse, FormatCodex} {
			t.Run(tt.name+"/"+format.String(), func(t *testing.T) {
				r := NewRegistry()
				registerCodexArgumentResponders(r, format)
				if got := r.TranslateNonStream(tt.ctx, Format("upstream"), format, "model", nil, nil, nil, nil); !bytes.Equal(got, []byte(codexNonStreamResponse)) {
					t.Fatalf("disabled policy changed non-stream response: %s", got)
				}
				if got := r.TranslateStream(tt.ctx, Format("upstream"), format, "model", nil, nil, nil, nil); len(got) != 1 || !bytes.Equal(got[0], []byte(codexStreamResponse)) {
					t.Fatalf("disabled policy changed stream response: %q", got)
				}
			})
		}
	}
	if !codexToolArgumentNormalizationEnabled(parent) {
		t.Fatal("child override changed the parent policy")
	}
}

func TestWithCodexToolArgumentNormalizationPreservesContext(t *testing.T) {
	type testKey struct{}
	parent, cancel := context.WithCancel(context.WithValue(context.Background(), testKey{}, "kept"))
	ctx := WithCodexToolArgumentNormalization(parent, true)
	if ctx.Value(testKey{}) != "kept" {
		t.Fatal("parent value lost")
	}
	cancel()
	if ctx.Err() != context.Canceled || ctx.Done() != parent.Done() {
		t.Fatal("parent cancellation lost")
	}
	if !codexToolArgumentNormalizationEnabled(WithCodexToolArgumentNormalization(nil, true)) {
		t.Fatal("nil parent did not enable the policy")
	}
}
