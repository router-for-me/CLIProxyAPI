package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type interactionsErrReadCloser struct {
	data []byte
	pos  int
	err  error
}

func (r *interactionsErrReadCloser) Read(p []byte) (int, error) {
	if r.pos < len(r.data) {
		n := copy(p, r.data[r.pos:])
		r.pos += n
		return n, nil
	}
	return 0, r.err
}

func (r *interactionsErrReadCloser) Close() error {
	return nil
}

type interactionsMockRoundTripper struct {
	fn func(req *http.Request) (*http.Response, error)
}

func (rt interactionsMockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return rt.fn(req)
}

func assertInteractionsScannerErrDropsTruncatedFrame(
	t *testing.T,
	exec interface {
		ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
	},
	ctx context.Context,
	auth *cliproxyauth.Auth,
	wantErr error,
) {
	t.Helper()

	req := cliproxyexecutor.Request{
		Model:   "gemini-3.8-flash",
		Payload: []byte(`{"model":"gemini-3.8-flash","input":"hello"}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
		Stream:         true,
	}

	stream, errStream := exec.ExecuteStream(ctx, auth, req, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}

	var payloads [][]byte
	var gotErr error
	for chunk := range stream.Chunks {
		if len(chunk.Payload) > 0 {
			payloads = append(payloads, append([]byte(nil), chunk.Payload...))
		}
		if chunk.Err != nil {
			gotErr = chunk.Err
		}
	}

	if !errors.Is(gotErr, wantErr) {
		t.Fatalf("stream error = %v, want %v", gotErr, wantErr)
	}
	for _, p := range payloads {
		if bytes.Contains(p, []byte("TRUNCATED_PARTIAL")) {
			t.Fatalf("truncated SSE frame was flushed downstream despite scanner error: %q", string(p))
		}
	}
}

func TestGeminiInteractionsScannerErrDoesNotFlushTruncatedFrame(t *testing.T) {
	wantErr := errors.New("simulated upstream connection reset")
	// Complete first frame followed by a truncated trailing frame with no blank-line terminator.
	sseBody := "event: interaction.created\ndata: {\"type\":\"interaction.created\"}\n\nevent: step.delta\ndata: {\"delta\":{\"text\":\"TRUNCATED_PARTIAL\"}"

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", interactionsMockRoundTripper{
		fn: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body: &interactionsErrReadCloser{
					data: []byte(sseBody),
					err:  wantErr,
				},
				Request: req,
			}, nil
		},
	})

	exec := NewGeminiInteractionsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "gemini-interactions",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": "https://generativelanguage.googleapis.com",
		},
	}

	assertInteractionsScannerErrDropsTruncatedFrame(t, exec, ctx, auth, wantErr)
}

func TestGeminiVertexInteractionsScannerErrDoesNotFlushTruncatedFrame(t *testing.T) {
	wantErr := errors.New("simulated vertex upstream connection reset")
	sseBody := "event: interaction.created\ndata: {\"type\":\"interaction.created\"}\n\nevent: step.delta\ndata: {\"delta\":{\"text\":\"TRUNCATED_PARTIAL\"}"

	var sa map[string]any
	if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, "https://oauth2.example.test/token"), &sa); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", interactionsMockRoundTripper{
		fn: func(req *http.Request) (*http.Response, error) {
			if strings.HasSuffix(req.URL.Path, "/token") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"access_token":"mock-vertex-sa-token","token_type":"Bearer","expires_in":3600}`)),
					Request:    req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body: &interactionsErrReadCloser{
					data: []byte(sseBody),
					err:  wantErr,
				},
				Request: req,
			}, nil
		},
	})

	exec := NewGeminiVertexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "vertex",
		Metadata: map[string]any{
			"project_id":      "proxy-test",
			"location":        "global",
			"interactions":    true,
			"service_account": sa,
		},
	}

	assertInteractionsScannerErrDropsTruncatedFrame(t, exec, ctx, auth, wantErr)
}
