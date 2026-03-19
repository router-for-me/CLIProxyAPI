package util

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// ClientAddressResolver resolves the effective client IP for a request.
// It only trusts forwarding headers when the immediate peer matches a configured trusted proxy.
type ClientAddressResolver struct {
	trustedProxyCIDRs []*net.IPNet
}

// ClientAddressInfo describes the immediate peer and the resolved client identity for a request.
type ClientAddressInfo struct {
	PeerIP                string
	ClientIP              string
	HasForwardingHeaders  bool
	UsedTrustedForwarding bool
}

// NewClientAddressResolver builds a resolver from trusted proxy IPs/CIDRs.
func NewClientAddressResolver(trustedProxies []string) (*ClientAddressResolver, error) {
	cidrs, err := parseTrustedProxies(trustedProxies)
	if err != nil {
		return nil, err
	}
	return &ClientAddressResolver{trustedProxyCIDRs: cidrs}, nil
}

// Resolve returns the immediate peer IP and the effective client IP for the request.
func (r *ClientAddressResolver) Resolve(req *http.Request) ClientAddressInfo {
	if req == nil {
		return ClientAddressInfo{}
	}

	peerIP := RemoteAddrHost(req.RemoteAddr)
	hasForwardingHeaders := hasForwardingHeaders(req.Header)

	info := ClientAddressInfo{
		PeerIP:               peerIP,
		ClientIP:             peerIP,
		HasForwardingHeaders: hasForwardingHeaders,
	}

	if !r.isTrustedProxy(peerIP) {
		return info
	}

	if clientIP := r.firstTrustedForwardedClientIP(req.Header); clientIP != "" {
		info.ClientIP = clientIP
		info.UsedTrustedForwarding = true
	}

	return info
}

// IsLoopbackClient reports whether the effective client should be treated as localhost.
// If a loopback peer sends forwarding headers without being trusted, the request is treated as non-local.
func (i ClientAddressInfo) IsLoopbackClient() bool {
	if i.UsedTrustedForwarding {
		return isLoopbackIP(i.ClientIP)
	}
	if i.HasForwardingHeaders && isLoopbackIP(i.PeerIP) {
		return false
	}
	return isLoopbackIP(i.PeerIP)
}

// RateLimitKey returns the address that should be used for per-client throttling.
func (i ClientAddressInfo) RateLimitKey() string {
	if i.UsedTrustedForwarding && i.ClientIP != "" {
		return i.ClientIP
	}
	if i.PeerIP != "" {
		return i.PeerIP
	}
	return i.ClientIP
}

// RemoteAddrHost extracts the host/IP portion from a request RemoteAddr value.
func RemoteAddrHost(remoteAddr string) string {
	trimmed := strings.TrimSpace(remoteAddr)
	if trimmed == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(trimmed)
	if err == nil {
		if ip := normalizeIPCandidate(host); ip != "" {
			return ip
		}
		return host
	}
	return normalizeIPCandidate(trimmed)
}

func parseTrustedProxies(trustedProxies []string) ([]*net.IPNet, error) {
	if len(trustedProxies) == 0 {
		return nil, nil
	}

	cidrs := make([]*net.IPNet, 0, len(trustedProxies))
	for _, entry := range trustedProxies {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}

		if strings.Contains(trimmed, "/") {
			_, network, err := net.ParseCIDR(trimmed)
			if err != nil {
				return nil, fmt.Errorf("invalid trusted proxy CIDR %q: %w", trimmed, err)
			}
			cidrs = append(cidrs, network)
			continue
		}

		ip := net.ParseIP(trimmed)
		if ip == nil {
			return nil, fmt.Errorf("invalid trusted proxy IP %q", trimmed)
		}
		maskBits := 32
		if ip.To4() == nil {
			maskBits = 128
		}
		cidrs = append(cidrs, &net.IPNet{
			IP:   ip,
			Mask: net.CIDRMask(maskBits, maskBits),
		})
	}

	if len(cidrs) == 0 {
		return nil, nil
	}
	return cidrs, nil
}

func (r *ClientAddressResolver) isTrustedProxy(ipStr string) bool {
	if r == nil || len(r.trustedProxyCIDRs) == 0 {
		return false
	}
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return false
	}
	for _, network := range r.trustedProxyCIDRs {
		if network != nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func hasForwardingHeaders(headers http.Header) bool {
	if headers == nil {
		return false
	}
	for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Real-IP"} {
		for _, value := range headers.Values(name) {
			if strings.TrimSpace(value) != "" {
				return true
			}
		}
	}
	return false
}

func (r *ClientAddressResolver) firstTrustedForwardedClientIP(headers http.Header) string {
	if headers == nil {
		return ""
	}
	if ip := r.parseForwardedHeader(headers.Values("Forwarded")); ip != "" {
		return ip
	}
	if ip := r.parseXForwardedFor(headers.Values("X-Forwarded-For")); ip != "" {
		return ip
	}
	for _, value := range headers.Values("X-Real-IP") {
		if ip := normalizeIPCandidate(value); ip != "" {
			return ip
		}
	}
	return ""
}

func (r *ClientAddressResolver) parseForwardedHeader(values []string) string {
	candidates := make([]string, 0, len(values))
	for _, value := range values {
		for _, element := range strings.Split(value, ",") {
			for _, param := range strings.Split(element, ";") {
				name, rawValue, ok := strings.Cut(param, "=")
				if !ok || !strings.EqualFold(strings.TrimSpace(name), "for") {
					continue
				}
				if ip := normalizeIPCandidate(rawValue); ip != "" {
					candidates = append(candidates, ip)
				}
			}
		}
	}
	return r.lastUntrustedForwardedIP(candidates)
}

func (r *ClientAddressResolver) parseXForwardedFor(values []string) string {
	candidates := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if ip := normalizeIPCandidate(part); ip != "" {
				candidates = append(candidates, ip)
			}
		}
	}
	return r.lastUntrustedForwardedIP(candidates)
}

func (r *ClientAddressResolver) lastUntrustedForwardedIP(candidates []string) string {
	for i := len(candidates) - 1; i >= 0; i-- {
		if i == 0 || !r.isTrustedProxy(candidates[i]) {
			return candidates[i]
		}
	}
	return ""
}

func normalizeIPCandidate(value string) string {
	trimmed := strings.TrimSpace(strings.Trim(value, `"`))
	if trimmed == "" || strings.EqualFold(trimmed, "unknown") || strings.HasPrefix(trimmed, "_") {
		return ""
	}
	if host, _, err := net.SplitHostPort(trimmed); err == nil {
		trimmed = host
	}
	trimmed = strings.TrimPrefix(trimmed, "[")
	trimmed = strings.TrimSuffix(trimmed, "]")
	if zoneIndex := strings.Index(trimmed, "%"); zoneIndex >= 0 {
		trimmed = trimmed[:zoneIndex]
	}
	ip := net.ParseIP(trimmed)
	if ip == nil {
		return ""
	}
	return ip.String()
}

func isLoopbackIP(value string) bool {
	ip := net.ParseIP(strings.TrimSpace(value))
	return ip != nil && ip.IsLoopback()
}
