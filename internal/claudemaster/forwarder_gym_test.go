package claudemaster

// The forwarder gym runs the real Connect, in both of its modes, against a fake claude-master
// server and a fake Claude, with fake hosts standing in for the internet. The fake Claude is this
// test binary run again (TestForwarderGymHelper) through a `claude` shim on PATH; it does what a
// session does through its proxy: inference, a tool that fetches from other hosts, a Claude started
// by a command, and it outlives the server it was talking to. Each step writes what it saw to a
// report the gym checks once Connect returns.

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Not CLAUDE_MASTER_*: ChildEnvironment keeps those from Claude.
const (
	forwarderGymRole     = "FORWARDER_GYM_ROLE"
	forwarderGymReport   = "FORWARDER_GYM_REPORT"
	forwarderGymOriginCA = "FORWARDER_GYM_ORIGIN_CA"
	forwarderGymPublicCA = "FORWARDER_GYM_PUBLIC_CA"
	forwarderGymBinary   = "FORWARDER_GYM_BINARY"

	forwarderGymOrigin     = "origin.example"
	forwarderGymStreamSize = 2 << 20
	forwarderGymExitCode   = 3
)

// forwarderGymStream is the body the master's inference returns: large enough to cross many
// relay reads, and checkable by its digest.
func forwarderGymStream() []byte {
	body := make([]byte, forwarderGymStreamSize)
	for i := range body {
		body[i] = byte(i*7 + i/4096)
	}
	return body
}

// forwarderGymCA makes a CA and a leaf for names; it returns the leaf and the CA's PEM.
func forwarderGymCA(t *testing.T, names ...string) (tls.Certificate, []byte) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "gym CA " + names[0]},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: key}, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}

// forwarderGymHost is a fake internet host: TLS under its own CA, counting what it serves.
type forwarderGymHost struct {
	listener net.Listener
	caPath   string
	mu       sync.Mutex
	paths    []string
}

func startForwarderGymHost(t *testing.T, dir string, handler func(w http.ResponseWriter, r *http.Request), names ...string) *forwarderGymHost {
	t.Helper()
	leaf, caPEM := forwarderGymCA(t, names...)
	h := &forwarderGymHost{caPath: filepath.Join(dir, names[0]+"-ca.pem")}
	if err := os.WriteFile(h.caPath, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp4", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{leaf}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	h.listener = listener
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.paths = append(h.paths, r.Method+" "+r.URL.RequestURI())
		h.mu.Unlock()
		handler(w, r)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return h
}

func (h *forwarderGymHost) served() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.paths...)
}

// forwarderGymMaster is the fake claude-master server: the real server proxy with a stand-in
// inference backend that records who asked.
type forwarderGymMaster struct {
	stateDir string
	proxy    *Proxy
	mu       sync.Mutex
	calls    []string
}

