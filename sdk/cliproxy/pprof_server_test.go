package cliproxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPprofServerStopOwnedServerKeepsReplacement(t *testing.T) {
	pprof := newPprofServer()
	oldServer := &http.Server{}
	replacement := &http.Server{}
	pprof.server = replacement

	if errStop := pprof.stopOwnedServerWithContext(context.Background(), oldServer, "old", "canceled", 1); errStop != nil {
		t.Fatalf("stopOwnedServerWithContext() error = %v", errStop)
	}
	pprof.mu.Lock()
	current := pprof.server
	pprof.mu.Unlock()
	if current != replacement {
		t.Fatal("stopping a stale pprof server removed the replacement server")
	}
}

func TestPprofServerSamePointerOwnerTransferKeepsCurrentServer(t *testing.T) {
	pprof := newPprofServer()
	server := &http.Server{}
	pprof.server = server
	pprof.addr = "127.0.0.1:6060"
	pprof.enabled = true
	pprof.owner = 1

	cfg := &config.Config{}
	cfg.Pprof.Enable = true
	cfg.Pprof.Addr = "127.0.0.1:6060"
	if !pprof.ApplyContext(context.Background(), cfg) {
		t.Fatal("ApplyContext() = false, want same-pointer owner transfer")
	}

	pprof.mu.Lock()
	owner := pprof.owner
	pprof.mu.Unlock()
	if owner == 1 {
		t.Fatal("ApplyContext() did not transfer same-server ownership")
	}
	if errStop := pprof.stopOwnedServerWithContext(context.Background(), server, cfg.Pprof.Addr, "canceled", 1); errStop != nil {
		t.Fatalf("stopOwnedServerWithContext() error = %v", errStop)
	}
	pprof.mu.Lock()
	current := pprof.server
	pprof.mu.Unlock()
	if current != server {
		t.Fatal("stale owner stopped the current same-pointer server")
	}
}

func TestPprofServerServeFailureClearsTransferredOwner(t *testing.T) {
	pprof := newPprofServer()
	server := &http.Server{}
	pprof.server = server
	pprof.owner = 2

	pprof.clearFailedServer(server)

	pprof.mu.Lock()
	current := pprof.server
	pprof.mu.Unlock()
	if current != nil {
		t.Fatal("serve failure retained a server after ownership transferred")
	}
}
