package helps

import (
	"bytes"
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCompatibilityRequestPair(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{}
	from, to := sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI
	for _, compat := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, distinct := range []bool{false, true} {
				original := []byte(`{"model":"test","input":"hello"}`)
				request := original
				if distinct {
					request = []byte(`{"model":"test","input":"changed"}`)
				}
				wantOriginal := TranslateRequestWithAPIKeyModelCompatibility(ctx, nil, cfg, from, to, "test", original, stream, compat)
				wantWorking := TranslateRequestWithAPIKeyModelCompatibility(ctx, nil, cfg, from, to, "test", request, stream, compat)
				base, work := TranslateRequestPairWithAPIKeyModelCompatibility(ctx, nil, cfg, from, to, "test", original, request, stream, compat)
				if !bytes.Equal(base, wantOriginal) || !bytes.Equal(work, wantWorking) {
					t.Fatalf("pair changed translation: compat=%v stream=%v distinct=%v", compat, stream, distinct)
				}
				work[0] = '!'
				if !bytes.Equal(base, wantOriginal) || original[0] != '{' {
					t.Fatal("working buffer aliases baseline or input")
				}
			}
		}
	}
}

// TestTranslateRequestPairWithAPIKeyModelCompatibilityCountsTranslations counts
// translator calls. Claude and Gemini Execute share this helper, and byte equality
// stays green when the same deterministic translator runs twice.
func TestTranslateRequestPairWithAPIKeyModelCompatibilityCountsTranslations(t *testing.T) {
	from := sdktranslator.Format("api-key-compat-count-from")
	to := sdktranslator.Format("api-key-compat-count-to")
	if sdktranslator.HasRequestTransformer(from, to) {
		t.Fatalf("request transformer %s -> %s is already registered", from, to)
	}
	if sdktranslator.HasPluginHooks() {
		t.Fatal("plugin hooks are installed and disable translation reuse")
	}

	const model = "compat-count-model"
	var calls int
	var wantStream bool
	sdktranslator.Register(from, to, func(gotModel string, rawJSON []byte, stream bool) []byte {
		if gotModel != model {
			t.Errorf("model = %q, want %q", gotModel, model)
		}
		if stream != wantStream {
			t.Errorf("stream = %v, want %v", stream, wantStream)
		}
		calls++
		return append([]byte(nil), rawJSON...)
	}, sdktranslator.ResponseTransform{})
	t.Cleanup(func() { sdktranslator.Unregister(from, to) })

	ctx := context.Background()
	cfg := &config.Config{}
	payload := []byte(`{"model":"compat-count-model","input":"hello"}`)
	for _, stream := range []bool{false, true} {
		for _, compat := range []bool{false, true} {
			wantStream = stream
			calls = 0
			base, work := TranslateRequestPairWithAPIKeyModelCompatibility(ctx, nil, cfg, from, to, model, payload, payload, stream, compat)
			if calls != 1 {
				t.Fatalf("stream=%v compat=%v: same slice translations = %d, want 1", stream, compat, calls)
			}
			if !bytes.Equal(base, payload) || !bytes.Equal(work, payload) {
				t.Fatalf("stream=%v compat=%v: same slice translation changed the payload", stream, compat)
			}
			if len(base) == 0 || &base[0] == &work[0] {
				t.Fatal("working buffer aliases the baseline")
			}
			baselineBefore := bytes.Clone(base)
			work[0] = 'X'
			if !bytes.Equal(base, baselineBefore) || payload[0] != '{' {
				t.Fatal("mutating the working buffer changed the baseline or input")
			}

			calls = 0
			detached := bytes.Clone(payload)
			base, work = TranslateRequestPairWithAPIKeyModelCompatibility(ctx, nil, cfg, from, to, model, payload, detached, stream, compat)
			if calls != 2 {
				t.Fatalf("stream=%v compat=%v: equal content with a different backing array translations = %d, want 2", stream, compat, calls)
			}
			if !bytes.Equal(base, payload) || !bytes.Equal(work, payload) {
				t.Fatalf("stream=%v compat=%v: distinct backing translation changed the payload", stream, compat)
			}
			if len(base) == 0 || &base[0] == &work[0] {
				t.Fatal("distinct working buffer aliases the baseline")
			}
		}
	}
}

func TestCompatibilityRequestPairPreservesHooks(t *testing.T) {
	hooks := &pairRequestPluginHooks{}
	sdktranslator.SetPluginHooks(hooks)
	t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })
	request := []byte(`{"model":"test","input":"hello"}`)
	base, work := TranslateRequestPairWithAPIKeyModelCompatibility(context.Background(), nil, &config.Config{}, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, "test", request, request, true, true)
	if hooks.calls != 2 || bytes.Equal(base, work) {
		t.Fatalf("stateful plugin calls must remain independent, calls=%d", hooks.calls)
	}
}
