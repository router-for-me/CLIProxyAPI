package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConfigV8ValidationErrorsPointIntoTheBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := "config-version: 8\nserver: {port: 8317}\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.PUT("/v8/management/config.yaml", h.ConfigV8)
	router.PATCH("/v8/management/config", h.ConfigV8)

	// Comments, blank lines, and four-space indentation shift the re-encoded copy.
	head := "# header\n\n# more\nconfig-version: 8\n\n\nserver:\n    # note\n    host: 127.0.0.1\n\n"
	cases := []struct {
		name, method, url, body string
		status                  int
		want                    []string
	}{
		{"type error", http.MethodPut, "/v8/management/config.yaml", head + "    port: notaport\n",
			http.StatusUnprocessableEntity, []string{"line 11 (server.port): cannot unmarshal !!str `notaport` into int"}},
		{"two type errors", http.MethodPut, "/v8/management/config.yaml", head + "    port: notaport\naccess:\n    api-keys: hello\n",
			http.StatusUnprocessableEntity, []string{"line 11 (server.port): ", "line 13 (access.api-keys): "}},
		{"unknown field", http.MethodPut, "/v8/management/config.yaml", head + "    port: 8317\n    hots: 0.0.0.0\n",
			http.StatusBadRequest, []string{"line 12 (server.hots): field hots not found"}},
		{"patch names the field without a line", http.MethodPatch, "/v8/management/config", `{"server":{"port":"notaport"}}`,
			http.StatusUnprocessableEntity, []string{"server.port: cannot unmarshal"}},
		{"unknown field in a section that is a struct in both layouts", http.MethodPut, "/v8/management/config.yaml",
			head + "    port: 8317\nrouting:\n  retry:\n    request-retry: 3\n    reqest-retry: 1\n",
			http.StatusBadRequest, []string{"line 15 (routing.retry.reqest-retry): field reqest-retry not found"}},
		{"unknown field in an expanded API-key entry", http.MethodPut, "/v8/management/config.yaml",
			"config-version: 8\napi-keys:\n  codex:\n    - name: x\n      keys:\n        - api-key: hi\n          oops: 1\n",
			http.StatusBadRequest, []string{"line 7 (api-keys.codex[0].keys[0].oops): field oops not found"}},
		{"historical spelling keeps the submitted path", http.MethodPut, "/v8/management/config.yaml",
			"config-version: 8\n# gap\n\noauth:\n  providers:\n    codex:\n      stream-bootstrap-buffering: oops\n",
			http.StatusUnprocessableEntity, []string{"line 7 (oauth.providers.codex.stream-bootstrap-buffering): cannot unmarshal"}},
		{"mapping where a list belongs names the mapping", http.MethodPut, "/v8/management/config.yaml",
			head + "    port: 8317\naccess:\n  api-keys:\n    a: b\n",
			http.StatusUnprocessableEntity, []string{"line 14 (access.api-keys): cannot unmarshal !!map"}},
		{"unknown field with a block value in an expanded API-key entry", http.MethodPut, "/v8/management/config.yaml",
			"config-version: 8\napi-keys:\n  codex:\n    - name: x\n      keys:\n        - api-key: hi\n          oops:\n            a: b\n",
			http.StatusBadRequest, []string{"line 7 (api-keys.codex[0].keys[0].oops): field oops not found"}},
		{"patch names an unknown field with a block value without a line", http.MethodPatch, "/v8/management/config",
			`{"api-keys":{"codex":[{"name":"x","keys":[{"api-key":"hi","oops":{"a":"b"}}]}]}}`,
			http.StatusBadRequest, []string{"api-keys.codex[0].keys[0].oops: field oops not found"}},
		{"an aliased value is not blamed on its anchor", http.MethodPut, "/v8/management/config.yaml",
			"config-version: 8\nserver:\n  host: &x wrong\noauth:\n  providers:\n    codex:\n      stream-bootstrap-buffering: *x\n",
			http.StatusUnprocessableEntity, []string{"upstream.codex.stream-bootstrap-buffering: cannot unmarshal !!str `wrong` into bool"}},
		{"an alias left as an alias is named where it is written", http.MethodPut, "/v8/management/config.yaml",
			"config-version: 8\noauth:\n  providers:\n    codex:\n      stream-bootstrap-buffering: &a bad\nupstream:\n  codex:\n    stream-bootstrap-buffering: true\nserver:\n  port: *a\n",
			http.StatusUnprocessableEntity, []string{"line 10 (server.port): cannot unmarshal !!str `bad` into int"}},
		{"an expanded alias whose anchor normalization removes is not blamed on the anchor", http.MethodPut, "/v8/management/config.yaml",
			"config-version: 8\noauth:\n  providers:\n    codex:\n      stream-bootstrap-buffering: &a bad\n      disable-codex-cloaking: *a\nupstream:\n  codex:\n    stream-bootstrap-buffering: true\n",
			http.StatusUnprocessableEntity, []string{"upstream.codex.disable-codex-cloaking: cannot unmarshal !!str `bad` into bool"}},
		{"merged keys are named where they were merged", http.MethodPut, "/v8/management/config.yaml",
			"config-version: 8\nserver: &s\n  port: 1\n  host: a\nrouting:\n  retry:\n    <<: *s\n",
			http.StatusBadRequest, []string{"routing.retry.port: field port not found", "routing.retry.host: field host not found"}},
		{"line references inside values are not rewritten", http.MethodPut, "/v8/management/config.yaml",
			"config-version: 8\nserver:\n  port: 'line 1: x'\n",
			http.StatusUnprocessableEntity, []string{"line 3 (server.port): cannot unmarshal !!str `line 1: x` into int"}},
	}
	for _, tc := range cases {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.url, strings.NewReader(tc.body)))
		var got struct{ Error, Message string }
		if err = json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != tc.status || got.Error != "invalid_config" {
			t.Errorf("%s: status=%d body=%s", tc.name, recorder.Code, recorder.Body.String())
			continue
		}
		for _, want := range tc.want {
			if !strings.Contains(got.Message, want) {
				t.Errorf("%s: message %q does not contain %q", tc.name, got.Message, want)
			}
		}
		// A case that expects no line must not get one, e.g. from the re-encoded document.
		if !strings.Contains(strings.Join(tc.want, ""), "line ") && strings.Contains(got.Message, "line ") {
			t.Errorf("%s: unexpected line in %q", tc.name, got.Message)
		}
	}
	if data, _ := os.ReadFile(path); string(data) != raw {
		t.Fatal("invalid config changed the file")
	}
}
