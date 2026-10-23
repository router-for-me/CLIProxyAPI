package clawmaster

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"fmt"
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

// A name with a random part (a temporary directory per run: OpenClaw's
// openclaw-isolated-completion-<uuid>, mktemp's tmp.XXXXXXXXXX, a timestamp) keeps only what comes before it,
// so all its runs are one project; nothing but random is "random".
func TestStableProjectDropsRandomParts(t *testing.T) {
	for raw, want := range map[string]string{
		"openclaw-isolated-completion-078ac676-970e-440e-b37e-b4407c50b51": "openclaw-isolated-completion",
		"openclaw-isolated-completion-10d22449-fdff-4718-b3bb-24bc75877a0": "openclaw-isolated-completion",
		"build-20261023153000":                 "build",
		"tmp.Ab3dE9fGh2":                       "tmp",
		"ci_run_8f3a2b1c":                      "ci_run",
		"job-k3j2h4g5f6d7-retry":               "job",
		"3f2a9c1b-4d5e-6f70-8192-a3b4c5d6e7f8": "random",
		"20261023":                             "random",
		// Real names stay as they are.
		"colton-games-snake":      "colton-games-snake",
		"dolphin-labs":            "dolphin-labs",
		"tsgo-pnp-rebuild":        "tsgo-pnp-rebuild",
		"t-claude":                "t-claude",
		"x86-dev":                 "x86-dev",
		"claude-master-dashboard": "claude-master-dashboard",
		"go1.26":                  "go1.26",
		"web3":                    "web3",
		"aws-2026":                "aws-2026",
		"deadbeef":                "deadbeef",
		"s3-uploader-v2":          "s3-uploader-v2",
		"CLIProxyAPI":             "CLIProxyAPI",
	} {
		if got := stableProject(raw); got != want {
			t.Errorf("stableProject(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Random names do not use up the cap: 150 runs in their own temporary directories take one slot, and
// real projects after them still get their names.
func TestRandomNamesDoNotTakeSlots(t *testing.T) {
	projectLabels.mu.Lock()
	saved := projectLabels.seen
	projectLabels.seen = nil
	projectLabels.mu.Unlock()
	t.Cleanup(func() {
		projectLabels.mu.Lock()
		projectLabels.seen = saved
		projectLabels.mu.Unlock()
	})
	for i := 0; i < 150; i++ {
		name := fmt.Sprintf("openclaw-isolated-completion-%08x-%04x-4%03x-b%03x-%012x", 0x1000000+i*7919, i, i, i, i*104729)
		if got := projectMetricLabel(name); got != "openclaw-isolated-completion" {
			t.Fatalf("run %d = %q, want openclaw-isolated-completion", i, got)
		}
	}
	for i := 0; i < maxProjectLabels-1; i++ {
		name := "project-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		if got := projectMetricLabel(name); got != name {
			t.Fatalf("real project %d after the random runs = %q, want its name", i, got)
		}
	}
	if got := projectMetricLabel("one-too-many"); got != "other" {
		t.Fatalf("past the cap = %q, want other", got)
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

// The forwarder names its project on the CONNECT it opens to the claw-master proxy, and only there.
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
		got <- req.Method + " " + req.RequestURI + " " + req.Header.Get(projectHeader) + " " + req.Header.Get(legacyProjectHeader)
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
	// Under both header names, so a server from before the rename still counts it.
	if line := <-got; line != "CONNECT "+masterAPIHost+":443 my-app my-app" {
		t.Fatalf("the proxy saw %q, want the CONNECT naming the sanitized project under both headers", line)
	}
}
