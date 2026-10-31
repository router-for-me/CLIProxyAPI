package helps

import (
	"sync"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// These tests use the process-wide pool and must not run in parallel.
func TestProxyPoolSettingsAndConcurrentBuild(t *testing.T) {
	ResetProxyTransports()
	t.Cleanup(ResetProxyTransports)
	// Writing the block without any field is the opt-in and must fall back to defaults.
	auth := poolAuth("defaults", poolBlock())
	cfg := testConfig()
	tr := pooledProxyTransport("direct", cfg, auth)
	if tr == nil || tr.IdleConnTimeout != proxyPoolDefaultIdleConnTimeout || tr.MaxIdleConnsPerHost != proxyPoolDefaultMaxIdleConnsPerHost {
		t.Fatalf("unexpected defaults: %+v", tr)
	}
	if pooledProxyTransport("none", cfg, auth) != tr {
		t.Fatal("direct and none should share transport")
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if pooledProxyTransport("direct", cfg, auth) != tr {
				t.Error("transport not shared")
			}
		}()
	}
	wg.Wait()
	for _, raw := range []string{"0s", "-1s"} {
		zero := poolAuth("zero", &cliproxyauth.ProxyConnectionPoolOverride{IdleConnTimeout: raw})
		if got := pooledProxyTransport("direct", cfg, zero); got == nil || !got.DisableKeepAlives {
			t.Fatalf("%s must disable keep-alives", raw)
		}
	}
	for _, max := range []int{0, -1} {
		val := max
		capped := poolAuth("max", &cliproxyauth.ProxyConnectionPoolOverride{MaxIdleConnsPerHost: &val})
		if got := pooledProxyTransport("direct", cfg, capped); got == nil || !got.DisableKeepAlives {
			t.Fatalf("max %d must disable keep-alives", max)
		}
	}
	invalid := poolAuth("invalid", &cliproxyauth.ProxyConnectionPoolOverride{IdleConnTimeout: "invalid"})
	bad := pooledProxyTransport("direct", cfg, invalid)
	if bad == nil || bad.IdleConnTimeout != proxyPoolDefaultIdleConnTimeout || pooledProxyTransport("direct", cfg, invalid) != bad {
		t.Fatal("invalid duration must use cached default")
	}
	if pooledProxyTransport("direct", nil, nil) == pooledProxyTransport("direct", nil, nil) {
		t.Fatal("a credential without the block must retain unpooled behavior")
	}
}
