// Package selfupdatee2e runs real proxx binaries through an in-place self-update against a mock GitHub
// release API and a mock OpenAI-compatible upstream. It is skipped unless PROXX_E2E=1:
//
//	PROXX_E2E=1 go test ./test/proxx/selfupdate_e2e/ -run E2E -v -count=1 -timeout 30m
//
// TestE2ELaunchd additionally needs PROXX_E2E_LAUNCHD=1 and macOS (it installs a temporary launchd agent).
package selfupdatee2e

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
)

const (
	clientKey = "test-key"
	mgmtKey   = "mgmt-test-secret"
	selfPkg   = "github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/selfupdate"
)

func requireE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("PROXX_E2E") != "1" {
		t.Skip("set PROXX_E2E=1 to run the self-update e2e")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("in-place self-update runs on linux and macOS only")
	}
}

// ---------------------------------------------------------------------------------------------
// Binaries

type builder struct {
	t       *testing.T
	dir     string
	pubB64  string
	apiBase string
}

// build links the server with a version, the test signing key and the mock update source.
func (b *builder) build(version string, crash bool) string {
	b.t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		b.t.Fatalf("repo root: %v", err)
	}
	out := filepath.Join(b.dir, "proxx-"+version)
	if crash {
		out += "-crash"
	}
	ldflags := fmt.Sprintf("-X main.Version=%s -X %s.publicKeyB64=%s -X %s.apiBaseURL=%s", version, selfPkg, b.pubB64, selfPkg, b.apiBase)
	if crash {
		ldflags += fmt.Sprintf(" -X %s.testCrashOnStart=1", selfPkg)
	}
	cmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", out, "./cmd/server")
	cmd.Dir = root
	if output, errBuild := cmd.CombinedOutput(); errBuild != nil {
		b.t.Fatalf("build %s: %v\n%s", version, errBuild, output)
	}
	return out
}

func makeArchive(t *testing.T, bin string) []byte {
	t.Helper()
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range map[string][]byte{"proxx": data, "LICENSE": []byte("test"), "config.example.yaml": []byte("# test\n")} {
		if err = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err = tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func assetArch() string {
	if runtime.GOARCH == "arm64" {
		return "aarch64"
	}
	return runtime.GOARCH
}

// ---------------------------------------------------------------------------------------------
// Mock GitHub release API

type mockGitHub struct {
	t    *testing.T
	srv  *httptest.Server
	priv ed25519.PrivateKey
	// wrong signs with a different key (bad signature scenario).
	wrong ed25519.PrivateKey

	mu     sync.Mutex
	tag    string
	files  map[string][]byte
	hits   atomic.Int64
	notMod atomic.Int64
}

func newMockGitHub(t *testing.T, ln net.Listener) *mockGitHub {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, wrong, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m := &mockGitHub{t: t, priv: priv, wrong: wrong, files: map[string][]byte{}}
	m.srv = httptest.NewUnstartedServer(http.HandlerFunc(m.serve))
	_ = m.srv.Listener.Close()
	m.srv.Listener = ln
	m.srv.Start()
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockGitHub) pubB64() string {
	return base64.StdEncoding.EncodeToString(m.priv.Public().(ed25519.PublicKey))
}

// publish serves bin as release version. mode: "good", "badsig" or "tampered".
func (m *mockGitHub) publish(version, bin, mode string) {
	m.t.Helper()
	archive := makeArchive(m.t, bin)
	files := map[string][]byte{}
	var sums strings.Builder
	for _, suffix := range []string{"", "_no-plugin"} {
		name := fmt.Sprintf("proxx_%s_%s_%s%s.tar.gz", version, runtime.GOOS, assetArch(), suffix)
		sum := sha256.Sum256(archive)
		files[name] = archive
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	checksums := []byte(sums.String())
	key := m.priv
	if mode == "badsig" {
		key = m.wrong
	}
	files["checksums.txt"] = checksums
	files["checksums.txt.sig"] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, checksums)) + "\n")
	if mode == "tampered" {
		for name, data := range files {
			if strings.HasSuffix(name, ".tar.gz") {
				tampered := append([]byte{}, data...)
				tampered[len(tampered)/2] ^= 0xff
				files[name] = tampered
			}
		}
	}
	m.mu.Lock()
	m.tag = "v" + version
	m.files = files
	m.mu.Unlock()
}

