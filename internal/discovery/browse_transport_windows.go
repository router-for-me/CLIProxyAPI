//go:build windows

package discovery

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"unsafe"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/windows"
)

// x/net's Windows PacketConn.ReadFrom does not expose control messages. Go's
// UDPConn.ReadMsgUDP uses WSARecvMsg, which does expose IP_PKTINFO/IPV6_PKTINFO.
// Keep the existing multicast socket setup, but receive through that API so
// the cache can enforce the same interface/destination checks as on Unix.
type windowsBrowseConn struct {
	udp *net.UDPConn
	v4  *ipv4.PacketConn
	v6  *ipv6.PacketConn
}

func openBrowseConn4(ifaces []net.Interface) (browseConn, error) {
	conn, err := openMDNSConn4(ifaces)
	if err != nil {
		return nil, err
	}
	c := &windowsBrowseConn{udp: conn.PacketConn.(*net.UDPConn), v4: conn}
	if err = enableBrowsePacketInfo(c.udp, false); err != nil {
		return nil, errors.Join(err, c.Close())
	}
	return c, nil
}

func openBrowseConn6(ifaces []net.Interface) (browseConn, error) {
	conn, err := openMDNSConn6(ifaces)
	if err != nil {
		return nil, err
	}
	c := &windowsBrowseConn{udp: conn.PacketConn.(*net.UDPConn), v6: conn}
	if err = enableBrowsePacketInfo(c.udp, true); err != nil {
		return nil, errors.Join(err, c.Close())
	}
	return c, nil
}

func enableBrowsePacketInfo(conn *net.UDPConn, v6 bool) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	level, option := windows.IPPROTO_IP, windows.IP_PKTINFO
	if v6 {
		level, option = windows.IPPROTO_IPV6, windows.IPV6_PKTINFO
	}
	var optionErr error
	if err = raw.Control(func(fd uintptr) {
		optionErr = windows.SetsockoptInt(windows.Handle(fd), level, option, 1)
	}); err != nil {
		return err
	}
	if optionErr != nil {
		return fmt.Errorf("enable browse packet info: %w", optionErr)
	}
	return nil
}

func (c *windowsBrowseConn) Close() error { return c.udp.Close() }

func (c *windowsBrowseConn) WriteQuery(wire []byte, iface *net.Interface) error {
	if c.v4 != nil {
		if err := c.v4.SetMulticastInterface(iface); err != nil {
			return err
		}
		_, err := c.v4.WriteTo(wire, nil, mdnsGroupAddrIPv4)
		return err
	}
	if err := c.v6.SetMulticastInterface(iface); err != nil {
		return err
	}
	// An explicit zone also supplies the scope required for link-local multicast.
	_, err := c.v6.WriteTo(wire, nil, mdnsQueryAddr6(iface))
	return err
}

func (c *windowsBrowseConn) ReadPacket(buf []byte) (int, int, *net.UDPAddr, net.IP, error) {
	var oob [256]byte
	for {
		n, oobn, flags, src, err := c.udp.ReadMsgUDP(buf, oob[:])
		if err != nil {
			return 0, 0, nil, nil, err
		}
		// Never admit a payload or metadata fragment to the cache.
		if flags&(windows.MSG_TRUNC|windows.MSG_CTRUNC) != 0 {
			continue
		}
		index, dst, err := parseBrowsePacketInfo(oob[:oobn], c.v6 != nil)
		if err != nil {
			return 0, 0, nil, nil, err
		}
		return n, index, src, dst, nil
	}
}

// WSACMSGHDR has a SIZE_T length, followed by two INTs. WSA_CMSG_DATA and
// WSA_CMSG_NXTHDR round to SIZE_T alignment. IN_PKTINFO/IN6_PKTINFO contain
// destination bytes followed by a native-order ULONG interface index.
// Layouts match x/sys/windows and Microsoft's ws2def.h/ws2ipdef.h definitions:
// https://learn.microsoft.com/en-us/windows/win32/api/ws2def/ns-ws2def-wsacmsghdr
// https://learn.microsoft.com/en-us/windows/win32/api/ws2ipdef/ns-ws2ipdef-in_pktinfo
func parseBrowsePacketInfo(oob []byte, v6 bool) (int, net.IP, error) {
	word := int(unsafe.Sizeof(uintptr(0)))
	header := int(unsafe.Sizeof(windows.WSACMSGHDR{}))
	align := func(n int) int { return (n + word - 1) &^ (word - 1) }
	index := 0
	var dst net.IP
	for len(oob) >= header {
		var size uint64
		if word == 8 {
			size = binary.NativeEndian.Uint64(oob[:8])
		} else {
			size = uint64(binary.NativeEndian.Uint32(oob[:4]))
		}
		if size < uint64(header) || size > uint64(len(oob)) {
			return 0, nil, fmt.Errorf("invalid browse packet-info control length")
		}
		level := int32(binary.NativeEndian.Uint32(oob[word : word+4]))
		kind := int32(binary.NativeEndian.Uint32(oob[word+4 : word+8]))
		ipLen := 0
		if !v6 && level == windows.IPPROTO_IP && kind == windows.IP_PKTINFO {
			ipLen = net.IPv4len
		}
		if v6 && level == windows.IPPROTO_IPV6 && kind == windows.IPV6_PKTINFO {
			ipLen = net.IPv6len
		}
		if ipLen != 0 {
			data := oob[align(header):int(size)]
			if len(data) < ipLen+4 {
				return 0, nil, fmt.Errorf("short browse packet-info control data")
			}
			if dst != nil {
				return 0, nil, fmt.Errorf("duplicate browse packet-info control data")
			}
			dst = append(net.IP(nil), data[:ipLen]...)
			index = int(binary.NativeEndian.Uint32(data[ipLen : ipLen+4]))
		}
		next := align(int(size))
		if next > len(oob) {
			break
		}
		oob = oob[next:]
	}
	if index <= 0 || dst == nil {
		return 0, nil, fmt.Errorf("missing browse interface or destination metadata")
	}
	return index, dst, nil
}
