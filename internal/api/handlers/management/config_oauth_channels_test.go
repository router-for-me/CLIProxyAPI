package management

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestOAuthMapPutValidatesBeforeNormalization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []struct {
		field, value string
		put          func(*Handler, *gin.Context)
		get          func(*config.Config) any
		want         any
	}{
		{"model-alias", `[{"name":" upstream ","alias":" public "}]`, (*Handler).PutOAuthModelAlias,
			func(c *config.Config) any { return c.OAuthModelAlias }, map[string][]config.OAuthModelAlias{"plugin-provider": {{Name: "upstream", Alias: "public"}}}},
		{"excluded-models", `[" PRIVATE-* ","private-*"]`, (*Handler).PutOAuthExcludedModels,
			func(c *config.Config) any { return c.OAuthExcludedModels }, map[string][]string{"plugin-provider": {"private-*"}}},
		{"request-scoped-errors", `[{"status":400,"match":[" invalid input "],"action":" cooldown "}]`, (*Handler).PutOAuthRequestScopedErrors,
			func(c *config.Config) any { return c.OAuthRequestScopedErrors }, map[string][]config.RequestScopedErrorRule{"plugin-provider": {{Status: 400, Match: []string{"invalid input"}, MatchRegexr: []string{}, Action: "cooldown"}}}},
	} {
		for _, form := range []string{"bare", "items"} {
			for _, tc := range []struct {
				name, entries string
				valid         bool
			}{
				{"case collision", fmt.Sprintf(`{"CODEX":%s,"codex":%s}`, endpoint.value, endpoint.value), false},
				{"whitespace collision", fmt.Sprintf(`{"codex":%s," codex ":[]}`, endpoint.value), false},
				{"empty plugin collision", `{" Plugin-Provider ":[],"plugin-provider":null}`, false},
				{"unambiguous plugin", fmt.Sprintf(`{" Plugin-Provider ":%s,"":[]," ":null}`, endpoint.value), true},
			} {
				t.Run(endpoint.field+"/"+form+"/"+tc.name, func(t *testing.T) {
					h, path, router := newFullDocumentHandler(t)
					before := h.cfg.CloneForRuntime()
					original, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					url := "/v0/management/oauth-" + endpoint.field
					router.PUT(url, func(c *gin.Context) { endpoint.put(h, c) })
					body := tc.entries
					if form == "items" {
						body = `{"items":` + body + `}`
					}
					rec := httptest.NewRecorder()
					router.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, url, strings.NewReader(body)))
					if tc.valid {
						if rec.Code != http.StatusOK {
							t.Fatalf("valid save status=%d", rec.Code)
						}
						loaded, err := config.LoadConfig(path)
						if err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(endpoint.get(loaded), endpoint.want) || !reflect.DeepEqual(endpoint.get(h.cfg), endpoint.want) {
							t.Fatal("valid save did not preserve normalized entries on disk and in memory")
						}
						return
					}
					if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "normalize to") {
						t.Errorf("ambiguous save status=%d body=%s", rec.Code, rec.Body.String())
					}
					after, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if string(after) != string(original) || !reflect.DeepEqual(h.cfg, before) || h.reloadGeneration != 0 {
						t.Fatal("rejected map changed disk, handler, or reload generation")
					}
				})
			}
		}
	}
}
