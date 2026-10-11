package claudemaster

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// forwarderTestEcho accepts connections and echoes them; it records each connection's first line
// when expectConnect is set, answering a CONNECT with 200 first, as a claude-master proxy does.
type forwarderTestEcho struct {
	listener net.Listener
	mu       sync.Mutex
	connects []string
}

func startForwarderTestEcho(t *testing.T, expectConnect bool) *forwarderTestEcho {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e := &forwarderTestEcho{listener: listener}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				reader := bufio.NewReader(conn)
				if expectConnect {
					req, err := http.ReadRequest(reader)
					if err != nil {
						return
					}
					e.mu.Lock()
					e.connects = append(e.connects, req.Method+" "+req.RequestURI)
					e.mu.Unlock()
					if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
						return
					}
				}
				_, _ = io.Copy(conn, reader)
			}()
		}
	}()
	return e
}

func (e *forwarderTestEcho) addr() string { return e.listener.Addr().String() }

func (e *forwarderTestEcho) seen() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.connects...)
}

func (e *forwarderTestEcho) dialer() upstreamDialer { return plainUpstream(e.addr()) }

func startForwarderForTest(t *testing.T, upstream upstreamDialer) *localForwarder {
	t.Helper()
	f, err := startLocalForwarder(upstream, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// forwarderTestConnect writes a CONNECT (plus any pipelined bytes) and returns the response and the
// connection, positioned after the response.
func forwarderTestConnect(t *testing.T, f *localForwarder, authority, password, pipelined string) (*http.Response, net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", f.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second)) // a stuck exchange fails, it does not hang
	auth := ""
	if password != "" {
		auth = "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(forwarderUser+":"+password)) + "\r\n"
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n%s\r\n%s", authority, authority, auth, pipelined); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	return resp, conn, reader
}

func forwarderTestEchoes(t *testing.T, conn net.Conn, reader *bufio.Reader, message string) {
	t.Helper()
	if _, err := io.WriteString(conn, message); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(message))
	if _, err := io.ReadFull(reader, got); err != nil || string(got) != message {
		t.Fatalf("tunnel returned %q, %v; want %q", got, err, message)
	}
}

func forwarderNoUpstream(t *testing.T) upstreamDialer {
	return func(context.Context) (net.Conn, error) {
		t.Error("the claude-master proxy was dialled")
		return nil, errors.New("unexpected upstream dial")
	}
}

func TestForwarderURLCarriesTheTokenForLoopbackOnly(t *testing.T) {
	f := startForwarderForTest(t, forwarderNoUpstream(t))
	u, err := url.Parse(f.URL())
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if u.Scheme != "http" || u.User.Username() != forwarderUser || password != f.token || len(f.token) < 26 {
		t.Fatalf("forwarder URL = %s://%s:<%d chars>@..., want http with the token", u.Scheme, u.User.Username(), len(password))
	}
	if host, _, _ := net.SplitHostPort(u.Host); host != "127.0.0.1" {
		t.Fatalf("forwarder listens on %s, want IPv4 loopback", host)
	}
	other := startForwarderForTest(t, forwarderNoUpstream(t))
	if other.token == f.token {
		t.Fatal("two launches share a token")
	}
}

func TestForwarderTunnelsOtherHostsDirectlyWithoutAToken(t *testing.T) {
	target := startForwarderTestEcho(t, false)
	f := startForwarderForTest(t, forwarderNoUpstream(t))
	resp, conn, reader := forwarderTestConnect(t, f, target.addr(), "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT = %d, want 200", resp.StatusCode)
	}
	forwarderTestEchoes(t, conn, reader, "ordinary traffic")
}

func TestForwarderSendsAuthorizedAnthropicTrafficToTheMaster(t *testing.T) {
	master := startForwarderTestEcho(t, true)
	f := startForwarderForTest(t, master.dialer())
	for _, authority := range []string{"api.anthropic.com:443", "API.Anthropic.com.:443", "api.anthropic.com:0443"} {
		resp, conn, reader := forwarderTestConnect(t, f, authority, f.token, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("CONNECT %s = %d, want 200", authority, resp.StatusCode)
		}
		forwarderTestEchoes(t, conn, reader, "inference for "+authority)
	}
	if got := master.seen(); len(got) != 3 || got[0] != "CONNECT api.anthropic.com:443" || got[1] != got[0] || got[2] != got[0] {
		t.Fatalf("the master saw %q, want three canonical CONNECTs", got)
	}
	forwarderTestDrains(t, f)
}

// forwarderTestDrains closes the test's client connections and waits until the forwarder tracks
// none: every finished tunnel must let go of both its ends.
func forwarderTestDrains(t *testing.T, f *localForwarder) {
	t.Helper()
	f.mu.Lock()
	clients := make([]net.Conn, 0, len(f.conns))
	for conn := range f.conns {
		clients = append(clients, conn)
	}
	f.mu.Unlock()
	for _, conn := range clients {
		_ = conn.Close()
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.conns)
		f.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d connections still tracked after every tunnel ended", n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestForwarderSendsAnthropicTrafficWithoutTheTokenDirect(t *testing.T) {
	master := startForwarderTestEcho(t, true)
	direct := startForwarderTestEcho(t, false)
	f := startForwarderForTest(t, master.dialer())
	var mu sync.Mutex
	var dialled []string
	f.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		mu.Lock()
		dialled = append(dialled, address)
		mu.Unlock()
		return (&net.Dialer{}).DialContext(ctx, network, direct.addr())
	}
	for _, password := range []string{"", "wrong", f.token + "x", strings.ToLower(f.token)} {
		resp, conn, reader := forwarderTestConnect(t, f, "api.anthropic.com:443", password, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("CONNECT = %d, want 200", resp.StatusCode)
		}
		forwarderTestEchoes(t, conn, reader, "direct")
	}
	// The right token on another port, or on a name with more than one trailing dot, is not the
	// master's either; nor is a credential with two spaces after Basic.
	for _, authority := range []string{"api.anthropic.com:8443", "api.anthropic.com..:443"} {
		resp, conn, reader := forwarderTestConnect(t, f, authority, f.token, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("CONNECT %s = %d, want 200", authority, resp.StatusCode)
		}
		forwarderTestEchoes(t, conn, reader, "direct")
	}
	if got := master.seen(); len(got) != 0 {
		t.Fatalf("the master saw %q without the token", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dialled) != 6 || dialled[0] != "api.anthropic.com:443" || dialled[4] != "api.anthropic.com:8443" || dialled[5] != "api.anthropic.com..:443" {
		t.Fatalf("dialled %q", dialled)
	}
}

func TestForwarderCarriesBytesPipelinedAfterConnect(t *testing.T) {
	master := startForwarderTestEcho(t, true)
	f := startForwarderForTest(t, master.dialer())
	resp, conn, reader := forwarderTestConnect(t, f, "api.anthropic.com:443", f.token, "client-hello")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT = %d, want 200", resp.StatusCode)
	}
	got := make([]byte, len("client-hello"))
	if _, err := io.ReadFull(reader, got); err != nil || string(got) != "client-hello" {
		t.Fatalf("pipelined bytes came back as %q, %v", got, err)
	}
	forwarderTestEchoes(t, conn, reader, "then more")
}

