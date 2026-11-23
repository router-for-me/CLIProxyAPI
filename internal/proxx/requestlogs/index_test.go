package requestlogs

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
)

// countMeta replaces readMetaFn with a counting wrapper for the test.
func countMeta(t *testing.T) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	orig := readMetaFn
	readMetaFn = func(path string, size int64) (fileMeta, error) {
		n.Add(1)
		return orig(path, size)
	}
	t.Cleanup(func() { readMetaFn = orig })
	return &n
}

// countFirstLine replaces firstLineFn with a counting wrapper for the test.
func countFirstLine(t *testing.T) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	orig := firstLineFn
	firstLineFn = func(path string) bool {
		n.Add(1)
		return orig(path)
	}
	t.Cleanup(func() { firstLineFn = orig })
	return &n
}

// newStatStore returns a store that has not started meta indexing (no viewer use yet).
func newStatStore(dir string, maxBytes int64) *Store {
	s := NewStore()
	s.Configure(dir, maxBytes, true)
	s.SetClock(func() time.Time { return baseTime.Add(time.Hour) })
	return s
}

// stampName returns a request log name whose file name timestamp is baseTime+i seconds.
func stampName(i int) string {
	return fmt.Sprintf("v1-chat-completions-%s-%08x.log", baseTime.Add(time.Duration(i)*time.Second).Format("2006-01-02T150405"), i)
}

func TestStatRefreshReadsNoBodies(t *testing.T) {
	metaCalls := countMeta(t)
	dir := t.TempDir()
	var want int64
	for i := 0; i < 5; i++ {
		path := fixtureCopy(t, dir, "openai-nonstream.log", logName("", i), baseTime.Add(time.Duration(i)*time.Second))
		info, _ := os.Stat(path)
		want += info.Size()
	}
	s := newStatStore(dir, 0)
	s.Refresh()
	s.Refresh()
	if n := metaCalls.Load(); n != 0 {
		t.Fatalf("stat refresh parsed %d files", n)
	}
	if got := s.Status().TotalBytes; got != want {
		t.Fatalf("total = %d, want %d", got, want)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) != 5 {
		t.Fatalf("entries = %d", len(s.entries))
	}
	for _, e := range s.entries {
		if !e.isLog || e.hasMeta || !e.settled {
			t.Fatalf("entry %+v", e)
		}
	}
}

func TestRetentionWithoutMeta(t *testing.T) {
	metaCalls := countMeta(t)
	dir := t.TempDir()
	var sizes []int64
	for i := 0; i < 5; i++ {
		path := fixtureCopy(t, dir, "openai-nonstream.log", logName("", i), baseTime.Add(time.Duration(i)*time.Minute))
		info, _ := os.Stat(path)
		sizes = append(sizes, info.Size())
	}
	old := baseTime.Add(-48 * time.Hour)
	// Matches the name pattern but is not a request log: never counted, never deleted.
	fake := writeTemp(t, dir, logName("", 0x77), strings.Repeat("not a request log\n", 10000))
	_ = os.Chtimes(fake, old, old)
	appLog := writeTemp(t, dir, "main.log", strings.Repeat("x", 100000))
	_ = os.Chtimes(appLog, old, old)
	// The oldest request log by name, but still being written ("now"): skipped.
	busy := fixtureCopy(t, dir, "openai-nonstream.log", logName("", 0x99), baseTime.Add(-time.Hour))
	now := baseTime.Add(time.Hour)
	_ = os.Chtimes(busy, now, now)
	busyInfo, _ := os.Stat(busy)

	s := newStatStore(dir, sizes[3]+sizes[4]+busyInfo.Size())
	s.Refresh()
	if n := metaCalls.Load(); n != 0 {
		t.Fatalf("retention parsed %d files", n)
	}
	for _, p := range []string{fake, appLog, busy} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("retention removed %s", filepath.Base(p))
		}
	}
	for i := 0; i < 5; i++ {
		_, err := os.Stat(filepath.Join(dir, logName("", i)))
		if gone := os.IsNotExist(err); gone != (i < 3) {
			t.Fatalf("file %d gone=%v", i, gone)
		}
	}
	if got, want := s.Status().TotalBytes, sizes[3]+sizes[4]+busyInfo.Size(); got != want {
		t.Fatalf("total = %d, want %d", got, want)
	}
}

