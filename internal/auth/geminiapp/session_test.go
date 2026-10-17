package geminiapp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestParseCookies(t *testing.T) {
	for _, input := range []string{
		`{"__Secure-1PSID":"session","__Secure-1PSIDTS":"timestamp","SID":"discard"}`,
		`[{"name":"__Secure-1PSID","value":"session","domain":".google.com"},{"name":"__Secure-1PSIDTS","value":"timestamp","domain":"gemini.google.com"},{"name":"SID","value":"discard"}]`,
	} {
		cookies, err := ParseCookies([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if len(cookies) != 2 || cookies[CookiePSID] != "session" || cookies[CookiePSIDTS] != "timestamp" {
			t.Fatalf("unexpected retained cookies: %v", cookies)
		}
	}
	for _, input := range []string{`null`, `{}`, `invalid`, `{"__Secure-1PSID":"invalid;secret"}`, `[{"name":"__Secure-1PSID","value":"secret","domain":"google.com.evil.test"}]`} {
		_, err := ParseCookies([]byte(input))
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("expected sanitized error, got %v", err)
		}
	}
}

func TestFetchSessionAndCookieIsolation(t *testing.T) {
	var calls int
	base := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host != "gemini.google.com" {
			t.Fatal("followed a cross-origin redirect")
		}
		cookie, err := req.Cookie(CookiePSID)
		if err != nil || cookie.Value != "session" {
			t.Fatal("missing session cookie")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"SNlM0e":"csrf\u002btoken","cfb2h":"build","FdrFJe":"session-id"}`)), Header: make(http.Header)}, nil
	})}
	client := NewClient(base, map[string]string{CookiePSID: "session"})
	session, err := FetchSession(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}
	if session.Token != "csrf+token" || session.Build != "build" || session.ID != "session-id" || calls != 1 {
		t.Fatalf("unexpected session: %+v", session)
	}
	if base.Jar != nil || base.CheckRedirect != nil {
		t.Fatal("mutated shared HTTP client")
	}
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "gemini.google.com" {
			t.Fatal("credentials left Gemini origin")
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://accounts.google.com/login"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	_, err = FetchSession(context.Background(), client)
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != 401 {
		t.Fatalf("expected authentication error: %v", err)
	}
}

func TestFetchSessionMissingToken(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("signed out page with secret")), Header: make(http.Header)}, nil
	})}
	_, err := FetchSession(context.Background(), client)
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != 401 || strings.Contains(err.Error(), "secret") {
		t.Fatalf("expected sanitized auth error: %v", err)
	}
}