func TestForwarderRejectsWhatIsNotATunnel(t *testing.T) {
	f := startForwarderForTest(t, forwarderNoUpstream(t))
	resp, err := http.Get("http://" + f.listener.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", resp.StatusCode)
	}
	for _, authority := range []string{"example.com", ":443", "example.com:0", "example.com:65536", "example.com:https", "api.anthropic.com:+443", "api.anthropic.com:-443"} {
		resp, _, _ := forwarderTestConnect(t, f, authority, "", "")
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("CONNECT %q = %d, want 400", authority, resp.StatusCode)
		}
	}
}

func TestForwarderAnswers502WhenTheFarEndFails(t *testing.T) {
	refusing := startForwarderTestEcho(t, false)
	_ = refusing.listener.Close() // nothing listens there now
	f := startForwarderForTest(t, func(context.Context) (net.Conn, error) { return nil, errors.New("server down") })
	if resp, _, _ := forwarderTestConnect(t, f, refusing.addr(), "", ""); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("CONNECT to a closed port = %d, want 502", resp.StatusCode)
	}
	if resp, _, _ := forwarderTestConnect(t, f, "api.anthropic.com:443", f.token, ""); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("CONNECT with the master down = %d, want 502", resp.StatusCode)
	}

	// A master that refuses the CONNECT is a 502 too, and its connection is not left open.
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	closed := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_, _ = http.ReadRequest(bufio.NewReader(conn))
		_, _ = io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
		_, _ = io.Copy(io.Discard, conn)
		close(closed)
	}()
	refused := startForwarderForTest(t, plainUpstream(listener.Addr().String()))
	if resp, _, _ := forwarderTestConnect(t, refused, "api.anthropic.com:443", refused.token, ""); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("CONNECT the master refused = %d, want 502", resp.StatusCode)
	}
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the refused upstream connection was left open")
	}
	refused.mu.Lock()
	defer refused.mu.Unlock()
	if len(refused.conns) != 0 {
		t.Fatalf("%d connections still tracked after a refused tunnel", len(refused.conns))
	}
}

