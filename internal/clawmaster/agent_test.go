package clawmaster

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func agentTestCA(t *testing.T, dir string) string {
	t.Helper()
	_, caPEM := forwarderGymCA(t, "claw-master agent test")
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, caPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAgentTokenIsCreatedPrivateAndKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.token")
	first, err := agentToken(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %v, want 0600", info.Mode().Perm())
	}
	second, err := agentToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) < 16 {
		t.Fatalf("token changed across starts or is short: %q then %q", first, second)
	}
}

func TestAgentTokenRefusesAFileOthersCanReadOrThatIsNotAToken(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]func(path string) error{
		"malformed": func(path string) error { return os.WriteFile(path, []byte("short\n"), 0o600) },
		"separator": func(path string) error { return os.WriteFile(path, []byte("abcdefghijklmnop:qrstuvwxyz\n"), 0o600) },
		"directory": func(path string) error { return os.Mkdir(path, 0o700) },
		"oversized": func(path string) error { return os.WriteFile(path, []byte(strings.Repeat("a", 300)), 0o600) },
		"empty":     func(path string) error { return os.WriteFile(path, nil, 0o600) },
		// Characters a URL's userinfo would decode or end at.
		"percent":  func(path string) error { return os.WriteFile(path, []byte("abcdefghijklmnop%41\n"), 0o600) },
		"fragment": func(path string) error { return os.WriteFile(path, []byte("abcdefghijklmnop#\n"), 0o600) },
		"query":    func(path string) error { return os.WriteFile(path, []byte("abcdefghijklmnop?\n"), 0o600) },
	}
	if runtime.GOOS != "windows" {
		cases["readable by others"] = func(path string) error {
			return os.WriteFile(path, []byte("abcdefghijklmnopqrstuvwxyz\n"), 0o644)
		}
		cases["symlink"] = func(path string) error {
			target := path + ".target"
			if err := os.WriteFile(target, []byte("abcdefghijklmnopqrstuvwxyz\n"), 0o600); err != nil {
				return err
			}
			return os.Symlink(target, path)
		}
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
			if err := setup(path); err != nil {
				t.Fatal(err)
			}
			if token, err := agentToken(path); err == nil {
				t.Fatalf("agentToken accepted it: %q", token)
			}
		})
	}
}

func TestAgentTokenCreatedConcurrentlyIsOneCompleteToken(t *testing.T) {
	for round := 0; round < 20; round++ {
		path := filepath.Join(t.TempDir(), "agent.token")
		const starters = 8
		tokens := make(chan string, starters)
		errs := make(chan error, starters)
		start := make(chan struct{})
		for i := 0; i < starters; i++ {
			go func() {
				<-start
				token, err := agentToken(path)
				tokens <- token
				errs <- err
			}()
		}
		close(start)
		seen := map[string]bool{}
		for i := 0; i < starters; i++ {
			if err := <-errs; err != nil {
				t.Fatalf("round %d: a concurrent start failed: %v", round, err)
			}
			seen[<-tokens] = true
		}
		if len(seen) != 1 {
			t.Fatalf("round %d: concurrent starts got %d different tokens", round, len(seen))
		}
		entries, err := os.ReadDir(filepath.Dir(path))
		if err != nil || len(entries) != 1 {
			t.Fatalf("round %d: the directory holds %d entries (%v), want only the token", round, len(entries), err)
		}
	}
}

func TestAgentListensOnLoopbackOnlyAndNeedsAServer(t *testing.T) {
	dir := t.TempDir()
	ca := agentTestCA(t, dir)
	for _, opts := range []AgentOptions{
		{Listen: "0.0.0.0:41000", Open: "127.0.0.1:41001", CAFile: ca},
		{Listen: "localhost:41000", Open: "127.0.0.1:41001", CAFile: ca},
		{Listen: "127.0.0.1:41000", Open: "192.0.2.1:41001", CAFile: ca},
		{Listen: "127.0.0.1:41000", Open: "127.0.0.1:41000", CAFile: ca},
		{Listen: "127.0.0.1:41000", Open: "127.0.0.1:041000", CAFile: ca},
		{Listen: "127.0.0.1:0", Open: "127.0.0.1:41001", CAFile: ca},
		{Listen: "127.0.0.1:65536", Open: "127.0.0.1:41001", CAFile: ca},
		{Listen: "127.0.0.1:http", Open: "127.0.0.1:41001", CAFile: ca},
		{Listen: "127.0.0.1:41000", Open: "127.0.0.1:41001"},
		{Listen: "127.0.0.1:41000", Open: "127.0.0.1:41001", CAFile: filepath.Join(dir, "missing.pem")},
		{Listen: "127.0.0.1:41000"},
	} {
		if _, err := resolveAgent(opts); err == nil {
			t.Errorf("resolveAgent(%+v) succeeded, want an error", opts)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "agent.token")); err == nil {
		t.Fatal("a refused configuration created a token")
	}
}

