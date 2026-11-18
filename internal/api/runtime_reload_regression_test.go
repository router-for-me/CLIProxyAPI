package api

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestHandlerRuntimeReloadConcurrentWithModelLists(t *testing.T) {
	server := newTestServer(t)
	host := &mockModelListInterceptorHost{}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 500; i++ {
			server.handlers.SetPluginHost(host)
			server.handlers.UpdateClients(&config.SDKConfig{RequestLog: i%2 == 0})
			server.handlers.SetPluginHost(nil)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 500; i++ {
			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("User-Agent", "codex_cli_rs/0.100.0")
			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Errorf("model list status = %d, want 200", rr.Code)
				return
			}
		}
	}()
	close(start)
	wg.Wait()
}
