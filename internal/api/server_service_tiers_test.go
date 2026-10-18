package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestModelsWithClientVersionHomeUltrafast(t *testing.T) {
	for _, toolSearch := range []bool{false, true} {
		for _, tc := range []struct {
			name      string
			payload   string
			localPlan string
			want      map[string]bool
		}{
			{
				name: "mixed Home pool",
				payload: `{
					"codex-pro":[
						{"id":"gpt-6.1-sol","supports_ultrafast":true},
						{"id":"gpt-6-astra","supports_ultrafast":true},
						{"id":"team/gpt-6-astra","supports_ultrafast":true},
						{"id":"sol-fast","metadata_model_id":"gpt-6.1-sol","supports_ultrafast":true},
						{"id":"team/sol-fast","metadata_model_id":"gpt-6.1-sol","supports_ultrafast":true},
						{"id":"old-sol","metadata_model_id":"gpt-6-sol","supports_ultrafast":true},
						{"id":"gpt-6-sol","supports_ultrafast":true}
					],
					"codex-plus":[
						{"id":"gpt-6.1-sol","supports_ultrafast":false},
						{"id":"gpt-6-astra"},
						{"id":"sol-fast"},
						{"id":"ordinary/gpt-6.1-sol"}
					]
				}`,
				localPlan: "pro",
				want: map[string]bool{
					"gpt-6.1-sol": true, "gpt-6-astra": true, "team/gpt-6-astra": true,
					"sol-fast": true, "team/sol-fast": true, "old-sol": false,
					"gpt-6-sol": false, "ordinary/gpt-6.1-sol": false,
				},
			},
			{
				name:      "older Home ignores local ProMax",
				payload:   `{"codex-pro":[{"id":"gpt-6.1-sol"},{"id":"gpt-6-astra"}]}`,
				localPlan: "promax",
				want:      map[string]bool{"gpt-6.1-sol": false, "gpt-6-astra": false},
			},
		} {
			t.Run(fmt.Sprintf("%s/tool_search=%t", tc.name, toolSearch), func(t *testing.T) {
				server := newTestServer(t)
				server.cfg.Home.Enabled = true
				setClaudeToolSearchForTest(t, toolSearch)
				if _, errRegister := server.handlers.AuthManager.Register(t.Context(), &auth.Auth{
					ID: "home-ultrafast-local", Provider: "codex", Status: auth.StatusActive,
					Attributes: map[string]string{"plan_type": tc.localPlan},
				}); errRegister != nil {
					t.Fatal(errRegister)
				}
				engine := gin.New()
				engine.GET("/v1/models", server.unifiedModelsHandler(nil, nil))
				client := newHomeCatalogClient(t, tc.payload)
				previousHome := home.Current()
				home.SetCurrent(client)
				t.Cleanup(func() { home.SetCurrent(previousHome) })

				request := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.162.0", nil)
				request.Header.Set("Authorization", "Bearer test-key")
				recorder := httptest.NewRecorder()
				engine.ServeHTTP(recorder, request)
				if recorder.Code != http.StatusOK {
					t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
				}
				if strings.Contains(recorder.Body.String(), "metadata_model_id") {
					t.Fatal("Home metadata identity leaked into the client catalog")
				}
				var response struct {
					Models []struct {
						Slug          string                `json:"slug"`
						Priority      int                   `json:"priority"`
						ContextWindow int                   `json:"context_window"`
						ServiceTiers  []struct{ ID string } `json:"service_tiers"`
					} `json:"models"`
				}
				if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
					t.Fatal(errUnmarshal)
				}
				if len(response.Models) != len(tc.want) {
					t.Fatalf("got %d models, want %d", len(response.Models), len(tc.want))
				}
				for _, model := range response.Models {
					want := []string{"priority"}
					if tc.want[model.Slug] {
						want = append(want, "ultrafast")
					}
					var got []string
					for _, tier := range model.ServiceTiers {
						got = append(got, tier.ID)
					}
					if !reflect.DeepEqual(got, want) {
						t.Errorf("%s tiers = %v, want %v", model.Slug, got, want)
					}
					if model.Slug == "sol-fast" || model.Slug == "team/sol-fast" {
						for _, canonical := range response.Models {
							if canonical.Slug == "gpt-6.1-sol" && (model.Priority != canonical.Priority || model.ContextWindow != canonical.ContextWindow) {
								t.Errorf("%s lost canonical template metadata", model.Slug)
							}
						}
					}
				}

				request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
				request.Header.Set("Authorization", "Bearer test-key")
				recorder = httptest.NewRecorder()
				engine.ServeHTTP(recorder, request)
				if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "ultrafast") {
					t.Fatalf("ordinary Home catalog changed: %s", recorder.Body.String())
				}
			})
		}
	}
}
