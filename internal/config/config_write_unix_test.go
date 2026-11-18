//go:build unix

package config

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A named pipe parks a read or a write inside the file I/O, so the test can check,
// without timing, that the other side cannot start until it finishes.
func TestConfigFileReadsAndWritesExcludeEachOther(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	read := make(chan error, 1)
	go func() {
		_, err := ReadConfigFile(path)
		read <- err
	}()
	// Opening the write end returns once the reader has opened the pipe, and the
	// reader then waits for data inside ReadConfigFile.
	pipe, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open pipe for writing: %v", err)
	}
	if configFileMu.TryLock() {
		configFileMu.Unlock()
		t.Error("a write could start while a read was in progress")
	}
	if _, err = pipe.Write([]byte("port: 8001\n")); err != nil {
		t.Fatalf("write pipe: %v", err)
	}
	if err = pipe.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	if err = <-read; err != nil {
		t.Fatalf("ReadConfigFile: %v", err)
	}

	written := make(chan error, 1)
	// More than a pipe buffer holds, so the writer stays inside WriteConfigFile
	// until the test drains the pipe.
	data := bytes.Repeat([]byte("# padding\n"), 1<<17)
	go func() {
		written <- WriteConfigFile(path, data, 0o600)
	}()
	pipe, err = os.Open(path)
	if err != nil {
		t.Fatalf("open pipe for reading: %v", err)
	}
	if configFileMu.TryRLock() {
		configFileMu.RUnlock()
		t.Error("a read could start while a write was in progress")
	}
	got, err := io.ReadAll(pipe)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if err = pipe.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("pipe carried %d bytes, want %d", len(got), len(data))
	}
	// A pipe cannot be synced, so the write reports an error after writing.
	<-written
}
