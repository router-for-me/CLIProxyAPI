package translator

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

type fakePluginHooks struct {
	calls                 []string
	requestTranslateBody  []byte
	requestTranslateOK    bool
	responseTranslateBody []byte
	responseTranslateOK   bool
	normalizeRequest      func([]byte) []byte
	normalizeBefore       func([]byte) []byte
	normalizeAfter        func([]byte) []byte
}

func (h *fakePluginHooks) NormalizeRequest(ctx context.Context, from, to Format, model string, body []byte, stream bool) []byte {
	h.calls = append(h.calls, "normalize-request")
	if h.normalizeRequest != nil {
		return h.normalizeRequest(body)
	}
	return body
}

func (h *fakePluginHooks) TranslateRequest(ctx context.Context, from, to Format, model string, body []byte, stream bool) ([]byte, bool) {
	h.calls = append(h.calls, "translate-request")
	return h.requestTranslateBody, h.requestTranslateOK
}

func (h *fakePluginHooks) NormalizeResponseBefore(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) []byte {
	h.calls = append(h.calls, "normalize-response-before")
	if h.normalizeBefore != nil {
		return h.normalizeBefore(body)
	}
	return body
}

func (h *fakePluginHooks) TranslateResponse(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) ([]byte, bool) {
	h.calls = append(h.calls, "translate-response")
	return h.responseTranslateBody, h.responseTranslateOK
}

func (h *fakePluginHooks) NormalizeResponseAfter(ctx context.Context, from, to Format, model string, originalRequestRawJSON, requestRawJSON, body []byte, stream bool) []byte {
	h.calls = append(h.calls, "normalize-response-after")
	if h.normalizeAfter != nil {
		return h.normalizeAfter(body)
	}
	return body
}

func hasCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

func TestTranslateRequest_FallbackNormalizesModel(t *testing.T) {
	r := NewRegistry()

	tests := []struct {
		name          string
		model         string
		payload       string
		wantModel     string
		wantUnchanged bool
	}{
		{
			name:      "prefixed model is rewritten",
			model:     "gpt-5-mini",
			payload:   `{"model":"copilot/gpt-5-mini","input":"ping"}`,
			wantModel: "gpt-5-mini",
		},
		{
			name:          "matching model is left unchanged",
			model:         "gpt-5-mini",
			payload:       `{"model":"gpt-5-mini","input":"ping"}`,
			wantModel:     "gpt-5-mini",
			wantUnchanged: true,
		},
		{
			name:          "empty model leaves payload unchanged",
			model:         "",
			payload:       `{"model":"copilot/gpt-5-mini","input":"ping"}`,
			wantModel:     "copilot/gpt-5-mini",
			wantUnchanged: true,
		},
		{
			name:      "deeply prefixed model is rewritten",
			model:     "gpt-5.3-codex",
			payload:   `{"model":"team/gpt-5.3-codex","stream":true}`,
			wantModel: "gpt-5.3-codex",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(tt.payload)
			got := r.TranslateRequest(Format("a"), Format("b"), tt.model, input, false)

			gotModel := gjson.GetBytes(got, "model").String()
			if gotModel != tt.wantModel {
				t.Errorf("model = %q, want %q", gotModel, tt.wantModel)
			}

			if tt.wantUnchanged && string(got) != tt.payload {
				t.Errorf("payload was modified when it should not have been:\ngot:  %s\nwant: %s", got, tt.payload)
			}

			// Verify other fields are preserved.
			for _, key := range []string{"input", "stream"} {
				orig := gjson.Get(tt.payload, key)
				if !orig.Exists() {
					continue
				}
				after := gjson.GetBytes(got, key)
				if orig.Raw != after.Raw {
					t.Errorf("field %q changed: got %s, want %s", key, after.Raw, orig.Raw)
				}
			}
		})
	}
}

func TestTranslateRequest_RegisteredTransformTakesPrecedence(t *testing.T) {
	r := NewRegistry()
	from := Format("openai-response")
	to := Format("openai-response")

	r.Register(from, to, func(model string, rawJSON []byte, stream bool) []byte {
		return []byte(`{"model":"from-transform"}`)
	}, ResponseTransform{})

	input := []byte(`{"model":"copilot/gpt-5-mini","input":"ping"}`)
	got := r.TranslateRequest(from, to, "gpt-5-mini", input, false)

	gotModel := gjson.GetBytes(got, "model").String()
	if gotModel != "from-transform" {
		t.Errorf("expected registered transform to take precedence, got model = %q", gotModel)
	}
}

func TestHasRequestTransformer(t *testing.T) {
	r := NewRegistry()
	from := Format("from")
	to := Format("to")

	if r.HasRequestTransformer(from, to) {
		t.Fatal("request transformer exists before registration")
	}

	r.Register(from, to, func(model string, rawJSON []byte, stream bool) []byte {
		return rawJSON
	}, ResponseTransform{})

	if !r.HasRequestTransformer(from, to) {
		t.Fatal("request transformer is missing after registration")
	}
}

