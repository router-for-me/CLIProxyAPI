package cliproxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/pprof"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

type pprofServer struct {
	mu      sync.Mutex
	server  *http.Server
	addr    string
	enabled bool
}

func newPprofServer() *pprofServer {
	return &pprofServer{}
}

func (s *Service) applyPprofConfig(cfg *config.Config) {
	if s == nil || cfg == nil {
		return
	}
	if s.pprofServer == nil {
		s.pprofServer = newPprofServer()
	}
	s.pprofServer.Apply(cfg)
}

func (s *Service) shutdownPprof(ctx context.Context) error {
	if s == nil || s.pprofServer == nil {
		return nil
	}
	return s.pprofServer.Shutdown(ctx)
}

func (p *pprofServer) Apply(cfg *config.Config) {
	if p == nil || cfg == nil {
		return
	}
	addr := strings.TrimSpace(cfg.Pprof.Addr)
	if addr == "" {
		addr = config.DefaultPprofAddr
	}
	enabled := cfg.Pprof.Enable

	p.mu.Lock()
	currentServer := p.server
	currentAddr := p.addr
	p.addr = addr
	p.enabled = enabled
	if !enabled {
		p.server = nil
		p.mu.Unlock()
		if currentServer != nil {
			p.stopServer(currentServer, currentAddr, "disabled")
		}
		return
	}
	if currentServer != nil && currentAddr == addr {
		p.mu.Unlock()
		return
	}
	p.server = nil
	p.mu.Unlock()

	if currentServer != nil {
		p.stopServer(currentServer, currentAddr, "restarted")
	}

	p.startServer(addr)
}

func (p *pprofServer) Shutdown(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	currentServer := p.server
	currentAddr := p.addr
	p.server = nil
	p.enabled = false
	p.mu.Unlock()

	if currentServer == nil {
		return nil
	}
	return p.stopServerWithContext(ctx, currentServer, currentAddr, "shutdown")
}

func (p *pprofServer) startServer(addr string) {
	mux := newPprofMux()
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	p.mu.Lock()
	if !p.enabled || p.addr != addr || p.server != nil {
		p.mu.Unlock()
		return
	}
	p.server = server
	p.mu.Unlock()

	log.Infof("pprof server starting on %s", addr)
	go func() {
		if errServe := server.ListenAndServe(); errServe != nil && !errors.Is(errServe, http.ErrServerClosed) {
			log.Errorf("pprof server failed on %s: %v", addr, errServe)
			p.mu.Lock()
			if p.server == server {
				p.server = nil
			}
			p.mu.Unlock()
		}
	}()
}

func (p *pprofServer) stopServer(server *http.Server, addr string, reason string) {
	_ = p.stopServerWithContext(context.Background(), server, addr, reason)
}

func (p *pprofServer) stopServerWithContext(ctx context.Context, server *http.Server, addr string, reason string) error {
	if server == nil {
		return nil
	}
	stopCtx := ctx
	if stopCtx == nil {
		stopCtx = context.Background()
	}
	stopCtx, cancel := context.WithTimeout(stopCtx, 5*time.Second)
	defer cancel()
	if errStop := server.Shutdown(stopCtx); errStop != nil {
		log.Errorf("pprof server stop failed on %s: %v", addr, errStop)
		return errStop
	}
	log.Infof("pprof server stopped on %s (%s)", addr, reason)
	return nil
}

func newPprofMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.Handle("/debug/pprof/allocs", pprof.Handler("allocs"))
	mux.Handle("/debug/pprof/block", pprof.Handler("block"))
	mux.Handle("/debug/pprof/goroutine", pprof.Handler("goroutine"))
	mux.Handle("/debug/pprof/heap", pprof.Handler("heap"))
	mux.Handle("/debug/pprof/mutex", pprof.Handler("mutex"))
	mux.Handle("/debug/pprof/threadcreate", pprof.Handler("threadcreate"))
	return mux
}
