package cache

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeCompactionHomeStorageFailsClosed(t *testing.T) {
	client := newFakeCodexReasoningReplayKVClient()
	original := currentClaudeCompactionKVClient
	currentClaudeCompactionKVClient = func() (codexReasoningReplayKVClient, bool, error) { return client, true, nil }
	t.Cleanup(func() { currentClaudeCompactionKVClient = original })
	ctx := context.Background()
	ref, err := StoreClaudeCompaction(ctx, []byte("saved"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadClaudeCompaction(ctx, ref)
	if err != nil || string(got) != "saved" {
		t.Fatalf("load = %s, %v", got, err)
	}
	if client.lastSetTTL != 0 {
		t.Fatal("compaction state must survive idle conversations")
	}
	client.getErr = errors.New("KV unavailable")
	if _, err := LoadClaudeCompaction(ctx, ref); err == nil {
		t.Fatal("KV failure ignored")
	}
	client.setErr = errors.New("KV unavailable")
	if _, err := StoreClaudeCompaction(ctx, []byte("unsaved")); err == nil {
		t.Fatal("failed cache write returned a reference")
	}
}

func TestClaudeCompactionDiskPersistenceAndIsolation(t *testing.T) {
	t.Setenv("WRITABLE_PATH", t.TempDir())
	ctx := context.Background()
	first := []byte(`{"output":[{"type":"compaction","encrypted_content":"first"}]}`)
	second := []byte(`{"output":[{"type":"compaction","encrypted_content":"second"}]}`)
	ref1, err := StoreClaudeCompaction(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	ref2, err := StoreClaudeCompaction(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if ref1 == ref2 {
		t.Fatal("distinct compactions reused a reference")
	}
	dir, err := claudeCompactionDirectory()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, ref1+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("file permissions = %o", info.Mode().Perm())
	}
	// Loads read durable files, without relying on process-local map state.
	for ref, want := range map[string][]byte{ref1: first, ref2: second} {
		got, err := LoadClaudeCompaction(ctx, ref)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("load = %s, %v", got, err)
		}
	}
	if _, err := LoadClaudeCompaction(ctx, strings.Repeat("0", 64)); err == nil {
		t.Fatal("missing state accepted")
	}
	if _, err := LoadClaudeCompaction(ctx, "../../etc/passwd"); err == nil {
		t.Fatal("invalid reference accepted")
	}
	if _, err := StoreClaudeCompaction(ctx, make([]byte, claudeCompactionMaxBytes+1)); err == nil {
		t.Fatal("oversized state accepted")
	}
}
