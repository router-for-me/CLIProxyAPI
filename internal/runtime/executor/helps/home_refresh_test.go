package helps

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

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
}

type fakeHomeRefreshClient struct {
	calls     atomic.Int32
	authIndex string
	raw       []byte
}

func (c *fakeHomeRefreshClient) HeartbeatOK() bool {
	return true
}

func (c *fakeHomeRefreshClient) GetRefreshAuth(_ context.Context, authIndex string) ([]byte, error) {
	c.calls.Add(1)
	c.authIndex = authIndex
	return c.raw, nil
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
	auth := &cliproxyauth.Auth{
		ID:       "home-auth-1",
		Provider: "antigravity",
		Index:    "home-index-1",
		Metadata: map[string]any{
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
