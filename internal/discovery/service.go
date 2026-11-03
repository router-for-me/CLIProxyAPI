package discovery

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

// ResolveDiscoveryStateDir returns an absolute state directory for discovery metadata,
// avoiding writing relative paths into the current working directory.
func ResolveDiscoveryStateDir() string {
	if writable := util.WritablePath(); writable != "" {
		return filepath.Join(writable, "discovery")
	}
	if configDir, err := os.UserConfigDir(); err == nil && configDir != "" {
		return filepath.Join(configDir, "cpa", "discovery")
	}
	if homeDir, err := os.UserHomeDir(); err == nil && homeDir != "" {
		return filepath.Join(homeDir, ".config", "cpa", "discovery")
	}
	return ""
}

// validateServiceType checks whether st conforms to RFC 6763 / RFC 6335 service type format.
// CPA only exposes a TCP listener, so advertised types must end with ._tcp.
func validateServiceType(st string) error {
	st = strings.TrimSpace(st)
	if st == "" {
		return fmt.Errorf("service type cannot be empty")
	}
	if len(st) > 63 {
		return fmt.Errorf("service type %q exceeds 63 characters", st)
	}
	if !strings.HasSuffix(st, "._tcp") {
		return fmt.Errorf("service type %q must end with ._tcp", st)
	}
	prefix := st[:len(st)-5]
	if !strings.HasPrefix(prefix, "_") {
		return fmt.Errorf("service type %q must start with an underscore", st)
	}
	name := prefix[1:]
	if len(name) == 0 || len(name) > 15 {
		return fmt.Errorf("service name %q must be between 1 and 15 characters (RFC 6335)", name)
	}
	if !strings.ContainsAny(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") || strings.Contains(name, "--") {
		return fmt.Errorf("service name %q must contain a letter and cannot contain consecutive hyphens (RFC 6335)", name)
	}
	for i, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-') {
			return fmt.Errorf("service type contains invalid character %q (RFC 6335)", r)
		}
		if (i == 0 || i == len(name)-1) && r == '-' {
			return fmt.Errorf("service name cannot start or end with a hyphen")
		}
	}
	return nil
}

// sanitizeSubtype validates and normalizes a subtype label according to RFC 6763 §7.1.
// Returns a valid label prefixed with '_' (e.g. "_responses") or empty string if invalid.
func sanitizeSubtype(sub string) string {
	sub = strings.TrimSpace(sub)
	if sub == "" {
		return ""
	}
	// Reject malformed attempts to pass full domain structures
	if strings.Contains(sub, "._sub") || strings.Contains(sub, ".") {
		return ""
	}
	if !strings.HasPrefix(sub, "_") {
		sub = "_" + sub
	}
	label := sub[1:]
	if len(label) == 0 || len(label) > 62 {
		return ""
	}
	// RFC 6335 / RFC 6763: alphanumeric and hyphen, cannot start or end with hyphen, no internal underscores
	if label[0] == '-' || label[len(label)-1] == '-' {
		return ""
	}
	for _, r := range label {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-') {
			return ""
		}
	}
	return sub
}

// normalizeBrowseQuery accepts the RFC 6763 subtype owner form as well as the
// comma form historically accepted by zeroconf. It returns lowercase DNS names.
func normalizeBrowseQuery(service, domain string) (string, string, error) {
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if domain == "" {
		domain = strings.TrimSuffix(DefaultDomain, ".")
	}
	if len(domain) > 253 {
		return "", "", fmt.Errorf("discovery: browse domain too long")
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", "", fmt.Errorf("discovery: invalid browse domain")
		}
		for _, ch := range label {
			if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-') {
				return "", "", fmt.Errorf("discovery: invalid browse domain")
			}
		}
	}
	service = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(service), "."))
	if service == "" {
		service = DefaultServiceType
	}
	service = strings.TrimSuffix(service, "."+domain)
	if base, subtype, comma := strings.Cut(service, ","); comma {
		service = subtype + "._sub." + base
	}
	base := service
	if subtype, rest, sub := strings.Cut(service, "._sub."); sub {
		if !strings.HasPrefix(subtype, "_") || sanitizeSubtype(subtype) != subtype {
			return "", "", fmt.Errorf("discovery: invalid browse subtype")
		}
		base = rest
	}
	if err := validateServiceType(base); err != nil {
		return "", "", fmt.Errorf("discovery: invalid browse service: %w", err)
	}
	return service, domain + ".", nil
}

func truncateRunesTo(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	for len(s) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(s)
		if size == 0 {
			break
		}
		s = s[:len(s)-size]
	}
	return s
}

// sanitizeInstanceName limits name to 63 bytes without breaking UTF-8 rune boundaries
// and removes ASCII control characters.
func sanitizeInstanceName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r >= 32 && r != 127 {
			b.WriteRune(r)
		}
	}
	res := strings.TrimSpace(b.String())
	for len(res) > 63 {
		_, size := utf8.DecodeLastRuneInString(res)
		if size == 0 {
			break
		}
		res = res[:len(res)-size]
	}
	return res
}

func interfaceHasIP(iface net.Interface, target net.IP) bool {
	addrs, err := iface.Addrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		var ip net.IP
		switch value := addr.(type) {
		case *net.IPNet:
			ip = value.IP
		case *net.IPAddr:
			ip = value.IP
		}
		if ip != nil && ip.Equal(target) {
			return true
		}
	}
	return false
}

