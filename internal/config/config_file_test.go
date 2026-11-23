package config

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestConfigFileCASWithConcurrentWriters(t *testing.T) {
	path := writeUploadTestConfig(t, "port: 1\n")
	expected, err := ReadConfigFileSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	copy := expected.Bytes()
	copy[0] = '!'
	if expected.SourceHash() != ComputeSourceHash([]byte("port: 1\n")) {
		t.Fatal("the exposed bytes modified the CAS token")
	}
	var wg sync.WaitGroup
	const count = 12
	results := make(chan error, count)
	start := make(chan struct{})
	for i := range count {
		wg.Go(func() {
			<-start
			replacement := []byte(fmt.Sprintf("port: %d\n", 2+i))
			committed, errWrite := WriteConfigFileCAS(path, expected, replacement)
			if errWrite == nil && committed.SourceHash() != ComputeSourceHash(replacement) {
				errWrite = errors.New("CAS returned a stale hash")
			}
			results <- errWrite
		})
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for errWrite := range results {
		switch {
		case errWrite == nil:
			success++
		case errors.Is(errWrite, ErrConfigFileChanged):
		default:
			t.Fatal(errWrite)
		}
	}
	info, err := os.Stat(path)
	if err != nil || !os.SameFile(info, expected.info) || success != 1 {
		t.Fatalf("CAS committed %d writes or replaced the original file: %v", success, err)
	}
}

func TestConfigFileCreateAndRemoveRequireMatchingSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	const original = "port: 8317\n---\nkeep: yes\n"
	if errCreate := CreateConfigFile(path, []byte(original)); errCreate != nil {
		t.Fatal(errCreate)
	}
	snapshot, errSnapshot := ReadConfigFileSnapshot(path)
	if errSnapshot != nil || string(snapshot.Bytes()) != original {
		t.Fatalf("created config differs: %v", errSnapshot)
	}
	if errCreate := CreateConfigFile(path, []byte("port: 8888\n")); !errors.Is(errCreate, ErrConfigFileChanged) {
		t.Fatalf("CreateConfigFile overwriting existing file: %v", errCreate)
	}
	updated, errWrite := WriteConfigFileCAS(path, snapshot, []byte("port: 7777\n"))
	if errWrite != nil {
		t.Fatal(errWrite)
	}
	if errRemove := RemoveConfigFileCAS(path, snapshot); !errors.Is(errRemove, ErrConfigFileChanged) {
		t.Fatalf("RemoveConfigFileCAS with stale snapshot: %v", errRemove)
	}
	if errRemove := RemoveConfigFileCAS(path, updated); errRemove != nil {
		t.Fatal(errRemove)
	}
	if _, errRead := ReadConfigFile(path); !errors.Is(errRead, os.ErrNotExist) {
		t.Fatalf("file still exists after successful removal: %v", errRead)
	}
}

func TestConfigFileRollbackRestoresCompleteSnapshot(t *testing.T) {
	for _, data := range []string{"x", "larger than the initial contents"} {
		t.Run(data, func(t *testing.T) {
			const original = "port: 1\n---\ntrailing\n"
			path := writeUploadTestConfig(t, original)
			err := withConfigFile(path, configWrite, func(f *lockedConfigFile) error {
				return replaceConfigContents(f.file, f.snapshot.data, []byte(data), func() error {
					return ErrConfigFileChanged
				})
			})
			if !errors.Is(err, ErrConfigFileChanged) || readUploadTestConfig(t, path) != original {
				t.Fatalf("rollback did not restore exact bytes: %v", err)
			}
		})
	}
}

func TestConfigFileOSLocks(t *testing.T) {
	for _, tc := range []struct {
		name      string
		exclusive bool
		writer    bool
		blocked   bool
	}{
		{"shared permits reader", false, false, false},
		{"exclusive blocks reader", true, false, true},
		{"shared blocks writer", false, true, true},
		{"exclusive blocks writer", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeUploadTestConfig(t, "port: 1\n")
			file, err := os.OpenFile(path, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if errClose := file.Close(); errClose != nil {
					t.Error(errClose)
				}
			}()
			if err = lockConfigFile(file, tc.exclusive); err != nil {
				t.Fatal(err)
			}
			locked := true
			release := func() {
				if locked {
					locked = false
					if errUnlock := unlockConfigFile(file); errUnlock != nil {
						t.Error(errUnlock)
					}
				}
			}
			defer release()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConfigFileLockChild$")
			cmd.Env = append(os.Environ(), "CPA_CONFIG_LOCK_TEST_PATH="+path)
			if tc.writer {
				cmd.Env = append(cmd.Env, "CPA_CONFIG_LOCK_TEST_WRITE=1")
			}
			pipe, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			var waited bool
			defer func() {
				if !waited {
					cancel()
					if errWait := cmd.Wait(); errWait != nil {
						t.Logf("wait for lock child: %v", errWait)
					}
				}
			}()
			scanner := bufio.NewScanner(pipe)
			if !scanner.Scan() || scanner.Text() != "ready" {
				t.Fatalf("child did not reach lock: %q, %v", scanner.Text(), scanner.Err())
			}
			result := make(chan string, 1)
			go func() {
				scanner.Scan()
				result <- scanner.Text()
			}()
			if tc.blocked {
				select {
				case message := <-result:
					t.Fatalf("child completed while conflicting lock held: %q", message)
				case <-time.After(100 * time.Millisecond):
				}
				release()
			}
			select {
			case message := <-result:
				if message != "done" {
					t.Fatalf("child did not complete successfully: %q", message)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			err = cmd.Wait()
			waited = true
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConfigFileLockChild(t *testing.T) {
	path := os.Getenv("CPA_CONFIG_LOCK_TEST_PATH")
	if path == "" {
		return
	}
	fmt.Println("ready")
	mode := configRead
	if os.Getenv("CPA_CONFIG_LOCK_TEST_WRITE") != "" {
		mode = configWrite
	}
	if err := withConfigFile(path, mode, func(*lockedConfigFile) error { return nil }); err != nil {
		t.Fatal(err)
	}
	fmt.Println("done")
}

func TestConfigFilesystemAliasesShareSnapshotCAS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks can require special privileges on Windows")
	}
	path := writeUploadTestConfig(t, "port: 8317\n")
	alias := filepath.Join(t.TempDir(), "alias.yaml")
	if errLink := os.Symlink(path, alias); errLink != nil {
		t.Fatal(errLink)
	}
	expected, err := ReadConfigFileSnapshot(alias)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil || cfg.ServerID == "" {
		t.Fatalf("startup: %v", err)
	}
	if _, err = WriteConfigFileCAS(alias, expected, []byte("port: 2\n")); !errors.Is(err, ErrConfigFileChanged) {
		t.Fatalf("alias CAS accepted bytes predating startup: %v", err)
	}
	aliasInfo, err := os.Lstat(alias)
	if err != nil || aliasInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("startup replaced config symlink: %v", err)
	}
}
