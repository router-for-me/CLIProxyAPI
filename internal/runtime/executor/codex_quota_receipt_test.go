package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
)

type codexReceiptCheckingBody struct {
	io.Reader
	check   func()
	checked bool
}

func (b *codexReceiptCheckingBody) Read(p []byte) (int, error) {
	if !b.checked {
		b.checked = true
		b.check()
	}
	return b.Reader.Read(p)
}
func (b *codexReceiptCheckingBody) Close() error { return nil }

func TestCodexResetCapturedBeforeBodyRead(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		name := "gzip"
		if fallback {
			name = "gzip after plain fallback"
		}
		t.Run(name, func(t *testing.T) {
			ctx := internallogging.WithResponseHeadersHolder(context.Background())
			plain := []byte(`{"input":"hello"}`)
			wire := plain
			if fallback {
				wire = codexCompressedResponse(t, "zstd", plain)
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(wire))
			if err != nil {
				t.Fatal(err)
			}
			raw := http.Header{"Content-Encoding": {"gzip"}, "X-Codex-Primary-Window-Minutes": {"10080"}, "X-Codex-Primary-Reset-After-Seconds": {"180"}}
			calls := 0
			body := &codexReceiptCheckingBody{Reader: bytes.NewReader(codexCompressedResponse(t, "gzip", []byte("response"))), check: func() {
				if internallogging.GetResponseHeaders(ctx).Get("X-Codex-Primary-Reset-At") == "" {
					t.Error("quota receipt was not captured before gzip decoder read body")
				}
			}}
			client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if fallback && calls == 1 {
					return &http.Response{StatusCode: 415, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("rejected"))}, nil
				}
				return &http.Response{StatusCode: 200, Header: raw, Body: body}, nil
			})}
			resp, err := doCodexRequest(client, req, plain, func(*http.Request) {}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(resp.Body)
			if err != nil || string(data) != "response" {
				t.Fatalf("response=%q err=%v", data, err)
			}
			if errClose := resp.Body.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			if raw.Get("X-Codex-Primary-Reset-At") != "" {
				t.Fatal("internal receipt changed forwarded headers")
			}
			wantCalls := 1
			if fallback {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("calls=%d want%d", calls, wantCalls)
			}
		})
	}
	t.Run("websocket upgrade rejection", func(t *testing.T) {
		ctx := internallogging.WithResponseHeadersHolder(context.Background())
		body := &codexReceiptCheckingBody{Reader: strings.NewReader("rejected"), check: func() {
			if internallogging.GetResponseHeaders(ctx).Get("X-Codex-Primary-Reset-At") == "" {
				t.Error("quota receipt was not captured before reading rejected upgrade body")
			}
		}}
		resp := &http.Response{StatusCode: 429, Header: http.Header{"X-Codex-Primary-Window-Minutes": {"10080"}, "X-Codex-Primary-Reset-After-Seconds": {"180"}}, Body: body}
		got := recordWebsocketUpgradeRejection(ctx, nil, helps.UpstreamRequestLog{}, resp)
		if string(got) != "rejected" {
			t.Fatalf("rejection body=%q", got)
		}
	})
}
