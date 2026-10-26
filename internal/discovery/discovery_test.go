package discovery

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/zeroconf/v2"
	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
)

func TestInstanceID_PersistenceAndFormat(t *testing.T) {
	ResetCachedInstanceID()
	tmpDir, err := os.MkdirTemp("", "cpa-discovery-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() {
		if errRemove := os.RemoveAll(tmpDir); errRemove != nil {
			t.Errorf("failed to remove temporary directory %s: %v", tmpDir, errRemove)
		}
		ResetCachedInstanceID()
	}()

	// 1. Generate new ID
	id1 := GetOrGenerateInstanceID(tmpDir)
	if len(id1) != 4 {
		t.Errorf("expected 4-char hex ID, got %s (len %d)", id1, len(id1))
	}

	// 2. Retrieve again from same dir, should be identical
	id2 := GetOrGenerateInstanceID(tmpDir)
	if id1 != id2 {
		t.Errorf("expected persistent ID %s, got %s", id1, id2)
	}

	// 3. Format instance name with default
	name1 := FormatInstanceName("", id1)
	expectedName := "CPA-" + id1
	if name1 != expectedName {
		t.Errorf("expected %s, got %s", expectedName, name1)
	}

	// 4. Test concurrent calls produce identical ID
	ResetCachedInstanceID()
	var wg sync.WaitGroup
	results := make([]string, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = GetOrGenerateInstanceID(tmpDir)
		}(i)
	}
	wg.Wait()
	for _, r := range results {
		if r != id1 {
			t.Errorf("concurrent ID mismatch: expected %s, got %s", id1, r)
		}
	}

	// 5. Test directory isolation
	tmpDirB, errB := os.MkdirTemp("", "cpa-discovery-test-b-*")
	if errB == nil {
		defer func() {
			if errRemove := os.RemoveAll(tmpDirB); errRemove != nil {
				t.Errorf("failed to remove temporary directory %s: %v", tmpDirB, errRemove)
			}
		}()
		idB := GetOrGenerateInstanceID(tmpDirB)
		if len(idB) != 4 {
			t.Errorf("expected 4-char hex ID for dir B, got %s", idB)
		}
	}

	// 6. Custom names stay unique by appending the persistent short ID
	name2 := FormatInstanceName("My-Custom-Node", id1)
	if name2 != "My-Custom-Node-"+id1 {
		t.Errorf("expected custom name with instance ID suffix, got %s", name2)
	}
}

func TestFormatInstanceNameAlwaysIncludesID(t *testing.T) {
	if got := FormatInstanceName("", "8F3B"); got != "CPA-8F3B" {
		t.Fatalf("default name = %q, want CPA-8F3B", got)
	}
	if got := FormatInstanceName("office", "8F3B"); got != "office-8F3B" {
		t.Fatalf("custom name = %q, want office-8F3B", got)
	}
	if got := FormatInstanceName("office-8F3B", "8F3B"); got != "office-8F3B" {
		t.Fatalf("already-suffixed name = %q, want office-8F3B", got)
	}
	if got := FormatInstanceName("CPA-8F3B", "8F3B"); got != "CPA-8F3B" {
		t.Fatalf("default-form custom name = %q, want CPA-8F3B", got)
	}
	long := strings.Repeat("n", 70)
	got := FormatInstanceName(long, "8F3B")
	if len(got) > 63 || !strings.HasSuffix(got, "-8F3B") {
		t.Fatalf("truncated name = %q (len %d)", got, len(got))
	}
}

func TestBuildTXTRecords_SizeAndKeys(t *testing.T) {
	opts := DefaultTXTOptions()
	opts.InstanceID = "8F3B"
	opts.TLS = true
	opts.AuthRequired = true

	records := BuildTXTRecords(opts)
	if len(records) == 0 {
		t.Fatalf("expected non-empty TXT records")
	}

	parsed := ParseTXTRecords(records)

	// Verify required keys
	if parsed["version"] != "1" {
		t.Errorf("expected version=1, got %s", parsed["version"])
	}
	if parsed["product"] != ProductCPA {
		t.Errorf("expected product=%s, got %s", ProductCPA, parsed["product"])
	}
	if parsed["tls"] != "1" {
		t.Errorf("expected tls=1, got %s", parsed["tls"])
	}
	if parsed["auth_required"] != "true" {
		t.Errorf("expected auth_required=true, got %s", parsed["auth_required"])
	}
	if parsed["management"] != "false" {
		t.Errorf("expected management=false, got %s", parsed["management"])
	}
	if parsed["api_openai"] != "/v1" {
		t.Errorf("expected api_openai=/v1, got %s", parsed["api_openai"])
	}
	if parsed["api_gemini"] != "/v1beta" {
		t.Errorf("expected api_gemini=/v1beta, got %s", parsed["api_gemini"])
	}
	expectedProtocols := "chat-completions,responses,messages,generate-content,interactions"
	if parsed["protocols"] != expectedProtocols {
		t.Errorf("expected protocols=%s, got %s", expectedProtocols, parsed["protocols"])
	}
	expectedFeatures := "chat,responses,messages,generate_content,interactions"
	if parsed["features"] != expectedFeatures {
		t.Errorf("expected features=%s, got %s", expectedFeatures, parsed["features"])
	}

	// Calculate total bytes
	totalBytes := 0
	for _, r := range records {
		totalBytes += len(r) + 1
	}
	if totalBytes > 400 {
		t.Errorf("TXT records exceed 400 bytes limit: %d bytes", totalBytes)
	}
}

func TestParseTXTRecords_NormalizesKeys(t *testing.T) {
	parsed := ParseTXTRecords([]string{"TLS=1", "API_OpenAI=/v1", "Product=cliproxyapi"})
	if parsed["tls"] != "1" || parsed["api_openai"] != "/v1" || parsed["product"] != "cliproxyapi" {
		t.Fatalf("parsed TXT records = %#v", parsed)
	}
}

func TestBuildTXTRecords_OversizedAndRFCEnforcement(t *testing.T) {
	opts := DefaultTXTOptions()
	// Giant 500-byte product and feature strings
	opts.Product = strings.Repeat("A", 500)
	opts.Features = []string{strings.Repeat("F", 300)}
	opts.NodeRole = strings.Repeat("R", 200)

	records := BuildTXTRecords(opts)

	totalBytes := 0
	for _, r := range records {
		if len(r) > maxTXTRecordBytes {
			t.Errorf("individual TXT record exceeds RFC 6763 limit of %d bytes: %d", maxTXTRecordBytes, len(r))
		}
		totalBytes += len(r) + 1
	}

	if totalBytes > maxTXTBytes {
		t.Errorf("total TXT records length %d exceeds max %d bytes", totalBytes, maxTXTBytes)
	}
	parsed := ParseTXTRecords(records)
	if _, ok := parsed["product"]; ok {
		t.Errorf("oversized product key should be omitted, got %q", parsed["product"])
	}
}

func TestValidation_ServiceTypeAndLabels(t *testing.T) {
	// Valid service types
	if err := validateServiceType("_ai-gateway._tcp"); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
	if err := validateServiceType("_cliproxy._tcp"); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}

	// Invalid service types
	invalidTypes := []string{
		"ai-gateway._tcp",                 // Missing leading underscore
		"_ai-gateway.tcp",                 // Missing underscore on protocol
		"_toolongservicenamemoret15._tcp", // Exceeds 15 chars (RFC 6335)
		"_-invalid._tcp",                  // Leading hyphen
		"_invalid-._tcp",                  // Trailing hyphen
		"_ai_gateway._tcp",                // Underscore in name (RFC 6335)
		"_ai-gateway._udp",                // CPA is TCP-only
	}
	for _, it := range invalidTypes {
		if err := validateServiceType(it); err == nil {
			t.Errorf("expected invalid for %q, got nil", it)
		}
	}

	// Subtype sanitization
	if s := sanitizeSubtype("responses"); s != SubtypeResponses {
		t.Errorf("expected %s, got %s", SubtypeResponses, s)
	}
	if s := sanitizeSubtype(SubtypeResponses); s != SubtypeResponses {
		t.Errorf("expected %s, got %s", SubtypeResponses, s)
	}
	// Invalid subtypes (RFC 6763 §7.1 violation)
	if s := sanitizeSubtype("_responses._sub"); s != "" {
		t.Errorf("expected empty for _responses._sub, got %s", s)
	}
	if s := sanitizeSubtype("_responses_"); s != "" {
		t.Errorf("expected empty for _responses_, got %s", s)
	}
	if s := sanitizeSubtype("_-responses"); s != "" {
		t.Errorf("expected empty for _-responses, got %s", s)
	}
	if s := sanitizeSubtype("_responses-"); s != "" {
		t.Errorf("expected empty for _responses-, got %s", s)
	}
	if s := sanitizeSubtype("_" + strings.Repeat("a", 62)); s == "" {
		t.Error("expected 62-character subtype payload to be accepted")
	}
	if s := sanitizeSubtype("_" + strings.Repeat("a", 63)); s != "" {
		t.Error("expected 63-character subtype payload to be rejected")
	}

	// Endpoint path sanitization (defense against traversal and protocol-relative SSRF)
	if p := sanitizeEndpointPath("/v1"); p != "/v1" {
		t.Errorf("expected /v1, got %s", p)
	}
	if p := sanitizeEndpointPath("/v1/chat/completions"); p != "/v1/chat/completions" {
		t.Errorf("expected /v1/chat/completions, got %s", p)
	}
	if p := sanitizeEndpointPath("http://evil.com/v1"); p != "" {
		t.Errorf("expected empty for absolute URL, got %s", p)
	}
	if p := sanitizeEndpointPath("//attacker.com/v1"); p != "" {
		t.Errorf("expected empty for protocol-relative URL //attacker.com/v1, got %s", p)
	}
	if p := sanitizeEndpointPath("/\\attacker.com/v1"); p != "" {
		t.Errorf("expected empty for backslash path /\\attacker.com/v1, got %s", p)
	}
	if p := sanitizeEndpointPath("/../etc/passwd"); p != "" {
		t.Errorf("expected empty for traversal, got %s", p)
	}

	// Instance name UTF-8 rune boundary sanitization
	// 20 Chinese characters = 60 bytes (valid), 22 Chinese characters = 66 bytes (exceeds 63)
	longChinese := strings.Repeat("网", 22)
	truncated := sanitizeInstanceName(longChinese)
	if len(truncated) > 63 {
		t.Errorf("expected instance name <= 63 bytes, got %d", len(truncated))
	}
	if len(truncated)%3 != 0 {
		t.Errorf("expected clean UTF-8 rune boundary (multiple of 3 for Chinese characters), got %d", len(truncated))
	}
}

