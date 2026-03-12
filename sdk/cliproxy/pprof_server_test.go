package cliproxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestNewPprofMuxIncludesDebugVars(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/debug/vars", nil)
	recorder := httptest.NewRecorder()

	newPprofMux().ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "cliproxy_runtime") {
		t.Fatalf("/debug/vars body missing cliproxy_runtime: %s", recorder.Body.String())
	}
}

func TestPprofApplyNormalizesNegativeSampling(t *testing.T) {
	t.Parallel()

	server := newPprofServer()
	cfg := &config.Config{}
	cfg.Pprof.Addr = config.DefaultPprofAddr
	cfg.Pprof.BlockProfileRate = -1
	cfg.Pprof.MutexProfileFraction = -2

	server.Apply(cfg)

	if server.addr != config.DefaultPprofAddr {
		t.Fatalf("addr = %q, want %q", server.addr, config.DefaultPprofAddr)
	}
}
