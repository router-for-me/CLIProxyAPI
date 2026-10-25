package cliproxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type kiroCatalogTransport func(*http.Request) (*http.Response, error)

func (f kiroCatalogTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestKiroModelDiscoveryPreparesRefreshOnlyImport(t *testing.T) {
	const profile = "arn:aws:codewhisperer:us-east-1:123:profile/test"
	var refreshes, listings int
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kiroCatalogTransport(func(req *http.Request) (*http.Response, error) {
		payload := ""
		switch {
		case req.URL.Path == "/refreshToken":
			refreshes++
			payload = `{"accessToken":"new-access","refreshToken":"rotated-refresh","expiresIn":3600,"profileArn":"` + profile + `"}`
		case req.Header.Get("X-Amz-Target") == "KiroControlPlaneBearerService.ListAvailableModels":
			listings++
			if req.URL.Host != "management.us-east-1.kiro.dev" || req.Header.Get("Authorization") != "Bearer new-access" {
				t.Error("discovery used the wrong endpoint or a stale token")
			}
			body, _ := io.ReadAll(req.Body)
			if !strings.Contains(string(body), profile) {
				t.Error("model request lost the refreshed profile")
			}
			payload = `{"models":[{"modelId":"upstream/model-one","tokenLimits":{"maxInputTokens":200000}}]}`
		default:
			t.Fatalf("unexpected request: %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
	}))
	manager := coreauth.NewManager(nil, nil, nil)
	auth, err := manager.Register(coreauth.WithSkipPersist(ctx), &coreauth.Auth{ID: t.Name(), Provider: "kiro", Metadata: map[string]any{"type": "kiro", "refresh_token": "test-refresh", "auth_method": "social"}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{cfg: &config.Config{}, coreManager: manager}
	catalog, err := s.credentialModelCatalog(ctx, auth, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 1 || catalog.Models[0].ID != "upstream/model-one" || refreshes != 1 || listings != 1 {
		t.Fatalf("invalid discovery result: refresh=%d listings=%d", refreshes, listings)
	}
	current, ok := manager.GetByID(auth.ID)
	if !ok || current.Metadata["refresh_token"] != "rotated-refresh" || current.Metadata["profile_arn"] != profile {
		t.Fatal("discovery did not publish prepared credentials")
	}
	GlobalModelRegistry().UnregisterClient(auth.ID)
}
