package claudemaster

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSanitizeProjectKeepsAHeaderSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"my-app":                 "my-app",
		"  Dash_board.v2  ":      "Dash_board.v2",
		"a b/c":                  "a-b-c",
		"evil\r\nX-Injected: 1":  "evil--X-Injected--1",
		"--.hidden.--":           "hidden",
		"":                       "",
		strings.Repeat("x", 100): strings.Repeat("x", projectMaxLen),
		"émoji🙂":                 "moji",
	} {
		if got := sanitizeProject(in); got != want {
			t.Errorf("sanitizeProject(%q) = %q, want %q", in, got, want)
		}
		if strings.ContainsAny(sanitizeProject(in), "\r\n:") {
			t.Errorf("sanitizeProject(%q) can break a header line", in)
		}
	}
}

func TestProjectLabelIsTheRepositoryName(t *testing.T) {
	root := canonicalTestTempDir(t)
	repo := filepath.Join(root, "my-app")
	deep := filepath.Join(repo, "src", "pkg")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "feature"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	// A linked worktree elsewhere: its .git file points into the main repository.
	worktree := filepath.Join(root, "my-app-feature")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	gitdir := "gitdir: " + filepath.Join(repo, ".git", "worktrees", "feature") + "\n"
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte(gitdir), 0o644); err != nil {
		t.Fatal(err)
	}
	// A submodule-style .git file that is not a worktree names its own directory.
	sub := filepath.Join(root, "vendored")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: ../my-app/.git/modules/vendored\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(root, "notes here")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	for cwd, want := range map[string]string{
		repo:     "my-app",
		deep:     "my-app",
		worktree: "my-app",
		sub:      "vendored",
		plain:    "notes-here",
		"":       "",
	} {
		if got := projectLabel(cwd); got != want {
			t.Errorf("projectLabel(%q) = %q, want %q", cwd, got, want)
		}
	}
}

func TestProjectMetricLabelIsCapped(t *testing.T) {
	projectLabels.mu.Lock()
	saved := projectLabels.seen
	projectLabels.seen = nil
	projectLabels.mu.Unlock()
	t.Cleanup(func() {
		projectLabels.mu.Lock()
		projectLabels.seen = saved
		projectLabels.mu.Unlock()
	})
	if got := projectMetricLabel(""); got != "none" {
		t.Fatalf("no project = %q, want none", got)
	}
	for i := 0; i < maxProjectLabels; i++ {
		name := "p" + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + string(rune('a'+i/26))
		if got := projectMetricLabel(name); got != name {
			t.Fatalf("project %d = %q, want its name", i, got)
		}
	}
	if got := projectMetricLabel("one-too-many"); got != "other" {
		t.Fatalf("past the cap = %q, want other", got)
	}
	// One already seen keeps its name.
	if got := projectMetricLabel("pa" + "a"); got != "paa" {
		t.Fatalf("a seen project = %q, want paa", got)
	}
}

// A tunnel's CONNECT names its project; every request on the tunnel carries it.
func TestEachRequestKnowsTheProjectItsTunnelNamed(t *testing.T) {
	stateDir := filepath.Join(canonicalTestTempDir(t), "state")
	certs, err := loadOrCreatePersistentCertificate(stateDir, serverNames([]byte{127, 0, 0, 1}))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	projects := []string{}
	proxy, err := startServerProxy(certs, "127.0.0.1:0", "127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		projects = append(projects, projectFromContext(r.Context()))
		mu.Unlock()
		_, _ = io.WriteString(w, "served")
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	roots := x509.NewCertPool()
	roots.AddCert(certs.ca)
	transport := func(header http.Header) *http.Transport {
		tr := &http.Transport{
			Proxy:              http.ProxyURL(&url.URL{Scheme: "http", Host: proxy.OpenAddr()}),
			TLSClientConfig:    &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
			ProxyConnectHeader: header,
		}
		t.Cleanup(tr.CloseIdleConnections)
		return tr
	}
	named := transport(http.Header{projectHeader: {"my app\r\n"}})
	for i := 0; i < 2; i++ {
		if body, err := post(t, named); err != nil || body != "served" {
			t.Fatalf("post: %q %v", body, err)
		}
	}
	if _, err := post(t, transport(nil)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(projects, ",") != "my-app,my-app," {
		t.Fatalf("requests carried the projects %q, want the tunnel's (sanitized) twice, then none", projects)
	}
}

// The forwarder names its project on the CONNECT it opens to the claude-master proxy, and only there.
func TestTheForwarderNamesItsProjectOnTheTunnelToTheProxy(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	got := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		req, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			got <- "unreadable"
			return
		}
		got <- req.Method + " " + req.RequestURI + " " + req.Header.Get(projectHeader)
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
	}()
	f, err := startLocalForwarder(plainUpstream(listener.Addr().String()), "my app")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	resp, _, _ := forwarderTestConnect(t, f, masterAPIHost+":443", f.token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT = %d", resp.StatusCode)
	}
	if line := <-got; line != "CONNECT "+masterAPIHost+":443 my-app" {
		t.Fatalf("the proxy saw %q, want the CONNECT naming the sanitized project", line)
	}
}
