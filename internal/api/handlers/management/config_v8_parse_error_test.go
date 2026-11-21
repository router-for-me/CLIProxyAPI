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

func TestConfigV8YAMLSyntaxErrorReportsLine(t *testing.T) {
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

	cases := []struct{ name, body, want string }{
		{"unclosed flow sequence", "config-version: 8\n# comment\n\nserver:\n  port: [8317\n", "yaml: line 5: did not find expected ',' or ']'"},
		{"dedent mismatch", "config-version: 8\nserver:\n   host: a\n  port: 1\n", "yaml: line 4: did not find expected key"},
		{"tab indentation", "config-version: 8\nserver:\n\tport: 1\n", "yaml: line 3: found character that cannot start any token"},
		{"unclosed quote", "config-version: 8\nserver: \"oops\nother: 2\n", "yaml: line 2: found unexpected end of stream"},
	}
	for _, tc := range cases {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/v8/management/config.yaml", strings.NewReader(tc.body)))
		var got struct{ Error, Message string }
		if err = json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusBadRequest || got.Error != "invalid_body" || got.Message != tc.want {
			t.Errorf("%s: status=%d got %+v, want message %q", tc.name, recorder.Code, got, tc.want)
		}
	}
	if data, _ := os.ReadFile(path); string(data) != raw {
		t.Fatal("invalid YAML changed the file")
	}
}
