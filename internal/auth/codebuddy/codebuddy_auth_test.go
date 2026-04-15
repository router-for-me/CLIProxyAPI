package codebuddy_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codebuddy"
)

func TestDecodeUserID_ValidJWT(t *testing.T) {
	// JWT payload: {"sub":"test-user-id-123","iat":1234567890}
	// base64url encode: eyJzdWIiOiJ0ZXN0LXVzZXItaWQtMTIzIiwiaWF0IjoxMjM0NTY3ODkwfQ
	token := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ0ZXN0LXVzZXItaWQtMTIzIiwiaWF0IjoxMjM0NTY3ODkwfQ.sig"
	auth := codebuddy.NewCodeBuddyAuth(nil)
	userID, err := auth.DecodeUserID(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if userID != "test-user-id-123" {
		t.Errorf("expected 'test-user-id-123', got '%s'", userID)
	}
}