func TestFilterUsableIPs(t *testing.T) {
	ips := []net.IP{
		net.ParseIP("192.0.2.10"),
		net.ParseIP("fe80::1"),
		net.ParseIP("127.0.0.1"),
		net.ParseIP("::"),
	}
	usable := filterUsableIPs(ips)
	if len(usable) != 2 || usable[0].String() != "192.0.2.10" || usable[1].String() != "fe80::1" {
		t.Fatalf("filterUsableIPs() = %v, want [192.0.2.10 fe80::1]", usable)
	}
}

func TestInterfaceFiltering_Helpers(t *testing.T) {
	// Test isVirtualOrTunnel
	virtualNames := []string{"docker0", "veth45a", "utun3", "tailscale0", "wg0", "tun1", "tap0", "br-123", "awdl0", "llw0"}
	for _, name := range virtualNames {
		if !isVirtualOrTunnel(name) {
			t.Errorf("expected %s to be recognized as virtual/tunnel interface", name)
		}
	}

	physicalNames := []string{"en0", "eth0", "wlan0", "eno1", "bond0"}
	for _, name := range physicalNames {
		if isVirtualOrTunnel(name) {
			t.Errorf("expected %s to be recognized as physical interface", name)
		}
		if !isLikelyPhysicalLAN(name) {
			t.Errorf("expected %s to be accepted as a likely physical LAN interface", name)
		}
	}
	for _, name := range []string{"bridge100", "p2p0", "ppp0", "mystery0"} {
		if isLikelyPhysicalLAN(name) {
			t.Errorf("expected %s not to be accepted by the default physical LAN allow-list", name)
		}
	}

	// Test matchesAny
	if !matchesAny("docker0", []string{"docker*"}) {
		t.Errorf("expected wildcard match for docker*")
	}
	if !matchesAny("en0", []string{"en0", "eth0"}) {
		t.Errorf("expected exact match for en0")
	}
	if matchesAny("wlan0", []string{"docker*", "utun*"}) {
		t.Errorf("expected wlan0 not to match")
	}
}

func TestBrowseEntryWithinLimits(t *testing.T) {
	if !browseEntryWithinLimits(&zeroconf.ServiceEntry{Text: []string{"product=cliproxyapi"}}) {
		t.Fatal("expected a small TXT entry to be accepted")
	}
	if browseEntryWithinLimits(&zeroconf.ServiceEntry{Text: []string{strings.Repeat("x", maxTXTRecordBytes+1)}}) {
		t.Fatal("expected an oversized TXT record to be rejected")
	}
	if browseEntryWithinLimits(&zeroconf.ServiceEntry{Text: make([]string, maxBrowseTXTRecords+1)}) {
		t.Fatal("expected too many TXT records to be rejected")
	}
}

func TestMergeDiscoveredService(t *testing.T) {
	dst := DiscoveredService{
		InstanceName: "node",
		ServiceType:  DefaultServiceType,
		Domain:       DefaultDomain,
		Port:         8317,
		IPv4:         []net.IP{net.ParseIP("192.0.2.10")},
		RawTXT:       map[string]string{"auth_required": "true"},
		Endpoints:    map[string]string{"openai": "/v1"},
	}
	src := DiscoveredService{
		InstanceName: "node",
		ServiceType:  DefaultServiceType,
		Domain:       DefaultDomain,
		Port:         8317,
		IPv4:         []net.IP{net.ParseIP("192.0.2.10"), net.ParseIP("192.0.2.11")},
		IPv6:         []net.IP{net.ParseIP("2001:db8::10")},
		Product:      ProductCPA,
		RawTXT:       map[string]string{"auth_required": "false", "tls": "1"},
		Endpoints:    map[string]string{"anthropic": "/v1/messages"},
	}
	mergeDiscoveredService(&dst, src)
	if len(dst.IPv4) != 2 || len(dst.IPv6) != 1 {
		t.Fatalf("merged addresses = v4 %v, v6 %v", dst.IPv4, dst.IPv6)
	}
	if dst.AuthRequired || dst.RawTXT["tls"] != "1" || dst.Endpoints["anthropic"] != "/v1/messages" {
		t.Fatalf("merged metadata = %#v", dst)
	}
}

func TestDiscoveryMergeLimits(t *testing.T) {
	dst := DiscoveredService{
		IPv4:   []net.IP{net.ParseIP("192.0.2.1")},
		RawTXT: map[string]string{"auth_required": "true"},
	}
	var srcIPs []net.IP
	for i := 0; i < maxDiscoveredAddresses*2; i++ {
		srcIPs = append(srcIPs, net.IPv4(198, 18, byte(i/256), byte(i%256)))
	}
	metadata := make([]string, 0, maxDiscoveredMetadataItems*2)
	for i := 0; i < maxDiscoveredMetadataItems*2; i++ {
		metadata = append(metadata, fmt.Sprintf("feature-%02d", i))
	}
	rawTXT := make(map[string]string, maxBrowseTXTRecords*2)
	for i := 0; i < maxBrowseTXTRecords*2; i++ {
		rawTXT[fmt.Sprintf("key-%03d", i)] = strings.Repeat("v", maxTXTRecordBytes)
	}

	mergeDiscoveredService(&dst, DiscoveredService{
		IPv4:        srcIPs,
		RawTXT:      rawTXT,
		AuthMethods: metadata,
		Protocols:   metadata,
		Features:    metadata,
	})

	if len(dst.IPv4) != maxDiscoveredAddresses {
		t.Fatalf("merged IPv4 count = %d, want %d", len(dst.IPv4), maxDiscoveredAddresses)
	}
	if len(dst.AuthMethods) != maxDiscoveredMetadataItems || len(dst.Protocols) != maxDiscoveredMetadataItems || len(dst.Features) != maxDiscoveredMetadataItems {
		t.Fatalf("merged metadata counts = auth %d, protocols %d, features %d; want %d each", len(dst.AuthMethods), len(dst.Protocols), len(dst.Features), maxDiscoveredMetadataItems)
	}
	if len(dst.RawTXT) > maxBrowseTXTRecords || rawTXTMapBytes(dst.RawTXT) > maxBrowseTXTBytes {
		t.Fatalf("merged TXT size = %d records/%d bytes, limits are %d records/%d bytes", len(dst.RawTXT), rawTXTMapBytes(dst.RawTXT), maxBrowseTXTRecords, maxBrowseTXTBytes)
	}
}

func TestEntryToDiscoveredLimitsTXTLists(t *testing.T) {
	var methods strings.Builder
	for i := 0; i < maxDiscoveredMetadataItems*2; i++ {
		if i > 0 {
			methods.WriteByte(',')
		}
		methods.WriteString(fmt.Sprintf("method-%02d", i))
	}
	addresses := make([]net.IP, 0, maxDiscoveredAddresses*2)
	for i := 0; i < maxDiscoveredAddresses*2; i++ {
		addresses = append(addresses, net.IPv4(198, 18, byte(i/256), byte(i%256)))
	}
	svc := entryToDiscovered(&zeroconf.ServiceEntry{
		ServiceRecord: zeroconf.ServiceRecord{
			Instance: "node",
			Service:  DefaultServiceType,
			Domain:   DefaultDomain,
		},
		Port:     8317,
		AddrIPv4: addresses,
		Text:     []string{"auth_methods=" + methods.String()},
	})
	if len(svc.AuthMethods) != maxDiscoveredMetadataItems {
		t.Fatalf("auth method count = %d, want %d", len(svc.AuthMethods), maxDiscoveredMetadataItems)
	}
	if len(svc.IPv4) != maxDiscoveredAddresses {
		t.Fatalf("IPv4 count = %d, want %d", len(svc.IPv4), maxDiscoveredAddresses)
	}
}

func TestFilterInterfaces_RealMachine(t *testing.T) {
	// Testing real interface filter on current test environment
	ifaces, err := FilterInterfaces(nil, nil)
	if err != nil {
		t.Fatalf("unexpected error filtering interfaces: %v", err)
	}

	// Verify no excluded interfaces slipped in
	for _, iface := range ifaces {
		name := strings.ToLower(iface.Name)
		if isVirtualOrTunnel(name) {
			t.Errorf("interface %s should have been filtered out", name)
		}
	}
}

