package panel

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestServe(t *testing.T) {
	if len(compressed()) == 0 {
		t.Skip("panel not built")
	}
	gin.SetMode(gin.TestMode)
	for _, enc := range []string{"", "gzip, br"} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/management.html", nil)
		c.Request.Header.Set("Accept-Encoding", enc)
		if !Serve(c) {
			t.Fatal("Serve returned false")
		}
		if w.Code != http.StatusOK {
			t.Fatalf("status %d", w.Code)
		}
		gz := w.Header().Get("Content-Encoding") == "gzip"
		if gz != (enc != "") {
			t.Fatalf("Accept-Encoding %q: gzip=%v", enc, gz)
		}
		if !gz && len(w.Body.Bytes()) < 1000 {
			t.Fatalf("body too small: %d", w.Body.Len())
		}
	}
}
