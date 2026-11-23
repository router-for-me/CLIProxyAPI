package requestlogs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestIndexDoesNotRetainRequestBodies guards against index entries keeping whole request
// bodies alive through substrings (the requested model used to alias the body buffer).
func TestIndexDoesNotRetainRequestBodies(t *testing.T) {
	const files = 30
	const bodySize = 2 << 20
	dir := t.TempDir()
	reqBody := `{"model":"big-model","stream":false,"messages":[{"role":"user","content":"` +
		strings.Repeat("x", bodySize) + `"}]}`
	for i := 0; i < files; i++ {
		path := writeTemp(t, dir, logName("", i), logFile(reqBody, "application/json", `{"usage":{"prompt_tokens":1}}`, ""))
		_ = os.Chtimes(path, baseTime, baseTime) // settled for the test clock
	}
	reqBody = ""

	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	s := newTestStore(dir, 0) // indexes every file and waits for the worker

	var after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&after)

	if got := len(s.entries); got != files {
		t.Fatalf("indexed %d files, want %d", got, files)
	}
	for _, e := range s.entries {
		if e.meta.requestedModel != "big-model" {
			t.Fatalf("requested model = %q", e.meta.requestedModel)
		}
	}
	grown := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if limit := int64(files * bodySize / 4); grown > limit {
		t.Fatalf("index retains %d MB after indexing %d files of %d MB bodies (limit %d MB)",
			grown>>20, files, bodySize>>20, limit>>20)
	}
	runtime.KeepAlive(s)
}

// TestRefreshDoesNotBlockConfigure guards against file reads holding the store lock: on a large log
// directory the initial scan stalled config reloads (and client loading) for minutes. It covers the
// first-line reads of the stat refresh, the meta worker and the inline parse of new files.
func TestRefreshDoesNotBlockConfigure(t *testing.T) {
	dir := t.TempDir()
	first := writeTemp(t, dir, logName("", 1), logFile(`{"model":"m"}`, "application/json", `{}`, ""))
	old := time.Now().Add(-time.Minute) // settled, so the worker parses it
	_ = os.Chtimes(first, old, old)

	configureWhile := func(t *testing.T, s *Store, phase string) {
		t.Helper()
		configured := make(chan struct{})
		go func() {
			s.Configure(dir, 1<<30, true)
			_ = s.Status()
			close(configured)
		}()
		select {
		case <-configured:
		case <-time.After(5 * time.Second):
			t.Fatalf("Configure blocked while %s was reading files", phase)
		}
	}

	// Stat refresh: block inside the first-line check.
	origFirst := firstLineFn
	defer func() { firstLineFn = origFirst }()
	entered := make(chan struct{})
	release := make(chan struct{})
	firstLineFn = func(path string) bool {
		close(entered)
		<-release
		return origFirst(path)
	}
	s := NewStore()
	s.Configure(dir, 0, true)
	refreshed := make(chan struct{})
	go func() {
		s.Refresh()
		close(refreshed)
	}()
	<-entered
	configureWhile(t, s, "the stat refresh")
	close(release)
	<-refreshed
	firstLineFn = origFirst // the later phases must not block in the first-line check

	// Meta worker and inline parse: block inside readMetaFn.
	orig := readMetaFn
	defer func() { readMetaFn = orig }()
	block := make(chan struct{}, 1)
	gate := make(chan struct{})
	readMetaFn = func(path string, size int64) (fileMeta, error) {
		block <- struct{}{}
		<-gate
		return orig(path, size)
	}
	listed := make(chan struct{})
	go func() {
		_, _ = s.List(Query{})
		close(listed)
	}()
	<-block
	configureWhile(t, s, "the meta worker")
	gate <- struct{}{}
	<-listed
	s.waitIndexIdle()

	writeTemp(t, dir, logName("", 2), logFile(`{"model":"m"}`, "application/json", `{}`, ""))
	_ = os.Chtimes(filepath.Join(dir, logName("", 2)), old, old)
	refreshed2 := make(chan struct{})
	go func() {
		s.Refresh()
		close(refreshed2)
	}()
	<-block
	configureWhile(t, s, "the inline parse")
	gate <- struct{}{}
	<-refreshed2
	s.mu.Lock()
	n := len(s.entries)
	s.mu.Unlock()
	if n != 2 {
		t.Fatalf("indexed %d files, want 2", n)
	}
}
