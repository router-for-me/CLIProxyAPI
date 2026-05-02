package api

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// TestServer_ConfigHotReload_NoRaceUnderConcurrentReads pins the
// hot-reload concurrency invariant the Phase C atomic.Pointer[Config]
// refactor must preserve.
//
// Today, Server.cfg is a plain *config.Config that is replaced by
// UpdateClients (s.cfg = cfg) without any synchronization. Concurrent
// readers (request handlers, OAuth callbacks, /management.html serving,
// management API patches) read s.cfg fields directly. Under go test
// -race this surfaces as a data race.
//
// After Phase C:
//   1. internal/api.Server owns the config pointer behind atomic.Pointer.
//   2. All readers go through Server.Config() *Config (atomic load).
//   3. All writers (mgmt PutDebug / PutAmpUpstreamURL / PutAmpModelMappings,
//      OnConfigUpdated, etc.) clone-modify-persist-swap.
//   4. This test must pass cleanly under go test -race.
//
// Phase A pre-writes the test body so Phase C just deletes the t.Skip
// line below and validates the refactor.
func TestServer_ConfigHotReload_NoRaceUnderConcurrentReads(t *testing.T) {
	t.Skip("Phase A: hot-reload race documented and reproducible under -race. Un-skip after Phase C atomic.Pointer[Config] swap.")

	server := newTestServer(t)

	// Reader pool: probe two real read paths that touch s.cfg today —
	//   1. Direct field reads (mirrors patterns like s.cfg.AuthDir,
	//      s.cfg.TLS.Enable, used at server.go:408,422,436,450,672,814).
	//   2. /management.html serving (server.go:671 reads s.cfg directly).
	stop := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = server.cfg.AuthDir
					_ = server.cfg.Debug
					_ = server.cfg.LoggingToFile
				}
			}
		}()
	}

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					req := httptest.NewRequest(http.MethodGet, "/management.html", nil)
					rr := httptest.NewRecorder()
					server.engine.ServeHTTP(rr, req)
					_ = rr.Code
				}
			}
		}()
	}

	// Writer: hot-reload the config repeatedly with alternating values that
	// the readers may observe. Phase C swap mechanism must remain race-free.
	wg.Add(1)
	go func() {
		defer wg.Done()
		flip := false
		for {
			select {
			case <-stop:
				return
			default:
				flip = !flip
				newCfg := &proxyconfig.Config{
					SDKConfig:              server.cfg.SDKConfig,
					Port:                   server.cfg.Port,
					AuthDir:                server.cfg.AuthDir,
					Debug:                  flip,
					LoggingToFile:          flip,
					UsageStatisticsEnabled: !flip,
				}
				server.UpdateClients(newCfg)
			}
		}
	}()

	time.Sleep(80 * time.Millisecond)
	close(stop)
	wg.Wait()
}
