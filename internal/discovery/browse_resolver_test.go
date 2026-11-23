package discovery

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

var browseTestNow = time.Unix(1700000000, 0)

func testBrowseCache(service string) *browseCache {
	c := newBrowseCache([]net.Interface{{Index: 7, Name: "lan7"}, {Index: 8, Name: "lan8"}}, service, "local.")
	for i, l := range c.links {
		l.local = []net.IP{net.ParseIP("192.0.2.1")}
		_, n, _ := net.ParseCIDR("192.0.2.0/24")
		l.networks = []*net.IPNet{n}
		c.links[i] = l
	}
	return c
}
func browseTestRR(t *testing.T, s string) dns.RR {
	t.Helper()
	r, e := dns.NewRR(s)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func browseTestIngest(t *testing.T, c *browseCache, index int, now time.Time, records ...string) {
	t.Helper()
	m := &dns.Msg{MsgHdr: dns.MsgHdr{Response: true}}
	for _, s := range records {
		m.Answer = append(m.Answer, browseTestRR(t, s))
	}
	// Exercise the real DNS wire decoder as well as the cache.
	wire, e := m.Pack()
	if e != nil {
		t.Fatal(e)
	}
	decoded := new(dns.Msg)
	if e = decoded.Unpack(wire); e != nil {
		t.Fatal(e)
	}
	c.ingest(decoded, index, &net.UDPAddr{IP: net.ParseIP("192.0.2.9"), Port: 5353}, net.ParseIP("224.0.0.251"), now)
}
func browseTestRecords() []string {
	return []string{
		"_ai-gateway._tcp.local. 120 IN PTR Demo._ai-gateway._tcp.local.",
		"Demo._ai-gateway._tcp.local. 120 IN SRV 0 0 8317 demo.local.",
		`Demo._ai-gateway._tcp.local. 120 IN TXT "txtvers=1" "path=/" "tls=0" "auth=1"`,
		"demo.local. 120 IN A 192.0.2.10",
		"demo.local. 120 IN AAAA fe80::10",
	}
}
func TestNativeBrowseSplitPermutations(t *testing.T) {
	records := browseTestRecords()
	var permute func([]int)
	permute = func(order []int) {
		if len(order) == len(records) {
			c := testBrowseCache("")
			for _, i := range order {
				browseTestIngest(t, c, 7, browseTestNow, records[i])
			}
			got := c.results(browseTestNow)
			if len(got) != 1 || got[0].Port != 8317 || got[0].InterfaceIndex != 7 || got[0].IPv6Zones["fe80::10"] != "lan7" {
				t.Fatalf("order %v: %+v", order, got)
			}
			return
		}
		for i := range records {
			used := false
			for _, v := range order {
				if v == i {
					used = true
				}
			}
			if !used {
				permute(append(order, i))
			}
		}
	}
	permute(nil)
}
func TestNativeBrowseInterfaceIsolation(t *testing.T) {
	c := testBrowseCache("")
	r := browseTestRecords()
	browseTestIngest(t, c, 7, browseTestNow, r[:3]...)
	browseTestIngest(t, c, 8, browseTestNow, r[3:]...)
	if got := c.results(browseTestNow); len(got) != 0 {
		t.Fatal("mixed interfaces", got)
	}
	browseTestIngest(t, c, 7, browseTestNow, r[3:]...)
	browseTestIngest(t, c, 8, browseTestNow, r[:3]...)
	if got := c.results(browseTestNow); len(got) != 2 || got[1].IPv6Zones["fe80::10"] != "lan8" {
		t.Fatal(got)
	}
}
func TestNativeBrowseTTLGoodbyeAndFlush(t *testing.T) {
	c := testBrowseCache("")
	r := browseTestRecords()
	browseTestIngest(t, c, 7, browseTestNow, r...)
	if len(c.results(browseTestNow.Add(120*time.Second))) != 0 {
		t.Fatal("expired origin retained")
	}
	browseTestIngest(t, c, 7, browseTestNow, r...)
	bye := strings.Replace(r[0], "120 IN", "0 IN", 1)
	browseTestIngest(t, c, 7, browseTestNow.Add(2*time.Second), bye)
	if len(c.results(browseTestNow.Add(2500*time.Millisecond))) != 1 {
		t.Fatal("goodbye lacks grace")
	}
	if len(c.results(browseTestNow.Add(3*time.Second))) != 0 {
		t.Fatal("goodbye not expired")
	}
	c = testBrowseCache("")
	browseTestIngest(t, c, 7, browseTestNow, r...)
	m := &dns.Msg{MsgHdr: dns.MsgHdr{Response: true}}
	rr := browseTestRR(t, "demo.local. 120 IN A 192.0.2.11")
	rr.Header().Class |= 0x8000
	m.Answer = []dns.RR{rr}
	c.ingest(m, 7, &net.UDPAddr{IP: net.ParseIP("192.0.2.9"), Port: 5353}, net.ParseIP("224.0.0.251"), browseTestNow.Add(2*time.Second))
	got := c.results(browseTestNow.Add(3 * time.Second))
	if len(got) != 1 || len(got[0].IPv4) != 1 || !got[0].IPv4[0].Equal(net.ParseIP("192.0.2.11")) {
		t.Fatal(got)
	}
	// A split RRset arriving less than one second later retains both members.
	rr2 := browseTestRR(t, "demo.local. 120 IN A 192.0.2.12")
	rr2.Header().Class |= 0x8000
	m.Answer = []dns.RR{rr2}
	c.ingest(m, 7, &net.UDPAddr{IP: net.ParseIP("192.0.2.9"), Port: 5353}, net.ParseIP("224.0.0.251"), browseTestNow.Add(2500*time.Millisecond))
	if len(c.rrset(7, "demo.local.", dns.TypeA)) != 2 {
		t.Fatal("split flush lost recent RR")
	}
}
func TestNativeBrowsePacketAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name        string
		index, port int
		src, dst    string
		response    bool
		want        bool
	}{
		{"multicast", 7, 5353, "198.51.100.9", "224.0.0.251", true, true},
		{"unknown interface", 99, 5353, "192.0.2.9", "224.0.0.251", true, false},
		{"missing interface", 0, 5353, "192.0.2.9", "224.0.0.251", true, false},
		{"wrong port", 7, 9999, "192.0.2.9", "224.0.0.251", true, false},
		{"known answer query", 7, 5353, "192.0.2.9", "224.0.0.251", false, false},
		{"wrong multicast group", 7, 5353, "192.0.2.9", "239.1.2.3", true, false},
		{"onlink unicast", 7, 5353, "192.0.2.9", "192.0.2.1", true, true},
		{"offlink unicast", 7, 5353, "198.51.100.9", "192.0.2.1", true, false},
		{"foreign unicast destination", 7, 5353, "192.0.2.9", "192.0.2.2", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testBrowseCache("")
			m := &dns.Msg{MsgHdr: dns.MsgHdr{Response: tc.response}, Answer: []dns.RR{browseTestRR(t, browseTestRecords()[0])}}
			c.ingest(m, tc.index, &net.UDPAddr{IP: net.ParseIP(tc.src), Port: tc.port}, net.ParseIP(tc.dst), browseTestNow)
			if (len(c.records) > 0) != tc.want {
				t.Fatal("acceptance mismatch")
			}
		})
	}
}
func TestNativeBrowseSubtypeAndQueries(t *testing.T) {
	c := testBrowseCache("_responses._sub._ai-gateway._tcp")
	r := browseTestRecords()
	r[0] = strings.Replace(r[0], "_ai-gateway._tcp.local.", "_responses._sub._ai-gateway._tcp.local.", 1)
	browseTestIngest(t, c, 7, browseTestNow, r[0])
	qs := c.queries(7, browseTestNow)
	if len(qs) != 3 {
		t.Fatal(qs)
	}
	browseTestIngest(t, c, 7, browseTestNow, r[1])
	qs = c.queries(7, browseTestNow)
	if len(qs) != 4 {
		t.Fatal(qs)
	}
	browseTestIngest(t, c, 7, browseTestNow, r[2:]...)
	got := c.results(browseTestNow)
	if len(got) != 1 || got[0].ServiceType != "_ai-gateway._tcp" {
		t.Fatal(got)
	}
	if len(c.queries(7, browseTestNow)) != 1 {
		t.Fatal("resolved records requeried")
	}
	if len(c.queries(7, browseTestNow.Add(100*time.Second))) != 5 {
		t.Fatal("TTL refresh missing")
	}
}
func TestNativeBrowseBounds(t *testing.T) {
	c := testBrowseCache("")
	for i := 0; i < 300; i++ {
		browseTestIngest(t, c, 7, browseTestNow, fmt.Sprintf("_ai-gateway._tcp.local. 120 IN PTR i%d._ai-gateway._tcp.local.", i))
	}
	if len(c.records) != 256 {
		t.Fatal(len(c.records))
	}
	c = testBrowseCache("")
	for i := 1; i <= 100; i++ {
		browseTestIngest(t, c, 7, browseTestNow, fmt.Sprintf("demo.local. 120 IN A 192.0.2.%d", i))
	}
	if len(c.records) != 64 {
		t.Fatal(len(c.records))
	}
	draft := []string{"txtvers=1", "path=/", "tls=0", "auth=1", "x=" + strings.Repeat("a", 250), "y=" + strings.Repeat("b", 150)}
	if browseTXTValid(draft) {
		t.Fatal("oversized draft accepted")
	}
	if !browseTXTValid(draft[1:]) {
		t.Fatal("bounded legacy rejected")
	}
	if browseTXTValid(make([]string, 65)) {
		t.Fatal("too many TXT strings")
	}
	c = testBrowseCache("")
	c.bytes = browseCacheByteLimit
	browseTestIngest(t, c, 7, browseTestNow, browseTestRecords()[0])
	if len(c.records) != 0 {
		t.Fatal("byte cap ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := browseLAN(ctx, nil, "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestNativeBrowseUnavailableAndRapidUpdates(t *testing.T) {
	c := testBrowseCache("")
	browseTestIngest(t, c, 7, browseTestNow, browseTestRecords()...)
	for _, port := range []int{8001, 8002, 8003} {
		browseTestIngest(t, c, 7, browseTestNow, fmt.Sprintf("Demo._ai-gateway._tcp.local. 120 IN SRV 0 0 %d demo.local.", port))
	}
	if got := c.results(browseTestNow); len(got) != 1 || got[0].Port != 8003 {
		t.Fatal("rapid update dropped", got)
	}
	browseTestIngest(t, c, 7, browseTestNow, "Demo._ai-gateway._tcp.local. 120 IN SRV 0 0 0 .")
	if got := c.results(browseTestNow); len(got) != 0 {
		t.Fatal("unavailable SRV retained old origin", got)
	}
}

func TestNativeBrowseFlushBatchAndGoodbyeRescue(t *testing.T) {
	c := testBrowseCache("")
	browseTestIngest(t, c, 7, browseTestNow, browseTestRecords()...)
	m := &dns.Msg{MsgHdr: dns.MsgHdr{Response: true}}
	for _, ip := range []string{"192.0.2.11", "192.0.2.12"} {
		r := browseTestRR(t, "demo.local. 120 IN A "+ip)
		r.Header().Class |= 0x8000
		m.Extra = append(m.Extra, r)
	}
	c.ingest(m, 7, &net.UDPAddr{IP: net.ParseIP("192.0.2.9"), Port: 5353}, net.ParseIP("224.0.0.251"), browseTestNow.Add(2*time.Second))
	got := c.results(browseTestNow.Add(3 * time.Second))
	if len(got) != 1 || len(got[0].IPv4) != 2 {
		t.Fatal("batch flush lost members", got)
	}
	ptr := browseTestRecords()[0]
	browseTestIngest(t, c, 7, browseTestNow.Add(4*time.Second), strings.Replace(ptr, "120 IN", "0 IN", 1))
	browseTestIngest(t, c, 7, browseTestNow.Add(4500*time.Millisecond), ptr)
	if len(c.results(browseTestNow.Add(6*time.Second))) != 1 {
		t.Fatal("rescue announcement ignored")
	}
}
