package cmd

import (
	"bytes"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/discovery"
)

func TestReviewScopedIPv6AndWebSocketURLs(t *testing.T) {
	for _, secure := range []bool{false, true} {
		name := "ws"
		if secure {
			name = "wss"
		}
		t.Run(name, func(t *testing.T) {
			gw := discovery.DiscoveredService{Port: 8317, TLS: secure, IPv6: []net.IP{net.ParseIP("fe80::1")}, IPv6Zones: map[string]string{"fe80::1": "en0"}, Endpoints: map[string]string{"responses-ws": "/v1/responses"}}
			primary, _ := preferredDisplayAddresses(gw)
			if primary != "fe80::1%en0" {
				t.Fatalf("scoped primary=%q", primary)
			}
			var stdout, stderr bytes.Buffer
			code := runDiscover(time.Second, false, &stdout, &stderr, func() (discovery.Browser, error) { return &fakeBrowser{result: []discovery.DiscoveredService{gw}}, nil })
			if code != 0 || !strings.Contains(stdout.String(), name+"://[fe80::1%25en0]:8317/v1/responses") {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
			}
		})
	}
}

func TestReviewDisplayRejectsMulticast(t *testing.T) {
	primary, _ := preferredDisplayAddresses(discovery.DiscoveredService{IPv4: []net.IP{net.ParseIP("224.0.0.251")}, IPv6: []net.IP{net.ParseIP("ff02::fb")}})
	if primary != "" {
		t.Fatalf("unsafe primary=%q", primary)
	}
}
