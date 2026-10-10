package auth

import (
	"strings"
	"testing"
)

func TestSessionLogKey(t *testing.T) {
	a := "header:fr-0123456789abcdef-repo-diff-a"
	b := "header:fr-0123456789abcdef-repo-diff-b"
	if truncateSessionID(a) != truncateSessionID(b) {
		t.Fatalf("precondition: truncated ids should collide")
	}
	ka, kb := sessionLogKey(a), sessionLogKey(b)
	if ka == kb {
		t.Fatalf("distinct sessions share log key %q", ka)
	}
	if ka != sessionLogKey(a) {
		t.Fatalf("log key not stable")
	}
	if !strings.HasPrefix(ka, "header:") || len(ka) != len("header:")+16 {
		t.Fatalf("unexpected log key shape %q", ka)
	}
	if strings.Contains(ka, "fr-0123") {
		t.Fatalf("log key leaks raw id: %q", ka)
	}
	if got := sessionLogKey("noprefixsessionidvalue"); len(got) != 16 {
		t.Fatalf("unprefixed key shape %q", got)
	}
	if sessionLogKey("") != "" {
		t.Fatalf("empty id should give empty key")
	}
}
