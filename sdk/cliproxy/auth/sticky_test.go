package auth

import (
	"testing"
	"time"
)

func TestStickyStore_SetAndGet(t *testing.T) {
	s := newStickyStore()
	s.Set("sess-1", "auth-ai", time.Hour)

	got, ok := s.Get("sess-1")
	if !ok || got != "auth-ai" {
		t.Fatalf("expected auth-ai, got %q (ok=%v)", got, ok)
	}
}

func TestStickyStore_GetMiss(t *testing.T) {
	s := newStickyStore()
	_, ok := s.Get("nonexistent")
	if ok {
		t.Fatal("expected miss for nonexistent session")
	}
}

func TestStickyStore_GetExpired(t *testing.T) {
	s := newStickyStore()
	s.Set("sess-1", "auth-ai", time.Millisecond)
	time.Sleep(2 * time.Millisecond)

	_, ok := s.Get("sess-1")
	if ok {
		t.Fatal("expected miss for expired entry")
	}
}

func TestStickyStore_Delete(t *testing.T) {
	s := newStickyStore()
	s.Set("sess-1", "auth-ai", time.Hour)
	s.Delete("sess-1")

	_, ok := s.Get("sess-1")
	if ok {
		t.Fatal("expected miss after delete")
	}
}

func TestStickyStore_Overwrite(t *testing.T) {
	s := newStickyStore()
	s.Set("sess-1", "auth-ai", time.Hour)
	s.Set("sess-1", "auth-cc", time.Hour)

	got, ok := s.Get("sess-1")
	if !ok || got != "auth-cc" {
		t.Fatalf("expected auth-cc after overwrite, got %q", got)
	}
}

func TestStickyStore_Cleanup(t *testing.T) {
	s := newStickyStore()
	s.Set("expired", "auth-ai", time.Millisecond)
	s.Set("alive", "auth-cc", time.Hour)
	time.Sleep(2 * time.Millisecond)

	s.Cleanup()

	if s.Len() != 1 {
		t.Fatalf("expected 1 entry after cleanup, got %d", s.Len())
	}
	_, ok := s.Get("alive")
	if !ok {
		t.Fatal("alive entry should still exist")
	}
}

func TestStickyStore_Len(t *testing.T) {
	s := newStickyStore()
	if s.Len() != 0 {
		t.Fatalf("expected 0, got %d", s.Len())
	}
	s.Set("a", "x", time.Hour)
	s.Set("b", "y", time.Hour)
	if s.Len() != 2 {
		t.Fatalf("expected 2, got %d", s.Len())
	}
}
