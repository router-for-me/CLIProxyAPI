package discovery

import (
	"net"
	"testing"
)

func TestMDNSQueryAddr6UsesInterfaceScope(t *testing.T) {
	before := mdnsGroupAddrIPv6.Zone
	for _, name := range []string{"en0", "Ethernet", "eth0"} {
		addr := mdnsQueryAddr6(&net.Interface{Name: name})
		if addr.Zone != name || addr.Port != 5353 || !addr.IP.Equal(net.ParseIP("ff02::fb")) {
			t.Fatalf("wrong scoped multicast destination: %v", addr)
		}
	}
	if mdnsGroupAddrIPv6.Zone != before {
		t.Fatal("shared destination was mutated")
	}
}