func TestAdvertiser_IdempotenceAndShutdown(t *testing.T) {
	adv := NewZeroconfAdvertiser()

	// Stopping an unstarted advertiser should be safe
	if err := adv.Stop(); err != nil {
		t.Errorf("unexpected error stopping unstarted advertiser: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ifaces, _ := FilterInterfaces(nil, nil)
	spec := ServiceSpec{
		InstanceName: "CPA-Test-Unit",
		ServiceType:  "_test-ai._tcp",
		Domain:       "local.",
		Port:         65432,
		TextRecords:  []string{"version=1", "product=test"},
		Interfaces:   ifaces,
	}

	errStart := adv.Start(ctx, spec)
	if errStart != nil {
		// In some restricted CI environments multicast bind may fail; handle gracefully
		t.Logf("multicast start failed (likely restricted sandbox network): %v", errStart)
		return
	}

	// Calling Start again while running should fail
	if errSecond := adv.Start(ctx, spec); errSecond == nil {
		t.Errorf("expected error on duplicate Start(), got nil")
	}

	// Calling Stop should cleanly shutdown
	if errStop := adv.Stop(); errStop != nil {
		t.Errorf("unexpected error on Stop(): %v", errStop)
	}

	// Calling Stop a second time should be idempotent
	if errStop2 := adv.Stop(); errStop2 != nil {
		t.Errorf("unexpected error on second Stop(): %v", errStop2)
	}
}

func TestAdvertiserAndBrowser_Integration(t *testing.T) {
	ifaces, _ := FilterInterfaces(nil, nil)
	spec := ServiceSpec{
		InstanceName: "CPA-LiveTest-42",
		ServiceType:  DefaultServiceType,
		Domain:       DefaultDomain,
		Port:         54321,
		Subtypes:     []string{SubtypeResponses},
		TextRecords:  BuildTXTRecords(DefaultTXTOptions()),
		Interfaces:   ifaces,
	}

	adv := NewZeroconfAdvertiser()
	startCtx, startCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer startCancel()
	if err := adv.Start(startCtx, spec); err != nil {
		t.Skipf("skipping live multicast test: %v", err)
	}
	defer func() {
		if errStop := adv.Stop(); errStop != nil {
			t.Errorf("advertiser cleanup failed: %v", errStop)
		}
	}()

	browseCtx, browseCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer browseCancel()
	browser := NewZeroconfBrowser(ifaces...)
	results, err := browser.Browse(browseCtx, DefaultServiceType, DefaultDomain)
	if err != nil {
		t.Fatalf("browse failed: %v", err)
	}

	found := false
	for _, res := range results {
		if res.InstanceName == "CPA-LiveTest-42" {
			found = true
			if res.Port != 54321 {
				t.Errorf("expected port 54321, got %d", res.Port)
			}
			if res.Product != ProductCPA {
				t.Errorf("expected product %s, got %s", ProductCPA, res.Product)
			}
			osHost := extractCleanHost() + "." + DefaultDomain
			if strings.EqualFold(res.Host, osHost) {
				t.Errorf("expected proxy-specific host instead of OS host %q, got %q", osHost, res.Host)
			}
			if res.Host != "cpa-livetest-42.local." {
				t.Errorf("expected advertised host cpa-livetest-42.local., got %q", res.Host)
			}
			break
		}
	}

	if !found {
		t.Fatalf("advertised instance CPA-LiveTest-42 was not discovered")
	}

	if errDirect := verifyDirectHostQueryAndGoodbye(t, ifaces, "cpa-livetest-42.local.", adv.Stop); errDirect != nil {
		t.Fatalf("direct host query / goodbye verification failed: %v", errDirect)
	}
}

func verifyDirectHostQueryAndGoodbye(t *testing.T, ifaces []net.Interface, hostFQDN string, stopAdv func() error) error {
	t.Helper()

	qName, errName := dnsmessage.NewName(hostFQDN)
	if errName != nil {
		return errName
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{})
	if errStartQ := builder.StartQuestions(); errStartQ != nil {
		return errStartQ
	}
	if errQ := builder.Question(dnsmessage.Question{
		Name:  qName,
		Type:  dnsmessage.TypeALL,
		Class: dnsmessage.ClassINET,
	}); errQ != nil {
		return errQ
	}
	queryBytes, errFinish := builder.Finish()
	if errFinish != nil {
		return errFinish
	}

	uConn, errListen := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if errListen != nil {
		return errListen
	}
	defer func() {
		if errClose := uConn.Close(); errClose != nil {
			t.Logf("close unicast query conn: %v", errClose)
		}
	}()
	pConn := ipv4.NewPacketConn(uConn)
	for i := range ifaces {
		_ = pConn.SetMulticastInterface(&ifaces[i])
		_, _ = pConn.WriteTo(queryBytes, nil, &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353})
	}

	_ = uConn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	buf := make([]byte, 4096)
	gotDirectAddr := false
	for !gotDirectAddr {
		n, _, errRead := uConn.ReadFromUDP(buf)
		if errRead != nil {
			break
		}
		if hasHostAddressRecord(buf[:n], hostFQDN, false) {
			gotDirectAddr = true
		}
	}
	if !gotDirectAddr {
		return fmt.Errorf("no A/AAAA response received for direct query to %s", hostFQDN)
	}

	supersetIPs := append(extractInterfaceIPs(ifaces), "198.51.100.254")
	conflictOwned, errProbeOwned := probeHostConflictOnLAN(context.Background(), hostFQDN, ifaces, supersetIPs)
	if errProbeOwned != nil || !conflictOwned {
		return fmt.Errorf("expected probeHostConflictOnLAN to detect conflict when responder already owns %s even for subset IPs (conflict=%v, err=%v)", hostFQDN, conflictOwned, errProbeOwned)
	}
	conflictFree, errProbeFree := probeHostConflictOnLAN(context.Background(), "cpa-unowned-"+hostFQDN, ifaces, supersetIPs)
	if errProbeFree != nil || conflictFree {
		return fmt.Errorf("expected probeHostConflictOnLAN not to report conflict for unowned host (conflict=%v, err=%v)", conflictFree, errProbeFree)
	}

	mConn, errMcast := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(224, 0, 0, 0), Port: 5353})
	if errMcast != nil {
		return errMcast
	}
	defer func() {
		if errClose := mConn.Close(); errClose != nil {
			t.Logf("close multicast goodbye conn: %v", errClose)
		}
	}()
	mpConn := ipv4.NewPacketConn(mConn)
	for i := range ifaces {
		_ = mpConn.JoinGroup(&ifaces[i], &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251)})
	}

	if errStop := stopAdv(); errStop != nil {
		return fmt.Errorf("stop advertiser: %w", errStop)
	}

	_ = mConn.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
	for {
		n, _, errRead := mConn.ReadFromUDP(buf)
		if errRead != nil {
			return fmt.Errorf("no TTL=0 A/AAAA goodbye record received for %s on Stop()", hostFQDN)
		}
		if hasHostAddressRecord(buf[:n], hostFQDN, true) {
			return nil
		}
	}
}

func hasHostAddressRecord(pkt []byte, hostFQDN string, wantZeroTTL bool) bool {
	var p dnsmessage.Parser
	hdr, err := p.Start(rewriteWireLabelChar(pkt, '.', dotInLabelSentinel))
	if err != nil || !hdr.Response {
		return false
	}
	if errSkipQ := p.SkipAllQuestions(); errSkipQ != nil {
		return false
	}
	checkSection := func(headers func() (dnsmessage.ResourceHeader, error), skip func() error) bool {
		for {
			rh, errH := headers()
			if errH != nil {
				return false
			}
			if errSkip := skip(); errSkip != nil {
				return false
			}
			if !strings.EqualFold(rh.Name.String(), hostFQDN) {
				continue
			}
			if rh.Type != dnsmessage.TypeA && rh.Type != dnsmessage.TypeAAAA {
				continue
			}
			if wantZeroTTL && rh.TTL == 0 {
				return true
			}
			if !wantZeroTTL && rh.TTL > 0 {
				return true
			}
		}
	}
	if checkSection(p.AnswerHeader, p.SkipAnswer) {
		return true
	}
	if errSkipAuth := p.SkipAllAuthorities(); errSkipAuth != nil {
		return false
	}
	return checkSection(p.AdditionalHeader, p.SkipAdditional)
}

type fakeZeroconfServer struct {
	mu        sync.Mutex
	shutdowns int
}

func (f *fakeZeroconfServer) Shutdown() {
	f.mu.Lock()
	f.shutdowns++
	f.mu.Unlock()
}

func (f *fakeZeroconfServer) shutdownCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shutdowns
}

func TestBuildAdvertisedHost(t *testing.T) {
	if got := buildAdvertisedHost("CPA-8F3B", "devbox.local."); got != "cpa-8f3b" {
		t.Fatalf("buildAdvertisedHost(CPA-8F3B) = %q, want cpa-8f3b", got)
	}
	if got := buildAdvertisedHost("office-8F3B", "devbox"); got != "cpa-office-8f3b" {
		t.Fatalf("buildAdvertisedHost(office-8F3B) = %q, want cpa-office-8f3b", got)
	}
	if got := buildAdvertisedHost("中文节点-8F3B", "devbox"); got != "cpa-8f3b" {
		t.Fatalf("buildAdvertisedHost(non-ASCII) = %q, want cpa-8f3b", got)
	}
	if got := buildAdvertisedHost("", "devbox"); got != "cpa-0001" {
		t.Fatalf("buildAdvertisedHost(empty) = %q, want cpa-0001", got)
	}
	if got := buildAdvertisedHost("CPA-8F3B", "cpa-8f3b.local"); got == "cpa-8f3b" || !strings.HasPrefix(got, "cpa-8f3b-") {
		t.Fatalf("expected collision avoidance when OS hostname matches advertised host, got %q", got)
	}

	longInstance := FormatInstanceName(strings.Repeat("a", 70), "8F3B")
	longHost := buildAdvertisedHost(longInstance, "devbox")
	if len(longHost) > maxDNSLabelBytes {
		t.Fatalf("len(longHost) = %d, exceeds %d", len(longHost), maxDNSLabelBytes)
	}
	if !strings.HasPrefix(longHost, "cpa-") || !strings.HasSuffix(longHost, "-8f3b") {
		t.Fatalf("longHost = %q, want cpa-...-8f3b", longHost)
	}
}

