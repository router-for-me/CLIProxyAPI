package discovery

import "net"

// browseConn preserves the receiving interface and destination across platforms.
// Both fields are required by the resolver's link-scoped peer validation.
type browseConn interface {
	ReadPacket([]byte) (n, index int, src *net.UDPAddr, dst net.IP, err error)
	WriteQuery([]byte, *net.Interface) error
	Close() error
}

// mdnsQueryAddr6 carries the scope required to route link-local multicast on
// platforms such as Darwin. Do not mutate the shared multicast destination.
func mdnsQueryAddr6(iface *net.Interface) *net.UDPAddr {
	return &net.UDPAddr{IP: mdnsGroupAddrIPv6.IP, Port: mdnsGroupAddrIPv6.Port, Zone: iface.Name}
}
