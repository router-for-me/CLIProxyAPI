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

func TestStickyStore_MaxEntries(t *testing.T) {
	s := newStickyStore()
	s.maxEntries = 2

	s.Set("a", "x", time.Hour)
	s.Set("b", "y", time.Hour)
	s.Set("c", "z", time.Hour) // should be silently dropped

	if s.Len() != 2 {
		t.Fatalf("expected 2 (capped), got %d", s.Len())
	}
	if _, ok := s.Get("c"); ok {
		t.Fatal("entry 'c' should have been dropped due to capacity")
	}
	// overwriting existing entry should still work at capacity
	s.Set("a", "updated", time.Hour)
	got, ok := s.Get("a")
	if !ok || got != "updated" {
		t.Fatalf("expected 'updated' for overwrite at capacity, got %q (ok=%v)", got, ok)
	}
}

func TestStickyKey(t *testing.T) {
	cases := []struct {
		sessionID string
		model     string
		want      string
	}{
		{"sess-1", "claude-3-opus", "sess-1|claude-3-opus"},
		{"sess-1", "claude-3-sonnet", "sess-1|claude-3-sonnet"},
		{"", "claude-3-opus", "|claude-3-opus"},
		{"sess-1", "", "sess-1|"},
	}
	for _, tc := range cases {
		got := stickyKey(tc.sessionID, tc.model)
		if got != tc.want {
			t.Errorf("stickyKey(%q, %q) = %q, want %q", tc.sessionID, tc.model, got, tc.want)
		}
	}
}

func TestStickyStore_CompositeKey(t *testing.T) {
	s := newStickyStore()

	// Same session ID, different models → independent bindings
	k1 := stickyKey("sess-1", "claude-3-opus")
	k2 := stickyKey("sess-1", "claude-3-sonnet")

	s.Set(k1, "auth-cc", time.Hour)
	s.Set(k2, "auth-ai", time.Hour)

	got1, ok1 := s.Get(k1)
	if !ok1 || got1 != "auth-cc" {
		t.Fatalf("expected auth-cc for opus key, got %q (ok=%v)", got1, ok1)
	}
	got2, ok2 := s.Get(k2)
	if !ok2 || got2 != "auth-ai" {
		t.Fatalf("expected auth-ai for sonnet key, got %q (ok=%v)", got2, ok2)
	}

	// Deleting one doesn't affect the other
	s.Delete(k1)
	if _, ok := s.Get(k1); ok {
		t.Fatal("expected miss after deleting opus key")
	}
	if _, ok := s.Get(k2); !ok {
		t.Fatal("sonnet key should still exist after deleting opus key")
	}
}
