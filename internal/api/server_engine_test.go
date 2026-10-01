package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestNewGinEngineEncodedSlashParam guards the fix for model ids containing
// "/" (e.g. "openai/gpt-4o"). The dashboard percent-encodes the id as %2F;
// Go decodes that into URL.Path, so a Gin engine routing on the decoded path
// fails to match the single-segment :id route (and returns 404). Routing on
// the raw path keeps the encoded segment intact while the default parameter
// unescaping still hands the handler the original value.
func TestNewGinEngineEncodedSlashParam(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newGinEngine()
	r.GET("/models-catalog/global/:id", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"id": c.Param("id")})
	})

	tests := []struct {
		name string
		path string
	}{
		{name: "encoded slash", path: "/models-catalog/global/openai%2Fgpt-4o"},
		{name: "plain segment", path: "/models-catalog/global/gpt-4o"},
		{name: "encoded plus and space", path: "/models-catalog/global/foo%2Bbar%20baz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("path %q: got status %d, want %d (body=%s)", tt.path, w.Code, http.StatusOK, w.Body.String())
			}
			want := map[string]string{
				"encoded slash":          `{"id":"openai/gpt-4o"}`,
				"plain segment":          `{"id":"gpt-4o"}`,
				"encoded plus and space": `{"id":"foo+bar baz"}`,
			}[tt.name]
			if got := w.Body.String(); got != want {
				t.Errorf("path %q: got body %s, want %s", tt.path, got, want)
			}
		})
	}
}