func TestHasResponseTransformerIgnoresEmptyRegistration(t *testing.T) {
	r := NewRegistry()
	from := Format("from")
	to := Format("to")

	r.Register(from, to, func(model string, rawJSON []byte, stream bool) []byte {
		return rawJSON
	}, ResponseTransform{})

	if r.HasResponseTransformer(from, to) {
		t.Fatal("empty response transform was reported as a response transformer")
	}
	if r.HasStreamResponseTransformer(from, to) {
		t.Fatal("empty response transform was reported as a stream response transformer")
	}
	if r.HasNonStreamResponseTransformer(from, to) {
		t.Fatal("empty response transform was reported as a non-stream response transformer")
	}
}

func TestHasResponseTransformerChecksConcreteResponseKinds(t *testing.T) {
	ctx := context.Background()
	r := NewRegistry()
	from := Format("from")
	streamOnlyTo := Format("stream-to")
	nonStreamOnlyTo := Format("non-stream-to")

	r.Register(from, streamOnlyTo, nil, ResponseTransform{
		Stream: func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
			return [][]byte{rawJSON}
		},
	})
	r.Register(from, nonStreamOnlyTo, nil, ResponseTransform{
		NonStream: func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
			return rawJSON
		},
	})

	if !r.HasResponseTransformer(from, streamOnlyTo) {
		t.Fatal("stream response transform was not reported as a response transformer")
	}
	if !r.HasStreamResponseTransformer(from, streamOnlyTo) {
		t.Fatal("stream response transform was not reported as a stream response transformer")
	}
	if r.HasNonStreamResponseTransformer(from, streamOnlyTo) {
		t.Fatal("stream-only transform was reported as a non-stream response transformer")
	}

	if !r.HasResponseTransformer(from, nonStreamOnlyTo) {
		t.Fatal("non-stream response transform was not reported as a response transformer")
	}
	if r.HasStreamResponseTransformer(from, nonStreamOnlyTo) {
		t.Fatal("non-stream-only transform was reported as a stream response transformer")
	}
	if !r.HasNonStreamResponseTransformer(from, nonStreamOnlyTo) {
		t.Fatal("non-stream response transform was not reported as a non-stream response transformer")
	}

	got := r.TranslateStream(ctx, streamOnlyTo, from, "model", nil, nil, []byte(`data: {"ok":true}`), nil)
	if len(got) != 1 || string(got[0]) != `data: {"ok":true}` {
		t.Fatalf("stream transform output = %q", got)
	}
}

func TestTranslateRequest_PluginTranslatorOnlyWhenNativeMissing(t *testing.T) {
	from := Format("from")
	to := Format("to")

	missingNative := NewRegistry()
	missingHooks := &fakePluginHooks{
		requestTranslateBody: []byte(`{"model":"plugin-request"}`),
		requestTranslateOK:   true,
	}
	missingNative.SetPluginHooks(missingHooks)

	gotMissing := missingNative.TranslateRequest(from, to, "resolved", []byte(`{"model":"prefixed/resolved"}`), false)
	if gjson.GetBytes(gotMissing, "model").String() != "plugin-request" {
		t.Fatalf("plugin request translator was not used, got %s", gotMissing)
	}
	if !hasCall(missingHooks.calls, "translate-request") {
		t.Fatal("plugin request translator was not called when native transformer was missing")
	}

	withNative := NewRegistry()
	nativeHooks := &fakePluginHooks{
		requestTranslateBody: []byte(`{"model":"plugin-request"}`),
		requestTranslateOK:   true,
	}
	withNative.SetPluginHooks(nativeHooks)
	withNative.Register(from, to, func(model string, rawJSON []byte, stream bool) []byte {
		return []byte(`{"model":"native-request"}`)
	}, ResponseTransform{})

	gotNative := withNative.TranslateRequest(from, to, "resolved", []byte(`{"model":"prefixed/resolved"}`), false)
	if gjson.GetBytes(gotNative, "model").String() != "native-request" {
		t.Fatalf("native request transformer was not preserved, got %s", gotNative)
	}
	if hasCall(nativeHooks.calls, "translate-request") {
		t.Fatal("plugin request translator was called despite native transformer")
	}
}

