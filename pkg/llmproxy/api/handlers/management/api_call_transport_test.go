package management

import (
	"errors"
	"net/http"
	"net/url"
	"testing"
)

type apiCallRoundTripFunc func(*http.Request) (*http.Response, error)

func (f apiCallRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestAPICallGuardedRoundTripperRejectsUnsafeRequestURL(t *testing.T) {
	t.Parallel()

	called := false
	transport := apiCallGuardedRoundTripper{
		base: apiCallRoundTripFunc(func(*http.Request) (*http.Response, error) {
			called = true
			return nil, errors.New("base transport should not run")
		}),
	}

	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8317/ping", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := transport.RoundTrip(req); err == nil {
		t.Fatalf("RoundTrip error = nil, want unsafe target rejection")
	}
	if called {
		t.Fatal("base transport ran for unsafe URL")
	}
}

func TestAPICallRequestURLValidationRejectsUnsafeRedirectURL(t *testing.T) {
	t.Parallel()

	redirectURL, err := url.Parse("http://localhost:8317/ping")
	if err != nil {
		t.Fatalf("parse redirect url: %v", err)
	}
	req := &http.Request{URL: redirectURL}
	if err := validateAPICallRequestURL(req.URL); err == nil {
		t.Fatalf("validation error = nil, want unsafe redirect rejection")
	}
}
