package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/discovery"
	"gopkg.in/yaml.v3"
)

// DiscoverOptions configures a one-shot LAN discovery scan.
type DiscoverOptions struct {
	Timeout     time.Duration
	JSONOutput  bool
	ServiceType string
	Include     []string
	Exclude     []string
}

func newLANBrowser(include, exclude []string) (discovery.Browser, error) {
	ifaces, err := discovery.FilterInterfaces(include, exclude)
	if err != nil {
		return nil, err
	}
	if len(ifaces) == 0 {
		return nil, fmt.Errorf("no qualified physical interfaces found for LAN discovery")
	}
	return discovery.NewZeroconfBrowser(ifaces...), nil
}

// ParseInterfaceList splits comma-separated interface names and drops blanks.
func ParseInterfaceList(values ...string) []string {
	var out []string
	seen := make(map[string]struct{})
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, exists := seen[part]; exists {
				continue
			}
			seen[part] = struct{}{}
			out = append(out, part)
		}
	}
	return out
}

// ResolveDiscoveryInterfaceFilters prefers explicit CLI filters over config filters.
func ResolveDiscoveryInterfaceFilters(cliInclude, cliExclude, cfgInclude, cfgExclude []string) (include, exclude []string) {
	if len(cliInclude) > 0 || len(cliExclude) > 0 {
		return append([]string(nil), cliInclude...), append([]string(nil), cliExclude...)
	}
	return append([]string(nil), cfgInclude...), append([]string(nil), cfgExclude...)
}

// LoadDiscoveryScanFilters reads discovery.interfaces from a config file.
// Missing or invalid files yield empty filters so the default physical LAN allow-list is used.
func defaultDiscoveryConfigPath() string {
	wd, errGetwd := os.Getwd()
	if errGetwd != nil {
		return "config.yaml"
	}
	return filepath.Join(wd, "config.yaml")
}

func LoadDiscoveryScanFilters(configPath string) (include, exclude []string) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		configPath = defaultDiscoveryConfigPath()
	}
	raw, errRead := os.ReadFile(configPath)
	if errRead != nil {
		return nil, nil
	}
	var payload struct {
		Discovery struct {
			Interfaces struct {
				Include []string `yaml:"include"`
				Exclude []string `yaml:"exclude"`
			} `yaml:"interfaces"`
		} `yaml:"discovery"`
	}
	if errParse := yaml.Unmarshal(raw, &payload); errParse != nil {
		return nil, nil
	}
	return ParseInterfaceList(payload.Discovery.Interfaces.Include...), ParseInterfaceList(payload.Discovery.Interfaces.Exclude...)
}

// DoDiscover executes the LAN AI gateway discovery workflow and outputs results.
// Returns 0 on success, 1 on error.
func DoDiscover(timeout time.Duration, jsonOutput bool) int {
	return DoDiscoverWithOptions(DiscoverOptions{Timeout: timeout, JSONOutput: jsonOutput})
}

// DoDiscoverWithServiceType executes LAN discovery for the requested DNS-SD service type.
func DoDiscoverWithServiceType(timeout time.Duration, jsonOutput bool, serviceType string) int {
	return DoDiscoverWithOptions(DiscoverOptions{Timeout: timeout, JSONOutput: jsonOutput, ServiceType: serviceType})
}

// DoDiscoverWithOptions executes LAN discovery with explicit scan options.
func DoDiscoverWithOptions(opts DiscoverOptions) int {
	include, exclude := ResolveDiscoveryInterfaceFilters(opts.Include, opts.Exclude, nil, nil)
	return runDiscoverWithOptions(opts, os.Stdout, os.Stderr, func() (discovery.Browser, error) {
		return newLANBrowser(include, exclude)
	})
}

func runDiscover(timeout time.Duration, jsonOutput bool, stdout, stderr io.Writer, newBrowser func() (discovery.Browser, error)) int {
	return runDiscoverWithOptions(DiscoverOptions{Timeout: timeout, JSONOutput: jsonOutput}, stdout, stderr, newBrowser)
}

