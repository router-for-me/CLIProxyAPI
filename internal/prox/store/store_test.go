package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type doc struct {
	A string `json:"a"`
	N int    `json:"n"`
}

func TestRoundTripAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if ok, _ := s.Writable(); !ok {
		t.Fatal("first opener must be writable")
	}
	var got doc
	if res, _ := s.Load("state.json", &got); res != Missing {
		t.Fatalf("expected missing, got %s", res)
	}
	if err := s.Save("state.json", doc{"x", 3}); err != nil {
		t.Fatal(err)
	}
	if res, _ := s.Load("state.json", &got); res != Loaded || got != (doc{"x", 3}) {
		t.Fatalf("round trip: %s %+v", res, got)
	}
	info, _ := os.Stat(filepath.Join(dir, "state.json"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode %v", info.Mode().Perm())
	}
	dinfo, _ := os.Stat(dir)
	if dinfo.Mode().Perm() != 0o700 {
		t.Fatalf("state dir mode %v", dinfo.Mode().Perm())
	}
}

func TestCorruptFileIsQuarantined(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"a":`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _ := Open(dir)
	defer s.Close()
	var got doc
	if res, _ := s.Load("state.json", &got); res != Quarantined {
		t.Fatalf("expected quarantine, got %s", res)
	}
	entries, _ := os.ReadDir(dir)
	found := false
	for _, e := range entries {
		found = found || strings.HasPrefix(e.Name(), "state.json.corrupt-")
	}
	if !found {
		t.Fatal("corrupt file not moved aside")
	}
	if res, _ := s.Load("state.json", &got); res != Missing {
		t.Fatalf("after quarantine expected missing, got %s", res)
	}
}

func TestInterruptedSaveKeepsPreviousDocument(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	defer s.Close()
	_ = s.Save("state.json", doc{"old", 1})
	// Simulate a crash after the temp file was partly written.
	_ = os.WriteFile(filepath.Join(dir, "state.json.tmp"), []byte(`{"a":"new","n`), 0o600)
	var got doc
	if res, _ := s.Load("state.json", &got); res != Loaded || got.A != "old" {
		t.Fatalf("interrupted save corrupted state: %s %+v", res, got)
	}
	if err := s.Save("state.json", doc{"new", 2}); err != nil {
		t.Fatal(err)
	}
	if res, _ := s.Load("state.json", &got); res != Loaded || got.A != "new" {
		t.Fatalf("save after interruption: %s %+v", res, got)
	}
}

func TestSecondProcessIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	a, _ := Open(dir)
	defer a.Close()
	_ = a.Save("state.json", doc{"owner", 1})
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if ok, reason := b.Writable(); ok || reason != "locked_by_another_process" {
		t.Fatalf("second opener writable=%v reason=%s", ok, reason)
	}
	_ = b.Save("state.json", doc{"intruder", 2})
	var got doc
	_, _ = a.Load("state.json", &got)
	if got.A != "owner" {
		t.Fatal("read-only store wrote")
	}
	a.Close()
	c, _ := Open(dir)
	defer c.Close()
	if ok, _ := c.Writable(); !ok {
		t.Fatal("lock not released on close")
	}
}
