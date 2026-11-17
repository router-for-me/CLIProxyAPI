package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/base64"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/buildinfo"
)

func sign(t *testing.T, priv ed25519.PrivateKey, data []byte) []byte {
	t.Helper()
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data)) + "\n")
}

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestVerifySignature(t *testing.T) {
	pub, priv := newKey(t)
	otherPub, _ := newKey(t)
	sums := []byte("abc  proxx_1.0.0_linux_amd64.tar.gz\n")
	sig := sign(t, priv, sums)

	if err := verifySignature(pub, sums, sig); err != nil {
		t.Fatalf("good signature rejected: %v", err)
	}
	if err := verifySignature(pub, append([]byte("x"), sums...), sig); err == nil {
		t.Fatal("signature over modified checksums accepted")
	}
	if err := verifySignature(otherPub, sums, sig); err == nil {
		t.Fatal("signature accepted with the wrong key")
	}
	if err := verifySignature(pub, sums, []byte("not base64!!")); err == nil {
		t.Fatal("garbage signature accepted")
	}
	short := []byte(base64.StdEncoding.EncodeToString([]byte("short")))
	if err := verifySignature(pub, sums, short); err == nil {
		t.Fatal("short signature accepted")
	}
}

func TestEmbeddedPublicKeyDecodes(t *testing.T) {
	if _, err := decodePublicKey(publicKeyB64); err != nil {
		t.Fatalf("embedded public key: %v", err)
	}
	if _, err := decodePublicKey("AAAA"); err == nil {
		t.Fatal("short key accepted")
	}
}

func makeArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestChecksumAndExtract(t *testing.T) {
	archive := makeArchive(t, map[string][]byte{"proxx": []byte("BINARY"), "LICENSE": []byte("l")})
	sum := sha256.Sum256(archive)
	name := "proxx_1.2.3_linux_amd64.tar.gz"
	sums := []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n" + strings.Repeat("0", 64) + "  other.tar.gz\n")

	if err := verifyArchive(sums, name, archive); err != nil {
		t.Fatalf("valid archive rejected: %v", err)
	}
	tampered := append([]byte{}, archive...)
	tampered[len(tampered)-1] ^= 0xff
	if err := verifyArchive(sums, name, tampered); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("tampered archive: err = %v, want checksum mismatch", err)
	}
	if err := verifyArchive(sums, "missing.tar.gz", archive); err == nil {
		t.Fatal("archive without a checksum entry accepted")
	}
	bin, err := extractBinary(archive)
	if err != nil || string(bin) != "BINARY" {
		t.Fatalf("extract = %q, %v", bin, err)
	}
	if _, err = extractBinary(makeArchive(t, map[string][]byte{"other": []byte("x")})); err == nil {
		t.Fatal("archive without proxx accepted")
	}
}

func TestAssetName(t *testing.T) {
	cases := []struct {
		ver, goos, goarch string
		noPlugin          bool
		want              string
	}{
		{"8.0.73-proxx.1", "linux", "amd64", false, "proxx_8.0.73-proxx.1_linux_amd64.tar.gz"},
		{"v8.0.73-proxx.1", "linux", "arm64", false, "proxx_8.0.73-proxx.1_linux_aarch64.tar.gz"},
		{"8.0.73-proxx.1", "linux", "amd64", true, "proxx_8.0.73-proxx.1_linux_amd64_no-plugin.tar.gz"},
		{"8.0.73-proxx.1", "linux", "arm64", true, "proxx_8.0.73-proxx.1_linux_aarch64_no-plugin.tar.gz"},
		{"8.0.73-proxx.1", "darwin", "arm64", false, "proxx_8.0.73-proxx.1_darwin_aarch64.tar.gz"},
		{"8.0.73-proxx.1", "darwin", "amd64", true, "proxx_8.0.73-proxx.1_darwin_amd64.tar.gz"},
	}
	for _, c := range cases {
		got, err := assetName(c.ver, c.goos, c.goarch, c.noPlugin)
		if err != nil || got != c.want {
			t.Errorf("assetName(%q,%s,%s,%v) = %q, %v; want %q", c.ver, c.goos, c.goarch, c.noPlugin, got, err, c.want)
		}
	}
	if _, err := assetName("1.0.0", "linux", "386", false); err == nil {
		t.Error("386 should have no asset")
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"8.0.73-proxx.1", "8.0.73-proxx.1", 0},
		{"v8.0.73-proxx.2", "8.0.73-proxx.1", 1},
		{"8.0.73-proxx.10", "8.0.73-proxx.9", 1},
		{"8.0.74-proxx.1", "8.0.73-proxx.9", 1},
		{"8.1.0", "8.0.99-proxx.5", 1},
		{"8.0.73-proxx.2-rc1", "8.0.73-proxx.2", -1},
		{"8.0.73-proxx.2-rc2", "8.0.73-proxx.2-rc1", 1},
		{"8.0.73-proxx.2-rc1", "8.0.73-proxx.1", 1},
		{"9.0.0", "10.0.0", -1},
	}
	for _, c := range cases {
		got, ok := compareVersions(c.a, c.b)
		if !ok || got != c.want {
			t.Errorf("compare(%s, %s) = %d, %v; want %d", c.a, c.b, got, ok, c.want)
		}
	}
	for _, bad := range []string{"dev", "", "1.2", "1.2.x", "1.2.3-beta"} {
		if _, ok := parseVersion(bad); ok {
			t.Errorf("parseVersion(%q) accepted", bad)
		}
	}
}

