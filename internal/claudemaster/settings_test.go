package claudemaster

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeSettingsFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsPreflightPreservesOrdinarySettings(t *testing.T) {
	base := canonicalTestTempDir(t)
	home, root, managed := filepath.Join(base, "home"), filepath.Join(base, "repo"), filepath.Join(base, "managed")
	path := filepath.Join(home, ".claude", "settings.json")
	const original = `{"permissions":{"allow":["Bash(git status)"]},"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"echo synthetic"}]}]},"env":{"EDITOR":"vim","ANTHROPIC_MODEL":"claude-opus-4-8"}}`
	writeSettingsFixture(t, path, original)
	if err := validateSettingsAt(home, []string{root}, managed); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != original {
		t.Fatal("settings were modified")
	}
}

func TestSettingsRejectsRoutingAtEverySource(t *testing.T) {
	for _, source := range []string{"user", "cache", "project", "local", "repo-root", "main-checkout", "managed", "drop-in"} {
		t.Run(source, func(t *testing.T) {
			base := canonicalTestTempDir(t)
			home, cwd, repo, main, managed := filepath.Join(base, "home"), filepath.Join(base, "cwd"), filepath.Join(base, "repo"), filepath.Join(base, "main"), filepath.Join(base, "managed")
			paths := map[string]string{
				"user":          filepath.Join(home, ".claude", "settings.json"),
				"cache":         filepath.Join(home, ".claude", "remote-settings.json"),
				"project":       filepath.Join(cwd, ".claude", "settings.json"),
				"local":         filepath.Join(cwd, ".claude", "settings.local.json"),
				"repo-root":     filepath.Join(repo, ".claude", "settings.local.json"),
				"main-checkout": filepath.Join(main, ".claude", "settings.local.json"),
				"managed":       filepath.Join(managed, "managed-settings.json"),
				"drop-in":       filepath.Join(managed, "managed-settings.d", "50-routing.json"),
			}
			writeSettingsFixture(t, paths[source], `{"env":{"HTTPS_PROXY":"sensitive-routing-canary"}}`)
			err := validateSettingsAt(home, []string{cwd, repo, main}, managed)
			if err == nil {
				t.Fatal("routing override accepted")
			}
			if strings.Contains(err.Error(), "sensitive-routing-canary") || strings.Contains(err.Error(), base) {
				t.Fatal("settings contents/path leaked in error")
			}
		})
	}
}

func TestSettingsBlocksEmptyProxyAndCredentialHelpers(t *testing.T) {
	for _, raw := range []string{
		`{"env":{"NO_PROXY":""}}`, `{"env":{"https_proxy":""}}`,
		`{"env":{"DISABLE_AUTOUPDATER":"0"}}`, `{"env":{"DISABLE_AUTOUPDATER":""}}`,
		`{"env":{"NODE_EXTRA_CA_CERTS":"certificate"}}`, `{"env":{"ANTHROPIC_BASE_URL":"https://other.example"}}`,
		`{"env":{"ANTHROPIC_PROFILE":"other"}}`, `{"env":{"CLAUDE_CODE_USE_MANTLE":"0"}}`,
		`{"env":{"HOME":"elsewhere"}}`, `{"apiKeyHelper":"credential-canary"}`,
		`{"policyHelper":"policy-canary"}`, `{"awsCredentialExport":"credential-canary"}`,
		`{"forceLoginOrgUUID":"other"}`, `{"env":null}`, `{"env":{"EDITOR":3}}`,
		`null`, `[]`, `{bad json`,
	} {
		if err := validateSettingsJSON([]byte(raw)); err == nil {
			t.Errorf("accepted incompatible settings %s", raw)
		}
	}
}

func TestSettingsRejectsLinkedMalformedAndOversizedFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "directory", "oversized", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			base := canonicalTestTempDir(t)
			path := filepath.Join(base, "settings.json")
			switch kind {
			case "symlink":
				target := filepath.Join(base, "target.json")
				writeSettingsFixture(t, target, `{}`)
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				if err := os.WriteFile(path, bytes.Repeat([]byte(" "), settingsByteLimit+1), 0o600); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				writeSettingsFixture(t, path, `{"env":`)
			}
			if err := validateSettingsFile(path); err == nil {
				t.Fatal("unsafe settings accepted")
			}
		})
	}
}