func TestEmptyFileBecomesRequestLog(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, logName("", 1), "")
	now := baseTime.Add(time.Hour)
	s := NewStore()
	s.Configure(dir, 0, true)
	clock := now
	s.SetClock(func() time.Time { return clock })
	_ = os.Chtimes(path, now, now)
	s.Refresh()
	if s.Status().TotalBytes != 0 {
		t.Fatal("empty file counted")
	}
	// The writer fills the file after creating it.
	fixtureCopy(t, dir, "openai-nonstream.log", logName("", 1), baseTime)
	_ = os.Chtimes(path, now, now)
	clock = now.Add(2 * time.Second)
	s.Refresh()
	info, _ := os.Stat(path)
	if got := s.Status().TotalBytes; got != info.Size() {
		t.Fatalf("total = %d, want %d", got, info.Size())
	}
	indexSync(s)
	p, _ := s.List(Query{})
	if len(p.Items) != 1 || p.IndexTotal != 1 || p.IndexPending != 0 {
		t.Fatalf("items=%d total=%d pending=%d", len(p.Items), p.IndexTotal, p.IndexPending)
	}
}

func TestWorkerNewestFirstAndPartialList(t *testing.T) {
	orig := readMetaFn
	origBatch := indexBatchSize
	indexBatchSize = 2
	var (
		mu    sync.Mutex
		order []string
	)
	entered := make(chan struct{})
	release := make(chan struct{})
	readMetaFn = func(path string, size int64) (fileMeta, error) {
		mu.Lock()
		order = append(order, filepath.Base(path))
		n := len(order)
		mu.Unlock()
		if n == 3 {
			close(entered)
			<-release
		}
		return orig(path, size)
	}
	defer func() { readMetaFn, indexBatchSize = orig, origBatch }()

	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		fixtureCopy(t, dir, "openai-nonstream.log", stampName(i), baseTime.Add(time.Duration(i)*time.Second))
	}
	s := newStatStore(dir, 0)
	p, err := s.List(Query{}) // first viewer use starts the worker and does not wait for it
	if err != nil {
		t.Fatal(err)
	}
	if p.IndexTotal != 5 {
		t.Fatalf("index_total = %d", p.IndexTotal)
	}
	<-entered // the first batch (2 newest files) is merged; the worker is inside the second batch
	p, _ = s.List(Query{})
	if len(p.Items) != 2 || p.Items[0].ID != "00000004" || p.Items[1].ID != "00000003" || p.IndexPending != 3 || p.IndexTotal != 5 {
		t.Fatalf("partial list items=%v pending=%d total=%d", ids(p.Items), p.IndexPending, p.IndexTotal)
	}
	close(release)
	s.waitIndexIdle()
	p, _ = s.List(Query{})
	if len(p.Items) != 5 || p.IndexPending != 0 {
		t.Fatalf("final list items=%d pending=%d", len(p.Items), p.IndexPending)
	}
	mu.Lock()
	defer mu.Unlock()
	for i, name := range order {
		if name != stampName(4-i) {
			t.Fatalf("parse order %v, want newest first", order)
		}
	}
}

