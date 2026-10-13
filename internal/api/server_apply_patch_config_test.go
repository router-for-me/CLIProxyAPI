package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
)

func TestModelsApplyPatchClientConfigAndReload(t *testing.T) {
	for _, source := range []string{"local", "home"} {
		t.Run(source, func(t *testing.T) {
			modelRegistry := registry.GetGlobalRegistry()
			models := []*registry.ModelInfo{{ID: "gpt-5.5"}, {ID: "config-patch-synthetic"}, {ID: "gpt-image-2"}}
			modelRegistry.RegisterClient("config-patch-models", "codex", models)
			t.Cleanup(func() { modelRegistry.UnregisterClient("config-patch-models") })
			modelRegistry.RegisterClient("config-patch-unknown", "remote", []*registry.ModelInfo{{ID: "config-patch-unknown"}})
			t.Cleanup(func() { modelRegistry.UnregisterClient("config-patch-unknown") })
			server := newTestServer(t)
			server.handlers.AuthManager.RegisterExecutor(executor.NewCodexAutoExecutor(&config.Config{}))
			engine := server.engine
			if source == "home" {
				server.cfg.Home.Enabled = true
				client := newHomeCatalogClient(t, `{"codex":[{"id":"gpt-5.5"},{"id":"config-patch-synthetic"},{"id":"gpt-image-2"}],"remote":[{"id":"config-patch-unknown"}]}`)
				previousHome := home.Current()
				home.SetCurrent(client)
				t.Cleanup(func() { home.SetCurrent(previousHome) })
				engine = gin.New()
				engine.GET("/v1/models", server.unifiedModelsHandler(nil, nil))
			}
			for _, tc := range []struct {
				name string
				raw  string
				want bool
			}{
				{"omitted", "{}", false},
				{"disabled", "client: {codex: {enable-apply-patch: false}}", false},
				{"enabled", "client: {codex: {enable-apply-patch: true}}", true},
				{"disabled-again", "client: {codex: {enable-apply-patch: false}}", false},
				{"enabled-again", "client: {codex: {enable-apply-patch: true}}", true},
				{"removed", "{}", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if tc.name != "omitted" {
						parsed, errParse := config.ParseConfigBytes([]byte(tc.raw))
						if errParse != nil {
							t.Fatalf("parse client config: %v", errParse)
						}
						updatedCfg := *server.cfg
						updatedCfg.Client = parsed.Client
						server.UpdateClients(&updatedCfg)
					}
					for _, version := range []string{"", "0.137.0", "0.153.4", "cpa"} {
						request := httptest.NewRequest(http.MethodGet, "/v1/models?client_version="+version, nil)
						request.Header.Set("Authorization", "Bearer test-key")
						recorder := httptest.NewRecorder()
						engine.ServeHTTP(recorder, request)
						if recorder.Code != http.StatusOK {
							t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
						}
						var response struct {
							Models []map[string]any `json:"models"`
						}
						if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
							t.Fatalf("decode catalog: %v", errUnmarshal)
						}
						seen := 0
						for _, entry := range response.Models {
							var want any
							switch entry["slug"] {
							case "gpt-5.5":
								want = "freeform"
							case "config-patch-synthetic":
								if tc.want {
									want = "freeform"
								}
							case "gpt-image-2", "config-patch-unknown":
							default:
								continue
							}
							seen++
							if value, present := entry["apply_patch_tool_type"]; !present || value != want {
								t.Errorf("version %q model %s: apply_patch_tool_type = %#v (present %t), want %#v", version, entry["slug"], value, present, want)
							}
						}
						if seen != 4 {
							t.Fatalf("checked %d models, want 4", seen)
						}
					}
				})
			}
		})
	}
}
