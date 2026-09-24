package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCreateUniqueLogFile_ConcurrentCollisions(t *testing.T) {
	dir := t.TempDir()
	const workers = 20
	const filename = "v1_chat_completions-2026-09-23T120000-00000000.log"

	var wg sync.WaitGroup
	paths := make(chan string, workers)
	errs := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		workerID := i
		go func() {
			defer wg.Done()
			f, path, err := createUniqueLogFile(dir, filename)
			if err != nil {
				errs <- err
				return
			}
			content := fmt.Sprintf("worker-%d-content\n", workerID)
			if _, errWrite := f.WriteString(content); errWrite != nil {
				_ = f.Close()
				errs <- errWrite
				return
			}
			if errClose := f.Close(); errClose != nil {
				errs <- errClose
				return
			}
			paths <- path
		}()
	}

	wg.Wait()
	close(paths)
	close(errs)

	for err := range errs {
		t.Fatalf("createUniqueLogFile error: %v", err)
	}

	seenPaths := make(map[string]bool, workers)
	seenContents := make(map[string]bool, workers)

	for path := range paths {
		if seenPaths[path] {
			t.Fatalf("duplicate path returned by createUniqueLogFile: %q", path)
		}
		seenPaths[path] = true

		if !strings.HasSuffix(path, "-00000000.log") {
			t.Fatalf("path %q does not have expected suffix -00000000.log", path)
		}

		data, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatalf("ReadFile(%q): %v", path, errRead)
		}
		content := string(data)
		if seenContents[content] {
			t.Fatalf("duplicate or overwritten file content: %q in %s", content, path)
		}
		seenContents[content] = true
	}

	if len(seenPaths) != workers {
		t.Fatalf("expected %d distinct files, got %d", workers, len(seenPaths))
	}
}

func TestFileRequestLogger_DeterministicPreExistingCollision(t *testing.T) {
	logsDir := t.TempDir()
	const targetFilename = "v1_chat_completions-2026-09-23T120000-00000000.log"

	// 1. Create the initial file
	f1, path1, err1 := createUniqueLogFile(logsDir, targetFilename)
	if err1 != nil {
		t.Fatalf("first createUniqueLogFile error: %v", err1)
	}
	if _, errWrite := f1.WriteString("first-log-content"); errWrite != nil {
		_ = f1.Close()
		t.Fatalf("write first log: %v", errWrite)
	}
	if errClose := f1.Close(); errClose != nil {
		t.Fatalf("close first log: %v", errClose)
	}

	// 2. Deterministically request the exact same targetFilename again
	f2, path2, err2 := createUniqueLogFile(logsDir, targetFilename)
	if err2 != nil {
		t.Fatalf("second createUniqueLogFile error: %v", err2)
	}
	if _, errWrite := f2.WriteString("second-log-content"); errWrite != nil {
		_ = f2.Close()
		t.Fatalf("write second log: %v", errWrite)
	}
	if errClose := f2.Close(); errClose != nil {
		t.Fatalf("close second log: %v", errClose)
	}

	// Verify path2 received the _1 collision sequence
	expectedSecondName := "v1_chat_completions-2026-09-23T120000_1-00000000.log"
	if filepath.Base(path2) != expectedSecondName {
		t.Fatalf("path2 basename = %q, want %q", filepath.Base(path2), expectedSecondName)
	}

	// Verify path1 was NOT overwritten
	content1, errRead1 := os.ReadFile(path1)
	if errRead1 != nil {
		t.Fatalf("read path1: %v", errRead1)
	}
	if string(content1) != "first-log-content" {
		t.Fatalf("path1 content = %q, want first-log-content", string(content1))
	}

	// Verify path2 has second content
	content2, errRead2 := os.ReadFile(path2)
	if errRead2 != nil {
		t.Fatalf("read path2: %v", errRead2)
	}
	if string(content2) != "second-log-content" {
		t.Fatalf("path2 content = %q, want second-log-content", string(content2))
	}
}
