package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// TestClaudeExecutor_PerAccountDeviceDiversification verifies end-to-end that,
// when the downstream client sends no device headers, the outbound request
// carries this account's DETERMINISTIC per-account device fingerprint (not the
// fixed stock default), and that it is stable across requests.
func TestClaudeExecutor_PerAccountDeviceDiversification(t *testing.T) {
	capture := func(auth *cliproxyauth.Auth, url string) http.Header {
		var seen http.Header
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = r.Header.Clone()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"m","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
		}))
		defer server.Close()
		auth.Attributes["base_url"] = server.URL

		executor := NewClaudeExecutor(&config.Config{})
		_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "claude-3-5-sonnet-20241022",
			Payload: []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`),
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		_ = io.Discard
		if seen == nil {
			t.Fatal("no upstream request captured")
		}
		return seen
	}

	newAuth := func(id string) *cliproxyauth.Auth {
		return &cliproxyauth.Auth{ID: id, Attributes: map[string]string{"api_key": "key-" + id}}
	}

	auth := newAuth("acct-fp-A")
	expected := helps.AugmentClaudeDeviceHeaders(nil, auth, "", &config.Config{})
	wantUA := expected.Get("User-Agent")
	wantPkg := expected.Get("X-Stainless-Package-Version")

	// The account's derived identity must be a real Claude Code UA drawn from the pool.
	if wantUA == "" || wantPkg == "" {
		t.Fatalf("per-account profile incomplete: ua=%q pkg=%q", wantUA, wantPkg)
	}

	h1 := capture(newAuth("acct-fp-A"), "")
	if got := h1.Get("User-Agent"); got != wantUA {
		t.Fatalf("outbound User-Agent = %q, want per-account %q", got, wantUA)
	}
	if got := h1.Get("X-Stainless-Package-Version"); got != wantPkg {
		t.Fatalf("outbound X-Stainless-Package-Version = %q, want %q", got, wantPkg)
	}

	// Stability: the same account resolves to the same fingerprint on a second request.
	h2 := capture(newAuth("acct-fp-A"), "")
	if h1.Get("User-Agent") != h2.Get("User-Agent") {
		t.Fatalf("per-account UA not stable across requests: %q vs %q", h1.Get("User-Agent"), h2.Get("User-Agent"))
	}
}
