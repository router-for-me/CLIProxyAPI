package helps

import (
	"net/http"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

func TestSharedProxyTransportCachesNormalizedProxy(t *testing.T) {
	resetSharedProxyTransportsForTest()
	t.Cleanup(resetSharedProxyTransportsForTest)

	first, mode, errFirst := SharedProxyTransport("auth-a", "  http://127.0.0.1:3128  ")
	if errFirst != nil {
		t.Fatalf("SharedProxyTransport() error = %v", errFirst)
	}
	second, _, errSecond := SharedProxyTransport("auth-a", "http://127.0.0.1:3128")
	if errSecond != nil {
		t.Fatalf("SharedProxyTransport() second error = %v", errSecond)
	}
	if mode != proxyutil.ModeProxy {
		t.Fatalf("mode = %v, want %v", mode, proxyutil.ModeProxy)
	}
	if first == nil || first != second {
		t.Fatalf("expected the same cached transport, got %p and %p", first, second)
	}

	other, _, errOther := SharedProxyTransport("auth-a", "http://127.0.0.1:3129")
	if errOther != nil {
		t.Fatalf("SharedProxyTransport() other proxy error = %v", errOther)
	}
	if other == first {
		t.Fatal("distinct proxies must not share a transport")
	}

	otherAuth, _, errOtherAuth := SharedProxyTransport("auth-b", "http://127.0.0.1:3128")
	if errOtherAuth != nil {
		t.Fatalf("SharedProxyTransport() other auth error = %v", errOtherAuth)
	}
	if otherAuth == first {
		t.Fatal("distinct credential scopes must not share a transport")
	}

	defaultTransport := http.DefaultTransport.(*http.Transport)
	if first.MaxIdleConns != defaultTransport.MaxIdleConns ||
		first.MaxIdleConnsPerHost != defaultTransport.MaxIdleConnsPerHost ||
		first.IdleConnTimeout != defaultTransport.IdleConnTimeout {
		t.Fatal("shared transport must preserve the standard Go connection pool settings")
	}
}

func TestSharedProxyTransportConcurrentCallersShareOneInstance(t *testing.T) {
	resetSharedProxyTransportsForTest()
	t.Cleanup(resetSharedProxyTransportsForTest)

	const callers = 32
	results := make([]*http.Transport, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func(index int) {
			defer wg.Done()
			results[index], _, _ = SharedProxyTransport("auth-concurrent", "socks5://127.0.0.1:1080")
		}(i)
	}
	wg.Wait()

	for i := 1; i < callers; i++ {
		if results[i] != results[0] {
			t.Fatalf("caller %d observed a different transport (%p vs %p)", i, results[i], results[0])
		}
	}
}
