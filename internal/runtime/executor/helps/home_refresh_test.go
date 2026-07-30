package helps

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestStatusFromHomeErrorCodeMapsAuthenticationErrorToUnauthorized(t *testing.T) {
	if got := statusFromHomeErrorCode("authentication_error"); got != http.StatusUnauthorized {
		t.Fatalf("statusFromHomeErrorCode(authentication_error) = %d, want %d", got, http.StatusUnauthorized)
	}
	if got := statusFromHomeErrorCode("unauthorized"); got != http.StatusUnauthorized {
		t.Fatalf("statusFromHomeErrorCode(unauthorized) = %d, want %d", got, http.StatusUnauthorized)
	}
	if got := statusFromHomeErrorCode("refresh_temporarily_unavailable"); got != http.StatusServiceUnavailable {
		t.Fatalf("statusFromHomeErrorCode(refresh_temporarily_unavailable) = %d, want %d", got, http.StatusServiceUnavailable)
	}
}

type fakeHomeRefreshClient struct {
	calls           atomic.Int32
	authIndex       string
	lastRefreshedAt time.Time
	accessTokenHash string
	raw             []byte
	err             error
}

func (c *fakeHomeRefreshClient) HeartbeatOK() bool {
	return true
}

func (c *fakeHomeRefreshClient) GetRefreshAuth(_ context.Context, authIndex string, lastRefreshedAt time.Time, accessTokenHash string) ([]byte, error) {
	c.calls.Add(1)
	c.authIndex = authIndex
	c.lastRefreshedAt = lastRefreshedAt
	c.accessTokenHash = accessTokenHash
	return c.raw, c.err
}

func TestRefreshAuthViaHomePreservesContextErrors(t *testing.T) {
	client := &fakeHomeRefreshClient{err: context.DeadlineExceeded}
	oldCurrentHomeRefreshClient := currentHomeRefreshClient
	currentHomeRefreshClient = func() homeRefreshClient { return client }
	t.Cleanup(func() { currentHomeRefreshClient = oldCurrentHomeRefreshClient })

	cfg := &config.Config{Home: config.HomeConfig{Enabled: true}}
	auth := &cliproxyauth.Auth{ID: "home-auth", Index: "home-auth", Provider: "codex"}
	_, handled, errRefresh := RefreshAuthViaHome(context.Background(), cfg, auth)
	if !handled || !errors.Is(errRefresh, context.DeadlineExceeded) {
		t.Fatalf("RefreshAuthViaHome() = handled %v err %v, want true/context.DeadlineExceeded", handled, errRefresh)
	}
}

func TestAuthAccessTokenSHA256SupportsKnownMetadataShapes(t *testing.T) {
	want := authAccessTokenSHA256(&cliproxyauth.Auth{Metadata: map[string]any{"access_token": "same-token"}})
	cases := map[string]*cliproxyauth.Auth{
		"camel case":        {Metadata: map[string]any{"accessToken": "same-token"}},
		"nested any map":    {Metadata: map[string]any{"token": map[string]any{"access_token": "same-token"}}},
		"nested string map": {Metadata: map[string]any{"Token": map[string]string{"accessToken": "same-token"}}},
	}
	for name, auth := range cases {
		t.Run(name, func(t *testing.T) {
			if got := authAccessTokenSHA256(auth); got == "" || got != want {
				t.Fatalf("token hash = %q, want %q", got, want)
			}
		})
	}
}

func TestRefreshAuthViaHomeAcceptsAuthEnvelope(t *testing.T) {
	raw, errMarshal := json.Marshal(struct {
		Auth      cliproxyauth.Auth `json:"auth"`
		AuthIndex string            `json:"auth_index"`
	}{
		Auth: cliproxyauth.Auth{
			ID:       "home-auth-1",
			Provider: "antigravity",
			Metadata: map[string]any{
				"access_token": "new-access-token",
			},
		},
		AuthIndex: "home-index-1",
	})
	if errMarshal != nil {
		t.Fatalf("marshal home envelope: %v", errMarshal)
	}

	client := &fakeHomeRefreshClient{raw: raw}
	oldCurrentHomeRefreshClient := currentHomeRefreshClient
	currentHomeRefreshClient = func() homeRefreshClient {
		return client
	}
	t.Cleanup(func() {
		currentHomeRefreshClient = oldCurrentHomeRefreshClient
	})

	cfg := &config.Config{Home: config.HomeConfig{Enabled: true}}
	observedRefreshAt := time.Now().UTC()
	auth := &cliproxyauth.Auth{
		ID:              "home-auth-1",
		Provider:        "antigravity",
		Index:           "home-index-1",
		LastRefreshedAt: observedRefreshAt,
		Metadata: map[string]any{
			"access_token":  "old-access-token",
			"refresh_token": "refresh-token",
		},
	}

	updated, handled, err := RefreshAuthViaHome(context.Background(), cfg, auth)
	if err != nil {
		t.Fatalf("RefreshAuthViaHome error: %v", err)
	}
	if !handled {
		t.Fatal("RefreshAuthViaHome handled = false, want true")
	}
	if got := client.calls.Load(); got != 1 {
		t.Fatalf("home refresh calls = %d, want 1", got)
	}
	if client.authIndex != "home-index-1" {
		t.Fatalf("home refresh auth_index = %q, want home-index-1", client.authIndex)
	}
	if !client.lastRefreshedAt.Equal(observedRefreshAt) {
		t.Fatalf("home refresh last_refreshed_at = %v, want %v", client.lastRefreshedAt, observedRefreshAt)
	}
	if client.accessTokenHash != authAccessTokenSHA256(auth) {
		t.Fatalf("home refresh access token hash = %q, want %q", client.accessTokenHash, authAccessTokenSHA256(auth))
	}
	if updated == nil {
		t.Fatal("updated auth = nil")
	}
	if got := updated.Metadata["access_token"]; got != "new-access-token" {
		t.Fatalf("updated access_token = %q, want new-access-token", got)
	}
	if updated.Index != "home-index-1" {
		t.Fatalf("updated auth_index = %q, want home-index-1", updated.Index)
	}
}
