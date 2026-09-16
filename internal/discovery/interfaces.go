package discovery

import (
	"net"
	"strings"
)

// IgnoredInterfacePrefixes defines interface prefixes to exclude by default (virtual/container/VPN/P2P).
var IgnoredInterfacePrefixes = []string{
	"docker",
	"veth",
	"utun",
	"tailscale",
	"wg",
	"tun",
	"tap",
	"br-",
	"cni",
	"flannel",
	"virbr",
	"vmnet",
	"vboxnet",
	"awdl",
	"llw",
}

// FilterInterfaces selects qualified physical multicast-capable LAN interfaces according to Strategy C.
// If include is non-empty, only matching interface names are accepted.
// If exclude is non-empty, matching interface names are dropped.
func FilterInterfaces(include, exclude []string) ([]net.Interface, error) {
	all, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	var valid []net.Interface
	for _, iface := range all {
		// 1. Must be UP, not Loopback, not Point-to-Point
		if iface.Flags&net.FlagUp == 0 ||
			iface.Flags&net.FlagLoopback != 0 ||
			iface.Flags&net.FlagPointToPoint != 0 {
			continue
		}

		// 2. Must support Multicast
		if iface.Flags&net.FlagMulticast == 0 {
			continue
		}

		name := strings.ToLower(iface.Name)

		// 3. User explicit exclude list
		if len(exclude) > 0 && matchesAny(name, exclude) {
			continue
		}

		// 4. Default allow-list: only common physical LAN adapter names are
		// accepted unless the user explicitly provides an include list.
		if len(include) == 0 {
			if isVirtualOrTunnel(name) || !isLikelyPhysicalLAN(name) {
				continue
			}
		}

		// 5. User explicit include list
		if len(include) > 0 && !matchesAny(name, include) {
			continue
		}

		// 6. Must have at least one valid non-loopback IP address
		addrs, errAddrs := iface.Addrs()
		if errAddrs != nil || len(addrs) == 0 {
			continue
		}

		hasValidIP := false
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
				hasValidIP = true
				break
			}
		}

		if hasValidIP {
			valid = append(valid, iface)
		}
	}

	return valid, nil
}

func isVirtualOrTunnel(name string) bool {
	for _, prefix := range IgnoredInterfacePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func isLikelyPhysicalLAN(name string) bool {
	for _, prefix := range []string{
		"en", "eth", "em", "igb", "ix", "re", "wl", "wlan", "wifi", "wi-fi", "ethernet",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func matchesAny(name string, patterns []string) bool {
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if strings.HasSuffix(p, "*") {
			prefix := strings.TrimSuffix(p, "*")
			if strings.HasPrefix(name, prefix) {
				return true
			}
		} else if name == p {
			return true
		}
	}
	return false
}
