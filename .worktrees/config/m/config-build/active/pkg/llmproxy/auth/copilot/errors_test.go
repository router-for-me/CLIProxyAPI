package copilot

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

	if errWithCause.Unwrap() != cause {
		t.Error("Unwrap failed")
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
		{&AuthenticationError{Type: "device_code_failed"}, "Failed to start GitHub authentication. Please check your network connection and try again."},
		{&AuthenticationError{Type: "device_code_expired"}, "The authentication code has expired. Please try again."},
		{&AuthenticationError{Type: "authorization_pending"}, "Waiting for you to authorize the application on GitHub."},
		{&AuthenticationError{Type: "slow_down"}, "Please wait a moment before trying again."},
		{&AuthenticationError{Type: "access_denied"}, "Authentication was cancelled or denied."},
		{&AuthenticationError{Type: "token_exchange_failed"}, "Failed to complete authentication. Please try again."},
		{&AuthenticationError{Type: "polling_timeout"}, "Authentication timed out. Please try again."},
		{&AuthenticationError{Type: "user_info_failed"}, "Failed to get your GitHub account information. Please try again."},
		{&AuthenticationError{Type: "unknown"}, "Authentication failed. Please try again."},
		{&OAuthError{Code: "access_denied"}, "Authentication was cancelled or denied."},
		{&OAuthError{Code: "invalid_request"}, "Invalid authentication request. Please try again."},
		{&OAuthError{Code: "server_error"}, "GitHub server error. Please try again later."},
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
