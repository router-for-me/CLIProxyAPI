package discovery

import (
	"net"
	"strings"
	"testing"

	"github.com/libp2p/zeroconf/v2"
)

func TestReviewAddressesRejectMulticastAndInvalid(t *testing.T) {
	ips := []net.IP{nil, {1, 2}, net.ParseIP("224.0.0.251"), net.ParseIP("ff02::fb"), net.IPv4bcast, net.ParseIP("127.0.0.1"), net.IPv6unspecified, net.ParseIP("192.168.1.1"), net.ParseIP("fd00::1"), net.ParseIP("fe80::1")}
	got := filterUsableIPs(ips)
	if len(got) != 3 {
		t.Fatalf("usable addresses=%v", got)
	}
}

func TestReviewServiceNamesRFC6335(t *testing.T) {
	for _, name := range []string{"_123._tcp", "_a--b._tcp"} {
		t.Run(name, func(t *testing.T) {
			if validateServiceType(name) == nil {
				t.Fatalf("accepted %s", name)
			}
		})
	}
	if err := validateServiceType("_a1-b._tcp"); err != nil {
		t.Fatal(err)
	}
}

func TestReviewEntryCountsRawDraftWireBytes(t *testing.T) {
	core := []string{"txtvers=1", "path=/", "tls=0", "auth=1"}
	entry := &zeroconf.ServiceEntry{Text: append(core, "unknown="+strings.Repeat("a", 240), "other="+strings.Repeat("b", 130))}
	if _, ok := entryToDiscovered(entry); ok {
		t.Fatal("accepted draft over 400 bytes")
	}
	entry.Text = core
	if _, ok := entryToDiscovered(entry); !ok {
		t.Fatal("rejected core draft")
	}
}
