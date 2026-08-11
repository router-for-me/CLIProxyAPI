package helps

import (
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

// sharedProxyTransports memoizes one transport per credential scope and normalized
// proxy setting. Antigravity uses auth.ID as the scope so different OAuth identities
// never share a TCP/TLS pool, matching the native client's one-credential process model.
var sharedProxyTransports sync.Map // sharedProxyTransportKey -> *sharedProxyTransportEntry

type sharedProxyTransportKey struct {
	scope string
	proxy string
}

type sharedProxyTransportEntry struct {
	once      sync.Once
	transport *http.Transport
	mode      proxyutil.Mode
	err       error
}

// SharedProxyTransport returns a stable transport for one credential scope and proxy
// setting. It preserves Go's standard connection-pool settings; callers must treat
// the result as read-only and clone it before changing protocol behavior.
func SharedProxyTransport(scope, raw string) (*http.Transport, proxyutil.Mode, error) {
	key := sharedProxyTransportKey{
		scope: strings.TrimSpace(scope),
		proxy: strings.TrimSpace(raw),
	}
	value, _ := sharedProxyTransports.LoadOrStore(key, &sharedProxyTransportEntry{})
	entry := value.(*sharedProxyTransportEntry)
	entry.once.Do(func() {
		entry.transport, entry.mode, entry.err = proxyutil.BuildHTTPTransport(key.proxy)
	})
	return entry.transport, entry.mode, entry.err
}

func resetSharedProxyTransportsForTest() {
	sharedProxyTransports.Range(func(key, _ any) bool {
		sharedProxyTransports.Delete(key)
		return true
	})
}
