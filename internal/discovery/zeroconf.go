package discovery

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/libp2p/zeroconf/v2"
	log "github.com/sirupsen/logrus"
)

// ZeroconfAdvertiser wraps libp2p/zeroconf/v2 Server with defensive lifecycle management.
type ZeroconfAdvertiser struct {
	mu       sync.Mutex
	server   *zeroconf.Server
	inFlight bool
	closed   bool
}

// NewZeroconfAdvertiser returns a new ZeroconfAdvertiser.
func NewZeroconfAdvertiser() *ZeroconfAdvertiser {
	return &ZeroconfAdvertiser{}
}

// extractCleanHost returns bare hostname without any .local suffix
// to prevent libp2p/zeroconf/v2 from appending a duplicate .local. (e.g. on macOS).
func extractCleanHost() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "localhost"
	}
	h = strings.TrimSpace(h)
	h = strings.TrimSuffix(h, ".")
	h = strings.TrimSuffix(h, ".local")
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return "localhost"
	}
	return h
}

// extractInterfaceIPs collects non-loopback IP addresses from the selected interfaces.
func extractInterfaceIPs(ifaces []net.Interface) []string {
	var ips []string
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
				continue
			}
			ips = append(ips, ip.String())
		}
	}
	return ips
}

// Start registers and starts mDNS advertisement for the primary service type
// and any configured API protocol subtypes.
func (a *ZeroconfAdvertiser) Start(ctx context.Context, spec ServiceSpec) (err error) {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if len(spec.Interfaces) == 0 {
		return fmt.Errorf("discovery: cannot start advertiser with empty interface list (refusing fallback to all interfaces)")
	}
	if spec.Port < 1 || spec.Port > 65535 {
		return fmt.Errorf("discovery: invalid service port %d (must be between 1 and 65535)", spec.Port)
	}

	a.mu.Lock()
	if a.server != nil || a.inFlight {
		a.mu.Unlock()
		return fmt.Errorf("discovery: advertiser already started")
	}
	a.inFlight = true
	a.closed = false
	a.mu.Unlock()

	var server *zeroconf.Server
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("discovery: panic starting advertiser: %v", r)
			log.Errorf("%v", err)
		}
		a.mu.Lock()
		a.inFlight = false
		if err != nil {
			hold := a.server
			a.server = nil
			a.mu.Unlock()
			if hold != nil {
				hold.Shutdown()
			}
			if server != nil && hold != server {
				server.Shutdown()
			}
			return
		}
		a.mu.Unlock()
	}()

	domain := spec.Domain
	if domain == "" {
		domain = DefaultDomain
	}
	serviceType := spec.ServiceType
	if serviceType == "" {
		serviceType = DefaultServiceType
	}

	cleanHost := extractCleanHost()
	ips := spec.AdvertisedIPs
	if len(ips) == 0 {
		ips = extractInterfaceIPs(spec.Interfaces)
	}
	if len(ips) == 0 {
		return fmt.Errorf("discovery: no usable IP addresses found on specified interfaces")
	}

	spec.InstanceName = sanitizeInstanceName(spec.InstanceName)
	if spec.InstanceName == "" {
		spec.InstanceName = DefaultInstancePrefix + "0001"
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}

	primaryService := serviceType
	for _, sub := range spec.Subtypes {
		if clean := sanitizeSubtype(sub); clean != "" {
			primaryService += "," + clean
		}
	}

	var errRegister error
	server, errRegister = zeroconf.RegisterProxy(
		spec.InstanceName,
		primaryService,
		domain,
		spec.Port,
		cleanHost,
		ips,
		spec.TextRecords,
		spec.Interfaces,
	)
	if errRegister != nil {
		return fmt.Errorf("discovery: failed to register primary service %s: %w", primaryService, errRegister)
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("discovery: advertiser stopped before start completed")
	}
	a.server = server
	server = nil
	return nil
}

// Stop shuts down the mDNS advertisement server and sends goodbye packets.
func (a *ZeroconfAdvertiser) Stop() error {
	a.mu.Lock()
	a.closed = true
	server := a.server
	a.server = nil
	a.mu.Unlock()
	if server == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			log.Warnf("discovery: panic during advertiser shutdown: %v", r)
		}
	}()
	server.Shutdown()
	return nil
}

// ZeroconfBrowser provides DNS-SD browsing with fallback support.
type ZeroconfBrowser struct {
	options []zeroconf.ClientOption
}

// NewZeroconfBrowser creates a new ZeroconfBrowser.
func NewZeroconfBrowser(ifaces ...net.Interface) *ZeroconfBrowser {
	var opts []zeroconf.ClientOption
	if len(ifaces) > 0 {
		opts = append(opts, zeroconf.SelectIfaces(ifaces))
	}
	return &ZeroconfBrowser{options: opts}
}

const (
	maxDiscoveredServices      = 256
	maxBrowseEntries           = 256
	maxBrowseTXTRecords        = 64
	maxBrowseTXTBytes          = 16 * 1024
	maxDiscoveredAddresses     = 32
	maxDiscoveredMetadataItems = 32
	maxMetadataItemBytes       = 64
)

