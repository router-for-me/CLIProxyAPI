package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestPutOpenAICompat_NormalizesKind(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/openai-compatibility", strings.NewReader(`[
		{"name":"demo","kind":" NewAPI ","base-url":"https://compat.example.com","api-key-entries":[{"api-key":"sk-demo"}]}
	]`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutOpenAICompat(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.OpenAICompatibility); got != 1 {
		t.Fatalf("openai compatibility len = %d, want 1", got)
	}
	if got := h.cfg.OpenAICompatibility[0].Kind; got != "newapi" {
		t.Fatalf("kind = %q, want %q", got, "newapi")
	}
}
