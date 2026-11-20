package geminiapp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
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

func TestSessionCookieRotationReplacesImportedCookies(t *testing.T) {
	for _, domain := range []string{"", ".google.com", "gemini.google.com"} {
		t.Run("domain="+domain, func(t *testing.T) {
			base := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/app" {
					header := make(http.Header)
					for _, name := range []string{CookiePSID, CookiePSIDTS} {
						header.Add("Set-Cookie", (&http.Cookie{Name: name, Value: "rotated", Domain: domain, Path: "/", Secure: true}).String())
					}
					return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(`{"SNlM0e":"token"}`))}, nil
				}
				cookies := req.Cookies()
				if len(cookies) != 2 {
					t.Fatalf("expected exactly two session cookies, got %v", cookies)
				}
				for _, cookie := range cookies {
					if cookie.Value != "rotated" {
						t.Fatalf("stale cookie survived rotation: %s", cookie.Name)
					}
				}
				deleted := (&http.Cookie{Name: CookiePSIDTS, Domain: domain, Path: "/", Secure: true, MaxAge: -1}).String()
				return &http.Response{StatusCode: 200, Header: http.Header{"Set-Cookie": {deleted}}, Body: http.NoBody}, nil
			})}
			client := NewClient(base, map[string]string{CookiePSID: "old", CookiePSIDTS: "old"})
			if _, err := FetchSession(context.Background(), client); err != nil {
				t.Fatal(err)
			}
			resp, err := client.Post(BaseURL+"/generate", "text/plain", strings.NewReader("prompt"))
			if err != nil {
				t.Fatal(err)
			}
			if errClose := resp.Body.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			if cookies := SessionCookies(client); len(cookies) != 1 || cookies[CookiePSID] != "rotated" {
				t.Fatal("cookie snapshot lost the rotation or retained a deleted cookie")
			}
			for _, origin := range []string{"https://accounts.google.com", "https://other.gemini.google.com", "http://gemini.google.com"} {
				endpoint, err := url.Parse(origin)
				if err != nil {
					t.Fatal(err)
				}
				if len(client.Jar.Cookies(endpoint)) != 0 {
					t.Fatalf("session cookies escaped the Gemini HTTPS origin: %s", origin)
				}
			}
		})
	}
}