func ids(rows []Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

// indexedStore builds a fully indexed store that persists to indexPath.
func indexedStore(t *testing.T, dir, indexPath string) *Store {
	t.Helper()
	s := NewStore()
	s.setIndexFile(indexPath)
	s.Configure(dir, 0, true)
	s.SetClock(func() time.Time { return baseTime.Add(time.Hour) })
	indexSync(s)
	return s
}

func listRows(t *testing.T, s *Store) Page {
	t.Helper()
	p, err := s.List(Query{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func writeIndexFixtures(t *testing.T, dir string) {
	t.Helper()
	fixtures := []string{"openai-nonstream.log", "openai-stream.log", "failover.log", "error-400.log"}
	for i, f := range fixtures {
		prefix := ""
		if strings.HasPrefix(f, "error-") {
			prefix = "error-"
		}
		fixtureCopy(t, dir, f, prefix+stampName(i), baseTime.Add(time.Duration(i)*time.Second))
	}
}

func TestPersistedIndexRestart(t *testing.T) {
	dir := t.TempDir()
	indexPath := filepath.Join(t.TempDir(), IndexFileName)
	writeIndexFixtures(t, dir)
	a := indexedStore(t, dir, indexPath)
	before := listRows(t, a)
	if len(before.Items) != 4 {
		t.Fatalf("rows = %d", len(before.Items))
	}
	a.saveIndex(true)
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatal("index not saved")
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(indexPath), "*.tmp"))
	if len(leftovers) != 0 {
		t.Fatalf("temp files left: %v", leftovers)
	}

	metaCalls := countMeta(t)
	firstLines := countFirstLine(t)
	b := indexedStore(t, dir, indexPath)
	after := listRows(t, b)
	if n := metaCalls.Load(); n != 0 {
		t.Fatalf("restart re-parsed %d files", n)
	}
	if n := firstLines.Load(); n != 0 {
		t.Fatalf("restart read %d first lines", n)
	}
	if after.IndexPending != 0 || after.IndexTotal != 4 {
		t.Fatalf("pending=%d total=%d", after.IndexPending, after.IndexTotal)
	}
	if !reflect.DeepEqual(before.Items, after.Items) {
		t.Fatalf("rows differ after restart:\nbefore %+v\nafter  %+v", before.Items, after.Items)
	}
	if !reflect.DeepEqual(before.Models, after.Models) || !reflect.DeepEqual(before.Providers, after.Providers) {
		t.Fatalf("facets differ: %v %v / %v %v", before.Models, before.Providers, after.Models, after.Providers)
	}
}

func TestPersistedIndexDropsStaleRecords(t *testing.T) {
	dir := t.TempDir()
	indexPath := filepath.Join(t.TempDir(), IndexFileName)
	writeIndexFixtures(t, dir)
	a := indexedStore(t, dir, indexPath)
	a.saveIndex(true)

	// File 0 is gone, file 1 changed size, file 2 changed mtime, file 3 is unchanged.
	if err := os.Remove(filepath.Join(dir, stampName(0))); err != nil {
		t.Fatal(err)
	}
	p1 := filepath.Join(dir, stampName(1))
	info1, _ := os.Stat(p1)
	f, _ := os.OpenFile(p1, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("\n")
	_ = f.Close()
	_ = os.Chtimes(p1, info1.ModTime(), info1.ModTime())
	p2 := filepath.Join(dir, stampName(2))
	mt := baseTime.Add(5 * time.Second)
	_ = os.Chtimes(p2, mt, mt)

	metaCalls := countMeta(t)
	b := indexedStore(t, dir, indexPath)
	p := listRows(t, b)
	if n := metaCalls.Load(); n != 2 {
		t.Fatalf("re-parsed %d files, want 2", n)
	}
	if len(p.Items) != 3 || p.IndexPending != 0 {
		t.Fatalf("rows = %d pending = %d", len(p.Items), p.IndexPending)
	}
}

func TestPersistedIndexInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	writeIndexFixtures(t, dir)
	good := filepath.Join(t.TempDir(), IndexFileName)
	indexedStore(t, dir, good).saveIndex(true)
	data, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	var f indexFile
	if err = json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if f.Version != indexVersion || f.LogDir != dir || len(f.Files) != 4 {
		t.Fatalf("index header %d %q %d", f.Version, f.LogDir, len(f.Files))
	}
	encode := func(v indexFile) []byte {
		out, errMarshal := json.Marshal(v)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		return out
	}
	wrongVersion, wrongDir := f, f
	wrongVersion.Version = indexVersion + 1
	wrongDir.LogDir = dir + "-other"
	cases := map[string][]byte{
		"missing":  nil,
		"corrupt":  []byte(`{"version":1,"log_dir":`),
		"version":  encode(wrongVersion),
		"log_dir":  encode(wrongDir),
		"not json": []byte("hello"),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), IndexFileName)
			if content != nil {
				if errWrite := os.WriteFile(path, content, 0o644); errWrite != nil {
					t.Fatal(errWrite)
				}
			}
			metaCalls := countMeta(t)
			s := indexedStore(t, dir, path)
			p := listRows(t, s)
			if n := metaCalls.Load(); n != 4 {
				t.Fatalf("parsed %d files, want 4 (empty index)", n)
			}
			if len(p.Items) != 4 {
				t.Fatalf("rows = %d", len(p.Items))
			}
		})
	}
}

