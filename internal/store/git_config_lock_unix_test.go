//go:build aix || darwin || linux || solaris

package store

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"testing"
	"time"
)

func TestGitSynchronizationWaitsForCrossProcessConfigWriter(t *testing.T) {
	remote, a, b := gitWorkspacePair(t)
	writeSeedTestFile(t, a.ConfigPath(), "port: 8317\n")
	if errPersist := a.PersistConfig(context.Background()); errPersist != nil {
		t.Fatal(errPersist)
	}
	if errSync := b.EnsureRepository(); errSync != nil {
		t.Fatal(errSync)
	}
	// Advance only an auth path. A raw config scan could finish without
	// reaching a locked config checkout, even while a writer holds the inode.
	if _, errSave := b.Save(context.Background(), &cliproxyauth.Auth{ID: "peer.json", Metadata: map[string]any{"type": "codex"}}); errSave != nil {
		t.Fatal(errSave)
	}
	file, errOpen := os.OpenFile(a.ConfigPath(), os.O_RDWR, 0)
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			t.Error(errClose)
		}
	}()
	if errLock := setGitTestConfigLock(file, true); errLock != nil {
		t.Fatal(errLock)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGitConfigurationSyncChild$")
	cmd.Env = append(os.Environ(), "CPA_GIT_CONFIG_TEST_REMOTE="+remote, "CPA_GIT_CONFIG_TEST_AUTHS="+a.AuthDir())
	pipe, errPipe := cmd.StdoutPipe()
	if errPipe != nil {
		t.Fatal(errPipe)
	}
	cmd.Stderr = os.Stderr
	if errStart := cmd.Start(); errStart != nil {
		t.Fatal(errStart)
	}
	waited := false
	defer func() {
		if !waited {
			cancel()
			if errWait := cmd.Wait(); errWait != nil {
				t.Logf("wait for synchronization child: %v", errWait)
			}
		}
	}()
	scanner := bufio.NewScanner(pipe)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatalf("child did not reach synchronization: %q, %v", scanner.Text(), scanner.Err())
	}
	result := make(chan string, 1)
	go func() {
		scanner.Scan()
		result <- scanner.Text()
	}()
	select {
	case msg := <-result:
		t.Fatalf("child bypassed the cross-process config lock: %q", msg)
	case <-time.After(100 * time.Millisecond):
	}
	if errUnlock := setGitTestConfigLock(file, false); errUnlock != nil {
		t.Fatal(errUnlock)
	}
	select {
	case msg := <-result:
		if msg != "done" {
			t.Fatalf("child did not synchronize: %q", msg)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	errWait := cmd.Wait()
	waited = true
	if errWait != nil {
		t.Fatal(errWait)
	}
	assertLocalFileContents(t, a.ConfigPath(), string(readSeedTestFile(t, b.ConfigPath())))
	assertLocalJSONValue(t, filepath.Join(a.AuthDir(), "peer.json"), "type", "codex")
}

func TestGitConfigurationSyncChild(t *testing.T) {
	remote := os.Getenv("CPA_GIT_CONFIG_TEST_REMOTE")
	if remote == "" {
		return
	}
	s := NewGitTokenStore(remote, "", "", "master")
	s.SetBaseDir(os.Getenv("CPA_GIT_CONFIG_TEST_AUTHS"))
	fmt.Println("ready")
	if errSync := s.EnsureRepository(); errSync != nil {
		t.Fatal(errSync)
	}
	fmt.Println("done")
}
