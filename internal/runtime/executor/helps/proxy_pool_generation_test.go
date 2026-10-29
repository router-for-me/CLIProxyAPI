package helps

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// This test changes the process-wide generation and must not run in parallel.
func TestProxyPoolGenerationRejectsOldSnapshotAfterReload(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)

	publisher := RegisterProxyPoolPublisher()
	old := &config.Config{}
	AdvanceProxyPoolGeneration(publisher, old)
	oldClone := old.CloneForRuntime()
	if oldClone.ProxyPoolGeneration != old.ProxyPoolGeneration || oldClone.ProxyPoolPublisher != publisher {
		t.Fatal("runtime clone lost the publisher fence")
	}
	first := pooledProxyTransport("direct", oldClone, poolOptInAuth("generation"))
	if first == nil || proxyTransportCache.Len() != 1 {
		t.Fatal("initial pool missing")
	}
	next := old.CloneForRuntime()
	AdvanceProxyPoolGeneration(publisher, next)
	if proxyTransportCache.Len() != 0 {
		t.Fatal("reload did not retire the publisher's pools")
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

// TestProxyPoolFenceIsPerPublisher pins the isolation requirement: a reload or a new
// runtime in one publisher must never retire the snapshots another publisher is still
// serving. A shared counter used to fence every runtime in the process, so building a
// second SDK service silently turned the first service's pooling into a fresh
// transport per request.
func TestProxyPoolFenceIsPerPublisher(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)

	firstPublisher := RegisterProxyPoolPublisher()
	secondPublisher := RegisterProxyPoolPublisher()
	if firstPublisher == 0 || firstPublisher == secondPublisher {
		t.Fatalf("publishers not unique: %d, %d", firstPublisher, secondPublisher)
	}
	auth := poolOptInAuth("shared-scope")
	firstCfg := &config.Config{}
	AdvanceProxyPoolGeneration(firstPublisher, firstCfg)
	firstTransport := pooledProxyTransport("direct", firstCfg, auth)
	if firstTransport == nil {
		t.Fatal("first publisher did not pool")
	}

	// Publishing in another runtime must not retire or purge the first runtime's pool.
	secondCfg := &config.Config{}
	AdvanceProxyPoolGeneration(secondPublisher, secondCfg)
	if got := pooledProxyTransport("direct", firstCfg, auth); got != firstTransport {
		t.Fatal("another publisher's publication retired a live snapshot")
	}
	if got := proxyTransportCache.Len(); got != 1 {
		t.Fatalf("cache holds %d pools, want the first publisher's pool only", got)
	}

	// Publishers keep separate pools even for the same credential and proxy.
	secondTransport := pooledProxyTransport("direct", secondCfg, auth)
	if secondTransport == nil || secondTransport == firstTransport {
		t.Fatal("publishers share one pool")
	}

	// A reload inside the first publisher still retires the first publisher's pool.
	nextFirst := firstCfg.CloneForRuntime()
	AdvanceProxyPoolGeneration(firstPublisher, nextFirst)
	if pooledProxyTransport("direct", firstCfg, auth) == firstTransport {
		t.Fatal("reload of the same publisher did not retire its stale snapshot")
	}
	if got := pooledProxyTransport("direct", nextFirst, auth); got == firstTransport {
		t.Fatal("reload of the same publisher reused the retired transport")
	}
	// ... while the second publisher's pool survives it.
	if got := pooledProxyTransport("direct", secondCfg, auth); got != secondTransport {
		t.Fatal("reload of one publisher retired another publisher's pool")
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

	// Publish a config so the process-wide generation moves past zero.
	AdvanceProxyPoolGeneration(RegisterProxyPoolPublisher(), &config.Config{})

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
	retired := &config.Config{ProxyPoolPublisher: plain.ProxyPoolPublisher}
	proxyPoolFence.RLock()
	retired.ProxyPoolGeneration = proxyPoolFence.current[retired.ProxyPoolPublisher] + 1
	proxyPoolFence.RUnlock()
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