func TestSettingsDropInsFollowNativeDocumentedNames(t *testing.T) {
	base := canonicalTestTempDir(t)
	managed := filepath.Join(base, "managed")
	for _, name := range []string{".hidden.json", "settings.txt"} {
		writeSettingsFixture(t, filepath.Join(managed, "managed-settings.d", name), `{"env":{"NO_PROXY":"*"}}`)
	}
	if err := validateSettingsAt(filepath.Join(base, "home"), []string{filepath.Join(base, "project")}, managed); err != nil {
		t.Fatal(err)
	}
}

func TestNativeProjectRootsIncludeMainCheckoutForWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required for synthetic repository discovery test")
	}
	base := canonicalTestTempDir(t)
	repo, work := filepath.Join(base, "main"), filepath.Join(base, "work")
	gitTest := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("synthetic git fixture: %v %s", err, output)
		}
	}
	gitTest("init", repo)
	gitTest("-C", repo, "-c", "user.email=synthetic@example.test", "-c", "user.name=Test", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-m", "synthetic")
	gitTest("-C", repo, "worktree", "add", "--detach", work)
	cwd := filepath.Join(work, "nested")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	roots, err := nativeProjectRoots(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, root := range roots {
		seen[root] = true
	}
	for _, want := range []string{cwd, work, repo} {
		if !seen[want] {
			t.Errorf("missing settings root %q", want)
		}
	}
}

func TestNativeProjectRootsOutsideGit(t *testing.T) {
	cwd := canonicalTestTempDir(t)
	roots, err := nativeProjectRoots(context.Background(), cwd)
	if err != nil || len(roots) != 1 || roots[0] != cwd {
		t.Fatalf("nonrepo roots: %v %v", roots, err)
	}
}

func TestNativeSettingsPreCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ValidateNativeSettings(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled settings validation did not stop: %v", err)
	}
	if _, err := nativeProjectRoots(ctx, canonicalTestTempDir(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled root discovery did not stop: %v", err)
	}
}

// The child acknowledges startup over a loopback test connection and then blocks on I/O.
// This proves cancellation after process start without sleeps or a live Git/provider operation.
func TestGitDiscoveryCancellationHelper(t *testing.T) {
	if os.Getenv("CLAUDE_MASTER_GIT_TEST_HELPER") != "1" {
		return
	}
	conn, err := net.Dial("tcp", os.Getenv("CLAUDE_MASTER_GIT_TEST_LISTENER"))
	if err != nil {
		os.Exit(2)
	}
	if _, err := conn.Write([]byte{1}); err != nil {
		os.Exit(2)
	}
	_, _ = conn.Read(make([]byte, 1))
	_ = conn.Close()
	os.Exit(0)
}

func TestNativeProjectRootsCancelsRunningGit(t *testing.T) {
	dir := canonicalTestTempDir(t)
	gitPath := filepath.Join(dir, "git")
	const helper = "#!/bin/sh\nexec \"$CLAUDE_MASTER_GIT_TEST_BINARY\" -test.run=^TestGitDiscoveryCancellationHelper$\n"
	if err := os.WriteFile(gitPath, []byte(helper), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_MASTER_GIT_TEST_BINARY", os.Args[0])
	t.Setenv("CLAUDE_MASTER_GIT_TEST_HELPER", "1")
	t.Setenv("CLAUDE_MASTER_GIT_TEST_LISTENER", listener.Addr().String())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := nativeProjectRoots(ctx, dir)
		result <- err
	}()
	ready := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			ready <- err
			return
		}
		defer func() { _ = conn.Close() }()
		_, err = io.ReadFull(conn, make([]byte, 1))
		ready <- err
		// Keep the child blocked until context cancellation closes its socket.
		_, _ = io.Copy(io.Discard, conn)
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case err := <-result:
		t.Fatalf("Git helper exited before the cancellation test: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Git helper did not acknowledge startup")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Git was treated as a successful/non-Git fallback: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled Git process did not stop")
	}
}

func TestNativeVersionPin(t *testing.T) {
	for _, supported := range []string{NativeClaudeVersion, NativeClaudeVersion + " (Claude Code)\n"} {
		if !supportedNativeVersion(supported) {
			t.Error("pinned version rejected")
		}
	}
	for _, unsupported := range []string{"", "2.1.268 (Claude Code)", "2.1.270 (Claude Code)", "2.1.269-canary (Claude Code)", "canary\n2.1.269 (Claude Code)"} {
		if supportedNativeVersion(unsupported) {
			t.Error("unsupported version accepted")
		}
	}
}