// BuildServiceSpec creates a ServiceSpec from configuration, port, and TLS setting.
func BuildServiceSpec(cfg *config.Config, port int, tlsEnabled bool) (ServiceSpec, error) {
	if cfg == nil {
		return ServiceSpec{}, fmt.Errorf("discovery: config is nil")
	}
	if port < 1 || port > 65535 {
		return ServiceSpec{}, fmt.Errorf("discovery: invalid service port %d (must be between 1 and 65535)", port)
	}
	discCfg := cfg.Discovery

	// 1. Resolve State Dir for persistent instance ID
	stateDir := ResolveDiscoveryStateDir()
	instanceID := GetOrGenerateInstanceID(stateDir)

	// 2. Format instance name (CPA-<ShortID> or <custom>-<ShortID>) within DNS label limits
	instanceName := FormatInstanceName(discCfg.ServiceName, instanceID)
	if instanceName == "" {
		instanceName = DefaultInstancePrefix + instanceID
	}

	// 3. Service type validation (RFC 6763 / RFC 6335)
	serviceType := strings.TrimSpace(discCfg.ServiceType)
	if serviceType == "" {
		serviceType = DefaultServiceType
	} else if errST := validateServiceType(serviceType); errST != nil {
		return ServiceSpec{}, fmt.Errorf("discovery: invalid service-type: %w", errST)
	}

	// 4. Subtypes sanitization
	var subtypes []string
	rawSubtypes := discCfg.Subtypes
	if len(rawSubtypes) == 0 {
		rawSubtypes = []string{
			SubtypeChatCompletions,
			SubtypeResponses,
			SubtypeMessages,
			SubtypeGenerateContent,
			SubtypeInteractions,
		}
	}
	for _, raw := range rawSubtypes {
		if clean := sanitizeSubtype(raw); clean != "" {
			subtypes = append(subtypes, clean)
		}
	}
	if len(subtypes) == 0 {
		subtypes = []string{SubtypeChatCompletions}
	}

	// 5. Interface filtering (Scheme C)
	ifaces, err := FilterInterfaces(discCfg.Interfaces.Include, discCfg.Interfaces.Exclude)
	if err != nil {
		log.Warnf("discovery: failed to filter interfaces: %v", err)
	}
	if len(ifaces) == 0 {
		return ServiceSpec{}, fmt.Errorf("discovery: no qualified physical interfaces found matching filters (refusing fallback to all interfaces)")
	}
	bindHost := strings.TrimSpace(cfg.Host)
	var bindIP net.IP
	if bindHost != "" {
		bindIP = net.ParseIP(bindHost)
		if bindIP == nil {
			return ServiceSpec{}, fmt.Errorf("discovery: refusing LAN advertising for non-IP bind host %q", bindHost)
		}
		if bindIP.IsLoopback() {
			return ServiceSpec{}, fmt.Errorf("discovery: LAN advertising is unavailable for loopback bind host %q", bindHost)
		}
	}
	if bindIP != nil && !bindIP.IsUnspecified() {
		filteredIfaces := make([]net.Interface, 0, 1)
		for _, iface := range ifaces {
			if interfaceHasIP(iface, bindIP) {
				filteredIfaces = append(filteredIfaces, iface)
			}
		}
		if len(filteredIfaces) == 0 {
			return ServiceSpec{}, fmt.Errorf("discovery: no interface owns bind host %q", bindHost)
		}
		ifaces = filteredIfaces
	}
	advertisedIPs := extractInterfaceIPs(ifaces)
	if bindIP != nil && !bindIP.IsUnspecified() {
		filtered := advertisedIPs[:0]
		for _, advertisedIP := range advertisedIPs {
			if net.ParseIP(advertisedIP).Equal(bindIP) {
				filtered = append(filtered, advertisedIP)
			}
		}
		if len(filtered) == 0 {
			filtered = append(filtered, bindIP.String())
		}
		advertisedIPs = filtered
	}
	if len(advertisedIPs) == 0 {
		return ServiceSpec{}, fmt.Errorf("discovery: no advertised address matches bind host %q", bindHost)
	}

	// 6. Build TXT records; protocols mirror the advertised subtypes.
	txtOpts := DefaultTXTOptions()
	txtOpts.InstanceID = instanceID
	txtOpts.TLS = tlsEnabled
	if discCfg.AuthRequired != nil {
		txtOpts.AuthRequired = *discCfg.AuthRequired
	}
	txtOpts.Protocols = protocolsFromSubtypes(subtypes)

	txtRecords, errTXT := BuildTXTRecords(txtOpts)
	if errTXT != nil {
		return ServiceSpec{}, errTXT
	}

	return ServiceSpec{
		InstanceName:  instanceName,
		ServiceType:   serviceType,
		Domain:        DefaultDomain,
		Port:          port,
		Subtypes:      subtypes,
		TextRecords:   txtRecords,
		Interfaces:    ifaces,
		AdvertisedIPs: advertisedIPs,
	}, nil
}

// protocolsFromSubtypes derives TXT protocol tokens from advertised subtype labels
// so the protocols attribute and DNS-SD subtypes stay consistent.
func protocolsFromSubtypes(subtypes []string) []string {
	protocols := make([]string, 0, len(subtypes))
	for _, subtype := range subtypes {
		protocols = append(protocols, ProtocolFromSubtype(subtype))
	}
	return protocols
}
