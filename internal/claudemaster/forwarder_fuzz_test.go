package claudemaster

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// forwarderTestPair returns the two ends of an in-memory Unix socket pair: fuzzing on these uses no
// ports (a TCP connection per input exhausts the ephemeral range), and either end can half-close.
// Like package net, it holds ForkLock and sets close-on-exec, so a child started meanwhile (the gym's
// fake Claude) cannot inherit an end and keep it from reaching EOF.
func forwarderTestPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	syscall.ForkLock.RLock()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fds[0])
		syscall.CloseOnExec(fds[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatal(err)
	}
	ends := make([]*net.UnixConn, 2)
	for i, fd := range fds {
		file := os.NewFile(uintptr(fd), "forwarder-pair")
		conn, err := net.FileConn(file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
		ends[i] = conn.(*net.UnixConn)
	}
	return ends[0], ends[1]
}

// forwarderTestFarEnd answers like a far end: as the master it first answers the CONNECT; then it
// sends a one-byte banner and echoes until the forwarder half-closes.
func forwarderTestFarEnd(conn *net.UnixConn, banner byte, master bool) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	if master {
		if _, err := http.ReadRequest(reader); err != nil {
			return
		}
		if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
	}
	if _, err := conn.Write([]byte{banner}); err != nil {
		return
	}
	_, _ = io.Copy(conn, reader)
}

