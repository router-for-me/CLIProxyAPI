package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type copilotStreamHooks struct{ before, after int }

func (*copilotStreamHooks) NormalizeRequest(_ context.Context, _, _ translator.Format, _ string, body []byte, _ bool) []byte {
	return body
}

func (*copilotStreamHooks) TranslateRequest(context.Context, translator.Format, translator.Format, string, []byte, bool) ([]byte, bool) {
	return nil, false
}

func (h *copilotStreamHooks) NormalizeResponseBefore(_ context.Context, _, _ translator.Format, _ string, _, _, body []byte, _ bool) []byte {
	h.before++
	return bytes.ReplaceAll(body, []byte(`"original"`), []byte(`"normalized"`))
}

func (*copilotStreamHooks) TranslateResponse(context.Context, translator.Format, translator.Format, string, []byte, []byte, []byte, bool) ([]byte, bool) {
	return nil, false
}

func (h *copilotStreamHooks) NormalizeResponseAfter(_ context.Context, _, _ translator.Format, _ string, _, _, body []byte, _ bool) []byte {
	h.after++
	return bytes.ReplaceAll(body, []byte(`"normalized"`), []byte(`"delivered"`))
}

func TestCopilotNativeStreamResponseHooks(t *testing.T) {
	const model = "claude-opus-5"
	for _, tc := range []struct {
		format                    translator.Format
		endpoint, request, frames string
	}{
		{translator.FormatClaude, copilot.Messages, `{"messages":[{"role":"user","content":"hi"}],"max_tokens":16}`,
			"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"original"}}` + "\n\n" +
				"event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n"},
		{translator.FormatOpenAIResponse, copilot.Responses, `{"input":"hi"}`,
			"event: response.output_text.delta\ndata: " + `{"type":"response.output_text.delta","delta":"original"}` + "\n\n" +
				"event: response.completed\ndata: " + `{"type":"response.completed","response":{"status":"completed","output":[]}}` + "\n\n"},
	} {
		for _, mode := range []string{"passthrough", "hooks"} {
			t.Run(tc.format.String()+"/"+mode, func(t *testing.T) {
				want := tc.frames
				hooks := &copilotStreamHooks{}
				if mode == "hooks" {
					translator.SetPluginHooks(hooks)
					t.Cleanup(func() { translator.SetPluginHooks(nil) })
					want = strings.Replace(want, `"original"`, `"delivered"`, 1)
				}
				ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.frames)), Request: r}, nil
				}))
				auth := copilotTestAuth(t, "account", model, tc.endpoint)
				req := cliproxyexecutor.Request{Model: copilot.ModelPrefix + model, Payload: []byte(tc.request)}
				res, err := NewCopilotExecutor(nil).ExecuteStream(ctx, auth, req, cliproxyexecutor.Options{SourceFormat: tc.format, Stream: true})
				if err != nil {
					t.Fatal(err)
				}
				var got strings.Builder
				for chunk := range res.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					got.Write(chunk.Payload)
				}
				if got.String() != want {
					t.Errorf("stream = %q, want %q", got.String(), want)
				}
				if mode == "hooks" && (hooks.before != 2 || hooks.after != 2) {
					t.Errorf("hook calls before=%d after=%d, want one of each per event", hooks.before, hooks.after)
				}
			})
		}
	}
}