func TestSaveAfterDirChangeUsesNewDir(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	writeIndexFixtures(t, dirA)
	fixtureCopy(t, dirB, "openai-nonstream.log", stampName(9), baseTime)
	indexPath := filepath.Join(t.TempDir(), IndexFileName)
	s := indexedStore(t, dirA, indexPath)
	s.Configure(dirB, 0, true)
	indexSync(s)
	s.saveIndex(true)
	data, _ := os.ReadFile(indexPath)
	var f indexFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if f.LogDir != dirB || len(f.Files) != 1 || f.Files[0].Name != stampName(9) {
		t.Fatalf("saved %q with %d files", f.LogDir, len(f.Files))
	}
}

func TestDetailParsesOnDemand(t *testing.T) {
	dir := t.TempDir()
	writeIndexFixtures(t, dir)
	indexPath := filepath.Join(t.TempDir(), IndexFileName)
	s := newStatStore(dir, 0)
	s.setIndexFile(indexPath)
	// Hold the worker so only Detail can parse the file.
	orig := readMetaFn
	block := make(chan struct{})
	readMetaFn = func(path string, size int64) (fileMeta, error) {
		if filepath.Base(path) != stampName(2) {
			<-block
		}
		return orig(path, size)
	}
	defer func() { readMetaFn = orig }()
	d, err := s.Detail("00000002")
	if err != nil {
		t.Fatal(err)
	}
	if d.Row.Group != "auto" || d.Row.Attempts != 2 {
		t.Fatalf("detail row = %+v", d.Row)
	}
	close(block)
	s.waitIndexIdle()
	s.mu.Lock()
	e := s.entries[stampName(2)]
	ok := e != nil && e.hasMeta && e.meta.valid
	s.mu.Unlock()
	if !ok {
		t.Fatal("detail did not merge meta into the index")
	}
}

func TestLiveTailAnnouncesOnlyFilesCompletingAfterSubscribe(t *testing.T) {
	dir := t.TempDir()
	fixtureCopy(t, dir, "openai-nonstream.log", stampName(1), baseTime)
	s := NewStore()
	s.Configure(dir, 0, true)
	now := baseTime.Add(time.Hour)
	s.SetClock(func() time.Time { return now })
	// Viewer use long before the tail opens: the backlog is indexed, nothing is announced.
	indexSync(s)

	// Written after the last refresh but finished before the subscription: never announced.
	late := fixtureCopy(t, dir, "openai-nonstream.log", stampName(2), baseTime)
	before := now.Add(-500 * time.Millisecond)
	_ = os.Chtimes(late, before, before)
	now = now.Add(10 * time.Second)

	rows, cancel := s.Subscribe()
	defer cancel()
	s.Refresh()
	s.waitIndexIdle()
	s.Refresh()
	select {
	case r := <-rows:
		t.Fatalf("announced %s", r.ID)
	default:
	}

	// Finishes after the subscription, observed while still being written, then settles.
	path := fixtureCopy(t, dir, "openai-stream.log", stampName(3), baseTime)
	_ = os.Chtimes(path, now, now)
	s.Refresh()
	select {
	case r := <-rows:
		t.Fatalf("emitted incomplete file %s", r.ID)
	default:
	}
	now = now.Add(2 * time.Second)
	s.Refresh()
	select {
	case r := <-rows:
		if r.ID != "00000003" || !r.Stream {
			t.Fatalf("row = %+v", r)
		}
	default:
		t.Fatal("completed file not emitted")
	}
	// Settled before the next poll saw it (poll and settle delay are both 1s): still announced.
	quick := fixtureCopy(t, dir, "openai-nonstream.log", stampName(4), baseTime)
	_ = os.Chtimes(quick, now, now)
	now = now.Add(3 * time.Second)
	s.Refresh()
	select {
	case r := <-rows:
		if r.ID != "00000004" {
			t.Fatalf("row = %+v", r)
		}
	default:
		t.Fatal("quickly settled file not emitted")
	}
	s.Refresh()
	select {
	case r := <-rows:
		t.Fatalf("emitted twice: %s", r.ID)
	default:
	}
}
