package discovery

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/zeroconf/v2"
	"github.com/miekg/dns"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

const (
	browseCacheRRLimit   = maxDiscoveredServices * (maxDiscoveredAddresses + 4)
	browseCacheByteLimit = 4 << 20
	browseIPLimit        = maxDiscoveredAddresses
)

type browseLink struct {
	iface    net.Interface
	networks []*net.IPNet
	local    []net.IP
}
type browseRR struct {
	rr                dns.RR
	expires, received time.Time
	size              int
	serial            uint64
}
type browseRRKey struct {
	link int
	name string
	kind uint16
	data string
}
type browseCache struct {
	browse, base, service, domain string
	links                         map[int]browseLink
	records                       map[browseRRKey]browseRR
	bytes                         int
	serial                        uint64
}

func browseName(s string) string { return strings.ToLower(dns.Fqdn(s)) }

func newBrowseCache(ifaces []net.Interface, service, domain string) *browseCache {
	if service == "" {
		service = DefaultServiceType
	}
	if domain == "" {
		domain = DefaultDomain
	}
	domain = dns.Fqdn(domain)
	service = strings.TrimSuffix(service, ".")
	// Accept both a service type and its fully qualified browse name.
	service = strings.TrimSuffix(service, "."+strings.TrimSuffix(domain, "."))
	base := service
	if i := strings.Index(strings.ToLower(base), "._sub."); i >= 0 {
		base = base[i+6:]
	}
	c := &browseCache{browse: browseName(service + "." + domain), base: browseName(base + "." + domain), service: base, domain: domain, links: make(map[int]browseLink), records: make(map[browseRRKey]browseRR)}
	for _, iface := range ifaces {
		l := browseLink{iface: iface}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				l.networks = append(l.networks, n)
				l.local = append(l.local, n.IP)
			}
		}
		if iface.Index > 0 {
			c.links[iface.Index] = l
		}
	}
	return c
}

// RFC 6762 section 11 permits off-subnet sources for multicast responses.
// Unicast responses must originate on-link and be addressed to this interface.
func (c *browseCache) validPeer(index int, src *net.UDPAddr, dst net.IP) bool {
	l, ok := c.links[index]
	if !ok || src == nil || src.Port != mdnsPort || src.IP == nil || src.IP.IsUnspecified() || src.IP.IsMulticast() || src.IP.IsLoopback() || dst == nil {
		return false
	}
	if (src.IP.To4() != nil) != (dst.To4() != nil) {
		return false
	}
	if dst.IsMulticast() {
		return dst.Equal(mdnsGroupAddrIPv4.IP) || dst.Equal(mdnsGroupAddrIPv6.IP)
	}
	local := false
	for _, ip := range l.local {
		if ip.Equal(dst) {
			local = true
			break
		}
	}
	if !local {
		return false
	}
	if src.IP.IsLinkLocalUnicast() {
		return true
	}
	for _, n := range l.networks {
		if n.Contains(src.IP) {
			return true
		}
	}
	return false
}
func (c *browseCache) prune(now time.Time) {
	for k, r := range c.records {
		if !now.Before(r.expires) {
			delete(c.records, k)
			c.bytes -= r.size
		}
	}
}
func (c *browseCache) instance(name string) bool {
	labels := dns.SplitDomainName(name)
	base := dns.SplitDomainName(c.base)
	return len(labels) == len(base)+1 && strings.EqualFold(strings.Join(labels[1:], "."), strings.Join(base, "."))
}
func browseTXTValid(txt []string) bool {
	if len(txt) > maxBrowseTXTRecords {
		return false
	}
	size := 0
	for _, s := range txt {
		if len(s) > 255 {
			return false
		}
		size += 1 + len(s)
	}
	limit := 16 * 1024
	if _, ok := ParseTXTRecords(txt)["txtvers"]; ok {
		limit = 400
	}
	return size <= limit
}
func (c *browseCache) admissible(rr dns.RR) bool {
	h := rr.Header()
	name := browseName(h.Name)
	if h.Class&0x7fff != dns.ClassINET {
		return false
	}
	switch r := rr.(type) {
	case *dns.PTR:
		return name == c.browse && c.instance(r.Ptr)
	case *dns.SRV:
		return c.instance(name) && (r.Target == "." || validBrowseHost(r.Target))
	case *dns.TXT:
		return c.instance(name) && browseTXTValid(r.Txt)
	case *dns.A:
		return validBrowseHost(name) && r.A.To4() != nil
	case *dns.AAAA:
		return validBrowseHost(name) && r.AAAA.To16() != nil && r.AAAA.To4() == nil
	}
	return false
}