// forwarderOracleRoutesToMaster states the routing rule independently of the forwarder: a CONNECT
// whose port is the number 443 and whose host is api.anthropic.com (any case, at most one trailing
// dot), carrying the token as the password of Basic credentials as net/http reads them.
func forwarderOracleRoutesToMaster(r *http.Request, token string) bool {
	if r.Method != http.MethodConnect {
		return false
	}
	host, port, err := net.SplitHostPort(r.RequestURI)
	if err != nil {
		return false
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n != 443 {
		return false
	}
	switch strings.ToLower(host) {
	case "api.anthropic.com", "api.anthropic.com.":
	default:
		return false
	}
	// net/http's own Basic parser, fed the first Proxy-Authorization.
	credentials := &http.Request{Header: http.Header{}}
	if value := r.Header.Values("Proxy-Authorization"); len(value) > 0 {
		credentials.Header.Set("Authorization", value[0])
	}
	_, password, ok := credentials.BasicAuth()
	return ok && password == token
}

// FuzzForwarderRequest sends arbitrary bytes to a live forwarder. "TOKENB64" in the input stands for
// the forwarder's real credential and "TOKEN" for its token, so the fuzzer can reach the authorized
// path. Whatever arrives, the
// forwarder must not reach the claude-master proxy unless the oracle says the request may, must
// answer with HTTP or nothing, and must shut down cleanly. Plain `go test` runs only the seeds.
func FuzzForwarderRequest(f *testing.F) {
	// TOKENB64 stands for base64("claude-master:<token>"), the credential as Claude sends it.
	auth := func(password string) string {
		if password == "TOKEN" {
			return "Proxy-Authorization: Basic TOKENB64\r\n"
		}
		return "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(forwarderUser+":"+password)) + "\r\n"
	}
	for _, seed := range []string{
		"CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n" + auth("TOKEN") + "\r\n",
		"CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n\r\n",
		"CONNECT API.ANTHROPIC.COM.:443 HTTP/1.1\r\nHost: x\r\n" + auth("TOKEN") + "\r\nclient-hello",
		"CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: x\r\n" + auth("TOKENx") + "\r\n",
		"CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: x\r\n" + auth("wrong") + auth("TOKEN") + "\r\n",
		"CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: x\r\nProxy-Authorization: Bearer TOKEN\r\n\r\n",
		"CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: x\r\nProxy-Authorization: basic !!!\r\n\r\n",
		"CONNECT api.anthropic.com:8443 HTTP/1.1\r\nHost: x\r\n" + auth("TOKEN") + "\r\n",
		"CONNECT api.anthropic.com:0443 HTTP/1.1\r\nHost: x\r\n" + auth("TOKEN") + "\r\nafter",
		"CONNECT example.com:443 HTTP/1.1\r\nHost: x\r\n\r\nbytes the far end must echo back",
		"CONNECT api.anthropic.com.evil:443 HTTP/1.1\r\nHost: x\r\n" + auth("TOKEN") + "\r\n",
		"CONNECT [::1]:443 HTTP/1.1\r\nHost: x\r\n\r\n",
		"CONNECT example.com:0 HTTP/1.1\r\nHost: x\r\n\r\n",
		"CONNECT example.com HTTP/1.1\r\nHost: x\r\n\r\n",
		"CONNECT example.com:443 HTTP/1.0\r\n\r\n",
		"GET http://api.anthropic.com/ HTTP/1.1\r\nHost: api.anthropic.com\r\n" + auth("TOKEN") + "\r\n",
		"CONNECT api.anthropic.com:443 HTTP/1.1\r\n" + auth("TOKEN") + "\r\n",
		"\x16\x03\x01\x02\x00\x01\x00\x01\xfc\x03\x03",
		"CONNECT a:1 HTTP/1.1\r\nHost: a\r\nX: " + strings.Repeat("y", 8192) + "\r\n\r\n",
		"",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var masterDials, directDials atomic.Int32
		pairs := &proxyListener{connections: make(chan net.Conn), done: make(chan struct{}), addr: &net.UnixAddr{Name: "pair", Net: "unix"}}
		fw := serveLocalForwarder(pairs, func(context.Context) (net.Conn, error) {
			masterDials.Add(1)
			near, far := forwarderTestPair(t)
			go forwarderTestFarEnd(far, 'M', true)
			return near, nil
		}, "")
		fw.dial = func(context.Context, string, string) (net.Conn, error) {
			directDials.Add(1)
			near, far := forwarderTestPair(t)
			go forwarderTestFarEnd(far, 'D', false)
			return near, nil
		}
		credential := base64.StdEncoding.EncodeToString([]byte(forwarderUser + ":" + fw.token))
		data = bytes.ReplaceAll(data, []byte("TOKENB64"), []byte(credential))
		data = bytes.ReplaceAll(data, []byte("TOKEN"), []byte(fw.token))

		conn, server := forwarderTestPair(t)
		pairs.connections <- server
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second)) // a stuck exchange fails, it does not hang
		_, _ = conn.Write(data)
		_ = conn.CloseWrite()
		response, _ := io.ReadAll(conn)
		_ = conn.Close()
		closed := make(chan error, 1)
		go func() { closed <- fw.Close() }()
		select {
		case <-closed:
		case <-time.After(10 * time.Second):
			t.Fatalf("Close hung after input %q", data)
		}

		if len(response) > 0 && !bytes.HasPrefix(response, []byte("HTTP/1.")) {
			t.Fatalf("answered %q to %q", response, data)
		}
		allowed := false
		var rest []byte
		reader := bufio.NewReader(bytes.NewReader(data))
		if r, err := http.ReadRequest(reader); err == nil {
			allowed = forwarderOracleRoutesToMaster(r, fw.token)
			rest, _ = io.ReadAll(reader) // what the client sent after its request
		}
		if masterDials.Load() > 0 && !allowed {
			t.Fatalf("reached the claude-master proxy for %q", data)
		}
		if directDials.Load() > 0 && allowed {
			t.Fatalf("sent an authorized request for %q past the claude-master proxy", data)
		}
		if masterDials.Load()+directDials.Load() > 1 {
			t.Fatalf("one request dialled %d times", masterDials.Load()+directDials.Load())
		}
		// net/http may refuse a request before the forwarder sees it (a bad header, say); the
		// forwarder's own answers are 200, 405, "invalid CONNECT authority" and 502.
		established := []byte("HTTP/1.1 200 Connection Established\r\n\r\n")
		ownAnswer := bytes.HasPrefix(response, established) || bytes.HasPrefix(response, []byte("HTTP/1.1 405 ")) ||
			bytes.Contains(response, []byte("invalid CONNECT authority")) || bytes.HasPrefix(response, []byte("HTTP/1.1 502 "))
		if allowed && ownAnswer && masterDials.Load() != 1 {
			t.Fatalf("an authorized request %q did not reach the claude-master proxy: %q", data, response)
		}
		// A tunnel carries everything the client sent after its request, and the client's
		// half-close, to the far end its request names, and the far end's whole reply back.
		if bytes.HasPrefix(response, established) {
			banner := byte('D')
			if masterDials.Load() == 1 {
				banner = 'M'
			}
			want := append(append(append([]byte(nil), established...), banner), rest...)
			if !bytes.Equal(response, want) {
				t.Fatalf("tunnel for %q returned %q, want %q", data, response, want)
			}
		}
		fw.mu.Lock()
		defer fw.mu.Unlock()
		if len(fw.conns) != 0 {
			t.Fatalf("%d connections tracked after Close", len(fw.conns))
		}
	})
}

