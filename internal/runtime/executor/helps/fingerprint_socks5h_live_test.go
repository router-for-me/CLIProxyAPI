package helps

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

type socksConnectLog struct {
	ATYP string
	Host string
	Port uint16
}

func (c socksConnectLog) String() string {
	return fmt.Sprintf("CONNECT %s %s:%d", c.ATYP, c.Host, c.Port)
}

func TestFingerprintRoundTripperSOCKS5HUsesProxy(t *testing.T) {
	connects := make(chan socksConnectLog, 8)
	proxyAddr := startLoggingSOCKS5(t, connects)

	fallback := utlsClientRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Errorf("fallback used for %s; expected chrome fingerprint path", req.URL)
		return nil, errors.New("fallback should not handle chatgpt.com")
	})

	proxyURL := "socks5h://" + proxyAddr
	client := &http.Client{
		Transport: NewFingerprintRoundTripper(proxyURL, fallback),
		Timeout:   12 * time.Second,
	}

	resp, errGet := client.Get("https://chatgpt.com/")
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		t.Logf("GET https://chatgpt.com/ via socks5h status=%d", resp.StatusCode)
	} else if errGet != nil {
		t.Logf("GET https://chatgpt.com/ via socks5h error (ok if CONNECT was logged): %v", errGet)
	}

	select {
	case got := <-connects:
		t.Logf("socks5h proxy log: %s", got)
		if got.Host != "chatgpt.com" {
			t.Fatalf("proxy CONNECT host = %q, want chatgpt.com", got.Host)
		}
		if got.Port != 443 {
			t.Fatalf("proxy CONNECT port = %d, want 443", got.Port)
		}
		if got.ATYP != "domain" {
			t.Fatalf("proxy CONNECT atyp = %q, want domain (socks5h remote DNS)", got.ATYP)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("socks5h proxy received no CONNECT; fingerprint path bypassed the proxy")
	}
}

func TestFingerprintRoundTripperDirectMissesSOCKS(t *testing.T) {
	connects := make(chan socksConnectLog, 8)
	_ = startLoggingSOCKS5(t, connects)

	client := &http.Client{
		Transport: NewFingerprintRoundTripper("direct", http.DefaultTransport),
		Timeout:   8 * time.Second,
	}
	resp, errGet := client.Get("https://chatgpt.com/")
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		t.Logf("GET https://chatgpt.com/ via direct status=%d", resp.StatusCode)
	} else if errGet != nil {
		t.Logf("GET https://chatgpt.com/ via direct error: %v", errGet)
	}

	select {
	case got := <-connects:
		t.Fatalf("direct fingerprint path unexpectedly used SOCKS: %s", got)
	case <-time.After(200 * time.Millisecond):
		t.Log("direct fingerprint path: proxy received no CONNECT (bypass)")
	}
}

func TestFromURLCurrentlyAcceptsSOCKS5H(t *testing.T) {
	t.Parallel()

	parsed, errParse := url.Parse("socks5h://127.0.0.1:1080")
	if errParse != nil {
		t.Fatalf("url.Parse: %v", errParse)
	}
	dialer, errFromURL := proxy.FromURL(parsed, proxy.Direct)
	if errFromURL != nil {
		t.Logf("FromURL(socks5h) error (older x/net behavior): %v", errFromURL)
		return
	}
	t.Logf("FromURL(socks5h) succeeded with %T; BuildDialer still uses SOCKS5 helper to match BuildHTTPTransport", dialer)
}

func startLoggingSOCKS5(t *testing.T, connects chan<- socksConnectLog) string {
	t.Helper()

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen SOCKS5: %v", errListen)
	}
	t.Cleanup(func() {
		if errClose := listener.Close(); errClose != nil {
			t.Errorf("close SOCKS5 listener: %v", errClose)
		}
	})

	go func() {
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go handleLoggingSOCKS5(conn, connects)
		}
	}()

	return listener.Addr().String()
}

func handleLoggingSOCKS5(conn net.Conn, connects chan<- socksConnectLog) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))

	dest, errRead := readSOCKS5Connect(conn)
	if errRead != nil {
		return
	}
	select {
	case connects <- dest:
	default:
	}

	if _, errWrite := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); errWrite != nil {
		return
	}

	upstream, errDial := net.DialTimeout("tcp", net.JoinHostPort(dest.Host, strconv.Itoa(int(dest.Port))), 8*time.Second)
	if errDial != nil {
		return
	}
	defer func() { _ = upstream.Close() }()

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(upstream, conn)
		close(done)
	}()
	_, _ = io.Copy(conn, upstream)
	<-done
}

func readSOCKS5Connect(conn net.Conn) (socksConnectLog, error) {
	reader := bufio.NewReader(conn)

	ver, errVer := reader.ReadByte()
	if errVer != nil {
		return socksConnectLog{}, errVer
	}
	nmethods, errMethods := reader.ReadByte()
	if errMethods != nil {
		return socksConnectLog{}, errMethods
	}
	if ver != 0x05 {
		return socksConnectLog{}, fmt.Errorf("socks version %d", ver)
	}
	if _, errDiscard := io.ReadFull(reader, make([]byte, nmethods)); errDiscard != nil {
		return socksConnectLog{}, errDiscard
	}
	if _, errWrite := conn.Write([]byte{0x05, 0x00}); errWrite != nil {
		return socksConnectLog{}, errWrite
	}

	header := make([]byte, 4)
	if _, errHeader := io.ReadFull(reader, header); errHeader != nil {
		return socksConnectLog{}, errHeader
	}
	if header[0] != 0x05 || header[1] != 0x01 {
		return socksConnectLog{}, fmt.Errorf("socks request ver=%d cmd=%d", header[0], header[1])
	}

	dest := socksConnectLog{}
	switch header[3] {
	case 0x01:
		ip := make([]byte, 4)
		if _, errIP := io.ReadFull(reader, ip); errIP != nil {
			return socksConnectLog{}, errIP
		}
		dest.ATYP = "ipv4"
		dest.Host = net.IP(ip).String()
	case 0x03:
		length, errLen := reader.ReadByte()
		if errLen != nil {
			return socksConnectLog{}, errLen
		}
		name := make([]byte, length)
		if _, errName := io.ReadFull(reader, name); errName != nil {
			return socksConnectLog{}, errName
		}
		dest.ATYP = "domain"
		dest.Host = string(name)
	case 0x04:
		ip := make([]byte, 16)
		if _, errIP := io.ReadFull(reader, ip); errIP != nil {
			return socksConnectLog{}, errIP
		}
		dest.ATYP = "ipv6"
		dest.Host = net.IP(ip).String()
	default:
		return socksConnectLog{}, fmt.Errorf("socks atyp %d", header[3])
	}

	portBytes := make([]byte, 2)
	if _, errPort := io.ReadFull(reader, portBytes); errPort != nil {
		return socksConnectLog{}, errPort
	}
	dest.Port = binary.BigEndian.Uint16(portBytes)
	return dest, nil
}