func (m *mockGitHub) releaseJSON() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.files))
	for name := range m.files {
		names = append(names, name)
	}
	sort.Strings(names) // stable JSON, so the ETag only changes with the release
	var assets []string
	for _, name := range names {
		assets = append(assets, fmt.Sprintf(`{"name":%q,"browser_download_url":"%s/download/%s"}`, name, m.srv.URL, name))
	}
	return []byte(fmt.Sprintf(`{"tag_name":%q,"html_url":"%s/release/%s","body":"## Test release %s","prerelease":false,"draft":false,"assets":[%s]}`,
		m.tag, m.srv.URL, m.tag, m.tag, strings.Join(assets, ",")))
}

func (m *mockGitHub) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/repos/iamkunal9/proxx/releases/latest":
		m.hits.Add(1)
		body := m.releaseJSON()
		sum := sha256.Sum256(body)
		etag := `"` + hex.EncodeToString(sum[:8]) + `"`
		if r.Header.Get("If-None-Match") == etag {
			m.notMod.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	case strings.HasPrefix(r.URL.Path, "/download/"):
		m.mu.Lock()
		data, ok := m.files[strings.TrimPrefix(r.URL.Path, "/download/")]
		m.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	default:
		http.NotFound(w, r)
	}
}

// ---------------------------------------------------------------------------------------------
// Mock OpenAI-compatible upstream: model "fast" answers at once; "slow" streams slowChunks chunks
// slowEvery apart.

const (
	slowChunks = 24
	slowEvery  = 250 * time.Millisecond
)

