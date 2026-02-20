package codex

import (
	"errors"
	"testing"
)

func TestOAuthError_Error(t *testing.T) {
	err := &OAuthError{
		Code:        "invalid_request",
		Description: "The request is missing a required parameter",
	}
	expected := "OAuth error invalid_request: The request is missing a required parameter"
	if err.Error() != expected {
		t.Errorf("expected %s, got %s", expected, err.Error())
	}

	errNoDesc := &OAuthError{Code: "server_error"}
	expectedNoDesc := "OAuth error: server_error"
	if errNoDesc.Error() != expectedNoDesc {
		t.Errorf("expected %s, got %s", expectedNoDesc, errNoDesc.Error())
	}
}

func TestNewOAuthError(t *testing.T) {
	err := NewOAuthError("code", "desc", 400)
	if err.Code != "code" || err.Description != "desc" || err.StatusCode != 400 {
		t.Errorf("NewOAuthError failed: %+v", err)
	}
}

func TestAuthenticationError_Error(t *testing.T) {
	err := &AuthenticationError{
		Type:    "type",
		Message: "msg",
	}
	expected := "type: msg"
	if err.Error() != expected {
		t.Errorf("expected %s, got %s", expected, err.Error())
	}

	cause := errors.New("underlying")
	errWithCause := &AuthenticationError{
		Type:    "type",
		Message: "msg",
		Cause:   cause,
	}
	expectedWithCause := "type: msg (caused by: underlying)"
	if errWithCause.Error() != expectedWithCause {
		t.Errorf("expected %s, got %s", expectedWithCause, errWithCause.Error())
	}
}

func TestNewAuthenticationError(t *testing.T) {
	base := &AuthenticationError{Type: "base", Message: "msg", Code: 400}
	cause := errors.New("cause")
	err := NewAuthenticationError(base, cause)
	if err.Type != "base" || err.Message != "msg" || err.Code != 400 || err.Cause != cause {
		t.Errorf("NewAuthenticationError failed: %+v", err)
	}
}

func TestIsAuthenticationError(t *testing.T) {
	authErr := &AuthenticationError{}
	if !IsAuthenticationError(authErr) {
		t.Error("expected true for AuthenticationError")
	}
	if IsAuthenticationError(errors.New("other")) {
		t.Error("expected false for other error")
	}
}

func TestIsOAuthError(t *testing.T) {
	oauthErr := &OAuthError{}
	if !IsOAuthError(oauthErr) {
		t.Error("expected true for OAuthError")
	}
	if IsOAuthError(errors.New("other")) {
		t.Error("expected false for other error")
	}
}

func TestGetUserFriendlyMessage(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&AuthenticationError{Type: "token_expired"}, "Your authentication has expired. Please log in again."},
		{&AuthenticationError{Type: "token_invalid"}, "Your authentication is invalid. Please log in again."},
		{&AuthenticationError{Type: "authentication_required"}, "Please log in to continue."},
		{&AuthenticationError{Type: "port_in_use"}, "The required port is already in use. Please close any applications using port 3000 and try again."},
		{&AuthenticationError{Type: "callback_timeout"}, "Authentication timed out. Please try again."},
		{&AuthenticationError{Type: "browser_open_failed"}, "Could not open your browser automatically. Please copy and paste the URL manually."},
		{&AuthenticationError{Type: "unknown"}, "Authentication failed. Please try again."},
		{&OAuthError{Code: "access_denied"}, "Authentication was cancelled or denied."},
		{&OAuthError{Code: "invalid_request"}, "Invalid authentication request. Please try again."},
		{&OAuthError{Code: "server_error"}, "Authentication server error. Please try again later."},
		{&OAuthError{Code: "other", Description: "desc"}, "Authentication failed: desc"},
		{errors.New("random"), "An unexpected error occurred. Please try again."},
	}

	for _, tc := range cases {
		got := GetUserFriendlyMessage(tc.err)
		if got != tc.want {
			t.Errorf("GetUserFriendlyMessage(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
