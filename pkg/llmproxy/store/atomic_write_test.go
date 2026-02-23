package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWriteFileAtomically_ConcurrentWritersNoTempCollisions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "auth.json")

	const writers = 48
	errCh := make(chan error, writers)
	var wg sync.WaitGroup

	for i := 0; i < writers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			payload := []byte(fmt.Sprintf(`{"writer":%d}`, i))
			if err := writeFileAtomically(target, payload, 0o600); err != nil {
				errCh <- err
			}
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("atomic write failed: %v", err)
		}
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected non-empty final file content")
	}

	tmpPattern := filepath.Join(dir, ".auth.json.tmp-*")
	tmpFiles, err := filepath.Glob(tmpPattern)
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(tmpFiles) != 0 {
		t.Fatalf("expected no temp files left behind, found %d", len(tmpFiles))
	}
}