func TestExtractInterfaceAddrs_PrefersGlobalIPv6OverLinkLocal(t *testing.T) {
	withGlobal := []net.Addr{
		&net.IPNet{IP: net.ParseIP("192.0.2.10"), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("2001:db8::10"), Mask: net.CIDRMask(64, 128)},
		&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)},
		&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
	}
	gotGlobal := extractInterfaceAddrs(withGlobal)
	if len(gotGlobal) != 2 || gotGlobal[0] != "192.0.2.10" || gotGlobal[1] != "2001:db8::10" {
		t.Fatalf("extractInterfaceAddrs(withGlobal) = %v, want [192.0.2.10 2001:db8::10]", gotGlobal)
	}

	linkLocalOnly := []net.Addr{
		&net.IPNet{IP: net.ParseIP("192.0.2.10"), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)},
	}
	gotLinkLocal := extractInterfaceAddrs(linkLocalOnly)
	if len(gotLinkLocal) != 2 || gotLinkLocal[0] != "192.0.2.10" || gotLinkLocal[1] != "fe80::1" {
		t.Fatalf("extractInterfaceAddrs(linkLocalOnly) = %v, want [192.0.2.10 fe80::1]", gotLinkLocal)
	}
}

func TestZeroconfAdvertiser_Start_ProxyHostAndPerInterfaceIPs(t *testing.T) {
	ifaces := []net.Interface{
		{Index: 1, Name: "eth0"},
		{Index: 2, Name: "wlan0"},
	}
	allIPs := []string{"192.0.2.10", "198.51.100.20"}

	var gotHost string
	var gotIPs []string
	srv := &fakeZeroconfServer{}

	adv := NewZeroconfAdvertiser()
	adv.osHostname = func() (string, error) { return "devbox.local", nil }
	adv.interfaceIPs = func([]net.Interface) []string { return append([]string(nil), allIPs...) }
	adv.registerProxy = func(instance, service, domain string, port int, host string, ips []string, text []string, regIfaces []net.Interface, opts ...zeroconf.ServerOption) (zeroconfServer, error) {
		gotHost = host
		gotIPs = append([]string(nil), ips...)
		return srv, nil
	}

	err := adv.Start(context.Background(), ServiceSpec{
		InstanceName:  "CPA-8F3B",
		ServiceType:   DefaultServiceType,
		Domain:        DefaultDomain,
		Port:          8317,
		Interfaces:    ifaces,
		AdvertisedIPs: allIPs,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if gotHost != "cpa-8f3b" {
		t.Fatalf("registered host = %q, want cpa-8f3b (not OS hostname devbox)", gotHost)
	}
	if supportsPerInterfaceAddrs(runtime.GOOS) {
		if len(gotIPs) != 0 {
			t.Fatalf("Start() registered IPs on %s = %v, want empty slice for per-interface emission", runtime.GOOS, gotIPs)
		}
	} else if len(gotIPs) != 2 || gotIPs[0] != "192.0.2.10" || gotIPs[1] != "198.51.100.20" {
		t.Fatalf("Start() registered IPs on %s = %v, want %v", runtime.GOOS, gotIPs, allIPs)
	}
	perIfaceIPs, errResolve := resolveRegisterProxyIPs(ifaces, allIPs, "linux", adv.interfaceIPs)
	if errResolve != nil || len(perIfaceIPs) != 0 {
		t.Fatalf("resolveRegisterProxyIPs(multi-homed unrestricted, linux) = %v, %v; want nil, nil", perIfaceIPs, errResolve)
	}
	winIPs, errWin := resolveRegisterProxyIPs(ifaces, allIPs, "windows", adv.interfaceIPs)
	if errWin != nil || len(winIPs) != 2 || winIPs[0] != "192.0.2.10" || winIPs[1] != "198.51.100.20" {
		t.Fatalf("resolveRegisterProxyIPs(multi-homed unrestricted, windows) = %v, %v; want %v, nil", winIPs, errWin, allIPs)
	}
	boundMultiIPs, errBoundMulti := resolveRegisterProxyIPs(ifaces, []string{"192.0.2.10"}, "linux", adv.interfaceIPs)
	if errBoundMulti != nil || len(boundMultiIPs) != 1 || boundMultiIPs[0] != "192.0.2.10" {
		t.Fatalf("resolveRegisterProxyIPs(bound subset, linux) = %v, %v; want [192.0.2.10], nil", boundMultiIPs, errBoundMulti)
	}
	if errStop := adv.Stop(); errStop != nil {
		t.Fatalf("Stop() error = %v", errStop)
	}
	if srv.shutdowns != 1 {
		t.Fatalf("shutdowns = %d, want 1", srv.shutdowns)
	}

	// When a bind-host restriction narrows AdvertisedIPs, the restricted IP list must be preserved.
	advBound := NewZeroconfAdvertiser()
	advBound.osHostname = func() (string, error) { return "devbox.local", nil }
	advBound.interfaceIPs = func([]net.Interface) []string { return append([]string(nil), allIPs...) }
	advBound.registerProxy = func(instance, service, domain string, port int, host string, ips []string, text []string, regIfaces []net.Interface, opts ...zeroconf.ServerOption) (zeroconfServer, error) {
		gotHost = host
		gotIPs = append([]string(nil), ips...)
		return &fakeZeroconfServer{}, nil
	}
	if errBound := advBound.Start(context.Background(), ServiceSpec{
		InstanceName:  "CPA-8F3B",
		ServiceType:   DefaultServiceType,
		Domain:        DefaultDomain,
		Port:          8317,
		Interfaces:    ifaces[:1],
		AdvertisedIPs: []string{"192.0.2.10"},
	}); errBound != nil {
		t.Fatalf("Start(bound) error = %v", errBound)
	}
	if len(gotIPs) != 1 || gotIPs[0] != "192.0.2.10" {
		t.Fatalf("bound registered IPs = %v, want [192.0.2.10]", gotIPs)
	}
	_ = advBound.Stop()
}

func TestZeroconfAdvertiser_Start_RegisterFailureDoesNotPanic(t *testing.T) {
	adv := NewZeroconfAdvertiser()
	adv.probeHostConflict = func(context.Context, string, []net.Interface, []string) (bool, error) {
		return false, nil
	}
	err := adv.Start(context.Background(), ServiceSpec{
		InstanceName:  "CPA-8F3B",
		ServiceType:   DefaultServiceType,
		Domain:        DefaultDomain,
		Port:          8317,
		Interfaces:    []net.Interface{{Index: 1, Name: "eth0"}},
		AdvertisedIPs: []string{"invalid-ip"},
	})
	if err == nil {
		_ = adv.Stop()
		t.Fatal("expected error when RegisterProxy fails on invalid IP, got nil")
	}
	if !strings.Contains(err.Error(), "failed to register primary service") {
		t.Fatalf("expected error from RegisterProxy path, got: %v", err)
	}
}

func TestBuildHostAddressReplyAndGoodbyePacket(t *testing.T) {
	hostFQDN := "cpa-8f3b.local."
	ips := []string{"192.0.2.10", "2001:db8::10"}

	makeQuery := func(name string, qtype dnsmessage.Type, unicast bool) []byte {
		qName, err := dnsmessage.NewName(name)
		if err != nil {
			t.Fatalf("NewName(%q): %v", name, err)
		}
		qClass := dnsmessage.ClassINET
		if unicast {
			qClass |= dnsmessage.Class(mdnsCacheFlushBit)
		}
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 42})
		_ = b.StartQuestions()
		_ = b.Question(dnsmessage.Question{Name: qName, Type: qtype, Class: qClass})
		out, err := b.Finish()
		if err != nil {
			t.Fatalf("Finish(): %v", err)
		}
		return out
	}

	replyA, unicastA := buildHostAddressReply(makeQuery(hostFQDN, dnsmessage.TypeA, true), hostFQDN, ips, false)
	if !unicastA || !hasHostAddressRecord(replyA, hostFQDN, false) {
		t.Fatalf("expected unicast A reply for %s", hostFQDN)
	}
	var stdParser dnsmessage.Parser
	if _, err := stdParser.Start(replyA); err != nil {
		t.Fatalf("stdParser.Start: %v", err)
	}
	stdQs, _ := stdParser.AllQuestions()
	if len(stdQs) != 0 {
		t.Fatalf("standard mDNS reply must not contain questions, got %d", len(stdQs))
	}
	stdAnsHdr, errStdAns := stdParser.AnswerHeader()
	if errStdAns != nil || uint16(stdAnsHdr.Class)&mdnsCacheFlushBit == 0 {
		t.Fatalf("expected cache-flush bit on standard mDNS answer, got class %v (err %v)", stdAnsHdr.Class, errStdAns)
	}

	replyLegacy, unicastLegacy := buildHostAddressReply(makeQuery(hostFQDN, dnsmessage.TypeA, false), hostFQDN, ips, true)
	if !unicastLegacy || !hasHostAddressRecord(replyLegacy, hostFQDN, false) {
		t.Fatalf("expected unicast reply for legacy query without QU bit")
	}
	var legParser dnsmessage.Parser
	if _, err := legParser.Start(replyLegacy); err != nil {
		t.Fatalf("legParser.Start: %v", err)
	}
	legQs, _ := legParser.AllQuestions()
	if len(legQs) != 1 {
		t.Fatalf("legacy unicast reply must echo question, got %d questions", len(legQs))
	}
	legAnsHdr, errLegAns := legParser.AnswerHeader()
	if errLegAns != nil || uint16(legAnsHdr.Class)&mdnsCacheFlushBit != 0 {
		t.Fatalf("legacy unicast answer must not set cache-flush bit, got class %v (err %v)", legAnsHdr.Class, errLegAns)
	}

	replyAAAA, unicastAAAA := buildHostAddressReply(makeQuery(hostFQDN, dnsmessage.TypeAAAA, false), hostFQDN, ips, false)
	if unicastAAAA || !hasHostAddressRecord(replyAAAA, hostFQDN, false) {
		t.Fatalf("expected multicast AAAA reply for %s", hostFQDN)
	}

	// RFC 6762 §6.1: IPv4-only service queried for AAAA (or IPv6-only queried for A) must return NSEC + complementary family in Additionals.
	nsecAAAA, _ := buildHostAddressReply(makeQuery(hostFQDN, dnsmessage.TypeAAAA, false), hostFQDN, []string{"192.0.2.10"}, false)
	if !hasHostAddressRecord(nsecAAAA, hostFQDN, false) {
		t.Fatal("expected IPv4-only AAAA query reply to include NSEC answer and Additional A record")
	}
	var nsecParser dnsmessage.Parser
	if _, errN := nsecParser.Start(nsecAAAA); errN != nil || nsecParser.SkipAllQuestions() != nil {
		t.Fatalf("nsecParser.Start: %v", errN)
	}
	nsecHdr, errNHdr := nsecParser.AnswerHeader()
	if errNHdr != nil || nsecHdr.Type != dnsTypeNSEC {
		t.Fatalf("expected NSEC answer header, got type=%v err=%v", nsecHdr.Type, errNHdr)
	}
	nsecRes, errNRes := nsecParser.UnknownResource()
	if errNRes != nil || !bytes.HasSuffix(nsecRes.Data, []byte{0x00, 0x01, 0x40}) {
		t.Fatalf("expected IPv4-only NSEC bitmap 00 01 40 (no trailing zero bytes), got %x (err %v)", nsecRes.Data, errNRes)
	}

	nsecA, _ := buildHostAddressReply(makeQuery(hostFQDN, dnsmessage.TypeA, false), hostFQDN, []string{"2001:db8::10"}, false)
	if !hasHostAddressRecord(nsecA, hostFQDN, false) {
		t.Fatal("expected IPv6-only A query reply to include NSEC answer and Additional AAAA record")
	}
	var nsecParser6 dnsmessage.Parser
	if _, errN6 := nsecParser6.Start(nsecA); errN6 != nil || nsecParser6.SkipAllQuestions() != nil {
		t.Fatalf("nsecParser6.Start: %v", errN6)
	}
	if _, errH6 := nsecParser6.AnswerHeader(); errH6 != nil {
		t.Fatalf("nsecParser6.AnswerHeader: %v", errH6)
	}
	nsecRes6, errNRes6 := nsecParser6.UnknownResource()
	if errNRes6 != nil || !bytes.HasSuffix(nsecRes6.Data, []byte{0x00, 0x04, 0x00, 0x00, 0x00, 0x08}) {
		t.Fatalf("expected IPv6-only NSEC bitmap 00 04 00 00 00 08, got %x (err %v)", nsecRes6.Data, errNRes6)
	}

	if otherReply, _ := buildHostAddressReply(makeQuery("other.local.", dnsmessage.TypeA, false), hostFQDN, ips, false); len(otherReply) != 0 {
		t.Fatalf("expected no reply for unrelated hostname, got %d bytes", len(otherReply))
	}

	// RFC 6762 probe packets include proposed records in the Authority section; responder must still answer to defend its host name.
	qName, _ := dnsmessage.NewName(hostFQDN)
	probeBuilder := dnsmessage.NewBuilder(nil, dnsmessage.Header{})
	_ = probeBuilder.StartQuestions()
	_ = probeBuilder.Question(dnsmessage.Question{Name: qName, Type: dnsmessage.TypeALL, Class: dnsmessage.ClassINET})
	_ = probeBuilder.StartAuthorities()
	_ = probeBuilder.AResource(dnsmessage.ResourceHeader{
		Name:  qName,
		Type:  dnsmessage.TypeA,
		Class: dnsmessage.ClassINET,
		TTL:   120,
	}, dnsmessage.AResource{A: [4]byte{198, 51, 100, 99}})
	probePkt, errProbePkt := probeBuilder.Finish()
	if errProbePkt != nil {
		t.Fatalf("probeBuilder.Finish(): %v", errProbePkt)
	}
	probeReply, _ := buildHostAddressReply(probePkt, hostFQDN, ips, false)
	if !hasHostAddressRecord(probeReply, hostFQDN, false) {
		t.Fatalf("expected responder to defend %s against probe carrying Authority records", hostFQDN)
	}

	goodbye := buildHostGoodbyePacket(hostFQDN, ips)
	if !hasHostAddressRecord(goodbye, hostFQDN, true) {
		t.Fatalf("expected TTL=0 goodbye records for %s", hostFQDN)
	}
}