func TestNoDowngrade(t *testing.T) {
	if isNewer("8.0.72-proxx.9", "8.0.73-proxx.1") {
		t.Fatal("lower version treated as an update")
	}
	if isNewer("8.0.73-proxx.1", "8.0.73-proxx.1") {
		t.Fatal("same version treated as an update")
	}
	if isNewer("9.0.0", "dev") || isNewer("dev", "1.0.0") {
		t.Fatal("unparseable version treated as an update")
	}
	if !isNewer("8.0.73-proxx.2", "8.0.73-proxx.1") {
		t.Fatal("newer version not detected")
	}
}

func withVersion(t *testing.T, v string) {
	t.Helper()
	old := buildinfo.Version
	buildinfo.Version = v
	t.Cleanup(func() { buildinfo.Version = old })
}

func TestCandidateIgnoresPrereleaseDraftAndDowngrade(t *testing.T) {
	withVersion(t, "1.0.0-proxx.1")
	m := newManager()
	m.latest = &release{Version: "1.0.0-proxx.2", Prerelease: true}
	if m.candidateLocked() != nil {
		t.Fatal("prerelease offered as an update")
	}
	m.latest = &release{Version: "1.0.0-proxx.2", Draft: true}
	if m.candidateLocked() != nil {
		t.Fatal("draft offered as an update")
	}
	m.latest = &release{Version: "0.9.0-proxx.9"}
	if m.candidateLocked() != nil {
		t.Fatal("downgrade offered as an update")
	}
	m.latest = &release{Version: "1.0.0-proxx.2"}
	if m.candidateLocked() == nil {
		t.Fatal("newer release not offered")
	}
}