// SRV targets are hostnames, not URLs. They need not share the service's
// browsing domain; the PTR owner still confines service membership.
func validBrowseHost(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-') {
				return false
			}
		}
	}
	return true
}

func browseRecordKey(index int, rr dns.RR) browseRRKey {
	copyRR := dns.Copy(rr)
	h := copyRR.Header()
	h.Name = browseName(h.Name)
	h.Ttl = 0
	h.Class = dns.ClassINET
	switch r := copyRR.(type) {
	case *dns.PTR:
		r.Ptr = browseName(r.Ptr)
	case *dns.SRV:
		r.Target = browseName(r.Target)
	}
	return browseRRKey{index, h.Name, h.Rrtype, copyRR.String()}
}

// Only answers and additional records in responses enter the cache. Query known
// answers and probe authorities must never manufacture a discovered service.
func (c *browseCache) ingest(msg *dns.Msg, index int, src *net.UDPAddr, dst net.IP, now time.Time) {
	c.prune(now)
	if msg == nil || !msg.Response || msg.Opcode != dns.OpcodeQuery || msg.Rcode != dns.RcodeSuccess || msg.Id != 0 || !c.validPeer(index, src, dst) {
		return
	}
	records := append(append([]dns.RR(nil), msg.Answer...), msg.Extra...)
	incoming := make(map[browseRRKey]bool, len(records))
	for _, rr := range records {
		if c.admissible(rr) && rr.Header().Ttl > 0 {
			incoming[browseRecordKey(index, rr)] = true
		}
	}
	flushVictims := make(map[browseRRKey]bool)
	evictFlushed := func(matches func(browseRRKey) bool) bool {
		var oldest browseRRKey
		sequence := ^uint64(0)
		for key := range flushVictims {
			if record, ok := c.records[key]; ok && matches(key) && record.serial < sequence {
				oldest, sequence = key, record.serial
			}
		}
		if sequence == ^uint64(0) {
			return false
		}
		c.bytes -= c.records[oldest].size
		delete(c.records, oldest)
		delete(flushVictims, oldest)
		return true
	}
	// Apply flush before inserting anything, preserving all members of this packet
	// and the one-second immunity for RRsets split across consecutive packets.
	for _, rr := range records {
		if !c.admissible(rr) || rr.Header().Ttl == 0 || rr.Header().Class&0x8000 == 0 || rr.Header().Rrtype == dns.TypePTR {
			continue
		}
		k := browseRecordKey(index, rr)
		for old, r := range c.records {
			if old.link == index && old.name == k.name && old.kind == k.kind && now.Sub(r.received) >= time.Second && !incoming[old] {
				flushVictims[old] = true
				if r.expires.After(now.Add(time.Second)) {
					r.expires = now.Add(time.Second)
					c.records[old] = r
				}
			}
		}
	}
	for _, rr := range records {
		if !c.admissible(rr) {
			continue
		}
		k := browseRecordKey(index, rr)
		h := rr.Header()
		if h.Ttl == 0 {
			if r, ok := c.records[k]; ok && r.expires.After(now.Add(time.Second)) {
				r.expires = now.Add(time.Second)
				c.records[k] = r
			}
			continue
		}
		size := len(k.data) + len(k.name) + 64
		old, exists := c.records[k]
		if !exists {
			for len(c.records) >= browseCacheRRLimit || c.bytes+size > browseCacheByteLimit {
				if !evictFlushed(func(browseRRKey) bool { return true }) {
					break
				}
			}
			if len(c.records) >= browseCacheRRLimit || c.bytes+size > browseCacheByteLimit {
				continue
			}
			count := 0
			instances := make(map[string]struct{})
			for key, r := range c.records {
				if key.link == index && key.name == k.name && (key.kind == k.kind || ((k.kind == dns.TypeA || k.kind == dns.TypeAAAA) && (key.kind == dns.TypeA || key.kind == dns.TypeAAAA))) {
					count++
				}
				if c.instance(key.name) {
					instances[fmt.Sprintf("%d/%s", key.link, key.name)] = struct{}{}
				}
				if p, ok := r.rr.(*dns.PTR); ok {
					instances[fmt.Sprintf("%d/%s", key.link, browseName(p.Ptr))] = struct{}{}
				}
			}
			instance := ""
			if c.instance(k.name) {
				instance = k.name
			}
			if p, ok := rr.(*dns.PTR); ok {
				instance = browseName(p.Ptr)
			}
			if instance != "" {
				if _, ok := instances[fmt.Sprintf("%d/%s", index, instance)]; !ok && len(instances) >= maxDiscoveredServices {
					continue
				}
			}
			limit := browseIPLimit
			if k.kind == dns.TypePTR {
				limit = maxDiscoveredServices
			}
			if k.kind == dns.TypeSRV || k.kind == dns.TypeTXT {
				limit = 2
			}
			if k.kind == dns.TypeA || k.kind == dns.TypeAAAA {
				for count >= limit && evictFlushed(func(key browseRRKey) bool {
					return key.link == k.link && key.name == k.name && (key.kind == dns.TypeA || key.kind == dns.TypeAAAA)
				}) {
					count--
				}
			}
			if count >= limit {
				if k.kind != dns.TypeSRV && k.kind != dns.TypeTXT {
					continue
				}
				// Unique data changes must not be blocked by the bounded grace
				// history when a peer publishes several rapid updates.
				var oldest browseRRKey
				sequence := ^uint64(0)
				for key, value := range c.records {
					if key.link == index && key.name == k.name && key.kind == k.kind && value.serial < sequence {
						oldest = key
						sequence = value.serial
					}
				}
				c.bytes -= c.records[oldest].size
				delete(c.records, oldest)
			}
		}
		c.serial++
		c.records[k] = browseRR{dns.Copy(rr), now.Add(time.Duration(h.Ttl) * time.Second), now, size, c.serial}
		c.bytes += size - old.size
	}
}

