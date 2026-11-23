package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCodexExecutorSIWCRetainsRotatedTokensWhenScopeIsOmitted(t *testing.T) {
	previousTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	cycle := 0
	http.DefaultTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if errParse := req.ParseForm(); errParse != nil {
			t.Fatal(errParse)
		}
		if got, want := req.Form.Get("refresh_token"), fmt.Sprintf("refresh-%d", cycle); got != want {
			t.Fatalf("submitted refresh token = %q, want saved replacement %q", got, want)
		}
		cycle++
		body := fmt.Sprintf(`{"access_token":"access-%d","refresh_token":"refresh-%d","expires_in":3600}`, cycle, cycle)
		if cycle == 1 {
			doc := ojson.NewDocString(body)
			if errSet := doc.Set("earliest_refresh_at", 946684800); errSet != nil {
				t.Fatal(errSet)
			}
			body = doc.String()
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.json")
	const grantedScope = "openid chatgpt.tokens.use.direct"
	initial := map[string]any{
		"type": "codex", "use_siwc": true,
		"siwc": map[string]any{
			"access_token": "access-0", "refresh_token": "refresh-0", "client_id": "oaiapp_test",
			"scope": grantedScope, "scopes": strings.Fields(grantedScope),
			"expires_at":          time.Now().Add(-time.Hour).Format(time.RFC3339),
			"earliest_refresh_at": "1999-01-01T00:00:00Z",
		},
	}
	if errWrite := codexauth.WriteJSONFileAtomic(path, initial); errWrite != nil {
		t.Fatal(errWrite)
	}
	executor := NewCodexExecutor(&config.Config{AuthDir: dir})
	metadata := initial
	for wantCycle := 1; wantCycle <= 2; wantCycle++ {
		auth := &cliproxyauth.Auth{ID: "codex.json", FileName: "codex.json", Provider: "codex", Metadata: metadata}
		if _, errRefresh := executor.Refresh(cliproxyauth.WithForceRefresh(context.Background()), auth); errRefresh != nil {
			t.Fatalf("renewal %d: %v", wantCycle, errRefresh)
		}
		raw, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatal(errRead)
		}
		saved := ojson.ParseBytes(raw)
		if got, want := saved.Get("siwc.refresh_token").String(), fmt.Sprintf("refresh-%d", wantCycle); got != want {
			t.Fatalf("persisted refresh token = %q, want %q", got, want)
		}
		if saved.Get("siwc.scope").String() != grantedScope || saved.Get("siwc.scopes.1").String() != codexauth.SIWCDirectTokenScope {
			t.Fatal("previously granted permissions were lost")
		}
		window := saved.Get("siwc.earliest_refresh_at")
		if wantCycle == 1 && (window.Type != ojson.Number || window.Int() != 946684800) {
			t.Fatalf("new server renewal window was not persisted: %s", window.Raw)
		}
		if wantCycle == 2 && window.Exists() {
			t.Fatalf("previous token's renewal window survived rotation: %s", window.Raw)
		}
		metadata = saved.Value().(map[string]any)
	}
}

func TestCodexExecutorSIWCExplicitRefreshHonorsServerWindow(t *testing.T) {
	previousTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	calls := 0
	http.DefaultTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"invalid_grant"}`))}, nil
	})
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("force=%t", force), func(t *testing.T) {
			ctx := context.Background()
			if force {
				ctx = cliproxyauth.WithForceRefresh(ctx)
			}
			auth := &cliproxyauth.Auth{Provider: "codex", Metadata: map[string]any{
				"siwc": map[string]any{
					"access_token": "access", "refresh_token": "refresh", "client_id": "oaiapp_window",
					"expires_at":          time.Now().Add(-time.Hour).Format(time.RFC3339),
					"earliest_refresh_at": time.Now().Add(time.Hour).Format(time.RFC3339),
				},
			}}
			if _, errRefresh := NewCodexExecutor(&config.Config{}).Refresh(ctx, auth); errRefresh != nil {
				t.Fatalf("refresh before the server window: %v", errRefresh)
			}
			if calls != 0 {
				t.Fatalf("sent %d token requests before the server window", calls)
			}
		})
	}
}
