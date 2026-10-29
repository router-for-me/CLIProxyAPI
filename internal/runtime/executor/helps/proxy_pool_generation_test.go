package helps

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// This test changes the process-wide generation and must not run in parallel.
func TestProxyPoolGenerationRejectsOldSnapshotAfterReload(t *testing.T) {
	proxyPoolGeneration.Lock()
	previous := proxyPoolGeneration.current
	proxyTransportCache.Purge()
	proxyPoolGeneration.Unlock()
	t.Cleanup(func() {
		proxyPoolGeneration.Lock()
		proxyTransportCache.Purge()
		proxyPoolGeneration.current = previous
		proxyPoolGeneration.Unlock()
	})
	old := &config.Config{}
	AdvanceProxyPoolGeneration(old)
	oldClone := old.CloneForRuntime()
	if oldClone.ProxyPoolGeneration != old.ProxyPoolGeneration {
		t.Fatal("runtime clone lost generation")
	}
	first := pooledProxyTransport("direct", oldClone, poolOptInAuth("generation"))
	if first == nil || proxyTransportCache.Len() != 1 {
		t.Fatal("initial pool missing")
	}
	next := old.CloneForRuntime()
	AdvanceProxyPoolGeneration(next)
	if proxyTransportCache.Len() != 0 {
		t.Fatal("reload did not purge pool")
	}
	// A request captured the old snapshot before reload and resumes only after it.
	resume := make(chan struct{})
	result := make(chan *http.Transport, 1)
	go func() {
		<-resume
		result <- pooledProxyTransport("direct", oldClone, poolOptInAuth("generation"))
	}()
	close(resume)
	stale := <-result
	if stale == nil || proxyTransportCache.Len() != 0 {
		t.Fatal("stale snapshot repopulated pool")
	}
	current := pooledProxyTransport("direct", next, poolOptInAuth("generation"))
	if current == nil || proxyTransportCache.Len() != 1 {
		t.Fatal("current snapshot did not populate pool")
	}
	if pooledProxyTransport("direct", next, poolOptInAuth("generation")) != current {
		t.Fatal("current transport not reused")
	}
	if first == current || stale == current {
		t.Fatal("old and new generations share transports")
	}
}

// TestProxyPoolUnfencedSnapshotStillPools covers configs that never passed through the
// SDK generation fence: generation 0 cannot be stale, so pooling must stay on even after
// the process-wide generation has advanced. Regression for callers that build their own
// config.Config (direct api usage, helper probes) and would otherwise lose pooling
// silently once any SDK service published a config.
func TestProxyPoolUnfencedSnapshotStillPools(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)
	proxyPoolGeneration.Lock()
	previous := proxyPoolGeneration.current
	proxyPoolGeneration.Unlock()
	t.Cleanup(func() {
		proxyPoolGeneration.Lock()
		proxyPoolGeneration.current = previous
		proxyTransportCache.Purge()
		proxyPoolGeneration.Unlock()
	})

	// Publish a config so the process-wide generation moves past zero.
	AdvanceProxyPoolGeneration(&config.Config{})

	plain := &config.Config{}
	if plain.ProxyPoolGeneration != 0 {
		t.Fatalf("expected an unfenced snapshot, generation = %d", plain.ProxyPoolGeneration)
	}
	auth := poolOptInAuth("unfenced")
	pooled := pooledProxyTransport("direct", plain, auth)
	if pooled == nil {
		t.Fatal("unfenced snapshot must still pool")
	}
	if pooledProxyTransport("direct", plain, auth) != pooled {
		t.Fatal("unfenced snapshot did not reuse its pool")
	}

	// A snapshot carrying a retired generation is still fenced out.
	retired := &config.Config{}
	proxyPoolGeneration.RLock()
	retired.ProxyPoolGeneration = proxyPoolGeneration.current + 1
	proxyPoolGeneration.RUnlock()
	before := proxyTransportCache.Len()
	first := pooledProxyTransport("direct", retired, auth)
	second := pooledProxyTransport("direct", retired, auth)
	if first == nil || second == nil {
		t.Fatal("fenced snapshot must still get a usable transport")
	}
	if first == second {
		t.Fatal("fenced snapshot must not be cached")
	}
	if got := proxyTransportCache.Len(); got != before {
		t.Fatalf("fenced snapshot repopulated the cache: %d -> %d", before, got)
	}
}
