package helps

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"gopkg.in/yaml.v3"
)

func TestCodexHTTP1ConfigAndRouting(t *testing.T) {
	cfg := &config.Config{}
	base := NewUtlsHTTPClient(context.Background(), cfg, nil, 0).Transport.(*fallbackRoundTripper)
	if _, ok := base.chrome.(*utlsRoundTripper); !ok {
		t.Fatal("Default transport changed")
	}
	if err := yaml.Unmarshal([]byte("codex:\n  http1: true\n"), cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Codex.HTTP1 {
		t.Fatal("Configuration flag not decoded")
	}
	updated := NewUtlsHTTPClient(context.Background(), cfg, nil, 0).Transport.(*fallbackRoundTripper)
	if _, ok := updated.chrome.(*http.Transport); !ok {
		t.Fatal("Codex HTTP1 option not wired")
	}
	if updated.anthropic != base.anthropic {
		t.Fatal("Anthropic transport changed")
	}
	if updated.fallback != base.fallback {
		t.Fatal("Other hosts transport changed")
	}
	// Per-account direct must override an invalid global proxy. Conversely an
	// invalid per-account route must fail closed even if global is direct.
	cfg.ProxyURL = "invalid://bad"
	auth := &cliproxyauth.Auth{ProxyURL: "direct"}
	chosen := NewUtlsHTTPClient(context.Background(), cfg, auth, 0).Transport.(*fallbackRoundTripper)
	if _, ok := chosen.chrome.(*http.Transport); !ok {
		t.Fatal("Account proxy precedence lost")
	}
	cfg.ProxyURL = "direct"
	auth.ProxyURL = "invalid://bad"
	chosen = NewUtlsHTTPClient(context.Background(), cfg, auth, 0).Transport.(*fallbackRoundTripper)
	if _, ok := chosen.chrome.(codexTransportError); !ok {
		t.Fatal("Invalid account proxy did not fail closed")
	}
	// Existing explicit caller transport injection keeps its precedence.
	cfg.ProxyURL = ""
	override := utlsClientRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, nil })
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", override)
	chosen = NewUtlsHTTPClient(ctx, cfg, nil, 0).Transport.(*fallbackRoundTripper)
	if _, ok := chosen.chrome.(utlsClientRoundTripFunc); !ok {
		t.Fatal("Caller transport override lost")
	}
}

func TestCodexHTTP1DoesNotReplayRedirect(t *testing.T) {
	for _, destination := range []string{"https://chatgpt.com/another-path", "https://example.invalid/collect"} {
		t.Run(destination, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Codex.HTTP1 = true
			client := NewUtlsHTTPClient(context.Background(), cfg, nil, 0)
			calls := 0
			client.Transport = utlsClientRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 307, Header: http.Header{"Location": []string{destination}}, Body: io.NopCloser(strings.NewReader("redirect")), Request: r}, nil
			})
			req, _ := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader("fixture"))
			req.Header.Set("Authorization", "Bearer fixture-only")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 307 || calls != 1 {
				t.Fatalf("Redirect replayed: status=%d calls=%d", resp.StatusCode, calls)
			}
		})
	}
}

func TestCodexHTTP1InvalidProxyClosesRequestBody(t *testing.T) {
	body := &trackedReadCloser{Reader: strings.NewReader("fixture")}
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/fixture", body)
	if err != nil {
		t.Fatal(err)
	}
	_, err = newCodexHTTP1RoundTripper("invalid://bad").RoundTrip(req)
	if err == nil || body.closeCount != 1 {
		t.Fatalf("error=%v body closes=%d", err, body.closeCount)
	}
}
