package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestFormatAntigravityCallbackServerError_PortInUse(t *testing.T) {
	msg := formatAntigravityCallbackServerError(51121, errors.New("listen tcp :51121: bind: address already in use"))
	if !strings.Contains(strings.ToLower(msg), "already in use") {
		t.Fatalf("expected in-use hint, got %q", msg)
	}
	if !strings.Contains(msg, "--oauth-callback-port") {
		t.Fatalf("expected callback-port suggestion, got %q", msg)
	}
}

func TestFormatAntigravityCallbackServerError_Permission(t *testing.T) {
	msg := formatAntigravityCallbackServerError(51121, errors.New("listen tcp :51121: bind: An attempt was made to access a socket in a way forbidden by its access permissions."))
	if !strings.Contains(strings.ToLower(msg), "blocked by os policy") {
		t.Fatalf("expected permission hint, got %q", msg)
	}
	if !strings.Contains(msg, "--oauth-callback-port") {
		t.Fatalf("expected callback-port suggestion, got %q", msg)
	}
}