func TestTranslateNonStream_PluginTranslatorOnlyWhenNativeMissing(t *testing.T) {
	ctx := context.Background()
	from := Format("client")
	to := Format("upstream")

	missingNative := NewRegistry()
	missingHooks := &fakePluginHooks{
		responseTranslateBody: []byte(`{"output":"plugin-response"}`),
		responseTranslateOK:   true,
	}
	missingNative.SetPluginHooks(missingHooks)

	gotMissing := missingNative.TranslateNonStream(ctx, from, to, "model", nil, nil, []byte(`{"output":"raw"}`), nil)
	if gjson.GetBytes(gotMissing, "output").String() != "plugin-response" {
		t.Fatalf("plugin response translator was not used, got %s", gotMissing)
	}
	if !hasCall(missingHooks.calls, "translate-response") {
		t.Fatal("plugin response translator was not called when native transformer was missing")
	}

	withNative := NewRegistry()
	nativeHooks := &fakePluginHooks{
		responseTranslateBody: []byte(`{"output":"plugin-response"}`),
		responseTranslateOK:   true,
	}
	withNative.SetPluginHooks(nativeHooks)
	withNative.Register(to, from, nil, ResponseTransform{
		NonStream: func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
			return []byte(`{"output":"native-response"}`)
		},
	})

	gotNative := withNative.TranslateNonStream(ctx, from, to, "model", nil, nil, []byte(`{"output":"raw"}`), nil)
	if gjson.GetBytes(gotNative, "output").String() != "native-response" {
		t.Fatalf("native response transformer was not preserved, got %s", gotNative)
	}
	if hasCall(nativeHooks.calls, "translate-response") {
		t.Fatal("plugin response translator was called despite native transformer")
	}
}

func TestTranslateStream_NativeEmptyOutputSuppressesRawFallback(t *testing.T) {
	ctx := context.Background()
	from := Format("client")
	to := Format("upstream")

	r := NewRegistry()
	r.Register(to, from, nil, ResponseTransform{
		Stream: func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
			return nil
		},
	})

	got := r.TranslateStream(ctx, from, to, "model", nil, nil, []byte(`data: {"raw":true}`), nil)
	if len(got) != 0 {
		t.Fatalf("native stream transformer returned empty output, got raw fallback %q", got)
	}
}

func TestTranslateStream_PluginTranslatorUsedWhenNativeStreamMissing(t *testing.T) {
	ctx := context.Background()
	from := Format("client")
	to := Format("upstream")

	r := NewRegistry()
	hooks := &fakePluginHooks{
		responseTranslateBody: []byte(`data: {"plugin":true}`),
		responseTranslateOK:   true,
	}
	r.SetPluginHooks(hooks)
	r.Register(to, from, nil, ResponseTransform{
		NonStream: func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
			return []byte(`{"native-non-stream":true}`)
		},
	})

	got := r.TranslateStream(ctx, from, to, "model", nil, nil, []byte(`data: {"raw":true}`), nil)
	if len(got) != 1 || string(got[0]) != `data: {"plugin":true}` {
		t.Fatalf("plugin stream translator was not used, got %q", got)
	}
	if !hasCall(hooks.calls, "translate-response") {
		t.Fatal("plugin response translator was not called when native stream transformer was missing")
	}
}

func TestPluginNormalizersChainAfterNative(t *testing.T) {
	ctx := context.Background()
	r := NewRegistry()
	from := Format("client")
	to := Format("upstream")
	hooks := &fakePluginHooks{
		normalizeRequest: func(body []byte) []byte {
			if string(body) != `{"stage":"native-request"}` {
				t.Fatalf("request normalizer saw %s", body)
			}
			return []byte(`{"stage":"normalized-request"}`)
		},
		normalizeBefore: func(body []byte) []byte {
			if string(body) != `{"stage":"raw-response"}` {
				t.Fatalf("response before normalizer saw %s", body)
			}
			return []byte(`{"stage":"before-response"}`)
		},
		normalizeAfter: func(body []byte) []byte {
			if string(body) != `{"stage":"native-response"}` {
				t.Fatalf("response after normalizer saw %s", body)
			}
			return []byte(`{"stage":"after-response"}`)
		},
	}
	r.SetPluginHooks(hooks)
	r.Register(from, to, func(model string, rawJSON []byte, stream bool) []byte {
		return []byte(`{"stage":"native-request"}`)
	}, ResponseTransform{})
	r.Register(to, from, nil, ResponseTransform{
		NonStream: func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
			if string(rawJSON) != `{"stage":"before-response"}` {
				t.Fatalf("native response transformer saw %s", rawJSON)
			}
			return []byte(`{"stage":"native-response"}`)
		},
	})

	gotRequest := r.TranslateRequest(from, to, "model", []byte(`{"stage":"raw-request"}`), false)
	if string(gotRequest) != `{"stage":"normalized-request"}` {
		t.Fatalf("request normalizer did not run after native transformer, got %s", gotRequest)
	}

	gotResponse := r.TranslateNonStream(ctx, from, to, "model", nil, nil, []byte(`{"stage":"raw-response"}`), nil)
	if string(gotResponse) != `{"stage":"after-response"}` {
		t.Fatalf("response normalizers did not wrap native transformer, got %s", gotResponse)
	}
	if hasCall(hooks.calls, "translate-request") || hasCall(hooks.calls, "translate-response") {
		t.Fatalf("plugin translators should not run when native transformers exist, calls=%v", hooks.calls)
	}
}