func TestForwarderCloseEndsTunnelsEvenMidHandshake(t *testing.T) {
	// This master accepts and then never answers the CONNECT.
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	target := startForwarderTestEcho(t, false)
	f, err := startLocalForwarder(plainUpstream(listener.Addr().String()), "")
	if err != nil {
		t.Fatal(err)
	}
	// One tunnel is live; another waits on the silent master.
	resp, live, reader := forwarderTestConnect(t, f, target.addr(), "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT = %d", resp.StatusCode)
	}
	forwarderTestEchoes(t, live, reader, "live")
	stuck, err := net.Dial("tcp", f.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stuck.Close() }()
	_ = stuck.SetDeadline(time.Now().Add(10 * time.Second))
	auth := base64.StdEncoding.EncodeToString([]byte(forwarderUser + ":" + f.token))
	if _, err := fmt.Fprintf(stuck, "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\nProxy-Authorization: Basic %s\r\n\r\n", auth); err != nil {
		t.Fatal(err)
	}
	var upstream net.Conn
	select {
	case upstream = <-accepted:
		defer func() { _ = upstream.Close() }()
	case <-time.After(10 * time.Second):
		t.Fatal("the forwarder never dialled the master")
	}

	done := make(chan error, 1)
	go func() { done <- f.Close() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close hung on a tunnel waiting for the master")
	}
	for name, conn := range map[string]net.Conn{"live": live, "stuck": stuck} {
		if _, err := conn.Read(make([]byte, 1)); err == nil {
			t.Errorf("the %s tunnel is still open after Close", name)
		}
	}
	// Accept on the closed listener, not a dial: the freed port may already be someone else's.
	if conn, err := f.listener.Accept(); err == nil {
		_ = conn.Close()
		t.Error("the forwarder still accepts after Close")
	}
	if err := f.Close(); err != nil {
		t.Errorf("a second Close = %v", err)
	}
}

