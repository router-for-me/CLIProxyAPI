package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type kimiRequestCompressionRoundTripFunc func(*http.Request) (*http.Response, error)

func (f kimiRequestCompressionRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestKimiExecutorCountTokensDoesNotCompressClaudeCompatibleRequests(t *testing.T) {
	var (
		contentEncoding string
		body            []byte
	)
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRequestCompressionRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		contentEncoding = req.Header.Get("Content-Encoding")
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"input_tokens":42}`)),
			Request:    req,
		}, nil
	}))

	executor := NewKimiExecutor(&config.Config{SDKConfig: config.SDKConfig{RequestCompression: config.RequestCompressionAuto}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-123"}}
	payload := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("x", config.DefaultRequestCompressionMinBytes) + `"}]}`)

	_, err := executor.CountTokens(ctx, auth, cliproxyexecutor.Request{
		Model:   "kimi-k3",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if contentEncoding != "" {
		t.Fatalf("Content-Encoding: got %q, want empty", contentEncoding)
	}
	if !bytes.HasPrefix(body, []byte("{")) {
		t.Fatalf("request body is not JSON: %q", body[:min(len(body), 32)])
	}
	if !bytes.Contains(body, []byte(`"model":"k3"`)) {
		t.Fatalf("upstream model was not normalized: %s", body)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
