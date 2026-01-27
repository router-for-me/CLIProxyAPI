package executor

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sscore "github.com/shadowsocks/go-shadowsocks2/core"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/proxy"
)

// newProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use auth.ProxyURL if configured (highest priority)
// 2. Use cfg.ProxyURL if auth proxy is not configured
// 3. Use RoundTripper from context if neither are configured
//
// Parameters:
//   - ctx: The context containing optional RoundTripper
//   - cfg: The application configuration
//   - auth: The authentication information
//   - timeout: The client timeout (0 means no timeout)
//
// Returns:
//   - *http.Client: An HTTP client with configured proxy or transport
func newProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	httpClient := &http.Client{}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}

	// Priority 1: Use auth.ProxyURL and auth.ProxyDNS if configured
	var proxyURL, proxyDNS string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
		proxyDNS = strings.TrimSpace(auth.ProxyDNS)
	}

	// Priority 2: Use cfg.ProxyURL and cfg.ProxyDNS if auth proxy is not configured
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
		proxyDNS = strings.TrimSpace(cfg.ProxyDNS)
	}

	// If we have a proxy URL configured, set up the transport
	if proxyURL != "" {
		transport := buildProxyTransport(proxyURL, proxyDNS)
		if transport != nil {
			httpClient.Transport = transport
			return httpClient
		}
		// If proxy setup failed, log and fall through to context RoundTripper
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyURL)
	}

	// Priority 3: Use RoundTripper from context (typically from RoundTripperFor)
	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		httpClient.Transport = rt
	}

	return httpClient
}

// buildProxyTransport creates an HTTP transport configured for the given proxy URL.
// It supports SOCKS5, SS (Shadowsocks), HTTP, and HTTPS proxy protocols.
//
// Parameters:
//   - proxyURL: The proxy URL string (e.g., "socks5://user:pass@host:port", "ss://method:pass@host:port", "http://host:port")
//   - proxyDNS: Optional DoT DNS server (format: "tls://host:port") for resolving SS proxy hostnames
//
// Returns:
//   - *http.Transport: A configured transport, or nil if the proxy URL is invalid
func buildProxyTransport(proxyURL, proxyDNS string) *http.Transport {
	if proxyURL == "" {
		return nil
	}

	parsedURL, errParse := url.Parse(proxyURL)
	if errParse != nil {
		log.Errorf("parse proxy URL failed: %v", errParse)
		return nil
	}

	var transport *http.Transport

	// Handle different proxy schemes
	if parsedURL.Scheme == "socks5" {
		// Configure SOCKS5 proxy with optional authentication
		var proxyAuth *proxy.Auth
		if parsedURL.User != nil {
			username := parsedURL.User.Username()
			password, _ := parsedURL.User.Password()
			proxyAuth = &proxy.Auth{User: username, Password: password}
		}
		dialer, errSOCKS5 := proxy.SOCKS5("tcp", parsedURL.Host, proxyAuth, proxy.Direct)
		if errSOCKS5 != nil {
			log.Errorf("create SOCKS5 dialer failed: %v", errSOCKS5)
			return nil
		}
		// Set up a custom transport using the SOCKS5 dialer
		transport = &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			},
		}
	} else if parsedURL.Scheme == "ss" {
		// Configure Shadowsocks proxy
		ssMethod, ssPassword, ssServer, errSS := parseSSProxyURL(proxyURL)
		if errSS != nil {
			log.Errorf("parse Shadowsocks URL failed: %v", errSS)
			return nil
		}

		// Resolve SS server address using custom DNS if provided
		resolvedServer, errResolve := resolveSSServerAddr(ssServer, proxyDNS)
		if errResolve != nil {
			log.Errorf("resolve Shadowsocks server address failed: %v", errResolve)
			return nil
		}

		cipher, errCipher := sscore.PickCipher(ssMethod, nil, ssPassword)
		if errCipher != nil {
			log.Errorf("create Shadowsocks cipher failed (method=%s): %v", ssMethod, errCipher)
			return nil
		}
		transport = &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				// Connect to the Shadowsocks server using resolved address
				rawConn, errDial := net.Dial("tcp", resolvedServer)
				if errDial != nil {
					return nil, errDial
				}
				// Wrap the connection with Shadowsocks encryption
				ssConn := cipher.StreamConn(rawConn)
				// Write the target address in SOCKS5-style format
				if errAddr := writeSSProxyTargetAddr(ssConn, addr); errAddr != nil {
					rawConn.Close()
					return nil, errAddr
				}
				return ssConn, nil
			},
		}
	} else if parsedURL.Scheme == "http" || parsedURL.Scheme == "https" {
		// Configure HTTP or HTTPS proxy
		transport = &http.Transport{Proxy: http.ProxyURL(parsedURL)}
	} else {
		log.Errorf("unsupported proxy scheme: %s", parsedURL.Scheme)
		return nil
	}

	return transport
}