func newMockUpstream(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		model := ojson.GetBytes(raw, "model").String()
		stream := ojson.GetBytes(raw, "stream").Bool()
		if !stream {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"id":"c1","object":"chat.completion","created":1700000000,"model":%q,"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`, model)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		n, every := 3, time.Duration(0)
		if model == "slow" {
			n, every = slowChunks, slowEvery
		}
		for i := 0; i < n; i++ {
			_, _ = fmt.Fprintf(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"content\":\"tok%03d \"},\"finish_reason\":null}]}\n\n", model, i)
			if flusher != nil {
				flusher.Flush()
			}
			if every > 0 {
				time.Sleep(every)
			}
		}
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":6,\"total_tokens\":11}}\n\n", model)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ---------------------------------------------------------------------------------------------
// Installation + supervisor (restarts the process when it exits, like launchd KeepAlive)

type install struct {
	t       *testing.T
	root    string
	binDir  string
	exe     string
	home    string
	cfgPath string
	port    int
	base    string
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func renderConfig(port int, upstream, authDir string) string {
	return fmt.Sprintf(`config-version: 8
server:
  host: 127.0.0.1
  port: %d
management:
  secret-key: %q
  disable-control-panel: true
access:
  api-keys: [%q]
oauth:
  auth-dir: %q
routing:
  retry:
    request-retry: 0
api-keys:
  openai-compatibility:
    - name: mock
      base-url: %s/v1
      request-retry: 0
      keys:
        - api-key: mock-key
      models:
        - name: fast
          alias: fast
        - name: slow
          alias: slow
proxx:
  update:
    check: false
`, port, mgmtKey, clientKey, authDir, upstream)
}

func newInstall(t *testing.T, root, v1Bin, upstream string) *install {
	t.Helper()
	in := &install{t: t, root: root, binDir: filepath.Join(root, "bin"), home: filepath.Join(root, "home")}
	for _, d := range []string{in.binDir, in.home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	in.exe = filepath.Join(in.binDir, "proxx")
	copyFile(t, v1Bin, in.exe)
	in.port = freePort(t)
	in.base = fmt.Sprintf("http://127.0.0.1:%d", in.port)
	in.cfgPath = filepath.Join(in.home, "config.yaml")
	if err := os.WriteFile(in.cfgPath, []byte(renderConfig(in.port, upstream, filepath.Join(in.home, "auths"))), 0o600); err != nil {
		t.Fatal(err)
	}
	return in
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

type supervisor struct {
	t        *testing.T
	in       *install
	logPath  string
	mu       sync.Mutex
	pid      int
	starts   int
	stopping bool
	done     chan struct{}
	proc     *os.Process
}

func startSupervisor(t *testing.T, in *install) *supervisor {
	s := &supervisor{t: t, in: in, logPath: filepath.Join(in.root, "proxx.log"), done: make(chan struct{})}
	logFile, err := os.Create(s.logPath)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(s.done)
		for {
			cmd := exec.Command(in.exe, "--config", in.cfgPath, "--local-model", "--no-browser")
			cmd.Dir = in.home
			cmd.Env = append(os.Environ(), "WRITABLE_PATH=")
			cmd.Stdout = logFile
			cmd.Stderr = logFile
			if errStart := cmd.Start(); errStart != nil {
				t.Errorf("supervisor: start: %v", errStart)
				return
			}
			s.mu.Lock()
			s.pid = cmd.Process.Pid
			s.proc = cmd.Process
			s.starts++
			s.mu.Unlock()
			_ = cmd.Wait()
			s.mu.Lock()
			stop := s.stopping
			s.mu.Unlock()
			if stop {
				return
			}
			time.Sleep(500 * time.Millisecond) // KeepAlive throttle
		}
	}()
	t.Cleanup(func() {
		s.stop()
		_ = logFile.Close()
		if t.Failed() {
			s.dumpLog()
		}
	})
	return s
}

func (s *supervisor) stop() {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return
	}
	s.stopping = true
	proc := s.proc
	s.mu.Unlock()
	if proc != nil {
		_ = proc.Signal(os.Interrupt)
	}
	select {
	case <-s.done:
	case <-time.After(60 * time.Second):
		if proc != nil {
			_ = proc.Kill()
		}
		<-s.done
	}
}

func (s *supervisor) state() (pid, starts int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pid, s.starts
}

func (s *supervisor) dumpLog() {
	data, _ := os.ReadFile(s.logPath)
	lines := strings.Split(string(data), "\n")
	if len(lines) > 150 {
		lines = lines[len(lines)-150:]
	}
	s.t.Logf("proxx log tail:\n%s", strings.Join(lines, "\n"))
}

// ---------------------------------------------------------------------------------------------
// HTTP helpers

var mgmtClient = &http.Client{Timeout: 30 * time.Second}

func mgmt(t *testing.T, base, method, path, body string) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, base+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+mgmtKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := mgmtClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

func status(t *testing.T, base string) string {
	t.Helper()
	code, body := mgmt(t, base, http.MethodGet, "/v8/management/proxx/update", "")
	if code != http.StatusOK {
		return ""
	}
	return body
}

func waitFor(t *testing.T, timeout time.Duration, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

func waitVersion(t *testing.T, base, version string, timeout time.Duration) string {
	t.Helper()
	var st string
	waitFor(t, timeout, "version "+version, func() bool {
		st = status(t, base)
		return ojson.Get(st, "current_version").String() == version
	})
	return st
}

func putConfig(t *testing.T, base, key, rawJSON string) {
	t.Helper()
	code, body := mgmt(t, base, http.MethodPut, "/v8/management/config/proxx/update/"+key, rawJSON)
	if code != http.StatusOK {
		t.Fatalf("PUT config proxx/update/%s = %d %s", key, code, body)
	}
}

// chatResult is one proxied request.
type chatResult struct {
	status int
	chunks int
	done   bool
	err    error
}

// chat sends one chat completion with a Go client (warm-up, long stream).
func chat(client *http.Client, base, model string, stream bool) chatResult {
	body := fmt.Sprintf(`{"model":%q,"stream":%v,"messages":[{"role":"user","content":"hi"}]}`, model, stream)
	req, err := http.NewRequest(http.MethodPost, base+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		return chatResult{err: err}
	}
	req.Header.Set("Authorization", "Bearer "+clientKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return chatResult{err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	res := chatResult{status: resp.StatusCode}
	if !stream {
		data, errRead := io.ReadAll(resp.Body)
		if errRead != nil {
			res.err = errRead
			return res
		}
		res.done = ojson.GetBytes(data, "choices.0.message.content").String() == "ok"
		return res
	}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			res.done = true
			continue
		}
		if strings.Contains(ojson.Get(data, "choices.0.delta.content").String(), "tok") {
			res.chunks++
		}
	}
	res.err = scanner.Err()
	return res
}

// loadGen is a raw HTTP/1.1 keep-alive client that never retries: workers write requests on their own
// net.Conn and parse responses with http.ReadResponse. Every error is a failure, including the case Go's
// http.Transport would silently retry (a server closing a keep-alive or freshly accepted connection
// without answering). A worker reconnects only after a response that says Connection: close (or after
// a failure).
type loadGen struct {
	sent, failed, reconnects atomic.Int64
	failures                 sync.Map
	stop                     chan struct{}
	wg                       sync.WaitGroup
}

const (
	loadWorkers  = 4
	loadInterval = 150 * time.Millisecond // ~26 requests/s in total
)

func startLoad(base string, _ int) *loadGen {
	lg := &loadGen{stop: make(chan struct{})}
	addr := strings.TrimPrefix(base, "http://")
	var seq atomic.Int64
	for w := 0; w < loadWorkers; w++ {
		lg.wg.Add(1)
		go func() {
			defer lg.wg.Done()
			var conn net.Conn
			var br *bufio.Reader
			defer func() {
				if conn != nil {
					_ = conn.Close()
				}
			}()
			for {
				select {
				case <-lg.stop:
					return
				default:
				}
				n := seq.Add(1)
				stream := n%3 == 0
				lg.sent.Add(1)
				if conn == nil {
					c, err := net.Dial("tcp", addr)
					if err != nil {
						lg.fail(n, "dial: "+err.Error())
						time.Sleep(loadInterval)
						continue
					}
					conn, br = c, bufio.NewReader(c)
				}
				closeAfter, err := rawChat(conn, br, addr, stream)
				if err != nil {
					lg.fail(n, fmt.Sprintf("stream=%v: %v", stream, err))
					_ = conn.Close()
					conn = nil
				} else if closeAfter {
					lg.reconnects.Add(1)
					_ = conn.Close()
					conn = nil
				}
				time.Sleep(loadInterval)
			}
		}()
	}
	return lg
}

func (lg *loadGen) fail(n int64, msg string) {
	lg.failed.Add(1)
	lg.failures.Store(n, msg)
}

// rawChat sends one request on conn and validates the whole response. closeAfter reports a
// Connection: close response.
func rawChat(conn net.Conn, br *bufio.Reader, host string, stream bool) (closeAfter bool, err error) {
	body := fmt.Sprintf(`{"model":"fast","stream":%v,"messages":[{"role":"user","content":"hi"}]}`, stream)
	req := fmt.Sprintf("POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
		host, clientKey, len(body), body)
	if _, err = io.WriteString(conn, req); err != nil {
		return false, fmt.Errorf("write: %w", err)
	}
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return false, fmt.Errorf("read response: %w", err)
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return resp.Close, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return resp.Close, fmt.Errorf("status %d: %.200s", resp.StatusCode, data)
	}
	if !stream {
		if ojson.GetBytes(data, "choices.0.message.content").String() != "ok" {
			return resp.Close, fmt.Errorf("bad body %.200s", data)
		}
		return resp.Close, nil
	}
	if c := strings.Count(string(data), "tok0"); c != 3 || !strings.Contains(string(data), "data: [DONE]") {
		return resp.Close, fmt.Errorf("incomplete stream (%d chunks): %.300s", c, data)
	}
	return resp.Close, nil
}

func (lg *loadGen) finish() {
	close(lg.stop)
	lg.wg.Wait()
}

func (lg *loadGen) failureSamples() []string {
	var out []string
	lg.failures.Range(func(_, v any) bool {
		out = append(out, v.(string))
		return len(out) < 10
	})
	return out
}
