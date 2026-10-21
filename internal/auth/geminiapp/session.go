// Package geminiapp authenticates Gemini App requests using browser session cookies.
package geminiapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"

	log "github.com/sirupsen/logrus"
)

const (
	Provider     = "gemini-app"
	BaseURL      = "https://gemini.google.com"
	CookiePSID   = "__Secure-1PSID"
	CookiePSIDTS = "__Secure-1PSIDTS"
)

// Session contains the per-page tokens required by StreamGenerate.
type Session struct {
	Token string
	Build string
	ID    string
}

type sessionError struct {
	code    int
	message string
}

func (e sessionError) Error() string   { return e.message }
func (e sessionError) StatusCode() int { return e.code }

var pageFields = map[string]*regexp.Regexp{
	"token": regexp.MustCompile(`"SNlM0e"\s*:\s*("(?:[^"\\]|\\.)*")`),
	"build": regexp.MustCompile(`"cfb2h"\s*:\s*("(?:[^"\\]|\\.)*")`),
	"id":    regexp.MustCompile(`"FdrFJe"\s*:\s*("(?:[^"\\]|\\.)*")`),
}

// ParseCookies accepts a cookie-name object or a browser cookie export array.
// Only Gemini session cookies are retained; other Google cookies are discarded.
func ParseCookies(data []byte) (map[string]string, error) {
	var values map[string]string
	if err := json.Unmarshal(data, &values); err != nil {
		var exported []struct {
			Name   string `json:"name"`
			Value  string `json:"value"`
			Domain string `json:"domain"`
		}
		if err := json.Unmarshal(data, &exported); err != nil {
			return nil, fmt.Errorf("gemini-app: expected a cookie object or browser cookie export")
		}
		values = make(map[string]string)
		for _, cookie := range exported {
			domain := strings.TrimPrefix(cookie.Domain, ".")
			if domain == "" || domain == "google.com" || domain == "gemini.google.com" {
				values[cookie.Name] = cookie.Value
			}
		}
	}
	result := make(map[string]string)
	for _, name := range []string{CookiePSID, CookiePSIDTS} {
		value := strings.TrimSpace(values[name])
		if value == "" {
			continue
		}
		cookie := &http.Cookie{Name: name, Value: value}
		if err := cookie.Valid(); err != nil {
			return nil, fmt.Errorf("gemini-app: invalid %s cookie", name)
		}
		result[name] = value
	}
	if result[CookiePSID] == "" {
		return nil, fmt.Errorf("gemini-app: %s cookie is required", CookiePSID)
	}
	return result, nil
}

// sessionCookieJar keeps parent-domain rotations in the same host-only slot as
// imported cookies, so stale and fresh values cannot be sent together.
type sessionCookieJar struct{ http.CookieJar }

func (j sessionCookieJar) Cookies(endpoint *url.URL) []*http.Cookie {
	if endpoint.Scheme != "https" || endpoint.Host != "gemini.google.com" {
		return nil
	}
	return j.CookieJar.Cookies(endpoint)
}

func (j sessionCookieJar) SetCookies(endpoint *url.URL, cookies []*http.Cookie) {
	if endpoint.Scheme != "https" || endpoint.Host != "gemini.google.com" {
		return
	}
	for _, cookie := range cookies {
		if cookie.Name == CookiePSID || cookie.Name == CookiePSIDTS {
			domain := strings.TrimPrefix(strings.ToLower(cookie.Domain), ".")
			if domain != "" && domain != "google.com" && domain != endpoint.Hostname() {
				continue
			}
			isolated := *cookie
			isolated.Domain = ""
			cookie = &isolated
		}
		j.CookieJar.SetCookies(endpoint, []*http.Cookie{cookie})
	}
}

// NewClient isolates cookies for one execution and never forwards them to another host.
func NewClient(base *http.Client, cookies map[string]string) *http.Client {
	client := *base
	baseJar, _ := cookiejar.New(nil)
	jar := sessionCookieJar{CookieJar: baseJar}
	endpoint, _ := url.Parse(BaseURL)
	for _, name := range []string{CookiePSID, CookiePSIDTS} {
		if value := cookies[name]; value != "" {
			jar.SetCookies(endpoint, []*http.Cookie{{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true}})
		}
	}
	client.Jar = jar
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || req.URL.Host != endpoint.Host || len(via) >= 10 {
			return http.ErrUseLastResponse
		}
		return nil
	}
	return &client
}

// SessionCookies snapshots only the current Gemini session cookies, including
// rotations and deletions received while validating or using the session.
func SessionCookies(client *http.Client) map[string]string {
	result := make(map[string]string)
	if client == nil || client.Jar == nil {
		return result
	}
	endpoint, _ := url.Parse(BaseURL)
	for _, cookie := range client.Jar.Cookies(endpoint) {
		if cookie.Name == CookiePSID || cookie.Name == CookiePSIDTS {
			result[cookie.Name] = cookie.Value
		}
	}
	return result
}

// FetchSession obtains fresh CSRF and page routing tokens without logging page contents.
func FetchSession(ctx context.Context, client *http.Client) (Session, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BaseURL+"/app", nil)
	if err != nil {
		return Session{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return Session{}, fmt.Errorf("gemini-app: load session: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			// Transport error messages can contain session material.
			log.Errorf("gemini-app: close session response body failed (%T)", errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		status := resp.StatusCode
		if status >= 300 && status < 400 {
			status = http.StatusUnauthorized
		}
		return Session{}, sessionError{status, fmt.Sprintf("gemini-app: session page returned HTTP %d; re-import cookies if signed out", resp.StatusCode)}
	}
	page, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Session{}, fmt.Errorf("gemini-app: read session page: %w", err)
	}
	fields := make(map[string]string)
	for name, pattern := range pageFields {
		if match := pattern.FindSubmatch(page); len(match) == 2 {
			var value string
			if json.Unmarshal(match[1], &value) == nil {
				fields[name] = value
			}
		}
	}
	if fields["token"] == "" {
		return Session{}, sessionError{http.StatusUnauthorized, "gemini-app: authenticated session token missing; sign in at gemini.google.com and re-import cookies"}
	}
	return Session{Token: fields["token"], Build: fields["build"], ID: fields["id"]}, nil
}
