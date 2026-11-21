package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestCopilotRefreshOnlyExchangesCredentials(t *testing.T) {
	for _, base := range []string{"https://api.individual.githubcopilot.com", "https://api.business.githubcopilot.com"} {
		t.Run(base, func(t *testing.T) {
			auth := copilotTestAuth(t, "a", "test-model", copilot.Chat)
			auth.Metadata["refresh_token"] = "github-grant"
			previous := auth.Metadata[copilot.CatalogKey]
			calls := 0
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path != "/copilot_internal/v2/token" || r.URL.Host != "api.github.com" || r.Header.Get("Authorization") != "Bearer github-grant" {
					t.Fatalf("credential refresh must not fetch the directory: %s", r.URL.Path)
				}
				payload := fmt.Sprintf(`{"token":"replacement","expires_at":%d,"endpoints":{"api":%q}}`, time.Now().Add(time.Hour).Unix(), base)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: r}, nil
			}))
			updated, err := NewCopilotExecutor(&config.Config{}).Refresh(ctx, auth)
			if err != nil || calls != 1 || updated.Metadata["access_token"] != "replacement" {
				t.Fatalf("refresh did not return the new token: %v (calls=%d)", err, calls)
			}
			if base == auth.Metadata["base_url"] {
				if updated.Metadata[copilot.CatalogKey] != previous {
					t.Fatal("same-origin refresh discarded the cached catalog")
				}
			} else if _, ok := updated.Metadata[copilot.CatalogKey]; ok {
				t.Fatal("changed origin retained the old entitlement")
			}
			if auth.Metadata[copilot.CatalogKey] != previous || auth.Metadata["access_token"] == "replacement" {
				t.Fatal("refresh mutated its input")
			}
		})
	}
}
