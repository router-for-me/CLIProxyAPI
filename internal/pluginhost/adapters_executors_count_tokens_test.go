package pluginhost

import (
	"context"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestExecutorAdapterCountTokensTranslatesAcrossFormats(t *testing.T) {
	adapter := newCurrentExecutorAdapterForTest(New(), "count-format-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		countTokens: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{Payload: []byte(`{"usage":{"prompt_tokens":17}}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})

	response, errCount := adapter.CountTokens(context.Background(), nil, coreexecutor.Request{
		Model:   "claude-model",
		Format:  sdktranslator.FormatClaude,
		Payload: []byte(`{"model":"claude-model","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if got, want := string(response.Payload), `{"input_tokens":17}`; got != want {
		t.Fatalf("CountTokens() payload = %s, want %s", got, want)
	}
}

func TestExecutorAdapterCountTokensKeepsPayloadWhenFormatsMatch(t *testing.T) {
	adapter := newCurrentExecutorAdapterForTest(New(), "count-same-format-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		countTokens: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":9}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatClaude}, []sdktranslator.Format{sdktranslator.FormatClaude})

	response, errCount := adapter.CountTokens(context.Background(), nil, coreexecutor.Request{
		Model:   "claude-model",
		Format:  sdktranslator.FormatClaude,
		Payload: []byte(`{"model":"claude-model","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if got, want := string(response.Payload), `{"input_tokens":9}`; got != want {
		t.Fatalf("CountTokens() payload = %s, want %s", got, want)
	}
}

func TestExecutorAdapterCountTokensFallsBackWhenCountMissing(t *testing.T) {
	adapter := newCurrentExecutorAdapterForTest(New(), "count-no-count-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		countTokens: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{Payload: []byte(`{"unexpected":true}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})

	response, errCount := adapter.CountTokens(context.Background(), nil, coreexecutor.Request{
		Model:   "claude-model",
		Format:  sdktranslator.FormatClaude,
		Payload: []byte(`{"model":"claude-model","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if len(response.Payload) == 0 {
		t.Fatal("CountTokens() returned an empty payload when no count was present")
	}
}

func TestExecutorAdapterCountTokensRecognizesNativeCodexCount(t *testing.T) {
	adapter := newCurrentExecutorAdapterForTest(New(), "count-codex-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		countTokens: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{Payload: []byte(`{"response":{"usage":{"input_tokens":11,"output_tokens":0,"total_tokens":11}}}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatCodex}, []sdktranslator.Format{sdktranslator.FormatCodex})

	response, errCount := adapter.CountTokens(context.Background(), nil, coreexecutor.Request{
		Model:   "claude-model",
		Format:  sdktranslator.FormatClaude,
		Payload: []byte(`{"model":"claude-model","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if got, want := string(response.Payload), `{"input_tokens":11}`; got != want {
		t.Fatalf("CountTokens() payload = %s, want %s", got, want)
	}
}

func TestExecutorAdapterCountTokensKeepsResponseTranslatorWithoutTokenCountHandler(t *testing.T) {
	pluginFormat := sdktranslator.Format("count-plugin-output-custom")
	clientFormat := sdktranslator.Format("count-client-output-custom")
	sdktranslator.Register(clientFormat, pluginFormat, func(_ string, rawJSON []byte, _ bool) ([]byte, error) { return rawJSON, nil }, sdktranslator.ResponseTransform{
		NonStream: func(context.Context, string, []byte, []byte, []byte, *any) []byte {
			return []byte(`{"input_tokens":9,"translated":"by-response-translator"}`)
		},
	})
	t.Cleanup(func() {
		sdktranslator.Unregister(clientFormat, pluginFormat)
	})
	adapter := newCurrentExecutorAdapterForTest(New(), "count-custom-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		countTokens: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{Payload: []byte(`{"total_tokens":9}`)}, nil
		},
	}, []sdktranslator.Format{pluginFormat}, []sdktranslator.Format{pluginFormat})

	response, errCount := adapter.CountTokens(context.Background(), nil, coreexecutor.Request{
		Model:   "custom-model",
		Format:  clientFormat,
		Payload: []byte(`{"model":"custom-model"}`),
	}, coreexecutor.Options{
		SourceFormat:   clientFormat,
		ResponseFormat: clientFormat,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if got, want := string(response.Payload), `{"input_tokens":9,"translated":"by-response-translator"}`; got != want {
		t.Fatalf("CountTokens() payload = %s, want response-translator output %s", got, want)
	}
}

func TestExecutorAdapterCountTokensKeepsIdenticalTokenCountTranslation(t *testing.T) {
	pluginFormat := sdktranslator.Format("count-identical-plugin")
	clientFormat := sdktranslator.Format("count-identical-client")
	raw := []byte(`{"total_tokens":9}`)
	sdktranslator.Register(clientFormat, pluginFormat, nil, sdktranslator.ResponseTransform{
		TokenCount: func(context.Context, int64) []byte { return raw },
		NonStream: func(context.Context, string, []byte, []byte, []byte, *any) []byte {
			return []byte(`{"translated":"wrong fallback"}`)
		},
	})
	t.Cleanup(func() {
		sdktranslator.Unregister(clientFormat, pluginFormat)
	})
	adapter := newCurrentExecutorAdapterForTest(New(), "count-identical-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		countTokens: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{Payload: raw}, nil
		},
	}, []sdktranslator.Format{clientFormat}, []sdktranslator.Format{pluginFormat})

	response, errCount := adapter.CountTokens(context.Background(), nil, coreexecutor.Request{
		Model:   "custom-model",
		Format:  clientFormat,
		Payload: []byte(`{"model":"custom-model"}`),
	}, coreexecutor.Options{
		SourceFormat:   clientFormat,
		ResponseFormat: clientFormat,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if got, want := string(response.Payload), string(raw); got != want {
		t.Fatalf("CountTokens() payload = %s, want token-count translator output %s", got, want)
	}
}

func TestExecutorAdapterCountTokensKeepsNilTokenCountTranslation(t *testing.T) {
	pluginFormat := sdktranslator.Format("count-nil-plugin")
	clientFormat := sdktranslator.Format("count-nil-client")
	sdktranslator.Register(clientFormat, pluginFormat, nil, sdktranslator.ResponseTransform{
		TokenCount: func(context.Context, int64) []byte { return nil },
		NonStream: func(context.Context, string, []byte, []byte, []byte, *any) []byte {
			return []byte(`{"translated":"wrong fallback"}`)
		},
	})
	t.Cleanup(func() {
		sdktranslator.Unregister(clientFormat, pluginFormat)
	})
	adapter := newCurrentExecutorAdapterForTest(New(), "count-nil-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		countTokens: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{Payload: []byte(`{"total_tokens":9}`)}, nil
		},
	}, []sdktranslator.Format{clientFormat}, []sdktranslator.Format{pluginFormat})

	response, errCount := adapter.CountTokens(context.Background(), nil, coreexecutor.Request{
		Model:   "custom-model",
		Format:  clientFormat,
		Payload: []byte(`{"model":"custom-model"}`),
	}, coreexecutor.Options{
		SourceFormat:   clientFormat,
		ResponseFormat: clientFormat,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if response.Payload != nil {
		t.Fatalf("CountTokens() payload = %s, want nil token-count translator output", response.Payload)
	}
}

func TestDefaultTokenCountTranslatorPairs(t *testing.T) {
	cases := []struct {
		name  string
		from  sdktranslator.Format
		to    sdktranslator.Format
		input string
		want  string
	}{
		{"openai-to-claude", sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, `{"usage":{"prompt_tokens":13}}`, `{"input_tokens":13}`},
		{"openai-to-gemini", sdktranslator.FormatOpenAI, sdktranslator.FormatGemini, `{"usage":{"prompt_tokens":13}}`, `{"totalTokens":13,"promptTokensDetails":[{"modality":"TEXT","tokenCount":13}]}`},
		{"gemini-to-claude", sdktranslator.FormatGemini, sdktranslator.FormatClaude, `{"totalTokens":13,"usageMetadata":{"promptTokenCount":13}}`, `{"input_tokens":13}`},
		{"codex-to-claude", sdktranslator.FormatCodex, sdktranslator.FormatClaude, `{"response":{"usage":{"input_tokens":13,"total_tokens":13}}}`, `{"input_tokens":13}`},
		{"codex-to-gemini", sdktranslator.FormatCodex, sdktranslator.FormatGemini, `{"response":{"usage":{"input_tokens":13,"total_tokens":13}}}`, `{"totalTokens":13,"promptTokensDetails":[{"modality":"TEXT","tokenCount":13}]}`},
		{"claude-to-gemini", sdktranslator.FormatClaude, sdktranslator.FormatGemini, `{"input_tokens":13}`, `{"totalTokens":13,"promptTokensDetails":[{"modality":"TEXT","tokenCount":13}]}`},
		{"gemini-to-gemini", sdktranslator.FormatGemini, sdktranslator.FormatGemini, `{"totalTokens":13,"usageMetadata":{"promptTokenCount":13}}`, `{"totalTokens":13,"promptTokensDetails":[{"modality":"TEXT","tokenCount":13}]}`},
		{"antigravity-to-claude", sdktranslator.FormatAntigravity, sdktranslator.FormatClaude, `{"totalTokens":13}`, `{"input_tokens":13}`},
		{"antigravity-to-gemini", sdktranslator.FormatAntigravity, sdktranslator.FormatGemini, `{"totalTokens":13}`, `{"totalTokens":13,"promptTokensDetails":[{"modality":"TEXT","tokenCount":13}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !sdktranslator.HasTokenCountTransformer(tc.from, tc.to) {
				t.Fatalf("missing token-count transformer from %q to %q", tc.from, tc.to)
			}
			count, ok := pluginExecutorTokenCount(tc.from, []byte(tc.input))
			if !ok || count != 13 {
				t.Fatalf("pluginExecutorTokenCount() = (%d, %t), want (13, true)", count, ok)
			}
			got := sdktranslator.TranslateTokenCount(context.Background(), tc.from, tc.to, count, []byte(`{"fallback":true}`))
			if string(got) != tc.want {
				t.Fatalf("TranslateTokenCount() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestPluginExecutorTokenCountRejectsInvalidOrModelResponseCounts(t *testing.T) {
	cases := []struct {
		name    string
		format  sdktranslator.Format
		payload string
	}{
		{"fractional", sdktranslator.FormatOpenAI, `{"usage":{"prompt_tokens":1.5}}`},
		{"overflow", sdktranslator.FormatOpenAI, `{"usage":{"prompt_tokens":9223372036854775808}}`},
		{"overflow-wraps-positive", sdktranslator.FormatOpenAI, `{"usage":{"prompt_tokens":18446744073709551617}}`},
		{"string-number", sdktranslator.FormatOpenAI, `{"usage":{"prompt_tokens":"12"}}`},
		{"negative", sdktranslator.FormatOpenAI, `{"usage":{"prompt_tokens":-1}}`},
		{"model-response", sdktranslator.FormatOpenAI, `{"choices":[{"message":{"content":"hello"}}],"usage":{"prompt_tokens":12,"total_tokens":13}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if count, ok := pluginExecutorTokenCount(tc.format, []byte(tc.payload)); ok {
				t.Fatalf("pluginExecutorTokenCount() = (%d, true), want no recognized count", count)
			}
		})
	}
}

func TestPluginExecutorTokenCountPrefersInputCount(t *testing.T) {
	for _, tc := range []struct {
		name    string
		format  sdktranslator.Format
		payload string
		want    int64
	}{
		{"prompt-over-total", sdktranslator.FormatOpenAI, `{"usage":{"prompt_tokens":7,"total_tokens":10}}`, 7},
		{"input-over-total", sdktranslator.FormatOpenAI, `{"input_tokens":8,"total_tokens":10}`, 8},
		{"codex-nested-input-over-total", sdktranslator.FormatCodex, `{"response":{"usage":{"input_tokens":9,"total_tokens":10}}}`, 9},
		{"ignores-deeper-similar-keys", sdktranslator.FormatOpenAI, `{"wrapper":{"usage":{"prompt_tokens":99}},"total_tokens":5}`, 5},
		{"zero-is-valid", sdktranslator.FormatOpenAI, `{"usage":{"prompt_tokens":0}}`, 0},
		{"maximum-int64", sdktranslator.FormatOpenAI, `{"usage":{"prompt_tokens":9223372036854775807}}`, 9223372036854775807},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := pluginExecutorTokenCount(tc.format, []byte(tc.payload))
			if !ok || got != tc.want {
				t.Fatalf("pluginExecutorTokenCount() = (%d, %t), want (%d, true)", got, ok, tc.want)
			}
		})
	}
}
