//go:build !windows

package discovery

import (
	"net"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

type browseConn4 struct{ *ipv4.PacketConn }
type browseConn6 struct{ *ipv6.PacketConn }

func openBrowseConn4(ifaces []net.Interface) (browseConn, error) {
	conn, err := openMDNSConn4(ifaces)
	if err != nil {
		return nil, err
	}
	if err = conn.SetControlMessage(ipv4.FlagInterface|ipv4.FlagDst, true); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &browseConn4{conn}, nil
}

func openBrowseConn6(ifaces []net.Interface) (browseConn, error) {
	conn, err := openMDNSConn6(ifaces)
	if err != nil {
		return nil, err
	}
	if err = conn.SetControlMessage(ipv6.FlagInterface|ipv6.FlagDst, true); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &browseConn6{conn}, nil
}

func (c *browseConn4) ReadPacket(b []byte) (n, index int, src *net.UDPAddr, dst net.IP, err error) {
	n, cm, from, err := c.ReadFrom(b)
	if cm != nil {
		index, dst = cm.IfIndex, cm.Dst
	}
	src, _ = from.(*net.UDPAddr)
	return
}

func (c *browseConn6) ReadPacket(b []byte) (n, index int, src *net.UDPAddr, dst net.IP, err error) {
	n, cm, from, err := c.ReadFrom(b)
	if cm != nil {
		index, dst = cm.IfIndex, cm.Dst
	}
	src, _ = from.(*net.UDPAddr)
	return
}

func (c *browseConn4) WriteQuery(b []byte, iface *net.Interface) error {
	if err := c.SetMulticastInterface(iface); err != nil {
		return err
	}
	_, err := c.WriteTo(b, nil, mdnsGroupAddrIPv4)
	return err
}

func (c *browseConn6) WriteQuery(b []byte, iface *net.Interface) error {
	if err := c.SetMulticastInterface(iface); err != nil {
		return err
	}
	_, err := c.WriteTo(b, nil, mdnsQueryAddr6(iface))
	return err
}