func browseEntryWithinLimits(entry *zeroconf.ServiceEntry) bool {
	if entry == nil || len(entry.Text) > maxBrowseTXTRecords {
		return false
	}
	total := 0
	for _, record := range entry.Text {
		if len(record) > maxTXTRecordBytes {
			return false
		}
		total += len(record) + 1
		if total > maxBrowseTXTBytes {
			return false
		}
	}
	return true
}

// Browse performs a standard mDNS browse query for the given service type.
func (b *ZeroconfBrowser) Browse(ctx context.Context, serviceType, domain string) ([]DiscoveredService, error) {
	if ctx == nil {
		ctx = context.TODO()
	}
	if domain == "" {
		domain = DefaultDomain
	}
	if serviceType == "" {
		serviceType = DefaultServiceType
	}

	entries := make(chan *zeroconf.ServiceEntry, 32)
	browseCtx, cancelBrowse := context.WithCancel(ctx)
	defer cancelBrowse()

	var discovered []DiscoveredService
	var mu sync.Mutex
	seen := make(map[string]int)

	// Stop the underlying zeroconf client after a bounded number of entries. This
	// is separate from the caller's context so a LAN flood cannot grow the
	// dependency's internal sentEntries cache for the full browse lifetime.
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		entriesSeen := 0
		for entry := range entries {
			if entriesSeen >= maxBrowseEntries {
				continue
			}
			entriesSeen++

			if browseEntryWithinLimits(entry) {
				svc := entryToDiscovered(entry)
				if svc.Port != 0 && (len(svc.IPv4) != 0 || len(svc.IPv6) != 0) {
					key := discoveredServiceKey(svc)
					mu.Lock()
					if index, ok := seen[key]; ok {
						mergeDiscoveredService(&discovered[index], svc)
					} else if len(discovered) < maxDiscoveredServices {
						seen[key] = len(discovered)
						discovered = append(discovered, svc)
					}
					mu.Unlock()
				}
			}

			if entriesSeen == maxBrowseEntries {
				cancelBrowse()
			}
		}
	}()

	errBrowse := zeroconf.Browse(browseCtx, serviceType, domain, entries, b.options...)
	if errBrowse != nil {
		// zeroconf may already have closed entries after a runtime error.
		closeBrowseEntries(entries)
		<-doneCh
		return nil, fmt.Errorf("discovery: browse query failed: %w", errBrowse)
	}

	<-doneCh

	return discovered, nil
}

func closeBrowseEntries(entries chan *zeroconf.ServiceEntry) {
	defer func() {
		_ = recover()
	}()
	close(entries)
}

func discoveredServiceKey(svc DiscoveredService) string {
	return strings.Join([]string{svc.InstanceName, svc.ServiceType, svc.Domain}, "\x00")
}

func mergeDiscoveredService(dst *DiscoveredService, src DiscoveredService) {
	if dst == nil {
		return
	}
	if src.Host != "" {
		dst.Host = src.Host
	}
	if src.Port != 0 {
		dst.Port = src.Port
	}
	dst.IPv4 = appendUniqueIPs(dst.IPv4, src.IPv4)
	dst.IPv6 = appendUniqueIPs(dst.IPv6, src.IPv6)
	if src.Product != "" {
		dst.Product = src.Product
	}
	if src.Version != "" {
		dst.Version = src.Version
	}
	if src.NodeRole != "" {
		dst.NodeRole = src.NodeRole
	}
	if dst.RawTXT == nil {
		dst.RawTXT = make(map[string]string)
	}
	mergeRawTXTRecords(dst.RawTXT, src.RawTXT)
	if value, ok := dst.RawTXT["auth_required"]; ok {
		dst.AuthRequired = strings.EqualFold(strings.TrimSpace(value), "true")
	}
	dst.AuthMethods = appendUniqueStrings(dst.AuthMethods, src.AuthMethods)
	dst.Protocols = appendUniqueStrings(dst.Protocols, src.Protocols)
	dst.Features = appendUniqueStrings(dst.Features, src.Features)
	if dst.Endpoints == nil {
		dst.Endpoints = make(map[string]string)
	}
	for key, value := range src.Endpoints {
		dst.Endpoints[key] = value
	}
}

func mergeRawTXTRecords(dst, src map[string]string) {
	totalBytes := rawTXTMapBytes(dst)
	for key, value := range src {
		if key == "" {
			continue
		}
		replacedBytes := 0
		_, exists := dst[key]
		if exists {
			replacedBytes = rawTXTRecordBytes(key, dst[key])
		} else if len(dst) >= maxBrowseTXTRecords {
			continue
		}
		nextBytes := totalBytes - replacedBytes + rawTXTRecordBytes(key, value)
		if nextBytes > maxBrowseTXTBytes {
			continue
		}
		dst[key] = value
		totalBytes = nextBytes
	}
}

func rawTXTMapBytes(records map[string]string) int {
	totalBytes := 0
	for key, value := range records {
		totalBytes += rawTXTRecordBytes(key, value)
	}
	return totalBytes
}

