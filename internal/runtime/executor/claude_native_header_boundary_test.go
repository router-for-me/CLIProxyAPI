package executor

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type nativeHeaderTransport func(*http.Request) (*http.Response, error)

func (fn nativeHeaderTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestNativeClaudeSendBoundaryPreservesProtocol(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		for _, osName := range []string{"Linux", "MacOS"} {
			t.Run(path+"/"+osName, func(t *testing.T) {
				native := http.Header{
					"Content-Type": {"application/json"}, "Accept": {"application/json"},
					"Accept-Encoding":   {"gzip, deflate, br, zstd"},
					"User-Agent":        {"claude-cli/2.1.269 (external, cli)"},
					"Anthropic-Version": {"2023-06-01"}, "Anthropic-Beta": {"native-beta-1", "native-beta-2"},
					"X-Stainless-Os": {osName}, "X-Stainless-Arch": {"arm64"}, "X-Stainless-Package-Version": {"0.112.1"},
					"X-Stainless-Runtime-Version": {"v26.3.0"}, "X-Stainless-Async": {"async"},
				}
				ctx := coreexecutor.WithNativeClaudeProtocolHeaders(context.Background(), native)
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com"+path, strings.NewReader("{}"))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer selected-synthetic-account")
				req.Header.Set("X-Claude-Code-Session-Id", "selected-session")
				req.Header.Set("User-Agent", "different-sdk-version")
				req.Header.Set("Anthropic-Beta", "reconstructed-beta")
				req.Header.Set("X-Stainless-Timeout", "should-remain-absent")
				req.Header.Set("X-Unknown", "must-not-leak")
				want := native.Clone()
				want.Set("Authorization", "Bearer selected-synthetic-account")
				want.Set("X-Claude-Code-Session-Id", "selected-session")
				client := &http.Client{Transport: nativeHeaderTransport(func(got *http.Request) (*http.Response, error) {
					canonical := make(http.Header)
					for key, values := range got.Header {
						canonical[http.CanonicalHeaderKey(key)] = values
					}
					if !reflect.DeepEqual(canonical, want) {
						t.Errorf("native protocol header mismatch: got %v want %v", canonical, want)
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}")), Request: got}, nil
				})}
				resp, err := doClaudeUpstreamRequest(client, req)
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
			})
		}
	}
}