func TestAgentEnvironmentNamesTheProxyTheCAAndLoopbackExceptions(t *testing.T) {
	dir := t.TempDir()
	ca := agentTestCA(t, dir)
	opts := AgentOptions{Listen: "127.0.0.1:41000", Open: "127.0.0.1:41001", CAFile: ca}
	lines, err := AgentEnvironment(opts)
	if err != nil {
		t.Fatal(err)
	}
	token, err := agentToken(filepath.Join(dir, "agent.token"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"HTTPS_PROXY=http://claw-master:" + token + "@127.0.0.1:41000",
		"NODE_EXTRA_CA_CERTS=" + ca,
		"NO_PROXY=localhost,127.0.0.1,::1",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("AgentEnvironment =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	again, err := AgentEnvironment(opts)
	if err != nil || again[0] != lines[0] {
		t.Fatalf("the proxy URL changed between calls: %q, %v", again, err)
	}
}

func agentTestConnect(t *testing.T, address, authority, password string) (*http.Response, net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	auth := ""
	if password != "" {
		auth = "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(forwarderUser+":"+password)) + "\r\n"
	}
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n%s\r\n", authority, authority, auth); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	return resp, conn, reader
}

func TestAgentSendsAnthropicTrafficOnlyToTheServerAndFailsClosed(t *testing.T) {
	logs := captureLogs(t, "info")
	dir := t.TempDir()
	ca := agentTestCA(t, dir)
	server := startForwarderTestEcho(t, true)
	direct := startForwarderTestEcho(t, false)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := listener.Addr().String()
	endpoint, err := resolveAgent(AgentOptions{Listen: listen, Open: server.addr(), CAFile: ca})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveAgent(ctx, listener, endpoint, nil) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serveAgent: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("serveAgent did not return after its context ended")
		}
	})
	token, err := agentToken(filepath.Join(dir, "agent.token"))
	if err != nil {
		t.Fatal(err)
	}

	resp, conn, reader := agentTestConnect(t, listen, "api.anthropic.com:443", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorized CONNECT = %d, want 200", resp.StatusCode)
	}
	forwarderTestEchoes(t, conn, reader, "through the pool")
	if seen := server.seen(); len(seen) == 0 || seen[len(seen)-1] != "CONNECT api.anthropic.com:443" {
		t.Fatalf("the server saw %q, want the tunnel to api.anthropic.com", seen)
	}

	// Without the token, Anthropic traffic is refused, never sent with this box's own login.
	before := len(server.seen())
	for _, password := range []string{"", "not-the-agent-token"} {
		if resp, _, _ := agentTestConnect(t, listen, "api.anthropic.com:443", password); resp.StatusCode != http.StatusProxyAuthRequired {
			t.Fatalf("CONNECT with password %q = %d, want 407", password, resp.StatusCode)
		}
	}
	if len(server.seen()) != before {
		t.Fatal("an unauthorized CONNECT reached the server")
	}

	// Other hosts (OAuth refresh, telemetry) still go directly.
	resp, conn, reader = agentTestConnect(t, listen, direct.addr(), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT to another host = %d, want 200", resp.StatusCode)
	}
	forwarderTestEchoes(t, conn, reader, "direct")

	// With the server gone, the agent answers 502 and says why in its log.
	_ = server.listener.Close()
	if resp, _, _ := agentTestConnect(t, listen, "api.anthropic.com:443", token); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("CONNECT with the server down = %d, want 502", resp.StatusCode)
	}
	for _, want := range []string{"could not reach the claw-master server", "without the agent token"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log does not say %q:\n%s", want, logs.String())
		}
	}
}
