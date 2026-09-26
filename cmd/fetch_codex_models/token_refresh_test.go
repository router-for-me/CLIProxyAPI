package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type tokenRoundTripper func(*http.Request) (*http.Response, error)

func (f tokenRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestEnsureAccessTokenPropagatesConfig(t *testing.T) {
	// The command has no injectable HTTP client. Replace the default transport
	// for these serial subtests so no real token endpoint is contacted.
	originalTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	for _, tc := range []struct {
		name            string
		disableCloaking bool
		want            string
	}{
		{"configured", true, "command-ua"},
		{"cloaking", false, constant.DefaultCodexUserAgent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				Codex:               config.CodexConfig{DisableCodexCloaking: tc.disableCloaking},
				CodexHeaderDefaults: config.CodexHeaderDefaults{UserAgent: "command-ua"},
			}
			calls := 0
			http.DefaultTransport = tokenRoundTripper(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != http.MethodPost || req.URL.String() != codexauth.TokenURL {
					t.Errorf("unexpected token request: %s %s", req.Method, req.URL)
				}
				if got := req.Header.Get("User-Agent"); got != tc.want {
					t.Errorf("User-Agent = %q, want %q", got, tc.want)
				}
				if err := req.ParseForm(); err != nil {
					t.Errorf("parse refresh form: %v", err)
				}
				if req.PostForm.Get("grant_type") != "refresh_token" || req.PostForm.Get("refresh_token") != tc.name+"-refresh" {
					t.Error("unexpected refresh form")
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`)),
					Header:     make(http.Header),
					Request:    req,
				}, nil
			})
			dir := t.TempDir()
			store := sdkauth.NewFileTokenStore()
			store.SetBaseDir(dir)
			auth := &coreauth.Auth{
				ID:       "codex-test.json",
				Provider: "codex",
				Metadata: map[string]any{
					"access_token":  "expired-access",
					"refresh_token": tc.name + "-refresh",
					"expired":       time.Now().Add(-time.Hour).Format(time.RFC3339),
				},
			}
			token, refreshed, err := ensureAccessToken(context.Background(), cfg, store, auth)
			if err != nil || !refreshed || token != "new-access" || calls != 1 {
				t.Fatalf("refresh: token=%q refreshed=%v calls=%d err=%v", token, refreshed, calls, err)
			}
			raw, errRead := os.ReadFile(filepath.Join(dir, auth.ID))
			if errRead != nil {
				t.Fatal(errRead)
			}
			var saved map[string]any
			if errJSON := json.Unmarshal(raw, &saved); errJSON != nil {
				t.Fatal(errJSON)
			}
			if saved["access_token"] != "new-access" || saved["refresh_token"] != "new-refresh" {
				t.Fatal("refreshed tokens were not persisted")
			}
			_, refreshed, err = ensureAccessToken(context.Background(), cfg, store, auth)
			if err != nil || refreshed || calls != 1 {
				t.Fatalf("valid access token should not refresh: refreshed=%v calls=%d err=%v", refreshed, calls, err)
			}
		})
	}
}