// forwarderChaosEnd is a far end that announces itself with a one-byte banner, then echoes; as the
// master it first answers the CONNECT, or refuses it.
type forwarderChaosEnd struct {
	listener net.Listener
	banner   byte
	refuse   atomic.Bool
	stall    atomic.Bool   // the next CONNECT is read and never answered
	stalled  chan struct{} // receives once per stalled CONNECT
}

func startForwarderChaosEnd(t *testing.T, banner byte, master bool) *forwarderChaosEnd {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e := &forwarderChaosEnd{listener: listener, banner: banner, stalled: make(chan struct{}, 64)}
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
				if master {
					if _, err := http.ReadRequest(reader); err != nil {
						return
					}
					if e.stall.CompareAndSwap(true, false) {
						e.stalled <- struct{}{}
						_, _ = io.Copy(io.Discard, reader) // until the forwarder hangs up
						return
					}
					if e.refuse.Load() {
						_, _ = io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
						return
					}
					if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
						return
					}
				}
				if _, err := conn.Write([]byte{e.banner}); err != nil {
					return
				}
				_, _ = io.Copy(conn, reader)
			}()
		}
	}()
	return e
}

// TestForwarderSeededChaos drives a forwarder through a reproducible random mix of tunnels, garbage,
// abandoned and half-finished clients, dead far ends, refusing masters and concurrent bursts, then
// closes it mid-traffic. Every tunnel must reach the far end its request names; nothing may be left
// tracked. A failure names the seed and step, which replays it exactly.
func TestForwarderSeededChaos(t *testing.T) {
	for seed := uint64(1); seed <= 12; seed++ {
		t.Run(fmt.Sprint("seed-", seed), func(t *testing.T) { forwarderChaos(t, seed, 48) })
	}
}

