//go:build windows

package discovery

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/net/ipv6"
	"golang.org/x/sys/windows"
)

func browseControlFixture(level, kind int, data []byte) []byte {
	word := int(unsafe.Sizeof(uintptr(0)))
	header := int(unsafe.Sizeof(windows.WSACMSGHDR{}))
	size := header + len(data)
	b := make([]byte, (size+word-1)&^(word-1))
	if word == 8 {
		binary.NativeEndian.PutUint64(b, uint64(size))
	} else {
		binary.NativeEndian.PutUint32(b, uint32(size))
	}
	binary.NativeEndian.PutUint32(b[word:], uint32(level))
	binary.NativeEndian.PutUint32(b[word+4:], uint32(kind))
	copy(b[header:], data)
	return b
}

func TestParseBrowsePacketInfo(t *testing.T) {
	for _, v6 := range []bool{false, true} {
		name, level, kind, ip := "IPv4", windows.IPPROTO_IP, windows.IP_PKTINFO, net.IP{224, 0, 0, 251}
		if v6 {
			name, level, kind, ip = "IPv6", windows.IPPROTO_IPV6, windows.IPV6_PKTINFO, net.ParseIP("ff02::fb").To16()
		}
		t.Run(name, func(t *testing.T) {
			data := append([]byte(nil), ip...)
			data = binary.NativeEndian.AppendUint32(data, 17)
			valid := browseControlFixture(level, kind, data)
			unknown := browseControlFixture(999, 999, []byte{1})
			for _, raw := range [][]byte{valid, append(unknown, valid...)} {
				index, dst, err := parseBrowsePacketInfo(raw, v6)
				if err != nil || index != 17 || !dst.Equal(ip) {
					t.Fatalf("got index=%d dst=%v err=%v", index, dst, err)
				}
				// The destination must not alias the read buffer reused for the next packet.
				copyRaw := append([]byte(nil), raw...)
				for i := range raw {
					raw[i] = 0
				}
				if !dst.Equal(ip) {
					t.Fatal("destination aliases input")
				}
				copy(raw, copyRaw)
			}
			malformed := []struct {
				name string
				raw  []byte
			}{
				{"missing", nil}, {"unknown only", unknown}, {"short header", valid[:3]},
				{"short data", browseControlFixture(level, kind, data[:len(data)-1])},
				{"zero index", browseControlFixture(level, kind, append(append([]byte(nil), ip...), 0, 0, 0, 0))},
				{"duplicate", append(append([]byte(nil), valid...), valid...)},
			}
			zeroLength := append([]byte(nil), valid...)
			for i := 0; i < int(unsafe.Sizeof(uintptr(0))); i++ {
				zeroLength[i] = 0
			}
			hugeLength := append([]byte(nil), valid...)
			for i := 0; i < int(unsafe.Sizeof(uintptr(0))); i++ {
				hugeLength[i] = 255
			}
			malformed = append(malformed, struct {
				name string
				raw  []byte
			}{"zero length", zeroLength}, struct {
				name string
				raw  []byte
			}{"oversize length", hugeLength})
			for _, tc := range malformed {
				t.Run(tc.name, func(t *testing.T) {
					if _, _, err := parseBrowsePacketInfo(tc.raw, v6); err == nil {
						t.Fatal("accepted invalid control message")
					}
				})
			}
			if _, _, err := parseBrowsePacketInfo(valid, !v6); err == nil {
				t.Fatal("accepted metadata for wrong address family")
			}
		})
	}
}

// Exercise real WSARecvMsg metadata without depending on LAN multicast access.
// This test must fail rather than silently skip if Windows metadata breaks.
func TestWindowsBrowsePacketInfoLoopback(t *testing.T) {
	for _, network := range []string{"udp4", "udp6"} {
		t.Run(network, func(t *testing.T) {
			ip := net.IPv4(127, 0, 0, 1)
			v6 := network == "udp6"
			if v6 {
				ip = net.IPv6loopback
			}
			conn, err := net.ListenUDP(network, &net.UDPAddr{IP: ip})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := conn.Close(); err != nil {
					t.Error(err)
				}
			})
			if err = enableBrowsePacketInfo(conn, v6); err != nil {
				t.Fatal(err)
			}
			if err = conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			sender, err := net.DialUDP(network, nil, conn.LocalAddr().(*net.UDPAddr))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := sender.Close(); err != nil {
					t.Error(err)
				}
			})
			payload := []byte("browse packet-info test")
			if _, err = sender.Write(payload); err != nil {
				t.Fatal(err)
			}
			c := &windowsBrowseConn{udp: conn}
			if v6 {
				c.v6 = ipv6.NewPacketConn(conn)
			}
			buf := make([]byte, 1024)
			n, index, src, dst, err := c.ReadPacket(buf)
			if err != nil {
				t.Fatal(err)
			}
			if index <= 0 || !dst.Equal(ip) || src == nil || !src.IP.Equal(ip) || !bytes.Equal(buf[:n], payload) {
				t.Fatalf("n=%d index=%d src=%v dst=%v payload=%q", n, index, src, dst, buf[:n])
			}
			if _, err := net.InterfaceByIndex(index); err != nil {
				t.Fatalf("received invalid interface index: %v", err)
			}
		})
	}
}
