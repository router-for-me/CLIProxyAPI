package clawmaster

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetenvFallsBackToTheNameBeforeTheRename(t *testing.T) {
	t.Setenv("CLAW_MASTER_SERVER", "")
	t.Setenv("CLAUDE_MASTER_SERVER", "10.1.2.3:8443")
	if got := Getenv("SERVER"); got != "10.1.2.3:8443" {
		t.Fatalf("Getenv(SERVER) = %q, want the old name's value", got)
	}
	t.Setenv("CLAW_MASTER_SERVER", "10.9.9.9:8443")
	if got := Getenv("SERVER"); got != "10.9.9.9:8443" {
		t.Fatalf("Getenv(SERVER) = %q, want the new name's value first", got)
	}
}

func TestDataDirKeepsTheDirectoryFromBeforeTheRename(t *testing.T) {
	share := t.TempDir()
	current := filepath.Join(share, "claw-master")
	legacy := filepath.Join(share, "claude-master")
	if got := dataDir(share); got != current {
		t.Fatalf("with neither directory, dataDir = %q, want %q", got, current)
	}
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := dataDir(share); got != legacy {
		t.Fatalf("with only the old directory, dataDir = %q, want %q", got, legacy)
	}
	if err := os.Mkdir(current, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := dataDir(share); got != current {
		t.Fatalf("with both, dataDir = %q, want the new %q", got, current)
	}
}

func TestConnectProjectReadsEitherHeader(t *testing.T) {
	h := http.Header{}
	h.Set(legacyProjectHeader, "old-name")
	if got := connectProject(h); got != "old-name" {
		t.Fatalf("connectProject = %q from the old header", got)
	}
	h.Set(projectHeader, "new-name")
	if got := connectProject(h); got != "new-name" {
		t.Fatalf("connectProject = %q, want the new header first", got)
	}
}

func TestProbeIsAnsweredUnderBothNames(t *testing.T) {
	for _, path := range []string{proxyProbePath, legacyProbePath} {
		rec := httptest.NewRecorder()
		(&Proxy{}).handleConnect(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNoContent || rec.Header().Get(proxyProbeHeader) != "1" || rec.Header().Get(legacyProbeHeader) != "1" {
			t.Fatalf("probe at %s: status %d, headers %v", path, rec.Code, rec.Header())
		}
	}
}

func TestProbeProxyTakesAServerFromBeforeTheRename(t *testing.T) {
	answer := func(header string) (string, error) {
		client, server := net.Pipe()
		asked := make(chan string, 1)
		go func() {
			defer func() { _ = server.Close() }()
			req, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				asked <- ""
				return
			}
			asked <- req.URL.Path
			reply := "HTTP/1.1 204 No Content\r\n"
			if header != "" {
				reply += header + ": 1\r\n"
			}
			_, _ = server.Write([]byte(reply + "Connection: close\r\n\r\n"))
		}()
		err := probeProxy(client)
		_ = client.Close()
		return <-asked, err
	}
	// An old server answers only the old path with the old header.
	path, err := answer(legacyProbeHeader)
	if err != nil {
		t.Fatalf("an old server's probe answer was refused: %v", err)
	}
	if path != legacyProbePath {
		t.Fatalf("probe asked %q: an old server answers only %q", path, legacyProbePath)
	}
	if _, err := answer(proxyProbeHeader); err != nil {
		t.Fatalf("a new server's probe answer was refused: %v", err)
	}
	if _, err := answer(""); err == nil || !strings.Contains(err.Error(), "not a claw-master proxy") {
		t.Fatalf("a service without either header was taken for the proxy: %v", err)
	}
}

func TestLaunchEnvironmentStripsTheBackupKeyUnderItsOldName(t *testing.T) {
	out, err := launchEnvironment([]string{
		"PATH=/bin",
		LegacyBackupAPIKeyEnvironment + "=old-name-canary",
		BackupAPIKeyEnvironment + "=new-name-canary",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(out, "\n"); strings.Contains(joined, "canary") || !strings.Contains(joined, "PATH=/bin") {
		t.Fatalf("the launched environment keeps a backup key or drops the rest: %q", out)
	}
}
