package management

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConfigV8ClientMultiAgentMigration(t *testing.T) {
	for _, raw := range []string{
		"codex: {optimize-multi-agent-v2: true}\n",
		"providers: {codex: {optimize-multi-agent-v2: true}}\n",
		"oauth: {providers: {codex: {optimize-multi-agent-v2: true}}}\n",
	} {
		t.Run(raw, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			raw += "client: {codex: {enable-apply-patch: true}}\n"
			if errWrite := os.WriteFile(path, []byte(raw), 0600); errWrite != nil {
				t.Fatalf("write: %v", errWrite)
			}
			cfg, errLoad := config.LoadConfig(path)
			if errLoad != nil {
				t.Fatalf("load: %v", errLoad)
			}
			h := &Handler{cfg: cfg, configFilePath: path}
			router := gin.New()
			router.GET("/v8/management/config/*path", h.ConfigV8)
			router.PUT("/v8/management/config/*path", h.ConfigV8)
			router.DELETE("/v8/management/config/*path", h.ConfigV8)
			request := func(method, url, body string, wantStatus int) string {
				t.Helper()
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, httptest.NewRequest(method, url, strings.NewReader(body)))
				if recorder.Code != wantStatus {
					t.Fatalf("%s %s: status=%d body=%s", method, url, recorder.Code, recorder.Body.String())
				}
				return recorder.Body.String()
			}
			const canonical = "/v8/management/config/client/codex/optimize-multi-agent-v2"
			if got := request(http.MethodGet, canonical, "", http.StatusOK); got != "true" {
				t.Fatalf("GET canonical value = %s, want true", got)
			}
			request(http.MethodPut, canonical, `"invalid"`, http.StatusUnprocessableEntity)
			for _, old := range []string{"providers/codex", "oauth/providers/codex", "codex"} {
				request(http.MethodPut, "/v8/management/config/"+old+"/optimize-multi-agent-v2", "true", http.StatusBadRequest)
			}
			data, errRead := os.ReadFile(path)
			if errRead != nil || !bytes.Equal(data, []byte(raw)) {
				t.Fatalf("GET or rejected write changed legacy file: %v", errRead)
			}
			for _, enabled := range []bool{false, true} {
				body := "false"
				if enabled {
					body = "true"
				}
				request(http.MethodPut, canonical, body, http.StatusOK)
				if h.cfg.Client.Codex.OptimizeMultiAgentV2 != enabled || !h.cfg.Client.Codex.EnableApplyPatch {
					t.Fatal("management update changed client settings")
				}
				data, errRead = os.ReadFile(path)
				if errRead != nil {
					t.Fatalf("read saved config: %v", errRead)
				}
				if errValidate := config.ValidateV8Config(data); errValidate != nil {
					t.Fatalf("invalid saved config: %v", errValidate)
				}
				loaded, errReload := config.LoadConfig(path)
				if errReload != nil || loaded.Client.Codex.OptimizeMultiAgentV2 != enabled {
					t.Fatalf("saved client flag differs: %v", errReload)
				}
			}
			request(http.MethodDelete, canonical, "", http.StatusOK)
			if h.cfg.Client.Codex.OptimizeMultiAgentV2 || !h.cfg.Client.Codex.EnableApplyPatch {
				t.Fatal("delete did not restore default false or changed apply_patch")
			}
		})
	}
}
