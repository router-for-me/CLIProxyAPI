package management

import (
	"crypto/rand"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCaptureManagementLeaseAndCursorValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	old := logging.DesktopCaptures
	logging.DesktopCaptures = logging.NewDurableCaptureSpool(t.TempDir(), key)
	defer func() { _ = logging.DesktopCaptures.Close(); logging.DesktopCaptures = old }()
	h := &Handler{}
	router := gin.New()
	router.PUT("/lease", h.LeaseCaptures)
	router.GET("/captures", h.GetCaptures)
	for _, test := range []struct {
		method, path, body string
		want               int
	}{
		{"PUT", "/lease", `{"enabled":true}`, 409},
		{"PUT", "/lease", `{"enabled":true,"version":2}`, 200},
		{"GET", "/captures?after=0", "", 200},
		{"GET", "/captures?after=-1", "", 400},
		{"GET", "/captures?after=invalid", "", 400},
		{"PUT", "/lease", `{"enabled":"true"}`, 400},
		{"PUT", "/lease", `{}`, 400},
		{"PUT", "/lease", `{"enabled":false}`, 200},
	} {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(res, req)
		if res.Code != test.want {
			t.Fatalf("%s %s=%d, want %d", test.method, test.path, res.Code, test.want)
		}
		if test.path == "/captures?after=0" && !strings.Contains(res.Body.String(), `"version":2`) {
			t.Fatal("missing capture version")
		}
	}
}