func startForwarderGymMaster(t *testing.T) *forwarderGymMaster {
	t.Helper()
	m := &forwarderGymMaster{stateDir: filepath.Join(canonicalTestTempDir(t), "state")}
	certs, err := loadOrCreatePersistentCertificate(m.stateDir, []net.IP{net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	stream := forwarderGymStream()
	inference := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Who string `json:"who"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.calls = append(m.calls, body.Who+"@"+clientFromContext(r.Context())+" "+r.Method+" "+r.URL.Path)
		m.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for offset := 0; offset < len(stream); offset += 32 << 10 {
			if _, err := w.Write(stream[offset:min(offset+32<<10, len(stream))]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	})
	m.proxy, err = startServerProxy(certs, "127.0.0.1:0", "127.0.0.1:0", inference)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.proxy.Close() })
	return m
}

func (m *forwarderGymMaster) seen() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

// forwarderGymReportLines reads the report as key=value lines.
func forwarderGymReportLines(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		key, value, _ := strings.Cut(line, "=")
		out[key] = value
	}
	return out
}

func TestForwarderGym(t *testing.T) {
	for _, mode := range []string{"certificate", "open"} {
		t.Run(mode, func(t *testing.T) { forwarderGymRun(t, mode) })
	}
}

func forwarderGymRun(t *testing.T, mode string) {
	dir := canonicalTestTempDir(t)
	master := startForwarderGymMaster(t)
	origin := startForwarderGymHost(t, dir, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stop-master" {
			// Synchronous: the server is gone before the session hears back.
			_ = master.proxy.Close()
		}
		_, _ = io.WriteString(w, "origin:"+r.URL.Query().Get("i"))
	}, forwarderGymOrigin)
	public := startForwarderGymHost(t, dir, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "public-anthropic")
	}, masterAPIHost)

	// The internet, as far as the forwarder can tell: two hosts, and nothing else.
	var dialMu sync.Mutex
	var dialled []string
	previous := forwarderDial
	forwarderDial = func(ctx context.Context, network, address string) (net.Conn, error) {
		dialMu.Lock()
		dialled = append(dialled, address)
		dialMu.Unlock()
		switch address {
		case forwarderGymOrigin + ":443", forwarderGymOrigin + ":8443":
			return (&net.Dialer{}).DialContext(ctx, network, origin.listener.Addr().String())
		case masterAPIHost + ":443":
			return (&net.Dialer{}).DialContext(ctx, network, public.listener.Addr().String())
		}
		return nil, fmt.Errorf("the gym has no host %s", address)
	}
	t.Cleanup(func() { forwarderDial = previous })
	var started []*localForwarder
	forwarderStarted = func(f *localForwarder) { started = append(started, f) }
	t.Cleanup(func() { forwarderStarted = nil })

	// The fake Claude: this test binary, through a `claude` shim on PATH.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	shim := "#!/bin/sh\nexec \"$" + forwarderGymBinary + "\" -test.run='^TestForwarderGymHelper$' -test.count=1 -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(dir, "report")
	home := filepath.Join(dir, "home")
	work := filepath.Join(dir, "work")
	for _, d := range []string{home, work} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv(forwarderGymRole, "claude")
	t.Setenv(forwarderGymReport, report)
	t.Setenv(forwarderGymOriginCA, origin.caPath)
	t.Setenv(forwarderGymPublicCA, public.caPath)
	t.Setenv(forwarderGymBinary, self)
	t.Chdir(work)

	opts := ConnectOptions{Open: master.proxy.OpenAddr(), CAFile: filepath.Join(master.stateDir, persistentCAFile)}
	wantClient := "tunnel"
	if mode == "certificate" {
		opts = ConnectOptions{Server: master.proxy.Addr(), Dir: issueTestClient(t, master.stateDir, "gym-box", 30)}
		wantClient = "gym-box"
	}
	// A test-process limit: a gym that hangs fails instead.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	code, err := Connect(ctx, opts, []string{"--print", "gym"})
	if err != nil || code != forwarderGymExitCode {
		t.Fatalf("Connect = %d, %v; want Claude's own exit code %d", code, err, forwarderGymExitCode)
	}

	got := forwarderGymReportLines(t, report)
	want := map[string]string{
		// Claude's environment: the forwarder, the CA for the master's api.anthropic.com, no certificate.
		"claude_proxy":       "http loopback with token",
		"claude_ca":          "set",
		"claude_client_cert": "absent",
		// Inference reaches the master and its stream arrives whole.
		"claude_inference": fmt.Sprintf("ok %d bytes", forwarderGymStreamSize),
		// A tool's traffic is relayed blind: it verifies each host's own certificate.
		"tool_origin_concurrent": "8 ok",
		"tool_origin_other_port": "ok",
		// Without the token, api.anthropic.com is the real one, dialled from this box.
		"tool_anthropic_without_token": "public-anthropic",
		// With the token it is the master's, which a client without the launch CA refuses.
		"tool_anthropic_with_token": "refused: not the public certificate",
		"nested_inference":          fmt.Sprintf("ok %d bytes", forwarderGymStreamSize),
		// The server going away mid-session is a 502 from the forwarder, not a hang.
		"claude_after_master_stopped": "502",
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}

	if calls := master.seen(); len(calls) != 2 || calls[0] != "claude@"+wantClient+" POST /v1/messages" || calls[1] != "nested@"+wantClient+" POST /v1/messages" {
		t.Errorf("the master served %q, want claude then nested, as %s", calls, wantClient)
	}
	served := origin.served()
	if len(served) != 10 || served[len(served)-1] != "GET /stop-master" {
		t.Errorf("the origin served %q, want 8 concurrent, one on another port, then the stop", served)
	}
	if served := public.served(); len(served) != 1 || served[0] != "GET /v1/models" {
		t.Errorf("the public api.anthropic.com served %q, want one direct request", served)
	}
	dialMu.Lock()
	for _, address := range dialled {
		if !strings.HasPrefix(address, forwarderGymOrigin+":") && address != masterAPIHost+":443" {
			t.Errorf("the forwarder dialled %s", address)
		}
	}
	dialMu.Unlock()

	// The launch started one forwarder, the one Claude was given, and it went away with Claude:
	// its listener is closed (checked by Accept, not a dial to a port that may be reused) and it
	// tracks no connection.
	if len(started) != 1 || started[0].listener.Addr().String() != got["claude_forwarder"] {
		t.Fatalf("the launch started %d forwarders; Claude was given %s", len(started), got["claude_forwarder"])
	}
	if conn, err := started[0].listener.Accept(); err == nil {
		_ = conn.Close()
		t.Errorf("the forwarder still accepts after Claude exited")
	}
	started[0].mu.Lock()
	if n := len(started[0].conns); n != 0 {
		t.Errorf("the forwarder tracks %d connections after Claude exited", n)
	}
	started[0].mu.Unlock()
}

// TestForwarderGymHelper is the fake Claude and the commands it runs; outside the gym it does nothing.
func TestForwarderGymHelper(t *testing.T) {
	role := os.Getenv(forwarderGymRole)
	if role == "" {
		return
	}
	report, err := os.OpenFile(os.Getenv(forwarderGymReport), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = report.Close() }()
	record := func(key, format string, args ...any) {
		if _, err := fmt.Fprintf(report, "%s=%s\n", key, fmt.Sprintf(format, args...)); err != nil {
			t.Fatal(err)
		}
	}
	switch role {
	case "claude":
		forwarderGymClaude(t, record)
		_ = report.Close()
		os.Exit(forwarderGymExitCode)
	case "nested":
		record("nested_inference", "%s", forwarderGymInference(t, "nested"))
	case "tool":
		forwarderGymTool(t, record)
	default:
		t.Fatalf("unknown gym role %q", role)
	}
}

func forwarderGymClaude(t *testing.T, record func(key, format string, args ...any)) {
	proxyURL, err := url.Parse(os.Getenv("HTTPS_PROXY"))
	if err != nil {
		t.Fatal(err)
	}
	password, hasPassword := proxyURL.User.Password()
	host, _, _ := net.SplitHostPort(proxyURL.Host)
	if proxyURL.Scheme == "http" && host == "127.0.0.1" && hasPassword && password != "" && os.Getenv("https_proxy") == os.Getenv("HTTPS_PROXY") {
		record("claude_proxy", "http loopback with token")
	} else {
		record("claude_proxy", "unexpected %s://%s", proxyURL.Scheme, proxyURL.Host)
	}
	record("claude_forwarder", "%s", proxyURL.Host)
	if os.Getenv("NODE_EXTRA_CA_CERTS") != "" {
		record("claude_ca", "set")
	}
	if os.Getenv("CLAUDE_CODE_CLIENT_CERT") == "" && os.Getenv("CLAUDE_CODE_CLIENT_KEY") == "" {
		record("claude_client_cert", "absent")
	} else {
		record("claude_client_cert", "present")
	}

	record("claude_inference", "%s", forwarderGymInference(t, "claude"))

	// A command Claude runs, and a Claude a command starts, inherit Claude's environment.
	tool := exec.Command(os.Args[0], os.Args[1:]...)
	tool.Env = append(os.Environ(), forwarderGymRole+"=tool")
	if out, err := tool.CombinedOutput(); err != nil {
		t.Fatalf("tool: %v\n%s", err, out)
	}
	nested := exec.Command("claude", "--print", "nested")
	nested.Env = append(os.Environ(), forwarderGymRole+"=nested")
	if out, err := nested.CombinedOutput(); err != nil {
		t.Fatalf("nested claude: %v\n%s", err, out)
	}

	if _, err := forwarderGymGet(forwarderGymTransport(t, http.ProxyFromEnvironment, os.Getenv(forwarderGymOriginCA)), "https://"+forwarderGymOrigin+"/stop-master"); err != nil {
		t.Fatalf("stop-master: %v", err)
	}
	if result := forwarderGymInference(t, "claude-after-stop"); strings.Contains(result, "Bad Gateway") {
		record("claude_after_master_stopped", "502")
	} else {
		record("claude_after_master_stopped", "%s", result)
	}
}

func forwarderGymTool(t *testing.T, record func(key, format string, args ...any)) {
	origin := forwarderGymTransport(t, http.ProxyFromEnvironment, os.Getenv(forwarderGymOriginCA))
	var wg sync.WaitGroup
	results := make([]string, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, err := forwarderGymGet(origin, fmt.Sprintf("https://%s/hello?i=%d", forwarderGymOrigin, i))
			if err != nil {
				results[i] = err.Error()
				return
			}
			results[i] = body
		}()
	}
	wg.Wait()
	ok := 0
	for i, body := range results {
		if body == fmt.Sprintf("origin:%d", i) {
			ok++
		}
	}
	record("tool_origin_concurrent", "%d ok", ok)
	if body, err := forwarderGymGet(origin, "https://"+forwarderGymOrigin+":8443/other-port?i=p"); err == nil && body == "origin:p" {
		record("tool_origin_other_port", "ok")
	} else {
		record("tool_origin_other_port", "%q %v", body, err)
	}

	// An SDK that trusts only the public certificates, with and without the forwarder's token.
	withoutToken := func(r *http.Request) (*url.URL, error) {
		u, err := http.ProxyFromEnvironment(r)
		if u != nil {
			stripped := *u
			stripped.User = nil
			u = &stripped
		}
		return u, err
	}
	if body, err := forwarderGymGet(forwarderGymTransport(t, withoutToken, os.Getenv(forwarderGymPublicCA)), "https://"+masterAPIHost+"/v1/models"); err == nil {
		record("tool_anthropic_without_token", "%s", body)
	} else {
		record("tool_anthropic_without_token", "%v", err)
	}
	_, err := forwarderGymGet(forwarderGymTransport(t, http.ProxyFromEnvironment, os.Getenv(forwarderGymPublicCA)), "https://"+masterAPIHost+"/v1/models")
	var unknown x509.UnknownAuthorityError
	if errors.As(err, &unknown) {
		record("tool_anthropic_with_token", "refused: not the public certificate")
	} else {
		record("tool_anthropic_with_token", "%v", err)
	}
}

// forwarderGymInference posts as Claude does: through HTTPS_PROXY, trusting NODE_EXTRA_CA_CERTS,
// with no client certificate. It returns "ok N bytes" when the stream arrived intact.
func forwarderGymInference(t *testing.T, who string) string {
	transport := forwarderGymTransport(t, http.ProxyFromEnvironment, os.Getenv("NODE_EXTRA_CA_CERTS"))
	defer transport.CloseIdleConnections()
	resp, err := (&http.Client{Transport: transport}).Post("https://"+masterAPIHost+"/v1/messages", "application/json", strings.NewReader(`{"who":"`+who+`"}`))
	if err != nil {
		return err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(bufio.NewReader(resp.Body))
	if err != nil || resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("status %d, %v", resp.StatusCode, err)
	}
	want := sha256.Sum256(forwarderGymStream())
	if got := sha256.Sum256(body); got != want {
		return "corrupted stream " + hex.EncodeToString(got[:4])
	}
	return fmt.Sprintf("ok %d bytes", len(body))
}

func forwarderGymTransport(t *testing.T, proxy func(*http.Request) (*url.URL, error), caPath string) *http.Transport {
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Transport{Proxy: proxy, TLSClientConfig: &tls.Config{RootCAs: x509CertPool(caPEM), MinVersion: tls.VersionTLS12}}
}

func forwarderGymGet(transport *http.Transport, target string) (string, error) {
	resp, err := (&http.Client{Transport: transport}).Get(target)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}
