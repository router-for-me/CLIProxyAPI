package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
)

func TestCodexSearchReadErrorDiagnosticInDownstreamLog(t *testing.T) {
	for _, fullLog := range []bool{false, true} {
		name := "deferred"
		if fullLog {
			name = "full"
		}
		t.Run(name, func(t *testing.T) {
			server := newTestServer(t)
			server.cfg.RequestLog = fullLog
			server.handlers.AuthManager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
			server.handlers.AuthManager.PublishHomeDispatch(&codexSearchHomeDispatcher{}, executionregistry.New(), 1)
			server.handlers.AuthManager.RegisterExecutor(&codexSearchCaptureExecutor{
				statuses:     []int{http.StatusOK},
				responseBody: &errorSearchResponseBody{payload: []byte(`{"partial":true}`)},
			})
			logsDir := t.TempDir()
			router := gin.New()
			router.Use(middleware.RequestLoggingMiddleware(logging.NewFileRequestLogger(fullLog, logsDir, "", 10)))
			router.POST("/v1/alpha/search", server.codexAlphaSearch)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"model":"gpt-5-codex","query":"test"}`)))
			if recorder.Code != http.StatusBadGateway || recorder.Body.String() != `{"error":"Failed to read Codex search response"}` {
				t.Fatalf("client response changed: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			paths, errGlob := filepath.Glob(filepath.Join(logsDir, "*.log"))
			if errGlob != nil || len(paths) != 1 {
				t.Fatalf("logs = %v, error = %v", paths, errGlob)
			}
			data, errRead := os.ReadFile(paths[0])
			if errRead != nil {
				t.Fatal(errRead)
			}
			before, response, found := strings.Cut(string(data), "=== RESPONSE ===\n")
			if !found || !strings.Contains(response, "Error: "+io.ErrUnexpectedEOF.Error()+"\n\nHeaders:\n") || !strings.Contains(response, "\nBody:\n"+recorder.Body.String()) {
				t.Fatalf("response diagnostic missing:\n%s", data)
			}
			if strings.Contains(before, io.ErrUnexpectedEOF.Error()) || !strings.Contains(before, `{"partial":true}`) {
				t.Fatalf("upstream response was modified:\n%s", data)
			}
		})
	}
}