func TestParseRelease(t *testing.T) {
	rel, err := parseRelease([]byte(`{"tag_name":"v1.2.3-proxx.4","html_url":"https://x/r","body":"notes","prerelease":false,"draft":false,
		"assets":[{"name":"checksums.txt","browser_download_url":"https://x/c"},{"name":"a.tar.gz","browser_download_url":"https://x/a"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "1.2.3-proxx.4" || rel.Assets["checksums.txt"] != "https://x/c" || rel.Assets["a.tar.gz"] != "https://x/a" || rel.Body != "notes" {
		t.Fatalf("parsed %+v", rel)
	}
	if _, err = parseRelease([]byte(`{"name":"x"}`)); err == nil {
		t.Fatal("release without a tag accepted")
	}
}

func TestFetchReleaseETag(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`{"tag_name":"v2.0.0","assets":[]}`))
	}))
	defer srv.Close()
	oldBase := apiBaseURL
	apiBaseURL = srv.URL
	defer func() { apiBaseURL = oldBase }()

	rel, etag, nm, err := fetchRelease(context.Background(), srv.Client(), "")
	if err != nil || nm || rel.Version != "2.0.0" || etag != `"v1"` {
		t.Fatalf("first fetch: %+v %q %v %v", rel, etag, nm, err)
	}
	_, _, nm, err = fetchRelease(context.Background(), srv.Client(), etag)
	if err != nil || !nm {
		t.Fatalf("second fetch: notModified=%v err=%v", nm, err)
	}
}

func TestParseWindow(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.Local) }
	w, err := parseWindow("02:00-05:30")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		h, m int
		in   bool
	}{{1, 59, false}, {2, 0, true}, {5, 29, true}, {5, 30, false}, {14, 0, false}} {
		if got := w.contains(at(c.h, c.m)); got != c.in {
			t.Errorf("02:00-05:30 contains %02d:%02d = %v, want %v", c.h, c.m, got, c.in)
		}
	}
	wrap, err := parseWindow("23:00-02:00")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		h, m int
		in   bool
	}{{22, 59, false}, {23, 0, true}, {23, 59, true}, {0, 0, true}, {1, 59, true}, {2, 0, false}, {12, 0, false}} {
		if got := wrap.contains(at(c.h, c.m)); got != c.in {
			t.Errorf("23:00-02:00 contains %02d:%02d = %v, want %v", c.h, c.m, got, c.in)
		}
	}
	anyW, err := parseWindow("")
	if err != nil || !anyW.contains(at(13, 37)) {
		t.Fatal("empty window should allow any time")
	}
	for _, bad := range []string{"2:00", "25:00-03:00", "02:00-03:60", "ab-cd", "02:00-3:0"} {
		if _, err = parseWindow(bad); err == nil {
			t.Errorf("parseWindow(%q) accepted", bad)
		}
	}
}

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), stateFileName)
	in := &updateState{PendingVersion: "2.0.0", FromVersion: "1.0.0", ExePath: "/x/proxx", PrevPath: "/x/proxx.prev", Attempts: 2,
		Last: &lastResult{From: "0.9.0", To: "1.0.0", At: "t", OK: true}}
	if err := saveState(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.PendingVersion != "2.0.0" || out.Attempts != 2 || out.Last == nil || !out.Last.OK || out.PrevPath != "/x/proxx.prev" {
		t.Fatalf("round trip: %+v %+v", out, out.Last)
	}
}

type exitPanic int

func TestRollbackAttemptCounter(t *testing.T) {
	withVersion(t, "2.0.0")
	dir := t.TempDir()
	exe := filepath.Join(dir, "proxx")
	prev := exe + ".prev"
	if err := os.WriteFile(exe, []byte("v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prev, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, stateFileName)
	if err := saveState(path, &updateState{PendingVersion: "2.0.0", FromVersion: "1.0.0", ExePath: exe, PrevPath: prev}); err != nil {
		t.Fatal(err)
	}
	var execed []string
	oldExec, oldExit, oldDeadline := execFn, exitFn, healthDeadline
	execFn = func(p string, _ []string, _ []string) error {
		execed = append(execed, p)
		return errors.New("test: no exec")
	}
	exitFn = func(code int) { panic(exitPanic(code)) }
	healthDeadline = time.Hour
	defer func() { execFn, exitFn, healthDeadline = oldExec, oldExit, oldDeadline }()
	boot.Lock()
	boot.healthy = make(chan struct{})
	boot.Unlock()

	start := func() (exited bool) {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(exitPanic); !ok {
					panic(r)
				}
				exited = true
			}
		}()
		st, err := loadState(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.PendingVersion != "" {
			handlePendingStart(path, st)
		}
		return false
	}
	for attempt := 1; attempt < maxStartAttempts; attempt++ {
		if start() {
			t.Fatalf("attempt %d rolled back too early", attempt)
		}
		st, _ := loadState(path)
		if st.Attempts != attempt || st.PendingVersion != "2.0.0" {
			t.Fatalf("after attempt %d: %+v", attempt, st)
		}
	}
	if !start() {
		t.Fatal("attempt 3 did not roll back")
	}
	if len(execed) != 1 || execed[0] != exe {
		t.Fatalf("exec calls = %v, want [%s]", execed, exe)
	}
	data, _ := os.ReadFile(exe)
	if string(data) != "v1" {
		t.Fatalf("exe content after rollback = %q, want v1", data)
	}
	st, _ := loadState(path)
	if st.PendingVersion != "" || st.Last == nil || !st.Last.RolledBack || st.Last.OK || st.Last.From != "1.0.0" || st.Last.To != "2.0.0" {
		t.Fatalf("state after rollback: %+v last=%+v", st, st.Last)
	}
}

func TestHealthyClearsPending(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, stateFileName)
	if err := saveState(path, &updateState{PendingVersion: "2.0.0", FromVersion: "1.0.0", Attempts: 1}); err != nil {
		t.Fatal(err)
	}
	boot.Lock()
	oldPath := boot.statePath
	boot.statePath = path
	boot.healthy = make(chan struct{})
	boot.Unlock()
	defer func() { boot.Lock(); boot.statePath = oldPath; boot.Unlock() }()
	markHealthy()
	st, _ := loadState(path)
	if st.PendingVersion != "" || st.Last == nil || !st.Last.OK || st.Last.To != "2.0.0" {
		t.Fatalf("state after healthy: %+v %+v", st, st.Last)
	}
}

func TestConfigPathFromArgs(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--config", "/a/c.yaml"}, "/a/c.yaml"},
		{[]string{"-config=/b/c.yaml", "--tui"}, "/b/c.yaml"},
		{[]string{"--local-model"}, "/wd/config.yaml"},
		{[]string{"---config", "/x"}, "/wd/config.yaml"},
	}
	for _, c := range cases {
		if got := configPathFromArgs(c.args, "/wd"); got != c.want {
			t.Errorf("configPathFromArgs(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func fakeEnv() environment {
	return environment{
		goos:         "linux",
		exePath:      "/home/u/.local/bin/proxx",
		hasTrigger:   true,
		writable:     func(string) error { return nil },
		fileExists:   func(string) bool { return false },
		readFile:     func(string) ([]byte, error) { return nil, os.ErrNotExist },
		platformImpl: true,
	}
}

func TestSupportedDetection(t *testing.T) {
	if reason, ok := checkSupported(fakeEnv()); !ok {
		t.Fatalf("plain linux install unsupported: %s", reason)
	}
	cases := map[string]func(e *environment){
		"windows":   func(e *environment) { e.goos = "windows" },
		"freebsd":   func(e *environment) { e.goos = "freebsd" },
		"dockerenv": func(e *environment) { e.fileExists = func(p string) bool { return p == "/.dockerenv" } },
		"cgroup": func(e *environment) {
			e.readFile = func(string) ([]byte, error) { return []byte("0::/docker/abc"), nil }
		},
		"homebrew":   func(e *environment) { e.goos = "darwin"; e.exePath = "/opt/homebrew/Cellar/proxx/1.0/bin/proxx" },
		"unwritable": func(e *environment) { e.writable = func(string) error { return os.ErrPermission } },
		"no trigger": func(e *environment) { e.hasTrigger = false },
		"no exe":     func(e *environment) { e.exePath = "" },
	}
	for name, mutate := range cases {
		env := fakeEnv()
		mutate(&env)
		reason, ok := checkSupported(env)
		if ok || reason == "" {
			t.Errorf("%s: supported=%v reason=%q, want unsupported with a reason", name, ok, reason)
		}
	}
	darwin := fakeEnv()
	darwin.goos = "darwin"
	darwin.fileExists = func(p string) bool { return p == "/.dockerenv" } // container checks are linux-only
	if _, ok := checkSupported(darwin); !ok {
		t.Error("darwin install unsupported")
	}
}

func TestSupportedRealUnwritableDir(t *testing.T) {
	if !platformSupported || os.Geteuid() == 0 {
		t.Skip("needs linux/darwin as non-root")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o755) }()
	env := fakeEnv()
	env.goos = "darwin"
	env.exePath = filepath.Join(dir, "proxx")
	env.writable = isWritableDir
	if reason, ok := checkSupported(env); ok || !strings.Contains(reason, "not writable") {
		t.Fatalf("unwritable dir: ok=%v reason=%q", ok, reason)
	}
}

func TestInFlightCounterIncludesHijackedWebSocket(t *testing.T) {
	tr := newTracker()
	release := make(chan struct{})
	var upgrader websocket.Upgrader
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		<-release
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Like wsrelay: the handler returns while the session lives on in a goroutine.
		go func() {
			defer func() { _ = conn.Close() }()
			for {
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					return
				}
			}
		}()
	})
	mux.HandleFunc("/v8/management/proxx/update", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // a long-lived management stream
	})
	srv := httptest.NewServer(tr.wrap(mux))
	defer srv.Close()

	waitCount := func(want int64) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for tr.active.Load() != want {
			if time.Now().After(deadline) {
				t.Fatalf("in-flight = %d, want %d", tr.active.Load(), want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	slowDone := make(chan struct{})
	go func() {
		resp, err := http.Get(srv.URL + "/slow")
		if err == nil {
			_ = resp.Body.Close()
		}
		close(slowDone)
	}()
	waitCount(1)

	mgmtDone := make(chan struct{})
	go func() {
		resp, err := http.Get(srv.URL + "/v8/management/proxx/update")
		if err == nil {
			_ = resp.Body.Close()
		}
		close(mgmtDone)
	}()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	waitCount(2) // slow request + hijacked websocket; management requests are not counted

	close(release)
	<-slowDone
	waitCount(1) // the websocket handler returned long ago, but the connection is still open

	sig := tr.idleSignal()
	_ = conn.Close()
	waitCount(0)
	select {
	case <-sig:
	case <-time.After(5 * time.Second):
		t.Fatal("idle signal not delivered")
	}

	tr.cancelManagement()
	select {
	case <-mgmtDone:
	case <-time.After(5 * time.Second):
		t.Fatal("management stream not cancelled")
	}
}

func TestDrainingSetsConnectionClose(t *testing.T) {
	tr := newTracker()
	srv := httptest.NewServer(tr.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("x")) })))
	defer srv.Close()
	get := func() *http.Response {
		resp, err := http.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}
	if resp := get(); resp.Close {
		t.Fatal("Connection: close before draining")
	}
	tr.draining.Store(true)
	if resp := get(); !resp.Close {
		t.Fatal("no Connection: close while draining")
	}
}

func TestGateListenerPause(t *testing.T) {
	inner, err := netListen()
	if err != nil {
		t.Fatal(err)
	}
	g := newGateListener(inner)
	defer func() { _ = g.Close() }()
	g.setPaused(true)
	accepted := make(chan error, 1)
	go func() {
		c, errAccept := g.Accept()
		if c != nil {
			_ = c.Close()
		}
		accepted <- errAccept
	}()
	client, err := dial(inner.Addr().String())
	if err != nil {
		t.Fatalf("dial while paused (connection must queue, not be refused): %v", err)
	}
	defer func() { _ = client.Close() }()
	select {
	case <-accepted:
		t.Fatal("accepted while paused")
	case <-time.After(200 * time.Millisecond):
	}
	g.setPaused(false)
	select {
	case errAccept := <-accepted:
		if errAccept != nil {
			t.Fatalf("accept after resume: %v", errAccept)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not accepted after resume")
	}
}

func TestWaitForApplyMomentWindow(t *testing.T) {
	m := newManager()
	now := time.Now()
	outside := fmt.Sprintf("%02d:%02d-%02d:%02d", (now.Hour()+2)%24, 0, (now.Hour()+3)%24, 0)
	m.cfg.Window = outside
	oldPoll := windowPoll
	windowPoll = 20 * time.Millisecond
	defer func() { windowPoll = oldPoll }()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := m.waitForApplyMoment(ctx, true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("outside the window: err = %v, want deadline (no apply)", err)
	}
	if m.state != StateStaged {
		t.Fatalf("state outside window = %s, want staged", m.state)
	}
}

type fakeConn struct {
	net.Conn
	closed bool
}

func (f *fakeConn) Close() error { f.closed = true; return nil }

type wrapConn struct {
	net.Conn
	inner net.Conn
}

func (w *wrapConn) NetConn() net.Conn { return w.inner }

func (w *wrapConn) Close() error { return w.inner.Close() }

func TestTrackedConnRawCloseDecrements(t *testing.T) {
	tr := newTracker()
	raw := &fakeConn{}
	tr.inc()
	tc := &trackedConn{Conn: &wrapConn{inner: raw}, t: tr}
	r := tc.NetConn()
	if _, ok := r.(interface{ NetConn() net.Conn }); ok {
		t.Fatal("raw view must not unwrap further")
	}
	_ = r.Close()
	if !raw.closed || tr.active.Load() != 0 {
		t.Fatalf("raw closed=%v active=%d", raw.closed, tr.active.Load())
	}
	_ = tc.Close() // a later outer Close must not decrement twice
	if tr.active.Load() != 0 {
		t.Fatalf("double decrement: %d", tr.active.Load())
	}
}
