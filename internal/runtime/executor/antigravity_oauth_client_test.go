package executor

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestResolveAntigravityOAuthClient(t *testing.T) {
	t.Run("defaults to built-in constants", func(t *testing.T) {
		id, secret := resolveAntigravityOAuthClient(nil)
		if id != antigravityClientID || secret != antigravityClientSecret {
			t.Fatalf("expected built-in client, got (%q, %q)", id, secret)
		}
	})

	t.Run("uses metadata client pair when both present", func(t *testing.T) {
		auth := &cliproxyauth.Auth{
			Metadata: map[string]any{
				"client_id":     "custom-meta-id",
				"client_secret": "custom-meta-secret",
			},
		}
		id, secret := resolveAntigravityOAuthClient(auth)
		if id != "custom-meta-id" || secret != "custom-meta-secret" {
			t.Fatalf("unexpected metadata client: (%q, %q)", id, secret)
		}
	})

	t.Run("uses attributes client pair when metadata absent", func(t *testing.T) {
		auth := &cliproxyauth.Auth{
			Attributes: map[string]string{
				"client_id":     "custom-attr-id",
				"client_secret": "custom-attr-secret",
			},
		}
		id, secret := resolveAntigravityOAuthClient(auth)
		if id != "custom-attr-id" || secret != "custom-attr-secret" {
			t.Fatalf("unexpected attributes client: (%q, %q)", id, secret)
		}
	})

	t.Run("uses env override when credential pair absent", func(t *testing.T) {
		t.Setenv("ANTIGRAVITY_OAUTH_CLIENT_ID", "env-client-id")
		t.Setenv("ANTIGRAVITY_OAUTH_CLIENT_SECRET", "env-client-secret")
		id, secret := resolveAntigravityOAuthClient(&cliproxyauth.Auth{})
		if id != "env-client-id" || secret != "env-client-secret" {
			t.Fatalf("unexpected env client: (%q, %q)", id, secret)
		}
	})

	t.Run("ignores partial metadata pair and falls back", func(t *testing.T) {
		auth := &cliproxyauth.Auth{
			Metadata: map[string]any{
				"client_id": "partial-id-only",
			},
		}
		id, secret := resolveAntigravityOAuthClient(auth)
		if id != antigravityClientID || secret != antigravityClientSecret {
			t.Fatalf("expected fallback to built-in client, got (%q, %q)", id, secret)
		}
	})
}

func TestRefreshTokenSingleFlightUsesPerCredentialOAuthClient(t *testing.T) {
	var captured url.Values
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		vals, _ := url.ParseQuery(string(body))
		captured = vals
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"new-tok","expires_in":3600,"token_type":"Bearer"}`)),
			Request:    req,
		}, nil
	})

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
	exec := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "test-per-cred-client",
		Provider: "antigravity",
		Metadata: map[string]any{
			"client_id":     "new-cred-client-id",
			"client_secret": "new-cred-client-secret",
		},
	}

	data, err := exec.refreshTokenSingleFlight(ctx, auth, "rt-123")
	if err != nil {
		t.Fatalf("refreshTokenSingleFlight failed: %v", err)
	}
	if data.AccessToken != "new-tok" {
		t.Fatalf("unexpected access token: %q", data.AccessToken)
	}
	if captured.Get("client_id") != "new-cred-client-id" || captured.Get("client_secret") != "new-cred-client-secret" {
		t.Fatalf("expected per-credential client in form, got id=%q secret=%q", captured.Get("client_id"), captured.Get("client_secret"))
	}
}
