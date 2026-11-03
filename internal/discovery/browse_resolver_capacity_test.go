package discovery

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestBrowseFlushAtCapacityPreservesReplacement(t *testing.T) {
	for _, capacity := range []string{"addresses", "bytes", "records"} {
		t.Run(capacity, func(t *testing.T) {
			c := testBrowseCache("")
			browseTestIngest(t, c, 7, browseTestNow, browseTestRecords()[:3]...)
			for i := 1; i <= 64; i++ {
				browseTestIngest(t, c, 7, browseTestNow, fmt.Sprintf("demo.local. 120 IN A 192.0.2.%d", i))
			}
			if capacity == "bytes" {
				c.bytes = browseCacheByteLimit
			}
			if capacity == "records" {
				for len(c.records) < browseCacheRRLimit {
					name := fmt.Sprintf("filler%d.local.", len(c.records))
					rr := browseTestRR(t, name+" 120 IN A 198.51.100.1")
					c.records[browseRecordKey(7, rr)] = browseRR{rr: rr, expires: browseTestNow.Add(120 * time.Second), received: browseTestNow}
				}
			}
			replacement := browseTestRR(t, "demo.local. 120 IN A 192.0.2.200")
			replacement.Header().Class |= 0x8000
			c.ingest(&dns.Msg{MsgHdr: dns.MsgHdr{Response: true}, Answer: []dns.RR{replacement}}, 7, &net.UDPAddr{IP: net.ParseIP("192.0.2.9"), Port: 5353}, net.ParseIP("224.0.0.251"), browseTestNow.Add(2*time.Second))
			got := c.results(browseTestNow.Add(3 * time.Second))
			if len(got) != 1 || len(got[0].IPv4) != 1 || !got[0].IPv4[0].Equal(net.ParseIP("192.0.2.200")) {
				t.Fatalf("replacement lost at %s cap: %#v", capacity, got)
			}
		})
	}
}

func TestBrowseOriginChangeDoesNotMixOldAddresses(t *testing.T) {
	c := testBrowseCache("")
	browseTestIngest(t, c, 7, browseTestNow, browseTestRecords()...)
	browseTestIngest(t, c, 7, browseTestNow.Add(2*time.Second),
		"Demo._ai-gateway._tcp.local. 120 IN SRV 0 0 443 replacement.local.",
		`Demo._ai-gateway._tcp.local. 120 IN TXT "txtvers=1" "path=/ai" "tls=1" "auth=1"`,
		"replacement.local. 120 IN A 192.0.2.200")
	got := c.results(browseTestNow.Add(2 * time.Second))
	if len(got) != 1 || len(got[0].IPv4) != 1 || !got[0].IPv4[0].Equal(net.ParseIP("192.0.2.200")) || len(got[0].IPv6) != 0 || got[0].Port != 443 || !got[0].TLS || got[0].Path != "/ai" {
		t.Fatalf("origin update mixed old addresses: %#v", got)
	}
}

func TestBrowseTargetMayUseDifferentDNSDomain(t *testing.T) {
	c := testBrowseCache("")
	records := browseTestRecords()
	browseTestIngest(t, c, 7, browseTestNow, records[0], records[2],
		"Demo._ai-gateway._tcp.local. 120 IN SRV 0 0 443 gateway.example.com.",
		"gateway.example.com. 120 IN A 192.0.2.200")
	if got := c.results(browseTestNow); len(got) != 1 || got[0].Host != "gateway.example.com." {
		t.Fatalf("valid off-domain target rejected: %#v", got)
	}
	for _, host := range []string{"http://evil.local.", "user@host.local.", "a..local.", "-host.local."} {
		if validBrowseHost(host) {
			t.Fatalf("accepted non-host SRV target %q", host)
		}
	}
}
