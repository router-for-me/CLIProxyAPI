package config

import (
	"fmt"
	"net"
	"strings"
)

func validateTrustedProxies(trustedProxies []string) error {
	for _, trustedProxy := range trustedProxies {
		if trustedProxy == "" || strings.TrimSpace(trustedProxy) != trustedProxy {
			return fmt.Errorf("invalid trusted-proxies entry %q: expected an IP address or CIDR", trustedProxy)
		}
		if net.ParseIP(trustedProxy) != nil {
			continue
		}
		if _, _, errParseCIDR := net.ParseCIDR(trustedProxy); errParseCIDR != nil {
			return fmt.Errorf("invalid trusted-proxies entry %q: %w", trustedProxy, errParseCIDR)
		}
	}
	return nil
}