func forwarderChaos(t *testing.T, seed uint64, steps int) {
	rng := rand.New(rand.NewPCG(seed, 0x6f72776172646572))
	master := startForwarderChaosEnd(t, 'M', true)
	direct := startForwarderChaosEnd(t, 'D', false)
	public := startForwarderChaosEnd(t, 'P', false)

	fw, err := startLocalForwarder(plainUpstream(master.listener.Addr().String()), "")
	if err != nil {
		t.Fatal(err)
	}
	fw.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == "dead.example:443" {
			// Refused outright: a closed port of our own could be handed to another listener.
			return nil, syscall.ECONNREFUSED
		}
		target := map[string]string{
			"direct.example:443":    direct.listener.Addr().String(),
			"api.anthropic.com:443": public.listener.Addr().String(),
		}[address]
		if target == "" {
			return nil, fmt.Errorf("no chaos host %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, target)
	}
	authHeader := "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(forwarderUser+":"+fw.token)) + "\r\n"

	type tunnel struct {
		conn   net.Conn
		reader *bufio.Reader
	}
	where := func(step int, what string) string { return fmt.Sprintf("seed %d step %d (%s)", seed, step, what) }
	// open writes a CONNECT and returns the status, and the tunnel after reading the far end's banner.
	open := func(step int, authority string, withToken bool, pipelined []byte) (int, *tunnel) {
		conn, err := net.Dial("tcp", fw.listener.Addr().String())
		if err != nil {
			t.Fatalf("%s: dial: %v", where(step, authority), err)
		}
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		header := ""
		if withToken {
			header = authHeader
		}
		request := append([]byte(fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n%s\r\n", authority, authority, header)), pipelined...)
		if _, err := conn.Write(request); err != nil {
			t.Fatalf("%s: write: %v", where(step, authority), err)
		}
		reader := bufio.NewReader(conn)
		resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
		if err != nil {
			t.Fatalf("%s: response: %v", where(step, authority), err)
		}
		if resp.StatusCode != http.StatusOK {
			_ = conn.Close()
			return resp.StatusCode, nil
		}
		return resp.StatusCode, &tunnel{conn: conn, reader: reader}
	}
	expectBanner := func(step int, what string, tun *tunnel, want byte) {
		got, err := tun.reader.ReadByte()
		if err != nil || got != want {
			t.Fatalf("%s: banner %q, %v; want %q", where(step, what), got, err, want)
		}
	}
	echo := func(step int, what string, tun *tunnel, size int) {
		payload := make([]byte, size)
		for i := range payload {
			payload[i] = byte(rng.IntN(256))
		}
		errs := make(chan error, 1)
		go func() { _, err := tun.conn.Write(payload); errs <- err }()
		got := make([]byte, size)
		if _, err := io.ReadFull(tun.reader, got); err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("%s: echo of %d bytes failed: %v", where(step, what), size, err)
		}
		if err := <-errs; err != nil {
			t.Fatalf("%s: write: %v", where(step, what), err)
		}
	}
	// A tunnel of one of the routable kinds: authority, token, and the banner it must reach.
	kinds := []struct {
		authority string
		token     bool
		banner    byte
	}{
		{"direct.example:443", false, 'D'},
		{"direct.example:443", true, 'D'},
		{"api.anthropic.com:443", true, 'M'},
		{"API.Anthropic.COM.:443", true, 'M'},
		{"api.anthropic.com:443", false, 'P'},
	}

	var held []*tunnel // live tunnels Close must end
	for step := 0; step < steps; step++ {
		switch op := rng.IntN(10); op {
		case 0, 1, 2: // a routable tunnel, used and kept or closed
			k := kinds[rng.IntN(len(kinds))]
			pipelined := []byte(nil)
			if rng.IntN(3) == 0 {
				pipelined = []byte("early")
			}
			status, tun := open(step, k.authority, k.token, pipelined)
			if status != http.StatusOK {
				t.Fatalf("%s: status %d", where(step, k.authority), status)
			}
			expectBanner(step, k.authority, tun, k.banner)
			if pipelined != nil {
				got := make([]byte, len(pipelined))
				if _, err := io.ReadFull(tun.reader, got); err != nil || !bytes.Equal(got, pipelined) {
					t.Fatalf("%s: pipelined bytes came back as %q, %v", where(step, k.authority), got, err)
				}
			}
			echo(step, k.authority, tun, rng.IntN(64<<10)+1)
			if rng.IntN(2) == 0 {
				held = append(held, tun)
			} else {
				_ = tun.conn.Close()
			}
		case 3: // garbage instead of a request
			conn, err := net.Dial("tcp", fw.listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			junk := make([]byte, rng.IntN(512)+1)
			for i := range junk {
				junk[i] = byte(rng.IntN(256))
			}
			_, _ = conn.Write(append(junk, "\r\n\r\n"...))
			response, _ := io.ReadAll(io.LimitReader(conn, 4096))
			if len(response) > 0 && !bytes.HasPrefix(response, []byte("HTTP/1.")) {
				t.Fatalf("%s: answered %q", where(step, "garbage"), response)
			}
			_ = conn.Close()
		case 4: // a client that leaves before hearing back
			conn, err := net.Dial("tcp", fw.listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			_, _ = fmt.Fprintf(conn, "CONNECT direct.example:443 HTTP/1.1\r\nHost: direct.example:443\r\n\r\n")
			_ = conn.Close()
		case 5: // a client that writes half a message and leaves
			k := kinds[rng.IntN(len(kinds))]
			status, tun := open(step, k.authority, k.token, nil)
			if status != http.StatusOK {
				t.Fatalf("%s: status %d", where(step, k.authority), status)
			}
			expectBanner(step, k.authority, tun, k.banner)
			_, _ = tun.conn.Write(make([]byte, rng.IntN(4096)+1))
			_ = tun.conn.Close()
		case 6: // a far end that is down
			if status, _ := open(step, "dead.example:443", false, nil); status != http.StatusBadGateway {
				t.Fatalf("%s: status %d, want 502", where(step, "dead"), status)
			}
		case 7: // a master that refuses
			master.refuse.Store(true)
			status, _ := open(step, "api.anthropic.com:443", true, nil)
			master.refuse.Store(false)
			if status != http.StatusBadGateway {
				t.Fatalf("%s: status %d, want 502", where(step, "refused"), status)
			}
		case 9: // a tunnel left waiting on a master that never answers, until Close
			master.stall.Store(true)
			conn, err := net.Dial("tcp", fw.listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			if _, err := fmt.Fprintf(conn, "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\n%s\r\n", authHeader); err != nil {
				t.Fatal(err)
			}
			select {
			case <-master.stalled:
			case <-time.After(10 * time.Second):
				t.Fatalf("%s: the master never saw the stalled CONNECT", where(step, "stall"))
			}
			held = append(held, &tunnel{conn: conn, reader: bufio.NewReader(conn)})
		case 8: // a concurrent burst of routable tunnels
			n := rng.IntN(6) + 2
			picks := make([]int, n)
			sizes := make([]int, n)
			payloads := make([][]byte, n)
			for i := range picks {
				picks[i], sizes[i] = rng.IntN(len(kinds)), rng.IntN(16<<10)+1
				payloads[i] = make([]byte, sizes[i])
				for j := range payloads[i] {
					payloads[i][j] = byte(rng.IntN(256))
				}
			}
			var wg sync.WaitGroup
			failures := make(chan string, n)
			for i := range picks {
				wg.Add(1)
				go func() {
					defer wg.Done()
					k := kinds[picks[i]]
					conn, err := net.Dial("tcp", fw.listener.Addr().String())
					if err != nil {
						failures <- err.Error()
						return
					}
					defer func() { _ = conn.Close() }()
					_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
					header := ""
					if k.token {
						header = authHeader
					}
					if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n%s\r\n", k.authority, k.authority, header); err != nil {
						failures <- err.Error()
						return
					}
					reader := bufio.NewReader(conn)
					resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
					if err != nil || resp.StatusCode != http.StatusOK {
						failures <- fmt.Sprintf("burst %d: %v", i, err)
						return
					}
					if banner, err := reader.ReadByte(); err != nil || banner != k.banner {
						failures <- fmt.Sprintf("burst %d reached %q, want %q", i, banner, k.banner)
						return
					}
					go func() { _, _ = conn.Write(payloads[i]) }()
					got := make([]byte, sizes[i])
					if _, err := io.ReadFull(reader, got); err != nil || !bytes.Equal(got, payloads[i]) {
						failures <- fmt.Sprintf("burst %d echo: %v", i, err)
					}
				}()
			}
			wg.Wait()
			close(failures)
			for failure := range failures {
				t.Fatalf("%s: %s", where(step, "burst"), failure)
			}
		}
	}

	// Close mid-traffic: every held tunnel ends, nothing stays tracked, nothing accepts.
	closed := make(chan error, 1)
	go func() { closed <- fw.Close() }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatalf("seed %d: Close hung with %d tunnels held", seed, len(held))
	}
	for i, tun := range held {
		_ = tun.conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := io.Copy(io.Discard, tun.reader); err != nil && !errors.Is(err, net.ErrClosed) && !strings.Contains(err.Error(), "reset") {
			t.Errorf("seed %d: held tunnel %d did not end: %v", seed, i, err)
		}
		_ = tun.conn.Close()
	}
	fw.mu.Lock()
	tracked := len(fw.conns)
	fw.mu.Unlock()
	if tracked != 0 {
		t.Fatalf("seed %d: %d connections tracked after Close", seed, tracked)
	}
	// Accept on the closed listener, not a dial: the freed port may already be someone else's.
	if conn, err := fw.listener.Accept(); err == nil {
		_ = conn.Close()
		t.Fatalf("seed %d: the forwarder accepts after Close", seed)
	}
}