func runDiscoverWithServiceType(timeout time.Duration, jsonOutput bool, serviceType string, stdout, stderr io.Writer, newBrowser func() (discovery.Browser, error)) int {
	return runDiscoverWithOptions(DiscoverOptions{Timeout: timeout, JSONOutput: jsonOutput, ServiceType: serviceType}, stdout, stderr, newBrowser)
}

func runDiscoverWithOptions(opts DiscoverOptions, stdout, stderr io.Writer, newBrowser func() (discovery.Browser, error)) int {
	timeout := opts.Timeout
	jsonOutput := opts.JSONOutput
	serviceType := opts.ServiceType
	if timeout <= 0 {
		timeout = 3 * time.Second
	} else if timeout > 60*time.Second {
		timeout = 60 * time.Second
	}
	serviceType = strings.TrimSpace(serviceType)
	if serviceType == "" {
		serviceType = discovery.DefaultServiceType
	}

	if !jsonOutput {
		fmt.Fprintf(stdout, "Scanning LAN for AI Gateways (%s)... (timeout %v)\n", serviceType, timeout)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	browser, errBrowser := newBrowser()
	if errBrowser != nil {
		if jsonOutput {
			out, _ := json.MarshalIndent(map[string]any{"error": errBrowser.Error(), "gateways": []any{}}, "", "  ")
			fmt.Fprintln(stdout, string(out))
		} else {
			fmt.Fprintf(stderr, "Error scanning LAN: %v\n", errBrowser)
		}
		return 1
	}
	gateways, err := browser.BrowseWithFallbackServiceType(ctx, serviceType)
	if err != nil {
		if jsonOutput {
			out, _ := json.MarshalIndent(map[string]any{"error": err.Error(), "gateways": []any{}}, "", "  ")
			fmt.Fprintln(stdout, string(out))
		} else {
			fmt.Fprintf(stderr, "Error scanning LAN: %v\n", err)
		}
		return 1
	}

	if gateways == nil {
		gateways = []discovery.DiscoveredService{}
	}

	if jsonOutput {
		out, errMarshal := json.MarshalIndent(gateways, "", "  ")
		if errMarshal != nil {
			fmt.Fprintf(stderr, "failed to marshal results: %v\n", errMarshal)
			return 1
		}
		fmt.Fprintln(stdout, string(out))
		return 0
	}

	if len(gateways) == 0 {
		fmt.Fprintln(stdout, "\nNo AI gateways found on local network.")
		fmt.Fprintln(stdout, "Tips:")
		fmt.Fprintln(stdout, "  1. Ensure the target CPA instance has 'discovery.enabled: true' in its config.yaml.")
		fmt.Fprintln(stdout, "  2. Ensure your device is on the same local Wi-Fi / Ethernet subnet (mDNS does not traverse WAN).")
		fmt.Fprintln(stdout, "  3. Check that your local firewall allows UDP port 5353 multicast traffic.")
		return 0
	}

	fmt.Fprintf(stdout, "\nFound %d AI Gateway(s) on local network:\n\n", len(gateways))
	for i, gw := range gateways {
		productLabel := sanitizeTerminal(gw.Product)
		if productLabel == "" {
			productLabel = "generic"
		}
		versionLabel := sanitizeTerminal(gw.Version)
		if versionLabel == "" {
			versionLabel = "unknown"
		}

		instanceLabel := sanitizeTerminal(gw.InstanceName)
		fmt.Fprintf(stdout, "[%d] %s (Product: %s, Version: %s)\n", i+1, instanceLabel, productLabel, versionLabel)

		primaryIP, allIPs := preferredDisplayAddresses(gw)

		hostPort := net.JoinHostPort(primaryIP, strconv.Itoa(gw.Port))
		hostLabel := sanitizeTerminal(gw.Host)
		fmt.Fprintf(stdout, "    Host:      %s (%s)\n", hostLabel, hostPort)
		if len(allIPs) > 1 {
			fmt.Fprintf(stdout, "    Addresses: %s\n", strings.Join(allIPs, ", "))
		}

		if len(gw.Protocols) > 0 {
			var safeProtocols []string
			for _, p := range gw.Protocols {
				safeProtocols = append(safeProtocols, sanitizeTerminal(p))
			}
			fmt.Fprintf(stdout, "    Protocols: %s\n", strings.Join(safeProtocols, ", "))
		}
		if len(gw.Features) > 0 {
			var safeFeatures []string
			for _, f := range gw.Features {
				safeFeatures = append(safeFeatures, sanitizeTerminal(f))
			}
			fmt.Fprintf(stdout, "    Features:  %s\n", strings.Join(safeFeatures, ", "))
		}

		authStatus := "No"
		if gw.AuthRequired {
			authStatus = "Required"
			if len(gw.AuthMethods) > 0 {
				var safeMethods []string
				for _, m := range gw.AuthMethods {
					safeMethods = append(safeMethods, sanitizeTerminal(m))
				}
				authStatus = fmt.Sprintf("Required (%s)", strings.Join(safeMethods, ", "))
			}
		}
		fmt.Fprintf(stdout, "    Auth:      %s\n", authStatus)

		// Endpoints display
		scheme := "http"
		if gw.RawTXT["tls"] == "1" {
			scheme = "https"
		}

		fmt.Fprintf(stdout, "    Base URLs:\n")
		if openAIPath, ok := gw.Endpoints["openai"]; ok {
			fmt.Fprintf(stdout, "      - OpenAI:    %s://%s%s\n", scheme, hostPort, sanitizeTerminal(openAIPath))
		} else {
			fmt.Fprintf(stdout, "      - OpenAI:    %s://%s/v1\n", scheme, hostPort)
		}

		if anthropicPath, ok := gw.Endpoints["anthropic"]; ok {
			fmt.Fprintf(stdout, "      - Anthropic: %s://%s%s\n", scheme, hostPort, sanitizeTerminal(anthropicPath))
		}
		if geminiPath, ok := gw.Endpoints["gemini"]; ok {
			fmt.Fprintf(stdout, "      - Gemini:    %s://%s%s\n", scheme, hostPort, sanitizeTerminal(geminiPath))
		}

		fmt.Fprintln(stdout)
	}

	return 0
}

func preferredDisplayAddresses(gw discovery.DiscoveredService) (primary string, all []string) {
	for _, ip := range gw.IPv4 {
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		all = append(all, ip.String())
	}
	if len(all) > 0 {
		return all[0], all
	}

	var routable []string
	var linkLocal []string
	for _, ip := range gw.IPv6 {
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		if ip.IsLinkLocalUnicast() {
			linkLocal = append(linkLocal, ip.String())
			continue
		}
		routable = append(routable, ip.String())
	}
	if len(routable) > 0 {
		return routable[0], append(routable, linkLocal...)
	}

	host := sanitizeDisplayHost(gw.Host)
	if host != "" {
		return host, append([]string{host}, linkLocal...)
	}
	if len(linkLocal) > 0 {
		return linkLocal[0], linkLocal
	}
	return "127.0.0.1", nil
}

func sanitizeDisplayHost(host string) string {
	host = sanitizeTerminal(strings.TrimSpace(strings.TrimSuffix(host, ".")))
	if host == "" || strings.EqualFold(host, "localhost") {
		return ""
	}
	for _, r := range host {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '.') {
			return ""
		}
	}
	if strings.Contains(host, "..") || strings.HasPrefix(host, "-") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, "-") || strings.HasSuffix(host, ".") {
		return ""
	}
	return host
}

// sanitizeTerminal strips control characters, ANSI escape sequences, Bidi overrides,
// and invisible Unicode format characters to prevent terminal injection and spoofing.
func sanitizeTerminal(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\t' {
			b.WriteRune(' ')
			continue
		}
		// Allow safe printable runes; block Bidi controls, zero-width chars, and line/paragraph separators
		if unicode.IsPrint(r) && !unicode.Is(unicode.Bidi_Control, r) {
			if r != '\u2028' && r != '\u2029' && !(r >= 0x200B && r <= 0x200F) && r != '\uFEFF' && r != 0x00AD {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}