// resolveSSServerAddr resolves the SS server address using custom DoT DNS if provided.
// If proxyDNS is empty or the host is already an IP, returns the original address.
//
// Parameters:
//   - serverAddr: The server address in "host:port" format
//   - proxyDNS: Optional DoT DNS server (format: "tls://host:port")
//
// Returns:
//   - resolved address in "ip:port" format
//   - error if resolution fails
func resolveSSServerAddr(serverAddr, proxyDNS string) (string, error) {
	host, port, err := net.SplitHostPort(serverAddr)
	if err != nil {
		return "", fmt.Errorf("invalid server address: %w", err)
	}

	// If host is already an IP, return as-is
	if ip := net.ParseIP(host); ip != nil {
		return serverAddr, nil
	}

	// If no custom DNS, use system DNS
	if proxyDNS == "" {
		return serverAddr, nil
	}

	// Parse DoT DNS URL (format: tls://host:port)
	dnsURL, err := url.Parse(proxyDNS)
	if err != nil {
		return "", fmt.Errorf("parse proxy-dns URL: %w", err)
	}
	if dnsURL.Scheme != "tls" {
		return "", fmt.Errorf("proxy-dns must use tls:// scheme, got: %s", dnsURL.Scheme)
	}
	dnsServer := dnsURL.Host
	if dnsServer == "" {
		return "", fmt.Errorf("proxy-dns missing host")
	}

	// Resolve using DoT
	resolvedIP, err := resolveWithDoT(host, dnsServer)
	if err != nil {
		return "", fmt.Errorf("DoT resolution failed: %w", err)
	}

	log.Debugf("resolved SS server %s to %s using DoT DNS %s", host, resolvedIP, dnsServer)
	return net.JoinHostPort(resolvedIP, port), nil
}

// resolveWithDoT resolves a domain name using DNS over TLS.
//
// Parameters:
//   - domain: The domain name to resolve
//   - dnsServer: The DoT server address in "host:port" format
//
// Returns:
//   - The resolved IP address as a string
//   - error if resolution fails
func resolveWithDoT(domain, dnsServer string) (string, error) {
	// Connect to DoT server with TLS
	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 10 * time.Second},
		"tcp",
		dnsServer,
		&tls.Config{InsecureSkipVerify: true},
	)
	if err != nil {
		return "", fmt.Errorf("connect to DoT server: %w", err)
	}
	defer conn.Close()

	// Build DNS query message
	var msg dnsmessage.Message
	msg.Header.ID = uint16(time.Now().UnixNano() & 0xFFFF)
	msg.Header.RecursionDesired = true
	msg.Questions = []dnsmessage.Question{{
		Name:  dnsmessage.MustNewName(domain + "."),
		Type:  dnsmessage.TypeA,
		Class: dnsmessage.ClassINET,
	}}

	packed, err := msg.Pack()
	if err != nil {
		return "", fmt.Errorf("pack DNS query: %w", err)
	}

	// DNS over TLS requires length prefix (2 bytes, big-endian)
	length := make([]byte, 2)
	binary.BigEndian.PutUint16(length, uint16(len(packed)))
	if _, err := conn.Write(length); err != nil {
		return "", fmt.Errorf("write length prefix: %w", err)
	}
	if _, err := conn.Write(packed); err != nil {
		return "", fmt.Errorf("write DNS query: %w", err)
	}

	// Read response
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	respLen := make([]byte, 2)
	if _, err := conn.Read(respLen); err != nil {
		return "", fmt.Errorf("read response length: %w", err)
	}
	respSize := int(binary.BigEndian.Uint16(respLen))
	resp := make([]byte, respSize)
	if _, err := conn.Read(resp); err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	// Parse response
	var respMsg dnsmessage.Message
	if err := respMsg.Unpack(resp); err != nil {
		return "", fmt.Errorf("unpack DNS response: %w", err)
	}

	// Extract A record
	for _, ans := range respMsg.Answers {
		if ans.Header.Type == dnsmessage.TypeA {
			aRecord := ans.Body.(*dnsmessage.AResource)
			return fmt.Sprintf("%d.%d.%d.%d", aRecord.A[0], aRecord.A[1], aRecord.A[2], aRecord.A[3]), nil
		}
	}

	return "", fmt.Errorf("no A record found for %s", domain)
}