// The forwarder in front of the real proxy, as `run` wires them: Claude's request reaches the
// inference handler through the forwarder, which presents the client certificate Claude no longer has.
func TestForwarderReachesTheProxyWithTheLaunchCertificate(t *testing.T) {
	var got string
	p, _, pool := proxyTestStart(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Method + " " + r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}), nil)
	f := startForwarderForTest(t, tlsUpstream(p.Addr(), proxyTestAuthFor(p).clientTLS(nil)))
	proxyURL, err := url.Parse(f.URL())
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: proxyTestAuthFor(p).clientTLS(pool)}
	transport.TLSClientConfig.Certificates = nil // Claude holds no client certificate now
	t.Cleanup(transport.CloseIdleConnections)
	resp, body := proxyTestRequest(t, &http.Client{Transport: transport}, http.MethodPost, "/v1/messages", `{"model":"claude-test-model"}`, http.Header{"Content-Type": {"application/json"}})
	if resp.StatusCode != http.StatusOK || body != `{"ok":true}` || got != "POST /v1/messages" {
		t.Fatalf("through the forwarder: %d %q, handler saw %q", resp.StatusCode, body, got)
	}
	if snapshot := p.Snapshot(); snapshot.ConnectAccepted != 1 || snapshot.InferenceRequests != 1 {
		t.Fatalf("proxy snapshot = %+v, want one accepted CONNECT and one inference", snapshot)
	}
}

func TestForwarderReadsCredentialsAsNetHTTPDoes(t *testing.T) {
	master := startForwarderTestEcho(t, true)
	direct := startForwarderTestEcho(t, false)
	f := startForwarderForTest(t, master.dialer())
	f.dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, direct.addr())
	}
	credential := base64.StdEncoding.EncodeToString([]byte(forwarderUser + ":" + f.token))
	for header, wantMaster := range map[string]bool{
		"Basic " + credential:  true,
		"BASIC " + credential:  true,
		"Basic  " + credential: false,
		"Basic\t" + credential: false,
		"Bearer " + f.token:    false,
	} {
		conn, err := net.Dial("tcp", f.listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		before := len(master.seen())
		if _, err := fmt.Fprintf(conn, "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\nProxy-Authorization: %s\r\n\r\n", header); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(conn)
		resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("%q: %v", header, err)
		}
		forwarderTestEchoes(t, conn, reader, "x")
		if got := len(master.seen()) > before; got != wantMaster {
			t.Errorf("Proxy-Authorization %q reached the master = %v, want %v", header[:7], got, wantMaster)
		}
		_ = conn.Close()
	}
}

// forwarderTestReplyAfterEOF is a far end that reads until the client has finished sending, then
// replies with how much it read: what a client that half-closes depends on.
func startForwarderTestReplyAfterEOF(t *testing.T, expectConnect bool) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				reader := bufio.NewReader(conn)
				if expectConnect {
					if _, err := http.ReadRequest(reader); err != nil {
						return
					}
					if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
						return
					}
				}
				n, _ := io.Copy(io.Discard, reader)
				_, _ = fmt.Fprintf(conn, "read %d bytes", n)
			}()
		}
	}()
	return listener.Addr().String()
}

func TestForwarderCarriesTheReplyAfterAClientHalfCloses(t *testing.T) {
	directAddr := startForwarderTestReplyAfterEOF(t, false)
	masterAddr := startForwarderTestReplyAfterEOF(t, true)
	f := startForwarderForTest(t, plainUpstream(masterAddr))
	for _, tc := range []struct{ authority, password string }{
		{directAddr, ""},
		{"api.anthropic.com:443", f.token},
	} {
		resp, conn, reader := forwarderTestConnect(t, f, tc.authority, tc.password, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("CONNECT %s = %d", tc.authority, resp.StatusCode)
		}
		if _, err := io.WriteString(conn, strings.Repeat("q", 100000)); err != nil {
			t.Fatal(err)
		}
		if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
			t.Fatal(err)
		}
		reply, err := io.ReadAll(reader)
		if err != nil || string(reply) != "read 100000 bytes" {
			t.Fatalf("%s: after a half-close the reply was %q, %v", tc.authority, reply, err)
		}
	}
	forwarderTestDrains(t, f)
}
