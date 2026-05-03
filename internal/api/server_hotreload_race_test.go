package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
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
//  1. internal/api.Server owns the config pointer behind atomic.Pointer.
//  2. All readers go through Server.Config() *Config (atomic load).
//  3. All writers (mgmt PutDebug / PutAmpUpstreamURL / PutAmpModelMappings,
//     OnConfigUpdated, etc.) clone-modify-persist-swap.
//  4. This test must pass cleanly under go test -race.
//
// Phase A pre-writes the test body so Phase C just deletes the t.Skip
// line below and validates the refactor.
func TestServer_ConfigHotReload_NoRaceUnderConcurrentReads(t *testing.T) {
	server := newTestServer(t)

	// Reader pool: probe two real read paths that touch the server config —
	//   1. Direct snapshot reads via Server.Config() (mirrors patterns like
	//      s.Config().AuthDir at server.go OAuth callbacks and TLS init).
	//   2. /management.html serving (reads via Server.Config()).
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
					cfg := server.Config()
					if cfg != nil {
						_ = cfg.AuthDir
						_ = cfg.Debug
						_ = cfg.LoggingToFile
					}
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
				cur := server.Config()
				newCfg := &proxyconfig.Config{
					SDKConfig:              cur.SDKConfig,
					Port:                   cur.Port,
					AuthDir:                cur.AuthDir,
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

// TestServer_MgmtHandlers_HotReloadRace drives real mgmt handlers
// (PutDebug, PutAmpUpstreamURL, PutAmpModelMappings) directly against
// the Handler concurrently with Server.Config() readers. Each handler
// enters applyConfigChange — clones the snapshot, mutates, persists,
// atomic-swaps Handler.cfgPtr, fires the commit hook (which does
// Server.UpdateClients fan-out and re-binds executors). The race test
// proves the chain stays race-clean even when readers race the writes.
//
// Codex Phase C round-6 review NIT #5 added this; the prior race test
// only exercised Server.Config() + Server.UpdateClients, missing the
// applyConfigChange and Handler.cfgPtr write path. The handler is
// invoked through gin.CreateTestContext to bypass the secret-key auth
// middleware (the middleware is not under test here; the swap chain is).
func TestServer_MgmtHandlers_HotReloadRace(t *testing.T) {
	server := newTestServer(t)

	// SaveConfigPreserveComments reads the existing YAML before merging,
	// so applyConfigChange's persist step needs a real file at the path.
	// Seed a minimal valid config so writes succeed and the swap fires.
	if err := os.WriteFile(server.configFilePath, []byte("port: 0\n"), 0o644); err != nil {
		t.Fatalf("seed config.yaml: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Reader pool: probe Server.Config() reads (mirrors live request
	// handlers, OAuth callbacks, /management.html serving).
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					cfg := server.Config()
					if cfg != nil {
						_ = cfg.AuthDir
						_ = cfg.Debug
						_ = cfg.AmpCode.UpstreamURL
						_ = cfg.AmpCode.ModelMappings
					}
				}
			}
		}()
	}

	// Writer 1: PutDebug — flips Debug bool through applyConfigChange.
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
				body := []byte(`{"value":true}`)
				if !flip {
					body = []byte(`{"value":false}`)
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/debug", bytes.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")
				server.mgmt.PutDebug(c)
			}
		}
	}()

	// Writer 2: PutAmpUpstreamURL — fires through the same chain on a
	// nested AmpCode field.
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
				url := "https://a.example.com"
				if !flip {
					url = "https://b.example.com"
				}
				body := []byte(`{"upstream_url":"` + url + `"}`)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/ampcode/upstream-url", bytes.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")
				server.mgmt.PutAmpUpstreamURL(c)
			}
		}
	}()

	// Writer 3: PutAmpModelMappings — biggest payload; triggers full
	// clone-modify-persist-swap on a slice-of-struct field.
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
				from := "model-A"
				to := "model-B"
				if !flip {
					from = "model-C"
					to = "model-D"
				}
				body := []byte(`{"model_mappings":[{"from":"` + from + `","to":"` + to + `"}]}`)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/ampcode/model-mappings", bytes.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")
				server.mgmt.PutAmpModelMappings(c)
			}
		}
	}()

	time.Sleep(120 * time.Millisecond)
	close(stop)
	wg.Wait()
}
