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

func TestFingerprintChromeDialerSOCKS5HIsNotDirect(t *testing.T) {
	t.Parallel()

	constructed := NewFingerprintRoundTripper("socks5h://127.0.0.1:1", http.DefaultTransport)
	roundTripper, ok := constructed.(*fallbackRoundTripper)
	if !ok {
		t.Fatalf("type = %T, want *fallbackRoundTripper", constructed)
	}
	chrome, ok := roundTripper.chrome.(*utlsRoundTripper)
	if !ok {
		t.Fatalf("chrome type = %T, want *utlsRoundTripper", roundTripper.chrome)
	}
	if chrome.dialer == nil {
		t.Fatal("socks5h chrome path configured a nil dialer")
	}
	if chrome.dialer == proxy.Direct {
		t.Fatal("socks5h chrome path silently fell back to proxy.Direct")
	}

	directConstructed := NewFingerprintRoundTripper("direct", http.DefaultTransport)
	directTripper, ok := directConstructed.(*fallbackRoundTripper)
	if !ok {
		t.Fatalf("direct type = %T, want *fallbackRoundTripper", directConstructed)
	}
	directChrome, ok := directTripper.chrome.(*utlsRoundTripper)
	if !ok {
		t.Fatalf("direct chrome type = %T, want *utlsRoundTripper", directTripper.chrome)
	}
	if directChrome.dialer != proxy.Direct {
		t.Fatalf("direct chrome dialer = %T, want proxy.Direct", directChrome.dialer)
	}
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
		Timeout:   2 * time.Second,
	}

	// chatgpt.com is required to select the chrome fingerprint path
	// (IsChatGPTUpstreamURL). The local SOCKS listener records CONNECT and
	// does not dial the requested host, so this stays offline.
	resp, errGet := client.Get("https://chatgpt.com/")
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	} else if errGet == nil {
		t.Fatal("expected GET through the local SOCKS sink to fail")
	}

	select {
	case got := <-connects:
		if got.Host != "chatgpt.com" {
			t.Fatalf("proxy CONNECT host = %q, want chatgpt.com", got.Host)
		}
		if got.Port != 443 {
			t.Fatalf("proxy CONNECT port = %d, want 443", got.Port)
		}
		if got.ATYP != "domain" {
			t.Fatalf("proxy CONNECT atyp = %q, want domain (socks5h remote DNS)", got.ATYP)
		}
	case <-time.After(time.Second):
		t.Fatal("socks5h proxy received no CONNECT; fingerprint path bypassed the proxy")
	}
}

func TestFingerprintRoundTripperDirectMissesSOCKS(t *testing.T) {
	connects := make(chan socksConnectLog, 8)
	_ = startLoggingSOCKS5(t, connects)

	// Dial a local closed port through the chrome path so Direct never leaves
	// the machine. A chatgpt.com GET would hit the public internet via proxy.Direct.
	local := startClosedLocalAddr(t)
	client := &http.Client{
		Transport: NewChromeRoundTripper("direct"),
		Timeout:   2 * time.Second,
	}
	resp, errGet := client.Get("https://" + local + "/")
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	} else if errGet == nil {
		t.Fatal("expected direct GET to the closed local address to fail")
	}

	select {
	case got := <-connects:
		t.Fatalf("direct fingerprint path unexpectedly used SOCKS: %s", got)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestChromeRoundTripperSOCKS5HDialsLocalHost(t *testing.T) {
	connects := make(chan socksConnectLog, 8)
	proxyAddr := startLoggingSOCKS5(t, connects)

	client := &http.Client{
		Transport: NewChromeRoundTripper("socks5h://" + proxyAddr),
		Timeout:   2 * time.Second,
	}
	resp, errGet := client.Get("https://chatgpt.local.test/")
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	} else if errGet == nil {
		t.Fatal("expected GET through the local SOCKS sink to fail")
	}

	select {
	case got := <-connects:
		if got.Host != "chatgpt.local.test" {
			t.Fatalf("proxy CONNECT host = %q, want chatgpt.local.test", got.Host)
		}
		if got.Port != 443 {
			t.Fatalf("proxy CONNECT port = %d, want 443", got.Port)
		}
		if got.ATYP != "domain" {
			t.Fatalf("proxy CONNECT atyp = %q, want domain (socks5h remote DNS)", got.ATYP)
		}
	case <-time.After(time.Second):
		t.Fatal("socks5h chrome path received no CONNECT")
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

func startClosedLocalAddr(t *testing.T) string {
	t.Helper()

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen local sink: %v", errListen)
	}
	addr := listener.Addr().String()
	if errClose := listener.Close(); errClose != nil {
		t.Fatalf("close local sink: %v", errClose)
	}
	return addr
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
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

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
	// Do not dial dest.Host: unit tests must stay offline even when the
	// chrome path CONNECTs chatgpt.com for IsChatGPTUpstreamURL routing.
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