func rawTXTRecordBytes(key, value string) int {
	return len(key) + len(value) + 1
}

func appendUniqueIPs(dst, src []net.IP) []net.IP {
	for _, ip := range src {
		if ip == nil || len(dst) >= maxDiscoveredAddresses {
			continue
		}
		duplicate := false
		for _, existing := range dst {
			if existing.Equal(ip) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			dst = append(dst, append(net.IP(nil), ip...))
		}
	}
	return dst
}

func appendUniqueStrings(dst, src []string) []string {
	for _, value := range src {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > maxMetadataItemBytes || len(dst) >= maxDiscoveredMetadataItems {
			continue
		}
		duplicate := false
		for _, existing := range dst {
			if existing == value {
				duplicate = true
				break
			}
		}
		if !duplicate {
			dst = append(dst, value)
		}
	}
	return dst
}

func parseTXTList(value string) []string {
	var result []string
	for len(value) > 0 && len(result) < maxDiscoveredMetadataItems {
		item := value
		if before, after, ok := strings.Cut(value, ","); ok {
			item = before
			value = after
		} else {
			value = ""
		}
		item = strings.TrimSpace(item)
		if item == "" || len(item) > maxMetadataItemBytes {
			continue
		}
		result = appendUniqueStrings(result, []string{item})
	}
	return result
}

// BrowseWithFallback discovers all AI gateways on the LAN (_ai-gateway._tcp) and prioritizes CPA instances.
func (b *ZeroconfBrowser) BrowseWithFallback(ctx context.Context) ([]DiscoveredService, error) {
	return b.BrowseWithFallbackServiceType(ctx, DefaultServiceType)
}

// BrowseWithFallbackServiceType discovers services of the requested type and prioritizes CPA instances.
func (b *ZeroconfBrowser) BrowseWithFallbackServiceType(ctx context.Context, serviceType string) ([]DiscoveredService, error) {
	if strings.TrimSpace(serviceType) == "" {
		serviceType = DefaultServiceType
	}
	allGateways, errMain := b.Browse(ctx, serviceType, DefaultDomain)
	if errMain != nil && len(allGateways) == 0 {
		return nil, errMain
	}

	// Partition: prioritize CPA instances first, then other standard AI gateways
	var cpaGateways []DiscoveredService
	var otherGateways []DiscoveredService
	for _, gw := range allGateways {
		if gw.Product == ProductCPA {
			cpaGateways = append(cpaGateways, gw)
		} else {
			otherGateways = append(otherGateways, gw)
		}
	}

	return append(cpaGateways, otherGateways...), nil
}

// sanitizeEndpointPath validates that an endpoint path is a safe relative API path
// starting with '/' and containing no scheme, domain, protocol-relative prefixes, or traversal elements.
func sanitizeEndpointPath(p string) string {
	p = strings.TrimSpace(p)
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.Contains(p, `\`) || strings.Contains(p, "://") || strings.Contains(p, "..") {
		return ""
	}
	for _, r := range p {
		if r < 32 || r == 127 {
			return ""
		}
	}
	return p
}

func entryToDiscovered(e *zeroconf.ServiceEntry) DiscoveredService {
	parsed := ParseTXTRecords(e.Text)

	port := e.Port
	if port < 1 || port > 65535 {
		port = 0
	}

	svc := DiscoveredService{
		InstanceName: e.Instance,
		ServiceType:  e.Service,
		Domain:       e.Domain,
		Host:         e.HostName,
		Port:         port,
		IPv4:         filterUsableIPs(e.AddrIPv4),
		IPv6:         filterUsableIPs(e.AddrIPv6),
		Product:      parsed["product"],
		Version:      parsed["version"],
		NodeRole:     parsed["node_role"],
		RawTXT:       parsed,
		Endpoints:    make(map[string]string),
	}

	if parsed["auth_required"] == "true" {
		svc.AuthRequired = true
	}
	if methods := parsed["auth_methods"]; methods != "" {
		svc.AuthMethods = parseTXTList(methods)
	}
	if protos := parsed["protocols"]; protos != "" {
		svc.Protocols = parseTXTList(protos)
	}
	if feats := parsed["features"]; feats != "" {
		svc.Features = parseTXTList(feats)
	}

	if v, ok := parsed["api_openai"]; ok {
		if clean := sanitizeEndpointPath(v); clean != "" {
			svc.Endpoints["openai"] = clean
		}
	}
	if v, ok := parsed["api_anthropic"]; ok {
		if clean := sanitizeEndpointPath(v); clean != "" {
			svc.Endpoints["anthropic"] = clean
		}
	}
	if v, ok := parsed["api_gemini"]; ok {
		if clean := sanitizeEndpointPath(v); clean != "" {
			svc.Endpoints["gemini"] = clean
		}
	}

	return svc
}

func filterUsableIPs(ips []net.IP) []net.IP {
	usable := make([]net.IP, 0, min(len(ips), maxDiscoveredAddresses))
	for _, ip := range ips {
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		usable = append(usable, append(net.IP(nil), ip...))
		if len(usable) >= maxDiscoveredAddresses {
			break
		}
	}
	return usable
}
