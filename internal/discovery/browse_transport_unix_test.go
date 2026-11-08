//go:build !windows

package discovery

import (
	"bytes"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"net"
	"testing"
	"time"
)

// Verify mandatory receive metadata without requiring a routed LAN multicast.
func TestUnixBrowsePacketInfoLoopback(t *testing.T) {
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
			var transport browseConn
			if v6 {
				pc := ipv6.NewPacketConn(conn)
				err = pc.SetControlMessage(ipv6.FlagInterface|ipv6.FlagDst, true)
				transport = &browseConn6{pc}
			} else {
				pc := ipv4.NewPacketConn(conn)
				err = pc.SetControlMessage(ipv4.FlagInterface|ipv4.FlagDst, true)
				transport = &browseConn4{pc}
			}
			if err != nil {
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
			buf := make([]byte, 1024)
			n, index, src, dst, err := transport.ReadPacket(buf)
			if err != nil {
				t.Fatal(err)
			}
			if index <= 0 || !dst.Equal(ip) || src == nil || !src.IP.Equal(ip) || !bytes.Equal(buf[:n], payload) {
				t.Fatalf("n=%d index=%d src=%v dst=%v payload=%q", n, index, src, dst, buf[:n])
			}

		})
	}
}