func (c *browseCache) rrset(index int, name string, kind uint16) []browseRR {
	var out []browseRR
	for k, r := range c.records {
		if k.link == index && k.name == browseName(name) && k.kind == kind {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].serial > out[j].serial })
	return out
}
func (c *browseCache) results(now time.Time) []DiscoveredService {
	c.prune(now)
	var out []DiscoveredService
	for k, r := range c.records {
		ptr, ok := r.rr.(*dns.PTR)
		if !ok {
			continue
		}
		srvs := c.rrset(k.link, ptr.Ptr, dns.TypeSRV)
		txts := c.rrset(k.link, ptr.Ptr, dns.TypeTXT)
		if len(srvs) == 0 || len(txts) == 0 {
			continue
		}
		srv := srvs[0].rr.(*dns.SRV)
		if srv.Target == "." || srv.Port == 0 {
			continue
		}
		labels := dns.SplitDomainName(ptr.Ptr)
		entry := &zeroconf.ServiceEntry{ServiceRecord: zeroconf.ServiceRecord{Instance: labels[0], Service: c.service, Domain: c.domain}, HostName: srv.Target, Port: int(srv.Port), Text: txts[0].rr.(*dns.TXT).Txt}
		for _, kind := range []uint16{dns.TypeA, dns.TypeAAAA} {
			for _, addr := range c.rrset(k.link, srv.Target, kind) {
				var ip net.IP
				switch v := addr.rr.(type) {
				case *dns.A:
					ip = v.A
				case *dns.AAAA:
					ip = v.AAAA
				}
				if ip.IsMulticast() || ip.IsUnspecified() || ip.IsLoopback() {
					continue
				}
				if kind == dns.TypeA {
					entry.AddrIPv4 = append(entry.AddrIPv4, ip)
				} else {
					entry.AddrIPv6 = append(entry.AddrIPv6, ip)
				}
			}
		}
		svc, valid := entryToDiscovered(entry)
		if !valid || len(svc.IPv4)+len(svc.IPv6) == 0 {
			continue
		}
		svc.InterfaceIndex = k.link
		svc.IPv6Zones = make(map[string]string)
		for _, ip := range svc.IPv6 {
			if ip.IsLinkLocalUnicast() {
				svc.IPv6Zones[ip.String()] = c.links[k.link].iface.Name
			}
		}
		out = append(out, svc)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].InstanceName == out[j].InstanceName {
			return out[i].InterfaceIndex < out[j].InterfaceIndex
		}
		return out[i].InstanceName < out[j].InstanceName
	})
	return out
}