func TestResolveAdvertisedHost_CollisionStepping(t *testing.T) {
	ifaces := []net.Interface{{Index: 1, Name: "eth0"}}
	localIPs := []string{"192.0.2.10"}

	var gotHost string
	adv := NewZeroconfAdvertiser()
	adv.osHostname = func() (string, error) { return "devbox.local", nil }
	adv.interfaceIPs = func([]net.Interface) []string { return localIPs }
	adv.probeHostConflict = func(_ context.Context, hostFQDN string, _ []net.Interface, _ []string) (bool, error) {
		return hostFQDN == "cpa-8f3b.local." || hostFQDN == "cpa-8f3b-2.local.", nil
	}
	adv.registerProxy = func(instance, service, domain string, port int, host string, ips []string, text []string, regIfaces []net.Interface, opts ...zeroconf.ServerOption) (zeroconfServer, error) {
		gotHost = host
		return &fakeZeroconfServer{}, nil
	}

	if err := adv.Start(context.Background(), ServiceSpec{
		InstanceName:  "CPA-8F3B",
		ServiceType:   DefaultServiceType,
		Domain:        DefaultDomain,
		Port:          8317,
		Interfaces:    ifaces,
		AdvertisedIPs: localIPs,
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() {
		_ = adv.Stop()
	}()
	if gotHost != "cpa-8f3b-3" {
		t.Fatalf("registered host after collisions = %q, want cpa-8f3b-3", gotHost)
	}

	// Candidate matching OS hostname on step-2 must be skipped.
	skippedHost, errSkip := resolveAdvertisedHost(
		context.Background(),
		"CPA-8F3B",
		"cpa-8f3b-2.local",
		DefaultDomain,
		ifaces,
		localIPs,
		func(_ context.Context, hostFQDN string, _ []net.Interface, _ []string) (bool, error) {
			return hostFQDN == "cpa-8f3b.local.", nil
		},
	)
	if errSkip != nil || skippedHost != "cpa-8f3b-3" {
		t.Fatalf("resolveAdvertisedHost(skip OS hostname) = %q, %v; want cpa-8f3b-3, nil", skippedHost, errSkip)
	}

	// Exhausting all collision attempts must return an error instead of using an unprobed fallback.
	if exhausted, errExhausted := resolveAdvertisedHost(
		context.Background(),
		"CPA-8F3B",
		"devbox.local",
		DefaultDomain,
		ifaces,
		localIPs,
		func(_ context.Context, _ string, _ []net.Interface, _ []string) (bool, error) {
			return true, nil
		},
	); errExhausted == nil {
		t.Fatalf("expected error when all collision attempts are exhausted, got host=%q", exhausted)
	}

	// Probe send failure on invalid interfaces (including IPv6-only or partial interface failure) must return an error.
	if _, errV6Fail := probeHostConflictOnLAN(
		context.Background(),
		"cpa-8f3b.local.",
		[]net.Interface{{Index: -1, Name: "invalid0"}},
		[]string{"2001:db8::10"},
	); errV6Fail == nil {
		t.Fatal("expected error when IPv6-only probe cannot be sent on invalid interface")
	}
	if realIfaces, _ := FilterInterfaces(nil, nil); len(realIfaces) > 0 {
		mixedIfaces := []net.Interface{realIfaces[0], {Index: -1, Name: "invalid0"}}
		if _, errPartial := probeHostConflictOnLAN(
			context.Background(),
			"cpa-8f3b.local.",
			mixedIfaces,
			[]string{"192.0.2.10"},
		); errPartial == nil {
			t.Fatal("expected error when probe fails on one of the selected interfaces")
		}
		shortCtx, shortCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer shortCancel()
		if _, errShort := probeHostConflictOnLAN(
			shortCtx,
			"cpa-8f3b.local.",
			realIfaces[:1],
			[]string{"192.0.2.10"},
		); errShort != context.DeadlineExceeded {
			t.Fatalf("expected context.DeadlineExceeded when deadline is shorter than full probe schedule, got %v", errShort)
		}
	}

	// Cross-family probe conflict: an IPv6 authority probe (TypeAAAA = 28) is lexicographically larger than
	// an IPv4-only local proposal (TypeA = 1), so the IPv4-only proposer must detect the conflict and step.
	peerV6ProbePkt, errPeerV6 := buildHostProbeQuery("cpa-8f3b.local.", 200, []string{"2001:db8::10"})
	if errPeerV6 != nil {
		t.Fatalf("buildHostProbeQuery(v6): %v", errPeerV6)
	}
	if !isHostProbeConflict(peerV6ProbePkt, "cpa-8f3b.local.", []string{"192.0.2.10"}, 50000, 50001) {
		t.Fatal("expected IPv4-only proposer to detect conflict against peer IPv6 authority probe")
	}

	// Deterministic check: when both conflictCh and doneCh are ready simultaneously,
	// awaitProbeConflictResult must always report the detected conflict.
	for i := 0; i < 100; i++ {
		conflictCh := make(chan bool, 1)
		conflictCh <- true
		doneCh := make(chan struct{})
		close(doneCh)
		var detected atomic.Bool
		detected.Store(true)
		gotConflict, errAwait := awaitProbeConflictResult(conflictCh, doneCh, &detected, context.Background(), nil)
		if errAwait != nil || !gotConflict {
			t.Fatalf("iteration %d: awaitProbeConflictResult = %v, %v; want true, nil", i, gotConflict, errAwait)
		}
	}

	// Simultaneous probe tie-breaking (RFC 6762 §8.2): lexicographically smaller RDATA loses while larger RDATA wins,
	// even if DNS Header.ID happens to match; equal RDATA from different ports breaks ties by port; loopback is filtered by exact packet + source port.
	peerProbePkt, errPeerPkt := buildHostProbeQuery("cpa-8f3b.local.", 200, []string{"192.0.2.20"})
	if errPeerPkt != nil {
		t.Fatalf("buildHostProbeQuery: %v", errPeerPkt)
	}
	if !isHostProbeConflict(peerProbePkt, "cpa-8f3b.local.", []string{"192.0.2.10"}, 50000, 50001) {
		t.Fatal("expected smaller RDATA (192.0.2.10 < 192.0.2.20) to lose tie-break even when DNS ID matches")
	}
	if isHostProbeConflict(peerProbePkt, "cpa-8f3b.local.", []string{"192.0.2.30"}, 50000, 50001) {
		t.Fatal("expected larger RDATA (192.0.2.30 > 192.0.2.20) to win tie-break and not report conflict")
	}
	if !isHostProbeConflict(peerProbePkt, "cpa-8f3b.local.", []string{"192.0.2.20"}, 50000, 50001) {
		t.Fatal("expected equal RDATA with lower source port (50000 < 50001) to lose tie-break")
	}
	if isHostProbeConflict(peerProbePkt, "cpa-8f3b.local.", []string{"192.0.2.20"}, 50002, 50001) {
		t.Fatal("expected equal RDATA with higher source port (50002 > 50001) to win tie-break")
	}
	if isOwnLoopbackProbe(peerProbePkt, []byte("different-query"), &net.UDPAddr{IP: net.IPv4(192, 0, 2, 10), Port: 50000}, map[int]struct{}{50000: {}}, ifaces) {
		t.Fatal("expected packet with different payload not to be treated as own loopback probe")
	}

	// Multi-probe schedule (RFC 6762 §8.1): must send 3 probes and detect conflict even if first probe is lost and response arrives on probe 2.
	sendCount := 0
	conflictCh2 := make(chan bool, 1)
	doneCh2 := make(chan struct{})
	var detected2 atomic.Bool
	gotScheduleConflict, errSched := runHostProbeSchedule(
		context.Background(),
		mdnsHostProbeCount,
		time.Millisecond,
		func() error {
			sendCount++
			if sendCount == 2 {
				detected2.Store(true)
				conflictCh2 <- true
			}
			return nil
		},
		conflictCh2,
		doneCh2,
		&detected2,
		func() {
			select {
			case <-doneCh2:
			default:
				close(doneCh2)
			}
		},
	)
	if errSched != nil || !gotScheduleConflict || sendCount != 2 {
		t.Fatalf("runHostProbeSchedule(conflict on 2nd probe) = conflict=%v err=%v sends=%d; want true, nil, 2", gotScheduleConflict, errSched, sendCount)
	}

	sendCountAll := 0
	conflictCh3 := make(chan bool, 1)
	doneCh3 := make(chan struct{})
	var detected3 atomic.Bool
	gotNoConflict, errSchedAll := runHostProbeSchedule(
		context.Background(),
		mdnsHostProbeCount,
		time.Millisecond,
		func() error {
			sendCountAll++
			return nil
		},
		conflictCh3,
		doneCh3,
		&detected3,
		func() {
			select {
			case <-doneCh3:
			default:
				close(doneCh3)
			}
		},
	)
	if errSchedAll != nil || gotNoConflict || sendCountAll != mdnsHostProbeCount {
		t.Fatalf("runHostProbeSchedule(no conflict) = conflict=%v err=%v sends=%d; want false, nil, %d", gotNoConflict, errSchedAll, sendCountAll, mdnsHostProbeCount)
	}

	// Premature reader exit before all probe intervals complete must return an error rather than false-negative "no conflict".
	prematureDoneCh := make(chan struct{})
	close(prematureDoneCh)
	var detectedPremature atomic.Bool
	if _, errPremature := runHostProbeSchedule(
		context.Background(),
		mdnsHostProbeCount,
		time.Second,
		func() error { return nil },
		make(chan bool, 1),
		prematureDoneCh,
		&detectedPremature,
		nil,
	); errPremature == nil {
		t.Fatal("expected error when probe readers terminate prematurely before probe window completes")
	}

	// Runtime conflict resolution: receiving a conflicting mDNS response for our active hostFQDN
	// must skip OS hostname candidates, probe replacement candidates, and rebind only after success.
	var reboundHosts []string
	var rebindMu sync.Mutex
	oldInner := &fakeZeroconfServer{}
	newInner := &fakeZeroconfServer{}
	responder := &hostRecordResponder{
		inner:    oldInner,
		baseHost: "cpa-8f3b",
		osHost:   "cpa-8f3b-2.local",
		domain:   DefaultDomain,
		attempt:  1,
		hostFQDN: "cpa-8f3b.local.",
		fixedIPs: []string{"192.0.2.10"},
		ifaces:   ifaces,
		probeConflict: func(_ context.Context, fqdn string, _ []net.Interface, _ []string) (bool, error) {
			return fqdn == "cpa-8f3b-3.local.", nil
		},
		rebindInner: func(nextHost string) (zeroconfServer, error) {
			rebindMu.Lock()
			reboundHosts = append(reboundHosts, nextHost)
			rebindMu.Unlock()
			return newInner, nil
		},
		stopCh: make(chan struct{}),
	}
	qName, _ := dnsmessage.NewName("cpa-8f3b.local.")
	conflictBuilder := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true, Authoritative: true})
	_ = conflictBuilder.StartAnswers()
	_ = conflictBuilder.AResource(dnsmessage.ResourceHeader{
		Name:  qName,
		Type:  dnsmessage.TypeA,
		Class: dnsmessage.ClassINET | dnsmessage.Class(mdnsCacheFlushBit),
		TTL:   120,
	}, dnsmessage.AResource{A: [4]byte{192, 0, 2, 77}})
	conflictRespPkt, errConflictPkt := conflictBuilder.Finish()
	if errConflictPkt != nil {
		t.Fatalf("conflictBuilder.Finish(): %v", errConflictPkt)
	}
	onLinkPeer := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 77), Port: mdnsPort}

	// Off-link or non-5353 port responses must be ignored per RFC 6762 §11 and not trigger conflict rebinding.
	responder.handlePacket(conflictRespPkt, 1, &net.UDPAddr{IP: net.IPv4(203, 0, 113, 77), Port: mdnsPort})
	responder.handlePacket(conflictRespPkt, 1, &net.UDPAddr{IP: net.IPv4(192, 0, 2, 77), Port: 54321})
	if responder.currentHostFQDN() != "cpa-8f3b.local." || len(reboundHosts) != 0 {
		t.Fatalf("off-link/non-5353 response should not trigger conflict: hostFQDN=%q rebound=%v", responder.currentHostFQDN(), reboundHosts)
	}

	responder.handlePacket(conflictRespPkt, 1, onLinkPeer)
	if responder.currentHostFQDN() != "cpa-8f3b-4.local." || len(reboundHosts) != 1 || reboundHosts[0] != "cpa-8f3b-4" {
		t.Fatalf("after runtime conflict: hostFQDN=%q reboundHosts=%v, want cpa-8f3b-4.local. / [cpa-8f3b-4]", responder.currentHostFQDN(), reboundHosts)
	}
	if oldInner.shutdownCount() != 1 {
		t.Fatalf("oldInner shutdowns = %d, want 1", oldInner.shutdownCount())
	}

	// Non-address records (such as CNAME) for the target host must also be treated as conflicts per RFC 6762 §8.1.
	cnameTarget, _ := dnsmessage.NewName("alias-target.local.")
	cnameBuilder := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true, Authoritative: true})
	_ = cnameBuilder.StartAnswers()
	_ = cnameBuilder.CNAMEResource(dnsmessage.ResourceHeader{
		Name:  qName,
		Type:  dnsmessage.TypeCNAME,
		Class: dnsmessage.ClassINET,
		TTL:   120,
	}, dnsmessage.CNAMEResource{CNAME: cnameTarget})
	cnamePkt, errCNAME := cnameBuilder.Finish()
	if errCNAME != nil {
		t.Fatalf("cnameBuilder.Finish(): %v", errCNAME)
	}
	if !isHostProbeConflict(cnamePkt, "cpa-8f3b.local.", []string{"192.0.2.10"}, 50000, mdnsPort) {
		t.Fatal("expected CNAME response for target host to be detected as conflict")
	}

	// If rebind fails or replacement attempts are exhausted, responder must withdraw the conflicted host and shut down the old server.
	failOldInner := &fakeZeroconfServer{}
	failResponder := &hostRecordResponder{
		inner:    failOldInner,
		baseHost: "cpa-8f3b",
		osHost:   "devbox",
		domain:   DefaultDomain,
		attempt:  1,
		hostFQDN: "cpa-8f3b.local.",
		fixedIPs: []string{"192.0.2.10"},
		ifaces:   ifaces,
		probeConflict: func(context.Context, string, []net.Interface, []string) (bool, error) {
			return false, nil
		},
		rebindInner: func(string) (zeroconfServer, error) {
			return nil, fmt.Errorf("simulated rebind failure")
		},
		stopCh: make(chan struct{}),
	}
	failResponder.handlePacket(conflictRespPkt, 1, onLinkPeer)
	if failResponder.currentHostFQDN() != "" || failOldInner.shutdownCount() != 1 {
		t.Fatalf("after failed rebind: hostFQDN=%q oldShutdowns=%d, want empty (withdrawn) / 1", failResponder.currentHostFQDN(), failOldInner.shutdownCount())
	}

	exhaustOldInner := &fakeZeroconfServer{}
	exhaustResponder := &hostRecordResponder{
		inner:    exhaustOldInner,
		baseHost: "cpa-8f3b",
		osHost:   "devbox",
		domain:   DefaultDomain,
		attempt:  maxHostCollisionAttempts,
		hostFQDN: "cpa-8f3b.local.",
		fixedIPs: []string{"192.0.2.10"},
		ifaces:   ifaces,
		stopCh:   make(chan struct{}),
	}
	exhaustResponder.handlePacket(conflictRespPkt, 1, onLinkPeer)
	if exhaustResponder.currentHostFQDN() != "" || exhaustOldInner.shutdownCount() != 1 {
		t.Fatalf("after exhausted attempts: hostFQDN=%q oldShutdowns=%d, want empty (withdrawn) / 1", exhaustResponder.currentHostFQDN(), exhaustOldInner.shutdownCount())
	}

	// Concurrent conflict packets for the same observed FQDN must not leak servers or double-rebind.
	concOldInner := &fakeZeroconfServer{}
	enteredRebind := make(chan struct{})
	releaseRebind := make(chan struct{})
	var createdServers []*fakeZeroconfServer
	var concMu sync.Mutex
	concResponder := &hostRecordResponder{
		inner:    concOldInner,
		baseHost: "cpa-8f3b",
		osHost:   "devbox",
		domain:   DefaultDomain,
		attempt:  1,
		hostFQDN: "cpa-8f3b.local.",
		fixedIPs: []string{"192.0.2.10"},
		ifaces:   ifaces,
		probeConflict: func(context.Context, string, []net.Interface, []string) (bool, error) {
			return false, nil
		},
		rebindInner: func(string) (zeroconfServer, error) {
			select {
			case <-enteredRebind:
			default:
				close(enteredRebind)
			}
			<-releaseRebind
			s := &fakeZeroconfServer{}
			concMu.Lock()
			createdServers = append(createdServers, s)
			concMu.Unlock()
			return s, nil
		},
		stopCh: make(chan struct{}),
	}
	var concWG sync.WaitGroup
	concWG.Add(2)
	go func() {
		defer concWG.Done()
		concResponder.handlePacket(conflictRespPkt, 1, onLinkPeer)
	}()
	<-enteredRebind
	go func() {
		defer concWG.Done()
		concResponder.handlePacket(conflictRespPkt, 1, onLinkPeer)
	}()
	close(releaseRebind)
	concWG.Wait()
	if len(createdServers) != 1 || concOldInner.shutdownCount() != 1 || createdServers[0].shutdownCount() != 0 {
		t.Fatalf("concurrent conflict: created=%d oldShutdowns=%d newShutdowns=%d", len(createdServers), concOldInner.shutdownCount(), createdServers[0].shutdownCount())
	}

	// Malformed/truncated DNS records must terminate immediately without spinning.
	truncatedAns := conflictRespPkt[:len(conflictRespPkt)-2]
	if got := extractHostAnswerIPs(truncatedAns, "cpa-8f3b.local."); len(got) != 0 {
		t.Fatalf("expected empty IPs on truncated answer packet, got %v", got)
	}
	truncatedAuth := peerProbePkt[:len(peerProbePkt)-2]
	if _, gotAuth, _ := extractHostAuthorityIPs(truncatedAuth, "cpa-8f3b.local."); len(gotAuth) != 0 {
		t.Fatalf("expected empty IPs on truncated authority packet, got %v", gotAuth)
	}

	// When fixedIPs is restricted (bound host), hostRecordResponder must not build extra goodbye records
	// from raw interface addresses (which could revoke other addresses on the same interface).
	if realIfaces, _ := FilterInterfaces(nil, nil); len(realIfaces) > 0 {
		boundResp := &hostRecordResponder{
			hostFQDN: "cpa-8f3b.local.",
			fixedIPs: []string{"192.0.2.10"},
			ifaces:   realIfaces[:1],
		}
		pkts := boundResp.goodbyePacketsForHost("cpa-8f3b.local.")
		for _, pkt := range pkts {
			var gp dnsmessage.Parser
			if _, errG := gp.Start(pkt); errG != nil {
				t.Fatalf("gp.Start: %v", errG)
			}
			_ = gp.SkipAllQuestions()
			for {
				rh, errH := gp.AnswerHeader()
				if errH != nil {
					break
				}
				if rh.Type == dnsmessage.TypeA {
					r, _ := gp.AResource()
					if net.IP(r.A[:]).String() != "192.0.2.10" {
						t.Fatalf("unexpected goodbye A record %s; want only 192.0.2.10", net.IP(r.A[:]).String())
					}
				} else if rh.Type == dnsmessage.TypeAAAA {
					t.Fatal("unexpected goodbye AAAA record when fixedIPs is IPv4-only")
				} else {
					_ = gp.SkipAnswer()
				}
			}
		}
	}

	// Off-link queries must not be answered; unified service responder must answer SRV/TXT/PTR queries directly
	// and preserve dots inside single DNS-SD instance labels on the wire (e.g. "\x0eoffice.v1-8F3B").
	dotCfg := parseMDNSServiceConfig("office.v1-8F3B", DefaultServiceType, DefaultDomain, 8317, []string{"product=cliproxyapi"})
	dotAnnPkt := buildServicePacket(dotCfg, "cpa-office-v1-8f3b.local.", []string{"192.0.2.10"}, mdnsServiceRecordTTL)
	if !bytes.Contains(dotAnnPkt, []byte("\x0eoffice.v1-8F3B")) {
		t.Fatalf("expected single 14-byte wire label for office.v1-8F3B in announcement packet")
	}
	if !isValidProbePeer(&net.UDPAddr{IP: net.IPv4(198, 51, 100, 77), Port: mdnsPort}, 1, ifaces, localIPs, false, true) {
		t.Fatal("expected probe reply arriving on allowed interface ifIndex=1 from port 5353 to be accepted across subnets")
	}
	if isValidProbePeer(&net.UDPAddr{IP: net.IPv4(198, 51, 100, 77), Port: mdnsPort}, 99, ifaces, localIPs, false, true) {
		t.Fatal("expected unicast probe reply on disallowed interface and foreign subnet to be rejected")
	}
	svcCfg := parseMDNSServiceConfig("CPA-8F3B", DefaultServiceType+","+SubtypeResponses, DefaultDomain, 8317, []string{"product=cliproxyapi"})

	// A browse query carrying a Known-Answer PTR record whose target has a literal dot inside a single label
	// ("\x0eoffice.v1-1234") must not fail parsing and must still return our CPA-8F3B browse answer.
	svcQName, _ := dnsmessage.NewName("_ai-gateway._tcp.local.")
	knownDotTarget, _ := dnsmessage.NewName("office" + string(dotInLabelSentinel) + "v1-1234._ai-gateway._tcp.local.")
	browseWithDotBuilder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 76})
	_ = browseWithDotBuilder.StartQuestions()
	_ = browseWithDotBuilder.Question(dnsmessage.Question{Name: svcQName, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET})
	_ = browseWithDotBuilder.StartAnswers()
	_ = browseWithDotBuilder.PTRResource(dnsmessage.ResourceHeader{
		Name:  svcQName,
		Type:  dnsmessage.TypePTR,
		Class: dnsmessage.ClassINET,
		TTL:   mdnsServiceRecordTTL,
	}, dnsmessage.PTRResource{PTR: knownDotTarget})
	rawBrowseWithDot, errBrowseDot := browseWithDotBuilder.Finish()
	if errBrowseDot != nil {
		t.Fatalf("browseWithDotBuilder.Finish(): %v", errBrowseDot)
	}
	wireBrowseWithDot := rewriteWireLabelChar(rawBrowseWithDot, dotInLabelSentinel, '.')
	if !bytes.Contains(wireBrowseWithDot, []byte("\x0eoffice.v1-1234")) {
		t.Fatal("expected single-label dotted PTR target in test query wire packet")
	}
	browseDotReply, _ := buildServiceQueryReply(wireBrowseWithDot, svcCfg, "cpa-8f3b.local.", []string{"192.0.2.10"}, true)
	if !hasHostAddressRecord(browseDotReply, "cpa-8f3b.local.", false) {
		t.Fatal("expected browse query with dotted foreign Known-Answer PTR to still return CPA-8F3B answer")
	}
	otherDotCfg := parseMDNSServiceConfig("office.v1-1234", DefaultServiceType, DefaultDomain, 8317, []string{"product=cliproxyapi"})
	if suppressedReply, _ := buildServiceQueryReply(wireBrowseWithDot, otherDotCfg, "cpa-office-v1-1234.local.", []string{"192.0.2.10"}, true); len(suppressedReply) != 0 {
		t.Fatal("expected Known-Answer Suppression to suppress matching dotted instance PTR")
	}
	instQName, _ := dnsmessage.NewName("CPA-8F3B._ai-gateway._tcp.local.")
	instQBuilder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 77})
	_ = instQBuilder.StartQuestions()
	_ = instQBuilder.Question(dnsmessage.Question{Name: instQName, Type: dnsmessage.TypeSRV, Class: dnsmessage.ClassINET})
	instQueryPkt, errInstQ := instQBuilder.Finish()
	if errInstQ != nil {
		t.Fatalf("instQBuilder.Finish(): %v", errInstQ)
	}
	svcReply, svcUnicast := buildServiceQueryReply(instQueryPkt, svcCfg, "cpa-8f3b.local.", []string{"192.0.2.10"}, true)
	if !svcUnicast || !hasHostAddressRecord(svcReply, "cpa-8f3b.local.", false) {
		t.Fatal("expected unified service query reply to include SRV and host address records")
	}
	if isOnLinkSource(&net.UDPAddr{IP: net.IPv4(203, 0, 113, 99), Port: 54321}, 1, ifaces, localIPs) {
		t.Fatal("expected off-link query source 203.0.113.99 to be rejected by isOnLinkSource")
	}

	// Multicast conflict response from a peer on the same L2 link with a different IP subnet must still trigger conflict resolution.
	mcastSubResp := &hostRecordResponder{
		baseHost: "cpa-8f3b",
		osHost:   "devbox",
		domain:   DefaultDomain,
		attempt:  1,
		hostFQDN: "cpa-8f3b.local.",
		fixedIPs: []string{"192.0.2.10"},
		ifaces:   ifaces,
		probeConflict: func(context.Context, string, []net.Interface, []string) (bool, error) {
			return false, nil
		},
		stopCh: make(chan struct{}),
	}
	mcastSubResp.handlePacketWithMeta(conflictRespPkt, 1, &net.UDPAddr{IP: net.IPv4(198, 51, 100, 77), Port: mdnsPort}, true)
	if mcastSubResp.currentHostFQDN() != "cpa-8f3b-2.local." {
		t.Fatalf("expected multicast conflict from different subnet on same link to rename to cpa-8f3b-2.local., got %q", mcastSubResp.currentHostFQDN())
	}

	// Multi-question query containing both host A and service PTR/SRV questions (and mixed QU/QM questions)
	// must produce separate replies preserving each question's unicast/multicast mode.
	enumQName, _ := dnsmessage.NewName("_services._dns-sd._udp.local.")
	mixedBuilder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 88})
	_ = mixedBuilder.StartQuestions()
	_ = mixedBuilder.Question(dnsmessage.Question{Name: qName, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET})
	_ = mixedBuilder.Question(dnsmessage.Question{Name: enumQName, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET | dnsmessage.Class(mdnsCacheFlushBit)})
	_ = mixedBuilder.Question(dnsmessage.Question{Name: svcQName, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET})
	mixedQueryPkt, errMixed := mixedBuilder.Finish()
	if errMixed != nil {
		t.Fatalf("mixedBuilder.Finish(): %v", errMixed)
	}
	combinedReplies := buildCombinedQueryReplies(mixedQueryPkt, svcCfg, "cpa-8f3b.local.", []string{"192.0.2.10"}, false)
	if len(combinedReplies) != 3 {
		t.Fatalf("expected 3 replies (QM host + QM service + QU service) for mixed QU/QM query, got %d", len(combinedReplies))
	}
	if combinedReplies[0].unicast || combinedReplies[1].unicast || !combinedReplies[2].unicast {
		t.Fatalf("expected [multicast, multicast, unicast] replies, got [%v, %v, %v]", combinedReplies[0].unicast, combinedReplies[1].unicast, combinedReplies[2].unicast)
	}

	// Repeated announcement schedule (RFC 6762 §8.3) and cancellation on stopCh via scheduleServiceAnnouncements.
	schedResp := &hostRecordResponder{
		svc:      svcCfg,
		hostFQDN: "cpa-8f3b.local.",
		stopCh:   make(chan struct{}),
	}
	var annCount atomic.Int32
	secondSent := make(chan struct{})
	schedResp.scheduleServiceAnnouncements(mdnsAnnounceRepetitions, 5*time.Millisecond, func() {
		if annCount.Add(1) == 2 {
			close(secondSent)
		}
	})
	<-secondSent
	schedResp.wg.Wait()
	if got := int(annCount.Load()); got != mdnsAnnounceRepetitions {
		t.Fatalf("scheduleServiceAnnouncements count = %d, want %d", got, mdnsAnnounceRepetitions)
	}

	cancelResp := &hostRecordResponder{
		svc:      svcCfg,
		hostFQDN: "cpa-8f3b.local.",
		stopCh:   make(chan struct{}),
	}
	var cancelCount atomic.Int32
	cancelResp.scheduleServiceAnnouncements(mdnsAnnounceRepetitions, time.Hour, func() {
		cancelCount.Add(1)
	})
	if got := int(cancelCount.Load()); got != 1 {
		t.Fatalf("initial synchronous announcement count = %d, want 1", got)
	}
	close(cancelResp.stopCh)
	cancelResp.wg.Wait()
	if got := int(cancelCount.Load()); got != 1 {
		t.Fatalf("canceled scheduleServiceAnnouncements count = %d, want 1", got)
	}

	// Stale host send fence: a send prepared under an older hostFQDN must be dropped
	// once handleConflict rotates the responder to a new hostFQDN.
	var sentFQDNs []string
	fenceResp := &hostRecordResponder{
		baseHost: "cpa-8f3b",
		osHost:   "devbox",
		domain:   DefaultDomain,
		attempt:  1,
		hostFQDN: "cpa-8f3b.local.",
		fixedIPs: []string{"192.0.2.10"},
		ifaces:   ifaces,
		probeConflict: func(context.Context, string, []net.Interface, []string) (bool, error) {
			return false, nil
		},
		onSend: func(fqdn string, _ bool, _ []byte) {
			sentFQDNs = append(sentFQDNs, fqdn)
		},
		stopCh: make(chan struct{}),
	}
	staleFQDN := fenceResp.currentHostFQDN()
	fenceResp.handlePacketWithMeta(conflictRespPkt, 1, onLinkPeer, true)
	if fenceResp.currentHostFQDN() != "cpa-8f3b-2.local." {
		t.Fatalf("expected rename to cpa-8f3b-2.local., got %q", fenceResp.currentHostFQDN())
	}
	if fenceResp.sendIfHostCurrent(staleFQDN, false, []byte{1, 2, 3}, 1, nil) {
		t.Fatal("expected sendIfHostCurrent to reject stale hostFQDN after rename")
	}
	if !fenceResp.sendIfHostCurrent("cpa-8f3b-2.local.", false, []byte{1, 2, 3}, 1, nil) {
		t.Fatal("expected sendIfHostCurrent to accept current hostFQDN")
	}
	if len(sentFQDNs) != 1 || sentFQDNs[0] != "cpa-8f3b-2.local." {
		t.Fatalf("sentFQDNs = %v, want [cpa-8f3b-2.local.]", sentFQDNs)
	}

	// Duplicate questions in a single query packet must be deduplicated in linear time.
	dupBuilder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 99})
	_ = dupBuilder.StartQuestions()
	for i := 0; i < 200; i++ {
		_ = dupBuilder.Question(dnsmessage.Question{Name: svcQName, Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET})
	}
	dupQuery, errDup := dupBuilder.Finish()
	if errDup != nil {
		t.Fatalf("dupBuilder.Finish(): %v", errDup)
	}
	dupReply, _ := buildServiceQueryReply(dupQuery, svcCfg, "cpa-8f3b.local.", []string{"192.0.2.10"}, true)
	var dupParser dnsmessage.Parser
	if _, errDP := dupParser.Start(dupReply); errDP != nil {
		t.Fatalf("dupParser.Start: %v", errDP)
	}
	dupQs, _ := dupParser.AllQuestions()
	if len(dupQs) != 1 {
		t.Fatalf("expected 200 duplicate questions to be deduplicated to 1, got %d", len(dupQs))
	}
}

func TestNewHostRecordResponder_FailsAndCleansUpWhenAllMulticastJoinsFail(t *testing.T) {
	inner := &fakeZeroconfServer{}
	resp, err := newHostRecordResponder(inner, "cpa-8f3b", DefaultDomain, []string{"192.0.2.10"}, []net.Interface{{Index: -1, Name: "invalid0"}})
	if err == nil || resp != nil {
		if resp != nil {
			resp.Shutdown()
		}
		t.Fatalf("expected error when all multicast joins fail, got resp=%v err=%v", resp, err)
	}
	if inner.shutdowns != 1 {
		t.Fatalf("inner shutdowns = %d, want 1", inner.shutdowns)
	}
}