// parseSSProxyURL parses a Shadowsocks URL and returns method, password, and server address.
// Supports formats:
//   - ss://method:password@host:port
//   - ss://BASE64(method:password)@host:port (SIP002 format)
func parseSSProxyURL(ssURL string) (method, password, server string, err error) {
	u, errParse := url.Parse(ssURL)
	if errParse != nil {
		return "", "", "", fmt.Errorf("parse URL: %w", errParse)
	}
	if u.Scheme != "ss" {
		return "", "", "", fmt.Errorf("not a Shadowsocks URL")
	}
	server = u.Host
	if server == "" {
		return "", "", "", fmt.Errorf("missing server address")
	}
	// Try to get method:password from userinfo
	if u.User != nil {
		// Format: ss://method:password@host:port
		method = u.User.Username()
		password, _ = u.User.Password()
		if method != "" && password != "" {
			return method, password, server, nil
		}
		// If only username is present, it might be base64 encoded
		encoded := u.User.Username()
		if encoded != "" {
			decoded, errDecode := decodeSSProxyUserinfo(encoded)
			if errDecode == nil {
				parts := strings.SplitN(decoded, ":", 2)
				if len(parts) == 2 {
					return parts[0], parts[1], server, nil
				}
			}
		}
	}
	return "", "", "", fmt.Errorf("cannot parse method and password from URL")
}

// decodeSSProxyUserinfo decodes base64 userinfo (supports both standard and URL-safe base64).
func decodeSSProxyUserinfo(encoded string) (string, error) {
	// Try URL-safe base64 first (used by SIP002)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err == nil {
		return string(decoded), nil
	}
	// Try standard base64
	decoded, err = base64.StdEncoding.DecodeString(encoded)
	if err == nil {
		return string(decoded), nil
	}
	// Try standard base64 without padding
	decoded, err = base64.RawStdEncoding.DecodeString(encoded)
	if err == nil {
		return string(decoded), nil
	}
	return "", fmt.Errorf("failed to decode base64: %w", err)
}

// writeSSProxyTargetAddr writes the target address in SOCKS5-style format to the Shadowsocks connection.
// Format: ATYP (1 byte) + DST.ADDR (variable) + DST.PORT (2 bytes big-endian)
func writeSSProxyTargetAddr(conn net.Conn, addr string) error {
	host, portStr, errSplit := net.SplitHostPort(addr)
	if errSplit != nil {
		return fmt.Errorf("split host port: %w", errSplit)
	}
	port, errPort := net.LookupPort("tcp", portStr)
	if errPort != nil {
		return fmt.Errorf("lookup port: %w", errPort)
	}
	var buf []byte
	// Check if the host is an IP address
	ip := net.ParseIP(host)
	if ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			// IPv4 address: ATYP=0x01
			buf = make([]byte, 1+4+2)
			buf[0] = 0x01
			copy(buf[1:5], ip4)
		} else {
			// IPv6 address: ATYP=0x04
			buf = make([]byte, 1+16+2)
			buf[0] = 0x04
			copy(buf[1:17], ip.To16())
		}
	} else {
		// Domain name: ATYP=0x03
		if len(host) > 255 {
			return fmt.Errorf("domain name too long: %d", len(host))
		}
		buf = make([]byte, 1+1+len(host)+2)
		buf[0] = 0x03
		buf[1] = byte(len(host))
		copy(buf[2:2+len(host)], host)
	}
	// Write port (big-endian) at the end
	binary.BigEndian.PutUint16(buf[len(buf)-2:], uint16(port))
	_, errWrite := conn.Write(buf)
	if errWrite != nil {
		return fmt.Errorf("write target address: %w", errWrite)
	}
	return nil
}