// queries includes missing dependencies and refreshes records after 80% of TTL.
func (c *browseCache) queries(index int, now time.Time) []dns.Question {
	c.prune(now)
	seen := make(map[string]bool)
	var out []dns.Question
	add := func(name string, kind uint16) {
		key := fmt.Sprintf("%s/%d", browseName(name), kind)
		if !seen[key] {
			seen[key] = true
			out = append(out, dns.Question{Name: name, Qtype: kind, Qclass: dns.ClassINET})
		}
	}
	add(c.browse, dns.TypePTR)
	need := func(name string, kind uint16) {
		set := c.rrset(index, name, kind)
		if len(set) == 0 {
			add(name, kind)
			return
		}
		for _, r := range set {
			if now.Sub(r.received) >= (r.expires.Sub(r.received) * 4 / 5) {
				add(name, kind)
				break
			}
		}
	}
	for k, r := range c.records {
		if k.link != index {
			continue
		}
		if ptr, ok := r.rr.(*dns.PTR); ok {
			need(ptr.Ptr, dns.TypeSRV)
			need(ptr.Ptr, dns.TypeTXT)
			for _, s := range c.rrset(index, ptr.Ptr, dns.TypeSRV) {
				target := s.rr.(*dns.SRV).Target
				if target != "." {
					need(target, dns.TypeA)
					need(target, dns.TypeAAAA)
				}
			}
		}
	}
	return out
}

type browsePacket struct {
	data  []byte
	index int
	src   *net.UDPAddr
	dst   net.IP
}

