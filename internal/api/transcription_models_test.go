package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestTranscriptionModelDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name       string
		credential *auth.Auth
		want       bool
	}{
		{"no credentials", nil, false},
		{"OAuth", &auth.Auth{ID: "discovery-oauth", Provider: "codex", Status: auth.StatusActive, Attributes: map[string]string{auth.AttributeAuthKind: auth.AuthKindOAuth}}, true},
		{"API key", &auth.Auth{ID: "discovery-key", Provider: "codex", Status: auth.StatusActive, Attributes: map[string]string{auth.AttributeAPIKey: "synthetic-key"}}, false},
		{"disabled OAuth", &auth.Auth{ID: "discovery-disabled", Provider: "codex", Status: auth.StatusActive, Disabled: true, Attributes: map[string]string{auth.AttributeAuthKind: auth.AuthKindOAuth}}, false},
		{"disabled status", &auth.Auth{ID: "discovery-disabled-status", Provider: "codex", Status: auth.StatusDisabled, Attributes: map[string]string{auth.AttributeAuthKind: auth.AuthKindOAuth}}, false},
		{"other provider", &auth.Auth{ID: "discovery-claude", Provider: "claude", Status: auth.StatusActive, Attributes: map[string]string{auth.AttributeAuthKind: auth.AuthKindOAuth}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newTestServer(t)
			server.handlers.AuthManager.RegisterExecutor(executor.NewCodexExecutor(server.cfg))
			if tc.credential != nil {
				if _, err := server.handlers.AuthManager.Register(context.Background(), tc.credential); err != nil {
					t.Fatal(err)
				}
			}
			proxy := httptest.NewServer(server.engine)
			defer proxy.Close()
			for _, path := range []string{"/v1/models", "/v1/models/codex-transcribe"} {
				request, err := http.NewRequest(http.MethodGet, proxy.URL+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Authorization", "Bearer test-key")
				response, err := proxy.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				payload, errRead := io.ReadAll(response.Body)
				if err := response.Body.Close(); err != nil {
					t.Fatal(err)
				}
				if errRead != nil {
					t.Fatal(errRead)
				}
				if path == "/v1/models" {
					var catalog struct {
						Data []struct{ ID string } `json:"data"`
					}
					if response.StatusCode != 200 || json.Unmarshal(payload, &catalog) != nil {
						t.Fatal("model catalog failed")
					}
					count := 0
					for _, model := range catalog.Data {
						if model.ID == "codex-transcribe" {
							count++
						}
					}
					if (count == 1) != tc.want || count > 1 {
						t.Fatalf("OpenWhispr model availability check: advertised=%d wantAvailable=%v", count, tc.want)
					}
				} else if tc.want {
					if response.StatusCode != 200 || !strings.Contains(string(payload), `"id":"codex-transcribe"`) {
						t.Fatalf("model detail not discoverable: HTTP %d", response.StatusCode)
					}
				} else if response.StatusCode != 404 {
					t.Fatalf("unavailable transcription model detail returned HTTP %d", response.StatusCode)
				}
			}
		})
	}
}
