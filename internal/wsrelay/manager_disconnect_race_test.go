package wsrelay

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// When a disconnecting session no longer owns the provider key, a newer session
// has replaced it: the disconnect must be reported as a replacement so the
// service layer skips the Delete that would drop the replacement's auth (#5392,
// Race B).
func TestHandleSessionClosed_ReportsReplacementInsteadOfOriginalCause(t *testing.T) {
	var mu sync.Mutex
	var causes []error
	m := NewManager(Options{OnDisconnected: func(provider string, err error) {
		mu.Lock()
		defer mu.Unlock()
		causes = append(causes, err)
	}})

	s1 := &session{provider: "prov", closed: make(chan struct{})}
	s2 := &session{provider: "prov", closed: make(chan struct{})}
	m.sessMutex.Lock()
	m.sessions["prov"] = s1
	m.sessMutex.Unlock()
	m.sessMutex.Lock()
	m.sessions["prov"] = s2
	m.sessMutex.Unlock()

	m.handleSessionClosed(s1, errors.New("original read error"))

	if got := m.session("prov"); got != s2 {
		t.Fatal("replacement session was removed from the provider key")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(causes) != 1 {
		t.Fatalf("onDisconnected calls = %d, want 1", len(causes))
	}
	if causes[0] == nil || !strings.Contains(causes[0].Error(), "replaced by new connection") {
		t.Fatalf("cause = %v, want replacement notice; the original cause would drop the live replacement's auth", causes[0])
	}
}

// A disconnecting session that still owns the provider key keeps the original
// cause and removes the entry.
func TestHandleSessionClosed_OwnerKeepsCauseAndDeletes(t *testing.T) {
	var mu sync.Mutex
	var causes []error
	m := NewManager(Options{OnDisconnected: func(provider string, err error) {
		mu.Lock()
		defer mu.Unlock()
		causes = append(causes, err)
	}})

	s1 := &session{provider: "owner", closed: make(chan struct{})}
	m.sessMutex.Lock()
	m.sessions["owner"] = s1
	m.sessMutex.Unlock()

	m.handleSessionClosed(s1, errors.New("connection reset"))

	if got := m.session("owner"); got != nil {
		t.Fatal("closed owner session was not removed from the provider key")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(causes) != 1 || causes[0] == nil || causes[0].Error() != "connection reset" {
		t.Fatalf("cause = %v, want the original error", causes)
	}
}