// browseLAN has no private deadline. Closing sockets interrupts blocked readers;
// the sole timer schedules queries, never ends the caller's browse window.
func browseLAN(ctx context.Context, ifaces []net.Interface, serviceType, domain string) ([]DiscoveredService, error) {
	if ctx == nil {
		ctx = context.TODO()
	}
	if ctx.Err() != nil {
		return nil, nil
	}
	if len(ifaces) == 0 {
		return nil, fmt.Errorf("discovery: no allowed browse interfaces")
	}
	cache := newBrowseCache(ifaces, serviceType, domain)
	conn4, err4 := openMDNSConn4(ifaces)
	conn6, err6 := openMDNSConn6(ifaces)
	// Interface and destination metadata are mandatory for receive isolation.
	if conn4 != nil {
		if err := conn4.SetControlMessage(ipv4.FlagInterface|ipv4.FlagDst, true); err != nil {
			err4 = err
			if errClose := conn4.Close(); errClose != nil {
				log.WithError(errClose).Debug("discovery: close unusable IPv4 browse socket")
			}
			conn4 = nil
		}
	}
	if conn6 != nil {
		if err := conn6.SetControlMessage(ipv6.FlagInterface|ipv6.FlagDst, true); err != nil {
			err6 = err
			if errClose := conn6.Close(); errClose != nil {
				log.WithError(errClose).Debug("discovery: close unusable IPv6 browse socket")
			}
			conn6 = nil
		}
	}
	if conn4 == nil && conn6 == nil {
		return nil, fmt.Errorf("discovery: open mDNS sockets: IPv4: %v; IPv6: %v", err4, err6)
	}
	packets := make(chan browsePacket, 32)
	readerErrors := make(chan error, 2)
	readers := 0
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var closeOnce sync.Once
	closeSockets := func() {
		closeOnce.Do(func() {
			if conn4 != nil {
				if errClose := conn4.Close(); errClose != nil {
					log.WithError(errClose).Debug("discovery: close IPv4 browse socket")
				}
			}
			if conn6 != nil {
				if errClose := conn6.Close(); errClose != nil {
					log.WithError(errClose).Debug("discovery: close IPv6 browse socket")
				}
			}
		})
	}
	// Cancellation interrupts reads and writes without installing socket deadlines.
	stopClose := context.AfterFunc(ctx, closeSockets)
	defer func() {
		stopClose()
		close(stop)
		closeSockets()
		wg.Wait()
	}()
	for _, v6 := range []bool{false, true} {
		if (!v6 && conn4 == nil) || (v6 && conn6 == nil) {
			continue
		}
		wg.Add(1)
		readers++
		go func(v6 bool) {
			defer wg.Done()
			buf := make([]byte, 65536)
			for {
				var p browsePacket
				var from net.Addr
				if v6 {
					n, cm, src, e := conn6.ReadFrom(buf)
					if e != nil {
						readerErrors <- e
						return
					}
					if cm != nil {
						p.index = cm.IfIndex
						p.dst = cm.Dst
					}
					from = src
					p.data = append([]byte(nil), buf[:n]...)
				} else {
					n, cm, src, e := conn4.ReadFrom(buf)
					if e != nil {
						readerErrors <- e
						return
					}
					if cm != nil {
						p.index = cm.IfIndex
						p.dst = cm.Dst
					}
					from = src
					p.data = append([]byte(nil), buf[:n]...)
				}
				p.src, _ = from.(*net.UDPAddr)
				select {
				case packets <- p:
				case <-stop:
					return
				case <-ctx.Done():
					return
				}
			}
		}(v6)
	}
	pending := make(map[string]browseQueryRetry)
	send := func(now time.Time) error {
		active := make(map[string]bool)
		attempted, sent := 0, 0
		var lastSendErr error
		for _, iface := range ifaces {
			qs := cache.queries(iface.Index, now)
			for _, q := range qs {
				key := fmt.Sprintf("%d/%s/%d", iface.Index, browseName(q.Name), q.Qtype)
				active[key] = true
				state := pending[key]
				if !cache.queryDue(iface.Index, q, state, now) {
					continue
				}
				state.sent(now)
				pending[key] = state
				for _, m := range cache.queryMessages(iface.Index, q, now) {
					wire, err := m.Pack()
					if err != nil {
						continue
					}
					if conn4 != nil {
						attempted++
						errSend := conn4.SetMulticastInterface(&iface)
						if errSend == nil {
							_, errSend = conn4.WriteTo(wire, nil, mdnsGroupAddrIPv4)
						}
						if errSend != nil {
							lastSendErr = errSend
						} else {
							sent++
						}
					}
					if conn6 != nil {
						attempted++
						errSend := conn6.SetMulticastInterface(&iface)
						if errSend == nil {
							_, errSend = conn6.WriteTo(wire, nil, mdnsGroupAddrIPv6)
						}
						if errSend != nil {
							lastSendErr = errSend
						} else {
							sent++
						}
					}
				}
			}
		}
		for key := range pending {
			if !active[key] {
				delete(pending, key)
			}
		}
		if attempted > 0 && sent == 0 && ctx.Err() == nil {
			return fmt.Errorf("discovery: send browse queries: %w", lastSendErr)
		}
		return nil
	}
	if errSend := send(time.Now()); errSend != nil {
		return nil, errSend
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return cache.results(time.Now()), nil
		case errRead := <-readerErrors:
			if ctx.Err() != nil {
				return cache.results(time.Now()), nil
			}
			readers--
			if readers == 0 {
				return cache.results(time.Now()), fmt.Errorf("discovery: mDNS readers stopped: %w", errRead)
			}
		case p := <-packets:
			msg := new(dns.Msg)
			if msg.Unpack(p.data) == nil {
				cache.ingest(msg, p.index, p.src, p.dst, time.Now())
			}
			if result := cache.results(time.Now()); len(result) >= maxDiscoveredServices {
				return result, nil
			}
		case now := <-timer.C:
			if errSend := send(now); errSend != nil {
				return cache.results(time.Now()), errSend
			}
			timer.Reset(time.Second)
		}
	}
}
